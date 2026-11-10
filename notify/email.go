package notify

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/util/mail"
)

// The colours of the severities, as the historical mails had them.
var severityColors = []string{"#009900", "#ffa500", "#dd0000", "#990000", "#292b2c"}

func severityColor(severity int) string {
	if severity < 0 {
		severity = 0
	}
	if severity >= len(severityColors) {
		severity = len(severityColors) - 1
	}
	return severityColors[severity]
}

// Digest is a message announcing new alerts to a recipient.
type Digest struct {
	// Title names the collector in the subject, "collector" by default.
	Title string
	// PublicURL is the address of the collector the links lead to; without
	// it the messages have no link.
	PublicURL string
	// Alerts are the alerts detailed in the message, the most severe first.
	Alerts []cdb.NotifyAlert
	// Total is the number of new alerts, more than len(Alerts) when the
	// message only details the first ones.
	Total int
	// Resolved announces alerts gone from the dashboard, rather than new
	// ones.
	Resolved bool
	// Reason tells the reader why they receive the message, "You receive
	// these alerts as responsible for their apps." by default.
	Reason string
}

func (d Digest) reason() string {
	if d.Reason == "" {
		return "You receive these alerts as responsible for their apps."
	}
	return d.Reason
}

func (d Digest) title() string {
	if d.Title == "" {
		return "collector"
	}
	return d.Title
}

func (d Digest) total() int {
	if d.Total < len(d.Alerts) {
		return len(d.Alerts)
	}
	return d.Total
}

func (d Digest) highest() int {
	h := 0
	for _, a := range d.Alerts {
		h = max(h, a.Severity)
	}
	return h
}

// link returns the address of path on the collector, "" without PublicURL.
func (d Digest) link(path string, query url.Values) string {
	base := strings.TrimRight(d.PublicURL, "/")
	if base == "" {
		return ""
	}
	s := base + path
	if len(query) > 0 {
		s += "?" + query.Encode()
	}
	return s
}

// ClaimItem returns what a message announces of an alert, kept to announce
// its resolution.
func ClaimItem(a cdb.NotifyAlert) cdb.NotifyClaimItem {
	return cdb.NotifyClaimItem{ID: a.ID, Severity: a.Severity, Type: a.Type, Object: object(a), Message: message(a)}
}

// message returns the message of an alert, as a former message gave it when
// the alert is gone.
func message(a cdb.NotifyAlert) string {
	if a.Message != "" {
		return a.Message
	}
	return FormatMessage(a.Fmt, a.Dict)
}

func (d Digest) alertLink(a cdb.NotifyAlert) string {
	if a.ID == 0 || d.Resolved {
		// A sample alert, the test of a channel: in no dashboard.
		return ""
	}
	return d.link("/", url.Values{"sel": {strconv.FormatInt(a.ID, 10)}})
}

// label names the state of an alert in a message: its severity, or resolved.
func (d Digest) label(a cdb.NotifyAlert) string {
	if d.Resolved {
		return "resolved, was " + SeverityName(a.Severity)
	}
	return SeverityName(a.Severity)
}

// color returns the colour of an alert in a message: of its severity, or
// green once resolved.
func (d Digest) color(a cdb.NotifyAlert) string {
	if d.Resolved {
		return severityColors[0]
	}
	return severityColor(a.Severity)
}

// Subject returns the subject of the message: "[collector] 3 new alerts,
// highest critical".
func (d Digest) Subject() string {
	n := d.total()
	noun := "alerts"
	if n == 1 {
		noun = "alert"
	}
	if d.Resolved {
		return fmt.Sprintf("[%s] %d %s resolved", d.title(), n, noun)
	}
	return fmt.Sprintf("[%s] %d new %s, highest %s", d.title(), n, noun, SeverityName(d.highest()))
}

// object names what an alert is about: the service, on its node, or the node.
func object(a cdb.NotifyAlert) string {
	switch {
	case a.Object != "":
		return a.Object
	case a.Svcname != "" && a.Nodename != "":
		return a.Svcname + "@" + a.Nodename
	case a.Svcname != "":
		return a.Svcname
	case a.Nodename != "":
		return a.Nodename
	default:
		return "-"
	}
}

func details(a cdb.NotifyAlert) string {
	var l []string
	if !a.Created.IsZero() {
		l = append(l, "since "+a.Created.Format("2006-01-02 15:04"))
	}
	if a.App != "" {
		l = append(l, "app "+a.App)
	}
	if a.Env != "" {
		l = append(l, "env "+a.Env)
	}
	return strings.Join(l, ", ")
}

// Text returns the message as plain text.
func (d Digest) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s on %s:\n\n", strings.TrimPrefix(d.Subject(), "["+d.title()+"] "), d.title())
	for _, a := range d.Alerts {
		fmt.Fprintf(&b, "[%s] %s - %s: %s\n", d.label(a), object(a), a.Type, message(a))
		if s := details(a); s != "" {
			fmt.Fprintf(&b, "    %s\n", s)
		}
		if s := d.alertLink(a); s != "" {
			fmt.Fprintf(&b, "    %s\n", s)
		}
	}
	if more := d.total() - len(d.Alerts); more > 0 {
		fmt.Fprintf(&b, "\n... and %d more", more)
		if s := d.link("/", nil); s != "" {
			fmt.Fprintf(&b, ": %s", s)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n-- \n" + d.reason())
	if s := d.link("/profile", nil); s != "" && d.Reason == "" {
		fmt.Fprintf(&b, "\nNotification settings: %s", s)
	}
	b.WriteString("\n")
	return b.String()
}

// HTML returns the message as html.
func (d Digest) HTML() string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="font-family:sans-serif;font-size:14px;color:#222">`)
	fmt.Fprintf(&b, "<p>%s on %s:</p>", e(strings.TrimPrefix(d.Subject(), "["+d.title()+"] ")), e(d.title()))
	b.WriteString(`<table style="border-collapse:collapse">`)
	for _, a := range d.Alerts {
		message := e(message(a))
		if s := d.alertLink(a); s != "" {
			message = `<a href="` + e(s) + `">` + message + `</a>`
		}
		fmt.Fprintf(&b, `<tr style="border-top:1px solid #ddd">`+
			`<td style="padding:6px 8px;vertical-align:top"><span style="background:%s;color:#fff;padding:1px 6px;border-radius:3px">%s</span></td>`+
			`<td style="padding:6px 8px;vertical-align:top"><b>%s</b><br>%s</td>`+
			`<td style="padding:6px 8px;vertical-align:top">%s<br><span style="color:#777">%s</span></td></tr>`,
			d.color(a), e(d.label(a)), e(object(a)), e(a.Type), message, e(details(a)))
	}
	b.WriteString("</table>")
	if more := d.total() - len(d.Alerts); more > 0 {
		text := fmt.Sprintf("... and %d more", more)
		if s := d.link("/", nil); s != "" {
			fmt.Fprintf(&b, `<p><a href="%s">%s</a></p>`, e(s), e(text))
		} else {
			fmt.Fprintf(&b, "<p>%s</p>", e(text))
		}
	}
	b.WriteString(`<p style="color:#777;font-size:12px">` + e(d.reason()))
	if s := d.link("/profile", nil); s != "" && d.Reason == "" {
		fmt.Fprintf(&b, ` <a href="%s">Notification settings</a>.`, e(s))
	}
	b.WriteString("</p></body></html>")
	return b.String()
}

// Mail returns the message as a mail to the addresses to.
func (d Digest) Mail(to ...string) mail.Message {
	return mail.Message{To: to, Subject: d.Subject(), Text: d.Text(), HTML: d.HTML()}
}
