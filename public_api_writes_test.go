package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// P4: the public write routes, the access-standing read and the event trail,
// end to end through the real router -- API_SPEC.md section 18. Credentials are
// issued through the console exactly as a customer's would be.

// publicCall performs any method on the public tree with a bearer credential
// and an optional JSON body and Idempotency-Key.
func publicCall(t *testing.T, env *testEnv, secret, method, path, body, idempotencyKey string) (int, http.Header, map[string]any, string) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	raw := w.Body.String()
	var parsed map[string]any
	if raw != "" {
		if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("%s %s: body is not a JSON object: %q", method, path, raw)
		}
	}
	return w.Code, w.Header(), parsed, raw
}

func writerCredential(t *testing.T, env *testEnv, slug, email string) string {
	t.Helper()
	return publicCredential(t, env, slug, email, `{"name":"writer","scopes":["members:write","access:read","events:read"]}`)
}

func auditCount(t *testing.T, action, actorEmail string) int {
	t.Helper()
	return queryInt(t, `SELECT count(*) FROM audit_events WHERE action = $1 AND actor_email = $2 AND actor_role = 'INTEGRATION'`,
		action, actorEmail)
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPublicCreateMemberDefaultsAndFansOut(t *testing.T) {
	env := newTestEnv(t)
	deviceKey := env.registerDevice(env.siteAKey, "P4-TERM-1")
	secret := writerCredential(t, env, "one", "p4-create@example.com")

	status, headers, body, raw := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members",
		`{"member_id":"P4-001","full_name":"  Ada Lovelace  "}`, "")
	if status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, raw)
	}
	// Section 18 defaults: membership_type STANDARD, active true; exactly the
	// seven public fields; no internal id or biometric field under any name.
	if body["member_id"] != "P4-001" || body["full_name"] != "Ada Lovelace" ||
		body["membership_type"] != "STANDARD" || body["active"] != true {
		t.Errorf("created member = %s", raw)
	}
	for _, key := range []string{"id", "member_id", "full_name", "membership_type", "active", "created_at", "updated_at"} {
		if _, ok := body[key]; !ok {
			t.Errorf("created member lacks %q: %s", key, raw)
		}
	}
	if len(body) != 7 {
		t.Errorf("created member has %d fields, want 7: %s", len(body), raw)
	}
	if strings.Contains(raw, "fingerprint") || strings.Contains(raw, "public_id") {
		t.Errorf("created member leaks a non-contract field: %s", raw)
	}
	if headers.Get("RateLimit-Limit") == "" {
		t.Error("a write carries no RateLimit headers; writes share the read allowance")
	}

	// The same write path as the console: a CREATE job reached the terminal
	// and the company's default access rule exists.
	if !contains(jobTypes(env.jobs(deviceKey)), models.SyncJobCreate) {
		t.Error("no CREATE job after the public create")
	}
	if n := queryInt(t, `SELECT count(*) FROM permissions pm JOIN people p ON p.id = pm.person_id
	                      WHERE p.external_id = 'P4-001' AND pm.deleted_at IS NULL AND pm.effect = 'ALLOW'`); n != 1 {
		t.Errorf("default access rules after create = %d, want 1", n)
	}
	// Audited as an integration, with the key prefix as the actor.
	if n := auditCount(t, "PERSON_CREATED", secret[:17]); n != 1 {
		t.Errorf("PERSON_CREATED audit rows for the credential = %d, want 1", n)
	}
	if changes := queryString(t, `SELECT changes::text FROM audit_events WHERE action = 'PERSON_CREATED' AND actor_role = 'INTEGRATION'`); !strings.Contains(changes, `"via": "public_api"`) {
		t.Errorf("audit changes = %s, want via public_api", changes)
	}
	// The legacy read sees it, so the two APIs describe one roster.
	if got := queryString(t, `SELECT membership_type FROM people WHERE external_id = 'P4-001'`); got != "STANDARD" {
		t.Errorf("stored membership_type = %q", got)
	}
}

func TestPublicCreateMemberRefusals(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "P4-DUP", "Already Here")
	secret := writerCredential(t, env, "one", "p4-refuse@example.com")
	readOnly := publicCredential(t, env, "one", "p4-ro@example.com", `{"name":"ro","scopes":["members:read"]}`)

	cases := []struct {
		name, secret, body string
		status             int
		code, param        string
	}{
		{"missing member_id", secret, `{"full_name":"X"}`, 400, models.CodeMissingField, "member_id"},
		{"missing full_name", secret, `{"member_id":"P4-NEW"}`, 400, models.CodeMissingField, "full_name"},
		{"unusable id (space)", secret, `{"member_id":"P4 NEW","full_name":"X"}`, 400, models.CodeMemberIDUnusable, "member_id"},
		{"unknown field", secret, `{"member_id":"P4-NEW","full_name":"X","nickname":"x"}`, 400, models.CodeUnknownField, "nickname"},
		{"fingerprint is unknown", secret, `{"member_id":"P4-NEW","full_name":"X","fingerprint_template":"AAAA"}`, 400, models.CodeUnknownField, "fingerprint_template"},
		{"wrong type", secret, `{"member_id":"P4-NEW","full_name":"X","active":"yes"}`, 400, models.CodeInvalidField, "active"},
		{"not an object", secret, `[1,2]`, 400, models.CodeInvalidField, "body"},
		{"malformed json", secret, `{"member_id":`, 400, models.CodeInvalidField, "body"},
		{"empty body", secret, ``, 400, models.CodeInvalidField, "body"},
		{"name too long", secret, `{"member_id":"P4-NEW","full_name":"` + strings.Repeat("n", 201) + `"}`, 400, models.CodeInvalidField, "full_name"},
		{"duplicate id", secret, `{"member_id":"P4-DUP","full_name":"X"}`, 409, models.CodeMemberIDExists, "member_id"},
		{"tenant in body", secret, `{"member_id":"P4-NEW","full_name":"X","company_id":1}`, 400, models.CodeUnknownField, "company_id"},
		{"read-only credential", readOnly, `{"member_id":"P4-NEW","full_name":"X"}`, 403, models.CodeInsufficientScope, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, headers, body, _ := publicCall(t, env, tc.secret, http.MethodPost, "/api/public/v1/members", tc.body, "")
			code := publicError(t, status, headers, body, tc.status)
			if code != tc.code {
				t.Errorf("code = %s, want %s", code, tc.code)
			}
			if tc.param != "" {
				if got := body["error"].(map[string]any)["param"]; got != tc.param {
					t.Errorf("param = %v, want %s", got, tc.param)
				}
			}
		})
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id LIKE 'P4-NEW%'`); n != 0 {
		t.Errorf("a refused create stored %d rows", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events WHERE actor_role = 'INTEGRATION'`); n != 0 {
		t.Errorf("refused writes wrote %d audit rows", n)
	}
}

// ---------------------------------------------------------------------------
// Update (activate / deactivate)
// ---------------------------------------------------------------------------

func TestPublicPatchIsPartialAndDeactivationReachesTheTerminal(t *testing.T) {
	env := newTestEnv(t)
	deviceKey := env.registerDevice(env.siteAKey, "P4-TERM-2")
	env.createMember(env.siteAKey, "P4-002", "Grace Hopper")
	env.jobs(deviceKey) // drain the CREATE
	secret := writerCredential(t, env, "one", "p4-patch@example.com")

	// Deactivate: only active changes; the name and type are kept.
	status, _, body, raw := publicCall(t, env, secret, http.MethodPatch, "/api/public/v1/members/P4-002", `{"active":false}`, "")
	if status != 200 || body["active"] != false || body["full_name"] != "Grace Hopper" {
		t.Fatalf("deactivate = %d %s", status, raw)
	}
	jobs := env.jobs(deviceKey)
	if !contains(jobTypes(jobs), models.SyncJobUpdate) {
		t.Error("no UPDATE job after deactivation")
	}
	// The engine now refuses the person online, with the reason a client can act on.
	var deviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = 'P4-TERM-2'`, &deviceID)
	decision, err := database.Authorize(models.AccessRequest{ExternalID: "P4-002", DeviceID: deviceID, At: time.Now()})
	if err != nil || decision.Granted || decision.Reason != models.ReasonPersonInactive {
		t.Errorf("after deactivation the engine says granted=%v reason=%s err=%v", decision != nil && decision.Granted, decision.Reason, err)
	}

	// Reactivate, and change the name in the same call.
	status, _, body, raw = publicCall(t, env, secret, http.MethodPatch, "/api/public/v1/members/P4-002",
		`{"active":true,"full_name":"Grace B. Hopper"}`, "")
	if status != 200 || body["active"] != true || body["full_name"] != "Grace B. Hopper" {
		t.Fatalf("reactivate = %d %s", status, raw)
	}
	if n := auditCount(t, "PERSON_UPDATED", secret[:17]); n != 2 {
		t.Errorf("PERSON_UPDATED audit rows = %d, want 2", n)
	}
}

func TestPublicPatchRefusals(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "P4-003", "One Person")
	env.createMember(env.siteCKey, "P4-OTHER", "Other Company")
	secret := writerCredential(t, env, "one", "p4-patchref@example.com")

	cases := []struct {
		name, path, body string
		status           int
		code, param      string
	}{
		{"member_id cannot change", "/api/public/v1/members/P4-003", `{"member_id":"P4-999"}`, 400, models.CodeInvalidField, "member_id"},
		{"nothing to change", "/api/public/v1/members/P4-003", `{}`, 400, models.CodeInvalidField, "body"},
		{"empty name", "/api/public/v1/members/P4-003", `{"full_name":"  "}`, 400, models.CodeInvalidField, "full_name"},
		{"unknown field", "/api/public/v1/members/P4-003", `{"active":true,"fingerprint_template":"x"}`, 400, models.CodeUnknownField, "fingerprint_template"},
		{"unknown member", "/api/public/v1/members/P4-NOPE", `{"active":false}`, 404, models.CodeResourceNotFound, ""},
		{"another company's member", "/api/public/v1/members/P4-OTHER", `{"active":false}`, 404, models.CodeResourceNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, headers, body, _ := publicCall(t, env, secret, http.MethodPatch, tc.path, tc.body, "")
			if code := publicError(t, status, headers, body, tc.status); code != tc.code {
				t.Errorf("code = %s, want %s", code, tc.code)
			}
			if tc.param != "" {
				if got := body["error"].(map[string]any)["param"]; got != tc.param {
					t.Errorf("param = %v, want %s", got, tc.param)
				}
			}
		})
	}
	if got := queryString(t, `SELECT full_name FROM people WHERE external_id = 'P4-OTHER'`); got != "Other Company" {
		t.Errorf("the foreign member was changed: %q", got)
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'P4-OTHER' AND active`); n != 1 {
		t.Error("the foreign member was deactivated")
	}
}

// ---------------------------------------------------------------------------
// Delete (revoke)
// ---------------------------------------------------------------------------

func TestPublicDeleteRevokesIdempotentlyAndSilently(t *testing.T) {
	env := newTestEnv(t)
	deviceKey := env.registerDevice(env.siteAKey, "P4-TERM-3")
	env.createMember(env.siteAKey, "P4-004", "To Revoke")
	env.createMember(env.siteCKey, "P4-FOREIGN", "Foreign")
	env.jobs(deviceKey)
	secret := writerCredential(t, env, "one", "p4-delete@example.com")

	// First delete removes; 204 with no body.
	status, _, _, raw := publicCall(t, env, secret, http.MethodDelete, "/api/public/v1/members/P4-004", "", "")
	if status != http.StatusNoContent || raw != "" {
		t.Fatalf("delete = %d %q", status, raw)
	}
	if !contains(jobTypes(env.jobs(deviceKey)), models.SyncJobDelete) {
		t.Error("no DELETE job after the public delete")
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'P4-004' AND deleted_at IS NOT NULL`); n != 1 {
		t.Error("the member was not soft-deleted")
	}
	// It is gone from the public read and from the legacy read.
	if status, _, _, _ := publicGet(t, env, secret, "/api/public/v1/members/P4-004"); status != 404 {
		t.Errorf("deleted member still readable: %d", status)
	}
	// Second delete: same answer, no second job, no second audit row.
	before := len(env.jobs(deviceKey))
	status, _, _, _ = publicCall(t, env, secret, http.MethodDelete, "/api/public/v1/members/P4-004", "", "")
	if status != http.StatusNoContent {
		t.Errorf("repeat delete = %d, want 204", status)
	}
	if after := len(env.jobs(deviceKey)); after != before {
		t.Errorf("repeat delete queued %d jobs", after-before)
	}
	// Never-existed and foreign: the same 204, and nothing happens to the foreign row.
	for _, path := range []string{"/api/public/v1/members/P4-NEVER", "/api/public/v1/members/P4-FOREIGN"} {
		if status, _, _, _ := publicCall(t, env, secret, http.MethodDelete, path, "", ""); status != http.StatusNoContent {
			t.Errorf("DELETE %s = %d, want 204", path, status)
		}
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'P4-FOREIGN' AND deleted_at IS NULL`); n != 1 {
		t.Error("a foreign member was deleted through company one's credential")
	}
	if n := auditCount(t, "PERSON_DELETED", secret[:17]); n != 1 {
		t.Errorf("PERSON_DELETED audit rows = %d, want exactly 1 (the removal, not the no-ops)", n)
	}
	// Without members:write it is 403 and nothing moves.
	readOnly := publicCredential(t, env, "one", "p4-delro@example.com", `{"name":"ro","scopes":["members:read"]}`)
	env.createMember(env.siteAKey, "P4-005", "Stays")
	if status, _, _, _ := publicCall(t, env, readOnly, http.MethodDelete, "/api/public/v1/members/P4-005", "", ""); status != 403 {
		t.Errorf("delete without members:write = %d, want 403", status)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestPublicWritesReplayUnderAnIdempotencyKey(t *testing.T) {
	env := newTestEnv(t)
	secret := writerCredential(t, env, "one", "p4-idem@example.com")
	other := writerCredential(t, env, "two", "p4-idem2@example.com")
	body := `{"member_id":"P4-IDEM","full_name":"Once"}`

	status, _, first, raw := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", body, "k-1")
	if status != 201 {
		t.Fatalf("first = %d %s", status, raw)
	}
	// The identical retry is the ORIGINAL response, marked as a replay, and
	// the row is not created twice.
	status, headers, second, _ := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", body, "k-1")
	if status != 201 || headers.Get("Idempotent-Replay") != "true" || second["id"] != first["id"] {
		t.Errorf("replay = %d replay-header=%q same id=%v", status, headers.Get("Idempotent-Replay"), second["id"] == first["id"])
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'P4-IDEM'`); n != 1 {
		t.Errorf("rows after replay = %d", n)
	}
	// The same key with a different body is a reuse.
	status, headers, body3, _ := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members",
		`{"member_id":"P4-IDEM-2","full_name":"Twice"}`, "k-1")
	if code := publicError(t, status, headers, body3, 409); code != models.CodeIdempotencyKeyReuse {
		t.Errorf("reuse code = %s", code)
	}
	// Keys are scoped to the credential: another company's credential with
	// the same key and body is a fresh request in ITS tenant.
	status, _, _, raw = publicCall(t, env, other, http.MethodPost, "/api/public/v1/members", body, "k-1")
	if status != 201 {
		t.Errorf("same key in another tenant = %d %s", status, raw)
	}
	if n := queryInt(t, `SELECT count(DISTINCT company_id) FROM people WHERE external_id = 'P4-IDEM'`); n != 2 {
		t.Errorf("P4-IDEM exists in %d companies, want 2", n)
	}
	// A 4xx IS a decision and is stored (P1: only 5xx releases the claim): the
	// identical retry replays the 400, and a corrected body under the same key
	// is a reuse. A client fixes its request under a NEW key.
	status, _, _, _ = publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"BAD ID","full_name":"x"}`, "k-2")
	if status != 400 {
		t.Fatalf("refused write = %d", status)
	}
	status, headers, _, _ = publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"BAD ID","full_name":"x"}`, "k-2")
	if status != 400 || headers.Get("Idempotent-Replay") != "true" {
		t.Errorf("identical retry of a refused write = %d replay=%q, want a replayed 400", status, headers.Get("Idempotent-Replay"))
	}
	if status, _, _, _ = publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"P4-IDEM-3","full_name":"x"}`, "k-2"); status != 409 {
		t.Errorf("corrected body under the refused key = %d, want 409 reuse", status)
	}
	if status, _, _, raw = publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"P4-IDEM-3","full_name":"x"}`, "k-3"); status != 201 {
		t.Errorf("corrected body under a new key = %d %s", status, raw)
	}
	// Without a key, two identical creates are one 201 and one 409.
	publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"P4-NOKEY","full_name":"x"}`, "")
	if status, _, _, _ := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", `{"member_id":"P4-NOKEY","full_name":"x"}`, ""); status != 409 {
		t.Errorf("keyless repeat = %d, want 409", status)
	}
	// An over-long key is refused before anything happens.
	status, headers, body4, _ := publicCall(t, env, secret, http.MethodPost, "/api/public/v1/members", body, strings.Repeat("k", 256))
	if code := publicError(t, status, headers, body4, 400); code != models.CodeIdempotencyKeyBad {
		t.Errorf("long key code = %s", code)
	}
}

// ---------------------------------------------------------------------------
// Access standing
// ---------------------------------------------------------------------------

func TestPublicMemberAccessListsRulesWithStanding(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "P4-TERM-4")
	env.createMember(env.siteAKey, "P4-006", "Ruled Person")
	env.createMember(env.siteCKey, "P4-FOREIGN", "Foreign")
	secret := writerCredential(t, env, "one", "p4-access@example.com")
	siteA := operatorSitePublicID(t, "Site A")
	companyID := operatorCompanyID(t, "one")

	// A site-scoped DENY that expired yesterday, on top of the default
	// company-wide ALLOW the create wrote.
	var personID, siteID int64
	mustScan(t, `SELECT id FROM people WHERE external_id = 'P4-006'`, &personID)
	if err := database.DB.QueryRow(`SELECT id FROM sites WHERE public_id::text = $1`, siteA).Scan(&siteID); err != nil {
		t.Fatalf("site id: %v", err)
	}
	if _, err := database.DB.Exec(`INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active, starts_at, ends_at)
	                               VALUES ($1, $2, 'SITE', $3, 'DENY', TRUE, CURRENT_TIMESTAMP - interval '2 days', CURRENT_TIMESTAMP - interval '1 day')`,
		companyID, personID, siteID); err != nil {
		t.Fatalf("seeding rule: %v", err)
	}

	status, _, body, raw := publicGet(t, env, secret, "/api/public/v1/members/P4-006/access")
	if status != 200 {
		t.Fatalf("access = %d %s", status, raw)
	}
	if body["member_id"] != "P4-006" || body["active"] != true {
		t.Errorf("access header = %s", raw)
	}
	rules := listOf(t, body, "rules")
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2: %s", len(rules), raw)
	}
	byEffect := map[string]map[string]any{}
	for _, r := range rules {
		rule := r.(map[string]any)
		byEffect[rule["effect"].(string)] = rule
	}
	if allow := byEffect["ALLOW"]; allow["scope"] != "COMPANY" || allow["standing"] != "IN_FORCE" || allow["site_id"] != nil {
		t.Errorf("allow rule = %v", allow)
	}
	if deny := byEffect["DENY"]; deny["scope"] != "SITE" || deny["site_id"] != siteA || deny["standing"] != "EXPIRED" {
		t.Errorf("deny rule = %v", deny)
	}
	// Identifiers only: no names, no credentials, no internal ids.
	for _, leak := range []string{"site_name", "person_name", "credential", "fingerprint", "\"person_id\""} {
		if strings.Contains(raw, leak) {
			t.Errorf("access response leaks %q: %s", leak, raw)
		}
	}
	// Unknown and foreign members are the same 404.
	for _, path := range []string{"/api/public/v1/members/P4-NOPE/access", "/api/public/v1/members/P4-FOREIGN/access"} {
		status, headers, body, _ := publicGet(t, env, secret, path)
		if code := publicError(t, status, headers, body, 404); code != models.CodeResourceNotFound {
			t.Errorf("%s: %s", path, code)
		}
	}
	// Scope: members:read alone does not grant it.
	readOnly := publicCredential(t, env, "one", "p4-accro@example.com", `{"name":"ro","scopes":["members:read"]}`)
	status, headers, body, _ := publicGet(t, env, readOnly, "/api/public/v1/members/P4-006/access")
	if code := publicError(t, status, headers, body, 403); code != models.CodeInsufficientScope {
		t.Errorf("without access:read: %s", code)
	}
	// And a query parameter is refused.
	status, headers, body, _ = publicGet(t, env, secret, "/api/public/v1/members/P4-006/access?at=now")
	if code := publicError(t, status, headers, body, 400); code != models.CodeUnknownParameter {
		t.Errorf("unknown parameter: %s", code)
	}
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

type eventSeed struct {
	companyID, siteID, deviceID, personID int64
}

func seedPublicEvents(t *testing.T, env *testEnv) (one eventSeed, two eventSeed) {
	t.Helper()
	env.registerDevice(env.siteAKey, "P4-EV-A")
	env.registerDevice(env.siteBKey, "P4-EV-B")
	env.registerDevice(env.siteCKey, "P4-EV-C")
	env.createMember(env.siteAKey, "P4-EV-001", "Event Person")
	env.createMember(env.siteCKey, "P4-EV-OTHER", "Other Person")
	one.companyID = operatorCompanyID(t, "one")
	two.companyID = operatorCompanyID(t, "two")
	mustScan(t, `SELECT d.id, d.site_id FROM devices d WHERE d.serial_number = 'P4-EV-A'`, &one.deviceID, &one.siteID)
	mustScan(t, `SELECT d.id, d.site_id FROM devices d WHERE d.serial_number = 'P4-EV-C'`, &two.deviceID, &two.siteID)
	mustScan(t, `SELECT id FROM people WHERE external_id = 'P4-EV-001'`, &one.personID)
	mustScan(t, `SELECT id FROM people WHERE external_id = 'P4-EV-OTHER'`, &two.personID)
	return one, two
}

func TestPublicEventsListNewestFirstWithFiltersAndCursor(t *testing.T) {
	env := newTestEnv(t)
	one, two := seedPublicEvents(t, env)
	secret := writerCredential(t, env, "one", "p4-events@example.com")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// Five in company one at site A (alternating decisions), one at site B
	// with no person, and one in company two.
	var siteBID, siteBDevice int64
	mustScan(t, `SELECT d.id, d.site_id FROM devices d WHERE d.serial_number = 'P4-EV-B'`, &siteBDevice, &siteBID)
	for i := 0; i < 5; i++ {
		decision, reason, typ := models.DecisionGranted, models.ReasonAllowed, models.EventAccessGranted
		if i%2 == 1 {
			decision, reason, typ = models.DecisionDenied, models.ReasonNoPermission, models.EventAccessDenied
		}
		seedEvent(t, one.companyID, one.siteID, one.deviceID, one.personID, typ, decision, reason, base.Add(time.Duration(i)*time.Minute))
	}
	seedEvent(t, one.companyID, siteBID, siteBDevice, 0, models.EventAccessDenied, models.DecisionDenied, models.ReasonPersonUnknown, base.Add(10*time.Minute))
	seedEvent(t, two.companyID, two.siteID, two.deviceID, two.personID, models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, base.Add(20*time.Minute))

	// Whole trail for company one: six events, newest first, nothing from company two.
	status, _, body, raw := publicGet(t, env, secret, "/api/public/v1/events")
	if status != 200 {
		t.Fatalf("events = %d %s", status, raw)
	}
	data := listOf(t, body, "data")
	if len(data) != 6 || body["has_more"] != false {
		t.Fatalf("company one events = %d has_more=%v: %s", len(data), body["has_more"], raw)
	}
	first := data[0].(map[string]any)
	if first["site_id"] != operatorSitePublicID(t, "Site B") || first["terminal_serial"] != "P4-EV-B" || first["decision"] != "DENIED" ||
		first["reason"] != "PERSON_UNKNOWN" || first["member_id"] != nil || first["subject"] != "P-AUTH" {
		t.Errorf("newest event = %v", first)
	}
	last := data[5].(map[string]any)
	if last["member_id"] != "P4-EV-001" || last["decision"] != "GRANTED" || last["occurred_at_trusted"] != true {
		t.Errorf("oldest event = %v", last)
	}
	for _, leak := range []string{"credential", "payload", "person_name", "site_name", "device_name", "\"person_id\""} {
		if strings.Contains(raw, leak) {
			t.Errorf("events response carries %q: %s", leak, raw)
		}
	}
	if strings.Contains(raw, "P4-EV-OTHER") || strings.Contains(raw, "P4-EV-C") {
		t.Errorf("company two's event is visible to company one: %s", raw)
	}

	// Filters.
	for _, tc := range []struct {
		query, want string
		n           int
	}{
		{"decision=denied", "DENIED", 3},
		{"member_id=P4-EV-001", "P4-EV-001", 5},
		{"site_id=" + operatorSitePublicID(t, "Site B"), "P4-EV-B", 1},
		{"from=2026-09-01T12:02:00Z&to=2026-09-01T12:04:00Z", "", 2},
		{"member_id=P4-EV-OTHER", "", 0},                        // foreign member: empty, not 404
		{"site_id=" + operatorSitePublicID(t, "Site C"), "", 0}, // foreign site: empty, not 403
		{"site_id=not-a-uuid", "", 0},                           // unparsable: empty
	} {
		status, _, body, raw := publicGet(t, env, secret, "/api/public/v1/events?"+tc.query)
		if status != 200 {
			t.Errorf("%s = %d %s", tc.query, status, raw)
			continue
		}
		if got := len(listOf(t, body, "data")); got != tc.n {
			t.Errorf("%s: %d events, want %d: %s", tc.query, got, tc.n, raw)
		}
		if tc.want != "" && !strings.Contains(raw, tc.want) {
			t.Errorf("%s: %s absent from %s", tc.query, tc.want, raw)
		}
	}

	// Cursor paging: pages of 4 then 2, no repeats, and a cursor cannot be
	// carried to a different filter set.
	status, _, page1, raw := publicGet(t, env, secret, "/api/public/v1/events?limit=4")
	if status != 200 || page1["has_more"] != true || page1["next_cursor"] == nil {
		t.Fatalf("page 1 = %d %s", status, raw)
	}
	cursor := page1["next_cursor"].(string)
	status, _, page2, raw := publicGet(t, env, secret, "/api/public/v1/events?limit=4&cursor="+cursor)
	if status != 200 || page2["has_more"] != false || len(listOf(t, page2, "data")) != 2 {
		t.Fatalf("page 2 = %d %s", status, raw)
	}
	seen := map[string]bool{}
	for _, page := range []map[string]any{page1, page2} {
		for _, e := range listOf(t, page, "data") {
			id := e.(map[string]any)["id"].(string)
			if seen[id] {
				t.Errorf("event %s appears on two pages", id)
			}
			seen[id] = true
		}
	}
	status, headers, body, _ := publicGet(t, env, secret, "/api/public/v1/events?limit=4&decision=denied&cursor="+cursor)
	if code := publicError(t, status, headers, body, 400); code != models.CodeCursorInvalid {
		t.Errorf("cursor across filters: %s", code)
	}
	// Refusals.
	for _, tc := range []struct{ query, code string }{
		{"decision=maybe", models.CodeInvalidField},
		{"from=yesterday", models.CodeInvalidTimestamp},
		{"from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z", models.CodeInvalidField},
		{"serial=x", models.CodeUnknownParameter},
		{"company_id=1", models.CodeTenantIdentity},
		{"limit=0", models.CodeInvalidField},
	} {
		status, headers, body, _ := publicGet(t, env, secret, "/api/public/v1/events?"+tc.query)
		if code := publicError(t, status, headers, body, 400); code != tc.code {
			t.Errorf("%s: code %s, want %s", tc.query, code, tc.code)
		}
	}
	// Scope.
	readOnly := publicCredential(t, env, "one", "p4-evro@example.com", `{"name":"ro","scopes":["members:read"]}`)
	status, headers, body, _ = publicGet(t, env, readOnly, "/api/public/v1/events")
	if code := publicError(t, status, headers, body, 403); code != models.CodeInsufficientScope {
		t.Errorf("without events:read: %s", code)
	}
}

func TestPublicEventsHonourTheCredentialSiteRestriction(t *testing.T) {
	env := newTestEnv(t)
	one, _ := seedPublicEvents(t, env)
	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	var siteBID, siteBDevice int64
	mustScan(t, `SELECT d.id, d.site_id FROM devices d WHERE d.serial_number = 'P4-EV-B'`, &siteBDevice, &siteBID)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seedEvent(t, one.companyID, one.siteID, one.deviceID, one.personID, models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, base)
	seedEvent(t, one.companyID, siteBID, siteBDevice, one.personID, models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, base.Add(time.Minute))
	// A company-level event with no site.
	seedEvent(t, one.companyID, 0, 0, 0, models.EventAccessDenied, models.DecisionDenied, models.ReasonPersonUnknown, base.Add(2*time.Minute))

	restricted := publicCredential(t, env, "one", "p4-evrestricted@example.com",
		`{"name":"site a","scopes":["events:read"],"site_ids":["`+siteA+`"]}`)
	unrestricted := publicCredential(t, env, "one", "p4-evall@example.com", `{"name":"all","scopes":["events:read"]}`)

	status, _, body, raw := publicGet(t, env, unrestricted, "/api/public/v1/events")
	if status != 200 || len(listOf(t, body, "data")) != 3 {
		t.Fatalf("unrestricted = %d %s", status, raw)
	}
	status, _, body, raw = publicGet(t, env, restricted, "/api/public/v1/events")
	if status != 200 {
		t.Fatalf("restricted = %d %s", status, raw)
	}
	data := listOf(t, body, "data")
	if len(data) != 1 || data[0].(map[string]any)["site_id"] != siteA {
		t.Errorf("restricted credential sees %d events (%s), want only site A's", len(data), raw)
	}
	// A filter for a site outside the restriction is an empty page, not a
	// refusal -- the same answer as a site that does not exist.
	status, _, body, raw = publicGet(t, env, restricted, "/api/public/v1/events?site_id="+siteB)
	if status != 200 || len(listOf(t, body, "data")) != 0 {
		t.Errorf("restricted filter outside the restriction = %d %s", status, raw)
	}
}

// The unauthenticated and wrong-credential answers on the new routes are the
// section 18 401s, with the challenge, exactly as on the read routes.
func TestPublicWriteRoutesRefuseWithoutACredential(t *testing.T) {
	env := newTestEnv(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/public/v1/members"},
		{http.MethodPatch, "/api/public/v1/members/X"},
		{http.MethodDelete, "/api/public/v1/members/X"},
		{http.MethodGet, "/api/public/v1/members/X/access"},
		{http.MethodGet, "/api/public/v1/events"},
	} {
		status, headers, body, _ := publicCall(t, env, "", tc.method, tc.path, "", "")
		if code := publicError(t, status, headers, body, 401); code != models.CodeCredentialMissing {
			t.Errorf("%s %s: %s", tc.method, tc.path, code)
		}
		if headers.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s: no WWW-Authenticate", tc.method, tc.path)
		}
	}
}
