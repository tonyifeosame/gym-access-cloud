package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// What Phase 3A must NOT have changed.
//
// ---------------------------------------------------------------------------
// WHY THESE ARE HERE RATHER THAN TRUSTED TO THE SUITES THEY DUPLICATE
// ---------------------------------------------------------------------------
//
// 037 touched three things that shipped behaviour already depends on: the
// session cookie (the consent page resolves one and the sign-in form creates
// one), the console's site projection (the public one grew a field), and the
// scope registry (the console issues credentials from it). Each of those has a
// suite of its own that would eventually catch a break -- but each would catch
// it as a failure in an unrelated file, and the point of this one is that the
// diff which caused it is the diff being reviewed.
//
// Nothing here is a new assertion about how the console works. Every one of
// them is a restatement of behaviour that existed before this change, chosen
// because this change could plausibly have broken it.

// ---------------------------------------------------------------------------
// Console authentication
// ---------------------------------------------------------------------------

func TestConsoleSessionAndCSRFAreUnchangedByTheAuthorizationServer(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"console-unchanged@example.com", models.RoleAdmin)

	// A GET with the cookie alone still works.
	if code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/sites", "", token, ""); code != http.StatusOK {
		t.Fatalf("console GET with a session cookie = %d (%v)", code, body)
	}

	// An unsafe method still REQUIRES the CSRF header.
	code, _ := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/sites",
		`{"name":"CSRF Check","timezone":"Africa/Lagos"}`, token, "")
	if code != http.StatusForbidden {
		t.Fatalf("a console write with no CSRF token = %d, want 403", code)
	}

	// And works with it.
	code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/sites",
		`{"name":"CSRF Check","timezone":"Africa/Lagos"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("a console write with a CSRF token = %d (%v)", code, body)
	}

	// No session, no access -- the authorization server did not open a second
	// door into the console tree.
	if code, _ := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/sites", "", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("console GET with no session = %d, want 401", code)
	}
}

func TestConsoleSiteResponsesDidNotGrowAField(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"console-shape@example.com", models.RoleAdmin)

	// THE PUBLIC PROJECTION GREW `country`; THE CONSOLE'S DID NOT, and that is
	// deliberate -- the console does not ask for one, so reporting an empty
	// field on every site would be a change to a shipped response for no
	// benefit. database.TenantSite carries the value instead.
	code, created := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/sites",
		`{"name":"Console Shape","timezone":"Africa/Lagos"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("console site create = %d (%v)", code, created)
	}
	site, ok := created["site"].(map[string]any)
	if !ok {
		t.Fatalf("console create response has no site object: %v", created)
	}
	if _, present := site["country"]; present {
		t.Error("the console's site projection has grown a country field")
	}

	code, listed := consoleCall(t, env.router, http.MethodGet, "/api/v1/console/sites", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("console site list = %d", code)
	}
	for _, entry := range listOf(t, listed, "sites") {
		if _, present := entry.(map[string]any)["country"]; present {
			t.Fatal("the console's site list has grown a country field")
		}
	}
}

func TestOAuthSignInProducesAnOrdinaryConsoleSession(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	defaultOAuthClient(t)
	companyID := operatorCompanyID(t, "one")
	mustCreateOperator(t, companyID, "sso-session@example.com", models.RoleAdmin)

	_, challenge := pkcePair("ordinary-session")
	q := authorizeQuery(challenge, nil)
	page, formToken := consentPage(t, env, "", q)
	if page.Code != http.StatusOK {
		t.Fatalf("the sign-in page = %d", page.Code)
	}

	form := map[string][]string{}
	for k, v := range q {
		form[k] = v
	}
	body := strings.NewReader(urlValues(form, map[string]string{
		"form_token": formToken,
		"action":     "signin",
		"email":      "sso-session@example.com",
		"password":   testPassword,
	}))
	req := httptest.NewRequest(http.MethodPost, oauthAuthorizeURL, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	nonceName, _ := oauthNonceCookieName()
	req.AddCookie(&http.Cookie{Name: nonceName, Value: formToken})
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sign-in through the consent page = %d", w.Code)
	}

	// EXACTLY THE COOKIE THE CONSOLE SETS, through the same helper -- so a
	// customer who signs in here is signed in to the console, and one who signs
	// out there is signed out here. Two cookies for one session table is how a
	// sign-out stops working.
	sessionName, _ := middleware.SessionCookieConfig()
	var token string
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name != sessionName {
			continue
		}
		token = cookie.Value
		if !cookie.HttpOnly {
			t.Error("the session cookie set by the consent page is not HttpOnly")
		}
		if cookie.Path != "/" {
			t.Errorf("the session cookie's path is %q", cookie.Path)
		}
		if cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("the session cookie's SameSite is %v", cookie.SameSite)
		}
	}
	if token == "" {
		t.Fatalf("signing in through the consent page set no %s cookie", sessionName)
	}

	// It is a session the console accepts, and an ordinary row -- not a second
	// kind of session with its own rules.
	identity, err := database.AuthenticateSession(token)
	if err != nil || identity == nil {
		t.Fatalf("the session does not resolve: %v", err)
	}
	if code, _ := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/sites", "", token, ""); code != http.StatusOK {
		t.Fatalf("the console refused a session opened through the consent page: %d", code)
	}
}

// ---------------------------------------------------------------------------
// The scope registry the console issues from
// ---------------------------------------------------------------------------

func TestConsoleCanIssueTheNewScopeAndStillRefusesUnknownOnes(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"scope-issue@example.com", models.RoleAdmin)

	// THE REGISTRY AND THE DATABASE CHECK MUST AGREE. A scope Go knows and the
	// column refuses surfaces as a 500 on this endpoint, which is exactly the
	// failure the two closed sets exist to prevent -- so the new one is issued
	// here for real rather than asserted in a unit test.
	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/api-credentials",
		`{"name":"phase3a","scopes":["sites:write"]}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing sites:write = %d (%v)", code, body)
	}
	scopes := listOf(t, body, "scopes")
	if len(scopes) != 2 {
		t.Fatalf("sites:write was stored as %v; implication is expanded at issue time", scopes)
	}

	// And an unknown scope is still refused rather than dropped.
	code, _ = consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/api-credentials",
		`{"name":"invented","scopes":["sites:delete"]}`, token, csrf)
	if code != http.StatusBadRequest {
		t.Fatalf("an unknown scope = %d, want 400", code)
	}
}

// ---------------------------------------------------------------------------
// The assistant
// ---------------------------------------------------------------------------

func TestAssistantGainedNoRouteAndNoSiteWriteAbility(t *testing.T) {
	env := newTestEnv(t)

	// The assistant's route set is what Phase 2b left. 037 adds a public tree
	// and a consent page; neither is reachable from the assistant, and the
	// assistant gains nothing from either.
	found := map[string]bool{}
	for _, route := range env.router.Routes() {
		if strings.Contains(route.Path, "assistant") {
			found[route.Method+" "+route.Path] = true
		}
	}
	want := []string{
		"GET /api/v1/console/assistant/capabilities",
		"GET /api/v1/console/assistant/conversations",
		"GET /api/v1/console/assistant/conversations/:id",
		"POST /api/v1/console/assistant/conversations",
		"POST /api/v1/console/assistant/conversations/:id/messages",
		"POST /api/v1/console/assistant/conversations/:id/confirmations",
	}
	for _, route := range want {
		if !found[route] {
			t.Errorf("assistant route missing: %s", route)
		}
		delete(found, route)
	}
	for extra := range found {
		t.Errorf("the assistant gained a route: %s", extra)
	}

	// The assistant reaches the console tree and nothing else. It must not
	// have grown a way to reach the public tree, where the site writes now
	// live -- create_site and update_site remain outside its catalogue, which
	// assistant/registry_test.go holds, and there is no route here that would
	// let it try.
	for _, route := range env.router.Routes() {
		if strings.Contains(route.Path, "assistant") &&
			strings.HasPrefix(route.Path, "/api/public/") {
			t.Errorf("the assistant reached the public tree: %s %s", route.Method, route.Path)
		}
	}
}

// ---------------------------------------------------------------------------
// The two credential classes stay apart
// ---------------------------------------------------------------------------

func TestAnIntegrationCredentialCannotReachTheAuthorizationServer(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "keyholder@example.com")

	// The token endpoint is how a caller OBTAINS a credential. Presenting one
	// does not make the request an authorized one -- and must not be mistaken
	// for client authentication.
	req := httptest.NewRequest(http.MethodPost, oauthTokenURL,
		strings.NewReader("grant_type=authorization_code&code=atc_"+strings.Repeat("0", 64)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an API key at the token endpoint = %d, want 401 invalid_client", w.Code)
	}
	if !strings.Contains(w.Body.String(), models.OAuthErrInvalidClient) {
		t.Errorf("the refusal is not invalid_client: %s", w.Body.String())
	}
}

func TestTheAuthorizationServerIsOutsideTheBearerTree(t *testing.T) {
	env := newTestEnv(t)

	// No credential at all, and the authorize endpoint still answers -- it has
	// to, because a customer arriving from a third party has none. What it must
	// NOT do is answer with the bearer tree's 401 envelope, which would mean it
	// had been mounted behind the resource middleware by accident.
	req := httptest.NewRequest(http.MethodGet, oauthAuthorizeURL, nil)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("the authorize endpoint is behind the bearer middleware: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), models.CodeCredentialMissing) {
		t.Fatalf("the authorize endpoint answered with the resource API's envelope: %s", w.Body.String())
	}
	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("the authorize endpoint issued a bearer challenge: %q", got)
	}
}

// urlValues renders a form body with overrides applied, for the one test that
// builds one by hand.
func urlValues(base map[string][]string, overrides map[string]string) string {
	var parts []string
	for k, vs := range base {
		if _, replaced := overrides[k]; replaced {
			continue
		}
		for _, v := range vs {
			parts = append(parts, k+"="+urlEscape(v))
		}
	}
	for k, v := range overrides {
		parts = append(parts, k+"="+urlEscape(v))
	}
	return strings.Join(parts, "&")
}

func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
