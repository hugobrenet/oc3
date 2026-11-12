package notify

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/opensvc/oc3/util/mail"
)

// Report is a text the collector sends to channels, rather than alerts: the
// answer of the AI assistant to a scheduled skill. Its body is Markdown, sent
// as is where the channel reads Markdown, as text elsewhere.
type Report struct {
	// Title names the collector in the subject, "collector" by default.
	Title string
	// PublicURL is the address of the collector the link leads to.
	PublicURL string
	// Name is what the report is, the name of its schedule.
	Name string
	// Skill is the skill the assistant ran.
	Skill string
	// Failed announces a run that gave no report; Body then tells why.
	Failed bool
	// Body is the report, in Markdown.
	Body string
	// Reason tells the reader why they receive it.
	Reason string
	// Path is the page of the collector the report links to, "/" by default.
	Path string
}

// The longest bodies the chat channels take, in characters: a Mattermost post
// holds 16383 of them, a Teams card about 28 KB, a Slack text 40000.
const (
	reportMaxChat  = 15000
	reportMaxTeams = 20000
)

func (r Report) title() string {
	if r.Title == "" {
		return "collector"
	}
	return r.Title
}

func (r Report) link() string {
	base := strings.TrimRight(r.PublicURL, "/")
	if base == "" {
		return ""
	}
	path := r.Path
	if path == "" {
		path = "/"
	}
	return base + path
}

// headline is the subject without the collector: "morning-check (skill
// morning-check)", or that it failed.
func (r Report) headline() string {
	s := r.Name
	if r.Skill != "" && r.Skill != r.Name {
		s += " (skill " + r.Skill + ")"
	}
	if r.Failed {
		s += " failed"
	}
	return s
}

// Subject returns the subject of the report: "[collector] morning-check".
func (r Report) Subject() string {
	return "[" + r.title() + "] " + r.headline()
}

// truncate keeps the first n characters of s, telling what it cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "\n\n... (truncated, " + strconv.Itoa(len(runes)-n) + " more characters)"
}

func (r Report) footer() string {
	var l []string
	if r.Reason != "" {
		l = append(l, r.Reason)
	}
	if link := r.link(); link != "" {
		l = append(l, link)
	}
	return strings.Join(l, "\n")
}

// Text returns the report as plain text.
func (r Report) Text() string {
	s := r.headline() + " on " + r.title() + ":\n\n" + strings.TrimSpace(r.Body) + "\n"
	if f := r.footer(); f != "" {
		s += "\n-- \n" + f + "\n"
	}
	return s
}

// HTML returns the report as html: its Markdown kept as text, its lines and
// its tables aligned.
func (r Report) HTML() string {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="font-family:sans-serif;font-size:14px;color:#222">`)
	fmt.Fprintf(&b, "<p><b>%s</b> on %s:</p>", e(r.headline()), e(r.title()))
	fmt.Fprintf(&b, `<div style="white-space:pre-wrap;font-family:monospace;font-size:13px">%s</div>`, e(strings.TrimSpace(r.Body)))
	b.WriteString(`<p style="color:#777;font-size:12px">` + e(r.Reason))
	if link := r.link(); link != "" {
		fmt.Fprintf(&b, ` <a href="%s">%s</a>`, e(link), e(r.title()))
	}
	b.WriteString("</p></body></html>")
	return b.String()
}

// Mail returns the report as a mail to the addresses to.
func (r Report) Mail(to ...string) mail.Message {
	return mail.Message{To: to, Subject: r.Subject(), Text: r.Text(), HTML: r.HTML()}
}

// SlackReportPayload returns the report as a Slack or a Mattermost message.
func SlackReportPayload(r Report, dest Destination) map[string]any {
	color := severityColors[0]
	if r.Failed {
		color = severityColors[2]
	}
	payload := map[string]any{
		"text": "**" + r.headline() + "** on " + r.title(),
		"attachments": []map[string]any{{
			"color":    color,
			"fallback": r.headline(),
			"text":     truncate(strings.TrimSpace(r.Body), reportMaxChat),
			"footer":   r.footer(),
		}},
	}
	if dest.Username != "" {
		payload["username"] = dest.Username
	}
	if dest.IconURL != "" {
		payload["icon_url"] = dest.IconURL
	}
	return payload
}

// TeamsReportPayload returns the report as a Teams Adaptive Card, whose text
// blocks read a part of Markdown.
func TeamsReportPayload(r Report) map[string]any {
	color := "default"
	if r.Failed {
		color = "attention"
	}
	body := []map[string]any{
		{"type": "TextBlock", "text": escapeMarkdown(r.headline() + " on " + r.title()), "weight": "Bolder", "size": "Medium", "color": color, "wrap": true},
		{"type": "TextBlock", "text": truncate(strings.TrimSpace(r.Body), reportMaxTeams), "wrap": true},
	}
	if r.Reason != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": escapeMarkdown(r.Reason), "isSubtle": true, "size": "Small", "wrap": true})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if link := r.link(); link != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open the collector", "url": link}}
	}
	return map[string]any{
		"type":        "message",
		"attachments": []map[string]any{{"contentType": "application/vnd.microsoft.card.adaptive", "content": card}},
	}
}

// WebhookReport is the body of a generic webhook carrying a report, version 1,
// told from the alerts by its event "report".
type WebhookReport struct {
	Version   int    `json:"version"`
	Event     string `json:"event"`
	Collector string `json:"collector"`
	URL       string `json:"url,omitempty"`
	Name      string `json:"name"`
	Skill     string `json:"skill,omitempty"`
	Failed    bool   `json:"failed"`
	Markdown  string `json:"markdown"`
}

// WebhookReportPayload returns the body of a generic webhook carrying r.
func WebhookReportPayload(r Report) WebhookReport {
	return WebhookReport{Version: 1, Event: "report", Collector: r.title(), URL: r.link(), Name: r.Name, Skill: r.Skill,
		Failed: r.Failed, Markdown: r.Body}
}

// SendReport sends the report to the destination.
func (s Sender) SendReport(ctx context.Context, dest Destination, r Report) error {
	switch dest.Type {
	case ChannelEmail:
		if len(dest.Addresses) == 0 {
			return errors.New("the channel has no address")
		}
		if !s.Mail.IsSet() {
			return errors.New("no SMTP server: set notifications.mail.server and notifications.mail.sender")
		}
		return mail.Send(ctx, s.Mail, r.Mail(dest.Addresses...))
	case ChannelSlack, ChannelMattermost:
		return s.post(ctx, dest, SlackReportPayload(r, dest), nil)
	case ChannelTeams:
		return s.post(ctx, dest, TeamsReportPayload(r), nil)
	case ChannelWebhook:
		return s.postSigned(ctx, dest, WebhookReportPayload(r))
	default:
		return fmt.Errorf("unknown channel type %q", dest.Type)
	}
}
