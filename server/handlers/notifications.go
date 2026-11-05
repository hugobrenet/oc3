package serverhandlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	neturl "net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/notify"
	"github.com/opensvc/oc3/safe"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/util/echolog"
	"github.com/opensvc/oc3/util/egress"
	utilmail "github.com/opensvc/oc3/util/mail"
)

var notifyChannelName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

const (
	notifyMaxAddresses = 50
	notifyMaxDelayMin  = 1440
)

// requireAlertsManager refuses anyone but a Manager or an AlertsManager: the
// channels decide where the alerts go, outside of the collector.
func requireAlertsManager(c echo.Context) error {
	if !IsAuthByUser(c) {
		return httpErrorf(http.StatusUnauthorized, "user authentication required")
	}
	if !IsAlertsManager(c) {
		return httpErrorf(http.StatusForbidden, "user has no AlertsManager privilege")
	}
	return nil
}

func (a *Api) safeRing() *safe.Keyring {
	if a.Safe == nil {
		return nil
	}
	return a.Safe.Ring
}

func (a *Api) notifyAudit(c echo.Context, action, format string, dict map[string]any) {
	userEmail, _ := c.Get(XUserEmail).(string)
	entry := cdb.LogEntry{Action: action, User: userEmail, Fmt: format, Dict: dict, Level: "info"}
	if err := a.ODB.Log(c.Request().Context(), entry); err != nil {
		echolog.GetLogHandler(c, action).Error("cannot write audit log", "error", err)
	}
}

func (a *Api) notifyAuthor(c echo.Context) string {
	if caller, err := a.formCaller(c.Request().Context(), c); err == nil {
		return caller.name
	}
	email, _ := c.Get(XUserEmail).(string)
	return email
}

// notifyChannelAPI is a channel as the API shows it: its secrets told set or
// not, never given.
func (a *Api) notifyChannelAPI(ch *cdb.NotifyChannel) (server.NotificationChannel, error) {
	out := server.NotificationChannel{
		Id:          ch.ID,
		Name:        ch.Name,
		Type:        server.NotificationChannelType(ch.Type),
		Description: ch.Description,
		Enabled:     ch.Enabled,
		Addresses:   nonNilStrings(ch.Settings.Addresses),
		Networks:    nonNilStrings(ch.Settings.Networks),
		UpdatedBy:   ch.UpdatedBy,
		Created:     ch.Created.Format(time.RFC3339),
		Updated:     ch.Updated.Format(time.RFC3339),
	}
	if ch.Settings.Username != "" {
		out.Username = &ch.Settings.Username
	}
	if ch.Settings.IconURL != "" {
		out.IconUrl = &ch.Settings.IconURL
	}
	secrets, err := notify.OpenSecrets(a.safeRing(), ch)
	if err != nil {
		return out, err
	}
	out.Secrets.Url = secrets[notify.SecretURL] != ""
	out.Secrets.Secret = secrets[notify.SecretSigner] != ""
	return out, nil
}

// readNotifyChannelInput reads the body of a create or a replace, and the
// change of each secret it asks: a value to set, nil to remove; absent, kept.
func readNotifyChannelInput(c echo.Context) (server.NotificationChannelInput, map[string]*string, error) {
	var in server.NotificationChannelInput
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if err != nil {
		return in, nil, httpErrorf(http.StatusBadRequest, "cannot read the body: %s", err)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, nil, httpErrorf(http.StatusBadRequest, "invalid body: %s", err)
	}
	// The generated type tells a null secret from an absent one no more: read
	// the object itself.
	var secretsOnly struct {
		Secrets map[string]*string `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &secretsOnly); err != nil {
		return in, nil, httpErrorf(http.StatusBadRequest, "invalid secrets: %s", err)
	}
	for k := range secretsOnly.Secrets {
		if k != notify.SecretURL && k != notify.SecretSigner {
			return in, nil, httpErrorf(http.StatusBadRequest, "unknown secret %q: expected url or secret", k)
		}
	}
	return in, secretsOnly.Secrets, nil
}

// buildNotifyChannel checks a channel as given, its secrets being the current
// ones with the changes asked, and returns the row to write and the secrets to
// seal: only those its type uses are kept.
func buildNotifyChannel(in server.NotificationChannelInput, current map[string]string, changes map[string]*string) (cdb.NotifyChannel, map[string]string, error) {
	bad := func(format string, args ...any) (cdb.NotifyChannel, map[string]string, error) {
		return cdb.NotifyChannel{}, nil, httpErrorf(http.StatusBadRequest, format, args...)
	}
	ch := cdb.NotifyChannel{Name: strings.TrimSpace(in.Name), Type: string(in.Type), Enabled: true}
	if !notifyChannelName.MatchString(ch.Name) {
		return bad("name: 1 to 64 letters, digits, \".\", \"_\" or \"-\" expected")
	}
	if !slices.Contains(notify.ChannelTypes, ch.Type) {
		return bad("type: %q is none of %s", in.Type, strings.Join(notify.ChannelTypes, ", "))
	}
	if in.Description != nil {
		ch.Description = strings.TrimSpace(*in.Description)
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}

	secrets := map[string]string{}
	for k, v := range current {
		secrets[k] = v
	}
	for k, v := range changes {
		if v == nil || strings.TrimSpace(*v) == "" {
			delete(secrets, k)
		} else {
			secrets[k] = strings.TrimSpace(*v)
		}
	}
	kept := map[string]string{}

	if ch.Type == notify.ChannelEmail {
		if in.Addresses == nil || len(*in.Addresses) == 0 {
			return bad("addresses: an email channel needs one address at least")
		}
		if len(*in.Addresses) > notifyMaxAddresses {
			return bad("addresses: %d at most", notifyMaxAddresses)
		}
		for _, s := range *in.Addresses {
			s = strings.TrimSpace(s)
			addr, err := mail.ParseAddress(s)
			if err != nil || addr.Address != s {
				return bad("addresses: %q is not a mail address", s)
			}
			if !slices.Contains(ch.Settings.Addresses, s) {
				ch.Settings.Addresses = append(ch.Settings.Addresses, s)
			}
		}
		return ch, kept, nil
	}

	// The webhook types.
	if in.Networks != nil {
		if _, err := egress.ParsePrefixes(*in.Networks); err != nil {
			return bad("networks: %s", err)
		}
		ch.Settings.Networks = *in.Networks
	}
	raw := secrets[notify.SecretURL]
	if raw == "" {
		return bad("secrets.url: the webhook url is required by the type %s", ch.Type)
	}
	u, err := neturl.Parse(raw)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return bad("secrets.url: an absolute https url expected")
	case u.Scheme == "http" && len(ch.Settings.Networks) == 0:
		// The url is the credential: in clear only to an endpoint declared
		// on a private network.
		return bad("secrets.url: https is required, but toward a host of the networks listed")
	case u.User != nil || u.Fragment != "":
		return bad("secrets.url: no credentials nor fragment")
	}
	kept[notify.SecretURL] = raw
	if ch.Type == notify.ChannelWebhook && secrets[notify.SecretSigner] != "" {
		kept[notify.SecretSigner] = secrets[notify.SecretSigner]
	}
	if ch.Type == notify.ChannelSlack || ch.Type == notify.ChannelMattermost {
		if in.Username != nil {
			ch.Settings.Username = strings.TrimSpace(*in.Username)
		}
		if in.IconUrl != nil && strings.TrimSpace(*in.IconUrl) != "" {
			icon, err := neturl.Parse(strings.TrimSpace(*in.IconUrl))
			if err != nil || icon.Scheme != "https" || icon.Host == "" {
				return bad("icon_url: an absolute https url expected")
			}
			ch.Settings.IconURL = icon.String()
		}
	}
	return ch, kept, nil
}

// GetNotificationChannels handles GET /notifications/channels.
func (a *Api) GetNotificationChannels(c echo.Context) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	log := echolog.GetLogHandler(c, "GetNotificationChannels")
	channels, err := a.ODB.NotifyChannels(c.Request().Context())
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channels", err))
	}
	data := make([]server.NotificationChannel, 0, len(channels))
	for i := range channels {
		item, err := a.notifyChannelAPI(&channels[i])
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot read the channel "+channels[i].Name, err))
		}
		data = append(data, item)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": data})
}

// GetNotificationChannel handles GET /notifications/channels/{channel_id}.
func (a *Api) GetNotificationChannel(c echo.Context, channelId server.InPathNotificationChannelId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.notifyChannelResponse(c, channelId)
}

func (a *Api) notifyChannelResponse(c echo.Context, id int64) error {
	log := echolog.GetLogHandler(c, "GetNotificationChannel")
	ch, err := a.ODB.NotifyChannelByID(c.Request().Context(), id)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channel", err))
	}
	if ch == nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "channel %d not found", id))
	}
	out, err := a.notifyChannelAPI(ch)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channel", err))
	}
	return c.JSON(http.StatusOK, out)
}

// PostNotificationChannels handles POST /notifications/channels.
func (a *Api) PostNotificationChannels(c echo.Context) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.saveNotifyChannel(c, 0)
}

// PutNotificationChannel handles PUT /notifications/channels/{channel_id}.
func (a *Api) PutNotificationChannel(c echo.Context, channelId server.InPathNotificationChannelId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.saveNotifyChannel(c, channelId)
}

// saveNotifyChannel creates the channel given (id 0) or replaces one, its
// secrets sealed under the label of its id, in one transaction.
func (a *Api) saveNotifyChannel(c echo.Context, id int64) error {
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "PutNotificationChannel")
	in, changes, err := readNotifyChannelInput(c)
	if err != nil {
		return httpProblem(c, err)
	}
	current := map[string]string{}
	if id != 0 {
		existing, err := a.ODB.NotifyChannelByID(ctx, id)
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot read the channel", err))
		}
		if existing == nil {
			return httpProblem(c, httpErrorf(http.StatusNotFound, "channel %d not found", id))
		}
		if current, err = notify.OpenSecrets(a.safeRing(), existing); err != nil {
			return httpProblem(c, httpInternal(log, "cannot open the channel secrets", err))
		}
	}
	ch, secrets, err := buildNotifyChannel(in, current, changes)
	if err != nil {
		return httpProblem(c, err)
	}
	if len(secrets) > 0 && (a.safeRing() == nil || a.safeRing().Active() == nil) {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "%s", notify.ErrNoKey))
	}
	if taken, err := a.ODB.NotifyChannelNameTaken(ctx, ch.Name, id); err != nil {
		return httpProblem(c, httpInternal(log, "cannot check the channel name", err))
	} else if taken {
		return httpProblem(c, httpErrorf(http.StatusConflict, "a channel named %s already exists", ch.Name))
	}
	ch.UpdatedBy = a.notifyAuthor(c)

	odb, _, _, err := a.ODB.BeginTxWithControl(ctx, log, &sql.TxOptions{})
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot start transaction", err))
	}
	defer func() { _ = odb.Rollback() }()
	if id == 0 {
		if id, err = odb.InsertNotifyChannel(ctx, ch); err != nil {
			return httpProblem(c, httpInternal(log, "cannot create the channel", err))
		}
	}
	ch.ID = id
	if ch.Secrets, err = notify.SealSecrets(a.safeRing(), id, secrets); err != nil {
		return httpProblem(c, httpInternal(log, "cannot seal the channel secrets", err))
	}
	if _, err := odb.UpdateNotifyChannel(ctx, ch); err != nil {
		return httpProblem(c, httpInternal(log, "cannot save the channel", err))
	}
	if err := odb.Commit(); err != nil {
		return httpProblem(c, httpInternal(log, "cannot save the channel", err))
	}
	changed := make([]string, 0, len(changes))
	for k := range changes {
		changed = append(changed, k)
	}
	slices.Sort(changed)
	a.notifyAudit(c, "notification.channel.change", "saved notification channel %(name)s (%(type)s, enabled %(enabled)s, secrets changed: %(secrets)s)",
		map[string]any{"name": ch.Name, "type": ch.Type, "enabled": strconv.FormatBool(ch.Enabled), "secrets": strings.Join(changed, ", ")})
	return a.notifyChannelResponse(c, id)
}

// DeleteNotificationChannel handles DELETE /notifications/channels/{channel_id}.
func (a *Api) DeleteNotificationChannel(c echo.Context, channelId server.InPathNotificationChannelId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "DeleteNotificationChannel")
	ch, err := a.ODB.NotifyChannelByID(ctx, channelId)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channel", err))
	}
	if ch == nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "channel %d not found", channelId))
	}
	if _, err := a.ODB.DeleteNotifyChannel(ctx, channelId); err != nil {
		return httpProblem(c, httpInternal(log, "cannot delete the channel", err))
	}
	a.notifyAudit(c, "notification.channel.delete", "deleted notification channel %(name)s", map[string]any{"name": ch.Name})
	return c.NoContent(http.StatusNoContent)
}

// notifySender returns the sender of a channel, as the scheduler builds it.
func notifySender(ch *cdb.NotifyChannel) (notify.Sender, error) {
	cfg := utilmail.ConfigFromViper("notifications.mail")
	if !cfg.IsSet() {
		cfg = utilmail.ConfigFromViper("server.mail")
	}
	s := notify.Sender{Mail: cfg}
	if !notify.IsWebhook(ch.Type) {
		return s, nil
	}
	allowed, err := egress.ParsePrefixes(ch.Settings.Networks)
	if err != nil {
		return s, err
	}
	timeout := viper.GetDuration("notifications.http_timeout")
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	s.Client = egress.NewClient(egress.Options{Policy: egress.Policy{Allowed: allowed}, Timeout: timeout})
	return s, nil
}

// PostNotificationChannelTest handles POST /notifications/channels/{channel_id}/test.
func (a *Api) PostNotificationChannelTest(c echo.Context, channelId server.InPathNotificationChannelId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "PostNotificationChannelTest")
	ch, err := a.ODB.NotifyChannelByID(ctx, channelId)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channel", err))
	}
	if ch == nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "channel %d not found", channelId))
	}
	author := a.notifyAuthor(c)
	digest := notify.Digest{
		Title:     viper.GetString("notifications.title"),
		PublicURL: viper.GetString("notifications.public_url"),
		Alerts: []cdb.NotifyAlert{{
			Severity: 0,
			Type:     "test",
			Fmt:      "Test message sent by %(author)s",
			Dict:     mustJSON(map[string]string{"author": author}),
			Created:  time.Now(),
		}},
		Reason: "A test of the channel " + ch.Name + ".",
	}
	started := time.Now()
	dest, err := notify.DestinationOf(a.safeRing(), ch)
	if err == nil {
		var sender notify.Sender
		if sender, err = notifySender(ch); err == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = sender.Send(sendCtx, dest, digest)
			cancel()
		}
	}
	out := server.NotificationChannelTestResult{Ok: err == nil, DurationMs: int(time.Since(started).Milliseconds())}
	if err != nil {
		text := err.Error()
		out.Error = &text
	}
	a.notifyAudit(c, "notification.channel.test", "tested notification channel %(name)s: %(result)s",
		map[string]any{"name": ch.Name, "result": map[bool]string{true: "ok", false: "failed"}[err == nil]})
	return c.JSON(http.StatusOK, out)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func notifySubscriptionAPI(s *cdb.NotifySubscription) server.NotificationSubscription {
	out := server.NotificationSubscription{
		Id:          s.ID,
		ChannelId:   s.ChannelID,
		ChannelName: s.ChannelName,
		GroupId:     s.GroupID,
		GroupRole:   s.GroupRole,
		Scope:       server.NotificationSubscriptionScope(s.Scope),
		MinSeverity: s.MinSeverity,
		DelayMin:    s.DelayMin,
		Enabled:     s.Enabled,
		UpdatedBy:   s.UpdatedBy,
		Created:     s.Created.Format(time.RFC3339),
		Updated:     s.Updated.Format(time.RFC3339),
	}
	out.TypesInclude = nonNilStrings(s.TypesInclude)
	out.TypesExclude = nonNilStrings(s.TypesExclude)
	out.Envs = nonNilStrings(s.Envs)
	if s.FiltersetID != 0 {
		id := s.FiltersetID
		out.FiltersetId = &id
	}
	out.Periods = map[string]server.NotificationPeriod{}
	if s.Periods != "" {
		_ = json.Unmarshal([]byte(s.Periods), &out.Periods)
	}
	out.NotifyResolved = s.NotifyResolved
	return out
}

// GetNotificationSubscriptions handles GET /notifications/subscriptions.
func (a *Api) GetNotificationSubscriptions(c echo.Context, params server.GetNotificationSubscriptionsParams) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	var channelID int64
	if params.ChannelId != nil {
		channelID = *params.ChannelId
	}
	subs, err := a.ODB.NotifySubscriptions(c.Request().Context(), channelID)
	if err != nil {
		return httpProblem(c, httpInternal(echolog.GetLogHandler(c, "GetNotificationSubscriptions"), "cannot read the subscriptions", err))
	}
	data := make([]server.NotificationSubscription, 0, len(subs))
	for i := range subs {
		data = append(data, notifySubscriptionAPI(&subs[i]))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": data})
}

// buildNotifySubscription checks a subscription as given.
func buildNotifySubscription(in server.NotificationSubscriptionInput) (cdb.NotifySubscription, error) {
	s := cdb.NotifySubscription{
		ChannelID:   in.ChannelId,
		GroupID:     in.GroupId,
		Scope:       string(server.NotificationScopeResponsible),
		MinSeverity: 1,
		Enabled:     true,
	}
	if in.Scope != nil {
		if !in.Scope.Valid() {
			return s, httpErrorf(http.StatusBadRequest, "scope: responsible or all")
		}
		s.Scope = string(*in.Scope)
	}
	if in.MinSeverity != nil {
		if *in.MinSeverity < 0 || *in.MinSeverity > 4 {
			return s, httpErrorf(http.StatusBadRequest, "min_severity: 0 (notice) to 4 (alert)")
		}
		s.MinSeverity = *in.MinSeverity
	}
	if in.DelayMin != nil {
		if *in.DelayMin < 0 || *in.DelayMin > notifyMaxDelayMin {
			return s, httpErrorf(http.StatusBadRequest, "delay_min: 0 to %d minutes", notifyMaxDelayMin)
		}
		s.DelayMin = *in.DelayMin
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	types := func(field string, l *[]string) ([]string, error) {
		if l == nil {
			return nil, nil
		}
		var out []string
		for _, t := range *l {
			t = strings.TrimSpace(t)
			if t == "" || len(t) > 60 {
				return nil, httpErrorf(http.StatusBadRequest, "%s: alert types of 1 to 60 characters expected", field)
			}
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
		return out, nil
	}
	var err error
	if s.TypesInclude, err = types("types_include", in.TypesInclude); err != nil {
		return s, err
	}
	if s.TypesExclude, err = types("types_exclude", in.TypesExclude); err != nil {
		return s, err
	}
	if in.Envs != nil {
		for _, e := range *in.Envs {
			e = strings.ToUpper(strings.TrimSpace(e))
			if !slices.Contains(notifyEnvs, e) {
				return s, httpErrorf(http.StatusBadRequest, "envs: %q is none of %s", e, strings.Join(notifyEnvs, ", "))
			}
			if !slices.Contains(s.Envs, e) {
				s.Envs = append(s.Envs, e)
			}
		}
	}
	if in.FiltersetId != nil {
		if *in.FiltersetId < 0 {
			return s, httpErrorf(http.StatusBadRequest, "filterset_id: a filterset id expected")
		}
		s.FiltersetID = *in.FiltersetId
	}
	if in.Periods != nil && len(*in.Periods) > 0 {
		b, err := json.Marshal(*in.Periods)
		if err != nil {
			return s, httpErrorf(http.StatusBadRequest, "periods: %s", err)
		}
		if err := notify.ValidatePeriods(string(b)); err != nil {
			return s, httpErrorf(http.StatusBadRequest, "periods: %s", err)
		}
		s.Periods = string(b)
	}
	if in.NotifyResolved != nil {
		s.NotifyResolved = *in.NotifyResolved
	}
	return s, nil
}

// notifyEnvs are the environments of the alerts (dashboard.dash_env).
var notifyEnvs = []string{"PRD", "PPRD", "REC", "INT", "DEV", "TST", "TMP", "DRP", "FOR", "PRA", "PRJ"}

// PostNotificationSubscriptions handles POST /notifications/subscriptions.
func (a *Api) PostNotificationSubscriptions(c echo.Context) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.saveNotifySubscription(c, 0)
}

// PutNotificationSubscription handles PUT /notifications/subscriptions/{subscription_id}.
func (a *Api) PutNotificationSubscription(c echo.Context, subscriptionId server.InPathNotificationSubscriptionId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	return a.saveNotifySubscription(c, subscriptionId)
}

func (a *Api) saveNotifySubscription(c echo.Context, id int64) error {
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "PutNotificationSubscription")
	var in server.NotificationSubscriptionInput
	if err := c.Bind(&in); err != nil {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "invalid body: %s", err))
	}
	s, err := buildNotifySubscription(in)
	if err != nil {
		return httpProblem(c, err)
	}
	if id != 0 {
		existing, err := a.ODB.NotifySubscriptionByID(ctx, id)
		if err != nil {
			return httpProblem(c, httpInternal(log, "cannot read the subscription", err))
		}
		if existing == nil {
			return httpProblem(c, httpErrorf(http.StatusNotFound, "subscription %d not found", id))
		}
	}
	if ch, err := a.ODB.NotifyChannelByID(ctx, s.ChannelID); err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the channel", err))
	} else if ch == nil {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "channel_id: channel %d not found", s.ChannelID))
	}
	if _, found, err := a.ODB.AuthGroupByIDOrRole(ctx, strconv.FormatInt(s.GroupID, 10)); err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the group", err))
	} else if !found {
		return httpProblem(c, httpErrorf(http.StatusBadRequest, "group_id: group %d not found", s.GroupID))
	}
	if s.FiltersetID != 0 {
		if fsetID, _, err := a.ODB.FiltersetByIDOrName(ctx, strconv.Itoa(s.FiltersetID)); err != nil {
			return httpProblem(c, httpInternal(log, "cannot read the filterset", err))
		} else if fsetID == 0 {
			return httpProblem(c, httpErrorf(http.StatusBadRequest, "filterset_id: filterset %d not found", s.FiltersetID))
		}
	}
	if taken, err := a.ODB.NotifySubscriptionTaken(ctx, s.ChannelID, s.GroupID, id); err != nil {
		return httpProblem(c, httpInternal(log, "cannot check the subscription", err))
	} else if taken {
		return httpProblem(c, httpErrorf(http.StatusConflict, "the group already has a subscription to the channel"))
	}
	s.UpdatedBy = a.notifyAuthor(c)
	if id == 0 {
		if id, err = a.ODB.InsertNotifySubscription(ctx, s); err != nil {
			return httpProblem(c, httpInternal(log, "cannot create the subscription", err))
		}
	} else {
		s.ID = id
		if _, err := a.ODB.UpdateNotifySubscription(ctx, s); err != nil {
			return httpProblem(c, httpInternal(log, "cannot save the subscription", err))
		}
	}
	saved, err := a.ODB.NotifySubscriptionByID(ctx, id)
	if err != nil || saved == nil {
		return httpProblem(c, httpInternal(log, "cannot read the subscription", errors.Join(err, errors.New("not found"))))
	}
	a.notifyAudit(c, "notification.subscription.change",
		"saved notification subscription of %(group)s to %(channel)s (scope %(scope)s, from severity %(severity)s, delay %(delay)s min, enabled %(enabled)s, filters: %(filters)s)",
		map[string]any{"group": saved.GroupRole, "channel": saved.ChannelName, "scope": saved.Scope,
			"filters":  strings.Join(subscriptionFilters(saved), ", "),
			"severity": notify.SeverityName(saved.MinSeverity), "delay": strconv.Itoa(saved.DelayMin), "enabled": strconv.FormatBool(saved.Enabled)})
	return c.JSON(http.StatusOK, notifySubscriptionAPI(saved))
}

// DeleteNotificationSubscription handles DELETE /notifications/subscriptions/{subscription_id}.
func (a *Api) DeleteNotificationSubscription(c echo.Context, subscriptionId server.InPathNotificationSubscriptionId) error {
	if err := requireAlertsManager(c); err != nil {
		return httpProblem(c, err)
	}
	ctx := c.Request().Context()
	log := echolog.GetLogHandler(c, "DeleteNotificationSubscription")
	s, err := a.ODB.NotifySubscriptionByID(ctx, subscriptionId)
	if err != nil {
		return httpProblem(c, httpInternal(log, "cannot read the subscription", err))
	}
	if s == nil {
		return httpProblem(c, httpErrorf(http.StatusNotFound, "subscription %d not found", subscriptionId))
	}
	if _, err := a.ODB.DeleteNotifySubscription(ctx, subscriptionId); err != nil {
		return httpProblem(c, httpInternal(log, "cannot delete the subscription", err))
	}
	a.notifyAudit(c, "notification.subscription.delete", "deleted notification subscription of %(group)s to %(channel)s",
		map[string]any{"group": s.GroupRole, "channel": s.ChannelName})
	return c.NoContent(http.StatusNoContent)
}

// subscriptionFilters describes the filters of a subscription, for the audit.
func subscriptionFilters(s *cdb.NotifySubscription) []string {
	var l []string
	if len(s.TypesInclude) > 0 {
		l = append(l, "types "+strings.Join(s.TypesInclude, "|"))
	}
	if len(s.TypesExclude) > 0 {
		l = append(l, "not types "+strings.Join(s.TypesExclude, "|"))
	}
	if len(s.Envs) > 0 {
		l = append(l, "envs "+strings.Join(s.Envs, "|"))
	}
	if s.FiltersetID != 0 {
		l = append(l, "filterset "+strconv.Itoa(s.FiltersetID))
	}
	if s.Periods != "" {
		l = append(l, "periods")
	}
	if s.NotifyResolved {
		l = append(l, "resolved")
	}
	if len(l) == 0 {
		l = append(l, "none")
	}
	return l
}
