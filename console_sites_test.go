package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Site management through the console, and the provisioning credential it mints.
//
// The credential is what makes this surface different from every other console
// resource. A site API key registers terminals and rotates their device keys, so
// most of what follows is negative: who cannot create a site, which responses
// must never contain a key, and what stops working when one is rotated or a site
// is retired.

// siteKeyPattern is the format the server issues: "ats_" and 64 hex characters,
// i.e. 256 bits from crypto/rand.
var siteKeyPattern = regexp.MustCompile(`^ats_[0-9a-f]{64}$`)

func createSite(t *testing.T, env *testEnv, token, csrf, name string) (int, map[string]any) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"address":"1 Test Road","timezone":"Africa/Lagos"}`, name)
	return consoleCall(t, env.router, "POST", "/api/v1/console/sites", body, token, csrf)
}

// credentialOf pulls the one-time key out of a create or rotate response.
func credentialOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	credential, ok := body["credential"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no credential object: %v", body)
	}
	return credential
}

// ---------------------------------------------------------------------------
// Creation
// ---------------------------------------------------------------------------

func TestConsoleSiteCreationIssuesAUsableKeyOnce(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "site-admin@example.com", models.RoleAdmin)

	code, body := createSite(t, env, token, csrf, "New Depot")
	if code != http.StatusCreated {
		t.Fatalf("creating a site = %d (%v)", code, body)
	}

	site, ok := body["site"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no site: %v", body)
	}
	if site["name"] != "New Depot" || site["timezone"] != "Africa/Lagos" {
		t.Errorf("site = %v", site)
	}
	if site["terminal_count"].(float64) != 0 {
		t.Errorf("a new site reports %v terminals", site["terminal_count"])
	}
	if _, present := site["api_key"]; present {
		t.Error("the site projection carried an api_key field")
	}

	credential := credentialOf(t, body)
	key, _ := credential["api_key"].(string)

	// ENTROPY AND FORMAT. 256 bits of crypto/rand, hex encoded, behind a prefix
	// that distinguishes a site key from a device key at a glance.
	if !siteKeyPattern.MatchString(key) {
		t.Fatalf("issued key %q does not match ats_<64 hex>", key)
	}
	if credential["shown_once"] != true {
		t.Error("the credential is not flagged as shown once")
	}
	if prefix, _ := credential["api_key_prefix"].(string); prefix != key[:12] {
		t.Errorf("prefix %q does not match the key it identifies", prefix)
	}

	// THE KEY ACTUALLY WORKS. A credential that is issued but does not
	// authenticate is worse than none, because the failure surfaces at a door.
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", key)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Errorf("the issued key authenticated with %d, want 200", status)
	}

	// AND IT IS STORED HASHED, not in plaintext.
	var stored int
	if err := database.DB.QueryRow(
		`SELECT count(*) FROM sites WHERE api_key_hash = $1`,
		database.HashSiteKey(key)).Scan(&stored); err != nil {
		t.Fatalf("checking stored hash: %v", err)
	}
	if stored != 1 {
		t.Errorf("found %d sites stored under the key's hash, want 1", stored)
	}
}

func TestConsoleSiteKeysAreUniquePerCreation(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "keys@example.com", models.RoleAdmin)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		_, body := createSite(t, env, token, csrf, fmt.Sprintf("Depot %d", i))
		key, _ := credentialOf(t, body)["api_key"].(string)
		if key == "" {
			t.Fatalf("creation %d issued no key", i)
		}
		if seen[key] {
			t.Fatalf("creation %d reissued a key already seen -- the generator is not random", i)
		}
		seen[key] = true
	}
}

func TestConsoleSiteNamesAreUniquePerCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "dup@example.com", models.RoleAdmin)

	if code, _ := createSite(t, env, token, csrf, "Shared Name"); code != http.StatusCreated {
		t.Fatal("the first site was refused")
	}
	if code, body := createSite(t, env, token, csrf, "Shared Name"); code != http.StatusConflict {
		t.Errorf("a duplicate name in the same company = %d, want 409 (%v)", code, body)
	}

	// The same name in ANOTHER company is fine -- tenants do not share a
	// namespace, and two customers both calling a site "Head Office" is normal.
	_, otherToken, otherCSRF := consoleOperatorSession(t, env.router, two, "other@example.com", models.RoleAdmin)
	if code, body := createSite(t, env, otherToken, otherCSRF, "Shared Name"); code != http.StatusCreated {
		t.Errorf("the same name in another company = %d, want 201 (%v)", code, body)
	}
}

func TestConsoleSiteCreationValidatesItsInput(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "validate@example.com", models.RoleAdmin)

	for _, body := range []string{`{}`, `{"name":""}`, `{"name":"   "}`} {
		if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/sites",
			body, token, csrf); code != http.StatusBadRequest {
			t.Errorf("creating with %s = %d, want 400", body, code)
		}
	}

	long := fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", 101))
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/sites",
		long, token, csrf); code != http.StatusBadRequest {
		t.Error("a 101-character site name was accepted")
	}

	// An omitted timezone defaults to UTC rather than to the API server's
	// incidental location.
	_, created := consoleCall(t, env.router, "POST", "/api/v1/console/sites",
		`{"name":"No Zone"}`, token, csrf)
	if site, ok := created["site"].(map[string]any); !ok || site["timezone"] != "UTC" {
		t.Errorf("a site created without a timezone = %v, want UTC", created["site"])
	}
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

func TestConsoleSiteManagementRequiresAdmin(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	// A MANAGER runs a site day to day and may write its settings. Creating one
	// mints a provisioning credential and retiring one stops doors opening;
	// neither is day-to-day work.
	for _, role := range []string{models.RoleViewer, models.RoleManager} {
		_, token, csrf := consoleOperatorSession(t, env.router, one,
			strings.ToLower(role)+"-site@example.com", role)

		checks := []struct{ method, path, body string }{
			{"POST", "/api/v1/console/sites", `{"name":"Sneaky Depot"}`},
			{"PUT", "/api/v1/console/sites/" + siteA, `{"name":"Renamed"}`},
			{"DELETE", "/api/v1/console/sites/" + siteA, ""},
			{"POST", "/api/v1/console/sites/" + siteA + "/api-key", ""},
		}
		for _, check := range checks {
			code, body := consoleCall(t, env.router, check.method, check.path, check.body, token, csrf)
			if code != http.StatusForbidden {
				t.Errorf("%s %s as %s = %d, want 403 (%v)", check.method, check.path, role, code, body)
			}
		}

		// And a MANAGER keeps what they are supposed to have.
		if role == models.RoleManager {
			if code, _ := consoleCall(t, env.router, "PUT",
				"/api/v1/console/sites/"+siteA+"/settings",
				`{"unlock_duration_seconds":6}`, token, csrf); code != http.StatusOK {
				t.Error("a MANAGER was refused a site settings write")
			}
		}
	}

	// Nothing was created by any of the refused attempts.
	if count := queryInt(t, `SELECT count(*) FROM sites WHERE site_name = 'Sneaky Depot'`); count != 0 {
		t.Errorf("a refused creation still made %d site(s)", count)
	}
}

func TestConsoleSiteManagementRefusesAnotherCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteC := operatorSitePublicID(t, "Site C") // company two

	_, token, csrf := consoleOperatorSession(t, env.router, one, "cross@example.com", models.RoleAdmin)

	// 404 in every case, never 403: answering "forbidden" would confirm that
	// the id exists in somebody else's account.
	for _, check := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/console/sites/" + siteC, `{"name":"Hijacked"}`},
		{"DELETE", "/api/v1/console/sites/" + siteC, ""},
		{"POST", "/api/v1/console/sites/" + siteC + "/api-key", ""},
	} {
		if code, body := consoleCall(t, env.router, check.method, check.path,
			check.body, token, csrf); code != http.StatusNotFound {
			t.Errorf("%s on another company's site = %d, want 404 (%v)", check.method, code, body)
		}
	}

	// The other company's site is untouched, and its key still works.
	if name := queryString(t, `SELECT site_name FROM sites WHERE public_id = $1`, siteC); name != "Site C" {
		t.Errorf("Site C was renamed to %q across a tenant boundary", name)
	}
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteCKey)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Errorf("Site C's key stopped working after a cross-tenant attempt: %d", status)
	}
}

func TestConsoleSiteWritesRequireCSRF(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	_, token, _ := consoleOperatorSession(t, env.router, one, "csrf-site@example.com", models.RoleAdmin)

	for _, check := range []struct{ method, path, body string }{
		{"POST", "/api/v1/console/sites", `{"name":"No Token"}`},
		{"PUT", "/api/v1/console/sites/" + siteA, `{"name":"No Token"}`},
		{"DELETE", "/api/v1/console/sites/" + siteA, ""},
		{"POST", "/api/v1/console/sites/" + siteA + "/api-key", ""},
	} {
		if code, _ := consoleCall(t, env.router, check.method, check.path,
			check.body, token, ""); code != http.StatusForbidden {
			t.Errorf("%s %s without a CSRF token = %d, want 403", check.method, check.path, code)
		}
	}
}

// ---------------------------------------------------------------------------
// The key never comes back
// ---------------------------------------------------------------------------

func TestConsoleNeverReturnsASiteKeyAfterCreation(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "once@example.com", models.RoleAdmin)

	_, created := createSite(t, env, token, csrf, "Secret Depot")
	key, _ := credentialOf(t, created)["api_key"].(string)
	if key == "" {
		t.Fatal("creation issued no key")
	}
	site := created["site"].(map[string]any)
	siteID := site["id"].(string)

	// Every read an operator can perform against this site.
	for _, path := range []string{
		"/api/v1/console/sites",
		"/api/v1/console/sites/" + siteID,
		"/api/v1/console/sites/" + siteID + "/settings",
	} {
		_, _, res := doAuth(t, env.router, authCall{method: "GET", path: path, token: token, csrf: csrf})
		raw := readBody(t, res)
		if len(raw) < 3 {
			t.Fatalf("read %q as the body of %s; the leak check would be vacuous", raw, path)
		}
		if strings.Contains(raw, key) {
			t.Errorf("%s returned the site key after creation", path)
		}
		for _, field := range []string{`"api_key"`, "api_key_hash", "ats_"} {
			if strings.Contains(raw, field) {
				t.Errorf("%s contains %q: %s", path, field, raw)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------

func TestConsoleSiteKeyRotationReplacesTheOldKeyImmediately(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "rotate@example.com", models.RoleAdmin)

	// The original key works.
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteAKey)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Fatalf("the original key did not authenticate: %d", status)
	}

	code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/sites/"+siteA+"/api-key", "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("rotating = %d (%v)", code, body)
	}

	newKey, _ := credentialOf(t, body)["api_key"].(string)
	if !siteKeyPattern.MatchString(newKey) {
		t.Fatalf("rotation issued %q, which is not a well-formed key", newKey)
	}
	if newKey == env.siteAKey {
		t.Fatal("rotation reissued the same key")
	}

	// NO OVERLAP WINDOW. The old key stops working the instant this commits.
	req = newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteAKey)
	if status := serve(env.router, req); status != http.StatusUnauthorized {
		t.Errorf("the old key still authenticated after rotation: %d", status)
	}

	// And the new one does.
	req = newRequestWithSiteKey(t, "GET", "/api/v1/members", "", newKey)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Errorf("the rotated key did not authenticate: %d", status)
	}

	// Another site's key is untouched -- rotation is scoped to one site.
	req = newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteBKey)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Errorf("rotating Site A broke Site B: %d", status)
	}
}

func TestConsoleSiteKeyRotationReportsTerminalsItLocksOut(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	// A terminal that has never been issued a device credential still depends
	// on the SITE key, so rotation locks it out. One that has its own key does
	// not, because that is a different secret.
	mustCreateDevice(t, "Site A", "LEGACY-1")
	registered := env.registerDevice(env.siteAKey, "MODERN-1")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "rotate2@example.com", models.RoleAdmin)
	_, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/sites/"+siteA+"/api-key", "", token, csrf)

	if legacy := body["legacy_terminals"].(float64); legacy != 1 {
		t.Errorf("legacy_terminals = %v, want 1 (only the unregistered terminal)", legacy)
	}

	// THE DEVICE KEY IS UNAFFECTED. Rotating the site's provisioning secret
	// must not brick terminals that already hold their own credential -- that is
	// a different secret, and bricking a fleet to re-key a provisioning
	// credential would make rotation something nobody dares do.
	if res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		deviceAuth(registered)); res.Code != http.StatusOK {
		t.Errorf("a registered terminal's own key stopped working after site rotation: %d (%s)",
			res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Deactivation and retirement
// ---------------------------------------------------------------------------

func TestConsoleSiteDeactivationIsReversible(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "deact@example.com", models.RoleAdmin)

	if code, body := consoleCall(t, env.router, "PUT", "/api/v1/console/sites/"+siteA,
		`{"active":false}`, token, csrf); code != http.StatusOK {
		t.Fatalf("deactivating = %d (%v)", code, body)
	}

	// The site key stops authenticating while inactive.
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteAKey)
	if status := serve(env.router, req); status != http.StatusUnauthorized {
		t.Errorf("an inactive site still authenticated: %d", status)
	}

	// ...and nothing was destroyed: reactivating restores it.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/sites/"+siteA,
		`{"active":true}`, token, csrf); code != http.StatusOK {
		t.Fatal("reactivating was refused")
	}
	req = newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteAKey)
	if status := serve(env.router, req); status != http.StatusOK {
		t.Errorf("a reactivated site did not authenticate: %d", status)
	}
}

func TestConsoleSiteRetirementCascadesToTerminals(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	mustCreateDevice(t, "Site A", "RETIRE-1")
	mustCreateDevice(t, "Site A", "RETIRE-2")
	mustCreateDevice(t, "Site B", "KEEP-1")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "retire@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "DELETE", "/api/v1/console/sites/"+siteA, "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("retiring = %d (%v)", code, body)
	}

	// THE COUNT IS THE POINT. An operator must be told how much hardware just
	// stopped working.
	if retired := body["terminals_retired"].(float64); retired != 2 {
		t.Errorf("terminals_retired = %v, want 2", retired)
	}

	// The site is gone from every console read, and its key is dead.
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteA, "", token, ""); code != http.StatusNotFound {
		t.Errorf("a retired site is still readable: %d", code)
	}
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", env.siteAKey)
	if status := serve(env.router, req); status != http.StatusUnauthorized {
		t.Errorf("a retired site's key still authenticated: %d", status)
	}

	// Its terminals went with it, and only its own.
	_, terminals := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	serials := map[string]bool{}
	for _, entry := range listOf(t, terminals, "terminals") {
		serials[entry.(map[string]any)["serial_number"].(string)] = true
	}
	if serials["RETIRE-1"] || serials["RETIRE-2"] {
		t.Error("a retired site's terminals are still listed")
	}
	if !serials["KEEP-1"] {
		t.Error("retiring Site A removed Site B's terminal")
	}

	// SOFT delete, not a purge: the rows are retained for audit.
	if count := queryInt(t,
		`SELECT count(*) FROM devices WHERE serial_number IN ('RETIRE-1','RETIRE-2')
		   AND deleted_at IS NOT NULL`); count != 2 {
		t.Error("retirement hard-deleted the terminal rows instead of soft-deleting them")
	}

	// The name is free for reuse, because the unique index ignores retired rows.
	if code, _ := createSite(t, env, token, csrf, "Site A"); code != http.StatusCreated {
		t.Errorf("a retired site's name could not be reused: %d", code)
	}
}

// ---------------------------------------------------------------------------
// Existing authentication is unchanged by the storage change
// ---------------------------------------------------------------------------

func TestSiteKeyAuthenticationSurvivesHashedStorage(t *testing.T) {
	env := newTestEnv(t)

	// The whole site-key surface, with the same header a terminal has always
	// sent. Migration 011 changed where the secret is compared, not what is
	// presented, and this is the assertion that says so.
	for _, path := range []string{
		"/api/v1/members",
		"/api/v1/devices",
		"/api/v1/devices/summary",
		"/api/v1/sites/settings",
		"/api/v1/access/logs",
	} {
		req := newRequestWithSiteKey(t, "GET", path, "", env.siteAKey)
		if status := serve(env.router, req); status != http.StatusOK {
			t.Errorf("GET %s with a valid site key = %d, want 200", path, status)
		}
	}

	// A wrong key is still refused, and so is no key.
	req := newRequestWithSiteKey(t, "GET", "/api/v1/members", "", "ats_"+strings.Repeat("0", 64))
	if status := serve(env.router, req); status != http.StatusUnauthorized {
		t.Errorf("an unknown key = %d, want 401", status)
	}
	req = newRequestWithSiteKey(t, "GET", "/api/v1/members", "", "")
	if status := serve(env.router, req); status != http.StatusUnauthorized {
		t.Errorf("an empty key = %d, want 401", status)
	}

	// Tenancy still holds: Site C's key reaches company two and nothing else.
	if _, err := database.GetSiteByAPIKey(env.siteCKey); err != nil {
		t.Fatalf("Site C's key did not resolve: %v", err)
	}
}

func TestNoPlaintextSiteKeyRemainsInTheDatabase(t *testing.T) {
	newTestEnv(t)

	// The column is gone, which is what makes the guarantee structural rather
	// than a promise about handler code.
	var exists bool
	if err := database.DB.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'sites'
			   AND column_name = 'api_key')`).Scan(&exists); err != nil {
		t.Fatalf("inspecting the schema: %v", err)
	}
	if exists {
		t.Error("sites.api_key still exists; the provisioning secret is recoverable from the database")
	}

	// And the hash is stored in the shape the constraint requires.
	hash := queryString(t, `SELECT api_key_hash FROM sites WHERE site_name = 'Site A'`)
	if len(hash) != 64 {
		t.Errorf("api_key_hash is %d characters, want 64", len(hash))
	}
	if hash == "" || strings.Contains(hash, "test-site-a-key") {
		t.Error("the stored value is not a hash of the key")
	}
}

// ---------------------------------------------------------------------------
// The whole site surface, swept for credential material
// ---------------------------------------------------------------------------

func TestConsoleSiteResponsesCarryNoCredentialMaterial(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "SWEEP-1")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "sweep@example.com", models.RoleAdmin)
	siteA := operatorSitePublicID(t, "Site A")

	// Create and rotate first, so the keys those produced are in play and can
	// be searched for in every subsequent read.
	_, created := createSite(t, env, token, csrf, "Sweep Depot")
	createdKey, _ := credentialOf(t, created)["api_key"].(string)

	_, rotated := consoleCall(t, env.router, "POST",
		"/api/v1/console/sites/"+siteA+"/api-key", "", token, csrf)
	rotatedKey, _ := credentialOf(t, rotated)["api_key"].(string)

	secrets := []string{createdKey, rotatedKey, env.siteBKey, env.siteCKey}

	for _, path := range []string{
		"/api/v1/console/sites",
		"/api/v1/console/sites/" + siteA,
		"/api/v1/console/sites/" + siteA + "/settings",
		"/api/v1/console/terminals",
		"/api/v1/console/terminals/SWEEP-1",
		"/api/v1/console/company",
		"/api/v1/console/operators",
	} {
		_, _, res := doAuth(t, env.router, authCall{method: "GET", path: path, token: token, csrf: csrf})
		raw := readBody(t, res)
		if len(raw) < 3 {
			t.Fatalf("read %q as the body of %s; the leak check would be vacuous", raw, path)
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(raw, secret) {
				t.Errorf("%s leaked a site provisioning key", path)
			}
		}
		for _, field := range []string{"api_key_hash", "password_hash", "fingerprint_template"} {
			if strings.Contains(raw, field) {
				t.Errorf("%s contains %q", path, field)
			}
		}
	}
}

// TestSiteKeyMigrationPreservesExistingCredentials is the one test that does not
// run against the suite's database.
//
// Every other test here proves the END STATE works on a database built from all
// migrations. That says nothing about the case 011 exists for: an installation
// whose sites already hold plaintext keys, whose terminals are in the field, and
// which must keep authenticating with the key they already have across the
// migration. If that were not true the migration would lock out every fleet on
// the platform at once.
//
// So this builds a database at the pre-011 schema, writes a site the old way,
// runs 011, and checks the same key still resolves.
func TestSiteKeyMigrationPreservesExistingCredentials(t *testing.T) {
	cfg := database.GetConfigFromEnv()
	scratch := envOr("TEST_DB_NAME", defaultTestDB) + "_sitekey"

	admin, err := database.Open(cfg.WithDatabase(envOr("TEST_ADMIN_DB", "postgres")))
	if err != nil {
		t.Fatalf("connecting to the maintenance database: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch)); err != nil {
		t.Fatalf("dropping the scratch database: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + quoteIdent(scratch)); err != nil {
		t.Fatalf("creating the scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch)) })

	db, err := database.Open(cfg.WithDatabase(scratch))
	if err != nil {
		t.Fatalf("connecting to the scratch database: %v", err)
	}
	defer db.Close()

	// The schema as it stood BEFORE this change.
	if err := applyMigrationsTo(db, "010"); err != nil {
		t.Fatalf("building the pre-011 schema: %v", err)
	}

	// A site provisioned the old way: plaintext, no hash, no prefix. This is
	// exactly the shape deploy/README.md step 6 produced.
	const legacyKey = "legacy-site-key-from-before-hashing"
	if _, err := db.Exec(`INSERT INTO companies (name, slug) VALUES ('Legacy Co', 'legacy')`); err != nil {
		t.Fatalf("seeding a company: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sites (company_id, site_name, api_key, active)
		SELECT id, 'Legacy Site', $1, TRUE FROM companies WHERE slug = 'legacy'`,
		legacyKey); err != nil {
		t.Fatalf("seeding a legacy site: %v", err)
	}

	// Run the migration.
	migration, err := os.ReadFile(filepath.Join("migrations", "011_site_credentials.sql"))
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("applying 011: %v", err)
	}

	// THE KEY THE TERMINAL ALREADY HOLDS STILL RESOLVES. The wire contract did
	// not change; only where the comparison happens did.
	var siteName string
	err = db.QueryRow(`
		SELECT site_name FROM sites
		 WHERE api_key_hash = $1 AND active = TRUE AND deleted_at IS NULL`,
		database.HashSiteKey(legacyKey)).Scan(&siteName)
	if err != nil {
		t.Fatalf("the pre-existing key no longer resolves after migrating: %v", err)
	}
	if siteName != "Legacy Site" {
		t.Errorf("resolved %q, want Legacy Site", siteName)
	}

	// The prefix was carried across for display, and is not the whole key.
	prefix := ""
	if err := db.QueryRow(
		`SELECT api_key_prefix FROM sites WHERE site_name = 'Legacy Site'`).Scan(&prefix); err != nil {
		t.Fatalf("reading the prefix: %v", err)
	}
	if prefix != legacyKey[:12] {
		t.Errorf("prefix = %q, want the first 12 characters of the key", prefix)
	}
	if prefix == legacyKey {
		t.Error("the whole key was stored as its own prefix")
	}

	// AND THE PLAINTEXT IS GONE.
	var stillThere bool
	if err := db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = 'public' AND table_name = 'sites'
			   AND column_name = 'api_key')`).Scan(&stillThere); err != nil {
		t.Fatalf("inspecting the schema: %v", err)
	}
	if stillThere {
		t.Error("sites.api_key survived the migration")
	}
}
