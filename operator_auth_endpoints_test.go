package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator authentication endpoints (handlers/auth.go, /api/v1/auth/*).
//
// These go through NewRouter, so the middleware chain, the route table and the
// handlers are exercised exactly as the server runs them.

type authCall struct {
	method      string
	path        string
	body        string
	token       string // session cookie value
	cookieName  string // defaults to the configured session cookie
	csrf        string
	contentType string // defaults to application/json
}

func doAuth(t *testing.T, router *gin.Engine, call authCall) (int, map[string]any, *http.Response) {
	t.Helper()

	var body *strings.Reader
	if call.body != "" {
		body = strings.NewReader(call.body)
	} else {
		body = strings.NewReader("")
	}

	req := httptest.NewRequest(call.method, call.path, body)

	contentType := call.contentType
	if contentType == "" {
		contentType = "application/json"
	}
	if contentType != "none" {
		req.Header.Set("Content-Type", contentType)
	}
	if call.token != "" {
		name := call.cookieName
		if name == "" {
			name, _ = middleware.SessionCookieConfig()
		}
		req.AddCookie(&http.Cookie{Name: name, Value: call.token})
	}
	if call.csrf != "" {
		req.Header.Set(middleware.CSRFHeader, call.csrf)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	decoded := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &decoded)
	}
	return w.Code, decoded, w.Result()
}

func loginBody(email, password string) string {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(payload)
}

// login performs a successful login and returns the session token and CSRF token
func login(t *testing.T, router *gin.Engine, email, password string) (string, string) {
	t.Helper()

	code, body, res := doAuth(t, router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody(email, password),
	})
	if code != http.StatusOK {
		t.Fatalf("login as %s = %d (%v)", email, code, body)
	}

	var token string
	name, _ := middleware.SessionCookieConfig()
	for _, cookie := range res.Cookies() {
		if cookie.Name == name {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatalf("login set no %s cookie", name)
	}

	csrf, _ := body["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("login returned no csrf_token")
	}
	return token, csrf
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestAuthLoginSucceedsAndSetsTheSessionCookie(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "login@example.com", models.RoleManager)
	if err := database.ReplaceSiteGrants(one, user.ID,
		[]string{operatorSitePublicID(t, "Site A")}); err != nil {
		t.Fatalf("granting a site: %v", err)
	}

	code, body, res := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("login@example.com", testPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d (%v)", code, body)
	}

	// The response is the extensible object shape, not a bare token.
	for _, field := range []string{"operator", "company", "role", "sites", "all_sites",
		"csrf_token", "session_expires_at", "session_expires_in_seconds"} {
		if _, present := body[field]; !present {
			t.Errorf("login response is missing %q", field)
		}
	}

	operator := body["operator"].(map[string]any)
	if operator["email"] != "login@example.com" || operator["role"] != models.RoleManager {
		t.Errorf("operator = %v", operator)
	}
	if operator["id"] != user.PublicID {
		t.Errorf("operator id = %v, want the public id %s", operator["id"], user.PublicID)
	}
	company := body["company"].(map[string]any)
	if company["slug"] != "one" || company["name"] != "Company One" {
		t.Errorf("company = %v", company)
	}
	sites := body["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites = %v, want the one granted site", sites)
	}
	if body["all_sites"] != false {
		t.Error("a MANAGER scoped to one site should not be marked all_sites")
	}

	// A password must never come back, and neither must the session token: the
	// token belongs in the cookie and nowhere else.
	raw := mustJSON(t, body)
	if strings.Contains(raw, testPassword) || strings.Contains(raw, "password") {
		t.Errorf("the login response mentions a password: %s", raw)
	}
	if strings.Contains(raw, "ats_") {
		t.Error("the session token appears in the response body")
	}

	// Cookie attributes. These are not cosmetic: the __Host- prefix is only
	// honoured by a browser when all three of Secure, Path=/ and no Domain hold.
	name, _ := middleware.SessionCookieConfig()
	if name != "__Host-al_session" {
		t.Fatalf("default cookie name = %q, want __Host-al_session", name)
	}
	var cookie *http.Cookie
	for _, candidate := range res.Cookies() {
		if candidate.Name == name {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatalf("no %s cookie was set (Set-Cookie: %q)", name, res.Header.Get("Set-Cookie"))
	}
	switch {
	case !strings.HasPrefix(cookie.Value, "ats_"):
		t.Errorf("cookie value %q is not a session token", cookie.Value)
	case !cookie.HttpOnly:
		t.Error("the session cookie is not HttpOnly")
	case !cookie.Secure:
		t.Error("the session cookie is not Secure")
	case cookie.SameSite != http.SameSiteLaxMode:
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	case cookie.Path != "/":
		t.Errorf("Path = %q, want /", cookie.Path)
	case cookie.Domain != "":
		t.Errorf("Domain = %q, want it absent for a __Host- cookie", cookie.Domain)
	case cookie.MaxAge != 0:
		t.Errorf("Max-Age = %d, want none: the server-side row is the lifetime", cookie.MaxAge)
	}

	// The expiry is a real remaining duration, not a wall-clock value forwarded
	// from a database in another time zone.
	expiresIn := int(body["session_expires_in_seconds"].(float64))
	wantSeconds := int(database.SessionAbsoluteTimeout().Seconds())
	if expiresIn < wantSeconds-60 || expiresIn > wantSeconds {
		t.Errorf("session_expires_in_seconds = %d, want about %d", expiresIn, wantSeconds)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, body["session_expires_at"].(string))
	if err != nil {
		t.Fatalf("session_expires_at is not RFC3339: %v", err)
	}
	if skew := time.Until(expiresAt) - time.Duration(expiresIn)*time.Second; skew > time.Minute || skew < -time.Minute {
		t.Errorf("session_expires_at is %v away from the reported duration", skew)
	}
}

func TestAuthLoginRejections(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	mustCreateOperator(t, one, "reject@example.com", models.RoleViewer)

	disabled := mustCreateOperator(t, one, "off@example.com", models.RoleViewer)
	if err := database.SetUserActive(one, disabled.ID, false); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	mustCreateOperator(t, two, "offcompany@example.com", models.RoleViewer)
	mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, two)

	// Everything the caller is not entitled to tell apart answers identically.
	sameAnswer := []struct{ name, body string }{
		{"wrong password", loginBody("reject@example.com", "not-the-password")},
		{"unknown address", loginBody("nobody@example.com", testPassword)},
		{"malformed address", loginBody("not-an-address", testPassword)},
		{"disabled operator", loginBody("off@example.com", testPassword)},
		{"disabled company", loginBody("offcompany@example.com", testPassword)},
	}
	for _, tc := range sameAnswer {
		t.Run(tc.name, func(t *testing.T) {
			code, body, res := doAuth(t, env.router, authCall{
				method: http.MethodPost, path: "/api/v1/auth/login", body: tc.body,
			})
			if code != http.StatusUnauthorized {
				t.Fatalf("%s = %d (%v)", tc.name, code, body)
			}
			if body["error"] != "Invalid email or password" {
				t.Errorf("%s error = %v, want the uniform message", tc.name, body["error"])
			}
			if len(res.Cookies()) != 0 {
				t.Error("a failed login set a cookie")
			}
		})
	}

	// Shape problems are 4xx of their own kind, and none of them authenticate.
	malformed := []struct {
		name        string
		body        string
		contentType string
		want        int
	}{
		{"missing password", `{"email":"reject@example.com"}`, "", http.StatusBadRequest},
		{"missing email", `{"password":"` + testPassword + `"}`, "", http.StatusBadRequest},
		{"empty body", ``, "", http.StatusBadRequest},
		{"not json", `email=x&password=y`, "application/x-www-form-urlencoded",
			http.StatusUnsupportedMediaType},
		{"no content type", `{}`, "none", http.StatusUnsupportedMediaType},
	}
	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			code, body, _ := doAuth(t, env.router, authCall{
				method: http.MethodPost, path: "/api/v1/auth/login",
				body: tc.body, contentType: tc.contentType,
			})
			if code != tc.want {
				t.Errorf("%s = %d, want %d (%v)", tc.name, code, tc.want, body)
			}
		})
	}
}

func TestAuthLoginLockoutAndRateLimit(t *testing.T) {
	cheapBcrypt(t)
	// A high per-address allowance so the ACCOUNT lock is what this half
	// measures, not the address limiter sitting in front of it.
	t.Setenv("LOGIN_RATE_LIMIT_PER_MINUTE", "1000")
	newTestEnv(t)
	router := NewRouter()
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "lockme@example.com", models.RoleViewer)

	for attempt := 1; attempt <= 5; attempt++ {
		code, _, _ := doAuth(t, router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/login",
			body: loginBody("lockme@example.com", "wrong-password-here"),
		})
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", attempt, code)
		}
	}

	code, body, res := doAuth(t, router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("lockme@example.com", "wrong-password-here"),
	})
	if code != http.StatusTooManyRequests {
		t.Fatalf("attempt after the threshold = %d, want 429 (%v)", code, body)
	}
	retryAfter := res.Header.Get("Retry-After")
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retryAfter)
	}
	// Correct credentials do not bypass the lock.
	if code, _, _ := doAuth(t, router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("lockme@example.com", testPassword),
	}); code != http.StatusTooManyRequests {
		t.Errorf("correct password while locked = %d, want 429", code)
	}

	// Now the per-address limiter, with its own router and a small allowance.
	t.Setenv("LOGIN_RATE_LIMIT_PER_MINUTE", "2")
	limited := NewRouter()
	mustCreateOperator(t, one, "ratelimit@example.com", models.RoleViewer)

	for attempt := 1; attempt <= 2; attempt++ {
		if code, _, _ := doAuth(t, limited, authCall{
			method: http.MethodPost, path: "/api/v1/auth/login",
			body: loginBody("ratelimit@example.com", "wrong-password-here"),
		}); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d within the allowance = %d, want 401", attempt, code)
		}
	}

	code, body, res = doAuth(t, limited, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ratelimit@example.com", "wrong-password-here"),
	})
	if code != http.StatusTooManyRequests {
		t.Fatalf("third attempt = %d, want 429 (%v)", code, body)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Error("the rate limiter sent no Retry-After")
	}
	// The limiter is per address and blocks BEFORE the credential check, so a
	// valid password is refused too -- that is what makes it a limiter rather
	// than a counter of failures.
	if code, _, _ := doAuth(t, limited, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ratelimit@example.com", testPassword),
	}); code != http.StatusTooManyRequests {
		t.Errorf("valid login while rate limited = %d, want 429", code)
	}
}

// ---------------------------------------------------------------------------
// /me
// ---------------------------------------------------------------------------

func TestAuthMeReturnsTheSameBodyAsLogin(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	owner := mustCreateOperator(t, one, "me@example.com", models.RoleOwner)

	token, csrf := login(t, env.router, "me@example.com", testPassword)

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("/me = %d (%v)", code, body)
	}

	operator := body["operator"].(map[string]any)
	if operator["id"] != owner.PublicID || operator["role"] != models.RoleOwner {
		t.Errorf("/me operator = %v", operator)
	}
	// An OWNER holds no grants but reaches every site: the flag is what tells
	// the dashboard that an empty list means "all", not "none".
	if body["all_sites"] != true {
		t.Error("an OWNER should be marked all_sites")
	}
	if sites := body["sites"].([]any); len(sites) != 0 {
		t.Errorf("sites = %v, want an empty array rather than null", sites)
	}

	// The CSRF token survives a reload. This is the reason it is derived from
	// the session token rather than stored: /me can hand back the same one.
	if body["csrf_token"] != csrf {
		t.Errorf("/me csrf_token = %v, want the token login issued", body["csrf_token"])
	}
	if !strings.Contains(mustJSON(t, body), "csrf_token") {
		t.Error("/me returned no csrf_token")
	}

	// Unauthenticated.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me",
	}); code != http.StatusUnauthorized {
		t.Errorf("/me without a cookie = %d, want 401", code)
	}
	// A site API key is not browser authentication.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("X-API-Key", env.siteAKey)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/me with a site API key = %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

func TestAuthLogout(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "logout@example.com", models.RoleViewer)

	token, csrf := login(t, env.router, "logout@example.com", testPassword)

	// Unsafe method, so the CSRF token is required.
	if code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/logout", token: token,
	}); code != http.StatusForbidden {
		t.Fatalf("logout without a CSRF token = %d, want 403 (%v)", code, body)
	}
	// And the session is still alive after that refusal.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	}); code != http.StatusOK {
		t.Fatal("a refused logout invalidated the session")
	}

	code, _, res := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/logout", token: token, csrf: csrf,
	})
	if code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", code)
	}

	// The cookie is cleared with matching attributes, or the browser keeps it.
	name, _ := middleware.SessionCookieConfig()
	var cleared *http.Cookie
	for _, cookie := range res.Cookies() {
		if cookie.Name == name {
			cleared = cookie
		}
	}
	if cleared == nil {
		t.Fatal("logout did not clear the session cookie")
	}
	if cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Errorf("cleared cookie = %q with Max-Age %d, want empty and expired",
			cleared.Value, cleared.MaxAge)
	}
	if cleared.Path != "/" || !cleared.HttpOnly || !cleared.Secure {
		t.Error("the clearing cookie does not match the attributes it was set with")
	}

	// The row is revoked too, so a copy of the cookie is worthless.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	}); code != http.StatusUnauthorized {
		t.Errorf("the session survived logout: %d", code)
	}
}

// ---------------------------------------------------------------------------
// Password change
// ---------------------------------------------------------------------------

func TestAuthPasswordChange(t *testing.T) {
	cheapBcrypt(t)
	t.Setenv("LOGIN_RATE_LIMIT_PER_MINUTE", "1000")
	newTestEnv(t)
	router := NewRouter()
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "pwchange@example.com", models.RoleAdmin)

	token, csrf := login(t, router, "pwchange@example.com", testPassword)
	// A second session, which must not survive the change.
	sibling, _ := login(t, router, "pwchange@example.com", testPassword)

	change := func(current, next, csrfToken, sessionToken, contentType string) (int, map[string]any) {
		payload, _ := json.Marshal(map[string]string{
			"current_password": current, "new_password": next,
		})
		code, body, _ := doAuth(t, router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/password",
			body: string(payload), token: sessionToken, csrf: csrfToken,
			contentType: contentType,
		})
		return code, body
	}

	newPassword := "a-completely-different-secret"

	// Refusals first, so the password is still the original for each of them.
	refusals := []struct {
		name                    string
		current, next           string
		csrfToken, sessionToken string
		contentType             string
		want                    int
	}{
		{"no session", testPassword, newPassword, csrf, "", "", http.StatusUnauthorized},
		{"no csrf", testPassword, newPassword, "", token, "", http.StatusForbidden},
		{"wrong csrf", testPassword, newPassword, "wrong", token, "", http.StatusForbidden},
		{"wrong current password", "not-my-password", newPassword, csrf, token, "", http.StatusForbidden},
		{"new password too short", testPassword, "short", csrf, token, "", http.StatusBadRequest},
		{"not json", testPassword, newPassword, csrf, token, "text/plain", http.StatusUnsupportedMediaType},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			code, body := change(tc.current, tc.next, tc.csrfToken, tc.sessionToken, tc.contentType)
			if code != tc.want {
				t.Errorf("%s = %d, want %d (%v)", tc.name, code, tc.want, body)
			}
		})
	}
	// None of those changed anything.
	if _, err := database.AuthenticatePassword("pwchange@example.com", testPassword); err != nil {
		t.Fatalf("a refused change altered the password: %v", err)
	}

	if code, body := change(testPassword, newPassword, csrf, token, ""); code != http.StatusNoContent {
		t.Fatalf("password change = %d, want 204 (%v)", code, body)
	}

	// The old password is gone and the new one works.
	if _, err := database.AuthenticatePassword("pwchange@example.com", testPassword); err == nil {
		t.Error("the old password still authenticates")
	}
	if _, err := database.AuthenticatePassword("pwchange@example.com", newPassword); err != nil {
		t.Errorf("the new password does not authenticate: %v", err)
	}

	// The tab that made the change stays logged in; the other one does not.
	if code, _, _ := doAuth(t, router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	}); code != http.StatusOK {
		t.Error("changing a password logged out the session that did it")
	}
	if code, _, _ := doAuth(t, router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: sibling,
	}); code != http.StatusUnauthorized {
		t.Error("a sibling session survived the password change")
	}
}

// ---------------------------------------------------------------------------
// Cookie configuration
// ---------------------------------------------------------------------------

func TestSessionCookieDevelopmentToggle(t *testing.T) {
	cheapBcrypt(t)
	t.Setenv("SESSION_COOKIE_INSECURE", "1")
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "devcookie@example.com", models.RoleViewer)

	name, secure := middleware.SessionCookieConfig()
	if name != "al_session" || secure {
		t.Fatalf("dev config = %q secure=%v, want al_session and not Secure", name, secure)
	}

	code, _, res := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("devcookie@example.com", testPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("login in dev mode = %d", code)
	}

	var cookie *http.Cookie
	for _, candidate := range res.Cookies() {
		if candidate.Name == name {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatalf("no %s cookie (Set-Cookie: %q)", name, res.Header.Get("Set-Cookie"))
	}
	if cookie.Secure {
		t.Error("the development cookie is marked Secure, which localhost http cannot deliver")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Error("the development cookie relaxed more than Secure and the name")
	}
	// The name must change with the flag: a __Host- cookie without Secure is
	// rejected by the browser outright, so keeping the prefix is not an option.
	if strings.HasPrefix(cookie.Name, "__Host-") {
		t.Error("a non-Secure cookie kept the __Host- prefix")
	}

	// Exactly one name is accepted. The production name must not authenticate
	// while the dev name is configured, or a sibling subdomain could set one.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me",
		token: cookie.Value, cookieName: "__Host-al_session",
	}); code != http.StatusUnauthorized {
		t.Errorf("the __Host- cookie authenticated while dev mode was on: %d", code)
	}
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me",
		token: cookie.Value, cookieName: "al_session",
	}); code != http.StatusOK {
		t.Error("the development cookie did not authenticate in dev mode")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return string(raw)
}
