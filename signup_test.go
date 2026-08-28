package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Self-service signup (handlers/signup.go, database/signup.go,
// POST /api/v1/auth/register).
//
// The flow a customer nobody has onboarded goes through: four fields in, a
// company, a site, an OWNER and a live session out. These go through NewRouter,
// so the route table, the rate limiter and the middleware chain are exercised
// exactly as the server runs them.
//
// A LARGE PART OF WHAT IS ASSERTED HERE IS ABSENCE -- that the response carries
// no provisioning key, that the session it hands out cannot reach the platform
// tree or another tenant's data, and that the bootstrap owner who existed before
// any of this still works. An unauthenticated endpoint that creates tenants is
// the sharpest surface in the API, and the interesting failures are all things
// it should not be able to do.

const signupPassword = "harbour-freight-2026"

// signupBody builds a registration payload with sensible defaults, so a test
// naming one field is a test about that field.
func signupBody(overrides map[string]any) string {
	payload := map[string]any{
		"full_name":    "Amaka Obi",
		"company_name": "Harbour Freight Ltd",
		"email":        "amaka@harbourfreight.com",
		"password":     signupPassword,
	}
	for key, value := range overrides {
		if value == nil {
			delete(payload, key)
			continue
		}
		payload[key] = value
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// register posts a signup and returns the status, the decoded body and the
// session cookie it set (empty when it set none).
func register(t *testing.T, router *gin.Engine, body string) (int, map[string]any, string) {
	t.Helper()

	code, decoded, res := doAuth(t, router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/register", body: body,
	})

	name, _ := middleware.SessionCookieConfig()
	var token string
	for _, cookie := range res.Cookies() {
		if cookie.Name == name {
			token = cookie.Value
		}
	}
	return code, decoded, token
}

// mustRegister registers successfully or fails the test.
func mustRegister(t *testing.T, router *gin.Engine, body string) (map[string]any, string) {
	t.Helper()
	code, decoded, token := register(t, router, body)
	if code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (body %v)", code, decoded)
	}
	if token == "" {
		t.Fatal("a successful signup set no session cookie")
	}
	return decoded, token
}

// ---------------------------------------------------------------------------
// The happy path
// ---------------------------------------------------------------------------

func TestSignupCreatesACompanyASiteAndAnOwner(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	body, _ := mustRegister(t, env.router, signupBody(nil))

	// The SAME extensible object login returns, built by the same function --
	// so whatever the console gets here is what /me will give it on the next
	// reload.
	for _, field := range []string{"operator", "company", "role", "sites", "all_sites",
		"applications", "csrf_token", "session_expires_at", "session_expires_in_seconds"} {
		if _, present := body[field]; !present {
			t.Errorf("signup response is missing %q", field)
		}
	}

	operator := body["operator"].(map[string]any)
	if operator["email"] != "amaka@harbourfreight.com" {
		t.Errorf("operator email = %v", operator["email"])
	}
	if operator["full_name"] != "Amaka Obi" {
		t.Errorf("operator full_name = %v", operator["full_name"])
	}
	// FORCED, not taken from the request. The first account in a company has to
	// be able to create the others and there is nobody to grant it anything.
	if operator["role"] != models.RoleOwner || body["role"] != models.RoleOwner {
		t.Errorf("registering user is not an OWNER: %v", body)
	}

	company := body["company"].(map[string]any)
	if company["name"] != "Harbour Freight Ltd" {
		t.Errorf("company name = %v", company["name"])
	}
	// Derived, never asked for.
	if company["slug"] != "harbour-freight-ltd" {
		t.Errorf("slug = %v, want it derived from the company name", company["slug"])
	}

	// An OWNER is never site-scoped, so an empty grant list means every site.
	if body["all_sites"] != true {
		t.Error("the new owner is not unscoped")
	}

	companyID := signupCompanyID(t, "harbour-freight-ltd")

	// The site the customer never had to ask for. Without it the tenant has
	// nowhere to put a terminal and no settings row for one to converge against.
	if got := queryString(t,
		`SELECT site_name FROM sites WHERE company_id = $1 AND deleted_at IS NULL`,
		companyID); got != "Main Site" {
		t.Errorf("automatic site = %q, want %q", got, "Main Site")
	}
	if count := queryInt(t,
		`SELECT count(*) FROM sites WHERE company_id = $1`, companyID); count != 1 {
		t.Errorf("new company has %d sites, want exactly 1", count)
	}

	// The site is USABLE: it has a stored credential, so the owner can rotate a
	// fresh one and provision hardware without anybody touching the database.
	if !queryBool(t, `SELECT api_key_hash IS NOT NULL FROM sites WHERE company_id = $1`,
		companyID) {
		t.Error("the automatic site has no provisioning credential at all")
	}

	// Deny-by-default, matching every other company created after the
	// authorization engine existed. A permissive default is a legacy carry-over
	// and a new tenant has no behaviour to preserve.
	if got := queryString(t,
		`SELECT default_person_access FROM companies WHERE id = $1`, companyID); got != "NONE" {
		t.Errorf("default_person_access = %q, want NONE", got)
	}

	// The address they signed up with is the tenant's contact. A company row
	// with no way to reach anybody makes a support ticket unanswerable.
	if got := queryString(t,
		`SELECT COALESCE(contact_email, '') FROM companies WHERE id = $1`,
		companyID); got != "amaka@harbourfreight.com" {
		t.Errorf("contact_email = %q", got)
	}

	// NOT flagged must_change_password, unlike every other way an account is
	// created. The flag means somebody else chose the credential; here the
	// person who chose it is the person typing it.
	if queryBool(t, `SELECT must_change_password FROM users WHERE company_id = $1`, companyID) {
		t.Error("a self-registered owner was told to change the password they just chose")
	}

	// The trail says the tenant created ITSELF, which is a different fact from
	// a vendor having created it.
	if count := queryInt(t,
		`SELECT count(*) FROM audit_events
		  WHERE company_id = $1 AND action = 'COMPANY_REGISTERED'
		    AND actor_role = 'OWNER'`, companyID); count != 1 {
		t.Errorf("audit rows for COMPANY_REGISTERED = %d, want 1", count)
	}
}

func TestSignupSignsTheNewOwnerStraightIn(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	body, token := mustRegister(t, env.router, signupBody(nil))

	// The cookie signup set authenticates the ordinary session routes. Nothing
	// about this session is special -- that is the whole point of not inventing
	// a second authentication path for it.
	code, me, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("/auth/me with the signup cookie = %d (%v)", code, me)
	}
	if me["company"].(map[string]any)["id"] != body["company"].(map[string]any)["id"] {
		t.Error("/auth/me reports a different company from the one signup returned")
	}

	// And the console it redirects to actually answers.
	code, sites, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/console/sites", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("console sites with the signup cookie = %d (%v)", code, sites)
	}
	if count, _ := sites["count"].(float64); count != 1 {
		t.Errorf("the new console shows %v sites, want 1 (its own Main Site)", sites["count"])
	}
}

func TestSignupLetsTheNewOwnerLogInAfterwards(t *testing.T) {
	// The password they chose is the password that works. A signup that stored
	// something else -- or flagged the account -- would only fail on the second
	// visit, which is the worst time to find out.
	cheapBcrypt(t)
	env := newTestEnv(t)

	mustRegister(t, env.router, signupBody(nil))

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("amaka@harbourfreight.com", signupPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("logging in as the new owner = %d (%v)", code, body)
	}
	if body["must_change_password"] == true {
		t.Error("the new owner is being forced to change the password they chose")
	}
}

// ---------------------------------------------------------------------------
// What the response and the session must not carry or reach
// ---------------------------------------------------------------------------

func TestSignupNeverDisclosesASiteProvisioningKey(t *testing.T) {
	// The sharpest requirement on this route. A site key registers terminals and
	// rotates their device credentials; an anonymous caller must not be handed
	// one, and the site is created with a credential regardless so that the
	// owner can rotate a fresh one from the console.
	cheapBcrypt(t)
	env := newTestEnv(t)

	raw := signupRawBody(t, env.router, signupBody(nil))
	for _, forbidden := range []string{"ats_", "api_key", "apiKey", "claim_code", "atd_"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the signup response contains %q: %s", forbidden, raw)
		}
	}

	// The site still HAS a credential -- it has to, or no terminal could ever be
	// registered at it. What the endpoint does not do is hand the plaintext to an
	// anonymous caller.
	companyID := signupCompanyID(t, "harbour-freight-ltd")
	if !queryBool(t, `SELECT api_key_hash IS NOT NULL AND length(api_key_hash) = 64
	                    FROM sites WHERE company_id = $1`, companyID) {
		t.Error("the automatic site's credential is not a stored SHA-256 hash")
	}
}

func TestSignupSessionCannotReachPlatformAdministration(t *testing.T) {
	// A THIRD CREDENTIAL CLASS, a different table and a different cookie. Both
	// cookies are Path=/, so a browser genuinely offers each to the other's
	// routes -- only the middleware names keep them apart, and a signup that
	// quietly created a platform identity would dissolve that.
	cheapBcrypt(t)
	env := newTestEnv(t)

	_, token := mustRegister(t, env.router, signupBody(nil))

	for _, path := range []string{
		"/api/v1/platform/me",
		"/api/v1/platform/companies",
	} {
		code, body, _ := doAuth(t, env.router, authCall{
			method: http.MethodGet, path: path, token: token,
		})
		if code != http.StatusUnauthorized {
			t.Errorf("%s with a signup session = %d, want 401 (%v)", path, code, body)
		}
	}

	// And nothing created one behind the scenes.
	if count := queryInt(t, `SELECT count(*) FROM platform_admins`); count != 0 {
		t.Errorf("signup created %d platform administrators, want 0", count)
	}
}

func TestSignupSessionSeesOnlyItsOwnTenant(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	_, token := mustRegister(t, env.router, signupBody(nil))

	// Site A belongs to Company One, which this owner has never heard of. A site
	// in another tenant is NOT FOUND rather than forbidden, so the API does not
	// confirm that the id exists elsewhere.
	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet,
		path:   "/api/v1/console/sites/" + operatorSitePublicID(t, "Site A"),
		token:  token,
	})
	if code != http.StatusNotFound {
		t.Errorf("reading another tenant's site = %d, want 404 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestSignupValidatesItsFields(t *testing.T) {
	cheapBcrypt(t)
	// More cases than the default allowance, and the limiter is not what this
	// test is about -- TestSignupSharesTheCredentialRateLimiter covers that.
	t.Setenv("LOGIN_RATE_LIMIT_PER_MINUTE", "100")
	env := newTestEnv(t)

	cases := []struct {
		name      string
		overrides map[string]any
		want      int
	}{
		{"no name", map[string]any{"full_name": nil}, http.StatusBadRequest},
		{"blank name", map[string]any{"full_name": "   "}, http.StatusBadRequest},
		{"no company", map[string]any{"company_name": nil}, http.StatusBadRequest},
		{"blank company", map[string]any{"company_name": "  "}, http.StatusBadRequest},
		{"no email", map[string]any{"email": nil}, http.StatusBadRequest},
		{"malformed email", map[string]any{"email": "not-an-address"}, http.StatusBadRequest},
		{"display-name email", map[string]any{"email": "Amaka <a@b.com>"}, http.StatusBadRequest},
		{"no password", map[string]any{"password": nil}, http.StatusBadRequest},
		{"short password", map[string]any{"password": "short"}, http.StatusBadRequest},
		{"password past bcrypt's limit",
			map[string]any{"password": strings.Repeat("x", 73)}, http.StatusBadRequest},
		{"company name past the column",
			map[string]any{"company_name": strings.Repeat("a", 151)}, http.StatusBadRequest},
		{"full name past the column",
			map[string]any{"full_name": strings.Repeat("a", 151)}, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, token := register(t, env.router, signupBody(tc.overrides))
			if code != tc.want {
				t.Errorf("register = %d, want %d (%v)", code, tc.want, body)
			}
			if token != "" {
				t.Error("a rejected signup set a session cookie")
			}
			if message, _ := body["error"].(string); message == "" {
				t.Error("a rejected signup said nothing about what was wrong")
			}
		})
	}

	// Nothing was created by any of them.
	if count := queryInt(t, `SELECT count(*) FROM companies`); count != 2 {
		t.Errorf("companies = %d, want the 2 the fixture created", count)
	}
	if count := queryInt(t, `SELECT count(*) FROM users`); count != 0 {
		t.Errorf("a rejected signup created %d users", count)
	}
}

func TestSignupRequiresJSON(t *testing.T) {
	// The same CSRF defence login has: an HTML form can be submitted cross-site
	// without script, but it cannot send application/json.
	cheapBcrypt(t)
	env := newTestEnv(t)

	code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/register",
		body:        signupBody(nil),
		contentType: "application/x-www-form-urlencoded",
	})
	if code != http.StatusUnsupportedMediaType {
		t.Errorf("a non-JSON signup = %d, want 415", code)
	}
}

func TestSignupRefusesAnAddressThatAlreadyHasAnAccount(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	mustRegister(t, env.router, signupBody(nil))

	// A DIFFERENT company, the same person's address. Email is unique globally
	// because the login form has no tenant selector, so this cannot be allowed.
	code, body, token := register(t, env.router, signupBody(map[string]any{
		"company_name": "Another Company Entirely",
	}))
	if code != http.StatusConflict {
		t.Fatalf("re-registering an address = %d, want 409 (%v)", code, body)
	}
	if token != "" {
		t.Error("a refused signup set a session cookie")
	}

	// AND NOTHING WAS LEFT BEHIND. A company created before the duplicate
	// address was detected would be a tenant nobody can ever sign into.
	if count := queryInt(t,
		`SELECT count(*) FROM companies WHERE name = 'Another Company Entirely'`); count != 0 {
		t.Error("the refused signup left an orphan company behind")
	}
}

func TestSignupTreatsAddressesCaseInsensitively(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	mustRegister(t, env.router, signupBody(map[string]any{"email": "Amaka@Harbour.COM"}))

	// Stored lowercased, as users_email_check requires -- otherwise the partial
	// unique index is not genuinely case-insensitive and two accounts a login
	// form cannot tell apart both look unique to it.
	if got := queryString(t, `SELECT email FROM users`); got != "amaka@harbour.com" {
		t.Errorf("stored email = %q, want it normalised", got)
	}

	code, _, _ := register(t, env.router, signupBody(map[string]any{
		"email": "AMAKA@harbour.com", "company_name": "Other Ltd",
	}))
	if code != http.StatusConflict {
		t.Errorf("the same address in another case = %d, want 409", code)
	}
}

// ---------------------------------------------------------------------------
// Slugs: derived, never asked for
// ---------------------------------------------------------------------------

func TestSignupDerivesAFreeSlugWhenTheNameIsTaken(t *testing.T) {
	// Two customers may genuinely be called the same thing, and neither should
	// be refused because of an identifier they never saw and cannot influence.
	cheapBcrypt(t)
	env := newTestEnv(t)

	first, _ := mustRegister(t, env.router, signupBody(nil))
	second, _ := mustRegister(t, env.router, signupBody(map[string]any{
		"email": "rival@harbourfreight.example",
	}))

	firstSlug := first["company"].(map[string]any)["slug"].(string)
	secondSlug := second["company"].(map[string]any)["slug"].(string)

	if firstSlug != "harbour-freight-ltd" {
		t.Errorf("first slug = %q", firstSlug)
	}
	if secondSlug == firstSlug {
		t.Fatalf("both companies got the slug %q", secondSlug)
	}
	if !strings.HasPrefix(secondSlug, "harbour-freight-ltd") {
		t.Errorf("second slug %q is not recognisably derived from the name", secondSlug)
	}

	// The NAME is untouched. Only the identifier had to move.
	if second["company"].(map[string]any)["name"] != "Harbour Freight Ltd" {
		t.Error("the second company's name was altered to make its slug unique")
	}
}

func TestSignupAcceptsACompanyNameThatNormalisesToNothing(t *testing.T) {
	// A company written entirely in a non-Latin script normalises to an empty
	// slug. Refusing that signup would be refusing a customer over a naming rule
	// they never saw, so the address is used instead.
	cheapBcrypt(t)
	env := newTestEnv(t)

	body, _ := mustRegister(t, env.router, signupBody(map[string]any{
		"company_name": "海運株式会社",
		"email":        "kenji@kaiun.example",
	}))

	company := body["company"].(map[string]any)
	if company["name"] != "海運株式会社" {
		t.Errorf("company name = %v, want it stored as typed", company["name"])
	}
	slug, _ := company["slug"].(string)
	if err := models.ValidateSlug(slug); err != nil {
		t.Errorf("derived slug %q is not storable: %v", slug, err)
	}
	if !strings.HasPrefix(slug, "kenji") {
		t.Errorf("slug = %q, want it derived from the address", slug)
	}
}

// ---------------------------------------------------------------------------
// The switch, the limiter, and the accounts that came before
// ---------------------------------------------------------------------------

func TestSignupCanBeSwitchedOff(t *testing.T) {
	// For an installation run for one organisation, where an anonymous endpoint
	// that creates tenants is a surface with no customer behind it. 403 rather
	// than 404: a route that pretends not to exist sends whoever hit it looking
	// for a typo rather than for the switch.
	cheapBcrypt(t)
	t.Setenv("PUBLIC_SIGNUP_ENABLED", "false")
	env := newTestEnv(t)

	code, body, token := register(t, env.router, signupBody(nil))
	if code != http.StatusForbidden {
		t.Fatalf("register with signup disabled = %d, want 403 (%v)", code, body)
	}
	if token != "" {
		t.Error("a disabled signup set a session cookie")
	}
	if count := queryInt(t, `SELECT count(*) FROM users`); count != 0 {
		t.Error("a disabled signup created an account anyway")
	}
}

func TestSignupSharesTheCredentialRateLimiter(t *testing.T) {
	// One allowance across login, signup and the handover routes, so an attacker
	// cannot get a second budget by alternating between them -- and mass tenant
	// creation from one address is bounded by the same bucket.
	//
	// Set before the router is built: the limiter reads its allowance once, when
	// the route table is constructed.
	cheapBcrypt(t)
	t.Setenv("LOGIN_RATE_LIMIT_PER_MINUTE", "1")
	env := newTestEnv(t)

	// Spend the allowance on a login attempt.
	doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("nobody@example.com", "wrong-password-entirely"),
	})

	code, body, _ := register(t, env.router, signupBody(nil))
	if code != http.StatusTooManyRequests {
		t.Fatalf("signup after the login allowance was spent = %d, want 429 (%v)",
			code, body)
	}
}

func TestSignupDoesNotDisturbTheBootstrapOwner(t *testing.T) {
	// The first OWNER on this installation was created from
	// BOOTSTRAP_OPERATOR_*, before any of this existed. Signing up a new
	// customer must leave that account signing in, in its own company, seeing
	// its own sites.
	cheapBcrypt(t)
	env := newTestEnv(t)

	bootstrapped, err := database.CreateFirstOperator("one", models.NewUser{
		Email:    "owner@companyone.example",
		FullName: "Company One Owner",
		Password: testPassword,
		Role:     models.RoleOwner,
	})
	if err != nil {
		t.Fatalf("creating the bootstrap owner: %v", err)
	}

	mustRegister(t, env.router, signupBody(nil))

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("owner@companyone.example", testPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("the bootstrap owner can no longer sign in: %d (%v)", code, body)
	}
	if body["company"].(map[string]any)["slug"] != "one" {
		t.Errorf("the bootstrap owner is now in %v", body["company"])
	}
	if body["operator"].(map[string]any)["id"] != bootstrapped.PublicID {
		t.Error("the bootstrap owner resolved to a different account")
	}

	// The bootstrap remains what it always was: only ever able to act on an
	// EMPTY system. A signed-up tenant is not an opening to re-run it.
	if _, err := database.CreateFirstOperator("one", models.NewUser{
		Email: "second@companyone.example", FullName: "Second", Password: testPassword,
	}); err == nil {
		t.Error("the bootstrap ran again on a system that already has operators")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// signupCompanyID resolves a company the signup flow created.
func signupCompanyID(t *testing.T, slug string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM companies WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("company %q: %v", slug, err)
	}
	return id
}

// signupRawBody registers and returns the response body verbatim, for
// assertions about what a string must NOT contain.
func signupRawBody(t *testing.T, router *gin.Engine, body string) string {
	t.Helper()
	env := &testEnv{t: t, router: router}
	rec := env.raw(http.MethodPost, "/api/v1/auth/register", json.RawMessage(body),
		map[string]string{"Content-Type": "application/json"})
	return rec.Body.String()
}
