package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/opensvc/oc3/cdb"
)

func TestMinSeverity(t *testing.T) {
	for level, want := range map[string]int{
		"debug": 0, "info": 0, "warning": 1, "error": 2, "critical": 3, "": 1, "bogus": 1, " Error ": 2,
	} {
		if got := MinSeverity(level); got != want {
			t.Errorf("%q: got %d, want %d", level, got, want)
		}
	}
}

func TestSeverityName(t *testing.T) {
	for sev, want := range map[int]string{-1: "notice", 0: "notice", 3: "critical", 4: "alert", 9: "alert"} {
		if got := SeverityName(sev); got != want {
			t.Errorf("%d: got %q, want %q", sev, got, want)
		}
	}
}

func TestInPeriod(t *testing.T) {
	// 2026-11-05 is a Thursday.
	at := func(hm string) time.Time {
		tm, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-05 "+hm, time.Local)
		return tm
	}
	for _, tc := range []struct {
		name  string
		prefs string
		at    string
		want  bool
	}{
		{"no prefs", "", "03:00", true},
		{"unreadable", "{", "03:00", true},
		{"no periods", `{"theme":"dark"}`, "03:00", true},
		{"day without period", `{"notifications_periods":{"mon":{"period":"never"}}}`, "03:00", true},
		{"always", `{"notifications_periods":{"thu":{"period":"always"}}}`, "03:00", true},
		{"never", `{"notifications_periods":{"thu":{"period":"never"}}}`, "12:00", false},
		{"custom in", `{"notifications_periods":{"thu":{"period":"custom","begin":"08:00","end":"18:00"}}}`, "12:00", true},
		{"custom begin", `{"notifications_periods":{"thu":{"period":"custom","begin":"08:00","end":"18:00"}}}`, "08:00", true},
		{"custom out", `{"notifications_periods":{"thu":{"period":"custom","begin":"08:00","end":"18:00"}}}`, "18:01", false},
		{"night in", `{"notifications_periods":{"thu":{"period":"custom","begin":"22:00","end":"06:00"}}}`, "23:30", true},
		{"night out", `{"notifications_periods":{"thu":{"period":"custom","begin":"22:00","end":"06:00"}}}`, "12:00", false},
		{"custom unreadable", `{"notifications_periods":{"thu":{"period":"custom","begin":"8h"}}}`, "03:00", true},
	} {
		if got := InPeriod(tc.prefs, at(tc.at)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFormatMessage(t *testing.T) {
	for _, tc := range []struct{ fmt, dict, want string }{
		{"%(n)s instances not updated", `{"n": 3}`, "3 instances not updated"},
		{"%(svc)s on %(node)s", `{"svc": "s1", "node": "n1"}`, "s1 on n1"},
		{"%(missing)s left", `{}`, "%(missing)s left"},
		{"%(a)s", `not json`, "%(a)s"},
		{"%(v)d%%", `{"v": 1.5}`, "1.5%%"},
		{"%(l)s", `{"l": ["a", "b"]}`, `["a","b"]`},
		{"plain", "", "plain"},
	} {
		if got := FormatMessage(tc.fmt, tc.dict); got != tc.want {
			t.Errorf("%q %q: got %q, want %q", tc.fmt, tc.dict, got, tc.want)
		}
	}
}

func TestDigest(t *testing.T) {
	created := time.Date(2026, 11, 5, 10, 12, 0, 0, time.Local)
	d := Digest{
		Title:     "dev6",
		PublicURL: "https://dev6-oc3.opensvc.com/",
		Alerts: []cdb.NotifyAlert{
			{ID: 12, Severity: 3, Type: "service unavailable", Fmt: "%(svc)s is down", Dict: `{"svc":"web"}`,
				Svcname: "web", Nodename: "n1", App: "shop", Env: "PRD", Created: created},
			{ID: 13, Severity: 1, Type: "node not updated", Fmt: "<b>late</b>", Nodename: "n2"},
		},
		Total: 5,
	}
	if got, want := d.Subject(), "[dev6] 5 new alerts, highest critical"; got != want {
		t.Errorf("subject %q, want %q", got, want)
	}
	text := d.Text()
	for _, want := range []string{
		"[critical] web@n1 - service unavailable: web is down",
		"since 2026-11-05 10:12, app shop, env PRD",
		"https://dev6-oc3.opensvc.com/?sel=12",
		"[warning] n2 - node not updated: <b>late</b>",
		"... and 3 more: https://dev6-oc3.opensvc.com/",
		"Notification settings: https://dev6-oc3.opensvc.com/profile",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	h := d.HTML()
	if strings.Contains(h, "<b>late</b>") || !strings.Contains(h, "&lt;b&gt;late&lt;/b&gt;") {
		t.Errorf("html not escaped:\n%s", h)
	}
	if !strings.Contains(h, `href="https://dev6-oc3.opensvc.com/?sel=12"`) {
		t.Errorf("html lacks the alert link:\n%s", h)
	}

	one := Digest{Alerts: d.Alerts[1:]}
	if got, want := one.Subject(), "[collector] 1 new alert, highest warning"; got != want {
		t.Errorf("subject %q, want %q", got, want)
	}
	if strings.Contains(one.Text(), "http") {
		t.Errorf("links without a public url:\n%s", one.Text())
	}
}

func TestValidatePeriods(t *testing.T) {
	for periods, ok := range map[string]bool{
		``: true,
		`{"mon":{"period":"never"},"sun":{"period":"custom","begin":"22:00","end":"06:00"}}`: true,
		`{"lun":{"period":"never"}}`:                         false,
		`{"mon":{"period":"sometimes"}}`:                     false,
		`{"mon":{"period":"custom","begin":"8h","end":"9"}}`: false,
		`{"mon":null}`: false,
		`[]`:           false,
	} {
		if err := ValidatePeriods(periods); (err == nil) != ok {
			t.Errorf("%s: %v", periods, err)
		}
	}
	thu, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-05 12:00", time.Local)
	if InPeriods(`{"thu":{"period":"never"}}`, thu) || !InPeriods(``, thu) || !InPeriods(`{"fri":{"period":"never"}}`, thu) {
		t.Error("InPeriods")
	}
}

func TestDigestResolved(t *testing.T) {
	d := Digest{
		Title:     "dev6",
		PublicURL: "https://dev6-oc3.opensvc.com",
		Resolved:  true,
		Alerts:    []cdb.NotifyAlert{{ID: 12, Severity: 3, Type: "service unavailable", Object: "web@n1", Message: "web is down"}},
		Reason:    "Sent to the channel ops.",
	}
	if got, want := d.Subject(), "[dev6] 1 alert resolved"; got != want {
		t.Errorf("subject %q, want %q", got, want)
	}
	text := d.Text()
	for _, want := range []string{"[resolved, was critical] web@n1 - service unavailable: web is down", "Sent to the channel ops."} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "?sel=") || strings.Contains(text, "/profile") {
		t.Errorf("link to a gone alert or to the profile:\n%s", text)
	}
	if m := WebhookPayload(d); m.Event != "resolved" || m.Alerts[0].Object != "web@n1" || m.Alerts[0].Link != "" {
		t.Errorf("webhook %+v", m)
	}
	if p := SlackPayload(d, Destination{}); p["attachments"].([]map[string]any)[0]["color"] != "#009900" {
		t.Errorf("slack %+v", p)
	}
	if item := ClaimItem(cdb.NotifyAlert{ID: 1, Severity: 2, Type: "t", Fmt: "%(a)s", Dict: `{"a":"x"}`, Nodename: "n"}); item.Object != "n" || item.Message != "x" {
		t.Errorf("claim item %+v", item)
	}
}
