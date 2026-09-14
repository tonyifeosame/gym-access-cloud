package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/oidc"
)

// Sign in with Google, end to end, against a fake Google.
//
// The fake serves the three things the real one does -- the discovery document,
// the token endpoint and the key set -- on a loopback httptest server, and
// mints ID tokens signed with a key it generated. Every check the oidc package
// performs is then exercised by handing it a token that fails exactly one of
// them, and every account rule in database.AuthenticateGoogle is exercised by
// arranging the account and reading what the browser is sent back with.
//
// Through NewRouter, so the rate limiter, the cookie attributes and the
// redirect chain are the real ones.

const (
	fakeGoogleClientID     = "1234567890-test.apps.googleusercontent.com"
	fakeGoogleClientSecret = "GOCSPX-test-secret"
	fakeGoogleRedirectURL  = "http://localhost:8080/api/v1/auth/google/callback"
	fakeConsoleURL         = "http://localhost:5173"
)

// fakeGoogle is the issuer under test control.
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string

	mu    sync.Mutex
	codes map[string]issuedCode
	// tokenCalls counts code exchanges, so a test can assert the callback
	// never reached Google when it should have refused earlier.
	tokenCalls int

	// Knobs. Set before the browser "returns" from Google.
	subject       string
	email         string
	emailVerified bool
	signWith      *rsa.PrivateKey // nil means the published key
	audience      string          // "" means the client id
	expiresIn     time.Duration   // 0 means an hour
	issuer        string          // "" means the server's own URL
	dropNonce     bool
}

type issuedCode struct {
	nonce     string
	challenge string
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating signing key: %v", err)
	}
	g := &fakeGoogle{
		key:           key,
		kid:           "test-key-1",
		codes:         map[string]issuedCode{},
		subject:       "108000000000000000001",
		email:         "ada@example.com",
		emailVerified: true,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 g.server.URL,
			"authorization_endpoint": g.server.URL + "/o/oauth2/v2/auth",
			"token_endpoint":         g.server.URL + "/token",
			"jwks_uri":               g.server.URL + "/oauth2/v3/certs",
		})
	})
	mux.HandleFunc("/oauth2/v3/certs", func(w http.ResponseWriter, r *http.Request) {
		pub := g.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": g.kid,
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			}},
		})
	})
	mux.HandleFunc("/token", g.tokenEndpoint)

	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

// issueCode is the user signing in at Google: given what the API sent in its
// authorization request, Google hands back a code bound to that attempt.
func (g *fakeGoogle) issueCode(t *testing.T, authURL string) string {
	t.Helper()
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	q := parsed.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	code := "code-" + q.Get("state")[:8]
	g.mu.Lock()
	g.codes[code] = issuedCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	g.mu.Unlock()
	return code
}

func (g *fakeGoogle) tokenEndpoint(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.tokenCalls++
	g.mu.Unlock()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	reject := func(reason string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": reason})
	}
	if r.PostForm.Get("client_id") != fakeGoogleClientID ||
		r.PostForm.Get("client_secret") != fakeGoogleClientSecret {
		reject("client credentials")
		return
	}
	if r.PostForm.Get("redirect_uri") != fakeGoogleRedirectURL {
		reject("redirect_uri")
		return
	}
	g.mu.Lock()
	issued, ok := g.codes[r.PostForm.Get("code")]
	delete(g.codes, r.PostForm.Get("code"))
	g.mu.Unlock()
	if !ok {
		reject("unknown or spent code")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != issued.challenge {
		reject("pkce verifier")
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "ya29.test",
		"token_type":   "Bearer",
		"expires_in":   3599,
		"id_token":     g.mintIDToken(issued.nonce),
	})
}

func (g *fakeGoogle) mintIDToken(nonce string) string {
	now := time.Now()
	expiresIn := g.expiresIn
	if expiresIn == 0 {
		expiresIn = time.Hour
	}
	aud := g.audience
	if aud == "" {
		aud = fakeGoogleClientID
	}
	iss := g.issuer
	if iss == "" {
		iss = g.server.URL
	}
	claims := map[string]any{
		"iss":            iss,
		"sub":            g.subject,
		"aud":            aud,
		"exp":            now.Add(expiresIn).Unix(),
		"iat":            now.Add(-time.Second).Unix(),
		"email":          g.email,
		"email_verified": g.emailVerified,
		"name":           "Ada Okonkwo",
	}
	if !g.dropNonce {
		claims["nonce"] = nonce
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": g.kid})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)

	key := g.signWith
	if key == nil {
		key = g.key
	}
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// googleFixture is a router with Google sign-in configured against the fake.
type googleFixture struct {
	env    *testEnv
	google *fakeGoogle
}

func newGoogleFixture(t *testing.T) *googleFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	google := newFakeGoogle(t)

	provider, err := oidc.New(oidc.Config{
		ClientID:     fakeGoogleClientID,
		ClientSecret: fakeGoogleClientSecret,
		RedirectURL:  fakeGoogleRedirectURL,
		Issuer:       google.server.URL,
		HTTPClient:   google.server.Client(),
	})
	if err != nil {
		t.Fatalf("building provider: %v", err)
	}
	handlers.ConfigureGoogleSignIn(provider)
	if err := handlers.ConfigureConsoleURL(fakeConsoleURL); err != nil {
		t.Fatalf("console url: %v", err)
	}
	t.Cleanup(func() {
		handlers.ConfigureGoogleSignIn(nil)
		_ = handlers.ConfigureConsoleURL("")
	})
	return &googleFixture{env: env, google: google}
}

// start is the browser following the "Sign in with Google" link. Returns the
// Google authorization URL and the state cookie the API set.
func (f *googleFixture) start(t *testing.T, next string) (authURL string, stateCookie *http.Cookie) {
	t.Helper()
	path := "/api/v1/auth/google/start"
	if next != "" {
		path += "?next=" + url.QueryEscape(next)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	f.env.router.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303: %s", w.Code, w.Body.String())
	}
	authURL = w.Header().Get("Location")
	for _, cookie := range w.Result().Cookies() {
		if strings.HasSuffix(cookie.Name, "al_oauth") && cookie.Value != "" {
			stateCookie = cookie
		}
	}
	if stateCookie == nil {
		t.Fatal("start set no state cookie")
	}
	return authURL, stateCookie
}

// callback is Google sending the browser back.
func (f *googleFixture) callback(t *testing.T, query string, stateCookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/callback?"+query, nil)
	if stateCookie != nil {
		req.AddCookie(stateCookie)
	}
	w := httptest.NewRecorder()
	f.env.router.ServeHTTP(w, req)
	return w
}

// signIn runs the whole flow and returns the callback response.
func (f *googleFixture) signIn(t *testing.T, next string) *httptest.ResponseRecorder {
	t.Helper()
	authURL, cookie := f.start(t, next)
	code := f.google.issueCode(t, authURL)
	state := url.Values{}
	state.Set("code", code)
	state.Set("state", mustQueryParam(t, authURL, "state"))
	return f.callback(t, state.Encode(), cookie)
}

func mustQueryParam(t *testing.T, raw, name string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	value := parsed.Query().Get(name)
	if value == "" {
		t.Fatalf("%q has no %s parameter", raw, name)
	}
	return value
}

func sessionCookieFrom(res *http.Response) string {
	name, _ := middleware.SessionCookieConfig()
	for _, cookie := range res.Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

// expectOutcome asserts the browser was sent back to the login page with one
// specific code and no session.
func expectOutcome(t *testing.T, w *httptest.ResponseRecorder, outcome string) {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d, want 303: %s", w.Code, w.Body.String())
	}
	want := fakeConsoleURL + "/login?error=" + outcome
	if got := w.Header().Get("Location"); got != want {
		t.Errorf("redirected to %q, want %q", got, want)
	}
	if sessionCookieFrom(w.Result()) != "" {
		t.Error("a session cookie was set on a refused sign-in")
	}
}

func googleSubjectOf(t *testing.T, email string) string {
	t.Helper()
	var subject sql.NullString
	if err := database.DB.QueryRow(`SELECT google_subject FROM users WHERE email = $1`, email).
		Scan(&subject); err != nil {
		t.Fatalf("reading google_subject for %s: %v", email, err)
	}
	return subject.String
}

// ---------------------------------------------------------------------------
// The happy paths
// ---------------------------------------------------------------------------

func TestGoogleSignInStartRedirectsToGoogleWithPKCEAndAStateCookie(t *testing.T) {
	f := newGoogleFixture(t)

	authURL, cookie := f.start(t, "/people")

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	if !strings.HasPrefix(authURL, f.google.server.URL+"/o/oauth2/v2/auth?") {
		t.Errorf("start redirected to %q, want the issuer's authorization endpoint", authURL)
	}
	q := parsed.Query()
	for name, want := range map[string]string{
		"response_type":         "code",
		"client_id":             fakeGoogleClientID,
		"redirect_uri":          fakeGoogleRedirectURL,
		"code_challenge_method": "S256",
		"prompt":                "select_account",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(name) == "" {
			t.Errorf("authorization request has no %s", name)
		}
	}
	if !strings.Contains(q.Get("scope"), "openid") || !strings.Contains(q.Get("scope"), "email") {
		t.Errorf("scope = %q, want openid and email", q.Get("scope"))
	}

	// The cookie: HttpOnly, Lax, short-lived, and it carries the secrets
	// without carrying the code_challenge (which is public anyway).
	if !cookie.HttpOnly {
		t.Error("state cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.MaxAge <= 0 || cookie.MaxAge > 15*60 {
		t.Errorf("state cookie Max-Age = %d, want a short positive lifetime", cookie.MaxAge)
	}
	if cookie.Secure != true {
		t.Error("state cookie is not Secure in the default (production) cookie mode")
	}
}

func TestGoogleSignInForALinkedAccountOpensAnOrdinarySession(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleManager)
	mustExec(t, `UPDATE users SET google_subject = $1, google_linked_at = now() WHERE id = $2`,
		f.google.subject, user.ID)
	// The token's email is DIFFERENT from the account's: a linked subject is
	// matched on the subject alone, and the address Google reports today is
	// not consulted.
	f.google.email = "ada.renamed@example.com"

	w := f.signIn(t, "/people")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d, want 303: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != fakeConsoleURL+"/people" {
		t.Errorf("redirected to %q, want the console's /people", got)
	}
	token := sessionCookieFrom(w.Result())
	if token == "" {
		t.Fatal("callback set no session cookie")
	}

	// THE SAME SESSION MECHANISM. /me answers exactly as it would after a
	// password login, and the CSRF token it returns works on an unsafe route.
	code, body, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("/me after google sign-in = %d (%v)", code, body)
	}
	operator := body["operator"].(map[string]any)
	if operator["email"] != "ada@example.com" {
		t.Errorf("/me reports %v, want the linked account", operator["email"])
	}
	if got := googleSubjectOf(t, "ada@example.com"); got != f.google.subject {
		t.Errorf("google_subject = %q after sign-in, want unchanged %q", got, f.google.subject)
	}
	if queryInt(t, `SELECT count(*) FROM users WHERE last_login_at IS NOT NULL`) != 1 {
		t.Error("last_login_at was not stamped by the google sign-in")
	}

	csrf, _ := body["csrf_token"].(string)
	logoutCode, _, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/logout", token: token, csrf: csrf,
	})
	if logoutCode != http.StatusNoContent {
		t.Errorf("logout with the session's csrf token = %d, want 204", logoutCode)
	}
}

func TestFirstGoogleSignInLinksTheAccountWithTheVerifiedAddress(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	// Mixed case at Google; the account is stored lowercased.
	f.google.email = "Ada@Example.com"

	w := f.signIn(t, "")

	if got := w.Header().Get("Location"); got != fakeConsoleURL+"/" {
		t.Fatalf("redirected to %q, want the console root: %s", got, w.Body.String())
	}
	if sessionCookieFrom(w.Result()) == "" {
		t.Fatal("no session cookie after a linking sign-in")
	}
	if got := googleSubjectOf(t, "ada@example.com"); got != f.google.subject {
		t.Errorf("google_subject = %q, want %q linked on first sign-in", got, f.google.subject)
	}

	// The linking is a distinct audit action, attributed to the account.
	var action, actor string
	if err := database.DB.QueryRow(`SELECT action, actor_email FROM audit_events
	              WHERE target_public_id = $1 ORDER BY id DESC LIMIT 1`, user.PublicID).
		Scan(&action, &actor); err != nil {
		t.Fatalf("reading the audit trail: %v", err)
	}
	if action != "OPERATOR_GOOGLE_LINKED" || actor != "ada@example.com" {
		t.Errorf("audit = %s by %s, want OPERATOR_GOOGLE_LINKED by the account", action, actor)
	}

	// Signing in again matches on the subject and links nothing new.
	w2 := f.signIn(t, "")
	if sessionCookieFrom(w2.Result()) == "" {
		t.Fatal("second sign-in opened no session")
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events WHERE action = 'OPERATOR_GOOGLE_LINKED'`); n != 1 {
		t.Errorf("linking audited %d times, want once", n)
	}
}

// ---------------------------------------------------------------------------
// Account rules
// ---------------------------------------------------------------------------

func TestGoogleSignInWithNoAccountCreatesNothing(t *testing.T) {
	f := newGoogleFixture(t)
	f.google.email = "stranger@example.com"

	w := f.signIn(t, "")

	expectOutcome(t, w, "google_no_account")
	if queryInt(t, `SELECT count(*) FROM users`) != 0 {
		t.Error("a google sign-in with no account created a user")
	}
	if queryInt(t, `SELECT count(*) FROM companies`) != 2 {
		t.Error("a google sign-in with no account created a company")
	}
}

func TestGoogleSignInWithAnUnverifiedAddressLinksNothing(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	f.google.emailVerified = false

	w := f.signIn(t, "")

	expectOutcome(t, w, "google_email_unverified")
	if got := googleSubjectOf(t, "ada@example.com"); got != "" {
		t.Errorf("an unverified address linked subject %q", got)
	}
}

func TestGoogleSignInRefusesAnAddressLinkedToAnotherGoogleAccount(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET google_subject = 'someone-else', google_linked_at = now() WHERE id = $1`, user.ID)

	w := f.signIn(t, "")

	expectOutcome(t, w, "google_account_conflict")
	if got := googleSubjectOf(t, "ada@example.com"); got != "someone-else" {
		t.Errorf("the conflicting sign-in changed the link to %q", got)
	}
}

func TestGoogleSignInRespectsDisabledAccountsAndCompanies(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)

	mustExec(t, `UPDATE users SET active = FALSE WHERE id = $1`, user.ID)
	expectOutcome(t, f.signIn(t, ""), "google_no_account")
	if googleSubjectOf(t, "ada@example.com") != "" {
		t.Error("a disabled account was linked")
	}

	mustExec(t, `UPDATE users SET active = TRUE, google_subject = $2 WHERE id = $1`, user.ID, f.google.subject)
	mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, one)
	expectOutcome(t, f.signIn(t, ""), "google_no_account")

	mustExec(t, `UPDATE companies SET active = TRUE WHERE id = $1`, one)
	if sessionCookieFrom(f.signIn(t, "").Result()) == "" {
		t.Error("re-enabling the company did not restore the sign-in")
	}
}

func TestGoogleSignInRetiresAPasswordSomebodyElseChose(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	// An administrative reset: the administrator knows this password.
	mustExec(t, `UPDATE users SET must_change_password = TRUE WHERE id = $1`, user.ID)
	adminToken, _ := login(t, f.env.router, "ada@example.com", testPassword)

	w := f.signIn(t, "")
	token := sessionCookieFrom(w.Result())
	if token == "" {
		t.Fatalf("no session after google sign-in: %s", w.Header().Get("Location"))
	}

	code, body, _ := doAuth(t, f.env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("/me = %d (%v)", code, body)
	}
	if body["must_change_password"] != false {
		t.Error("the session still insists on a password change the operator cannot make")
	}

	// The third-party-known password is dead, and so is the session it opened.
	code, _, _ = doAuth(t, f.env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("ada@example.com", testPassword),
	})
	if code != http.StatusUnauthorized {
		t.Errorf("the administrator-chosen password still signs in (%d)", code)
	}
	code, _, _ = doAuth(t, f.env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: adminToken,
	})
	if code != http.StatusUnauthorized {
		t.Errorf("the session opened with the retired password is still live (%d)", code)
	}

	// A reset link still works, so the operator can choose a password of
	// their own whenever they want one.
	if !queryBool(t, `SELECT NOT must_change_password FROM users WHERE id = $1`, user.ID) {
		t.Error("must_change_password is still set")
	}
}

func TestGoogleSignInClearsALoginLockout(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET failed_login_count = 7, locked_until = now() + interval '10 minutes',
	                              google_subject = $2 WHERE id = $1`, user.ID, f.google.subject)

	if sessionCookieFrom(f.signIn(t, "").Result()) == "" {
		t.Fatal("a locked account could not sign in with google, which proves the identity the lock was waiting for")
	}
	if queryInt(t, `SELECT failed_login_count FROM users WHERE id = $1`, user.ID) != 0 {
		t.Error("failed_login_count was not reset")
	}
}

// ---------------------------------------------------------------------------
// Protocol failures. Each hands the callback a token that fails one check.
// ---------------------------------------------------------------------------

func TestGoogleCallbackRefusesAMissingOrMismatchedState(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)

	authURL, cookie := f.start(t, "")
	code := f.google.issueCode(t, authURL)
	state := mustQueryParam(t, authURL, "state")

	// No cookie: the pasted-link case.
	expectOutcome(t, f.callback(t, "code="+code+"&state="+state, nil), "google_state_mismatch")
	// Wrong state with the right cookie.
	expectOutcome(t, f.callback(t, "code="+code+"&state=forged", cookie), "google_state_mismatch")
	// Cookie from one attempt, state from another.
	otherURL, _ := f.start(t, "")
	expectOutcome(t, f.callback(t, "code="+code+"&state="+mustQueryParam(t, otherURL, "state"), cookie),
		"google_state_mismatch")

	f.google.mu.Lock()
	calls := f.google.tokenCalls
	f.google.mu.Unlock()
	if calls != 0 {
		t.Errorf("the code was exchanged %d times despite the state check failing", calls)
	}
}

func TestGoogleCallbackReportsTheUserDeclining(t *testing.T) {
	f := newGoogleFixture(t)
	authURL, cookie := f.start(t, "")
	state := mustQueryParam(t, authURL, "state")

	expectOutcome(t, f.callback(t, "error=access_denied&state="+state, cookie), "google_cancelled")
	expectOutcome(t, f.callback(t, "error=server_error&state="+state, cookie), "google_failed")
}

func TestGoogleCallbackRefusesABadlyIssuedToken(t *testing.T) {
	one := operatorCompanyID(t, "one")

	cases := []struct {
		name    string
		arrange func(g *fakeGoogle)
	}{
		{"signed with a key google does not publish", func(g *fakeGoogle) {
			other, _ := rsa.GenerateKey(rand.Reader, 2048)
			g.signWith = other
		}},
		{"issued for a different client", func(g *fakeGoogle) {
			g.audience = "another-client.apps.googleusercontent.com"
		}},
		{"already expired", func(g *fakeGoogle) {
			g.expiresIn = -2 * time.Hour
		}},
		{"missing the nonce", func(g *fakeGoogle) {
			g.dropNonce = true
		}},
		{"from another issuer", func(g *fakeGoogle) {
			g.issuer = "https://accounts.example.net"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGoogleFixture(t)
			mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
			tc.arrange(f.google)

			expectOutcome(t, f.signIn(t, ""), "google_failed")
			if googleSubjectOf(t, "ada@example.com") != "" {
				t.Error("a refused token linked the account")
			}
		})
	}
}

func TestGoogleCallbackRefusesAnUnknownCode(t *testing.T) {
	f := newGoogleFixture(t)
	authURL, cookie := f.start(t, "")
	state := mustQueryParam(t, authURL, "state")

	expectOutcome(t, f.callback(t, "code=never-issued&state="+state, cookie), "google_failed")
}

func TestGoogleNextIsKeptInsideTheConsole(t *testing.T) {
	f := newGoogleFixture(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "ada@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET google_subject = $2 WHERE id = $1`, user.ID, f.google.subject)

	for _, next := range []string{"//evil.example/steal", "https://evil.example/", "javascript:alert(1)", "people"} {
		w := f.signIn(t, next)
		if got := w.Header().Get("Location"); got != fakeConsoleURL+"/" {
			t.Errorf("next=%q redirected to %q, want the console root", next, got)
		}
	}
	w := f.signIn(t, "/terminals?q=front")
	if got := w.Header().Get("Location"); got != fakeConsoleURL+"/terminals?q=front" {
		t.Errorf("a console path was rewritten to %q", got)
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func TestGoogleRoutesAreOffUntilConfigured(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	handlers.ConfigureGoogleSignIn(nil)

	code, body, _ := doAuth(t, env.router, authCall{method: http.MethodGet, path: "/api/v1/auth/providers"})
	if code != http.StatusOK {
		t.Fatalf("providers = %d", code)
	}
	google := body["google"].(map[string]any)
	if google["enabled"] != false {
		t.Errorf("providers reports google enabled=%v with nothing configured", google["enabled"])
	}
	if body["password"].(map[string]any)["enabled"] != true {
		t.Error("providers does not report password sign-in")
	}

	for _, path := range []string{"/api/v1/auth/google/start", "/api/v1/auth/google/callback?code=x&state=y"} {
		code, _, res := doAuth(t, env.router, authCall{method: http.MethodGet, path: path})
		if code != http.StatusForbidden {
			t.Errorf("%s = %d with google unconfigured, want 403", path, code)
		}
		if sessionCookieFrom(res) != "" {
			t.Errorf("%s set a session cookie while unconfigured", path)
		}
	}

	// And on once it is.
	f := newGoogleFixture(t)
	_, body, _ = doAuth(t, f.env.router, authCall{method: http.MethodGet, path: "/api/v1/auth/providers"})
	if body["google"].(map[string]any)["enabled"] != true {
		t.Error("providers does not report google once configured")
	}
	if body["google"].(map[string]any)["start_path"] != "/api/v1/auth/google/start" {
		t.Error("providers does not name the start path")
	}
}

func TestGoogleConfigurationRefusesToBeHalfSet(t *testing.T) {
	clear := func(t *testing.T) {
		for _, name := range []string{oidc.EnvGoogleClientID, oidc.EnvGoogleClientSecret,
			oidc.EnvGoogleRedirectURL, oidc.EnvGoogleIssuerURL, handlers.EnvConsoleURL} {
			t.Setenv(name, "")
		}
	}

	t.Run("nothing set is off, not an error", func(t *testing.T) {
		clear(t)
		if _, err := oidc.ConfigFromEnv(); err != oidc.ErrNotConfigured {
			t.Errorf("err = %v, want ErrNotConfigured", err)
		}
		if err := configureIdentityProviders(); err != nil {
			t.Errorf("startup with nothing configured = %v", err)
		}
		if handlers.GoogleSignInEnabled() {
			t.Error("google enabled with nothing configured")
		}
	})

	t.Run("a client id without its secret is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(oidc.EnvGoogleClientID, fakeGoogleClientID)
		_, err := oidc.ConfigFromEnv()
		if err == nil || !strings.Contains(err.Error(), oidc.EnvGoogleClientSecret) {
			t.Errorf("err = %v, want one naming the missing variable", err)
		}
		if configureIdentityProviders() == nil {
			t.Error("startup accepted a half-configured provider")
		}
	})

	t.Run("a plain-http callback off loopback is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(oidc.EnvGoogleClientID, fakeGoogleClientID)
		t.Setenv(oidc.EnvGoogleClientSecret, fakeGoogleClientSecret)
		t.Setenv(oidc.EnvGoogleRedirectURL, "http://api.example.com/api/v1/auth/google/callback")
		if _, err := oidc.ConfigFromEnv(); err == nil {
			t.Error("an http callback on a public host was accepted")
		}
	})

	t.Run("google without a console url is refused", func(t *testing.T) {
		clear(t)
		t.Setenv(oidc.EnvGoogleClientID, fakeGoogleClientID)
		t.Setenv(oidc.EnvGoogleClientSecret, fakeGoogleClientSecret)
		t.Setenv(oidc.EnvGoogleRedirectURL, fakeGoogleRedirectURL)
		err := configureIdentityProviders()
		if err == nil || !strings.Contains(err.Error(), handlers.EnvConsoleURL) {
			t.Errorf("err = %v, want one naming %s", err, handlers.EnvConsoleURL)
		}
	})

	t.Run("fully configured enables the routes", func(t *testing.T) {
		clear(t)
		t.Setenv(oidc.EnvGoogleClientID, fakeGoogleClientID)
		t.Setenv(oidc.EnvGoogleClientSecret, fakeGoogleClientSecret)
		t.Setenv(oidc.EnvGoogleRedirectURL, fakeGoogleRedirectURL)
		t.Setenv(handlers.EnvConsoleURL, fakeConsoleURL)
		t.Cleanup(func() {
			handlers.ConfigureGoogleSignIn(nil)
			_ = handlers.ConfigureConsoleURL("")
		})
		if err := configureIdentityProviders(); err != nil {
			t.Fatalf("startup = %v", err)
		}
		if !handlers.GoogleSignInEnabled() {
			t.Error("google not enabled after full configuration")
		}
	})
}
