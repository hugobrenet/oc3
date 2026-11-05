// Package notify builds the notifications of the alerts of the dashboard: who
// is notified when, and the messages, independently of the way they are sent.
package notify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The severities of the dashboard alerts (dashboard.dash_severity).
var severityNames = []string{"notice", "warning", "error", "critical", "alert"}

// SeverityName returns the name of a dashboard severity.
func SeverityName(severity int) string {
	if severity < 0 {
		severity = 0
	}
	if severity >= len(severityNames) {
		severity = len(severityNames) - 1
	}
	return severityNames[severity]
}

// MinSeverity returns the lowest dashboard severity notified to a user who
// set level as the level of their notifications (auth_user.email_log_level,
// im_log_level): debug and info notify every alert, warning from the
// warnings, error from the errors, critical from the critical ones. An
// unknown level counts as warning, the default of the column.
func MinSeverity(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "notice":
		return 0
	case "error":
		return 2
	case "critical":
		return 3
	case "alert":
		return 4
	default:
		return 1
	}
}

type (
	// Period is the notification period of a day of the week, in the
	// preferences of a user (user_prefs.prefs, notifications_periods.<day>):
	// always, never, or custom, from Begin to End, "HH:MM", local time.
	Period struct {
		Period string `json:"period"`
		Begin  string `json:"begin"`
		End    string `json:"end"`
	}
)

var weekdays = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// InPeriod tells whether a user whose preferences document is prefs is to
// be notified at t, as init/alertd/alertd.py in_period() does: always, but on
// a day whose period is never, or custom with t out of [begin, end]. A custom
// period ending before it begins spans midnight: 22:00-06:00 is the night.
// An unreadable document, or a day without period, notifies.
func InPeriod(prefs string, t time.Time) bool {
	if strings.TrimSpace(prefs) == "" {
		return true
	}
	var doc struct {
		Periods map[string]*Period `json:"notifications_periods"`
	}
	if err := json.Unmarshal([]byte(prefs), &doc); err != nil || doc.Periods == nil {
		return true
	}
	return inPeriods(doc.Periods, t)
}

// InPeriods tells whether a subscription whose periods are the JSON object
// periods, by day of the week as in the user preferences, sends at t; empty,
// it always does.
func InPeriods(periods string, t time.Time) bool {
	if strings.TrimSpace(periods) == "" {
		return true
	}
	var m map[string]*Period
	if err := json.Unmarshal([]byte(periods), &m); err != nil {
		return true
	}
	return inPeriods(m, t)
}

// ValidatePeriods verifies the periods of a subscription: an object whose
// keys are days, mon to sun, and whose values are always, never, or custom
// with begin and end "HH:MM".
func ValidatePeriods(periods string) error {
	if strings.TrimSpace(periods) == "" {
		return nil
	}
	var m map[string]*Period
	if err := json.Unmarshal([]byte(periods), &m); err != nil {
		return fmt.Errorf("an object of the days of the week expected: %w", err)
	}
	for day, p := range m {
		if !slicesContains(weekdays[:], day) {
			return fmt.Errorf("%q is not a day: mon, tue, wed, thu, fri, sat or sun", day)
		}
		if p == nil {
			return fmt.Errorf("%s: no period", day)
		}
		switch p.Period {
		case "always", "never":
		case "custom":
			if _, ok := minuteOfDay(p.Begin); !ok {
				return fmt.Errorf("%s: begin %q is not HH:MM", day, p.Begin)
			}
			if _, ok := minuteOfDay(p.End); !ok {
				return fmt.Errorf("%s: end %q is not HH:MM", day, p.End)
			}
		default:
			return fmt.Errorf("%s: period %q is none of always, never, custom", day, p.Period)
		}
	}
	return nil
}

func slicesContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func inPeriods(periods map[string]*Period, t time.Time) bool {
	p := periods[weekdays[t.Weekday()]]
	if p == nil {
		return true
	}
	switch p.Period {
	case "never":
		return false
	case "custom":
		begin, okBegin := minuteOfDay(p.Begin)
		end, okEnd := minuteOfDay(p.End)
		if !okBegin || !okEnd {
			return true
		}
		now := t.Hour()*60 + t.Minute()
		if begin <= end {
			return now >= begin && now <= end
		}
		return now >= begin || now <= end
	default:
		return true
	}
}

func minuteOfDay(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// placeholder is a %(key)s or %(key)d of the Python format of the dashboard
// messages (dash_fmt), as the frontend reads it (features/logs/log-message).
var placeholder = regexp.MustCompile(`%\(([^)]+)\)[sd]`)

// FormatMessage returns the message of an alert: fmt with each placeholder
// receiving its value from dict, a JSON object. A placeholder without value
// stays as it is, and an unreadable dict leaves the format.
func FormatMessage(fmtString, dict string) string {
	values := map[string]any{}
	if strings.TrimSpace(dict) != "" {
		if err := json.Unmarshal([]byte(dict), &values); err != nil {
			return fmtString
		}
	}
	return placeholder.ReplaceAllStringFunc(fmtString, func(m string) string {
		key := placeholder.FindStringSubmatch(m)[1]
		v, ok := values[key]
		if !ok {
			return m
		}
		switch v := v.(type) {
		case nil:
			return ""
		case string:
			return v
		case float64:
			return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
		default:
			b, _ := json.Marshal(v)
			return string(b)
		}
	})
}

// RetryAfter returns the wait before the next try of a sending which failed
// attempts times.
func RetryAfter(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Minute
	case attempts == 2:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}
