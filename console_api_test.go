package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The operator console API (/api/v1/console/*).
//
// Exercised through NewRouter, so the whole chain runs: session, CSRF, role
// gate, site grant, handler. What is being protected here is mostly negative --
// who cannot reach what, and what must never appear in a response.

func consoleCall(t *testing.T, router *gin.Engine, method, path, body, token, csrf string) (int, map[string]any) {
	t.Helper()
	code, decoded, _ := doAuth(t, router, authCall{
		method: method, path: path, body: body, token: token, csrf: csrf,
	})
	return code, decoded
}

// consoleOperatorSession creates an operator and logs it in.
func consoleOperatorSession(t *testing.T, router *gin.Engine, companyID int64,
	email, role string) (*models.User, string, string) {
	t.Helper()
	user := mustCreateOperator(t, companyID, email, role)
	token, csrf := login(t, router, email, testPassword)
	return user, token, csrf
}

func listOf(t *testing.T, body map[string]any, key string) []any {
	t.Helper()
	value, present := body[key]
	if !present {
		t.Fatalf("response has no %q: %v", key, body)
	}
	if value == nil {
		t.Fatalf("%q is null, want an array", key)
	}
	return value.([]any)
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// consoleRoutes is every route the console exposes, with the least-privileged
// role that may reach it. Used to prove the whole surface is behind
// authentication rather than spot-checking a few paths.
var consoleRoutes = []struct {
	method, path, minimumRole string
	body                      string
}{
	{"GET", "/api/v1/console/company", models.RoleViewer, ""},
	{"GET", "/api/v1/console/sites", models.RoleViewer, ""},
	{"GET", "/api/v1/console/applications", models.RoleViewer, ""},
	{"GET", "/api/v1/console/terminals", models.RoleViewer, ""},
	{"GET", "/api/v1/console/terminals/summary", models.RoleViewer, ""},
	{"GET", "/api/v1/console/terminals/TERM-1", models.RoleViewer, ""},
	{"GET", "/api/v1/console/people", models.RoleViewer, ""},
	{"POST", "/api/v1/console/people", models.RoleManager,
		`{"external_id":"P-NEW","full_name":"New Person"}`},
	{"PUT", "/api/v1/console/people/P-1", models.RoleManager, `{"full_name":"Renamed"}`},
	{"DELETE", "/api/v1/console/people/P-1", models.RoleManager, ""},
	{"PUT", "/api/v1/console/terminals/TERM-1/application-mode", models.RoleManager,
		`{"application_mode":"MULTI_PURPOSE"}`},
	{"GET", "/api/v1/console/operators", models.RoleAdmin, ""},
	{"POST", "/api/v1/console/operators", models.RoleAdmin,
		`{"email":"new@example.com","full_name":"New","password":"a-long-enough-password","role":"VIEWER"}`},
	{"PUT", "/api/v1/console/applications/ATTENDANCE", models.RoleOwner, `{"enabled":true}`},
}

func TestConsoleRejectsUnauthenticatedRequests(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	for _, route := range consoleRoutes {
		name := route.method + " " + route.path
		t.Run(name, func(t *testing.T) {
			// No credential at all.
			code, body := consoleCall(t, env.router, route.method, route.path, route.body, "", "")
			if code != http.StatusUnauthorized {
				t.Errorf("without a session = %d, want 401 (%v)", code, body)
			}

			// A SITE API KEY IS NOT BROWSER AUTHENTICATION. It is the
			// provisioning secret; presenting it to the console must achieve
			// nothing at all.
			req := newRequestWithSiteKey(t, route.method, route.path, route.body, env.siteAKey)
			w := serve(env.router, req)
			if w != http.StatusUnauthorized {
				t.Errorf("with a site API key = %d, want 401", w)
			}
		})
	}
}

func TestConsoleAllowsAuthenticatedOperator(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "viewer@example.com", models.RoleViewer)

	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/company", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /company = %d (%v)", code, body)
	}
	if body["slug"] != "one" || body["name"] != "Company One" {
		t.Errorf("company = %v", body)
	}

	code, body = consoleCall(t, env.router, "GET", "/api/v1/console/sites", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /sites = %d (%v)", code, body)
	}
	sites := listOf(t, body, "sites")
	if len(sites) != 2 {
		t.Errorf("an unscoped operator sees %d sites, want both in the company", len(sites))
	}

	// Empty collections are arrays, never null, so a client can iterate.
	code, body = consoleCall(t, env.router, "GET", "/api/v1/console/people", "", token, "")
	if code != http.StatusOK || listOf(t, body, "people") == nil {
		t.Errorf("GET /people = %d (%v)", code, body)
	}
}

func TestConsoleRejectsDisabledOperatorAndCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	user, token, _ := consoleOperatorSession(t, env.router, one, "willdisable@example.com", models.RoleViewer)
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/company", "", token, ""); code != http.StatusOK {
		t.Fatalf("the session should work before being disabled: %d", code)
	}

	// Disabling revokes every session in the same transaction, so the next
	// request is refused rather than served from a live cookie.
	if err := database.SetUserActive(one, user.ID, false); err != nil {
		t.Fatalf("disabling operator: %v", err)
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/company", "", token, ""); code != http.StatusUnauthorized {
		t.Errorf("a disabled operator reached the console: %d", code)
	}

	// A disabled COMPANY takes its operators with it.
	_, otherToken, _ := consoleOperatorSession(t, env.router, two, "othertenant@example.com", models.RoleOwner)
	mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, two)
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/company", "", otherToken, ""); code != http.StatusUnauthorized {
		t.Errorf("an operator of a disabled company reached the console: %d", code)
	}
}

// ---------------------------------------------------------------------------
// Tenancy
// ---------------------------------------------------------------------------

func TestConsoleCompanyIsolation(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	mustCreateDevice(t, "Site A", "ONE-TERM-1")
	mustCreateDevice(t, "Site C", "TWO-TERM-1")

	_, tokenOne, csrfOne := consoleOperatorSession(t, env.router, one, "one-owner@example.com", models.RoleOwner)
	_, tokenTwo, _ := consoleOperatorSession(t, env.router, two, "two-owner@example.com", models.RoleOwner)

	// A person in company one only.
	if code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"ONE-P1","full_name":"Company One Person"}`, tokenOne, csrfOne); code != http.StatusCreated {
		t.Fatalf("creating a person = %d (%v)", code, body)
	}

	// Company two sees none of it.
	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/people", "", tokenTwo, "")
	if people := listOf(t, body, "people"); len(people) != 0 {
		t.Errorf("company two sees %d people from company one", len(people))
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/people/ONE-P1", "", tokenTwo, ""); code != http.StatusNotFound {
		t.Errorf("cross-tenant person read = %d, want 404", code)
	}

	// Sites, terminals and operators are all scoped the same way.
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/sites", "", tokenTwo, "")
	sites := listOf(t, body, "sites")
	if len(sites) != 1 || sites[0].(map[string]any)["name"] != "Site C" {
		t.Errorf("company two sees sites %v, want only its own", sites)
	}

	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", tokenTwo, "")
	terminals := listOf(t, body, "terminals")
	if len(terminals) != 1 || terminals[0].(map[string]any)["serial_number"] != "TWO-TERM-1" {
		t.Errorf("company two sees terminals %v, want only its own", terminals)
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/terminals/ONE-TERM-1", "", tokenTwo, ""); code != http.StatusNotFound {
		t.Errorf("cross-tenant terminal read = %d, want 404", code)
	}

	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/operators", "", tokenTwo, "")
	operators := listOf(t, body, "operators")
	if len(operators) != 1 {
		t.Errorf("company two sees %d operators, want only its own", len(operators))
	}
	if body["count"].(float64) != 1 {
		t.Errorf("operator count = %v, want 1", body["count"])
	}
}

// ---------------------------------------------------------------------------
// Site grants
// ---------------------------------------------------------------------------

func TestConsoleSiteGrantEnforcement(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	siteC := operatorSitePublicID(t, "Site C") // company two

	mustCreateDevice(t, "Site A", "A-TERM-1")
	mustCreateDevice(t, "Site B", "B-TERM-1")

	scoped, token, csrf := consoleOperatorSession(t, env.router, one, "scoped-mgr@example.com", models.RoleManager)
	if err := database.ReplaceSiteGrants(one, scoped.ID, []string{siteA}); err != nil {
		t.Fatalf("granting: %v", err)
	}

	// Lists are narrowed to the grant.
	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/sites", "", token, "")
	sites := listOf(t, body, "sites")
	if len(sites) != 1 || sites[0].(map[string]any)["name"] != "Site A" {
		t.Errorf("scoped operator sees %v, want only Site A", sites)
	}

	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	terminals := listOf(t, body, "terminals")
	if len(terminals) != 1 || terminals[0].(map[string]any)["serial_number"] != "A-TERM-1" {
		t.Errorf("scoped operator sees terminals %v, want only Site A's", terminals)
	}

	// Detail routes agree with the list.
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteA, "", token, ""); code != http.StatusOK {
		t.Errorf("granted site = %d, want 200", code)
	}
	// In the company but not granted: it exists and they may know it does.
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteB, "", token, ""); code != http.StatusForbidden {
		t.Errorf("ungranted site = %d, want 403", code)
	}
	// Another company: not found, never forbidden.
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteC, "", token, ""); code != http.StatusNotFound {
		t.Errorf("cross-company site = %d, want 404", code)
	}

	// Site settings, read and write, obey the same gate.
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteA+"/settings", "", token, ""); code != http.StatusOK {
		t.Error("granted site settings were refused")
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/sites/"+siteA+"/settings",
		`{"unlock_duration_seconds":7}`, token, csrf); code != http.StatusOK {
		t.Error("writing granted site settings was refused")
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/sites/"+siteB+"/settings",
		`{"unlock_duration_seconds":7}`, token, csrf); code != http.StatusForbidden {
		t.Error("writing ungranted site settings was allowed")
	}
	// CSRF still applies on top of the grant.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/sites/"+siteA+"/settings",
		`{"unlock_duration_seconds":7}`, token, ""); code != http.StatusForbidden {
		t.Error("a site settings write without a CSRF token was allowed")
	}

	// An OWNER is never scoped, and still cannot cross a tenant boundary.
	_, ownerToken, _ := consoleOperatorSession(t, env.router, one, "unscoped-owner@example.com", models.RoleOwner)
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteB, "", ownerToken, ""); code != http.StatusOK {
		t.Error("an OWNER was refused a site in its own company")
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/sites/"+siteC, "", ownerToken, ""); code != http.StatusNotFound {
		t.Error("an OWNER reached another company's site")
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func TestConsoleRoleEnforcement(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "ROLE-TERM-1")

	tokens := map[string]string{}
	csrfs := map[string]string{}
	for _, role := range []string{models.RoleViewer, models.RoleManager, models.RoleAdmin, models.RoleOwner} {
		_, token, csrf := consoleOperatorSession(t, env.router, one,
			strings.ToLower(role)+"-role@example.com", role)
		tokens[role] = token
		csrfs[role] = csrf
	}

	// Each route, and the lowest role that may reach it.
	checks := []struct {
		name, method, path, body, minimum string
	}{
		{"read people", "GET", "/api/v1/console/people", "", models.RoleViewer},
		{"read terminals", "GET", "/api/v1/console/terminals", "", models.RoleViewer},
		{"read applications", "GET", "/api/v1/console/applications", "", models.RoleViewer},
		{"create a person", "POST", "/api/v1/console/people",
			`{"external_id":"ROLE-P","full_name":"Role Person"}`, models.RoleManager},
		{"configure a terminal", "PUT", "/api/v1/console/terminals/ROLE-TERM-1/application-mode",
			`{"application_mode":"MULTI_PURPOSE"}`, models.RoleManager},
		{"list operators", "GET", "/api/v1/console/operators", "", models.RoleAdmin},
		{"enable an application", "PUT", "/api/v1/console/applications/ATTENDANCE",
			`{"enabled":true}`, models.RoleOwner},
	}

	rank := map[string]int{
		models.RoleViewer: 1, models.RoleManager: 2, models.RoleAdmin: 3, models.RoleOwner: 4,
	}

	for _, check := range checks {
		for _, actor := range []string{models.RoleViewer, models.RoleManager, models.RoleAdmin, models.RoleOwner} {
			t.Run(check.name+"/"+actor, func(t *testing.T) {
				code, body := consoleCall(t, env.router, check.method, check.path,
					check.body, tokens[actor], csrfs[actor])

				if rank[actor] < rank[check.minimum] {
					if code != http.StatusForbidden {
						t.Errorf("%s as %s = %d, want 403 (%v)", check.name, actor, code, body)
					}
					return
				}
				if code == http.StatusForbidden || code == http.StatusUnauthorized {
					t.Errorf("%s as %s = %d, want it allowed (%v)", check.name, actor, code, body)
				}
				// Creating the same person twice is a 409, not an authorization
				// problem -- the point here is only that the gate let it past.
			})
		}
	}
}

func TestConsoleOperatorAdministrationGuards(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	admin, adminToken, adminCSRF := consoleOperatorSession(t, env.router, one, "admin-guard@example.com", models.RoleAdmin)
	owner, ownerToken, ownerCSRF := consoleOperatorSession(t, env.router, one, "owner-guard@example.com", models.RoleOwner)

	// An ADMIN manages everyone below an OWNER, but cannot create one...
	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/operators",
		`{"email":"newowner@example.com","full_name":"New Owner","password":"a-long-enough-password","role":"OWNER"}`,
		adminToken, adminCSRF)
	if code != http.StatusForbidden {
		t.Errorf("ADMIN creating an OWNER = %d, want 403 (%v)", code, body)
	}
	// ...nor promote anyone to one, nor modify an existing one.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/operators/"+owner.PublicID,
		`{"active":false}`, adminToken, adminCSRF); code != http.StatusForbidden {
		t.Errorf("ADMIN modifying an OWNER = %d, want 403", code)
	}
	if code, _ := consoleCall(t, env.router, "DELETE", "/api/v1/console/operators/"+owner.PublicID,
		"", adminToken, adminCSRF); code != http.StatusForbidden {
		t.Errorf("ADMIN deleting an OWNER = %d, want 403", code)
	}

	// An ADMIN may create a MANAGER.
	code, body = consoleCall(t, env.router, "POST", "/api/v1/console/operators",
		`{"email":"made@example.com","full_name":"Made","password":"a-long-enough-password","role":"MANAGER"}`,
		adminToken, adminCSRF)
	if code != http.StatusCreated {
		t.Fatalf("ADMIN creating a MANAGER = %d (%v)", code, body)
	}
	madeID := body["id"].(string)
	if _, present := body["password"]; present {
		t.Error("the created operator response echoes a password")
	}

	// Duplicate address is a conflict, not a server fault.
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/operators",
		`{"email":"made@example.com","full_name":"Again","password":"a-long-enough-password","role":"VIEWER"}`,
		adminToken, adminCSRF); code != http.StatusConflict {
		t.Errorf("duplicate operator = %d, want 409", code)
	}
	// A weak password is rejected with the policy, before an account exists.
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/operators",
		`{"email":"weak@example.com","full_name":"Weak","password":"short","role":"VIEWER"}`,
		adminToken, adminCSRF); code != http.StatusBadRequest {
		t.Errorf("weak password = %d, want 400", code)
	}

	// Nobody may change their own role or disable themselves: the sole OWNER
	// demoting themselves would leave nobody able to manage operators.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/operators/"+owner.PublicID,
		`{"role":"VIEWER"}`, ownerToken, ownerCSRF); code != http.StatusForbidden {
		t.Error("an OWNER changed its own role")
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/operators/"+admin.PublicID,
		`{"active":false}`, adminToken, adminCSRF); code != http.StatusForbidden {
		t.Error("an operator disabled its own account")
	}
	if code, _ := consoleCall(t, env.router, "DELETE", "/api/v1/console/operators/"+admin.PublicID,
		"", adminToken, adminCSRF); code != http.StatusForbidden {
		t.Error("an operator deleted its own account")
	}

	// Site grants, replaced wholesale and confined to the company.
	siteA := operatorSitePublicID(t, "Site A")
	siteC := operatorSitePublicID(t, "Site C") // company two
	code, body = consoleCall(t, env.router, "PUT", "/api/v1/console/operators/"+madeID+"/sites",
		fmt.Sprintf(`{"site_ids":[%q]}`, siteA), adminToken, adminCSRF)
	if code != http.StatusOK {
		t.Fatalf("granting a site = %d (%v)", code, body)
	}
	if body["all_sites"] != false || len(listOf(t, body, "sites")) != 1 {
		t.Errorf("grants = %v, want one site and all_sites false", body)
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/operators/"+madeID+"/sites",
		fmt.Sprintf(`{"site_ids":[%q]}`, siteC), adminToken, adminCSRF); code != http.StatusBadRequest {
		t.Error("a site from another company was granted")
	}
	// The refused call left the previous grants alone.
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/operators/"+madeID+"/sites", "", adminToken, adminCSRF)
	if len(listOf(t, body, "sites")) != 1 {
		t.Error("a refused grant changed the operator's site access")
	}

	// An unknown operator is not found, and a malformed id is too -- never a 500.
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "not-a-uuid"} {
		if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/operators/"+id, "", adminToken, adminCSRF); code != http.StatusNotFound {
			t.Errorf("operator %q = %d, want 404", id, code)
		}
	}
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

func TestConsoleApplicationConfiguration(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "app-owner@example.com", models.RoleOwner)

	// A company starts with nothing enabled, and the catalog is advertised so a
	// dashboard does not hard-code the capability list.
	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/applications", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /applications = %d (%v)", code, body)
	}
	if len(listOf(t, body, "configured")) != 0 || len(listOf(t, body, "enabled")) != 0 {
		t.Errorf("a new company has applications configured: %v", body)
	}
	if len(listOf(t, body, "available")) != len(models.ApplicationOrder) {
		t.Errorf("available = %v, want the whole catalog", body["available"])
	}

	// Enable one with settings.
	code, body = consoleCall(t, env.router, "PUT", "/api/v1/console/applications/ATTENDANCE",
		`{"enabled":true,"settings":{"grace_minutes":5}}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("enabling ATTENDANCE = %d (%v)", code, body)
	}
	if body["code"] != models.AppAttendance || body["enabled"] != true {
		t.Errorf("response = %v", body)
	}
	settings, ok := body["settings"].(map[string]any)
	if !ok || settings["grace_minutes"] != float64(5) {
		t.Errorf("settings = %v, want the object that was sent", body["settings"])
	}

	// Disable it: it disappears from `enabled` but keeps its configuration.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/applications/ATTENDANCE",
		`{"enabled":false}`, token, csrf); code != http.StatusOK {
		t.Fatal("disabling failed")
	}
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/applications", "", token, "")
	if len(listOf(t, body, "enabled")) != 0 {
		t.Errorf("a disabled application is still reported as enabled: %v", body["enabled"])
	}
	configured := listOf(t, body, "configured")
	if len(configured) != 1 {
		t.Fatalf("configured = %v, want the disabled row retained", configured)
	}
	entry := configured[0].(map[string]any)
	if entry["enabled"] != false {
		t.Error("the retained row is not marked disabled")
	}
	if kept := entry["settings"].(map[string]any); kept["grace_minutes"] != float64(5) {
		t.Errorf("disabling lost the settings: %v", entry["settings"])
	}

	// Rejections.
	rejections := []struct {
		name, path, body string
		want             int
	}{
		{"unknown capability", "/api/v1/console/applications/GYM_MEMBERSHIP", `{"enabled":true}`, http.StatusBadRequest},
		{"lowercase code", "/api/v1/console/applications/attendance", `{"enabled":true}`, http.StatusBadRequest},
		// MULTI_PURPOSE is a TERMINAL mode. It is not a capability a company
		// can enable, and the API must say so rather than quietly accept it.
		{"a device mode as an application", "/api/v1/console/applications/MULTI_PURPOSE",
			`{"enabled":true}`, http.StatusBadRequest},
		{"settings that are not an object", "/api/v1/console/applications/CHECK_IN",
			`{"enabled":true,"settings":[1,2]}`, http.StatusBadRequest},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := consoleCall(t, env.router, "PUT", tc.path, tc.body, token, csrf); code != tc.want {
				t.Errorf("%s = %d, want %d (%v)", tc.name, code, tc.want, body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

func TestConsoleTerminalListingAndConfiguration(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "CFG-TERM-1")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "term-owner@example.com", models.RoleOwner)

	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	if code != http.StatusOK || len(listOf(t, body, "terminals")) != 1 {
		t.Fatalf("GET /terminals = %d (%v)", code, body)
	}

	if code, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals/summary", "", token, ""); code != http.StatusOK {
		t.Fatalf("GET /terminals/summary = %d (%v)", code, body)
	}
	if body["total"].(float64) != 1 {
		t.Errorf("summary total = %v, want 1", body["total"])
	}

	// Every terminal defaults to MULTI_PURPOSE, so nothing already deployed
	// changes behaviour, and it resolves to whatever the company has enabled.
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals/CFG-TERM-1", "", token, "")
	if body["application_mode"] != models.AppMultiPurpose {
		t.Errorf("default mode = %v, want MULTI_PURPOSE", body["application_mode"])
	}
	if len(listOf(t, body, "effective_applications")) != 0 {
		t.Errorf("effective = %v, want nothing while the company has no modules", body["effective_applications"])
	}

	// A capability the company has not enabled cannot be assigned.
	if code, body = consoleCall(t, env.router, "PUT", "/api/v1/console/terminals/CFG-TERM-1/application-mode",
		`{"application_mode":"CHECK_IN"}`, token, csrf); code != http.StatusConflict {
		t.Errorf("assigning a disabled capability = %d, want 409 (%v)", code, body)
	}

	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/applications/CHECK_IN",
		`{"enabled":true}`, token, csrf); code != http.StatusOK {
		t.Fatal("enabling CHECK_IN failed")
	}
	code, body = consoleCall(t, env.router, "PUT", "/api/v1/console/terminals/CFG-TERM-1/application-mode",
		`{"application_mode":"CHECK_IN"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("assigning an enabled capability = %d (%v)", code, body)
	}
	if body["application_mode"] != models.AppCheckIn {
		t.Errorf("mode = %v, want CHECK_IN", body["application_mode"])
	}
	if effective := listOf(t, body, "effective_applications"); len(effective) != 1 {
		t.Errorf("effective = %v, want only CHECK_IN", effective)
	}

	// Disabling the capability retains the assignment and stops it resolving.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/applications/CHECK_IN",
		`{"enabled":false}`, token, csrf); code != http.StatusOK {
		t.Fatal("disabling CHECK_IN failed")
	}
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals/CFG-TERM-1", "", token, "")
	if body["application_mode"] != models.AppCheckIn {
		t.Error("disabling a capability rewrote the terminal's assignment")
	}
	if len(listOf(t, body, "effective_applications")) != 0 {
		t.Error("a disabled capability still resolves")
	}

	// Bad input.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/terminals/CFG-TERM-1/application-mode",
		`{"application_mode":"DOOR_OPENER"}`, token, csrf); code != http.StatusBadRequest {
		t.Error("an unknown application mode was accepted")
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/terminals/NO-SUCH-SERIAL/application-mode",
		`{"application_mode":"MULTI_PURPOSE"}`, token, csrf); code != http.StatusNotFound {
		t.Error("an unknown terminal was not reported as not found")
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/terminals/NO-SUCH-SERIAL", "", token, ""); code != http.StatusNotFound {
		t.Error("reading an unknown terminal was not a 404")
	}
}

// TestConsoleTerminalGrantEnforcement covers the routes that name ONE terminal.
//
// The list was always narrowed to the caller's grants; the detail and the
// application-mode write were company-scoped only, so a scoped operator could
// not see another site's terminal in the list and could still read it -- and
// repoint it -- by naming its serial. Serials are printed on the hardware, so
// "they would have to know the serial" was never a control.
//
// The 404/403 split is asserted in both directions here, because getting it
// backwards leaks tenancy: 403 on another company's serial would confirm that
// the serial is registered to somebody.
func TestConsoleTerminalGrantEnforcement(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	siteA := operatorSitePublicID(t, "Site A")

	mustCreateDevice(t, "Site A", "GRANT-A-1")
	mustCreateDevice(t, "Site B", "GRANT-B-1")
	mustCreateDevice(t, "Site C", "GRANT-C-1") // company two

	scoped, token, csrf := consoleOperatorSession(t, env.router, one,
		"terminal-scoped@example.com", models.RoleManager)
	if err := database.ReplaceSiteGrants(one, scoped.ID, []string{siteA}); err != nil {
		t.Fatalf("granting: %v", err)
	}

	// The list is narrowed, and the detail route agrees with it.
	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	terminals := listOf(t, body, "terminals")
	if len(terminals) != 1 || terminals[0].(map[string]any)["serial_number"] != "GRANT-A-1" {
		t.Fatalf("scoped list = %v, want only GRANT-A-1", terminals)
	}

	if code, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/GRANT-A-1", "", token, ""); code != http.StatusOK {
		t.Errorf("granted terminal = %d, want 200 (%v)", code, body)
	}

	// Same company, ungranted site: it exists and they may know it does.
	if code, _ := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/GRANT-B-1", "", token, ""); code != http.StatusForbidden {
		t.Errorf("reading an ungranted terminal = %d, want 403", code)
	}

	// Another company: not found, never forbidden.
	if code, _ := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/GRANT-C-1", "", token, ""); code != http.StatusNotFound {
		t.Errorf("reading another tenant's terminal = %d, want 404", code)
	}
	if code, _ := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/NO-SUCH-SERIAL", "", token, ""); code != http.StatusNotFound {
		t.Errorf("reading an unknown terminal = %d, want 404", code)
	}

	// The write obeys the same gate.
	if code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/GRANT-A-1/application-mode",
		`{"application_mode":"MULTI_PURPOSE"}`, token, csrf); code != http.StatusOK {
		t.Error("configuring a granted terminal was refused")
	}
	if code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/GRANT-B-1/application-mode",
		`{"application_mode":"MULTI_PURPOSE"}`, token, csrf); code != http.StatusForbidden {
		t.Error("configuring an ungranted terminal was allowed")
	}
	if code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/GRANT-C-1/application-mode",
		`{"application_mode":"MULTI_PURPOSE"}`, token, csrf); code != http.StatusNotFound {
		t.Error("configuring another tenant's terminal did not report 404")
	}

	// The mode really did not change on the terminal the write was refused for.
	if mode := queryString(t,
		`SELECT application_mode FROM devices WHERE serial_number = 'GRANT-B-1'`); mode != models.AppMultiPurpose {
		t.Errorf("ungranted terminal mode = %q, want it untouched", mode)
	}

	// THE GATE RUNS BEFORE THE HANDLER. A malformed body against an ungranted
	// terminal must be refused as forbidden, not validated -- otherwise the
	// error message becomes an oracle for which serials exist.
	if code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/GRANT-B-1/application-mode",
		`{"application_mode":"NONSENSE"}`, token, csrf); code != http.StatusForbidden {
		t.Error("an ungranted terminal validated its body before checking the grant")
	}

	// The summary is narrowed to the same scope as the list. A company-wide
	// rollup over a scoped list both misreads and discloses the wider fleet.
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals/summary", "", token, "")
	if total := body["total"].(float64); total != 1 {
		t.Errorf("scoped summary total = %v, want 1 (only Site A's terminal)", total)
	}

	// An ADMIN is never scoped by grants, and still cannot cross the tenant.
	_, adminToken, adminCSRF := consoleOperatorSession(t, env.router, one,
		"terminal-admin@example.com", models.RoleAdmin)
	if code, _ := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/GRANT-B-1", "", adminToken, ""); code != http.StatusOK {
		t.Error("an ADMIN was refused a terminal in its own company")
	}
	if code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/GRANT-B-1/application-mode",
		`{"application_mode":"MULTI_PURPOSE"}`, adminToken, adminCSRF); code != http.StatusOK {
		t.Error("an ADMIN was refused a terminal write in its own company")
	}
	if code, _ := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/GRANT-C-1", "", adminToken, ""); code != http.StatusNotFound {
		t.Error("an ADMIN reached another company's terminal")
	}
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/terminals/summary", "", adminToken, "")
	if total := body["total"].(float64); total != 2 {
		t.Errorf("unscoped summary total = %v, want 2 (the company's own terminals)", total)
	}
}

// TestConsoleTerminalsCarryAJoinableSiteID covers the identifier a console
// needs to relate the two resources it shows side by side.
//
// A terminal used to carry only the internal site row id and the site's NAME.
// /console/sites keys its entries by public_id, so a browser holding both had
// nothing to match them on except the name -- which is editable and not unique,
// so scoping terminals to the selected site would have been wrong precisely
// when two sites were named alike.
func TestConsoleTerminalsCarryAJoinableSiteID(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "JOIN-1")

	_, token, _ := consoleOperatorSession(t, env.router, one, "join@example.com", models.RoleOwner)

	_, sitesBody := consoleCall(t, env.router, "GET", "/api/v1/console/sites", "", token, "")
	siteIDs := map[string]string{}
	for _, entry := range listOf(t, sitesBody, "sites") {
		site := entry.(map[string]any)
		siteIDs[site["name"].(string)] = site["id"].(string)
	}
	wantSiteID := siteIDs["Site A"]
	if wantSiteID == "" {
		t.Fatal("Site A has no public id in the sites listing")
	}

	// The list.
	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	terminals := listOf(t, body, "terminals")
	if len(terminals) != 1 {
		t.Fatalf("expected one terminal, got %v", terminals)
	}
	listed := terminals[0].(map[string]any)
	if got := listed["site_public_id"]; got != wantSiteID {
		t.Errorf("terminal site_public_id = %v, want %v (the id /console/sites uses)", got, wantSiteID)
	}

	// And the detail, which must agree with the row it was opened from.
	_, detail := consoleCall(t, env.router, "GET", "/api/v1/console/terminals/JOIN-1", "", token, "")
	if got := detail["site_public_id"]; got != wantSiteID {
		t.Errorf("terminal detail site_public_id = %v, want %v", got, wantSiteID)
	}

	// The internal id is still present -- this was additive, and terminals and
	// existing tooling speak that contract.
	if _, present := listed["site_id"]; !present {
		t.Error("site_id disappeared from the inventory projection")
	}
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

func TestConsolePeopleManagement(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "people-mgr@example.com", models.RoleManager)

	// Category is OPTIONAL. A company doing visitor management has no
	// "membership", and requiring one would be the product assuming a workflow.
	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"P-100","full_name":"Sam Taylor"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("creating a person = %d (%v)", code, body)
	}
	if body["external_id"] != "P-100" || body["full_name"] != "Sam Taylor" {
		t.Errorf("created person = %v", body)
	}
	if body["active"] != true {
		t.Error("a new person should default to active")
	}
	if body["biometric_enrolled"] != false {
		t.Error("a new person should have no biometric credential")
	}

	// A duplicate external id is the caller's mistake.
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"P-100","full_name":"Someone Else"}`, token, csrf); code != http.StatusConflict {
		t.Error("a duplicate external_id was accepted")
	}
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"full_name":"No Identifier"}`, token, csrf); code != http.StatusBadRequest {
		t.Error("a person without an external_id was accepted")
	}

	// Enrol a credential the way a terminal would, then confirm the console
	// reports its existence without ever disclosing it.
	mustExec(t, `UPDATE people SET fingerprint_template = 'terminal:TERM-1:slot:4'
	              WHERE external_id = 'P-100' AND company_id = $1`, one)

	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/people/P-100", "", token, "")
	if body["biometric_enrolled"] != true {
		t.Error("an enrolled person is not reported as enrolled")
	}
	if raw := mustJSON(t, body); strings.Contains(raw, "slot:4") || strings.Contains(raw, "fingerprint") {
		t.Errorf("the credential locator leaked into the response: %s", raw)
	}

	// AN UPDATE MUST NOT UNENROL ANYONE. UpdateMember clears the template when
	// given an empty one, so a rename that did not carry it through would
	// silently delete the person's biometric credential.
	code, body = consoleCall(t, env.router, "PUT", "/api/v1/console/people/P-100",
		`{"full_name":"Sam Taylor-Jones"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("updating a person = %d (%v)", code, body)
	}
	if body["full_name"] != "Sam Taylor-Jones" {
		t.Errorf("the rename did not apply: %v", body)
	}
	if body["biometric_enrolled"] != true {
		t.Error("updating a person dropped their biometric credential")
	}
	stored := queryString(t, `SELECT COALESCE(fingerprint_template,'') FROM people
	                           WHERE external_id = 'P-100' AND company_id = $1`, one)
	if stored != "terminal:TERM-1:slot:4" {
		t.Errorf("the stored credential is now %q -- the update erased it", stored)
	}

	// Deactivating through the console.
	_, body = consoleCall(t, env.router, "PUT", "/api/v1/console/people/P-100",
		`{"full_name":"Sam Taylor-Jones","active":false}`, token, csrf)
	if body["active"] != false {
		t.Error("the person was not deactivated")
	}

	// Listing.
	_, body = consoleCall(t, env.router, "GET", "/api/v1/console/people", "", token, "")
	people := listOf(t, body, "people")
	if len(people) != 1 {
		t.Fatalf("people = %v, want the one created", people)
	}
	if raw := mustJSON(t, people); strings.Contains(raw, "fingerprint_template") {
		t.Errorf("the people list exposes fingerprint_template: %s", raw)
	}

	// Deletion is a soft delete, and idempotent.
	if code, _ := consoleCall(t, env.router, "DELETE", "/api/v1/console/people/P-100", "", token, csrf); code != http.StatusNoContent {
		t.Error("deleting a person did not return 204")
	}
	if code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/people/P-100", "", token, ""); code != http.StatusNotFound {
		t.Error("a deleted person is still readable")
	}
	if code, _ := consoleCall(t, env.router, "DELETE", "/api/v1/console/people/P-100", "", token, csrf); code != http.StatusNoContent {
		t.Error("deleting twice was not idempotent")
	}
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/people/GONE",
		`{"full_name":"Nobody"}`, token, csrf); code != http.StatusNotFound {
		t.Error("updating a nonexistent person was not a 404")
	}
}

// ---------------------------------------------------------------------------
// The site API key must never reach a browser
// ---------------------------------------------------------------------------

func TestConsoleNeverExposesTheSiteAPIKey(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "KEY-TERM-1")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "keycheck@example.com", models.RoleOwner)
	siteA := operatorSitePublicID(t, "Site A")

	// Sweep every console read an operator can perform.
	paths := []string{
		"/api/v1/console/company",
		"/api/v1/console/sites",
		"/api/v1/console/sites/" + siteA,
		"/api/v1/console/sites/" + siteA + "/settings",
		"/api/v1/console/applications",
		"/api/v1/console/terminals",
		"/api/v1/console/terminals/summary",
		"/api/v1/console/terminals/KEY-TERM-1",
		"/api/v1/console/people",
		"/api/v1/console/operators",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			code, decoded, res := doAuth(t, env.router, authCall{
				method: "GET", path: path, token: token, csrf: csrf,
			})
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d (%v)", path, code, decoded)
			}

			raw := readBody(t, res)
			// Without this the sweep below would pass on an empty string, which
			// is the way a test like this silently stops testing anything.
			if len(raw) < 3 {
				t.Fatalf("read %q as the body of %s; the leak check would be vacuous", raw, path)
			}
			for _, secret := range []string{env.siteAKey, env.siteBKey, env.siteCKey} {
				if strings.Contains(raw, secret) {
					t.Errorf("%s leaked a site API key", path)
				}
			}
			// Nor any field that could carry one, or a device credential.
			for _, field := range []string{"api_key", "apiKey", "api_key_hash", "atd_", "ats_"} {
				if strings.Contains(raw, field) {
					t.Errorf("%s contains %q: %s", path, field, raw)
				}
			}
		})
	}

	// And the console offers no route that could mint a device credential:
	// registration remains a site-key operation on the device API.
	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/terminals",
		`{"serial_number":"NEW-1"}`, token, csrf); code != http.StatusNotFound &&
		code != http.StatusMethodNotAllowed {
		t.Errorf("the console exposes a terminal registration route: %d", code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newRequestWithSiteKey(t *testing.T, method, path, body, siteKey string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", siteKey)
	return req
}

// serve runs one request and reports the status code.
func serve(router *gin.Engine, req *http.Request) int {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	decoder := json.NewDecoder(res.Body)
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return ""
	}
	return mustJSON(t, payload)
}
