package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Platform administration, /api/v1/platform/* (GP-01, CON-01).
//
// GP-01 was the audit's first blocker: NO API CREATED A COMPANY. `companies` had
// exactly one writer -- the INSERT in migration 002 -- so onboarding a second
// customer required somebody with SQL against production, and CON-01 was the
// same gap for updating one.
//
// These go through NewRouter, so the route table, the middleware chain and the
// handlers are exercised exactly as the server runs them. That matters more here
// than anywhere else in the suite: the entire safety argument for this surface is
// that it authenticates a DIFFERENT credential class, and a test that called the
// handler directly would prove nothing about it.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const testPlatformPassword = "platform-admin-password-1"

// mustCreatePlatformAdmin seeds the installation's administrator.
//
// Through CreateFirstPlatformAdmin rather than a raw INSERT, so the tests
// exercise the same "only on an empty installation" predicate the bootstrap
// relies on.
func mustCreatePlatformAdmin(t *testing.T, email string) *models.PlatformAdmin {
	t.Helper()
	admin, err := database.CreateFirstPlatformAdmin(models.NewPlatformAdmin{
		Email:    email,
		FullName: "Platform Administrator",
		Password: testPlatformPassword,
	})
	if err != nil {
		t.Fatalf("creating the first platform administrator: %v", err)
	}
	return admin
}

// platformLogin signs an administrator in and returns the session and CSRF
// tokens, the way a browser would obtain them.
func platformLogin(t *testing.T, router *gin.Engine, email, password string) (string, string) {
	t.Helper()

	code, body, res := doAuth(t, router, authCall{
		method: http.MethodPost, path: "/api/v1/platform/login",
		body: loginBody(email, password),
	})
	if code != http.StatusOK {
		t.Fatalf("platform login as %s = %d (%v)", email, code, body)
	}

	name, _ := middleware.PlatformCookieConfig()
	var token string
	for _, cookie := range res.Cookies() {
		if cookie.Name == name {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatalf("platform login set no %s cookie", name)
	}

	csrf, _ := body["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("platform login returned no csrf_token")
	}
	return token, csrf
}

// platformCall issues a request carrying a platform session cookie.
func platformCall(t *testing.T, router *gin.Engine, method, path, body, token,
	csrf string) (int, map[string]any) {
	t.Helper()

	name, _ := middleware.PlatformCookieConfig()
	code, decoded, _ := doAuth(t, router, authCall{
		method: method, path: path, body: body,
		token: token, cookieName: name, csrf: csrf,
	})
	return code, decoded
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding request body: %v", err)
	}
	return string(encoded)
}

// ---------------------------------------------------------------------------
// GP-01: a company can be created through the API
// ---------------------------------------------------------------------------

// THE TEST THAT WOULD HAVE CAUGHT THE BLOCKER. Before this surface existed there
// was no request a caller could make that produced a second company.
func TestPlatformCreatesACompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")

	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	code, body := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{
			"name":          "Northwind Logistics",
			"contact_email": "ops@northwind.example",
			"timezone":      "Europe/London",
		}), token, csrf)

	if code != http.StatusCreated {
		t.Fatalf("creating a company = %d (%v)", code, body)
	}
	if body["slug"] != "northwind-logistics" {
		t.Errorf("slug = %v, want it derived from the name", body["slug"])
	}
	if body["timezone"] != "Europe/London" {
		t.Errorf("timezone = %v, want Europe/London", body["timezone"])
	}
	if body["active"] != true {
		t.Errorf("a new company is not active: %v", body["active"])
	}

	// It is a real tenant, not just a response.
	if got := queryInt(t, `SELECT count(*) FROM companies WHERE slug = 'northwind-logistics'`); got != 1 {
		t.Errorf("companies with the new slug = %d, want 1", got)
	}
}

// The platform is general-purpose, and the slug derivation must not assume any
// particular kind of customer -- a school, a factory and a residential block all
// have to come out with a usable identifier.
func TestPlatformDerivesSlugsFromArbitraryNames(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Acme Logistics (UK)", "acme-logistics-uk"},
		{"St. Mary's School", "st-mary-s-school"},
		{"Werk 7 — Produktion", "werk-7-produktion"},
		{"  Riverside Apartments  ", "riverside-apartments"},
	}

	for _, tc := range cases {
		if got := models.NormalizeSlug(tc.name); got != tc.want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPlatformRefusesADuplicateSlug(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	// newTestEnv already seeded a company with the slug "one".
	code, body := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "Anything", "slug": "one"}), token, csrf)

	if code != http.StatusConflict {
		t.Fatalf("duplicate slug = %d (%v), want 409", code, body)
	}
}

// ---------------------------------------------------------------------------
// CON-01: a company can be updated
// ---------------------------------------------------------------------------

func TestPlatformUpdatesACompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	publicID := queryString(t, `SELECT public_id FROM companies WHERE slug = 'one'`)

	retention := 90
	code, body := platformCall(t, env.router, http.MethodPut,
		"/api/v1/platform/companies/"+publicID,
		jsonBody(t, map[string]any{
			"name":                 "Company One, Renamed",
			"event_retention_days": retention,
		}), token, csrf)

	if code != http.StatusOK {
		t.Fatalf("updating a company = %d (%v)", code, body)
	}
	if body["name"] != "Company One, Renamed" {
		t.Errorf("name = %v, want the new one", body["name"])
	}
	// THE SLUG IS NOT UPDATABLE and must survive a rename: it appears in the
	// bootstrap environment variable and in operator-facing URLs.
	if body["slug"] != "one" {
		t.Errorf("slug = %v, want it unchanged by a rename", body["slug"])
	}
	if got := queryInt(t,
		`SELECT event_retention_days FROM companies WHERE slug = 'one'`); got != retention {
		t.Errorf("event_retention_days = %d, want %d", got, retention)
	}
}

// DEACTIVATION IS THE SHARP ONE: a company with active=false fails the
// company_ok predicate in AuthenticateSession, so every operator session inside
// it stops resolving. Suspension, not deletion.
func TestDeactivatingACompanyStopsItsOperatorSessions(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")

	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "tenant@example.com", models.RoleOwner)
	operatorToken, _ := login(t, env.router, "tenant@example.com", testPassword)

	// The operator's session works before the tenant is suspended.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: operatorToken,
	}); code != http.StatusOK {
		t.Fatalf("operator /me before deactivation = %d, want 200", code)
	}

	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)
	publicID := queryString(t, `SELECT public_id FROM companies WHERE id = $1`, one)

	inactive := false
	code, body := platformCall(t, env.router, http.MethodPut,
		"/api/v1/platform/companies/"+publicID,
		jsonBody(t, map[string]any{
			"active":             inactive,
			"deactivated_reason": "unpaid invoice",
		}), token, csrf)
	if code != http.StatusOK {
		t.Fatalf("deactivating = %d (%v)", code, body)
	}

	// And now it does not.
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: operatorToken,
	}); code != http.StatusUnauthorized {
		t.Errorf("operator /me after deactivation = %d, want 401", code)
	}

	// Nothing was destroyed: the operator row is still there, and reactivation
	// is a request away.
	if got := queryInt(t,
		`SELECT count(*) FROM users WHERE id = $1 AND deleted_at IS NULL`, user.ID); got != 1 {
		t.Errorf("the operator row survived deactivation = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// The credential classes do not fall through to one another
// ---------------------------------------------------------------------------

// BOTH COOKIES ARE Path=/, so a browser genuinely sends each to the other's
// routes. This is the property the whole separation rests on, and it is checked
// in both directions.
func TestPlatformSessionIsNotAConsoleSession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, _ := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	// Presented under the OPERATOR cookie name, which is what a browser holding
	// both would do if the middleware read whichever it found.
	operatorCookie, _ := middleware.SessionCookieConfig()
	for _, path := range []string{
		"/api/v1/auth/me",
		"/api/v1/console/people",
		"/api/v1/console/operators",
		"/api/v1/console/audit",
	} {
		code, _, _ := doAuth(t, env.router, authCall{
			method: http.MethodGet, path: path,
			token: token, cookieName: operatorCookie,
		})
		if code != http.StatusUnauthorized {
			t.Errorf("platform token on %s = %d, want 401", path, code)
		}
	}
}

func TestConsoleSessionIsNotAPlatformSession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")

	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "owner@example.com", models.RoleOwner)
	operatorToken, csrf := login(t, env.router, "owner@example.com", testPassword)

	// An OWNER is the highest role a tenant has, and it reaches nothing here.
	platformCookie, _ := middleware.PlatformCookieConfig()
	for _, path := range []string{
		"/api/v1/platform/me",
		"/api/v1/platform/companies",
	} {
		code, _, _ := doAuth(t, env.router, authCall{
			method: http.MethodGet, path: path,
			token: operatorToken, cookieName: platformCookie, csrf: csrf,
		})
		if code != http.StatusUnauthorized {
			t.Errorf("operator token on %s = %d, want 401", path, code)
		}
	}

	// And a company cannot be created with it either.
	code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/platform/companies",
		body:  jsonBody(t, map[string]string{"name": "Smuggled Tenant"}),
		token: operatorToken, cookieName: platformCookie, csrf: csrf,
	})
	if code != http.StatusUnauthorized {
		t.Errorf("operator creating a company = %d, want 401", code)
	}
	if got := queryInt(t, `SELECT count(*) FROM companies WHERE name = 'Smuggled Tenant'`); got != 0 {
		t.Errorf("a company was created by an operator session: %d", got)
	}
}

// THE BOUNDARY THAT MATTERS MOST: a platform administrator can see that a tenant
// exists and how big it is, and cannot read anything inside it. Enforced by
// there being no such route, which is what this asserts.
func TestPlatformCannotReachTenantData(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")

	one := operatorCompanyID(t, "one")
	seedPerson(t, one, "person-001", "A Person")

	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)
	publicID := queryString(t, `SELECT public_id FROM companies WHERE id = $1`, one)

	// Every shape somebody would reach for. None of them exist, and 404 from the
	// router is the enforcement: no route, no handler, no query.
	for _, path := range []string{
		"/api/v1/platform/companies/" + publicID + "/people",
		"/api/v1/platform/companies/" + publicID + "/credentials",
		"/api/v1/platform/companies/" + publicID + "/events",
		"/api/v1/platform/companies/" + publicID + "/terminals",
		"/api/v1/platform/companies/" + publicID + "/sites",
	} {
		code, _ := platformCall(t, env.router, http.MethodGet, path, "", token, csrf)
		if code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 -- no platform route may read tenant data", path, code)
		}
	}

	// What it CAN see stops at cardinality.
	code, body := platformCall(t, env.router, http.MethodGet,
		"/api/v1/platform/companies/"+publicID, "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("reading the company = %d (%v)", code, body)
	}
	if body["person_count"] != float64(1) {
		t.Errorf("person_count = %v, want 1", body["person_count"])
	}
	// A count, and nothing that could carry a person.
	for _, forbidden := range []string{"people", "persons", "credentials", "events", "api_key"} {
		if _, present := body[forbidden]; present {
			t.Errorf("the company body carries %q, which is tenant data", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// The first operator
// ---------------------------------------------------------------------------

// ONBOARDING, NOT ACCOUNT ADMINISTRATION. Once a customer has an owner, every
// further account is created by them. A platform identity that could add
// operators to a running tenant at any time would be a standing back door.
func TestPlatformIssuesTheFirstOperatorOnlyOnce(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	// A company with nobody in it.
	_, created := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "Fresh Tenant"}), token, csrf)
	companyID, _ := created["id"].(string)
	if companyID == "" {
		t.Fatalf("no company id in %v", created)
	}

	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/operators",
		jsonBody(t, map[string]string{
			"email":     "owner@fresh.example",
			"full_name": "First Owner",
		}), token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("first operator = %d (%v)", code, body)
	}

	operator, _ := body["operator"].(map[string]any)
	if operator["role"] != models.RoleOwner {
		t.Errorf("role = %v, want OWNER", operator["role"])
	}

	// NO PASSWORD WAS SUPPLIED, so an INVITATION came back and no credential did.
	invitation, _ := body["invitation"].(map[string]any)
	if invitation == nil {
		t.Fatalf("no invitation in %v", body)
	}
	if link, _ := invitation["token"].(string); link == "" {
		t.Error("the invitation carries no token")
	}
	if body["must_change_password"] != true {
		t.Error("the first operator is not flagged must_change_password")
	}

	// A second attempt is refused, and the refusal is the query's own predicate.
	second, _ := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/operators",
		jsonBody(t, map[string]string{"email": "another@fresh.example"}), token, csrf)
	if second != http.StatusConflict {
		t.Errorf("second first-operator = %d, want 409", second)
	}
}

// The vendor must not end up knowing the customer's owner password. An invited
// account cannot be signed in to at all until its link is redeemed.
func TestInvitedFirstOperatorHasNoUsableCredential(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	_, created := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "Invited Tenant"}), token, csrf)
	companyID, _ := created["id"].(string)

	_, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/operators",
		jsonBody(t, map[string]string{"email": "owner@invited.example"}), token, csrf)

	invitation, _ := body["invitation"].(map[string]any)
	link, _ := invitation["token"].(string)
	if link == "" {
		t.Fatalf("no invitation token in %v", body)
	}

	// The token is stored as a hash and nowhere in plaintext, exactly like a site
	// key or a session token.
	if got := queryInt(t,
		`SELECT count(*) FROM user_credential_tokens WHERE token_hash = $1`,
		queryString(t, `SELECT encode(sha256($1::bytea), 'hex')`, link)); got != 1 {
		t.Error("the invitation is not stored as the hash of the plaintext")
	}

	// Nothing anybody could guess signs this account in. The generated password
	// is discarded, so there is no value to try -- which is asserted by the only
	// means available: the plausible ones do not work.
	for _, guess := range []string{"", "password", "owner@invited.example", testPassword} {
		code, _, _ := doAuth(t, env.router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/login",
			body: loginBody("owner@invited.example", guess),
		})
		if code == http.StatusOK {
			t.Fatalf("an invited account signed in with %q", guess)
		}
	}
}

// ---------------------------------------------------------------------------
// Platform session mechanics
// ---------------------------------------------------------------------------

func TestPlatformUnsafeRequestsRequireCSRF(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	// No token at all.
	code, _ := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "No CSRF"}), token, "")
	if code != http.StatusForbidden {
		t.Errorf("create without a CSRF token = %d, want 403", code)
	}

	// Somebody else's token.
	code, _ = platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "Wrong CSRF"}), token, csrf+"x")
	if code != http.StatusForbidden {
		t.Errorf("create with a wrong CSRF token = %d, want 403", code)
	}

	if got := queryInt(t,
		`SELECT count(*) FROM companies WHERE name IN ('No CSRF', 'Wrong CSRF')`); got != 0 {
		t.Errorf("a company was created without CSRF: %d", got)
	}
}

func TestPlatformLogoutRevokesTheSession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	if code, _ := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/logout",
		"", token, csrf); code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", code)
	}

	// Revoked, not merely cleared from the browser: the token itself is dead.
	if code, _ := platformCall(t, env.router, http.MethodGet, "/api/v1/platform/me",
		"", token, ""); code != http.StatusUnauthorized {
		t.Errorf("/me after logout = %d, want 401", code)
	}
}

// A disabled account is refused AFTER the password check, so the response is
// indistinguishable from a wrong password -- the same rule the operator login
// follows, for the same reason.
func TestPlatformLoginRefusalsAreIndistinguishable(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	mustExec(t, `UPDATE platform_admins SET active = FALSE WHERE email = 'platform@example.com'`)

	cases := []struct{ email, password string }{
		{"platform@example.com", testPlatformPassword}, // exists, disabled
		{"platform@example.com", "wrong-password-here"},
		{"nobody@example.com", testPlatformPassword}, // does not exist
	}

	var messages []string
	for _, tc := range cases {
		code, body, _ := doAuth(t, env.router, authCall{
			method: http.MethodPost, path: "/api/v1/platform/login",
			body: loginBody(tc.email, tc.password),
		})
		if code != http.StatusUnauthorized {
			t.Fatalf("login(%s) = %d, want 401", tc.email, code)
		}
		message, _ := body["error"].(string)
		messages = append(messages, message)
	}

	for _, m := range messages[1:] {
		if m != messages[0] {
			t.Errorf("refusals differ: %q vs %q -- that is an enumeration oracle",
				messages[0], m)
		}
	}
}

// The bootstrap's safety rule: only on an installation that has none.
func TestFirstPlatformAdminOnlyOnAnEmptyInstallation(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)

	mustCreatePlatformAdmin(t, "first@example.com")

	_, err := database.CreateFirstPlatformAdmin(models.NewPlatformAdmin{
		Email:    "second@example.com",
		FullName: "Second",
		Password: testPlatformPassword,
	})
	if err == nil {
		t.Fatal("a second platform administrator was created on a non-empty installation")
	}
	if got := queryInt(t, `SELECT count(*) FROM platform_admins WHERE deleted_at IS NULL`); got != 1 {
		t.Errorf("platform administrators = %d, want 1", got)
	}
}

// Platform actions land in the AFFECTED COMPANY's audit trail, with the platform
// named as the actor. A customer asking "who created this company and who was
// given the first account" is asking about their own installation.
func TestPlatformActionsAreAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	mustCreatePlatformAdmin(t, "platform@example.com")
	token, csrf := platformLogin(t, env.router, "platform@example.com", testPlatformPassword)

	_, created := platformCall(t, env.router, http.MethodPost, "/api/v1/platform/companies",
		jsonBody(t, map[string]string{"name": "Audited Tenant"}), token, csrf)
	companyID, _ := created["id"].(string)

	platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/operators",
		jsonBody(t, map[string]string{"email": "owner@audited.example"}), token, csrf)

	for _, action := range []string{"COMPANY_CREATED", "COMPANY_FIRST_OPERATOR_CREATED"} {
		if got := queryInt(t, `
			SELECT count(*) FROM audit_events a
			  JOIN companies c ON c.id = a.company_id
			 WHERE c.public_id = $1 AND a.action = $2
			   AND a.actor_role = 'PLATFORM'
			   AND a.actor_email = 'platform@example.com'
			   AND a.actor_user_id IS NULL`, companyID, action); got != 1 {
			t.Errorf("audit rows for %s = %d, want 1", action, got)
		}
	}
}
