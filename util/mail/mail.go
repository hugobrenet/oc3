// Package mail sends the mails of the collector, the alert notifications and
// the outputs of the forms, through an SMTP server.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// The security of the connection to the SMTP server.
const (
	// SecurityAuto upgrades the connection with STARTTLS when the server
	// offers it, as net/smtp.SendMail does.
	SecurityAuto = "auto"
	// SecurityStartTLS requires STARTTLS: a server not offering it is refused.
	SecurityStartTLS = "starttls"
	// SecurityTLS opens a TLS connection from the start, the port 465 way.
	SecurityTLS = "tls"
	// SecurityNone never encrypts, for a relay on the same host or network.
	SecurityNone = "none"
)

type (
	// Config is where and how the mails are sent.
	Config struct {
		// Server is the SMTP server, host:port.
		Server string
		// Sender is the address of the From header and of the envelope.
		Sender string
		// Username and Password authenticate to the server, when Username is
		// set. The password is only sent over an encrypted connection, or to
		// a server on the loopback.
		Username string
		Password string
		// Security is one of the Security* values, SecurityAuto when empty.
		Security string
		// Timeout bounds the whole exchange with the server.
		Timeout time.Duration
	}

	// Message is a mail to send: a text body, and an html one shown instead by
	// the mail readers able to.
	Message struct {
		To      []string
		Subject string
		Text    string
		HTML    string
	}
)

// ConfigFromViper reads the configuration under prefix: <prefix>.server,
// .sender, .username, .password, .security and .timeout. The historical
// <prefix>.login, "user:password", is read when .username is not set.
func ConfigFromViper(prefix string) Config {
	cfg := Config{
		Server:   strings.TrimSpace(viper.GetString(prefix + ".server")),
		Sender:   strings.TrimSpace(viper.GetString(prefix + ".sender")),
		Username: viper.GetString(prefix + ".username"),
		Password: viper.GetString(prefix + ".password"),
		Security: strings.ToLower(strings.TrimSpace(viper.GetString(prefix + ".security"))),
		Timeout:  viper.GetDuration(prefix + ".timeout"),
	}
	if cfg.Username == "" {
		if login := viper.GetString(prefix + ".login"); login != "" {
			cfg.Username, cfg.Password, _ = strings.Cut(login, ":")
		}
	}
	return cfg
}

// IsSet tells whether the configuration names a server and a sender.
func (c Config) IsSet() bool {
	return c.Server != "" && c.Sender != ""
}

// Validate verifies the configuration, so a bad one is reported once rather
// than at each mail.
func (c Config) Validate() error {
	if !c.IsSet() {
		return errors.New("the server and the sender must be set")
	}
	if _, _, err := net.SplitHostPort(c.Server); err != nil {
		return fmt.Errorf("server %q: %w", c.Server, err)
	}
	if _, err := mail.ParseAddress(c.Sender); err != nil {
		return fmt.Errorf("sender %q: %w", c.Sender, err)
	}
	switch c.Security {
	case "", SecurityAuto, SecurityStartTLS, SecurityTLS, SecurityNone:
	default:
		return fmt.Errorf("security %q: use auto, starttls, tls or none", c.Security)
	}
	return nil
}

// Send sends msg through the server of c.
func Send(ctx context.Context, c Config, msg Message) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	if len(msg.To) == 0 {
		return errors.New("mail: no recipient")
	}
	for _, to := range msg.To {
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("mail: recipient %q: %w", to, err)
		}
	}
	data, err := Compose(c.Sender, msg, time.Now())
	if err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return send(ctx, c, msg.To, data)
}

func send(ctx context.Context, c Config, to []string, data []byte) error {
	host, _, _ := net.SplitHostPort(c.Server)
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if c.Security == SecurityTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", c.Server)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", c.Server)
	}
	if err != nil {
		return fmt.Errorf("mail: connect to %s: %w", c.Server, err)
	}
	// The deadline of the context bounds every exchange with the server.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: %s: %w", c.Server, err)
	}
	defer func() { _ = client.Close() }()

	switch c.Security {
	case SecurityTLS, SecurityNone:
	default:
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		} else if c.Security == SecurityStartTLS {
			return fmt.Errorf("mail: %s does not offer STARTTLS", c.Server)
		}
	}
	if c.Username != "" {
		// PlainAuth refuses to send the password over a connection neither
		// encrypted nor to the loopback.
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, host)); err != nil {
			return fmt.Errorf("mail: authenticate: %w", err)
		}
	}
	if err := client.Mail(c.Sender); err != nil {
		return fmt.Errorf("mail: sender: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("mail: recipient %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	return client.Quit()
}

// Compose returns the message as sent: the headers, then the text body, and
// the html one when msg has one, as the two parts of a multipart/alternative.
func Compose(sender string, msg Message, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	header := func(k, v string) {
		// No header injection through a value.
		v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
		b.WriteString(k + ": " + v + "\r\n")
	}
	header("From", sender)
	header("To", strings.Join(msg.To, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", messageID(sender))
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")

	if msg.HTML == "" {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, msg.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	header("Content-Type", `multipart/alternative; boundary="`+w.Boundary()+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, content string }{
		{"text/plain; charset=utf-8", msg.Text},
		{"text/html; charset=utf-8", msg.HTML},
	} {
		pw, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		if err := writeQP(pw, part.content); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	b.Write(body.Bytes())
	return b.Bytes(), nil
}

type writer interface {
	Write([]byte) (int, error)
}

func writeQP(w writer, s string) error {
	qp := quotedprintable.NewWriter(w)
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(s)); err != nil {
		return err
	}
	return qp.Close()
}

func messageID(sender string) string {
	domain := "localhost"
	if a, err := mail.ParseAddress(sender); err == nil {
		if _, d, ok := strings.Cut(a.Address, "@"); ok && d != "" {
			domain = d
		}
	}
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "<" + hex.EncodeToString(buf) + "@" + domain + ">"
}
