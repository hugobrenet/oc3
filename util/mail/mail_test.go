package mail

import (
	"bufio"
	"context"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestCompose(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	data, err := Compose("collector@example.com", Message{
		To:      []string{"a@example.com", "b@example.com"},
		Subject: "[collector] 2 alertes, critique\r\nBcc: x@evil",
		Text:    "ligne 1\nligne 2 é",
		HTML:    "<p>é</p>",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("not a mail: %s\n%s", err, data)
	}
	if got := m.Header.Get("Bcc"); got != "" {
		t.Errorf("header injected: Bcc %q", got)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || !strings.HasPrefix(subject, "[collector] 2 alertes, critique") {
		t.Errorf("subject %q (%v)", subject, err)
	}
	if got := m.Header.Get("To"); got != "a@example.com, b@example.com" {
		t.Errorf("to %q", got)
	}
	if !strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/alternative;") {
		t.Errorf("content-type %q", m.Header.Get("Content-Type"))
	}
	if !strings.HasSuffix(m.Header.Get("Message-ID"), "@example.com>") {
		t.Errorf("message-id %q", m.Header.Get("Message-ID"))
	}
	body, _ := io.ReadAll(m.Body)
	for _, want := range []string{"text/plain; charset=utf-8", "text/html; charset=utf-8", "ligne 2 =C3=A9"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestComposeText(t *testing.T) {
	data, err := Compose("c@example.com", Message{To: []string{"a@example.com"}, Subject: "s", Text: "x\r\ny"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content-type %q", got)
	}
	body, _ := io.ReadAll(m.Body)
	if string(body) != "x\r\ny" {
		t.Errorf("body %q", body)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		cfg Config
		ok  bool
	}{
		{Config{Server: "smtp:25", Sender: "c@example.com"}, true},
		{Config{Server: "smtp:25", Sender: "c@example.com", Security: "tls"}, true},
		{Config{Server: "smtp", Sender: "c@example.com"}, false},
		{Config{Server: "smtp:25", Sender: "nobody"}, false},
		{Config{Server: "smtp:25", Sender: "c@example.com", Security: "ssl"}, false},
		{Config{}, false},
	} {
		if err := tc.cfg.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc.cfg, err)
		}
	}
}

// TestSend sends through a minimal SMTP server, without encryption.
func TestSend(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	got := make(chan []string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		var lines []string
		say("220 test")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				got <- lines
				return
			}
			line = strings.TrimRight(line, "\r\n")
			lines = append(lines, line)
			switch {
			case strings.HasPrefix(line, "EHLO"):
				say("250 test")
			case line == "DATA":
				say("354 go")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					lines = append(lines, strings.TrimRight(l, "\r\n"))
				}
				say("250 queued")
			case line == "QUIT":
				say("221 bye")
				got <- lines
				return
			default:
				say("250 ok")
			}
		}
	}()
	cfg := Config{Server: l.Addr().String(), Sender: "c@example.com", Security: SecurityNone, Timeout: 5 * time.Second}
	if err := Send(context.Background(), cfg, Message{To: []string{"a@example.com"}, Subject: "hello", Text: "body"}); err != nil {
		t.Fatal(err)
	}
	lines := <-got
	all := strings.Join(lines, "\n")
	for _, want := range []string{"MAIL FROM:<c@example.com>", "RCPT TO:<a@example.com>", "Subject: hello", "body"} {
		if !strings.Contains(all, want) {
			t.Errorf("exchange lacks %q:\n%s", want, all)
		}
	}
}

func TestSendRefusesBadRecipient(t *testing.T) {
	cfg := Config{Server: "127.0.0.1:1", Sender: "c@example.com"}
	if err := Send(context.Background(), cfg, Message{To: []string{"not an address"}}); err == nil {
		t.Error("a bad recipient was accepted")
	}
}
