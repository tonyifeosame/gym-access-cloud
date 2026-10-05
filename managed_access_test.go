package main

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Integration-managed access (API_SPEC.md section 18, migrations/039): an
// integration with access:write gives the members IT CREATED the standard
// access its credential covers -- never anyone else's, never other rules.
//
// Every test sets the company's default_person_access to NONE, which is what a
// real company created through signup or the platform API has, so an access
// rule here can only have come from the integration.

const managedScopes = `"members:write","access:read","access:write"`

type managedFixture struct {
	env          *testEnv
	companyID    int64
	token, csrf  string
	secret       string // members:write + access:write, every site
	credentialID string
	keyA, keyB   string // terminals at Site A and Site B
	siteA, siteB string
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	f := &managedFixture{env: env, companyID: operatorCompanyID(t, "one"),
		siteA: operatorSitePublicID(t, "Site A"), siteB: operatorSitePublicID(t, "Site B")}
	mustExec(t, `UPDATE companies SET default_person_access = 'NONE'`)
	f.keyA = env.registerDevice(env.siteAKey, "MA-A1")
	f.keyB = env.registerDevice(env.siteBKey, "MA-B1")
	terminalApplies(t, env, f.keyA, "") // bootstrap work
	terminalApplies(t, env, f.keyB, "")
	_, f.token, f.csrf = consoleOperatorSession(t, env.router, f.companyID, "ma-admin@example.com", models.RoleAdmin)
	issued := issueCredential(t, env, f.token, f.csrf, `{"name":"DataVase","scopes":[`+managedScopes+`]}`)
	f.secret, f.credentialID = secretOf(t, issued), issued["id"].(string)
	return f
}

func (f *managedFixture) credential(t *testing.T, body string) string {
	t.Helper()
	return secretOf(t, issueCredential(t, f.env, f.token, f.csrf, body))
}

func (f *managedFixture) create(t *testing.T, secret, memberID string) {
	t.Helper()
	if status, _, _, raw := publicCall(t, f.env, secret, http.MethodPost, "/api/public/v1/members",
		`{"member_id":"`+memberID+`","full_name":"`+memberID+` Person"}`, ""); status != http.StatusCreated {
		t.Fatalf("creating %s = %d %s", memberID, status, raw)
	}
}

func (f *managedFixture) access(t *testing.T, secret, method, memberID, key string) (int, map[string]any, string) {
	t.Helper()
	status, _, body, raw := publicCall(t, f.env, secret, method, "/api/public/v1/members/"+memberID+"/access", "", key)
	return status, body, raw
}

// liveRules: every live rule for a person, as "SCOPE/EFFECT/owner" where owner
// is "integration" or "operator".
func liveRules(t *testing.T, memberID string) []string {
	t.Helper()
	rows, err := database.DB.Query(`
		SELECT pm.scope_type, pm.effect, COALESCE(s.site_name, ''), pm.granted_by_lineage_id IS NOT NULL
		  FROM permissions pm JOIN people p ON p.id = pm.person_id LEFT JOIN sites s ON s.id = pm.site_id
		 WHERE p.external_id = $1 AND pm.deleted_at IS NULL`, memberID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var scope, effect, site string
		var integration bool
		if err := rows.Scan(&scope, &effect, &site, &integration); err != nil {
			t.Fatal(err)
		}
		owner := map[bool]string{true: "integration", false: "operator"}[integration]
		out = append(out, strings.TrimSuffix(scope+"/"+effect+"/"+site, "/")+"/"+owner)
	}
	sort.Strings(out)
	return out
}

// terminalApplies is a terminal polling and acknowledging everything it is
// sent, as firmware does. Returns the job types it received for memberID.
func terminalApplies(t *testing.T, env *testEnv, deviceKey, memberID string) []string {
	t.Helper()
	var out []string
	for _, j := range env.jobs(deviceKey) {
		if j["entity_external_id"] == memberID {
			out = append(out, j["job_type"].(string))
		}
		if ack := env.do(http.MethodPost, jobPath(jobID(t, j)), map[string]any{"status": "COMPLETED"}, deviceAuth(deviceKey)); ack.Code != http.StatusOK {
			t.Fatalf("acknowledging job = %d %s", ack.Code, ack.Raw)
		}
	}
	return out
}

func decideAt(t *testing.T, memberID, serial string) *models.AccessDecision {
	t.Helper()
	var deviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = '`+serial+`'`, &deviceID)
	d, err := database.Authorize(models.AccessRequest{ExternalID: memberID, DeviceID: deviceID, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

func TestAccessWriteIsAnAdminScopeAndIsRequired(t *testing.T) {
	spec := models.Scopes[models.ScopeAccessWrite]
	if spec.MinRole != models.RoleAdmin || !spec.SiteRestrictable || !spec.Write || len(spec.Implies) != 0 {
		t.Errorf("access:write spec = %+v, want ADMIN, site-restrictable, write, implying nothing", spec)
	}
	f := newManagedFixture(t)
	membersOnly := f.credential(t, `{"name":"members only","scopes":["members:write"]}`)
	f.create(t, membersOnly, "MA-NOSCOPE")
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		status, body, _ := f.access(t, membersOnly, method, "MA-NOSCOPE", "")
		if code := publicError(t, status, http.Header{"X-Request-Id": []string{body["error"].(map[string]any)["request_id"].(string)}}, body, 403); code != models.CodeInsufficientScope {
			t.Errorf("%s without access:write = %s", method, code)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		status, _, _, _ := publicCall(t, f.env, "", method, "/api/public/v1/members/X/access", "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s without a credential = %d, want 401", method, status)
		}
	}
	if got := liveRules(t, "MA-NOSCOPE"); len(got) != 0 {
		t.Errorf("a credential without access:write left rules %v", got)
	}
}

// ---------------------------------------------------------------------------
// Creation
// ---------------------------------------------------------------------------

func TestCreatingWithAccessWriteGrantsAndReachesTheTerminalsAtOnce(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-NEW")

	if got := liveRules(t, "MA-NEW"); strings.Join(got, ",") != "COMPANY/ALLOW/integration" {
		t.Errorf("rules after a managed create = %v", got)
	}
	if !contains(terminalApplies(t, f.env, f.keyA, "MA-NEW"), models.SyncJobCreate) || !contains(terminalApplies(t, f.env, f.keyB, "MA-NEW"), models.SyncJobCreate) {
		t.Error("the new member did not reach both terminals in the same request")
	}
	if !decideAt(t, "MA-NEW", "MA-A1").Granted {
		t.Error("an active member with integration access is refused")
	}
	// Provenance: the credential's lineage.
	if n := queryInt(t, `SELECT count(*) FROM people p JOIN api_credentials c ON c.lineage_id = p.managed_by_lineage_id
	                      WHERE p.external_id = 'MA-NEW' AND c.public_id::text = $1`, f.credentialID); n != 1 {
		t.Error("the created member does not record the integration's lineage")
	}
	// The company default is untouched.
	if got := queryString(t, `SELECT default_person_access FROM companies WHERE id = $1`, f.companyID); got != "NONE" {
		t.Errorf("default_person_access became %s", got)
	}
}

func TestCreatingWithoutAccessWriteRecordsProvenanceButGrantsNothing(t *testing.T) {
	f := newManagedFixture(t)
	membersOnly := f.credential(t, `{"name":"members only","scopes":["members:write"]}`)
	f.create(t, membersOnly, "MA-BARE")

	if got := liveRules(t, "MA-BARE"); len(got) != 0 {
		t.Errorf("rules = %v, want none", got)
	}
	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'MA-BARE' AND managed_by_lineage_id IS NOT NULL`); n != 1 {
		t.Error("provenance is recorded whatever the scopes")
	}
	if jobs := terminalApplies(t, f.env, f.keyA, "MA-BARE"); len(jobs) != 0 {
		t.Errorf("a member with no rule reached a terminal: %v", jobs)
	}
}

// ---------------------------------------------------------------------------
// Grant and removal
// ---------------------------------------------------------------------------

func TestGrantAndRemovalAreIdempotentAndMoveTheRoster(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-001")
	terminalApplies(t, f.env, f.keyA, "MA-001")

	status, body, raw := f.access(t, f.secret, http.MethodDelete, "MA-001", "")
	if status != http.StatusOK || len(body["rules"].([]any)) != 0 {
		t.Fatalf("removal = %d %s", status, raw)
	}
	if got := liveRules(t, "MA-001"); len(got) != 0 {
		t.Errorf("rules after removal = %v", got)
	}
	if jobs := terminalApplies(t, f.env, f.keyA, "MA-001"); !contains(jobs, models.SyncJobDelete) {
		t.Errorf("removal did not withdraw the member from the terminal: %v", jobs)
	}
	if decideAt(t, "MA-001", "MA-A1").Granted {
		t.Error("access remained after removal")
	}

	status, body, raw = f.access(t, f.secret, http.MethodPut, "MA-001", "")
	rules := body["rules"].([]any)
	if status != http.StatusOK || len(rules) != 1 || rules[0].(map[string]any)["scope"] != "COMPANY" || rules[0].(map[string]any)["effect"] != "ALLOW" {
		t.Fatalf("grant = %d %s", status, raw)
	}
	if jobs := terminalApplies(t, f.env, f.keyA, "MA-001"); !contains(jobs, models.SyncJobCreate) {
		t.Errorf("the grant did not reach the terminal at once: %v", jobs)
	}

	// Repeating changes nothing: no rule, no job, no audit line.
	audits := auditCount(t, "PERMISSION_CREATED", f.secret[:17])
	jobsBefore := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-001'`)
	if status, _, _ := f.access(t, f.secret, http.MethodPut, "MA-001", ""); status != http.StatusOK {
		t.Fatalf("repeat grant = %d", status)
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-001'`); n != jobsBefore {
		t.Errorf("a repeated grant queued %d more jobs", n-jobsBefore)
	}
	if got := liveRules(t, "MA-001"); len(got) != 1 {
		t.Errorf("rules after a repeated grant = %v", got)
	}
	if n := auditCount(t, "PERMISSION_CREATED", f.secret[:17]); n != audits {
		t.Errorf("a no-op grant was audited")
	}
	if n := auditCount(t, "PERMISSION_DELETED", f.secret[:17]); n != 1 {
		t.Errorf("PERMISSION_DELETED audit rows = %d, want 1", n)
	}
	// Reconciling again finds nothing to do.
	var personID int64
	mustScan(t, `SELECT id FROM people WHERE external_id = 'MA-001'`, &personID)
	if _, added, removed, err := database.ReconcilePersonRoster(f.companyID, personID); err != nil || added+removed != 0 {
		t.Errorf("a second reconcile changed %d/%d (err %v)", added, removed, err)
	}
}

func TestIdempotencyKeyReplaysTheGrant(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-IDEM")
	f.access(t, f.secret, http.MethodDelete, "MA-IDEM", "")

	status, first, _ := f.access(t, f.secret, http.MethodPut, "MA-IDEM", "grant-1")
	_, headers, second, _ := publicCall(t, f.env, f.secret, http.MethodPut, "/api/public/v1/members/MA-IDEM/access", "", "grant-1")
	if status != 200 || headers.Get("Idempotent-Replay") != "true" || fmt.Sprint(first) != fmt.Sprint(second) {
		t.Errorf("replay = %v %v", headers.Get("Idempotent-Replay"), second)
	}
}

func TestRemovalNeverTouchesOperatorRulesAndDenyStillWins(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-MIX")
	grant := func(body string) {
		if code, resp := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/people/MA-MIX/permissions", body, f.token, f.csrf); code != http.StatusCreated {
			t.Fatalf("operator grant %s = %d %v", body, code, resp)
		}
	}
	grant(`{"scope_type":"SITE","site_id":"` + f.siteB + `","effect":"ALLOW"}`)

	f.access(t, f.secret, http.MethodDelete, "MA-MIX", "")
	if got := strings.Join(liveRules(t, "MA-MIX"), ","); got != "SITE/ALLOW/Site B/operator" {
		t.Errorf("after the integration's removal, rules = %s", got)
	}
	if !decideAt(t, "MA-MIX", "MA-B1").Granted || decideAt(t, "MA-MIX", "MA-A1").Granted {
		t.Error("the operator's Site B rule should still admit at B only")
	}

	// An operator DENY outweighs the integration's ALLOW.
	f.access(t, f.secret, http.MethodPut, "MA-MIX", "")
	grant(`{"scope_type":"COMPANY","effect":"DENY"}`)
	if d := decideAt(t, "MA-MIX", "MA-A1"); d.Granted {
		t.Errorf("DENY did not override the integration ALLOW: %+v", d)
	}
	f.access(t, f.secret, http.MethodDelete, "MA-MIX", "")
	if got := strings.Join(liveRules(t, "MA-MIX"), ","); got != "COMPANY/DENY/operator,SITE/ALLOW/Site B/operator" {
		t.Errorf("the integration removed an operator rule: %s", got)
	}
}

func TestAnIdenticalOperatorRuleIsNeitherDuplicatedNorRemoved(t *testing.T) {
	f := newManagedFixture(t)
	membersOnly := f.credential(t, `{"name":"members only","scopes":["members:write"]}`)
	_ = membersOnly
	f.create(t, f.secret, "MA-SAME")
	f.access(t, f.secret, http.MethodDelete, "MA-SAME", "")
	if code, _ := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/people/MA-SAME/permissions",
		`{"scope_type":"COMPANY","effect":"ALLOW"}`, f.token, f.csrf); code != http.StatusCreated {
		t.Fatal("operator grant failed")
	}
	f.access(t, f.secret, http.MethodPut, "MA-SAME", "")
	f.access(t, f.secret, http.MethodDelete, "MA-SAME", "")
	if got := strings.Join(liveRules(t, "MA-SAME"), ","); got != "COMPANY/ALLOW/operator" {
		t.Errorf("rules = %s, want only the operator's", got)
	}
}

// ---------------------------------------------------------------------------
// Ownership
// ---------------------------------------------------------------------------

func TestOnlyMembersThisIntegrationCreatedCanBeManaged(t *testing.T) {
	f := newManagedFixture(t)
	other := f.credential(t, `{"name":"another integration","scopes":[`+managedScopes+`]}`)
	f.create(t, other, "MA-THEIRS")                           // another integration's member
	f.env.createMember(f.env.siteAKey, "MA-LEGACY", "Legacy") // created outside the public API
	f.env.createMember(f.env.siteCKey, "MA-FOREIGN", "Other company")
	otherCompany := publicCredential(t, f.env, "two", "ma-two@example.com", `{"name":"two","scopes":[`+managedScopes+`]}`)
	f.create(t, otherCompany, "MA-TWO")

	for _, id := range []string{"MA-THEIRS", "MA-LEGACY", "MA-FOREIGN", "MA-TWO", "MA-NOBODY"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			status, body, _ := f.access(t, f.secret, method, id, "")
			if status != http.StatusNotFound || body["error"].(map[string]any)["code"] != models.CodeResourceNotFound {
				t.Errorf("%s %s = %d %v, want 404", method, id, status, body)
			}
		}
	}
	if got := liveRules(t, "MA-LEGACY"); len(got) != 0 {
		t.Errorf("an unmanaged member got rules %v", got)
	}
	// The other integration's own rule is untouched by this one's DELETE.
	if got := strings.Join(liveRules(t, "MA-THEIRS"), ","); got != "COMPANY/ALLOW/integration" {
		t.Errorf("another integration's member = %s", got)
	}
}

func TestASiteRestrictedCredentialGrantsAndRemovesOnlyAtItsSites(t *testing.T) {
	f := newManagedFixture(t)
	restricted := f.credential(t, `{"name":"site a","scopes":[`+managedScopes+`],"site_ids":["`+f.siteA+`"]}`)
	f.create(t, restricted, "MA-SITE")

	if got := strings.Join(liveRules(t, "MA-SITE"), ","); got != "SITE/ALLOW/Site A/integration" {
		t.Errorf("restricted grant = %s", got)
	}
	if !contains(terminalApplies(t, f.env, f.keyA, "MA-SITE"), models.SyncJobCreate) {
		t.Error("the Site A terminal did not receive the member")
	}
	if jobs := terminalApplies(t, f.env, f.keyB, "MA-SITE"); len(jobs) != 0 {
		t.Errorf("the Site B terminal received %v", jobs)
	}
	if decideAt(t, "MA-SITE", "MA-B1").Granted {
		t.Error("a site-restricted grant admitted at another site")
	}
	f.access(t, restricted, http.MethodDelete, "MA-SITE", "")
	if got := liveRules(t, "MA-SITE"); len(got) != 0 {
		t.Errorf("rules after the restricted removal = %v", got)
	}
}

func TestRotationKeepsOwnershipAndRevocationKeepsAccess(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-ROT")

	code, rotated := consoleCall(t, f.env.router, http.MethodPost, credentialsPath+"/"+f.credentialID+"/rotate", `{"grace_seconds":0}`, f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("rotate = %d %v", code, rotated)
	}
	next := secretOf(t, rotated)
	if n := queryInt(t, `SELECT count(DISTINCT lineage_id) FROM api_credentials WHERE public_id::text IN ($1, $2)`, f.credentialID, rotated["id"].(string)); n != 1 {
		t.Error("rotation started a new lineage")
	}
	if status, _, raw := f.access(t, next, http.MethodDelete, "MA-ROT", ""); status != http.StatusOK {
		t.Fatalf("the rotated key cannot manage its predecessor's member: %d %s", status, raw)
	}
	f.access(t, next, http.MethodPut, "MA-ROT", "")

	// Revoking the key leaves the access in place; a new key is a new lineage.
	if code, _ := consoleCall(t, f.env.router, http.MethodDelete, credentialsPath+"/"+rotated["id"].(string), `{"reason":"test"}`, f.token, f.csrf); code != http.StatusOK {
		t.Fatal("revoke failed")
	}
	if got := strings.Join(liveRules(t, "MA-ROT"), ","); got != "COMPANY/ALLOW/integration" || !decideAt(t, "MA-ROT", "MA-A1").Granted {
		t.Errorf("revoking the key removed access: %s", got)
	}
	status, _, _ := f.access(t, next, http.MethodDelete, "MA-ROT", "")
	if status != http.StatusUnauthorized {
		t.Errorf("a revoked key = %d, want 401", status)
	}
	fresh := f.credential(t, `{"name":"new key","scopes":[`+managedScopes+`]}`)
	if status, _, _ := f.access(t, fresh, http.MethodDelete, "MA-ROT", ""); status != http.StatusNotFound {
		t.Errorf("a new, unrelated key = %d, want 404", status)
	}
}

func TestConsoleGrantsReachTheTerminalsAtOnce(t *testing.T) {
	f := newManagedFixture(t)
	f.env.createMember(f.env.siteAKey, "MA-OP", "Operator person")
	terminalApplies(t, f.env, f.keyA, "MA-OP")
	if code, _ := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/people/MA-OP/permissions",
		`{"scope_type":"COMPANY","effect":"ALLOW"}`, f.token, f.csrf); code != http.StatusCreated {
		t.Fatal("operator grant failed")
	}
	if !contains(terminalApplies(t, f.env, f.keyA, "MA-OP"), models.SyncJobCreate) {
		t.Error("a console grant waited for the sweep")
	}
	var permissionID string
	mustScan(t, `SELECT pm.public_id::text FROM permissions pm JOIN people p ON p.id = pm.person_id WHERE p.external_id = 'MA-OP' AND pm.deleted_at IS NULL`, &permissionID)
	if code, _ := consoleCall(t, f.env.router, http.MethodDelete, "/api/v1/console/permissions/"+permissionID, "", f.token, f.csrf); code != http.StatusOK {
		t.Fatal("operator revoke failed")
	}
	if !contains(terminalApplies(t, f.env, f.keyA, "MA-OP"), models.SyncJobDelete) {
		t.Error("a console revoke waited for the sweep")
	}
}

// ---------------------------------------------------------------------------
// Adoption from the audit trail (migrations/039)
// ---------------------------------------------------------------------------

func TestAdoptionOnlyFromAnUnambiguousAuditTrail(t *testing.T) {
	f := newManagedFixture(t)
	prefix := f.secret[:17]
	var lineage int64
	mustScan(t, `SELECT lineage_id FROM api_credentials WHERE public_id::text = '`+f.credentialID+`'`, &lineage)
	twoID := operatorCompanyID(t, "two")

	// People created outside the public API (no provenance), plus audit rows.
	audit := func(company int64, person, role, via, keyPrefix string) {
		mustExec(t, `INSERT INTO audit_events (company_id, actor_email, actor_role, action, target_type, target_public_id, changes)
		             SELECT $1, $2::text, $3, 'PERSON_CREATED', 'PERSON', p.public_id,
		                    jsonb_build_object('via', $4::text, 'credential_key_prefix', $2::text)
		               FROM people p WHERE p.external_id = $5`, company, keyPrefix, role, via, person)
	}
	for _, id := range []string{"AD-GOOD", "AD-TWICE", "AD-OPERATOR", "AD-CONSOLE", "AD-OTHERCO", "AD-UNKNOWNKEY", "AD-DELETED"} {
		f.env.createMember(f.env.siteAKey, id, id)
	}
	audit(f.companyID, "AD-GOOD", "INTEGRATION", "public_api", prefix)
	audit(f.companyID, "AD-TWICE", "INTEGRATION", "public_api", prefix)
	audit(f.companyID, "AD-TWICE", "INTEGRATION", "public_api", prefix)
	audit(f.companyID, "AD-OPERATOR", "ADMIN", "public_api", prefix)
	audit(f.companyID, "AD-CONSOLE", "INTEGRATION", "console", prefix)
	audit(twoID, "AD-OTHERCO", "INTEGRATION", "public_api", prefix)
	audit(f.companyID, "AD-UNKNOWNKEY", "INTEGRATION", "public_api", "atp_test_ffffffff")
	audit(f.companyID, "AD-DELETED", "INTEGRATION", "public_api", prefix)
	mustExec(t, `UPDATE people SET deleted_at = CURRENT_TIMESTAMP WHERE external_id = 'AD-DELETED'`)

	var adopted int
	mustScan(t, `SELECT adopt_integration_members_from_audit()`, &adopted)
	if adopted != 1 {
		t.Errorf("adopted %d people, want exactly 1", adopted)
	}
	managed := func(id string) bool {
		return queryInt(t, `SELECT count(*) FROM people WHERE external_id = $1 AND managed_by_lineage_id = $2`, id, lineage) == 1
	}
	if !managed("AD-GOOD") {
		t.Error("the unambiguous case was not adopted")
	}
	for _, id := range []string{"AD-TWICE", "AD-OPERATOR", "AD-CONSOLE", "AD-OTHERCO", "AD-UNKNOWNKEY", "AD-DELETED"} {
		if queryInt(t, `SELECT count(*) FROM people WHERE external_id = $1 AND managed_by_lineage_id IS NOT NULL`, id) != 0 {
			t.Errorf("%s was adopted", id)
		}
	}
	// Adoption grants nothing by itself, and a second run changes nothing.
	if got := liveRules(t, "AD-GOOD"); len(got) != 0 {
		t.Errorf("adoption wrote rules %v", got)
	}
	mustScan(t, `SELECT adopt_integration_members_from_audit()`, &adopted)
	if adopted != 0 {
		t.Errorf("a second adoption run changed %d people", adopted)
	}
	// The adopted member is now manageable by its integration.
	if status, _, raw := f.access(t, f.secret, http.MethodPut, "AD-GOOD", ""); status != http.StatusOK {
		t.Errorf("the adopted member = %d %s", status, raw)
	}
}

// ---------------------------------------------------------------------------
// Scope drift between the database and the registry
// ---------------------------------------------------------------------------

// The scope list is a closed CHECK rebuilt by several migrations on different
// branches, and the one APPLIED LAST wins (deploy/migrate.sh). This reads the
// live constraint and compares it with the registry, so any merge order that
// leaves them apart fails here rather than at a deploy (see migrations/039).
func TestScopeCheckConstraintMatchesTheRegistry(t *testing.T) {
	newTestEnv(t)
	var def string
	mustScan(t, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'api_credentials_scopes_check'`, &def)
	var inDB []string
	for _, m := range regexp.MustCompile(`'([a-z]+:[a-z]+)'`).FindAllStringSubmatch(def, -1) {
		inDB = append(inDB, m[1])
	}
	sort.Strings(inDB)
	if want := models.AllScopes(); strings.Join(inDB, ",") != strings.Join(want, ",") {
		t.Fatalf("api_credentials_scopes_check allows %v, the registry has %v.\n"+
			"The last-applied migration's scope list must equal models.AllScopes(); see the merge rule in migrations/039.", inDB, want)
	}
}

// The scheduled sweep (ReconcileCompanyRosters) used to re-send every person a
// terminal had already acknowledged, on every run: it only looked for LIVE jobs.
// A converged, acknowledged terminal now receives nothing.
func TestTheSweepSendsNothingToAConvergedTerminal(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-SWEEP-1")
	f.create(t, f.secret, "MA-SWEEP-2")
	f.access(t, f.secret, http.MethodDelete, "MA-SWEEP-2", "")
	terminalApplies(t, f.env, f.keyA, "")
	terminalApplies(t, f.env, f.keyB, "")

	before := queryInt(t, `SELECT count(*) FROM sync_jobs`)
	for i := 0; i < 2; i++ {
		if _, added, removed, err := database.ReconcileCompanyRosters(f.companyID); err != nil || added+removed != 0 {
			t.Fatalf("sweep %d on a converged company queued %d/%d (err %v)", i+1, added, removed, err)
		}
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs`); n != before {
		t.Errorf("the sweep queued %d jobs for acknowledged terminals", n-before)
	}
	// And it still repairs a real gap: a CREATE the terminal failed is re-sent.
	mustExec(t, `UPDATE sync_jobs SET status = 'FAILED', acknowledged_at = NULL WHERE entity_external_id = 'MA-SWEEP-1' AND job_type = 'CREATE'`)
	if _, added, _, err := database.ReconcileCompanyRosters(f.companyID); err != nil || added != 2 {
		t.Errorf("a failed CREATE was not re-sent to both terminals: added %d (err %v)", added, err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle and ordering (review of integration-managed access)
// ---------------------------------------------------------------------------

// lastJob is the job type and payload.active of the newest job a terminal would
// apply for a person: what it ends up holding once it has caught up.
func lastJob(t *testing.T, serial, memberID string) (string, bool) {
	t.Helper()
	var jobType string
	var active bool
	mustScan(t, `SELECT j.job_type, COALESCE((j.payload->>'active')::boolean, FALSE)
	               FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	              WHERE d.serial_number = '`+serial+`' AND j.entity_external_id = '`+memberID+`'
	                AND j.status IN ('PENDING', 'DELIVERED', 'COMPLETED')
	              ORDER BY j.id DESC LIMIT 1`, &jobType, &active)
	return jobType, active
}

func (f *managedFixture) setActive(t *testing.T, memberID string, active bool) {
	t.Helper()
	if status, _, _, raw := publicCall(t, f.env, f.secret, http.MethodPatch, "/api/public/v1/members/"+memberID,
		fmt.Sprintf(`{"active":%v}`, active), ""); status != http.StatusOK {
		t.Fatalf("PATCH active=%v = %d %s", active, status, raw)
	}
}

// The integration's rule never admits an inactive member: the engine checks
// people.active before it looks at any permission, and the terminal receives
// active=false in the roster payload, which the firmware refuses
// (recognition_engine.cpp: record.active == 0 -> kDeniedInactive).
func TestAnInactiveMemberWithIntegrationAccessIsDeniedByTheEngineAndTheTerminal(t *testing.T) {
	f := newManagedFixture(t)
	if status, _, _, raw := publicCall(t, f.env, f.secret, http.MethodPost, "/api/public/v1/members",
		`{"member_id":"MA-INACTIVE","full_name":"Lapsed Member","active":false}`, ""); status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, raw)
	}
	if got := strings.Join(liveRules(t, "MA-INACTIVE"), ","); got != "COMPANY/ALLOW/integration" {
		t.Fatalf("rules = %s", got)
	}
	if d := decideAt(t, "MA-INACTIVE", "MA-A1"); d.Granted || d.Reason != models.ReasonPersonInactive {
		t.Errorf("inactive with an ALLOW rule: granted=%v reason=%s, want PERSON_INACTIVE", d.Granted, d.Reason)
	}
	if job, active := lastJob(t, "MA-A1", "MA-INACTIVE"); job != models.SyncJobCreate || active {
		t.Errorf("terminal holds %s active=%v, want CREATE active=false", job, active)
	}

	// Only DataVase's active flag opens the door, and only while it is true.
	f.setActive(t, "MA-INACTIVE", true)
	if d := decideAt(t, "MA-INACTIVE", "MA-A1"); !d.Granted {
		t.Errorf("activated: %+v", d)
	}
	if _, active := lastJob(t, "MA-A1", "MA-INACTIVE"); !active {
		t.Error("the terminal was not told the member is active")
	}
	f.setActive(t, "MA-INACTIVE", false)
	if d := decideAt(t, "MA-INACTIVE", "MA-A1"); d.Granted || d.Reason != models.ReasonPersonInactive {
		t.Errorf("deactivated: %+v", d)
	}
	if job, active := lastJob(t, "MA-A1", "MA-INACTIVE"); job == models.SyncJobDelete || active {
		t.Errorf("deactivation: terminal holds %s active=%v, want the record kept with active=false", job, active)
	}
}

// Whatever order the calls arrive in, the last job each terminal applies is the
// current state: the diff is taken against what the terminal was last told.
func TestRapidGrantAndRemovalConvergeOnTheLatestState(t *testing.T) {
	f := newManagedFixture(t)
	f.create(t, f.secret, "MA-RACE")
	f.create(t, f.secret, "MA-BYSTANDER")
	terminalApplies(t, f.env, f.keyA, "")
	bystanderJobs := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-BYSTANDER'`)

	// Removal immediately followed by a grant, before the terminal polls.
	f.access(t, f.secret, http.MethodDelete, "MA-RACE", "")
	f.access(t, f.secret, http.MethodPut, "MA-RACE", "")
	if job, _ := lastJob(t, "MA-A1", "MA-RACE"); job != models.SyncJobCreate {
		t.Errorf("remove then grant: terminal ends on %s, want CREATE", job)
	}
	// Grant immediately followed by a removal.
	f.access(t, f.secret, http.MethodDelete, "MA-RACE", "")
	if job, _ := lastJob(t, "MA-A1", "MA-RACE"); job != models.SyncJobDelete {
		t.Errorf("grant then remove: terminal ends on %s, want DELETE", job)
	}
	// Duplicates change nothing.
	before := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-RACE'`)
	f.access(t, f.secret, http.MethodDelete, "MA-RACE", "")
	f.access(t, f.secret, http.MethodDelete, "MA-RACE", "")
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-RACE'`); n != before {
		t.Errorf("duplicate removals queued %d jobs", n-before)
	}
	f.access(t, f.secret, http.MethodPut, "MA-RACE", "")
	before = queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-RACE'`)
	f.access(t, f.secret, http.MethodPut, "MA-RACE", "")
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-RACE'`); n != before {
		t.Errorf("duplicate grants queued %d jobs", n-before)
	}
	// The terminal applies everything in order and holds the member.
	terminalApplies(t, f.env, f.keyA, "")
	if job, _ := lastJob(t, "MA-A1", "MA-RACE"); job != models.SyncJobCreate || !decideAt(t, "MA-RACE", "MA-A1").Granted {
		t.Errorf("after catching up the terminal holds %s", job)
	}

	// A removal the terminal failed is repaired by the next reconcile.
	f.access(t, f.secret, http.MethodDelete, "MA-RACE", "")
	mustExec(t, `UPDATE sync_jobs SET status = 'FAILED', acknowledged_at = NULL
	              WHERE entity_external_id = 'MA-RACE' AND job_type = 'DELETE' AND status = 'PENDING'`)
	if _, _, removed, err := database.ReconcileCompanyRosters(f.companyID); err != nil || removed != 2 {
		t.Errorf("a failed DELETE was not re-sent to both terminals: removed %d (err %v)", removed, err)
	}
	if job, _ := lastJob(t, "MA-A1", "MA-RACE"); job != models.SyncJobDelete {
		t.Errorf("after repair the terminal ends on %s, want DELETE", job)
	}
	// None of this touched anybody else.
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = 'MA-BYSTANDER'`); n != bystanderJobs {
		t.Errorf("an unrelated member gained %d jobs", n-bystanderJobs)
	}
}
