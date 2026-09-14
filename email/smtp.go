package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

// SMTP delivery, on the standard library.
//
// Three transport modes, because the three are what providers actually offer:
//
//	starttls   port 587, plaintext connection upgraded with STARTTLS. The
//	           common case, and the default.
//	tls        port 465, TLS from the first byte ("implicit TLS", SMTPS).
//	none       plaintext throughout. For a relay on localhost or a test
//	           harness, and refused for any other host.
//
// Authentication is PLAIN when a username is set, which every provider accepts
// over TLS. net/smtp refuses PLAIN on an unencrypted connection to a non-local
// host, which is the right refusal and is not overridden here.

// SMTP environment variables.
const (
	EnvSMTPHost     = "SMTP_HOST"
	EnvSMTPPort     = "SMTP_PORT"
	EnvSMTPUsername = "SMTP_USERNAME"
	EnvSMTPPassword = "SMTP_PASSWORD"
	// EnvSMTPTLS is starttls (default), tls or none.
	EnvSMTPTLS = "SMTP_TLS"
)

// SMTPConfig is what the SMTP sender needs.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// TLS is "starttls", "tls" or "none".
	TLS  string
	From *mail.Address
	// Timeout bounds the whole conversation. Unset means 20 seconds.
	Timeout time.Duration
}

func smtpConfigFromEnv(from *mail.Address) (SMTPConfig, error) {
	cfg := SMTPConfig{
		Host:     strings.TrimSpace(os.Getenv(EnvSMTPHost)),
		Username: os.Getenv(EnvSMTPUsername),
		Password: os.Getenv(EnvSMTPPassword),
		TLS:      strings.ToLower(strings.TrimSpace(os.Getenv(EnvSMTPTLS))),
		From:     from,
	}
	if cfg.Host == "" {
		return cfg, fmt.Errorf("%s is required for %s=smtp", EnvSMTPHost, EnvProvider)
	}
	if cfg.TLS == "" {
		cfg.TLS = "starttls"
	}

	rawPort := strings.TrimSpace(os.Getenv(EnvSMTPPort))
	if rawPort == "" {
		if cfg.TLS == "tls" {
			cfg.Port = 465
		} else {
			cfg.Port = 587
		}
	} else {
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return cfg, fmt.Errorf("%s must be a port number, got %q", EnvSMTPPort, rawPort)
		}
		cfg.Port = port
	}

	return cfg, cfg.validate()
}

func (c SMTPConfig) validate() error {
	switch c.TLS {
	case "starttls", "tls":
	case "none":
		host := c.Host
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("%s=none is only accepted for a loopback %s; %q would send "+
				"credentials and reset links in the clear", EnvSMTPTLS, EnvSMTPHost, host)
		}
	default:
		return fmt.Errorf("%s must be starttls, tls or none, got %q", EnvSMTPTLS, c.TLS)
	}
	if (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("%s and %s must be set together", EnvSMTPUsername, EnvSMTPPassword)
	}
	if c.From == nil {
		return fmt.Errorf("%s is required", EnvFrom)
	}
	return nil
}

// SMTPSender delivers over one configured relay.
type SMTPSender struct {
	cfg SMTPConfig
}

// NewSMTP builds the sender. It opens no connection: a relay that is down at
// startup is a delivery failure later, not a reason the API cannot boot.
func NewSMTP(cfg SMTPConfig) *SMTPSender {
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &SMTPSender{cfg: cfg}
}

func (s *SMTPSender) Name() string { return "smtp" }

// Send delivers one message, honouring the context's deadline for the whole
// conversation.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if err := ValidateRecipient(m.To); err != nil {
		return err
	}

	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	address := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	var (
		conn net.Conn
		err  error
	)
	if s.cfg.TLS == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: s.cfg.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("smtp: connecting to %s: %w", address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer client.Close()

	if s.cfg.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: %s does not offer STARTTLS", s.cfg.Host)
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: authenticating: %w", err)
		}
	}

	if err := client.Mail(s.cfg.From.Address); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if err := writeMIME(w, s.cfg.From, m); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp: writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: delivering: %w", err)
	}
	return client.Quit()
}

// writeMIME renders the message: text alone, or multipart/alternative with
// text first and HTML second, the order clients treat as "prefer the last".
func writeMIME(w io.Writer, from *mail.Address, m Message) error {
	headers := textproto.MIMEHeader{}
	headers.Set("From", from.String())
	headers.Set("To", m.To)
	headers.Set("Subject", encodeHeader(m.Subject))
	headers.Set("Date", time.Now().UTC().Format(time.RFC1123Z))
	headers.Set("MIME-Version", "1.0")
	// A reset email is a notification, not a conversation, and saying so keeps
	// out-of-office replies from bouncing back at the sender.
	headers.Set("Auto-Submitted", "auto-generated")

	if m.HTML == "" {
		headers.Set("Content-Type", "text/plain; charset=utf-8")
		headers.Set("Content-Transfer-Encoding", "8bit")
		if err := writeHeaders(w, headers); err != nil {
			return err
		}
		_, err := io.WriteString(w, dotStuff(m.Text))
		return err
	}

	mp := multipart.NewWriter(w)
	headers.Set("Content-Type", `multipart/alternative; boundary="`+mp.Boundary()+`"`)
	if err := writeHeaders(w, headers); err != nil {
		return err
	}

	for _, part := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", m.Text},
		{"text/html; charset=utf-8", m.HTML},
	} {
		ph := textproto.MIMEHeader{}
		ph.Set("Content-Type", part.ctype)
		ph.Set("Content-Transfer-Encoding", "8bit")
		pw, err := mp.CreatePart(ph)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(pw, dotStuff(part.body)); err != nil {
			return err
		}
	}
	return mp.Close()
}

func writeHeaders(w io.Writer, headers textproto.MIMEHeader) error {
	for _, key := range []string{"From", "To", "Subject", "Date", "MIME-Version",
		"Auto-Submitted", "Content-Type", "Content-Transfer-Encoding"} {
		if value := headers.Get(key); value != "" {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", key, value); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// encodeHeader keeps a subject on one line and ASCII-safe. net/smtp's DATA
// writer already dot-stuffs the body; headers are ours to keep clean.
func encodeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 126 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

// dotStuff normalises line endings to CRLF. The DATA writer net/smtp returns
// performs the actual leading-dot escaping, so this only has to keep the
// message well-formed.
func dotStuff(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return strings.ReplaceAll(body, "\n", "\r\n")
}
