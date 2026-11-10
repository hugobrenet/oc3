package serverhandlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/shaj13/go-guardian/v2/auth"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/notify"
	"github.com/opensvc/oc3/server"
	"github.com/opensvc/oc3/xauth"
)

// basicUserContext is a request of a user signed in with a password, member of
// the groups.
func basicUserContext(t *testing.T, groups []string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	ext := make(auth.Extensions)
	ext.Set(xauth.XAuthSource, xauth.AuthSourceBasic)
	ext.Set(xauth.XUserID, "1")
	c.Set("user", auth.NewUserInfo("m@example.com", "1", nil, ext))
	c.Set(XAuthMode, AuthModeUser)
	c.Set("groups", groups)
	return c, rec
}

func TestBuildNotifyChannel(t *testing.T) {
	url := "https://hooks.slack.com/services/T/B/x"
	for _, tc := range []struct {
		name    string
		in      server.NotificationChannelInput
		current map[string]string
		changes map[string]*string
		err     string
		check   func(t *testing.T, secrets map[string]string)
	}{
		{name: "bad name", in: server.NotificationChannelInput{Name: "a b", Type: "slack"}, err: "name:"},
		{name: "bad type", in: server.NotificationChannelInput{Name: "a", Type: "sms"}, err: "type:"},
		{name: "email without address", in: server.NotificationChannelInput{Name: "a", Type: "email"}, err: "addresses:"},
		{name: "email bad address", in: server.NotificationChannelInput{Name: "a", Type: "email", Addresses: &[]string{"Bob <b@x.org>"}}, err: "addresses:"},
		{name: "email", in: server.NotificationChannelInput{Name: "ops", Type: "email", Addresses: &[]string{"a@x.org", "a@x.org", "b@x.org"}},
			changes: map[string]*string{"url": &url},
			check: func(t *testing.T, secrets map[string]string) {
				if len(secrets) != 0 {
					t.Errorf("an email channel keeps no secret: %v", secrets)
				}
			}},
		{name: "webhook without url", in: server.NotificationChannelInput{Name: "a", Type: "slack"}, err: "secrets.url"},
		{name: "http url", in: server.NotificationChannelInput{Name: "a", Type: "mattermost"},
			changes: map[string]*string{"url": ptr("http://mm.example.com/hooks/x")}, err: "https is required"},
		{name: "http url on a private network", in: server.NotificationChannelInput{Name: "a", Type: "mattermost", Networks: &[]string{"10.0.0.0/8"}},
			changes: map[string]*string{"url": ptr("http://mm.internal/hooks/x")}},
		{name: "bad network", in: server.NotificationChannelInput{Name: "a", Type: "teams", Networks: &[]string{"nowhere"}},
			changes: map[string]*string{"url": &url}, err: "networks:"},
		{name: "url with credentials", in: server.NotificationChannelInput{Name: "a", Type: "webhook"},
			changes: map[string]*string{"url": ptr("https://u:p@x.org/h")}, err: "credentials"},
		{name: "kept url, removed secret", in: server.NotificationChannelInput{Name: "a", Type: "webhook"},
			current: map[string]string{"url": url, "secret": "s"}, changes: map[string]*string{"secret": nil},
			check: func(t *testing.T, secrets map[string]string) {
				if secrets["url"] != url || secrets["secret"] != "" {
					t.Errorf("secrets %v", secrets)
				}
			}},
		{name: "signing secret only for webhook", in: server.NotificationChannelInput{Name: "a", Type: "slack"},
			changes: map[string]*string{"url": &url, "secret": ptr("s")},
			check: func(t *testing.T, secrets map[string]string) {
				if _, ok := secrets["secret"]; ok {
					t.Errorf("secrets %v", secrets)
				}
			}},
		{name: "bad icon", in: server.NotificationChannelInput{Name: "a", Type: "slack", IconUrl: ptr("http://x.org/i.png")},
			changes: map[string]*string{"url": &url}, err: "icon_url"},
	} {
		ch, secrets, err := buildNotifyChannel(tc.in, tc.current, tc.changes)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.err)
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err == "" && tc.check != nil:
			tc.check(t, secrets)
		}
		if tc.name == "email" && err == nil && len(ch.Settings.Addresses) != 2 {
			t.Errorf("email: addresses %v", ch.Settings.Addresses)
		}
	}
}

func TestBuildNotifySubscription(t *testing.T) {
	s, err := buildNotifySubscription(server.NotificationSubscriptionInput{ChannelId: 1, GroupId: 2})
	if err != nil || s.Scope != "responsible" || s.MinSeverity != 1 || s.DelayMin != 0 || !s.Enabled {
		t.Errorf("defaults %+v %v", s, err)
	}
	all := server.NotificationScopeAll
	if s, err := buildNotifySubscription(server.NotificationSubscriptionInput{Scope: &all, MinSeverity: ptr(3), DelayMin: ptr(30), Enabled: ptr(false)}); err != nil ||
		s.Scope != "all" || s.MinSeverity != 3 || s.DelayMin != 30 || s.Enabled {
		t.Errorf("given %+v %v", s, err)
	}
	for _, in := range []server.NotificationSubscriptionInput{
		{Scope: ptr(server.NotificationSubscriptionScope("some"))},
		{MinSeverity: ptr(5)},
		{DelayMin: ptr(-1)},
		{DelayMin: ptr(1441)},
	} {
		if _, err := buildNotifySubscription(in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
}

func TestNotificationsRequireAlertsManager(t *testing.T) {
	a := &Api{}
	for _, tc := range []struct {
		groups []string
		code   int
	}{
		{[]string{"Everybody"}, http.StatusForbidden},
	} {
		c, rec := basicUserContext(t, tc.groups)
		err := a.GetNotificationChannels(c)
		if err != nil && !errors.Is(err, errRequestDenied) {
			t.Fatal(err)
		}
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), "AlertsManager") {
			t.Errorf("%v: got %d %s", tc.groups, rec.Code, rec.Body.String())
		}
	}
}

func TestBuildNotifySubscriptionFilters(t *testing.T) {
	s, err := buildNotifySubscription(server.NotificationSubscriptionInput{
		TypesInclude:   &[]string{"service unavailable", " service unavailable "},
		TypesExclude:   &[]string{"node not updated"},
		Envs:           &[]string{"prd", "DEV"},
		FiltersetId:    ptr(7),
		NotifyResolved: ptr(true),
		Periods: &map[string]server.NotificationPeriod{
			"sat": {Period: server.NotificationPeriodNever},
			"mon": {Period: server.NotificationPeriodCustom, Begin: ptr("08:00"), End: ptr("18:00")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.TypesInclude) != 1 || s.TypesExclude[0] != "node not updated" || strings.Join(s.Envs, ",") != "PRD,DEV" ||
		s.FiltersetID != 7 || !s.NotifyResolved || !strings.Contains(s.Periods, `"sat":{"period":"never"}`) {
		t.Errorf("%+v", s)
	}
	for _, in := range []server.NotificationSubscriptionInput{
		{Envs: &[]string{"PROD"}},
		{TypesInclude: &[]string{""}},
		{FiltersetId: ptr(-1)},
		{Periods: &map[string]server.NotificationPeriod{"mon": {Period: server.NotificationPeriodCustom, Begin: ptr("8")}}},
		{Periods: &map[string]server.NotificationPeriod{"monday": {Period: server.NotificationPeriodNever}}},
	} {
		if _, err := buildNotifySubscription(in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
}

// TestSealedSecretLabels checks that the rewrap of the master key uses the
// labels the secrets are sealed under.
func TestSealedSecretLabels(t *testing.T) {
	for _, tbl := range cdb.SealedSecretTables {
		var want string
		switch tbl.Table {
		case "form_connectors":
			want = formConnectorLabel(7)
		case "notification_channels":
			want = notify.ChannelLabel(7)
		default:
			t.Fatalf("no label test for %s", tbl.Table)
		}
		if got := tbl.Label(7); got != want {
			t.Errorf("%s: %q, want %q", tbl.Table, got, want)
		}
	}
}

func TestGetSafeSecretsRefused(t *testing.T) {
	c, rec := basicUserContext(t, []string{"Everybody"})
	if err := (&Api{}).GetSafeSecrets(c); err != nil && !errors.Is(err, errRequestDenied) {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}
