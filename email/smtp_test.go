package email

import (
	"bufio"
	"context"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// A minimal SMTP server: enough of RFC 5321 to accept one message over a
// plaintext connection and hand its DATA back to the test. It is not a relay,
// it does not implement STARTTLS, and it exists so the sender's conversation
// -- greeting, envelope, DATA, QUIT -- is exercised against a real socket
// rather than a mocked writer.
type fakeSMTP struct {
	listener net.Listener
	mu       sync.Mutex
	from     string
	to       []string
	data     string
	authed   string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	s := &fakeSMTP{listener: listener}
	go s.serve()
	t.Cleanup(func() { listener.Close() })
	return s
}

func (s *fakeSMTP) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}
	say("220 fake.test ESMTP")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			say("250-fake.test")
			say("250-AUTH PLAIN")
			say("250 8BITMIME")
		case strings.HasPrefix(verb, "HELO"):
			say("250 fake.test")
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			s.mu.Lock()
			s.authed = strings.TrimSpace(line[len("AUTH PLAIN"):])
			s.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			s.mu.Lock()
			// Strip any ESMTP parameters (BODY=8BITMIME) after the path.
			path := strings.Fields(line[len("MAIL FROM:"):])[0]
			s.from = strings.Trim(path, "<> ")
			s.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			s.mu.Lock()
			s.to = append(s.to, strings.Trim(line[len("RCPT TO:"):], "<> "))
			s.mu.Unlock()
			say("250 ok")
		case verb == "DATA":
			say("354 go ahead")
			var body strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" {
					break
				}
				body.WriteString(dl)
			}
			s.mu.Lock()
			s.data = body.String()
			s.mu.Unlock()
			say("250 queued")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("500 unknown")
		}
	}
}

func (s *fakeSMTP) received() (from string, to []string, data string, authed string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.from, s.to, s.data, s.authed
}

func TestSMTPSenderDeliversATextAndHTMLMessage(t *testing.T) {
	server := newFakeSMTP(t)
	from, _ := mail.ParseAddress("AccessLink <no-reply@example.com>")
	sender := NewSMTP(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), TLS: "none", From: from,
		Username: "user", Password: "secret", Timeout: 5 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := sender.Send(ctx, Message{
		To:      "ada@example.com",
		Subject: "Reset your AccessLink password",
		Text:    "Open this link:\nhttps://console.example/redeem?token=abc\n.leading dot line\n",
		HTML:    "<p>Open <a href=\"https://console.example/redeem?token=abc\">this link</a></p>",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	envFrom, to, data, authed := server.received()
	if envFrom != "no-reply@example.com" {
		t.Errorf("MAIL FROM = %q", envFrom)
	}
	if len(to) != 1 || to[0] != "ada@example.com" {
		t.Errorf("RCPT TO = %v", to)
	}
	if authed == "" {
		t.Error("no AUTH PLAIN was sent despite a username being configured")
	}
	for _, want := range []string{
		"From: \"AccessLink\" <no-reply@example.com>",
		"To: ada@example.com",
		"Subject: Reset your AccessLink password",
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative;",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Type: text/html; charset=utf-8",
		"https://console.example/redeem?token=abc",
		"<a href=\"https://console.example/redeem?token=abc\">",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message lacks %q:\n%s", want, data)
		}
	}
	// A line starting with a dot must reach the server dot-stuffed and be
	// un-stuffed on receipt -- net/smtp's writer does it; the assertion is that
	// the message was not truncated at it.
	if !strings.Contains(data, ".leading dot line") {
		t.Errorf("the dot-leading line was lost:\n%s", data)
	}
}

func TestSMTPSenderRefusesABadRecipientBeforeConnecting(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@example.com")
	// A port nothing listens on: if Send tried to connect it would fail with a
	// different error.
	sender := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: 1, TLS: "none", From: from})
	err := sender.Send(context.Background(), Message{To: "Ada <ada@example.com>", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "not a bare email address") {
		t.Errorf("err = %v, want a recipient validation error", err)
	}
}

func TestSMTPConfigurationIsValidated(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@example.com")

	cases := []struct {
		name string
		cfg  SMTPConfig
		want string
	}{
		{"plaintext to a public host", SMTPConfig{Host: "smtp.example.com", Port: 25, TLS: "none", From: from},
			"only accepted for a loopback"},
		{"unknown tls mode", SMTPConfig{Host: "smtp.example.com", Port: 587, TLS: "ssl", From: from},
			"must be starttls, tls or none"},
		{"username without password", SMTPConfig{Host: "smtp.example.com", Port: 587, TLS: "starttls",
			Username: "u", From: from}, "must be set together"},
		{"no from", SMTPConfig{Host: "smtp.example.com", Port: 587, TLS: "starttls"}, EnvFrom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}

	ok := SMTPConfig{Host: "smtp.example.com", Port: 587, TLS: "starttls", From: from,
		Username: "u", Password: "p"}
	if err := ok.validate(); err != nil {
		t.Errorf("a complete configuration was refused: %v", err)
	}
}

func TestFromEnvSelectsAProviderOrNothing(t *testing.T) {
	clear := func(t *testing.T) {
		for _, name := range []string{EnvProvider, EnvFrom, EnvSMTPHost, EnvSMTPPort,
			EnvSMTPUsername, EnvSMTPPassword, EnvSMTPTLS} {
			t.Setenv(name, "")
		}
	}

	t.Run("unset is off", func(t *testing.T) {
		clear(t)
		if _, err := FromEnv(); err != ErrNotConfigured {
			t.Errorf("err = %v, want ErrNotConfigured", err)
		}
	})

	t.Run("a provider without a from address is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "log")
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), EnvFrom) {
			t.Errorf("err = %v, want one naming %s", err, EnvFrom)
		}
	})

	t.Run("an unknown provider is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "carrier-pigeon")
		t.Setenv(EnvFrom, "no-reply@example.com")
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "not a known provider") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("smtp without a host is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "smtp")
		t.Setenv(EnvFrom, "no-reply@example.com")
		if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), EnvSMTPHost) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("smtp defaults to starttls on 587", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "smtp")
		t.Setenv(EnvFrom, "no-reply@example.com")
		t.Setenv(EnvSMTPHost, "smtp.example.com")
		sender, err := FromEnv()
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		smtpSender, ok := sender.(*SMTPSender)
		if !ok {
			t.Fatalf("sender is %T", sender)
		}
		if smtpSender.cfg.Port != 587 || smtpSender.cfg.TLS != "starttls" {
			t.Errorf("defaults = port %d tls %s", smtpSender.cfg.Port, smtpSender.cfg.TLS)
		}
	})

	t.Run("smtp with implicit tls defaults to 465", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "smtp")
		t.Setenv(EnvFrom, "no-reply@example.com")
		t.Setenv(EnvSMTPHost, "smtp.example.com")
		t.Setenv(EnvSMTPTLS, "tls")
		sender, err := FromEnv()
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if sender.(*SMTPSender).cfg.Port != 465 {
			t.Errorf("port = %d, want 465", sender.(*SMTPSender).cfg.Port)
		}
	})

	t.Run("log provider", func(t *testing.T) {
		clear(t)
		t.Setenv(EnvProvider, "log")
		t.Setenv(EnvFrom, "AccessLink <no-reply@example.com>")
		sender, err := FromEnv()
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if sender.Name() != "log" {
			t.Errorf("provider = %s", sender.Name())
		}
	})
}
