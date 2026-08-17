package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Terminal lifecycle and site-key narrowing.
//
// SEC-01 was that there was no way to revoke a terminal at any credential
// class; SEC-02 was that the site provisioning key read the whole company. Both
// are security findings, so the tests are written as attacks: each one performs
// the action the finding said was possible and asserts it now is not.

// heartbeat calls the device liveness endpoint with a credential and returns the
// status. Used throughout this file as the question "does this key still
// authenticate", which is the only question revocation is about.
func (e *testEnv) heartbeat(deviceKey string) int {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/devices/heartbeat", nil, deviceAuth(deviceKey)).Code
}

// TestRevokedTerminalCannotAuthenticate is the finding, directly.
//
// A stolen terminal kept a valid device key indefinitely. The only lever was
// retiring its entire site, which stops every other door there.
func TestRevokedTerminalCannotAuthenticate(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "revoke-admin@example.com", models.RoleAdmin)

	deviceKey := env.registerDevice(env.siteAKey, "ESP32-REVOKE")

	// It works before revocation, or the test proves nothing.
	if code := env.heartbeat(deviceKey); code != http.StatusOK {
		t.Fatalf("heartbeat before revocation = %d, want 200", code)
	}

	code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/ESP32-REVOKE/revoke",
		`{"reason":"reported stolen"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200 (%v)", code, body)
	}

	// THE ASSERTION THAT MATTERS.
	if code := env.heartbeat(deviceKey); code != http.StatusUnauthorized {
		t.Fatalf("heartbeat after revocation = %d, want 401", code)
	}

	// The hash is GONE, not flagged. A status the authentication lookup does not
	// consult authorizes nothing, and a revocation that depends on a check
	// somewhere above the lookup is one a refactor can lose.
	var hasHash bool
	mustScan(t, `SELECT api_key_hash IS NOT NULL FROM devices WHERE serial_number = 'ESP32-REVOKE'`,
		&hasHash)
	if hasHash {
		t.Error("the device key hash survived revocation")
	}

	var recorded bool
	mustScan(t, `SELECT credential_revoked_at IS NOT NULL FROM devices
	              WHERE serial_number = 'ESP32-REVOKE'`, &recorded)
	if !recorded {
		t.Error("revocation was not recorded")
	}
}

// TestDisabledTerminalKeepsItsCredential proves the distinction between disable
// and revoke.
//
// Disable is for "this terminal is faulty" and must be reversible without a site
// visit to re-provision it. Revoke is for "this terminal is stolen" and must
// hold even if somebody re-enables the row.
func TestDisabledTerminalKeepsItsCredential(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "disable-admin@example.com", models.RoleAdmin)

	deviceKey := env.registerDevice(env.siteAKey, "ESP32-DISABLE")

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/ESP32-DISABLE/state",
		`{"disabled":true,"reason":"faulty reader"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("disable = %d, want 200 (%v)", code, body)
	}

	if got := env.heartbeat(deviceKey); got != http.StatusForbidden {
		t.Fatalf("heartbeat while disabled = %d, want 403", got)
	}

	// The credential survived, which is what makes this reversible.
	var hasHash bool
	mustScan(t, `SELECT api_key_hash IS NOT NULL FROM devices WHERE serial_number = 'ESP32-DISABLE'`,
		&hasHash)
	if !hasHash {
		t.Fatal("disabling destroyed the credential; that is revocation, not disabling")
	}

	code, body = consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/ESP32-DISABLE/state",
		`{"disabled":false}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("re-enable = %d, want 200 (%v)", code, body)
	}

	// Back in service on the SAME key -- no re-provisioning.
	if got := env.heartbeat(deviceKey); got != http.StatusOK {
		t.Fatalf("heartbeat after re-enable = %d, want 200", got)
	}
}

// TestRetiredTerminalCannotAuthenticate proves retirement revokes on the way
// out.
//
// A retired row is invisible to every console query -- they all join sites and
// filter deleted_at -- so a credential that kept working would be orphaned
// hardware nobody could see and nobody could revoke.
func TestRetiredTerminalCannotAuthenticate(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "retire-admin@example.com", models.RoleAdmin)

	deviceKey := env.registerDevice(env.siteAKey, "ESP32-RETIRE")

	code, body := consoleCall(t, env.router, "DELETE",
		"/api/v1/console/terminals/ESP32-RETIRE",
		`{"reason":"decommissioned"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("retire = %d, want 200 (%v)", code, body)
	}

	if got := env.heartbeat(deviceKey); got != http.StatusUnauthorized {
		t.Fatalf("heartbeat after retirement = %d, want 401", got)
	}
}

// TestRevokedTerminalDoesNotAffectItsNeighbours proves the blast radius is one
// terminal.
//
// This is the property the whole design rests on: revocation had to be per
// device precisely so that dealing with one compromised unit does not mean
// breaking every other one, which is what rotating the site key would do.
func TestRevokedTerminalDoesNotAffectItsNeighbours(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "blast-admin@example.com", models.RoleAdmin)

	victim := env.registerDevice(env.siteAKey, "ESP32-VICTIM")
	neighbour := env.registerDevice(env.siteAKey, "ESP32-NEIGHBOUR")
	otherSite := env.registerDevice(env.siteBKey, "ESP32-OTHER-SITE")

	if code, _ := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/ESP32-VICTIM/revoke",
		`{"reason":"stolen"}`, token, csrf); code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", code)
	}

	if got := env.heartbeat(victim); got != http.StatusUnauthorized {
		t.Errorf("victim heartbeat = %d, want 401", got)
	}
	if got := env.heartbeat(neighbour); got != http.StatusOK {
		t.Errorf("neighbour at the same site = %d, want 200 -- revocation must not spread", got)
	}
	if got := env.heartbeat(otherSite); got != http.StatusOK {
		t.Errorf("terminal at another site = %d, want 200", got)
	}
}

// TestTerminalLifecycleIsCompanyScoped proves the tenancy boundary holds on
// every new route.
//
// A serial is printed on the hardware, so possession of one is not a control.
// An operator naming another tenant's terminal must get 404 -- never 403, which
// would confirm the serial is registered somewhere.
func TestTerminalLifecycleIsCompanyScoped(t *testing.T) {
	env := newTestEnv(t)

	// Company two's terminal.
	env.registerDevice(env.siteCKey, "ESP32-TENANT-TWO")

	// Company one's admin.
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "tenant-admin@example.com", models.RoleAdmin)

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{"PUT", "/api/v1/console/terminals/ESP32-TENANT-TWO/state", `{"disabled":true}`},
		{"POST", "/api/v1/console/terminals/ESP32-TENANT-TWO/revoke", `{}`},
		{"DELETE", "/api/v1/console/terminals/ESP32-TENANT-TWO", `{}`},
		{"POST", "/api/v1/console/terminals/ESP32-TENANT-TWO/resync", ``},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			code, body := consoleCall(t, env.router, route.method, route.path, route.body, token, csrf)
			if code != http.StatusNotFound {
				t.Fatalf("%s = %d, want 404 (%v)", route.method, code, body)
			}
		})
	}

	// And it is untouched.
	var status string
	mustScan(t, `SELECT status FROM devices WHERE serial_number = 'ESP32-TENANT-TWO'`, &status)
	if status == "DISABLED" {
		t.Fatal("another tenant's terminal was disabled across the company boundary")
	}
}

// TestTerminalCannotMoveAcrossCompanies proves reassignment cannot be used as a
// tenancy escape.
//
// A device reaches its company only through its site, so a cross-company move
// would silently hand whatever roster it holds to a different tenant.
func TestTerminalCannotMoveAcrossCompanies(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "move-admin@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-MOVE")

	var otherSitePublicID string
	mustScan(t, `SELECT s.public_id FROM sites s
	              JOIN companies c ON c.id = s.company_id
	             WHERE c.slug = 'two'`, &otherSitePublicID)

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/ESP32-MOVE/site",
		`{"site_id":"`+otherSitePublicID+`"}`, token, csrf)
	if code != http.StatusNotFound {
		t.Fatalf("cross-company move = %d, want 404 (%v)", code, body)
	}

	var siteCompany string
	mustScan(t, `SELECT c.slug FROM devices d
	              JOIN sites s ON s.id = d.site_id
	              JOIN companies c ON c.id = s.company_id
	             WHERE d.serial_number = 'ESP32-MOVE'`, &siteCompany)
	if siteCompany != "one" {
		t.Fatalf("terminal ended up in company %q; the tenancy boundary was crossed", siteCompany)
	}
}

// TestTerminalMoveRebuildsTheRoster proves a moved terminal does not keep
// serving the previous location's people.
func TestTerminalMoveRebuildsTheRoster(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "relocate@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-RELOCATE")
	env.createMember(env.siteAKey, "M-OLD-SITE", "Old Site Person")

	var pendingBefore int
	mustScan(t, `SELECT count(*) FROM sync_jobs j
	              JOIN devices d ON d.id = j.device_id
	             WHERE d.serial_number = 'ESP32-RELOCATE' AND j.status = 'PENDING'`,
		&pendingBefore)
	if pendingBefore == 0 {
		t.Fatal("no work was queued before the move, so the test proves nothing")
	}

	var siteBPublicID string
	mustScan(t, `SELECT public_id FROM sites WHERE site_name = 'Site B'`, &siteBPublicID)

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/ESP32-RELOCATE/site",
		`{"site_id":"`+siteBPublicID+`"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("move = %d, want 200 (%v)", code, body)
	}

	// NO WORK FOR THE OLD SITE SURVIVES.
	//
	// Asserted by the job's OWN site_id rather than by counting the queue.
	// sync_jobs records the site each job was enqueued for, so "nothing stale
	// from Site A is still deliverable" is directly checkable -- and stays
	// checkable now that the move queues replacement work.
	//
	// This assertion used to be `pendingAfter != 0`, i.e. "the queue is empty
	// afterwards". That was asserting the defect: an empty queue is precisely
	// what left a relocated terminal running on its cached Site A roster until
	// a background reconciler happened to run.
	var stalePending int
	mustScan(t, `SELECT count(*) FROM sync_jobs j
	              JOIN devices d ON d.id = j.device_id
	              JOIN sites s ON s.id = j.site_id
	             WHERE d.serial_number = 'ESP32-RELOCATE'
	               AND s.site_name = 'Site A'
	               AND j.status = 'PENDING'`, &stalePending)
	if stalePending != 0 {
		t.Errorf("%d jobs for the old site are still deliverable after the move", stalePending)
	}

	// And they were superseded rather than quietly deleted, so an operator can
	// still see what was dropped.
	var cancelled int
	mustScan(t, `SELECT count(*) FROM sync_jobs j
	              JOIN devices d ON d.id = j.device_id
	             WHERE d.serial_number = 'ESP32-RELOCATE' AND j.status = 'CANCELLED'`,
		&cancelled)
	if cancelled == 0 {
		t.Error("the old site's queued work vanished instead of being cancelled")
	}

	var siteName string
	mustScan(t, `SELECT s.site_name FROM devices d JOIN sites s ON s.id = d.site_id
	             WHERE d.serial_number = 'ESP32-RELOCATE'`, &siteName)
	if siteName != "Site B" {
		t.Errorf("terminal is at %q after the move, want Site B", siteName)
	}
}

// ---------------------------------------------------------------------------
// SEC-02: the site key stops being a company-wide credential
// ---------------------------------------------------------------------------

// TestSiteKeyCannotReadAnotherSitesTerminals proves the fleet inventory is
// narrowed.
//
// The old handler passed a nil scope with the comment that a site key holder "is
// trusted with the whole company's inventory by construction". A site key is a
// secret installed on hardware bolted to a wall at one location; that reasoning
// does not survive contact with what it physically is.
func TestSiteKeyCannotReadAnotherSitesTerminals(t *testing.T) {
	env := newTestEnv(t)

	env.registerDevice(env.siteAKey, "ESP32-AT-SITE-A")
	env.registerDevice(env.siteBKey, "ESP32-AT-SITE-B")

	res := env.do(http.MethodGet, "/api/v1/devices", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("device list = %d, want 200", res.Code)
	}

	devices, _ := res.Body["devices"].([]any)
	for _, raw := range devices {
		device, _ := raw.(map[string]any)
		if device["serial_number"] == "ESP32-AT-SITE-B" {
			t.Fatal("Site A's key listed a terminal at Site B")
		}
	}
	if len(devices) != 1 {
		t.Fatalf("Site A's key sees %d terminals, want exactly its own", len(devices))
	}
}

// TestSiteKeyCannotReadAnotherSitesAccessLogs proves the audit read is narrowed.
func TestSiteKeyCannotReadAnotherSitesAccessLogs(t *testing.T) {
	env := newTestEnv(t)

	siteA := siteIDByKey(t, env.siteAKey)
	siteB := siteIDByKey(t, env.siteBKey)
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `INSERT INTO access_logs (company_id, site_id, person_external_id, granted, source, site_name)
	             VALUES ($1, $2, 'M-A', TRUE, 'FINGERPRINT', 'Site A')`, companyID, siteA)
	mustExec(t, `INSERT INTO access_logs (company_id, site_id, person_external_id, granted, source, site_name)
	             VALUES ($1, $2, 'M-B', TRUE, 'FINGERPRINT', 'Site B')`, companyID, siteB)

	code, logs := env.list(http.MethodGet, "/api/v1/access/logs", siteAuth(env.siteAKey))
	if code != http.StatusOK {
		t.Fatalf("access logs = %d, want 200", code)
	}

	for _, entry := range logs {
		if entry["member_id"] == "M-B" {
			t.Fatal("Site A's key read a door event from Site B")
		}
	}
	if len(logs) != 1 {
		t.Fatalf("Site A's key sees %d events, want exactly its own", len(logs))
	}
}

// TestSiteKeyCannotWriteTheFirmwareCatalogue proves the writes moved.
//
// Any site key could previously add a build and flip `is_current` -- the target
// every "is this terminal outdated" report is measured against, and once OTA
// exists the row a terminal would be pointed at.
func TestSiteKeyCannotWriteTheFirmwareCatalogue(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodPost, "/api/v1/firmware",
		map[string]any{"version": "9.9.9", "device_type": "TERMINAL"},
		siteAuth(env.siteAKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("site-key firmware publish = %d, want 404 -- the route must be gone", res.Code)
	}

	res = env.do(http.MethodPut, "/api/v1/firmware/1/current", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("site-key firmware target set = %d, want 404", res.Code)
	}

	// The read stays: a terminal checking what it should be running is a
	// legitimate use of a device-adjacent secret.
	res = env.do(http.MethodGet, "/api/v1/firmware", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("site-key firmware read = %d, want 200 -- the read was not supposed to move", res.Code)
	}
}

// TestOperatorCanWriteTheFirmwareCatalogue proves the capability was MOVED
// rather than removed.
//
// The distinction matters: an audit item made to disappear by deleting a feature
// is not a fix.
func TestOperatorCanWriteTheFirmwareCatalogue(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "fw-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/firmware",
		`{"version":"2.1.0","device_type":"TERMINAL","release_channel":"STABLE"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("console firmware publish = %d, want 201 (%v)", code, body)
	}

	code, listing := consoleCall(t, env.router, "GET", "/api/v1/console/firmware", ``, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("console firmware list = %d, want 200 (%v)", code, listing)
	}
}

// TestLegacyMemberReadsCarryNoCredentialMaterial proves the biggest single
// disclosure is closed.
//
// GET /members returned fingerprint_template for every person in the COMPANY to
// anyone holding ANY site key.
func TestLegacyMemberReadsCarryNoCredentialMaterial(t *testing.T) {
	env := newTestEnv(t)

	env.createMember(env.siteAKey, "M-CRED", "Credentialed Person")
	mustExec(t, `UPDATE people SET fingerprint_template = 'terminal:ESP32-X:slot:4'
	              WHERE external_id = 'M-CRED'`)

	paths := []string{
		"/api/v1/members",
		"/api/v1/members/changes?since=2000-01-01T00:00:00Z",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			code, members := env.list(http.MethodGet, path, siteAuth(env.siteAKey))
			if code != http.StatusOK {
				t.Fatalf("%s = %d, want 200", path, code)
			}
			if len(members) == 0 {
				t.Fatalf("%s returned nothing, so the test proves nothing", path)
			}
			for _, member := range members {
				if _, present := member["fingerprint_template"]; present {
					t.Errorf("%s still discloses fingerprint_template", path)
				}
				if member["member_id"] == "M-CRED" {
					if enrolled, _ := member["biometric_enrolled"].(bool); !enrolled {
						t.Errorf("%s dropped the enrolment fact along with the material", path)
					}
				}
			}
		})
	}

	// The single read too.
	res := env.do(http.MethodGet, "/api/v1/members/M-CRED", nil, siteAuth(env.siteAKey))
	if _, present := res.Body["fingerprint_template"]; present {
		t.Error("GET /members/{id} still discloses fingerprint_template")
	}
}

// ---------------------------------------------------------------------------
// SEC-07: the operator audit trail
// ---------------------------------------------------------------------------

// TestTerminalLifecycleIsAudited proves the operations a disputed incident turns
// on are attributable.
func TestTerminalLifecycleIsAudited(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	const actor = "audited-admin@example.com"
	_, token, csrf := consoleOperatorSession(t, env.router, one, actor, models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-AUDITED")

	if code, _ := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/ESP32-AUDITED/revoke",
		`{"reason":"lost in transit"}`, token, csrf); code != http.StatusOK {
		t.Fatal("revoke did not succeed")
	}

	var actorEmail, targetLabel string
	mustScan(t, `SELECT actor_email, target_label
	               FROM audit_events
	              WHERE action = 'TERMINAL_CREDENTIAL_REVOKED'
	              ORDER BY id DESC LIMIT 1`, &actorEmail, &targetLabel)

	if actorEmail != actor {
		t.Errorf("audit actor = %q, want %q", actorEmail, actor)
	}
	if targetLabel != "ESP32-AUDITED" {
		t.Errorf("audit target = %q, want the serial", targetLabel)
	}

	// And it is readable through the console.
	code, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/audit?action=TERMINAL_CREDENTIAL_REVOKED", ``, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("audit read = %d, want 200 (%v)", code, body)
	}
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("audit read returned %d entries, want 1", len(entries))
	}
}

// TestAuditTrailIsCompanyScoped proves one tenant cannot read another's
// administrative history.
func TestAuditTrailIsCompanyScoped(t *testing.T) {
	env := newTestEnv(t)

	two := companyIDBySlug(t, "two")
	mustExec(t, `INSERT INTO audit_events (company_id, action, actor_email, target_label)
	             VALUES ($1, 'SITE_CREATED', 'other@example.com', 'Their Site')`, two)

	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "scope-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/audit", ``, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("audit read = %d, want 200", code)
	}

	entries, _ := body["entries"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["actor_email"] == "other@example.com" {
			t.Fatal("one company's audit trail disclosed another's")
		}
	}
}

// TestAuditTrailRequiresAdmin proves a viewer cannot read who did what.
func TestAuditTrailRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "viewer@example.com", models.RoleViewer)

	code, _ := consoleCall(t, env.router, "GET", "/api/v1/console/audit", ``, token, csrf)
	if code != http.StatusForbidden {
		t.Fatalf("viewer reading the audit trail = %d, want 403", code)
	}
}

// ---------------------------------------------------------------------------
// SYN-04: sync visibility
// ---------------------------------------------------------------------------

// TestTerminalHealthReportsBacklog proves an operator can tell that a terminal
// is behind.
//
// The console could show last_sync_at and nothing else -- no backlog, no failed
// jobs, no way to tell that people were missing from a door.
func TestTerminalHealthReportsBacklog(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-HEALTH")

	env.createMember(env.siteAKey, "M-H1", "One")
	env.createMember(env.siteAKey, "M-H2", "Two")

	companyID := companyIDBySlug(t, "one")
	health, err := database.GetTerminalHealth(companyID, "ESP32-HEALTH")
	if err != nil {
		t.Fatalf("terminal health: %v", err)
	}

	if health.PendingJobs == 0 {
		t.Error("terminal health reports no pending work despite two queued person changes")
	}
	if !health.CredentialActive {
		t.Error("a registered terminal reports no active credential")
	}
	if health.OfflinePolicy == "" {
		t.Error("terminal health carries no offline policy")
	}
}
