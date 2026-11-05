package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/oc3/cdb"
)

func testDigest() Digest {
	return Digest{
		Title:     "dev6",
		PublicURL: "https://dev6-oc3.opensvc.com",
		Alerts: []cdb.NotifyAlert{
			{ID: 12, Severity: 3, Type: "service unavailable", Fmt: "%(svc)s is down", Dict: `{"svc":"web_1"}`,
				Svcname: "web_1", Nodename: "n1", App: "shop", Env: "PRD", Created: time.Date(2026, 11, 5, 10, 12, 0, 0, time.UTC)},
		},
		Total: 3,
	}
}

type received struct {
	header http.Header
	body   []byte
}

func receiver(t *testing.T, status int) (*httptest.Server, chan received) {
	t.Helper()
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{header: r.Header.Clone(), body: b}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("refused: bad token"))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestSendSlack(t *testing.T) {
	srv, got := receiver(t, http.StatusOK)
	s := Sender{Client: srv.Client()}
	dest := Destination{Type: ChannelMattermost, URL: srv.URL + "/hooks/xyz", Username: "collector"}
	if err := s.Send(context.Background(), dest, testDigest()); err != nil {
		t.Fatal(err)
	}
	r := <-got
	var p struct {
		Text        string `json:"text"`
		Username    string `json:"username"`
		Attachments []struct {
			Color     string `json:"color"`
			Title     string `json:"title"`
			TitleLink string `json:"title_link"`
			Text      string `json:"text"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Text != "3 new alerts, highest critical on dev6" || p.Username != "collector" || len(p.Attachments) != 2 {
		t.Fatalf("payload %s", r.body)
	}
	a := p.Attachments[0]
	if a.Title != "[critical] web_1@n1 - service unavailable" || a.Text != "web_1 is down" ||
		a.TitleLink != "https://dev6-oc3.opensvc.com/?sel=12" || a.Color != "#990000" {
		t.Errorf("attachment %+v", a)
	}
	if !strings.Contains(p.Attachments[1].Text, "... and 2 more") {
		t.Errorf("more %+v", p.Attachments[1])
	}
}

func TestSendTeams(t *testing.T) {
	srv, got := receiver(t, http.StatusAccepted)
	s := Sender{Client: srv.Client()}
	if err := s.Send(context.Background(), Destination{Type: ChannelTeams, URL: srv.URL}, testDigest()); err != nil {
		t.Fatal(err)
	}
	r := <-got
	body := string(r.body)
	for _, want := range []string{
		`"contentType":"application/vnd.microsoft.card.adaptive"`,
		`"type":"AdaptiveCard"`,
		`**CRITICAL** web\\_1@n1 - service unavailable`,
		`[web\\_1 is down](https://dev6-oc3.opensvc.com/?sel=12)`,
		`"Action.OpenUrl"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("payload lacks %s:\n%s", want, body)
		}
	}
}

func TestSendWebhookSigned(t *testing.T) {
	srv, got := receiver(t, http.StatusNoContent)
	now := time.Unix(1700000000, 0)
	s := Sender{Client: srv.Client(), Now: func() time.Time { return now }}
	if err := s.Send(context.Background(), Destination{Type: ChannelWebhook, URL: srv.URL, Secret: "k"}, testDigest()); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.header.Get("X-OC3-Timestamp") != "1700000000" {
		t.Errorf("timestamp %q", r.header.Get("X-OC3-Timestamp"))
	}
	if want := Sign("k", "1700000000", r.body); r.header.Get("X-OC3-Signature") != want {
		t.Errorf("signature %q, want %q", r.header.Get("X-OC3-Signature"), want)
	}
	var m WebhookMessage
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.Total != 3 || len(m.Alerts) != 1 || m.Alerts[0].Message != "web_1 is down" ||
		m.Alerts[0].SeverityName != "critical" || m.Alerts[0].Created != "2026-11-05T10:12:00Z" {
		t.Errorf("message %+v", m)
	}
}

func TestSendWebhookUnsigned(t *testing.T) {
	srv, got := receiver(t, http.StatusOK)
	s := Sender{Client: srv.Client()}
	if err := s.Send(context.Background(), Destination{Type: ChannelWebhook, URL: srv.URL}, testDigest()); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.header.Get("X-OC3-Signature") != "" {
		t.Error("signed without a secret")
	}
}

func TestSendRefused(t *testing.T) {
	srv, _ := receiver(t, http.StatusForbidden)
	s := Sender{Client: srv.Client()}
	err := s.Send(context.Background(), Destination{Type: ChannelSlack, URL: srv.URL + "/secret-token"}, testDigest())
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err %v", err)
	}
}

func TestSendHidesURL(t *testing.T) {
	s := Sender{Client: &http.Client{Timeout: time.Second}}
	url := "http://127.0.0.1:1/hooks/secret-token"
	err := s.Send(context.Background(), Destination{Type: ChannelSlack, URL: url}, testDigest())
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("err %v", err)
	}
}

func TestSendEmailWithoutAddress(t *testing.T) {
	if err := (Sender{}).Send(context.Background(), Destination{Type: ChannelEmail}, testDigest()); err == nil {
		t.Error("sent to no address")
	}
}
