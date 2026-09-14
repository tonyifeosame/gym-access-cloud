package email

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBrevo is Brevo's transactional endpoint under test control: it records
// what it was sent and answers with whatever the test arranged.
type fakeBrevo struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []brevoCapture

	status int    // 0 means 201
	body   string // "" means a messageId body
	delay  time.Duration
}

type brevoCapture struct {
	Path        string
	APIKey      string
	ContentType string
	Body        map[string]any
}

func newFakeBrevo(t *testing.T) *fakeBrevo {
	t.Helper()
	f := &fakeBrevo{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests = append(f.requests, brevoCapture{
			Path: r.URL.Path, APIKey: r.Header.Get("api-key"),
			ContentType: r.Header.Get("Content-Type"), Body: body,
		})
		status, respBody, delay := f.status, f.body, f.delay
		f.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		if status == 0 {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"messageId":"<202609141300.12345@smtp-relay.mailin.fr>"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBrevo) sender(t *testing.T) *BrevoSender {
	t.Helper()
	from, _ := mail.ParseAddress("AccessLink <no-reply@example.com>")
	cfg := BrevoConfig{APIKey: "xkeysib-test-key", BaseURL: f.server.URL, From: from,
		HTTPClient: &http.Client{Timeout: 2 * time.Second}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return NewBrevo(cfg)
}

func (f *fakeBrevo) last(t *testing.T) brevoCapture {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("brevo received no request")
	}
	return f.requests[len(f.requests)-1]
}

func TestBrevoSenderPostsTheMessageWithTheAPIKey(t *testing.T) {
	brevo := newFakeBrevo(t)
	sender := brevo.sender(t)

	err := sender.Send(context.Background(), Message{
		To:      "ada@example.com",
		Subject: "Reset your AccessLink password",
		Text:    "Open this link: https://console.example/redeem?token=abc",
		HTML:    `<p><a href="https://console.example/redeem?token=abc">Choose a new password</a></p>`,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sender.Name() != "brevo_api" {
		t.Errorf("provider name = %q", sender.Name())
	}

	got := brevo.last(t)
	if got.Path != "/smtp/email" {
		t.Errorf("posted to %q, want /smtp/email", got.Path)
	}
	if got.APIKey != "xkeysib-test-key" {
		t.Errorf("api-key header = %q", got.APIKey)
	}
	if !strings.HasPrefix(got.ContentType, "application/json") {
		t.Errorf("content-type = %q", got.ContentType)
	}

	sender0 := got.Body["sender"].(map[string]any)
	if sender0["email"] != "no-reply@example.com" || sender0["name"] != "AccessLink" {
		t.Errorf("sender = %v", sender0)
	}
	to := got.Body["to"].([]any)
	if len(to) != 1 || to[0].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("to = %v", to)
	}
	if got.Body["subject"] != "Reset your AccessLink password" {
		t.Errorf("subject = %v", got.Body["subject"])
	}
	if !strings.Contains(got.Body["textContent"].(string), "redeem?token=abc") {
		t.Errorf("textContent lacks the link: %v", got.Body["textContent"])
	}
	if !strings.Contains(got.Body["htmlContent"].(string), "redeem?token=abc") {
		t.Errorf("htmlContent lacks the link: %v", got.Body["htmlContent"])
	}
}

func TestBrevoSenderOmitsHTMLWhenThereIsNone(t *testing.T) {
	brevo := newFakeBrevo(t)
	if err := brevo.sender(t).Send(context.Background(), Message{To: "ada@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, present := brevo.last(t).Body["htmlContent"]; present {
		t.Error("htmlContent was sent for a text-only message")
	}
}

func TestBrevoSenderReportsBrevoOwnReasonOnFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"bad key", 401, `{"code":"unauthorized","message":"Key not found"}`, "401 unauthorized: Key not found"},
		{"unverified sender", 400, `{"code":"invalid_parameter","message":"sender email not valid"}`, "400 invalid_parameter: sender email not valid"},
		{"rate limited", 429, `{"code":"too_many_requests","message":"daily limit reached"}`, "429 too_many_requests"},
		{"non-json failure", 502, `<html>bad gateway</html>`, "502: <html>bad gateway</html>"},
		{"empty failure", 500, ``, "500 Internal Server Error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			brevo := newFakeBrevo(t)
			brevo.status, brevo.body = tc.status, tc.body
			err := brevo.sender(t).Send(context.Background(), Message{To: "ada@example.com", Subject: "s", Text: "t"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestBrevoSenderHonoursTheDeadline(t *testing.T) {
	brevo := newFakeBrevo(t)
	brevo.delay = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := brevo.sender(t).Send(ctx, Message{To: "ada@example.com", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("a stalled endpoint was reported as delivered")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("send took %s; the context deadline was not honoured", time.Since(start))
	}
}

func TestBrevoSenderRefusesABadRecipientBeforeCalling(t *testing.T) {
	brevo := newFakeBrevo(t)
	err := brevo.sender(t).Send(context.Background(), Message{To: "Ada <ada@example.com>", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "not a bare email address") {
		t.Errorf("err = %v", err)
	}
	brevo.mu.Lock()
	defer brevo.mu.Unlock()
	if len(brevo.requests) != 0 {
		t.Error("brevo was called for a recipient the sender should have refused")
	}
}

func TestBrevoConfigurationIsValidated(t *testing.T) {
	from, _ := mail.ParseAddress("no-reply@example.com")
	cases := []struct {
		name string
		cfg  BrevoConfig
		want string
	}{
		{"no key", BrevoConfig{From: from}, EnvBrevoAPIKey},
		{"no from", BrevoConfig{APIKey: "k"}, EnvFrom},
		{"plain http off loopback", BrevoConfig{APIKey: "k", From: from, BaseURL: "http://api.example.com/v3"}, "must use https"},
		{"not a url", BrevoConfig{APIKey: "k", From: from, BaseURL: "v3"}, "absolute URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}

	ok := BrevoConfig{APIKey: "k", From: from}
	if err := ok.validate(); err != nil {
		t.Fatalf("a minimal configuration was refused: %v", err)
	}
	if ok.BaseURL != "https://api.brevo.com/v3" {
		t.Errorf("default base = %q", ok.BaseURL)
	}
	trailing := BrevoConfig{APIKey: "k", From: from, BaseURL: "https://api.brevo.com/v3/"}
	if err := trailing.validate(); err != nil || trailing.BaseURL != "https://api.brevo.com/v3" {
		t.Errorf("trailing slash not trimmed: %q (%v)", trailing.BaseURL, err)
	}
}

func TestFromEnvSelectsBrevo(t *testing.T) {
	for _, name := range []string{EnvProvider, EnvFrom, EnvBrevoAPIKey, EnvBrevoAPIURL} {
		t.Setenv(name, "")
	}
	t.Setenv(EnvProvider, "brevo_api")
	t.Setenv(EnvFrom, "AccessLink <no-reply@example.com>")

	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), EnvBrevoAPIKey) {
		t.Errorf("brevo_api without a key: err = %v, want one naming %s", err, EnvBrevoAPIKey)
	}

	t.Setenv(EnvBrevoAPIKey, "xkeysib-abc")
	sender, err := FromEnv()
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	brevo, ok := sender.(*BrevoSender)
	if !ok {
		t.Fatalf("sender is %T", sender)
	}
	if brevo.cfg.BaseURL != "https://api.brevo.com/v3" || brevo.cfg.APIKey != "xkeysib-abc" {
		t.Errorf("config = %+v", brevo.cfg)
	}
	if brevo.cfg.From.Name != "AccessLink" || brevo.cfg.From.Address != "no-reply@example.com" {
		t.Errorf("from = %v", brevo.cfg.From)
	}
}
