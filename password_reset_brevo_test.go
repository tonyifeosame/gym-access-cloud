package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/mail"
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

// Forgot password delivered through Brevo's HTTPS API, end to end.
//
// password_reset_delivery_test.go proves the handler's behaviour against a
// capturing sender. This file proves the same behaviour holds with the REAL
// Brevo sender talking to a fake Brevo endpoint: the 202 is unchanged and
// immediate, the request Brevo receives carries the link, the log names the
// outcome under the same request id the caller was given, and a Brevo refusal
// changes nothing the caller can observe.

// fakeBrevoAPI is Brevo's endpoint on httptest, recording bodies.
type fakeBrevoAPI struct {
	server   *httptest.Server
	mu       sync.Mutex
	received chan map[string]any
	status   int
	body     string
}

func newFakeBrevoAPI(t *testing.T) *fakeBrevoAPI {
	t.Helper()
	f := &fakeBrevoAPI{received: make(chan map[string]any, 8)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_api_key"] = r.Header.Get("api-key")
		body["_path"] = r.URL.Path
		f.mu.Lock()
		status, respBody := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status == 0 {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"messageId":"<test@brevo>"}`))
		} else {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(respBody))
		}
		f.received <- body
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBrevoAPI) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case body := <-f.received:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("brevo received nothing within 5s")
		return nil
	}
}

func (f *fakeBrevoAPI) none(t *testing.T) {
	t.Helper()
	select {
	case body := <-f.received:
		t.Fatalf("brevo received a message for %v when none should have been sent", body["to"])
	case <-time.After(300 * time.Millisecond):
	}
}

type brevoResetFixture struct {
	env   *testEnv
	brevo *fakeBrevoAPI
	logs  *bytes.Buffer
}

func newBrevoResetFixture(t *testing.T) *brevoResetFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	brevo := newFakeBrevoAPI(t)

	from, _ := mail.ParseAddress("AccessLink <no-reply@example.com>")
	sender := email.NewBrevo(email.BrevoConfig{
		APIKey: "xkeysib-test", BaseURL: brevo.server.URL, From: from,
		HTTPClient: brevo.server.Client(),
	})
	handlers.ConfigurePasswordResetDelivery(sender)
	if err := handlers.ConfigureConsoleURL("http://localhost:5173"); err != nil {
		t.Fatalf("console url: %v", err)
	}

	logs := &bytes.Buffer{}
	previous := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() {
		handlers.ConfigurePasswordResetDelivery(nil)
		_ = handlers.ConfigureConsoleURL("")
		log.SetOutput(previous)
	})
	return &brevoResetFixture{env: env, brevo: brevo, logs: logs}
}

// request submits forgot-password and returns the code, body, elapsed time
// and the request id the API echoed.
func (f *brevoResetFixture) request(t *testing.T, address string) (int, map[string]any, time.Duration, string) {
	t.Helper()
	start := time.Now()
	code, body, res := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/forgot-password",
		body: jsonBody(t, map[string]string{"email": address}),
	})
	return code, body, time.Since(start), res.Header.Get("X-Request-ID")
}

// waitForLog polls the captured log for a substring.
func (f *brevoResetFixture) waitForLog(t *testing.T, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.logs.String(), needle) {
			return f.logs.String()
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log never contained %q:\n%s", needle, f.logs.String())
	return ""
}

var brevoLinkPattern = regexp.MustCompile(`http://localhost:5173/redeem\?token=([A-Za-z0-9_%.-]+)`)

func TestForgotPasswordThroughBrevoDeliversARedeemableLink(t *testing.T) {
	f := newBrevoResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleManager)

	code, body, _, requestID := f.request(t, "ada@example.com")
	if code != http.StatusAccepted || body["status"] != "accepted" {
		t.Fatalf("forgot-password = %d (%v)", code, body)
	}
	if requestID == "" {
		t.Fatal("no X-Request-ID on the response")
	}

	// What Brevo received: the right endpoint, the key, the sender, the
	// recipient, and the link -- in both parts.
	posted := f.brevo.wait(t)
	if posted["_path"] != "/smtp/email" || posted["_api_key"] != "xkeysib-test" {
		t.Errorf("posted to %v with key %v", posted["_path"], posted["_api_key"])
	}
	if to := posted["to"].([]any); len(to) != 1 || to[0].(map[string]any)["email"] != "ada@example.com" {
		t.Errorf("to = %v", posted["to"])
	}
	if sender := posted["sender"].(map[string]any); sender["email"] != "no-reply@example.com" {
		t.Errorf("sender = %v", sender)
	}
	text, _ := posted["textContent"].(string)
	html, _ := posted["htmlContent"].(string)
	match := brevoLinkPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("no reset link in textContent:\n%s", text)
	}
	if !strings.Contains(html, "redeem?token=") {
		t.Error("htmlContent carries no link")
	}
	token, _ := url.QueryUnescape(match[1])

	// THE SAME LOG LINE, THE SAME REQUEST ID as the caller was given.
	logged := f.waitForLog(t, "auth=reset-delivered")
	if !strings.Contains(logged, "request_id="+requestID+" auth=reset-delivered provider=brevo_api email=a***@example.com") {
		t.Errorf("delivered line does not carry the request id and provider:\n%s", logged)
	}
	if strings.Contains(logged, token) {
		t.Error("the token was written to the log")
	}

	// The link works once, and sets the password it was redeemed with.
	redeem := func(password string) int {
		code, _, _ := doAuth(t, f.env.router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/redeem",
			body: jsonBody(t, map[string]string{"token": token, "new_password": password}),
		})
		return code
	}
	if code := redeem("a-brand-new-password-123"); code != http.StatusNoContent {
		t.Fatalf("redeem = %d, want 204", code)
	}
	if code := redeem("another-password-456"); code != http.StatusConflict {
		t.Errorf("second redeem = %d, want 409", code)
	}
	if code, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ada@example.com", "a-brand-new-password-123"),
	}); code != http.StatusOK {
		t.Errorf("login with the new password = %d", code)
	}
}

func TestForgotPasswordThroughBrevoIsAsynchronousAndUniform(t *testing.T) {
	f := newBrevoResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)

	// Brevo is slow. The 202 must not wait for it.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"messageId":"<slow>"}`))
	}))
	defer slow.Close()
	from, _ := mail.ParseAddress("no-reply@example.com")
	handlers.ConfigurePasswordResetDelivery(email.NewBrevo(email.BrevoConfig{
		APIKey: "k", BaseURL: slow.URL, From: from, HTTPClient: slow.Client(),
	}))

	knownCode, knownBody, knownTook, _ := f.request(t, "ada@example.com")
	unknownCode, unknownBody, _, _ := f.request(t, "nobody@example.com")

	if knownCode != http.StatusAccepted || unknownCode != http.StatusAccepted {
		t.Fatalf("codes = %d / %d", knownCode, unknownCode)
	}
	if knownBody["message"] != unknownBody["message"] {
		t.Error("known and unknown addresses answered differently")
	}
	if knownTook > time.Second {
		t.Errorf("the known-address request took %s: it waited for Brevo", knownTook)
	}
	f.waitForLog(t, "auth=reset-delivered provider=brevo_api")
}

func TestForgotPasswordThroughBrevoSurvivesARefusal(t *testing.T) {
	f := newBrevoResetFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)

	f.brevo.mu.Lock()
	f.brevo.status, f.brevo.body = 401, `{"code":"unauthorized","message":"Key not found"}`
	f.brevo.mu.Unlock()

	code, body, _, requestID := f.request(t, "ada@example.com")
	if code != http.StatusAccepted || body["status"] != "accepted" {
		t.Fatalf("forgot-password with brevo refusing = %d (%v)", code, body)
	}
	f.brevo.wait(t)

	logged := f.waitForLog(t, "auth=reset-delivery-failed")
	line := ""
	for _, l := range strings.Split(logged, "\n") {
		if strings.Contains(l, "reset-delivery-failed") {
			line = l
		}
	}
	for _, want := range []string{"request_id=" + requestID, "provider=brevo_api", "email=a***@example.com",
		"401 unauthorized: Key not found"} {
		if !strings.Contains(line, want) {
			t.Errorf("failure line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "ada@example.com") {
		t.Error("the recipient was logged in full")
	}

	// The token exists and is still redeemable: a delivery failure did not
	// consume or cancel it, so a retry after the relay is fixed can succeed.
	if n := queryInt(t, `SELECT count(*) FROM user_credential_tokens WHERE purpose = 'RESET' AND redeemed_at IS NULL`); n != 1 {
		t.Errorf("live reset tokens after a delivery failure = %d, want 1", n)
	}

	// And an unknown address still produces no call to Brevo at all.
	f.request(t, "nobody@example.com")
	f.brevo.none(t)
}
