package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// The OAuth authorization server, end to end through the real router
// (migrations/037).
//
// NOTHING IS SEEDED AROUND THE FLOW. Every test here drives the same path a
// customer's browser and a third party's server would: the authorize page, the
// consent form, the token endpoint. The one thing reached directly is the
// clock -- an expiry is moved by updating the row, because waiting an hour is
// not a test.

const (
	oauthClientID     = "datavase"
	oauthClientSecret = "test-client-secret-not-a-real-one"
	oauthRedirectURI  = "https://app.datavase.example/integrations/accesslink/callback"
	oauthAuthorizeURL = "/api/public/v1/oauth/authorize"
	oauthTokenURL     = "/api/public/v1/oauth/token"
	oauthRevokeURL    = "/api/public/v1/oauth/revoke"
)

// seedOAuthClient configures a client the way a deployment would.
func seedOAuthClient(t *testing.T, clientID, secret string, redirects, scopes []string) *models.OAuthClient {
	t.Helper()
	client, err := database.UpsertOAuthClient(database.OAuthClientConfig{
		ClientID:     clientID,
		Name:         "Datavase",
		Secret:       secret,
		RedirectURIs: redirects,
		Scopes:       scopes,
	})
	if err != nil {
		t.Fatalf("configuring oauth client %q: %v", clientID, err)
	}
	return client
}

// defaultOAuthClient is the confidential Datavase client every happy path uses.
func defaultOAuthClient(t *testing.T) *models.OAuthClient {
	t.Helper()
	return seedOAuthClient(t, oauthClientID, oauthClientSecret,
		[]string{oauthRedirectURI},
		[]string{models.ScopeSitesRead, models.ScopeSitesWrite})
}

// pkcePair returns a verifier and its S256 challenge.
func pkcePair(seed string) (verifier, challenge string) {
	verifier = seed + strings.Repeat("a", 43-len(seed))
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeQuery builds the authorization request, with overrides applied last
// so a test can bend exactly one parameter and leave the rest correct.
func authorizeQuery(challenge string, overrides map[string]string) url.Values {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", oauthClientID)
	q.Set("redirect_uri", oauthRedirectURI)
	q.Set("scope", models.ScopeSitesRead+" "+models.ScopeSitesWrite)
	q.Set("state", "state-abc-123")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
			continue
		}
		q.Set(k, v)
	}
	return q
}

var formTokenPattern = regexp.MustCompile(`name="form_token" value="([^"]+)"`)

// consentPage requests the authorize page as a signed-in operator and returns
// the form token and the cookies the page set.
func consentPage(t *testing.T, env *testEnv, sessionToken string, q url.Values) (
	*httptest.ResponseRecorder, string) {

	t.Helper()
	req := httptest.NewRequest(http.MethodGet, oauthAuthorizeURL+"?"+q.Encode(), nil)
	if sessionToken != "" {
		name, _ := middleware.SessionCookieConfig()
		req.AddCookie(&http.Cookie{Name: name, Value: sessionToken})
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	match := formTokenPattern.FindStringSubmatch(w.Body.String())
	if match == nil {
		return w, ""
	}
	return w, match[1]
}

// submitConsent posts the consent form as the browser would.
func submitConsent(t *testing.T, env *testEnv, sessionToken, formToken, action string,
	q url.Values) *httptest.ResponseRecorder {

	t.Helper()
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("form_token", formToken)
	form.Set("action", action)

	req := httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if sessionToken != "" {
		name, _ := middleware.SessionCookieConfig()
		req.AddCookie(&http.Cookie{Name: name, Value: sessionToken})
	}
	if formToken != "" {
		nonceName, _ := oauthNonceCookieName()
		req.AddCookie(&http.Cookie{Name: nonceName, Value: formToken})
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	return w
}

// oauthNonceCookieName mirrors the handler's own choice. Duplicated rather
// than exported: the name is an implementation detail of the consent page, and
// a test that needs it is testing the page, not a contract.
func oauthNonceCookieName() (string, bool) { return "__Host-al_oauth", true }

// codeFromRedirect pulls the authorization code out of a 303 Location.
func codeFromRedirect(t *testing.T, w *httptest.ResponseRecorder) (code, state, oerr string) {
	t.Helper()
	if w.Code != http.StatusSeeOther {
		t.Fatalf("consent = %d, want 303 (body %s)", w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	q := location.Query()
	return q.Get("code"), q.Get("state"), q.Get("error")
}

// tokenCall posts to the token endpoint with client_secret_post.
func tokenCall(t *testing.T, env *testEnv, form url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, oauthTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	var body map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("token response is not JSON: %q", w.Body.String())
		}
	}
	return w.Code, body
}

func codeExchangeForm(code, verifier string) url.Values {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", oauthRedirectURI)
	form.Set("code_verifier", verifier)
	form.Set("client_id", oauthClientID)
	form.Set("client_secret", oauthClientSecret)
	return form
}

// connectedGrant drives the whole flow and returns the issued tokens.
//
// THE ONE HELPER EVERY OTHER TEST BUILDS ON, so if the happy path breaks the
// failure is one test rather than twenty.
type connectedGrant struct {
	AccessToken  string
	RefreshToken string
	Scope        string
	CompanyID    int64
	UserEmail    string
	SessionToken string
}

func connectDatavase(t *testing.T, env *testEnv, slug, email, role string) connectedGrant {
	t.Helper()
	cheapBcrypt(t)
	defaultOAuthClient(t)

	companyID := operatorCompanyID(t, slug)
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, email, role)

	verifier, challenge := pkcePair("connect-verifier")
	q := authorizeQuery(challenge, nil)

	page, formToken := consentPage(t, env, sessionToken, q)
	if page.Code != http.StatusOK || formToken == "" {
		t.Fatalf("consent page = %d, form token %q (body %s)", page.Code, formToken, page.Body.String())
	}

	code, state, oerr := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))
	if oerr != "" || code == "" {
		t.Fatalf("consent returned error=%q code=%q", oerr, code)
	}
	if state != "state-abc-123" {
		t.Fatalf("state came back as %q", state)
	}

	status, body := tokenCall(t, env, codeExchangeForm(code, verifier))
	if status != http.StatusOK {
		t.Fatalf("token exchange = %d (%v)", status, body)
	}
	access, _ := body["access_token"].(string)
	refresh, _ := body["refresh_token"].(string)
	scope, _ := body["scope"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("token response is missing a token: %v", body)
	}
	return connectedGrant{
		AccessToken:  access,
		RefreshToken: refresh,
		Scope:        scope,
		CompanyID:    companyID,
		UserEmail:    email,
		SessionToken: sessionToken,
	}
}

// ---------------------------------------------------------------------------
// The happy path
// ---------------------------------------------------------------------------

func TestOAuthAuthorizationCodeFlowIssuesAUsableToken(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "oauth-owner@example.com", models.RoleAdmin)

	if !models.ValidOAuthAccessTokenShape(grant.AccessToken) {
		t.Errorf("access token %q is not the documented shape", grant.AccessToken)
	}
	if !models.ValidOAuthRefreshTokenShape(grant.RefreshToken) {
		t.Errorf("refresh token is not the documented shape")
	}
	// The granted scope is the EXPANDED set, stated rather than inferred.
	if grant.Scope != models.ScopeSitesRead+" "+models.ScopeSitesWrite {
		t.Errorf("granted scope = %q", grant.Scope)
	}

	// The token reaches the resource API, as the same bearer header an API key
	// uses, and sees this company's sites.
	status, _, body, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites")
	if status != http.StatusOK {
		t.Fatalf("GET /sites with an access token = %d (%v)", status, body)
	}
	if len(listOf(t, body, "data")) != 2 {
		t.Errorf("company one has two sites; the grant saw %v", body["data"])
	}
}

func TestOAuthTokenResponseNeverLeaksBeyondTheProtocol(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "oauth-leak@example.com", models.RoleAdmin)

	// The stored form is a digest, not the token: a dump of the table contains
	// nothing presentable.
	var stored string
	scanRow(t, `SELECT token_hash FROM oauth_access_tokens WHERE token_hash = $1`,
		[]any{database.HashOAuthSecret(grant.AccessToken)}, &stored)
	if stored == grant.AccessToken {
		t.Fatal("the access token is stored in plaintext")
	}

	// Nothing in the audit trail carries a token or a code.
	rows, err := database.DB.Query(
		`SELECT action, coalesce(changes::text, '') FROM audit_events WHERE company_id = $1`,
		grant.CompanyID)
	if err != nil {
		t.Fatalf("reading audit rows: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var action, changes string
		if err := rows.Scan(&action, &changes); err != nil {
			t.Fatalf("scanning audit row: %v", err)
		}
		seen[action] = true
		for _, secret := range []string{grant.AccessToken, grant.RefreshToken, oauthClientSecret} {
			if strings.Contains(changes, secret) {
				t.Fatalf("audit row %s carries a secret", action)
			}
		}
		for _, prefix := range []string{models.OAuthCodePrefix, models.OAuthAccessPrefix, models.OAuthRefreshPrefix} {
			if strings.Contains(changes, prefix) {
				t.Fatalf("audit row %s carries a %s value: %s", action, prefix, changes)
			}
		}
	}
	// Consent and issuance are separate records, because they have different
	// actors.
	for _, action := range []string{"OAUTH_CONSENT_GRANTED", "OAUTH_TOKEN_ISSUED"} {
		if !seen[action] {
			t.Errorf("no %s audit record was written", action)
		}
	}
}

// ---------------------------------------------------------------------------
// PKCE, the redirect URI and the client
// ---------------------------------------------------------------------------

func TestOAuthCodeExchangeRefusesTheWrongVerifier(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "pkce@example.com", models.RoleAdmin)

	_, challenge := pkcePair("right-verifier")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	wrong, _ := pkcePair("wrong-verifier")
	status, body := tokenCall(t, env, codeExchangeForm(code, wrong))
	if status != http.StatusBadRequest || body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("wrong verifier = %d %v, want 400 invalid_grant", status, body)
	}

	// AND THE CODE IS STILL SPENDABLE ONLY ONCE: a failed PKCE check must not
	// have consumed it, or a man in the middle could burn a legitimate code by
	// guessing. The right verifier still works.
	right, _ := pkcePair("right-verifier")
	if status, body := tokenCall(t, env, codeExchangeForm(code, right)); status != http.StatusOK {
		t.Fatalf("the correct verifier after a failed attempt = %d (%v)", status, body)
	}
}

func TestOAuthCodeExchangeRefusesAnotherRedirectURI(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	seedOAuthClient(t, oauthClientID, oauthClientSecret,
		[]string{oauthRedirectURI, "https://app.datavase.example/other"},
		[]string{models.ScopeSitesRead, models.ScopeSitesWrite})
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "redirect@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("redirect-verifier")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	// Registered for this client, but not the one the code was bound to.
	form := codeExchangeForm(code, verifier)
	form.Set("redirect_uri", "https://app.datavase.example/other")
	if status, body := tokenCall(t, env, form); status != http.StatusBadRequest ||
		body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("mismatched redirect_uri = %d %v, want 400 invalid_grant", status, body)
	}
}

func TestOAuthAuthorizeRefusesAnUnregisteredRedirectWithoutRedirecting(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)

	_, challenge := pkcePair("open-redirect")
	q := authorizeQuery(challenge, map[string]string{
		"redirect_uri": "https://attacker.example/steal",
	})
	page, _ := consentPage(t, env, "", q)

	if page.Code != http.StatusBadRequest {
		t.Fatalf("an unregistered redirect_uri = %d, want 400", page.Code)
	}
	if location := page.Header().Get("Location"); location != "" {
		t.Fatalf("an unregistered redirect_uri produced a redirect to %q; that is an open redirector", location)
	}
	if strings.Contains(page.Body.String(), "attacker.example") {
		t.Error("the page reflects the attacker's address back at the reader")
	}
}

func TestOAuthAuthorizeRefusesAnUnknownClientWithoutRedirecting(t *testing.T) {
	env := newTestEnv(t)
	defaultOAuthClient(t)

	_, challenge := pkcePair("unknown-client")
	q := authorizeQuery(challenge, map[string]string{"client_id": "not-configured"})
	page, _ := consentPage(t, env, "", q)

	if page.Code != http.StatusBadRequest || page.Header().Get("Location") != "" {
		t.Fatalf("an unknown client = %d, Location %q", page.Code, page.Header().Get("Location"))
	}
}

func TestOAuthCodeCannotBeExchangedByAnotherClient(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	seedOAuthClient(t, "otherapp", "another-secret-entirely",
		[]string{"https://other.example/callback"}, []string{models.ScopeSitesRead})

	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "wrongclient@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("wrong-client")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	form := codeExchangeForm(code, verifier)
	form.Set("client_id", "otherapp")
	form.Set("client_secret", "another-secret-entirely")
	if status, body := tokenCall(t, env, form); status != http.StatusBadRequest ||
		body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("another client's exchange = %d %v, want 400 invalid_grant", status, body)
	}

	// AND THE LEGITIMATE CLIENT'S CODE SURVIVES. A wrong-client presentation
	// must not be a way to destroy somebody else's pending connection.
	if status, body := tokenCall(t, env, codeExchangeForm(code, verifier)); status != http.StatusOK {
		t.Fatalf("the rightful client after a wrong-client attempt = %d (%v)", status, body)
	}
}

func TestOAuthClientSecretIsRequiredAndCheckedConstantTime(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "secret@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("client-secret")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	form := codeExchangeForm(code, verifier)
	form.Set("client_secret", "not-the-secret")
	status, body := tokenCall(t, env, form)
	if status != http.StatusUnauthorized || body["error"] != models.OAuthErrInvalidClient {
		t.Fatalf("a wrong client secret = %d %v, want 401 invalid_client", status, body)
	}
}

// ---------------------------------------------------------------------------
// Single use, expiry and replay
// ---------------------------------------------------------------------------

func TestOAuthCodeIsSingleUseAndAReplayRevokesWhatItIssued(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "replay@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("replay-verifier")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	status, first := tokenCall(t, env, codeExchangeForm(code, verifier))
	if status != http.StatusOK {
		t.Fatalf("first exchange = %d (%v)", status, first)
	}
	accessToken, _ := first["access_token"].(string)

	// The second presentation is refused...
	status, body := tokenCall(t, env, codeExchangeForm(code, verifier))
	if status != http.StatusBadRequest || body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("a replayed code = %d %v, want 400 invalid_grant", status, body)
	}

	// ...AND the tokens the first exchange produced are revoked, because the
	// server cannot tell which presentation was the legitimate one.
	if got, _, _, _ := publicGet(t, env, accessToken, "/api/public/v1/sites"); got != http.StatusUnauthorized {
		t.Fatalf("the first exchange's token still works after a replay: %d", got)
	}
	var reason string
	scanRow(t, `SELECT revoked_reason FROM oauth_access_tokens WHERE token_hash = $1`,
		[]any{database.HashOAuthSecret(accessToken)}, &reason)
	if reason != models.OAuthRevokedCodeReplay {
		t.Errorf("revoked_reason = %q, want %s", reason, models.OAuthRevokedCodeReplay)
	}
}

func TestOAuthExpiredCodeIsRefused(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "expired@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("expiry-verifier")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	// The clock, moved rather than waited for. BOTH timestamps move: the
	// column CHECK is expires_at > created_at, so a row cannot be made to look
	// born-expired -- what is modelled is a code issued an hour ago that has
	// since lapsed.
	if _, err := database.DB.Exec(
		`UPDATE oauth_authorization_codes
		    SET created_at = CURRENT_TIMESTAMP - INTERVAL '1 hour',
		        expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
		  WHERE code_hash = $1`, database.HashOAuthSecret(code)); err != nil {
		t.Fatalf("expiring the code: %v", err)
	}

	if status, body := tokenCall(t, env, codeExchangeForm(code, verifier)); status != http.StatusBadRequest ||
		body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("an expired code = %d %v, want 400 invalid_grant", status, body)
	}
}

func TestOAuthAccessTokenStopsWorkingWhenItExpires(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "tokenexpiry@example.com", models.RoleAdmin)

	if status, _, _, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites"); status != http.StatusOK {
		t.Fatalf("the fresh token did not work: %d", status)
	}
	if _, err := database.DB.Exec(
		`UPDATE oauth_access_tokens
		    SET created_at = CURRENT_TIMESTAMP - INTERVAL '2 hours',
		        expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'
		  WHERE token_hash = $1`, database.HashOAuthSecret(grant.AccessToken)); err != nil {
		t.Fatalf("expiring the token: %v", err)
	}

	status, headers, body, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites")
	if code := publicError(t, status, headers, body, http.StatusUnauthorized); code != models.CodeCredentialInvalid {
		t.Errorf("an expired access token: code %s", code)
	}
}

func TestOAuthRevokedTokenStopsWorkingImmediately(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "revoke@example.com", models.RoleAdmin)

	form := url.Values{}
	form.Set("token", grant.RefreshToken)
	form.Set("client_id", oauthClientID)
	form.Set("client_secret", oauthClientSecret)
	req := httptest.NewRequest(http.MethodPost, oauthRevokeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revocation = %d (%s)", w.Code, w.Body.String())
	}

	// Revoking the refresh token ends the whole connection, access token
	// included -- a customer saying "disconnect this" means the connection.
	if status, _, _, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites"); status != http.StatusUnauthorized {
		t.Errorf("the access token survived the revocation: %d", status)
	}
	if status, body := tokenCall(t, env, refreshForm(grant.RefreshToken)); status != http.StatusBadRequest ||
		body["error"] != models.OAuthErrInvalidGrant {
		t.Errorf("the refresh token survived the revocation: %d %v", status, body)
	}

	// RFC 7009 section 2.2: an unknown token is still a 200.
	form.Set("token", "atr_live_"+strings.Repeat("0", 64))
	req = httptest.NewRequest(http.MethodPost, oauthRevokeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("revoking an unknown token = %d, want 200", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Refresh rotation and reuse detection
// ---------------------------------------------------------------------------

func refreshForm(refreshToken string) url.Values {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", oauthClientID)
	form.Set("client_secret", oauthClientSecret)
	return form
}

func TestOAuthRefreshRotatesTheToken(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "rotate@example.com", models.RoleAdmin)

	status, body := tokenCall(t, env, refreshForm(grant.RefreshToken))
	if status != http.StatusOK {
		t.Fatalf("refresh = %d (%v)", status, body)
	}
	rotated, _ := body["refresh_token"].(string)
	access, _ := body["access_token"].(string)
	if rotated == "" || rotated == grant.RefreshToken {
		t.Fatalf("the refresh token did not rotate: %q", rotated)
	}
	if access == grant.AccessToken {
		t.Fatal("the access token did not change")
	}
	if status, _, _, _ := publicGet(t, env, access, "/api/public/v1/sites"); status != http.StatusOK {
		t.Errorf("the refreshed access token does not work: %d", status)
	}

	// THE FAMILY'S DEADLINE IS NOT EXTENDED BY ROTATION: the new token expires
	// when the original would have, not sixty days from now.
	var original, replacement time.Time
	scanRow(t, `SELECT expires_at FROM oauth_refresh_tokens WHERE token_hash = $1`,
		[]any{database.HashOAuthSecret(grant.RefreshToken)}, &original)
	scanRow(t, `SELECT expires_at FROM oauth_refresh_tokens WHERE token_hash = $1`,
		[]any{database.HashOAuthSecret(rotated)}, &replacement)
	if !replacement.Equal(original) {
		t.Errorf("rotation moved the family deadline from %s to %s", original, replacement)
	}
}

func TestOAuthRefreshReuseRevokesTheWholeFamily(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "reuse@example.com", models.RoleAdmin)

	status, body := tokenCall(t, env, refreshForm(grant.RefreshToken))
	if status != http.StatusOK {
		t.Fatalf("first refresh = %d (%v)", status, body)
	}
	rotated, _ := body["refresh_token"].(string)
	liveAccess, _ := body["access_token"].(string)

	// The retired token, presented again. Two parties hold tokens from one
	// chain; the server cannot tell which is the thief, so it ends both.
	status, body = tokenCall(t, env, refreshForm(grant.RefreshToken))
	if status != http.StatusBadRequest || body["error"] != models.OAuthErrInvalidGrant {
		t.Fatalf("a reused refresh token = %d %v, want 400 invalid_grant", status, body)
	}

	if status, body := tokenCall(t, env, refreshForm(rotated)); status != http.StatusBadRequest {
		t.Errorf("the successor survived reuse detection: %d %v", status, body)
	}
	if status, _, _, _ := publicGet(t, env, liveAccess, "/api/public/v1/sites"); status != http.StatusUnauthorized {
		t.Errorf("an access token in the family survived reuse detection: %d", status)
	}

	var reason string
	scanRow(t, `SELECT revoked_reason FROM oauth_refresh_tokens WHERE token_hash = $1`,
		[]any{database.HashOAuthSecret(rotated)}, &reason)
	if reason != models.OAuthRevokedReuse {
		t.Errorf("revoked_reason = %q, want %s", reason, models.OAuthRevokedReuse)
	}
}

func TestOAuthRefreshCanNarrowButNotWidenAGrant(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "narrow@example.com", models.RoleAdmin)

	form := refreshForm(grant.RefreshToken)
	form.Set("scope", models.ScopeSitesRead)
	status, body := tokenCall(t, env, form)
	if status != http.StatusOK {
		t.Fatalf("narrowing refresh = %d (%v)", status, body)
	}
	if body["scope"] != models.ScopeSitesRead {
		t.Fatalf("narrowed scope = %v, want %s", body["scope"], models.ScopeSitesRead)
	}
	narrowed, _ := body["access_token"].(string)

	// The narrowed token can read and cannot write.
	if status, _, _, _ := publicGet(t, env, narrowed, "/api/public/v1/sites"); status != http.StatusOK {
		t.Errorf("the narrowed token cannot read: %d", status)
	}
	status, _, body, _ = publicCall(t, env, narrowed, http.MethodPost, "/api/public/v1/sites",
		`{"name":"Nope","country":"NG","timezone":"Africa/Lagos"}`, "")
	if status != http.StatusForbidden {
		t.Errorf("the narrowed token can still write: %d (%v)", status, body)
	}

	// Widening is refused rather than silently ignored. The ORIGINAL refresh
	// token is used: the narrowing refresh above rotated it, and presenting a
	// rotated one would be reuse rather than a widening attempt.
	form = refreshForm(grant.RefreshToken)
	form.Set("scope", models.ScopeMembersRead)
	if status, body := tokenCall(t, env, form); status == http.StatusOK {
		t.Errorf("a refresh widened the grant: %v", body)
	}
}

// ---------------------------------------------------------------------------
// Scope restriction at consent
// ---------------------------------------------------------------------------

func TestOAuthConsentCannotGrantBeyondTheClientRegistration(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	// Registered for reads only.
	seedOAuthClient(t, oauthClientID, oauthClientSecret,
		[]string{oauthRedirectURI}, []string{models.ScopeSitesRead})
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "clientscope@example.com", models.RoleAdmin)

	_, challenge := pkcePair("client-scope")
	q := authorizeQuery(challenge, map[string]string{"scope": models.ScopeSitesWrite})
	page, _ := consentPage(t, env, sessionToken, q)

	if page.Code != http.StatusSeeOther {
		t.Fatalf("asking for an unregistered scope = %d, want a redirect", page.Code)
	}
	location, _ := url.Parse(page.Header().Get("Location"))
	if location.Query().Get("error") != models.OAuthErrInvalidScope {
		t.Fatalf("error = %q, want invalid_scope", location.Query().Get("error"))
	}
}

func TestOAuthConsentCannotGrantBeyondTheOperatorsOwnRole(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	// A MANAGER. sites:write needs an ADMIN, exactly as creating a site in the
	// console does.
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "manager@example.com", models.RoleManager)

	_, challenge := pkcePair("role-bound")
	q := authorizeQuery(challenge, nil)
	page, formToken := consentPage(t, env, sessionToken, q)

	if page.Code != http.StatusOK {
		t.Fatalf("the consent page = %d", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, "does not have permission") {
		t.Fatalf("a manager was not told they cannot grant this: %s", body)
	}
	// And allowing anyway is refused rather than quietly narrowed.
	w := submitConsent(t, env, sessionToken, formToken, "allow", q)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("submitting anyway = %d", w.Code)
	}
	location, _ := url.Parse(w.Header().Get("Location"))
	if location.Query().Get("code") != "" {
		t.Fatal("a manager was issued a code for sites:write")
	}
}

func TestOAuthConsentIsNarrowedToTheOperatorsSiteGrants(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	seedOAuthClient(t, oauthClientID, oauthClientSecret,
		[]string{oauthRedirectURI}, []string{models.ScopeSitesRead})

	companyID := operatorCompanyID(t, "one")
	user, sessionToken, _ := consoleOperatorSession(t, env.router, companyID,
		"scoped-viewer@example.com", models.RoleViewer)

	// Granted Site A only. An ADMIN would reach both.
	var siteA int64
	scanRow(t, `SELECT id FROM sites WHERE site_name = 'Site A'`, nil, &siteA)
	if _, err := database.DB.Exec(
		`INSERT INTO user_site_grants (user_id, site_id) VALUES ($1, $2)`, user.ID, siteA); err != nil {
		t.Fatalf("granting a site: %v", err)
	}

	verifier, challenge := pkcePair("site-narrow")
	q := authorizeQuery(challenge, map[string]string{"scope": models.ScopeSitesRead})
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	status, body := tokenCall(t, env, codeExchangeForm(code, verifier))
	if status != http.StatusOK {
		t.Fatalf("exchange = %d (%v)", status, body)
	}
	access, _ := body["access_token"].(string)

	status, _, listed, _ := publicGet(t, env, access, "/api/public/v1/sites")
	if status != http.StatusOK {
		t.Fatalf("GET /sites = %d", status)
	}
	data := listOf(t, listed, "data")
	if len(data) != 1 {
		t.Fatalf("the grant saw %d sites; its owner can reach one", len(data))
	}
	if name := data[0].(map[string]any)["name"]; name != "Site A" {
		t.Errorf("the grant saw %v", name)
	}
}

// ---------------------------------------------------------------------------
// Tenant isolation
// ---------------------------------------------------------------------------

func TestOAuthTokenSeesOnlyItsOwnCompany(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "tenant-one@example.com", models.RoleAdmin)

	// Company two's site, addressed by its real public id.
	var otherSite string
	scanRow(t, `SELECT public_id::text FROM sites WHERE site_name = 'Site C'`, nil, &otherSite)

	status, headers, body, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites/"+otherSite)
	if code := publicError(t, status, headers, body, http.StatusNotFound); code != models.CodeResourceNotFound {
		t.Errorf("another company's site: code %s, want resource_not_found", code)
	}

	// And a write cannot reach it either.
	status, _, body, _ = publicCall(t, env, grant.AccessToken, http.MethodPatch,
		"/api/public/v1/sites/"+otherSite, `{"active":false}`, "")
	if status != http.StatusNotFound {
		t.Errorf("PATCH on another company's site = %d, want 404", status)
	}
	var stillActive bool
	scanRow(t, `SELECT active FROM sites WHERE public_id::text = $1`, []any{otherSite}, &stillActive)
	if !stillActive {
		t.Fatal("a cross-company PATCH changed the other company's site")
	}
}

// ---------------------------------------------------------------------------
// What must NOT authenticate
// ---------------------------------------------------------------------------

func TestOAuthResourceRoutesRefuseAMissingOrMalformedToken(t *testing.T) {
	env := newTestEnv(t)

	cases := map[string]string{
		"missing":     "",
		"nonsense":    "not-a-token",
		"wrong class": "atp_live_" + strings.Repeat("0", 64),
		"short":       models.OAuthAccessPrefix + "live_abc",
		"unknown":     models.OAuthAccessPrefix + "live_" + strings.Repeat("0", 64),
	}
	for name, token := range cases {
		status, headers, body, _ := publicGet(t, env, token, "/api/public/v1/sites")
		code := publicError(t, status, headers, body, http.StatusUnauthorized)
		want := models.CodeCredentialInvalid
		if name == "missing" {
			want = models.CodeCredentialMissing
		}
		if code != want {
			t.Errorf("%s token: code %s, want %s", name, code, want)
		}
	}
}

func TestConsoleSessionCookieCannotAuthenticateTheResourceAPI(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, csrf := consoleOperatorSession(t, env.router, companyID,
		"cookie@example.com", models.RoleOwner)

	name, _ := middleware.SessionCookieConfig()
	for _, path := range []string{"/api/public/v1/sites", "/api/public/v1/members"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: name, Value: sessionToken})
		req.Header.Set(middleware.CSRFHeader, csrf)
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s with an operator session cookie = %d, want 401", path, w.Code)
		}
	}

	// And the write routes, where a cookie plus CSRF is exactly what the
	// console sends -- so this is the case a mis-mounted route would fail.
	req := httptest.NewRequest(http.MethodPost, "/api/public/v1/sites",
		strings.NewReader(`{"name":"Cookie Site","country":"NG","timezone":"Africa/Lagos"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: name, Value: sessionToken})
	req.Header.Set(middleware.CSRFHeader, csrf)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /sites with a session cookie = %d, want 401", w.Code)
	}
	var created int
	scanRow(t, `SELECT count(*) FROM sites WHERE site_name = 'Cookie Site'`, nil, &created)
	if created != 0 {
		t.Fatal("a session cookie created a site through the public API")
	}
}

func TestOAuthBearerRequiresNoCSRFToken(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "nocsrf@example.com", models.RoleAdmin)

	// No X-CSRF-Token header anywhere in this request, and it is a write.
	status, _, body, _ := publicCall(t, env, grant.AccessToken, http.MethodPost,
		"/api/public/v1/sites",
		`{"name":"No CSRF Needed","country":"NG","timezone":"Africa/Lagos"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("a bearer write without CSRF = %d (%v)", status, body)
	}
}

// ---------------------------------------------------------------------------
// The consent form's own defences
// ---------------------------------------------------------------------------

func TestOAuthConsentRequiresTheFormTokenAndItsCookie(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "csrf-form@example.com", models.RoleAdmin)

	_, challenge := pkcePair("form-guard")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)

	// A cross-site post: the browser would send neither the nonce cookie nor a
	// token it cannot read. Re-rendered, never acted on.
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("action", "allow")
	req := httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	name, _ := middleware.SessionCookieConfig()
	req.AddCookie(&http.Cookie{Name: name, Value: sessionToken})
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code == http.StatusSeeOther {
		t.Fatalf("a consent post with no form token was acted on: %s", w.Header().Get("Location"))
	}
	var codes int
	scanRow(t, `SELECT count(*) FROM oauth_authorization_codes`, nil, &codes)
	if codes != 0 {
		t.Fatal("a consent post with no form token minted an authorization code")
	}

	// The same submission WITH the token and its cookie is honoured.
	if got := submitConsent(t, env, sessionToken, formToken, "allow", q); got.Code != http.StatusSeeOther {
		t.Fatalf("the legitimate submission = %d", got.Code)
	}
}

func TestOAuthConsentPagesCarryAStrictPolicyAndAreNotCached(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "csp@example.com", models.RoleAdmin)

	_, challenge := pkcePair("csp-check")
	page, _ := consentPage(t, env, sessionToken, authorizeQuery(challenge, nil))

	csp := page.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("the consent page's policy is missing %q: %q", want, csp)
		}
	}
	if page.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", page.Header().Get("Cache-Control"))
	}
	if strings.Contains(page.Body.String(), "<script") {
		t.Error("the consent page carries script")
	}
}

func TestOAuthSignInFormAppearsForASignedOutCustomer(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	mustCreateOperator(t, companyID, "signin@example.com", models.RoleAdmin)

	_, challenge := pkcePair("sign-in-flow")
	q := authorizeQuery(challenge, nil)

	page, formToken := consentPage(t, env, "", q)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Sign in to AccessLink") {
		t.Fatalf("a signed-out customer did not get the sign-in form: %d", page.Code)
	}

	// Signing in shows the CONSENT screen. Signing in is not consenting.
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("form_token", formToken)
	form.Set("action", "signin")
	form.Set("email", "signin@example.com")
	form.Set("password", testPassword)

	req := httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	nonceName, _ := oauthNonceCookieName()
	req.AddCookie(&http.Cookie{Name: nonceName, Value: formToken})
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("sign-in = %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Connect Datavase?") {
		t.Fatalf("signing in did not show the consent screen: %s", w.Body.String())
	}
	var codes int
	scanRow(t, `SELECT count(*) FROM oauth_authorization_codes`, nil, &codes)
	if codes != 0 {
		t.Fatal("signing in minted an authorization code without consent")
	}

	// A wrong password says the same thing an unknown address would.
	form.Set("password", "not-the-password")
	req = httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: nonceName, Value: formToken})
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "do not match") {
		t.Errorf("a wrong password did not re-render the form with a message: %s", w.Body.String())
	}
}

func TestOAuthDeclinedConsentReturnsAccessDeniedAndMintsNothing(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "decline@example.com", models.RoleAdmin)

	_, challenge := pkcePair("decline-flow")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)

	w := submitConsent(t, env, sessionToken, formToken, "deny", q)
	_, state, oerr := codeFromRedirect(t, w)
	if oerr != models.OAuthErrAccessDenied {
		t.Fatalf("declining = %q, want access_denied", oerr)
	}
	if state != "state-abc-123" {
		t.Errorf("state was not echoed on a refusal: %q", state)
	}
	var codes int
	scanRow(t, `SELECT count(*) FROM oauth_authorization_codes`, nil, &codes)
	if codes != 0 {
		t.Fatal("declining minted an authorization code")
	}
}

// ---------------------------------------------------------------------------
// Protocol parameters
// ---------------------------------------------------------------------------

func TestOAuthAuthorizeRefusesAnythingButCodeAndS256(t *testing.T) {
	env := newTestEnv(t)
	defaultOAuthClient(t)
	_, challenge := pkcePair("param-checks")

	cases := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"implicit flow", map[string]string{"response_type": "token"}, models.OAuthErrUnsupportedResponseType},
		{"plain PKCE", map[string]string{"code_challenge_method": "plain"}, models.OAuthErrInvalidRequest},
		{"no challenge", map[string]string{"code_challenge": ""}, models.OAuthErrInvalidRequest},
		{"short challenge", map[string]string{"code_challenge": "too-short"}, models.OAuthErrInvalidRequest},
		{"unknown scope", map[string]string{"scope": "doors:open"}, models.OAuthErrInvalidScope},
		// REFUSED, NOT TRUNCATED. Reading the first n characters would grant a
		// prefix of what was asked for and say nothing about it.
		{"oversized scope", map[string]string{
			"scope": strings.Repeat("sites:read ", 40),
		}, models.OAuthErrInvalidRequest},
	}
	for _, tc := range cases {
		page, _ := consentPage(t, env, "", authorizeQuery(challenge, tc.overrides))
		if page.Code != http.StatusSeeOther {
			t.Errorf("%s = %d, want a redirect carrying %s", tc.name, page.Code, tc.want)
			continue
		}
		location, _ := url.Parse(page.Header().Get("Location"))
		if got := location.Query().Get("error"); got != tc.want {
			t.Errorf("%s: error = %q, want %q", tc.name, got, tc.want)
		}
		if location.Query().Get("state") != "state-abc-123" {
			t.Errorf("%s: state was not echoed", tc.name)
		}
	}
}

func TestOAuthTokenEndpointRefusesAnUnsupportedGrantType(t *testing.T) {
	env := newTestEnv(t)
	defaultOAuthClient(t)

	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", oauthClientID)
	form.Set("client_secret", oauthClientSecret)
	status, body := tokenCall(t, env, form)
	if status != http.StatusBadRequest || body["error"] != models.OAuthErrUnsupportedGrantType {
		t.Fatalf("grant_type=password = %d %v", status, body)
	}

	// The resource-owner password grant is not merely unimplemented: there is
	// no path here that takes an end user's password for a machine.
	if strings.Contains(fmt.Sprint(body), "password") &&
		!strings.Contains(fmt.Sprint(body["error_description"]), "authorization_code") {
		t.Errorf("the refusal does not say what is supported: %v", body)
	}
}

func TestOAuthClientSecretMayBeSentAsBasicCredentials(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	_, sessionToken, _ := consoleOperatorSession(t, env.router, companyID, "basic@example.com", models.RoleAdmin)

	verifier, challenge := pkcePair("basic-auth")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, sessionToken, q)
	code, _, _ := codeFromRedirect(t, submitConsent(t, env, sessionToken, formToken, "allow", q))

	form := codeExchangeForm(code, verifier)
	form.Del("client_id")
	form.Del("client_secret")

	req := httptest.NewRequest(http.MethodPost, oauthTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString(
		[]byte(url.QueryEscape(oauthClientID)+":"+url.QueryEscape(oauthClientSecret))))
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("client_secret_basic = %d (%s)", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The grant follows its owner
// ---------------------------------------------------------------------------

func TestOAuthGrantDiesWithTheOperatorWhoMadeIt(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "leaver@example.com", models.RoleAdmin)

	if status, _, _, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites"); status != http.StatusOK {
		t.Fatalf("the fresh grant did not work")
	}

	// The operator is deactivated. A grant is one person lending their access
	// to a machine; when the person is gone, the lending stops -- and nothing
	// had to remember that tokens exist.
	if _, err := database.DB.Exec(
		`UPDATE users SET active = FALSE WHERE email = $1`, grant.UserEmail); err != nil {
		t.Fatalf("deactivating the operator: %v", err)
	}
	if status, _, _, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites"); status != http.StatusUnauthorized {
		t.Fatalf("the grant survived its owner's deactivation: %d", status)
	}
}

// ---------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------

func TestOAuthHousekeepingKeepsReplayEvidence(t *testing.T) {
	env := newTestEnv(t)
	grant := connectDatavase(t, env, "one", "sweep@example.com", models.RoleAdmin)

	// A code consumed a minute ago has expired, and must NOT be swept: it is
	// what a replay is detected against.
	if _, err := database.DB.Exec(
		`UPDATE oauth_authorization_codes
		    SET created_at = CURRENT_TIMESTAMP - INTERVAL '2 hours',
		        expires_at = CURRENT_TIMESTAMP - INTERVAL '1 hour'`); err != nil {
		t.Fatalf("ageing the code: %v", err)
	}
	if _, err := database.PurgeExpiredOAuthGrants(t.Context()); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	var codes int
	scanRow(t, `SELECT count(*) FROM oauth_authorization_codes`, nil, &codes)
	if codes != 1 {
		t.Fatalf("the sweep removed a code that is still replay evidence")
	}

	// Beyond the evidence window it goes.
	if _, err := database.DB.Exec(
		`UPDATE oauth_authorization_codes
		    SET created_at = CURRENT_TIMESTAMP - INTERVAL '31 days',
		        expires_at = CURRENT_TIMESTAMP - INTERVAL '30 days'`); err != nil {
		t.Fatalf("ageing the code: %v", err)
	}
	if _, err := database.PurgeExpiredOAuthGrants(t.Context()); err != nil {
		t.Fatalf("sweeping: %v", err)
	}
	scanRow(t, `SELECT count(*) FROM oauth_authorization_codes`, nil, &codes)
	if codes != 0 {
		t.Fatalf("the sweep kept %d long-expired code(s)", codes)
	}

	// The live access token is untouched by any of it.
	if status, _, _, _ := publicGet(t, env, grant.AccessToken, "/api/public/v1/sites"); status != http.StatusOK {
		t.Errorf("the sweep broke a live grant: %d", status)
	}
}

// ---------------------------------------------------------------------------
// The phase boundary on what an OAuth client may be configured for
// ---------------------------------------------------------------------------

// A GRANT'S CEILING IS NARROWER THAN A CREDENTIAL'S, and the difference is
// enforced rather than intended. members:write is a registered scope an
// administrator may put on an API key; it is NOT available to an OAuth client
// in this version, because a grant cannot key an Idempotency-Key record and a
// member create is not retry-safe without one. See models.OAuthGrantableScopes.
func TestOAuthClientCannotBeConfiguredBeyondThisPhase(t *testing.T) {
	newTestEnv(t)

	_, err := database.UpsertOAuthClient(database.OAuthClientConfig{
		ClientID:     "overreach",
		Name:         "Overreach",
		RedirectURIs: []string{"https://overreach.example/callback"},
		Scopes:       []string{models.ScopeSitesRead, models.ScopeMembersWrite},
	})
	if err == nil {
		t.Fatal("a client was configured with members:write")
	}
	if !errors.Is(err, models.ErrOAuthScopeNotGrantable) {
		t.Fatalf("configuring members:write = %v, want ErrOAuthScopeNotGrantable", err)
	}
	var rows int
	scanRow(t, `SELECT count(*) FROM oauth_clients WHERE client_id = 'overreach'`, nil, &rows)
	if rows != 0 {
		t.Fatal("the refused client was written anyway")
	}

	// The check is against the EXPANDED set, so an implication cannot smuggle
	// one in: members:write implies members:read, and neither is grantable.
	if _, err := database.UpsertOAuthClient(database.OAuthClientConfig{
		ClientID:     "overreach",
		Name:         "Overreach",
		RedirectURIs: []string{"https://overreach.example/callback"},
		Scopes:       []string{models.ScopeMembersRead},
	}); !errors.Is(err, models.ErrOAuthScopeNotGrantable) {
		t.Fatalf("configuring members:read = %v, want ErrOAuthScopeNotGrantable", err)
	}

	// The phase's own scopes are accepted, so the rule is a boundary and not a
	// blanket refusal.
	if _, err := database.UpsertOAuthClient(database.OAuthClientConfig{
		ClientID:     "withinphase",
		Name:         "Within Phase",
		RedirectURIs: []string{"https://within.example/callback"},
		Scopes:       []string{models.ScopeSitesWrite},
	}); err != nil {
		t.Fatalf("configuring sites:write = %v, want success", err)
	}
}

// ---------------------------------------------------------------------------
// The sign-in form draws on the console's own allowance
// ---------------------------------------------------------------------------

// A PASSWORD FORM MUST NOT BE A SECOND BUDGET. LoginRateLimiter is ONE shared
// allowance across login, registration, password change and the handover
// routes, for the reason its own comment gives: an attacker must not get a
// second budget by alternating between them. The consent page's sign-in is
// another such surface, so it spends the same tokens -- otherwise adding a
// connect flow would have doubled the password attempts one address gets.
func TestOAuthSignInSpendsTheSharedLoginAllowance(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	mustCreateOperator(t, companyID, "budget@example.com", models.RoleAdmin)

	// Drain the allowance on the CONSOLE's login route, against an address
	// that owns no account -- so what runs out is the per-address bucket and
	// not one account's lockout.
	drained := false
	for i := 0; i < 40 && !drained; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(`{"email":"nobody@example.com","password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			drained = true
		}
	}
	if !drained {
		t.Fatal("the console login allowance never ran out; this test is not testing what it says")
	}

	// The consent page's sign-in is refused from the same address, with the
	// correct password, because the budget is shared.
	_, challenge := pkcePair("shared-budget")
	q := authorizeQuery(challenge, nil)
	_, formToken := consentPage(t, env, "", q)

	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("form_token", formToken)
	form.Set("action", "signin")
	form.Set("email", "budget@example.com")
	form.Set("password", testPassword)

	req := httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	nonceName, _ := oauthNonceCookieName()
	req.AddCookie(&http.Cookie{Name: nonceName, Value: formToken})
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if !strings.Contains(w.Body.String(), "Too many sign-in attempts") {
		t.Fatalf("the consent page's sign-in did not draw on the console allowance: %d %s",
			w.Code, w.Body.String())
	}
	// And no session was opened.
	sessionName, _ := middleware.SessionCookieConfig()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionName && cookie.Value != "" {
			t.Fatal("a refused sign-in still opened a session")
		}
	}
}
