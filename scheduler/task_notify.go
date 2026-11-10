package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/notify"
	"github.com/opensvc/oc3/safe"
	"github.com/opensvc/oc3/util/egress"
	"github.com/opensvc/oc3/util/mail"
)

var (
	TaskNotify = Task{
		name:    "notify",
		desc:    "send the new alerts of the dashboard to the users asking for them by mail, and to the channels the groups subscribed to (notifications.enable)",
		period:  time.Minute,
		fn:      taskNotify,
		timeout: 5 * time.Minute,
	}

	TaskNotifyJanitor = Task{
		name:    "notify_janitor",
		desc:    "end the expired snoozes of the nodes and services, and forget the notifications of the alerts, users and subscriptions gone",
		period:  time.Minute,
		fn:      taskNotifyJanitor,
		timeout: time.Minute,
	}
)

const (
	// notifyFetchLimit bounds the alerts considered for a recipient per run;
	// the next ones wait for the next run.
	notifyFetchLimit = 1000
	// notifyStalePending is the age after which an alert left pending, by a
	// scheduler stopped while it sent it, is sent again.
	notifyStalePending = 10 * time.Minute
)

// notifyConfig is the configuration of the notifications, under
// notifications in the configuration file.
type notifyConfig struct {
	title       string
	publicURL   string
	maxListed   int
	maxAttempts int
	workers     int
	httpTimeout time.Duration
	// mail is the SMTP server, unset when the notifications send no mail.
	mail mail.Config
	// ring holds the safe master keys opening the secrets of the channels,
	// nil without safe.keys_file.
	ring *safe.Keyring
}

func readNotifyConfig() (notifyConfig, error) {
	c := notifyConfig{
		title:       viper.GetString("notifications.title"),
		publicURL:   viper.GetString("notifications.public_url"),
		maxListed:   viper.GetInt("notifications.max_alerts_per_message"),
		maxAttempts: viper.GetInt("notifications.max_attempts"),
		workers:     viper.GetInt("notifications.workers"),
		httpTimeout: viper.GetDuration("notifications.http_timeout"),
		mail:        mail.ConfigFromViper("notifications.mail"),
	}
	if !c.mail.IsSet() {
		// The SMTP server of the forms, when the notifications have none of
		// their own.
		if fallback := mail.ConfigFromViper("server.mail"); fallback.IsSet() {
			c.mail = fallback
		}
	}
	c.maxListed = max(c.maxListed, 1)
	c.maxAttempts = max(c.maxAttempts, 1)
	c.workers = max(c.workers, 1)
	if c.httpTimeout <= 0 {
		c.httpTimeout = 10 * time.Second
	}
	if c.mail.IsSet() {
		if err := c.mail.Validate(); err != nil {
			return c, fmt.Errorf("notifications.mail: %w", err)
		}
	}
	if path := viper.GetString("safe.keys_file"); path != "" {
		ring, err := safe.LoadKeyring(path)
		if err != nil {
			return c, err
		}
		c.ring = ring
	}
	return c, nil
}

// notifyJob is a recipient to serve: it returns the number of alerts it
// announced, 0 when it had none to.
type notifyJob struct {
	name string
	run  func(context.Context) (int, error)
}

func taskNotify(ctx context.Context, task *Task) error {
	if !viper.GetBool("notifications.enable") {
		return nil
	}
	cfg, err := readNotifyConfig()
	if err != nil {
		return err
	}
	odb := task.DB()
	var jobs []notifyJob

	if cfg.mail.IsSet() {
		users, err := odb.NotifyEmailUsers(ctx)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, u := range users {
			if !notify.InPeriod(u.Prefs, now) {
				continue
			}
			jobs = append(jobs, notifyJob{
				name: "user " + strconv.FormatInt(u.ID, 10),
				run:  func(ctx context.Context) (int, error) { return notifyEmailUser(ctx, odb, cfg, u) },
			})
		}
	}

	subs, channels, err := odb.NotifyActiveSubscriptions(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		channel := channels[sub.ChannelID]
		if channel == nil {
			continue
		}
		jobs = append(jobs, notifyJob{
			name: "subscription " + strconv.FormatInt(sub.ID, 10) + " (" + channel.Name + ", " + sub.GroupRole + ")",
			run:  func(ctx context.Context) (int, error) { return notifySubscription(ctx, odb, cfg, sub, channel) },
		})
	}

	var (
		wg             sync.WaitGroup
		mu             sync.Mutex
		errs           []error
		sent, messages int
		queue          = make(chan notifyJob)
	)
	for range cfg.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				n, err := job.run(ctx)
				mu.Lock()
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", job.name, err))
				}
				if n > 0 {
					sent += n
					messages++
				}
				mu.Unlock()
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	if messages > 0 {
		task.Infof("%d alerts notified in %d messages", sent, messages)
	}
	return errors.Join(errs...)
}

// deliver sends the alerts found for a recipient, in one message detailing
// the most severe, recording them pending before and sent or failed after.
func deliver(ctx context.Context, odb *cdb.DB, cfg notifyConfig, recipient string, alerts []cdb.NotifyAlert, reason string,
	send func(context.Context, notify.Digest) error) (int, error) {
	if len(alerts) == 0 {
		return 0, nil
	}
	ids := make([]int64, len(alerts))
	items := make([]cdb.NotifyClaimItem, len(alerts))
	for i, a := range alerts {
		ids[i] = a.ID
		items[i] = notify.ClaimItem(a)
	}
	// Claimed before the sending: a scheduler stopping in the middle does not
	// send them again at once.
	if err := odb.NotifyClaim(ctx, recipient, items); err != nil {
		return 0, err
	}
	digest := notify.Digest{
		Title:     cfg.title,
		PublicURL: cfg.publicURL,
		Alerts:    alerts[:min(len(alerts), cfg.maxListed)],
		Total:     len(alerts),
		Reason:    reason,
	}
	sendErr := send(ctx, digest)
	// Recorded even when the run is over: a context of its own.
	doneCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := odb.NotifyDone(doneCtx, recipient, ids, sendErr, notify.RetryAfter); err != nil {
		return 0, errors.Join(sendErr, err)
	}
	if sendErr != nil {
		return 0, sendErr
	}
	return len(alerts), nil
}

// notifyEmailUser sends the new alerts of u to their mail address.
func notifyEmailUser(ctx context.Context, odb *cdb.DB, cfg notifyConfig, u cdb.NotifyUser) (int, error) {
	recipient := "email:user:" + strconv.FormatInt(u.ID, 10)
	alerts, err := odb.NotifyAlertsForUser(ctx, u.ID, recipient, notify.MinSeverity(u.LogLevel), u.Delay,
		cfg.maxAttempts, notifyStalePending, notifyFetchLimit)
	if err != nil {
		return 0, err
	}
	return deliver(ctx, odb, cfg, recipient, alerts, "", func(ctx context.Context, d notify.Digest) error {
		return mail.Send(ctx, cfg.mail, d.Mail(u.Email))
	})
}

// notifySubscription sends the new alerts of the group of sub to its channel,
// and, when sub asks for it, the resolution of the alerts it sent, on the
// periods of sub.
func notifySubscription(ctx context.Context, odb *cdb.DB, cfg notifyConfig, sub cdb.NotifySubscription, channel *cdb.NotifyChannel) (int, error) {
	if !notify.InPeriods(sub.Periods, time.Now()) {
		return 0, nil
	}
	recipient := "subscription:" + strconv.FormatInt(sub.ID, 10)
	filter := cdb.NotifyFilter{
		TypesInclude: sub.TypesInclude,
		TypesExclude: sub.TypesExclude,
		Envs:         sub.Envs,
		FiltersetID:  sub.FiltersetID,
	}
	alerts, err := odb.NotifyAlertsForGroup(ctx, sub.GroupID, sub.Scope == "all", filter, recipient, sub.MinSeverity,
		time.Duration(sub.DelayMin)*time.Minute, cfg.maxAttempts, notifyStalePending, notifyFetchLimit)
	if err != nil {
		return 0, err
	}
	var resolved []cdb.NotifyResolvedAlert
	if sub.NotifyResolved {
		if resolved, err = odb.NotifyResolved(ctx, recipient, notifyFetchLimit); err != nil {
			return 0, err
		}
	}
	if len(alerts) == 0 && len(resolved) == 0 {
		return 0, nil
	}
	dest, destErr := notify.DestinationOf(cfg.ring, channel)
	sender, senderErr := channelSender(cfg, channel)
	reason := fmt.Sprintf("Sent to the channel %s, subscribed for the group %s.", channel.Name, sub.GroupRole)
	send := func(ctx context.Context, d notify.Digest) error {
		// A channel whose secrets do not open fails as a sending does:
		// recorded, tried again, seen in the deliveries.
		if err := errors.Join(destErr, senderErr); err != nil {
			return err
		}
		return sender.Send(ctx, dest, d)
	}
	n, err := deliver(ctx, odb, cfg, recipient, alerts, reason, send)
	if err != nil || len(resolved) == 0 {
		return n, err
	}
	// The resolutions: one message, then the deliveries forgotten. A sending
	// failing leaves them to the next run, two days at most (NotifyPurge).
	gone := make([]cdb.NotifyAlert, len(resolved))
	ids := make([]int64, len(resolved))
	for i, r := range resolved {
		gone[i] = cdb.NotifyAlert{ID: r.ID, Severity: r.Severity, Type: r.Type, Object: r.Object, Message: r.Message}
		ids[i] = r.ID
	}
	digest := notify.Digest{
		Title:     cfg.title,
		PublicURL: cfg.publicURL,
		Alerts:    gone[:min(len(gone), cfg.maxListed)],
		Total:     len(gone),
		Resolved:  true,
		Reason:    reason,
	}
	if err := send(ctx, digest); err != nil {
		return n, fmt.Errorf("resolutions: %w", err)
	}
	if err := odb.NotifyForget(ctx, recipient, ids); err != nil {
		return n, err
	}
	return n + len(resolved), nil
}

// channelSender returns the sender of a channel: its webhooks connect only to
// the public addresses and to the private networks the channel allows.
func channelSender(cfg notifyConfig, channel *cdb.NotifyChannel) (notify.Sender, error) {
	s := notify.Sender{Mail: cfg.mail}
	if !notify.IsWebhook(channel.Type) {
		return s, nil
	}
	allowed, err := egress.ParsePrefixes(channel.Settings.Networks)
	if err != nil {
		return s, fmt.Errorf("networks: %w", err)
	}
	s.Client = egress.NewClient(egress.Options{Policy: egress.Policy{Allowed: allowed}, Timeout: cfg.httpTimeout})
	return s, nil
}

func taskNotifyJanitor(ctx context.Context, task *Task) error {
	odb := task.DB()
	var errs []error
	if n, err := odb.ExpireSnoozes(ctx); err != nil {
		errs = append(errs, err)
	} else if n > 0 {
		task.Infof("%d snoozes expired", n)
	}
	if _, err := odb.NotifyPurge(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
