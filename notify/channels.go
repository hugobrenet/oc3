package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/opensvc/oc3/util/mail"
)

// The types of channels.
const (
	ChannelEmail      = "email"
	ChannelSlack      = "slack"
	ChannelMattermost = "mattermost"
	ChannelTeams      = "teams"
	ChannelWebhook    = "webhook"
)

// ChannelTypes are the types of channels, in the order the API lists them.
var ChannelTypes = []string{ChannelEmail, ChannelSlack, ChannelMattermost, ChannelTeams, ChannelWebhook}

// IsWebhook tells whether a channel of the type posts to a url.
func IsWebhook(channelType string) bool {
	switch channelType {
	case ChannelSlack, ChannelMattermost, ChannelTeams, ChannelWebhook:
		return true
	}
	return false
}

// Destination is a channel as the notifications send to it, its secrets
// opened.
type Destination struct {
	Type string
	Name string
	// Addresses are the mail addresses of an email channel.
	Addresses []string
	// URL is the webhook url of the other types.
	URL string
	// Secret signs the generic webhooks, when set.
	Secret string
	// Username and IconURL, when set, are the name and the picture a Slack
	// or a Mattermost message shows.
	Username string
	IconURL  string
}

// Sender sends the messages: mails through Mail, webhooks with Client.
type Sender struct {
	Mail   mail.Config
	Client *http.Client
	// Now returns the time the generic webhooks are signed at.
	Now func() time.Time
}

// Send sends the digest to the destination.
func (s Sender) Send(ctx context.Context, dest Destination, d Digest) error {
	switch dest.Type {
	case ChannelEmail:
		if len(dest.Addresses) == 0 {
			return errors.New("the channel has no address")
		}
		if !s.Mail.IsSet() {
			return errors.New("no SMTP server: set notifications.mail.server and notifications.mail.sender")
		}
		return mail.Send(ctx, s.Mail, d.Mail(dest.Addresses...))
	case ChannelSlack, ChannelMattermost:
		return s.post(ctx, dest, SlackPayload(d, dest), nil)
	case ChannelTeams:
		return s.post(ctx, dest, TeamsPayload(d), nil)
	case ChannelWebhook:
		body, err := json.Marshal(WebhookPayload(d))
		if err != nil {
			return err
		}
		headers := http.Header{}
		if dest.Secret != "" {
			now := time.Now
			if s.Now != nil {
				now = s.Now
			}
			ts := strconv.FormatInt(now().Unix(), 10)
			headers.Set("X-OC3-Timestamp", ts)
			headers.Set("X-OC3-Signature", Sign(dest.Secret, ts, body))
		}
		return s.postRaw(ctx, dest, body, headers)
	default:
		return fmt.Errorf("unknown channel type %q", dest.Type)
	}
}

// Sign returns the signature of a generic webhook: "sha256=" and the hex of
// the HMAC-SHA256, keyed by the secret, of the timestamp, a dot and the body.
// The receiver recomputes it, and refuses an old timestamp.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (s Sender) post(ctx context.Context, dest Destination, payload any, headers http.Header) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.postRaw(ctx, dest, body, headers)
}

func (s Sender) postRaw(ctx context.Context, dest Destination, body []byte, headers http.Header) error {
	if dest.URL == "" {
		return errors.New("the channel has no url")
	}
	if s.Client == nil {
		return errors.New("no http client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest.URL, bytes.NewReader(body))
	if err != nil {
		// The url is a secret: not in the message.
		return errors.New("the url of the channel is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "oc3-notifications")
	for k, v := range headers {
		req.Header[k] = v
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("post to the channel: %s", redactURL(err.Error(), dest.URL))
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		text := strings.TrimSpace(string(answer))
		if len(text) > 200 {
			text = text[:200]
		}
		return fmt.Errorf("the channel answered %s: %s", resp.Status, text)
	}
	return nil
}

// redactURL removes the url, a secret, from an error of the http client.
func redactURL(s, url string) string {
	return strings.ReplaceAll(s, url, "<channel url>")
}

func (d Digest) intro() string {
	return strings.TrimPrefix(d.Subject(), "["+d.title()+"] ") + " on " + d.title()
}

// SlackPayload returns the message of a Slack or a Mattermost incoming
// webhook: the summary, then an attachment per alert, coloured by severity.
func SlackPayload(d Digest, dest Destination) map[string]any {
	attachments := make([]map[string]any, 0, len(d.Alerts)+1)
	for _, a := range d.Alerts {
		att := map[string]any{
			"color":    d.color(a),
			"fallback": fmt.Sprintf("[%s] %s - %s: %s", d.label(a), object(a), a.Type, message(a)),
			"title":    fmt.Sprintf("[%s] %s - %s", d.label(a), object(a), a.Type),
			"text":     message(a),
			"footer":   details(a),
		}
		if link := d.alertLink(a); link != "" {
			att["title_link"] = link
		}
		attachments = append(attachments, att)
	}
	if more := d.total() - len(d.Alerts); more > 0 {
		text := fmt.Sprintf("... and %d more", more)
		if link := d.link("/", nil); link != "" {
			text += ": " + link
		}
		attachments = append(attachments, map[string]any{"fallback": text, "text": text})
	}
	payload := map[string]any{
		"text":        d.intro(),
		"attachments": attachments,
	}
	if dest.Username != "" {
		payload["username"] = dest.Username
	}
	if dest.IconURL != "" {
		payload["icon_url"] = dest.IconURL
	}
	return payload
}

// The colours of the Adaptive Cards for the severities.
var teamsColors = []string{"good", "warning", "attention", "attention", "attention"}

// TeamsPayload returns the message of a Teams Workflows webhook ("When a
// Teams webhook request is received"): an Adaptive Card.
func TeamsPayload(d Digest) map[string]any {
	body := []map[string]any{{
		"type":   "TextBlock",
		"text":   d.intro(),
		"weight": "Bolder",
		"size":   "Medium",
		"wrap":   true,
	}}
	for _, a := range d.Alerts {
		sev := a.Severity
		if sev < 0 {
			sev = 0
		}
		if sev >= len(teamsColors) {
			sev = len(teamsColors) - 1
		}
		color := teamsColors[sev]
		if d.Resolved {
			color = "good"
		}
		text := message(a)
		if link := d.alertLink(a); link != "" {
			text = "[" + escapeMarkdown(text) + "](" + link + ")"
		} else {
			text = escapeMarkdown(text)
		}
		items := []map[string]any{
			{"type": "TextBlock", "text": fmt.Sprintf("**%s** %s - %s", strings.ToUpper(d.label(a)), escapeMarkdown(object(a)), escapeMarkdown(a.Type)), "color": color, "wrap": true},
			{"type": "TextBlock", "text": text, "wrap": true, "spacing": "None"},
		}
		if s := details(a); s != "" {
			items = append(items, map[string]any{"type": "TextBlock", "text": s, "isSubtle": true, "size": "Small", "wrap": true, "spacing": "None"})
		}
		body = append(body, map[string]any{"type": "Container", "separator": true, "items": items})
	}
	if more := d.total() - len(d.Alerts); more > 0 {
		body = append(body, map[string]any{"type": "TextBlock", "text": fmt.Sprintf("... and %d more", more), "wrap": true})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if link := d.link("/", nil); link != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open the dashboard", "url": link}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     card,
		}},
	}
}

// escapeMarkdown keeps a text from being read as markdown in a card.
func escapeMarkdown(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "`", "\\`").Replace(s)
}

type (
	// WebhookMessage is the body of a generic webhook, version 1.
	WebhookMessage struct {
		Version int `json:"version"`
		// Event is "new" for new alerts, "resolved" for alerts gone.
		Event     string         `json:"event"`
		Collector string         `json:"collector"`
		URL       string         `json:"url,omitempty"`
		Total     int            `json:"total"`
		Alerts    []WebhookAlert `json:"alerts"`
	}

	// WebhookAlert is an alert in a generic webhook.
	WebhookAlert struct {
		ID           int64  `json:"id"`
		Severity     int    `json:"severity"`
		SeverityName string `json:"severity_name"`
		Type         string `json:"type"`
		Message      string `json:"message"`
		// Object names what a resolved alert was about, its node and
		// service names being gone with it.
		Object   string `json:"object,omitempty"`
		NodeID   string `json:"node_id,omitempty"`
		Nodename string `json:"nodename,omitempty"`
		SvcID    string `json:"svc_id,omitempty"`
		Svcname  string `json:"svcname,omitempty"`
		App      string `json:"app,omitempty"`
		Env      string `json:"env,omitempty"`
		Created  string `json:"created,omitempty"`
		Link     string `json:"link,omitempty"`
	}
)

// WebhookPayload returns the body of a generic webhook.
func WebhookPayload(d Digest) WebhookMessage {
	m := WebhookMessage{Version: 1, Event: "new", Collector: d.title(), URL: d.link("/", nil), Total: d.total(), Alerts: []WebhookAlert{}}
	if d.Resolved {
		m.Event = "resolved"
	}
	for _, a := range d.Alerts {
		w := WebhookAlert{
			ID: a.ID, Severity: a.Severity, SeverityName: SeverityName(a.Severity), Type: a.Type,
			Message: message(a), Object: a.Object, NodeID: a.NodeID, Nodename: a.Nodename,
			SvcID: a.SvcID, Svcname: a.Svcname, App: a.App, Env: a.Env, Link: d.alertLink(a),
		}
		if !a.Created.IsZero() {
			w.Created = a.Created.Format(time.RFC3339)
		}
		m.Alerts = append(m.Alerts, w)
	}
	return m
}
