package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testReport() Report {
	return Report{Title: "dev6", PublicURL: "https://dev6-oc3.opensvc.com", Name: "morning", Skill: "morning-check",
		Body: "# Morning check\n\n| node | state |\n|---|---|\n| n1 | <down> |\n", Reason: "Sent by the schedule morning.", Path: "/ai/skills"}
}

func TestReportMail(t *testing.T) {
	m := testReport().Mail("ops@example.com")
	if m.Subject != "[dev6] morning (skill morning-check)" {
		t.Errorf("subject %q", m.Subject)
	}
	if !strings.Contains(m.Text, "| n1 | <down> |") || !strings.Contains(m.Text, "https://dev6-oc3.opensvc.com/ai/skills") {
		t.Errorf("text %q", m.Text)
	}
	if strings.Contains(m.HTML, "<down>") || !strings.Contains(m.HTML, "&lt;down&gt;") {
		t.Errorf("the html does not escape the report: %q", m.HTML)
	}
	failed := testReport()
	failed.Failed = true
	if s := failed.Subject(); s != "[dev6] morning (skill morning-check) failed" {
		t.Errorf("subject %q", s)
	}
}

func TestSendReportToChats(t *testing.T) {
	long := testReport()
	long.Body = strings.Repeat("é", reportMaxChat+10)
	srv, got := receiver(t, http.StatusOK)
	s := Sender{Client: srv.Client()}
	if err := s.SendReport(context.Background(), Destination{Type: ChannelSlack, URL: srv.URL}, long); err != nil {
		t.Fatal(err)
	}
	var slack struct {
		Text        string `json:"text"`
		Attachments []struct {
			Text string `json:"text"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal((<-got).body, &slack); err != nil {
		t.Fatal(err)
	}
	if len(slack.Attachments) != 1 || !strings.HasSuffix(slack.Attachments[0].Text, "(truncated, 10 more characters)") ||
		!strings.Contains(slack.Text, "morning") {
		t.Errorf("slack %+v", slack)
	}

	if err := s.SendReport(context.Background(), Destination{Type: ChannelTeams, URL: srv.URL}, testReport()); err != nil {
		t.Fatal(err)
	}
	if body := string((<-got).body); !strings.Contains(body, "AdaptiveCard") || !strings.Contains(body, "| n1 |") {
		t.Errorf("teams %s", body)
	}
}

func TestSendReportToWebhook(t *testing.T) {
	srv, got := receiver(t, http.StatusOK)
	s := Sender{Client: srv.Client(), Now: func() time.Time { return time.Unix(1700000000, 0) }}
	if err := s.SendReport(context.Background(), Destination{Type: ChannelWebhook, URL: srv.URL, Secret: "k"}, testReport()); err != nil {
		t.Fatal(err)
	}
	r := <-got
	var p WebhookReport
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != "report" || p.Name != "morning" || p.Markdown != testReport().Body || p.URL != "https://dev6-oc3.opensvc.com/ai/skills" {
		t.Errorf("payload %+v", p)
	}
	if r.header.Get("X-OC3-Signature") != Sign("k", "1700000000", r.body) {
		t.Error("the report is not signed")
	}
}
