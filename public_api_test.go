package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// P3: the four public read routes, end to end through the real router --
// API_SPEC.md section 18. Credentials are issued through the console exactly
// as a customer's would be; nothing is seeded around the authentication path.

// publicGet performs GET on the public tree with a bearer credential.
func publicGet(t *testing.T, env *testEnv, secret, path string) (int, http.Header, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	var body map[string]any
	raw := w.Body.String()
	if raw != "" {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: body is not a JSON object: %q", path, raw)
		}
	}
	return w.Code, w.Header(), body, raw
}

// publicError asserts the section 18 envelope and returns the code.
func publicError(t *testing.T, status int, headers http.Header, body map[string]any, wantStatus int) string {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status %d, want %d (body %v)", status, wantStatus, body)
	}
	detail, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error is not an object: %v", body)
	}
	code, _ := detail["code"].(string)
	if code == "" || detail["type"] == nil || detail["message"] == nil {
		t.Fatalf("envelope incomplete: %v", detail)
	}
	if detail["request_id"] != headers.Get("X-Request-ID") || detail["request_id"] == "" {
		t.Errorf("request_id %v does not match X-Request-ID %q", detail["request_id"], headers.Get("X-Request-ID"))
	}
	if !models.KnownAPIErrorCode(code) {
		t.Errorf("code %q is not in the registry", code)
	}
	if models.APIErrors[code].Status != status {
		t.Errorf("code %q served with %d, registry says %d", code, status, models.APIErrors[code].Status)
	}
	return code
}

func publicCredential(t *testing.T, env *testEnv, slug, email, body string) string {
	t.Helper()
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, slug)
	_, token, csrf := consoleOperatorSession(t, env.router, companyID, email, models.RoleAdmin)
	return secretOf(t, issueCredential(t, env, token, csrf, body))
}

// ---------------------------------------------------------------------------
// Authentication and scope at the route
// ---------------------------------------------------------------------------

func TestPublicRoutesRefuseWithoutACredential(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{
		"/api/public/v1/members", "/api/public/v1/members/X",
		"/api/public/v1/sites", "/api/public/v1/sites/X",
	} {
		status, headers, body, _ := publicGet(t, env, "", path)
		if code := publicError(t, status, headers, body, http.StatusUnauthorized); code != models.CodeCredentialMissing {
			t.Errorf("%s without a credential: code %s", path, code)
		}
		if got := headers.Get("WWW-Authenticate"); got != `Bearer realm="accesslink"` {
			t.Errorf("%s: WWW-Authenticate = %q", path, got)
		}
	}

	// The five documented 401 cases (API_SPEC.md section 18), every one with
	// the challenge header: missing, wrong scheme, malformed, wrong
	// environment, unknown/revoked. Revoked is exercised through a real
	// credential so the store's refusal path is the one under test.
	cheapBcrypt(t)
	_, token, csrf := consoleOperatorSession(t, env.router, operatorCompanyID(t, "one"),
		"p3-revoked@example.com", models.RoleAdmin)
	issued := issueCredential(t, env, token, csrf, `{"name":"to revoke","scopes":["sites:read"]}`)
	revoked := secretOf(t, issued)
	if code, _ := consoleCall(t, env.router, "DELETE", credentialsPath+"/"+issued["id"].(string),
		`{"reason":"test"}`, token, csrf); code != http.StatusOK {
		t.Fatalf("revoking through the console = %d", code)
	}
	cases := []struct {
		name, authorization, code string
	}{
		{"missing", "", models.CodeCredentialMissing},
		{"wrong scheme", "Basic dXNlcjpwYXNz", models.CodeCredentialMissing},
		{"malformed", "Bearer not-a-key", models.CodeCredentialInvalid},
		{"wrong environment", "Bearer atp_test_" + strings.Repeat("0", 64), models.CodeCredentialWrongEnvironment},
		{"unknown", "Bearer atp_live_" + strings.Repeat("0", 64), models.CodeCredentialInvalid},
		{"revoked", "Bearer " + revoked, models.CodeCredentialInvalid},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/public/v1/sites", nil)
		if c.authorization != "" {
			req.Header.Set("Authorization", c.authorization)
		}
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if code := publicError(t, w.Code, w.Header(), body, http.StatusUnauthorized); code != c.code {
			t.Errorf("%s: code %s, want %s", c.name, code, c.code)
		}
		if got := w.Header().Get("WWW-Authenticate"); got != `Bearer realm="accesslink"` {
			t.Errorf("%s: WWW-Authenticate = %q, want the challenge on every 401", c.name, got)
		}
	}

	// And the challenge is a 401 thing only: no 403, 404 or 400 carries it.
	valid := publicCredential(t, env, "one", "p3-nochallenge@example.com", `{"name":"sites","scopes":["sites:read"]}`)
	for path, want := range map[string]int{
		"/api/public/v1/members":            http.StatusForbidden,
		"/api/public/v1/sites/not-a-uuid":   http.StatusNotFound,
		"/api/public/v1/sites?unexpected=1": http.StatusBadRequest,
	} {
		status, headers, _, raw := publicGet(t, env, valid, path)
		if status != want {
			t.Errorf("%s = %d, want %d (%s)", path, status, want, raw)
		}
		if headers.Get("WWW-Authenticate") != "" {
			t.Errorf("%s: a %d must not carry WWW-Authenticate", path, status)
		}
	}
}

func TestPublicRoutesRefuseOutsideTheCredentialScope(t *testing.T) {
	env := newTestEnv(t)
	sitesOnly := publicCredential(t, env, "one", "p3-scope@example.com", `{"name":"sites only","scopes":["sites:read"]}`)

	status, headers, body, _ := publicGet(t, env, sitesOnly, "/api/public/v1/members")
	if code := publicError(t, status, headers, body, http.StatusForbidden); code != models.CodeInsufficientScope {
		t.Errorf("members without members:read: %s", code)
	}
	// Scope is checked before the lookup: a missing member is still 403.
	status, headers, body, _ = publicGet(t, env, sitesOnly, "/api/public/v1/members/NOPE")
	if code := publicError(t, status, headers, body, http.StatusForbidden); code != models.CodeInsufficientScope {
		t.Errorf("member lookup without scope: %s", code)
	}
	if status, _, _, _ := publicGet(t, env, sitesOnly, "/api/public/v1/sites"); status != http.StatusOK {
		t.Errorf("sites with sites:read = %d", status)
	}
}

// ---------------------------------------------------------------------------
// Members
// ---------------------------------------------------------------------------

func TestPublicMembersListPagesNewestFirstWithTheSpecifiedEnvelope(t *testing.T) {
	env := newTestEnv(t)
	for i := 1; i <= 5; i++ {
		env.createMember(env.siteAKey, fmt.Sprintf("PUB-%03d", i), "Public Person")
	}
	env.createMember(env.siteCKey, "OTHER-001", "Elsewhere")
	secret := publicCredential(t, env, "one", "p3-list@example.com", `{"name":"reader","scopes":["members:read"]}`)

	var seen []string
	path := "/api/public/v1/members?limit=2"
	for pages := 0; ; pages++ {
		status, _, body, raw := publicGet(t, env, secret, path)
		if status != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, status, raw)
		}
		// Exactly the three envelope keys.
		if len(body) != 3 || body["data"] == nil || body["has_more"] == nil {
			t.Fatalf("envelope keys: %v", body)
		}
		if _, present := body["next_cursor"]; !present {
			t.Fatalf("next_cursor key absent: %v", body)
		}
		for _, item := range body["data"].([]any) {
			m := item.(map[string]any)
			seen = append(seen, m["member_id"].(string))
			// The whole object, and nothing else.
			for _, k := range []string{"id", "member_id", "full_name", "membership_type", "active", "created_at", "updated_at"} {
				if _, ok := m[k]; !ok {
					t.Errorf("member lacks %s: %v", k, m)
				}
			}
			if len(m) != 7 {
				t.Errorf("member has %d fields, want 7: %v", len(m), m)
			}
			if strings.Contains(raw, "fingerprint") || strings.Contains(raw, "public_id") {
				t.Errorf("forbidden field in %s", raw)
			}
			if _, isNumber := m["id"].(float64); isNumber {
				t.Errorf("id is numeric (internal): %v", m["id"])
			}
		}
		if body["has_more"] == false {
			if body["next_cursor"] != nil {
				t.Errorf("has_more=false but next_cursor=%v", body["next_cursor"])
			}
			break
		}
		cursor, _ := body["next_cursor"].(string)
		if cursor == "" {
			t.Fatalf("has_more=true without a cursor: %v", body)
		}
		path = "/api/public/v1/members?limit=2&cursor=" + cursor
		if pages > 5 {
			t.Fatal("paging did not terminate")
		}
	}
	if strings.Join(seen, ",") != "PUB-005,PUB-004,PUB-003,PUB-002,PUB-001" {
		t.Errorf("order/coverage: %v", seen)
	}

	// Empty tenant answers [] not null.
	empty := publicCredential(t, env, "two", "p3-empty@example.com", `{"name":"reader","scopes":["members:read"]}`)
	mustExec(t, `UPDATE people SET deleted_at = now() WHERE external_id = 'OTHER-001'`)
	_, _, _, raw := publicGet(t, env, empty, "/api/public/v1/members")
	if !strings.Contains(raw, `"data":[]`) {
		t.Errorf("empty list = %s", raw)
	}
}

func TestPublicMembersListRefusesBadLimitsCursorsAndParameters(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "PUB-001", "Public Person")
	secret := publicCredential(t, env, "one", "p3-bad@example.com", `{"name":"reader","scopes":["members:read"]}`)
	other := publicCredential(t, env, "two", "p3-other@example.com", `{"name":"reader","scopes":["members:read"]}`)

	cases := []struct {
		query  string
		status int
		code   string
		param  string
	}{
		{"limit=0", 400, models.CodeInvalidField, "limit"},
		{"limit=201", 400, models.CodeInvalidField, "limit"},
		{"limit=-1", 400, models.CodeInvalidField, "limit"},
		{"limit=abc", 400, models.CodeInvalidField, "limit"},
		{"limit=", 400, models.CodeInvalidField, "limit"},
		{"offset=10", 400, models.CodeUnknownParameter, "offset"},
		{"company_id=1", 400, models.CodeTenantIdentity, "company_id"},
		{"company=two&limit=abc", 400, models.CodeTenantIdentity, "company"},
		{"cursor=garbage.garbage", 400, models.CodeCursorInvalid, "cursor"},
	}
	for _, c := range cases {
		status, headers, body, _ := publicGet(t, env, secret, "/api/public/v1/members?"+c.query)
		code := publicError(t, status, headers, body, c.status)
		detail := body["error"].(map[string]any)
		if code != c.code || detail["param"] != c.param {
			t.Errorf("?%s -> %s param=%v, want %s/%s", c.query, code, detail["param"], c.code, c.param)
		}
	}

	// limit=200 is the ceiling and is accepted; an OMITTED limit is the
	// default, which is the only way to get it.
	if status, _, _, raw := publicGet(t, env, secret, "/api/public/v1/members?limit=200"); status != 200 {
		t.Errorf("limit=200 = %d %s", status, raw)
	}
	if status, _, _, raw := publicGet(t, env, secret, "/api/public/v1/members"); status != 200 {
		t.Errorf("no limit = %d %s", status, raw)
	}

	// A cursor minted for company one is refused for company two.
	for i := 2; i <= 3; i++ {
		env.createMember(env.siteAKey, fmt.Sprintf("PUB-%03d", i), "Public Person")
	}
	_, _, first, _ := publicGet(t, env, secret, "/api/public/v1/members?limit=1")
	cursor := first["next_cursor"].(string)
	status, headers, body, _ := publicGet(t, env, other, "/api/public/v1/members?cursor="+cursor)
	if code := publicError(t, status, headers, body, 400); code != models.CodeCursorInvalid {
		t.Errorf("another tenant's cursor: %s", code)
	}
}

func TestPublicMemberGetIsTenantScoped(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "ONE-001", "One Person")
	env.createMember(env.siteCKey, "TWO-001", "Two Person")
	two := publicCredential(t, env, "two", "p3-get@example.com", `{"name":"reader","scopes":["members:read"]}`)

	status, _, body, raw := publicGet(t, env, two, "/api/public/v1/members/TWO-001")
	if status != 200 || body["member_id"] != "TWO-001" || body["full_name"] != "Two Person" {
		t.Fatalf("own member: %d %s", status, raw)
	}
	if _, ok := body["id"].(string); !ok || len(body["id"].(string)) != 36 {
		t.Errorf("id is not the public UUID: %v", body["id"])
	}

	// Foreign and missing are identical answers.
	sF, hF, bF, rawF := publicGet(t, env, two, "/api/public/v1/members/ONE-001")
	sM, hM, bM, rawM := publicGet(t, env, two, "/api/public/v1/members/NOPE-999")
	if publicError(t, sF, hF, bF, 404) != models.CodeResourceNotFound || publicError(t, sM, hM, bM, 404) != models.CodeResourceNotFound {
		t.Error("foreign/missing member must both be resource_not_found")
	}
	strip := func(s string) string { // request ids differ; everything else must not
		var m map[string]any
		_ = json.Unmarshal([]byte(s), &m)
		delete(m["error"].(map[string]any), "request_id")
		out, _ := json.Marshal(m)
		return string(out)
	}
	if strip(rawF) != strip(rawM) {
		t.Errorf("foreign and missing bodies differ:\n%s\n%s", rawF, rawM)
	}

	// A soft-deleted member is gone.
	mustExec(t, `UPDATE people SET deleted_at = now() WHERE external_id = 'TWO-001'`)
	if status, _, _, _ := publicGet(t, env, two, "/api/public/v1/members/TWO-001"); status != 404 {
		t.Errorf("deleted member = %d", status)
	}
	// Query parameters are refused on the single-resource route too.
	status, headers, body, _ := publicGet(t, env, two, "/api/public/v1/members/TWO-001?company_id=9")
	if publicError(t, status, headers, body, 400) != models.CodeTenantIdentity {
		t.Error("company_id on a get must be tenant_identity_not_permitted")
	}
}

// ---------------------------------------------------------------------------
// Sites
// ---------------------------------------------------------------------------

func TestPublicSitesHonourTheCredentialRestriction(t *testing.T) {
	env := newTestEnv(t)
	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	siteC := operatorSitePublicID(t, "Site C")
	env.registerDevice(env.siteAKey, "PUB-TERM-1")

	restricted := publicCredential(t, env, "one", "p3-restricted@example.com",
		`{"name":"site a","scopes":["sites:read"],"site_ids":["`+siteA+`"]}`)
	unrestricted := publicCredential(t, env, "one", "p3-all@example.com", `{"name":"all","scopes":["sites:read"]}`)

	// Unrestricted: both of company one's sites, by name, under data alone.
	status, _, body, raw := publicGet(t, env, unrestricted, "/api/public/v1/sites")
	if status != 200 || len(body) != 1 {
		t.Fatalf("sites list = %d %s", status, raw)
	}
	data := body["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["name"] != "Site A" || data[1].(map[string]any)["name"] != "Site B" {
		t.Errorf("unrestricted sites = %s", raw)
	}

	// Order is by name (section 18). The tiebreak is the public id in the SQL
	// (database.SitesInTenant); it cannot be observed here because site names
	// are unique per company (sites_company_id_site_name_key), so a third site
	// sorting between the two is what proves the ORDER BY is on the name.
	companyOne := operatorCompanyID(t, "one")
	mustExec(t, `INSERT INTO sites (company_id, site_name, api_key_hash, api_key_prefix, active)
	             VALUES ($1, 'Site AB', encode(sha256('site-ab-key'::bytea), 'hex'), 'ab', TRUE)`, companyOne)
	_, _, body, raw = publicGet(t, env, unrestricted, "/api/public/v1/sites")
	data = body["data"].([]any)
	var names []string
	for _, s := range data {
		names = append(names, s.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "Site A,Site AB,Site B" {
		t.Errorf("sites are not ordered by name: %v (%s)", names, raw)
	}
	siteObj := data[0].(map[string]any)
	for _, k := range []string{"id", "name", "address", "timezone", "active", "terminal_count", "created_at"} {
		if _, ok := siteObj[k]; !ok {
			t.Errorf("site lacks %s: %v", k, siteObj)
		}
	}
	if len(siteObj) != 7 {
		t.Errorf("site has %d fields, want 7: %v", len(siteObj), siteObj)
	}
	if siteObj["terminal_count"] != float64(1) || siteObj["address"] != "" {
		t.Errorf("terminal_count/address: %v", siteObj)
	}
	for _, forbidden := range []string{"api_key", "offline_policy", "offline_grace_minutes", "settings"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("forbidden field %s in %s", forbidden, raw)
		}
	}

	// Restricted: only Site A listed; B is 403; C (other company) is 404.
	_, _, body, raw = publicGet(t, env, restricted, "/api/public/v1/sites")
	if data := body["data"].([]any); len(data) != 1 || data[0].(map[string]any)["id"] != siteA {
		t.Errorf("restricted list = %s", raw)
	}
	if status, _, body, _ := publicGet(t, env, restricted, "/api/public/v1/sites/"+siteA); status != 200 || body["id"] != siteA {
		t.Errorf("permitted site = %d %v", status, body)
	}
	status, headers, body, _ := publicGet(t, env, restricted, "/api/public/v1/sites/"+siteB)
	if publicError(t, status, headers, body, 403) != models.CodeSiteNotPermitted {
		t.Error("unassigned same-company site must be site_not_permitted")
	}
	status, headers, body, _ = publicGet(t, env, restricted, "/api/public/v1/sites/"+siteC)
	if publicError(t, status, headers, body, 404) != models.CodeResourceNotFound {
		t.Error("foreign site must be resource_not_found")
	}
	status, headers, body, _ = publicGet(t, env, unrestricted, "/api/public/v1/sites/"+siteC)
	if publicError(t, status, headers, body, 404) != models.CodeResourceNotFound {
		t.Error("foreign site must be 404 even for an unrestricted credential")
	}
	status, headers, body, _ = publicGet(t, env, unrestricted, "/api/public/v1/sites/not-a-uuid")
	if publicError(t, status, headers, body, 404) != models.CodeResourceNotFound {
		t.Error("malformed site id must be 404")
	}
	status, headers, body, _ = publicGet(t, env, unrestricted, "/api/public/v1/sites?q=A")
	if publicError(t, status, headers, body, 400) != models.CodeUnknownParameter {
		t.Error("an unknown query parameter must be refused")
	}
}

// The legacy and console trees are untouched by the public mount: the same
// member is still served by the site-key route in its legacy shape.
func TestLegacyMembersRouteIsUnchangedByThePublicTree(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "LEG-001", "Legacy Person")
	res := env.do(http.MethodGet, "/api/v1/members/LEG-001", nil, siteAuth(env.siteAKey))
	if res.Code != 200 || !strings.Contains(res.Raw, `"public_id"`) || !strings.Contains(res.Raw, `"member_id":"LEG-001"`) {
		t.Errorf("legacy route changed: %d %s", res.Code, res.Raw)
	}
	// And a bearer credential opens nothing on the legacy tree.
	secret := publicCredential(t, env, "one", "p3-legacy@example.com", `{"name":"r","scopes":["members:read"]}`)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/members", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("integration credential on the site-key tree = %d", w.Code)
	}
}
