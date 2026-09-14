package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"access-terminal-cloud-api/email"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/models"
)

// Forgot password, end to end, with delivery configured.
//
// The pre-delivery behaviour -- 202 either way, token only in the log -- is
// covered in credential_handover_test.go and is unchanged. This file covers
// what changes once EMAIL_PROVIDER is set: the link goes to the address that
// asked, the token leaves the log, and everything a stranger could observe
// stays identical for an address that exists and one that does not.

// capturingSender records what would have been sent.
type capturingSender struct {
	mu        sync.Mutex
	messages  []email.Message
	delivered chan email.Message
	fail      error
}

func newCapturingSender() *capturingSender {
	return &capturingSender{delivered: make(chan email.Message, 8)}
}

func (s *capturingSender) Name() string { return "capture" }

func (s *capturingSender) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.messages = append(s.messages, m)
	s.delivered <- m
	return nil
}

// wait returns the next delivered message, or fails the test.
func (s *capturingSender) wait(t *testing.T) email.Message {
	t.Helper()
	select {
	case m := <-s.delivered:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no reset email was delivered within 5s")
		return email.Message{}
	}
}

// none asserts nothing is delivered in a short window.
func (s *capturingSender) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-s.delivered:
		t.Fatalf("an email was delivered to %s when none should have been", m.To)
	case <-time.After(300 * time.Millisecond):
	}
}

type resetFixture struct {
	env    *testEnv
	sender *capturingSender
	logs   *bytes.Buffer
}

func newResetFixture(t *testing.T) *resetFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	sender := newCapturingSender()

	handlers.ConfigurePasswordResetDelivery(sender)
	if err := handlers.ConfigureConsoleURL("http://localhost:5173"); err != nil {
		t.Fatalf("console url: %v", err)
	}

	// The log is captured so the test can assert the token is NOT in it once
	// delivery exists.
	logs := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(logs)

	t.Cleanup(func() {
		handlers.ConfigurePasswordResetDelivery(nil)
		_ = handlers.ConfigureConsoleURL("")
		log.SetOutput(previous)
	})
	return &resetFixture{env: env, sender: sender, logs: logs}
}

func (f *resetFixture) request(t *testing.T, address string) (int, map[string]any) {
	t.Helper()
	code, body, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/forgot-password",
		body: jsonBody(t, map[string]string{"email": address}),
	})
	return code, body
}

var resetLinkPattern = regexp.MustCompile(`http://localhost:5173/redeem\?token=([A-Za-z0-9_%.-]+)`)

// tokenFrom extracts the redeemable token from the email's text body.
func tokenFrom(t *testing.T, m email.Message) string {
	t.Helper()
	match := resetLinkPattern.FindStringSubmatch(m.Text)
	if match == nil {
		t.Fatalf("no reset link in the email:\n%s", m.Text)
	}
	token, err := url.QueryUnescape(match[1])
	if err != nil {
		t.Fatalf("unescaping token: %v", err)
	}
	return token
}

func (f *resetFixture) redeem(t *testing.T, token, password string) int {
	t.Helper()
	code, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{"token": token, "new_password": password}),
	})
	return code
}

func TestForgotPasswordEmailsASingleUseLinkThatSetsThePassword(t *testing.T) {
	f := newResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleManager)

	code, body := f.request(t, "Ada@Example.com")
	if code != http.StatusAccepted {
		t.Fatalf("forgot-password = %d (%v)", code, body)
	}

	m := f.sender.wait(t)
	if m.To != "ada@example.com" {
		t.Errorf("sent to %q, want the normalised account address", m.To)
	}
	if !strings.Contains(m.Subject, "password") {
		t.Errorf("subject = %q", m.Subject)
	}
	if !strings.Contains(m.Text, "works once") || !strings.Contains(m.Text, "expires in 1 hour") {
		t.Errorf("the email does not say the link is single-use and how long it lasts:\n%s", m.Text)
	}
	if m.HTML == "" || !strings.Contains(m.HTML, "redeem?token=") {
		t.Error("no HTML alternative carrying the link")
	}
	if strings.Contains(m.Text, "Company One") || strings.Contains(m.Text, "MANAGER") {
		t.Error("the email names the company or role; it should say nothing about the account")
	}

	token := tokenFrom(t, m)
	if !strings.HasPrefix(token, "ali_") {
		t.Errorf("token %q does not have the credential-token prefix", token)
	}

	// THE TOKEN IS NOT IN THE LOG once delivery exists.
	if strings.Contains(f.logs.String(), token) {
		t.Error("the reset token was written to the log despite email delivery being configured")
	}
	if !strings.Contains(f.logs.String(), "delivery=queued") {
		t.Errorf("the log does not record that delivery was queued:\n%s", f.logs.String())
	}

	// The link works exactly once.
	if code := f.redeem(t, token, "a-brand-new-password-123"); code != http.StatusNoContent {
		t.Fatalf("redeem = %d, want 204", code)
	}
	if code := f.redeem(t, token, "another-password-456"); code != http.StatusConflict {
		t.Errorf("second redeem = %d, want 409 (single use)", code)
	}

	// And the password it set is the one that signs in now.
	if code, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ada@example.com", "a-brand-new-password-123"),
	}); code != http.StatusOK {
		t.Errorf("login with the new password = %d", code)
	}
	if code, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ada@example.com", testPassword),
	}); code != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", code)
	}
}

func TestForgotPasswordLinkExpires(t *testing.T) {
	f := newResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleManager)

	f.request(t, "ada@example.com")
	token := tokenFrom(t, f.sender.wait(t))

	// Time passes. Moving the deadline is faster than waiting for it and
	// exercises exactly the predicate the store checks.
	mustExec(t, `UPDATE user_credential_tokens
	   SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'`)

	if code := f.redeem(t, token, "a-brand-new-password-123"); code != http.StatusGone {
		t.Errorf("redeem after expiry = %d, want 410", code)
	}
	if code, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ada@example.com", testPassword),
	}); code != http.StatusOK {
		t.Errorf("the original password stopped working (%d) although the reset was never redeemed", code)
	}
}

func TestForgotPasswordDiscloseNothingAboutWhichAddressesExist(t *testing.T) {
	f := newResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "known@example.com", models.RoleViewer)
	disabled := mustCreateOperator(t, one, "disabled@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET active = FALSE WHERE id = $1`, disabled.ID)

	knownCode, knownBody := f.request(t, "known@example.com")
	f.sender.wait(t)

	unknownCode, unknownBody := f.request(t, "nobody@example.com")
	f.sender.none(t)

	disabledCode, disabledBody := f.request(t, "disabled@example.com")
	f.sender.none(t)

	for name, got := range map[string]int{"known": knownCode, "unknown": unknownCode, "disabled": disabledCode} {
		if got != http.StatusAccepted {
			t.Errorf("%s address answered %d, want 202", name, got)
		}
	}
	for name, body := range map[string]map[string]any{"unknown": unknownBody, "disabled": disabledBody} {
		if body["message"] != knownBody["message"] || body["status"] != knownBody["status"] {
			t.Errorf("%s address answered a different body: %v vs %v", name, body, knownBody)
		}
	}
	if n := queryInt(t, `SELECT count(*) FROM user_credential_tokens`); n != 1 {
		t.Errorf("%d tokens were minted, want exactly one (for the known, active address)", n)
	}
}

func TestForgotPasswordAnswersTheSameWhenDeliveryFails(t *testing.T) {
	f := newResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)

	_, okBody := f.request(t, "someone-else@example.com")

	f.sender.mu.Lock()
	f.sender.fail = errors.New("relay refused the connection")
	f.sender.mu.Unlock()

	code, body := f.request(t, "ada@example.com")
	if code != http.StatusAccepted {
		t.Fatalf("forgot-password with a failing relay = %d", code)
	}
	if body["message"] != okBody["message"] {
		t.Error("a delivery failure changed the response body")
	}
	f.sender.none(t)

	// The failure is in the log, correlated by request id, and redacted.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(f.logs.String(), "reset-delivery-failed") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	logged := f.logs.String()
	if !strings.Contains(logged, "reset-delivery-failed") {
		t.Errorf("delivery failure was not logged:\n%s", logged)
	}
	if strings.Contains(logged, "ada@example.com") {
		t.Error("the recipient address was logged in full")
	}
}

func TestProvidersReportsWhetherResetsAreEmailed(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	handlers.ConfigurePasswordResetDelivery(nil)
	_, body, _ := doAuth(t, env.router, authCall{method: http.MethodGet, path: "/api/v1/auth/providers"})
	if body["password_reset"].(map[string]any)["email_delivery"] != false {
		t.Error("providers claims email delivery with no sender configured")
	}

	f := newResetFixture(t)
	_, body, _ = doAuth(t, f.env.router, authCall{method: http.MethodGet, path: "/api/v1/auth/providers"})
	if body["password_reset"].(map[string]any)["email_delivery"] != true {
		t.Error("providers does not report email delivery once configured")
	}
}

func TestEmailDeliveryWithoutAConsoleURLIsRefusedAtStartup(t *testing.T) {
	for _, name := range []string{email.EnvProvider, email.EnvFrom, handlers.EnvConsoleURL,
		"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_REDIRECT_URL"} {
		t.Setenv(name, "")
	}
	t.Cleanup(func() {
		handlers.ConfigurePasswordResetDelivery(nil)
		_ = handlers.ConfigureConsoleURL("")
	})

	t.Setenv(email.EnvProvider, "log")
	t.Setenv(email.EnvFrom, "no-reply@example.com")
	err := configureIdentityProviders()
	if err == nil || !strings.Contains(err.Error(), handlers.EnvConsoleURL) {
		t.Errorf("startup = %v, want a refusal naming %s", err, handlers.EnvConsoleURL)
	}

	t.Setenv(handlers.EnvConsoleURL, "https://console.example.com/")
	if err := configureIdentityProviders(); err != nil {
		t.Fatalf("startup with a console url = %v", err)
	}
	if !handlers.PasswordResetDeliveryEnabled() {
		t.Error("delivery not enabled after configuration")
	}
	if handlers.ConsoleURL() != "https://console.example.com" {
		t.Errorf("console url = %q, want the trailing slash trimmed", handlers.ConsoleURL())
	}
}
