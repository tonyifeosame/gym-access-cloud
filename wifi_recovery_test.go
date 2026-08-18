package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The console's Change Wi-Fi command (024), end to end.
//
// THE FEATURE IN ONE SENTENCE: an administrator presses Change Wi-Fi, the
// terminal collects a WIFI_RECOVERY job on its next poll, acknowledges it, and
// only then does the console say anything happened. Every test below is one
// promise out of that sentence, written as the thing that must not be possible.
//
// WHAT IS DELIBERATELY NOT TESTED HERE: what the terminal does with the command.
// That is the firmware's WIFI_RECOVERY suite in 15caf88 and this repository must
// not restate it. What IS tested is the contract between them -- the exact job
// type token, the absence of a payload, and the acknowledgement retiring the
// job -- because that is the half this side owns.

// wifiRecoveryPath is the endpoint under test, for one serial.
func wifiRecoveryPath(serial string) string {
	return "/api/v1/console/terminals/" + serial + "/wifi-recovery"
}

// changeWifi presses the button as one operator and returns the raw answer.
func changeWifi(t *testing.T, env *testEnv, serial, token, csrf string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, env.router, "POST", wifiRecoveryPath(serial), "", token, csrf)
}

// wifiRecoveryState reads the command's state back the way the dialog polls it.
func wifiRecoveryState(t *testing.T, env *testEnv, serial, token string) map[string]any {
	t.Helper()
	code, body := consoleCall(t, env.router, "GET", wifiRecoveryPath(serial), "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET wifi-recovery = %d, want 200 (%v)", code, body)
	}
	return body
}

// onlineTerminal registers a terminal at Site A and leaves it ONLINE with a
// working credential AND the capability to carry this command out -- which
// together are the only state in which a command may be queued.
//
// THE HEARTBEAT IS PART OF THE FIXTURE NOW (025), and that is the feature
// rather than test scaffolding. A terminal that has never told the platform
// what it can do is refused, because the alternative -- assuming it can -- is
// what produced a console reporting that a door had confirmed a command its
// firmware never recognised. Registration alone no longer earns a terminal the
// right to be sent one.
func onlineTerminal(t *testing.T, env *testEnv, serial string) string {
	t.Helper()
	key := env.registerDevice(env.siteAKey, serial)
	reportCapabilities(t, env, key,
		models.CapabilityWifiProvisioning,
		models.CapabilityWifiRecovery,
		models.CapabilityTerminalAnnounce)
	return key
}

// reportCapabilities heartbeats as the terminal, saying what it can do.
//
// Passing NO tokens sends an explicit empty list -- "I report, and I have none"
// -- which is a different answer from never having beaten at all, and the two
// are tested separately.
func reportCapabilities(t *testing.T, env *testEnv, key string, tokens ...string) {
	t.Helper()

	list := tokens
	if list == nil {
		list = []string{}
	}
	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{
		"status":       "ONLINE",
		"capabilities": list,
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat reporting capabilities: got %d, want 200 (body %s)",
			res.Code, res.Raw)
	}
}

// wifiJobsFor returns the WIFI_RECOVERY rows queued for one serial, newest last.
func wifiJobsFor(t *testing.T, serial string) []struct {
	ID      int64
	Status  string
	Payload []byte
} {
	t.Helper()
	rows, err := database.DB.Query(`
		SELECT j.id, j.status, j.payload
		  FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.job_type = 'WIFI_RECOVERY'
		 ORDER BY j.id ASC`, serial)
	if err != nil {
		t.Fatalf("reading wifi recovery jobs: %v", err)
	}
	defer rows.Close()

	var out []struct {
		ID      int64
		Status  string
		Payload []byte
	}
	for rows.Next() {
		var row struct {
			ID      int64
			Status  string
			Payload []byte
		}
		if err := rows.Scan(&row.ID, &row.Status, &row.Payload); err != nil {
			t.Fatalf("scanning wifi recovery job: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading wifi recovery jobs: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

// TestChangeWifiIsAdministratorOnly is the role rule, stated as the four answers
// the four roles get.
//
// ADMIN rather than MANAGER because of what a misdirected command costs: the
// terminal stops working until somebody physically stands next to it with a
// phone. A resync, which IS manager-level, is invisible to everybody.
func TestChangeWifiIsAdministratorOnly(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	for _, tc := range []struct {
		role string
		want int
	}{
		{models.RoleOwner, http.StatusAccepted},
		{models.RoleAdmin, http.StatusAccepted},
		{models.RoleManager, http.StatusForbidden},
		{models.RoleViewer, http.StatusForbidden},
	} {
		t.Run(tc.role, func(t *testing.T) {
			// A terminal per role, so an earlier ACCEPTED does not leave a
			// pending command that changes a later answer.
			serial := "WIFI-ROLE-" + tc.role
			onlineTerminal(t, env, serial)

			_, token, csrf := consoleOperatorSession(t, env.router, one,
				strings.ToLower(tc.role)+"-wifi@example.com", tc.role)

			code, body := changeWifi(t, env, serial, token, csrf)
			if code != tc.want {
				t.Fatalf("%s Change Wi-Fi = %d, want %d (%v)", tc.role, code, tc.want, body)
			}

			// THE ASSERTION THE STATUS CODE DOES NOT MAKE. A refusal must queue
			// nothing: a 403 that had already written the row would be a role
			// check that only hides the button.
			jobs := wifiJobsFor(t, serial)
			if tc.want == http.StatusForbidden && len(jobs) != 0 {
				t.Errorf("%s was refused but %d command(s) were queued", tc.role, len(jobs))
			}
			if tc.want == http.StatusAccepted && len(jobs) != 1 {
				t.Errorf("%s queued %d command(s), want 1", tc.role, len(jobs))
			}
		})
	}
}

// TestChangeWifiStatusIsAdministratorOnly. The READ is gated like the write.
//
// It is not fleet information -- it says whether a command an administrator sent
// has been picked up -- and the only screen that asks for it is the dialog that
// sent it. A MANAGER who cannot send the command has no use for its progress.
func TestChangeWifiStatusIsAdministratorOnly(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-READ")

	_, manager, _ := consoleOperatorSession(t, env.router, one, "read-manager@example.com", models.RoleManager)
	if code, body := consoleCall(t, env.router, "GET", wifiRecoveryPath("WIFI-READ"),
		"", manager, ""); code != http.StatusForbidden {
		t.Errorf("MANAGER read the command status = %d, want 403 (%v)", code, body)
	}

	_, admin, _ := consoleOperatorSession(t, env.router, one, "read-admin@example.com", models.RoleAdmin)
	if code, body := consoleCall(t, env.router, "GET", wifiRecoveryPath("WIFI-READ"),
		"", admin, ""); code != http.StatusOK {
		t.Errorf("ADMIN read the command status = %d, want 200 (%v)", code, body)
	}
}

// TestChangeWifiRefusesWithoutASession, and refuses the SITE PROVISIONING KEY
// just as loudly.
//
// The site key registers terminals and rotates their credentials. It is not
// browser authentication, and presenting it to a console route must achieve
// nothing -- least of all a route that can take a door out of service.
func TestChangeWifiRefusesWithoutASession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	onlineTerminal(t, env, "WIFI-ANON")

	if code, _ := changeWifi(t, env, "WIFI-ANON", "", ""); code != http.StatusUnauthorized {
		t.Errorf("Change Wi-Fi without a session = %d, want 401", code)
	}

	req := newRequestWithSiteKey(t, "POST", wifiRecoveryPath("WIFI-ANON"), "", env.siteAKey)
	if code := serve(env.router, req); code != http.StatusUnauthorized {
		t.Errorf("Change Wi-Fi with a site API key = %d, want 401", code)
	}

	if len(wifiJobsFor(t, "WIFI-ANON")) != 0 {
		t.Error("an unauthenticated request queued a command")
	}
}

// TestChangeWifiRequiresCSRF. The command is a state change made by a cookie
// session, which is precisely the shape a cross-site form can forge.
func TestChangeWifiRequiresCSRF(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-CSRF")

	_, token, _ := consoleOperatorSession(t, env.router, one, "csrf-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-CSRF", token, ""); code != http.StatusForbidden {
		t.Fatalf("Change Wi-Fi without a CSRF token = %d, want 403 (%v)", code, body)
	}
	if len(wifiJobsFor(t, "WIFI-CSRF")) != 0 {
		t.Error("a request with no CSRF token queued a command")
	}
}

// TestChangeWifiCannotReachAnotherCompanysTerminal.
//
// 404 AND NOT 403. Answering "forbidden" would confirm that the serial is
// registered to somebody else, and a serial is printed on the hardware -- so the
// cross-tenant answer must be indistinguishable from "no such terminal".
func TestChangeWifiCannotReachAnotherCompanysTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	two := operatorCompanyID(t, "two")

	// The terminal lives in company one. The operator is an OWNER of company
	// two -- the most privileged role there is, so the refusal cannot be
	// mistaken for an insufficient one.
	onlineTerminal(t, env, "WIFI-TENANT")
	_, token, csrf := consoleOperatorSession(t, env.router, two, "tenant-owner@example.com", models.RoleOwner)

	code, body := changeWifi(t, env, "WIFI-TENANT", token, csrf)
	if code != http.StatusNotFound {
		t.Fatalf("cross-company Change Wi-Fi = %d, want 404 (%v)", code, body)
	}
	if code, body := consoleCall(t, env.router, "GET", wifiRecoveryPath("WIFI-TENANT"),
		"", token, ""); code != http.StatusNotFound {
		t.Errorf("cross-company status read = %d, want 404 (%v)", code, body)
	}

	if len(wifiJobsFor(t, "WIFI-TENANT")) != 0 {
		t.Error("another tenant queued a command against this terminal")
	}
}

// TestChangeWifiRefusesATerminalAtAnUngrantedSite.
//
// 403 here, unlike the cross-tenant 404. The terminal is in the operator's own
// company and they may know it exists; they are simply not scoped to the site it
// stands at.
func TestChangeWifiRefusesATerminalAtAnUngrantedSite(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	onlineTerminal(t, env, "WIFI-SCOPED")

	// An ADMIN would bypass grants entirely (OWNER and ADMIN are never
	// site-scoped), so the grant rule is only observable on a role below them --
	// and a MANAGER is refused on role before grants are ever consulted. The
	// grant path is therefore asserted directly against the middleware's own
	// resolver rather than through a role that cannot reach it.
	user, _, _ := consoleOperatorSession(t, env.router, one, "scoped-admin@example.com", models.RoleAdmin)
	siteB := siteID(t, "Site B")
	mustExec(t, `INSERT INTO user_site_grants (user_id, site_id) VALUES ($1, $2)`, user.ID, siteB)

	access, err := database.ResolveDeviceAccess(one, user.ID, "WIFI-SCOPED")
	if err != nil {
		t.Fatalf("resolving device access: %v", err)
	}
	if access.Granted {
		t.Fatal("an operator granted only Site B resolved as granted to a Site A terminal")
	}
}

// TestChangeWifiOnANonexistentTerminalIs404, on both routes.
func TestChangeWifiOnANonexistentTerminalIs404(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "ghost-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "NO-SUCH-TERMINAL", token, csrf); code != http.StatusNotFound {
		t.Errorf("Change Wi-Fi on a serial that does not exist = %d, want 404 (%v)", code, body)
	}
	if code, body := consoleCall(t, env.router, "GET", wifiRecoveryPath("NO-SUCH-TERMINAL"),
		"", token, ""); code != http.StatusNotFound {
		t.Errorf("status of a serial that does not exist = %d, want 404 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Eligibility: which terminals may be sent a command at all
// ---------------------------------------------------------------------------

// TestChangeWifiRefusesAnOfflineTerminal is the case the whole feature is about
// and the one that must NOT quietly queue.
//
// A terminal whose Wi-Fi is broken is offline, which is precisely why somebody
// is reaching for this button -- and it is exactly the terminal that will never
// collect the command. Queueing one anyway would be worse than refusing: it
// would sit in the outbox until the customer recovered the unit by hand, and
// then arrive and wipe the network they had just joined it to.
func TestChangeWifiRefusesAnOfflineTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-OFFLINE")
	mustExec(t, `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = 'WIFI-OFFLINE'`)

	_, token, csrf := consoleOperatorSession(t, env.router, one, "offline-admin@example.com", models.RoleAdmin)

	code, body := changeWifi(t, env, "WIFI-OFFLINE", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on an offline terminal = %d, want 409 (%v)", code, body)
	}
	// The console branches on the CODE to decide which recovery to explain --
	// for this one, the terminal's own local procedure. The message beside it is
	// for humans and is allowed to change.
	if body["code"] != models.WifiRecoveryTerminalOffline {
		t.Errorf("refusal code = %v, want %s", body["code"], models.WifiRecoveryTerminalOffline)
	}
	if body["terminal_status"] != "OFFLINE" {
		t.Errorf("terminal_status = %v, want OFFLINE", body["terminal_status"])
	}

	if len(wifiJobsFor(t, "WIFI-OFFLINE")) != 0 {
		t.Error("an offline terminal was given a command it can never collect")
	}
}

// TestChangeWifiRefusesADisabledTerminal, and says DISABLED rather than OFFLINE.
//
// A disabled terminal is also not heartbeating, so "offline" would be true and
// useless: it would send somebody to the door with a phone to fix something that
// is one click away in this console.
func TestChangeWifiRefusesADisabledTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-DISABLED")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "disabled-admin@example.com", models.RoleAdmin)

	if code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/WIFI-DISABLED/state",
		`{"disabled":true}`, token, csrf); code != http.StatusOK {
		t.Fatalf("disabling the terminal = %d (%v)", code, body)
	}

	code, body := changeWifi(t, env, "WIFI-DISABLED", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on a disabled terminal = %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.WifiRecoveryTerminalDisabled {
		t.Errorf("refusal code = %v, want %s", body["code"], models.WifiRecoveryTerminalDisabled)
	}
	if len(wifiJobsFor(t, "WIFI-DISABLED")) != 0 {
		t.Error("a disabled terminal was queued a command")
	}
}

// TestChangeWifiRefusesATerminalWithNoCredential.
//
// A terminal that has never been provisioned, or whose credential was revoked,
// cannot authenticate and therefore cannot poll. mustCreateDevice makes exactly
// that row: ONLINE by status, and holding no key.
func TestChangeWifiRefusesATerminalWithNoCredential(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "WIFI-UNPROVISIONED")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "unprov-admin@example.com", models.RoleAdmin)

	code, body := changeWifi(t, env, "WIFI-UNPROVISIONED", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on an unprovisioned terminal = %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.WifiRecoveryTerminalNoCredential {
		t.Errorf("refusal code = %v, want %s", body["code"], models.WifiRecoveryTerminalNoCredential)
	}
}

// TestChangeWifiRefusesARetiredTerminal. A retired row is invisible to every
// console query, so this is a 404 and not a conflict.
func TestChangeWifiRefusesARetiredTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-RETIRED")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "retired-admin@example.com", models.RoleAdmin)

	if code, body := consoleCall(t, env.router, "DELETE",
		"/api/v1/console/terminals/WIFI-RETIRED", "", token, csrf); code != http.StatusOK {
		t.Fatalf("retiring the terminal = %d (%v)", code, body)
	}

	if code, body := changeWifi(t, env, "WIFI-RETIRED", token, csrf); code != http.StatusNotFound {
		t.Errorf("Change Wi-Fi on a retired terminal = %d, want 404 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// The job itself, and the firmware contract
// ---------------------------------------------------------------------------

// TestChangeWifiQueuesTheJobFirmwareExpects is the compatibility test.
//
// Firmware 15caf88 compares the job type with strcmp and reads NO PAYLOAD -- the
// job id is the whole message. Both halves are asserted, because a token that
// drifted by one character parses as kUnknown, which the firmware acknowledges
// and ignores: the console would report the command delivered and accepted, and
// the terminal would have done nothing at all.
func TestChangeWifiQueuesTheJobFirmwareExpects(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := onlineTerminal(t, env, "WIFI-CONTRACT")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "contract-admin@example.com", models.RoleAdmin)

	code, body := changeWifi(t, env, "WIFI-CONTRACT", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d, want 202 (%v)", code, body)
	}
	if body["state"] != models.WifiRecoveryQueued {
		t.Errorf("state = %v, want %s", body["state"], models.WifiRecoveryQueued)
	}
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Error("the response carries no request id to poll on")
	}

	// The terminal polls, exactly as it does for everything else.
	var command map[string]any
	for _, job := range env.jobs(key) {
		if job["job_type"] == models.WifiRecoveryJobType {
			command = job
		}
	}
	if command == nil {
		t.Fatalf("the terminal's poll carried no %s job: %v", models.WifiRecoveryJobType, env.jobs(key))
	}

	// THE EXACT TOKEN. Not a constant defined next to the assertion -- the
	// literal the firmware's syncJobTypeFromName compares against.
	if command["job_type"] != "WIFI_RECOVERY" {
		t.Errorf("job_type = %v, want the literal WIFI_RECOVERY", command["job_type"])
	}
	if id, ok := command["id"].(float64); !ok || id <= 0 {
		t.Errorf("job id = %v, want a positive integer the terminal can acknowledge", command["id"])
	}

	// NO PAYLOAD, NO ENTITY. There is nowhere on this path for a network name or
	// a password to be, which is what makes "credentials do not travel through
	// the cloud" a property of the shape rather than of a redaction step.
	if payload, present := command["payload"]; present && payload != nil {
		t.Errorf("the command carries a payload: %v", payload)
	}
	if external, present := command["entity_external_id"]; present && external != "" {
		t.Errorf("the command names an entity: %v", external)
	}
}

// TestChangeWifiCarriesNoWifiCredentialAnywhere.
//
// The no-leak test, written as a search rather than as a field check: the point
// is that there is no field, so nothing on the path may contain anything that
// looks like a network name or a passphrase -- not the job row, not the audit
// record, not the response.
func TestChangeWifiCarriesNoWifiCredentialAnywhere(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-NOLEAK")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "noleak-admin@example.com", models.RoleAdmin)

	// A body that TRIES to smuggle credentials through. The endpoint takes no
	// body at all, and this asserts that ignoring it is what actually happens
	// rather than what was intended.
	code, body := consoleCall(t, env.router, "POST", wifiRecoveryPath("WIFI-NOLEAK"),
		`{"ssid":"Gym-Guest-5G","password":"hunter2-hunter2","psk":"hunter2-hunter2"}`,
		token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d, want 202 (%v)", code, body)
	}

	secrets := []string{"Gym-Guest-5G", "hunter2-hunter2", "ssid", "password", "psk"}

	// The queued row.
	for _, job := range wifiJobsFor(t, "WIFI-NOLEAK") {
		for _, secret := range secrets {
			if strings.Contains(strings.ToLower(string(job.Payload)), strings.ToLower(secret)) {
				t.Errorf("the queued command carries %q: %s", secret, job.Payload)
			}
		}
	}

	// The audit record's `changes` column, which is the one place on this path
	// that stores caller-influenced JSON at all.
	var changes string
	mustScan(t, `SELECT COALESCE(changes::text, '') FROM audit_events
	              WHERE action = 'TERMINAL_WIFI_RECOVERY_REQUESTED' ORDER BY id DESC LIMIT 1`,
		&changes)
	for _, secret := range secrets {
		if strings.Contains(strings.ToLower(changes), strings.ToLower(secret)) {
			t.Errorf("the audit record carries %q: %s", secret, changes)
		}
	}

	// And the answer the operator's browser got back.
	for _, secret := range secrets {
		if strings.Contains(strings.ToLower(fmt.Sprint(body)), strings.ToLower(secret)) {
			t.Errorf("the response carries %q: %v", secret, body)
		}
	}
}

// TestChangeWifiDestroysNothing.
//
// Recovery is not a factory reset, and the cloud half must not become one by
// accident. The terminal's identity, its credential, its company and site, its
// people and their fingerprint bindings all survive -- asserted directly,
// because "this endpoint only inserts a row" is exactly the kind of claim that
// stops being true when somebody adds a convenience to it.
func TestChangeWifiDestroysNothing(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := onlineTerminal(t, env, "WIFI-KEEPS")
	env.createMember(env.siteAKey, "M-KEEP", "Ada")

	var beforeKey, beforeSite, beforePublic string
	mustScan(t, `SELECT api_key_hash, site_id::text, public_id::text
	               FROM devices WHERE serial_number = 'WIFI-KEEPS'`,
		&beforeKey, &beforeSite, &beforePublic)

	var peopleBefore int
	mustScan(t, `SELECT count(*) FROM people WHERE deleted_at IS NULL`, &peopleBefore)

	_, token, csrf := consoleOperatorSession(t, env.router, one, "keeps-admin@example.com", models.RoleAdmin)
	if code, body := changeWifi(t, env, "WIFI-KEEPS", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d, want 202 (%v)", code, body)
	}

	var afterKey, afterSite, afterPublic string
	mustScan(t, `SELECT api_key_hash, site_id::text, public_id::text
	               FROM devices WHERE serial_number = 'WIFI-KEEPS'`,
		&afterKey, &afterSite, &afterPublic)

	if afterKey != beforeKey {
		t.Error("the device credential changed")
	}
	if afterSite != beforeSite || afterPublic != beforePublic {
		t.Error("the terminal's identity or site changed")
	}

	var peopleAfter int
	mustScan(t, `SELECT count(*) FROM people WHERE deleted_at IS NULL`, &peopleAfter)
	if peopleAfter != peopleBefore {
		t.Errorf("people went from %d to %d", peopleBefore, peopleAfter)
	}

	// THE CREDENTIAL STILL WORKS. The strongest form of "nothing was revoked":
	// the terminal can still authenticate a moment after the command was queued.
	if got := env.heartbeat(key); got != http.StatusOK {
		t.Errorf("heartbeat after Change Wi-Fi = %d, want 200", got)
	}
}

// ---------------------------------------------------------------------------
// Idempotency and the lifecycle the console shows
// ---------------------------------------------------------------------------

// TestChangeWifiIsIdempotentWhileOneIsWaiting.
//
// Pressing the button twice must not queue two commands. The second would be
// redelivered AFTER the customer had re-provisioned the terminal and would wipe
// the Wi-Fi they had just given it -- and, because a wipe puts the terminal back
// offline, could do it again.
func TestChangeWifiIsIdempotentWhileOneIsWaiting(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-TWICE")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "twice-admin@example.com", models.RoleAdmin)

	code, first := changeWifi(t, env, "WIFI-TWICE", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("first Change Wi-Fi = %d, want 202 (%v)", code, first)
	}
	if first["already_queued"] == true {
		t.Error("the first request reported a command already waiting")
	}

	code, second := changeWifi(t, env, "WIFI-TWICE", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("second Change Wi-Fi = %d, want 202 (%v)", code, second)
	}
	if second["already_queued"] != true {
		t.Errorf("the second request did not report the command as already waiting: %v", second)
	}
	if second["request_id"] != first["request_id"] {
		t.Errorf("the second request returned a different command: %v then %v",
			first["request_id"], second["request_id"])
	}

	if jobs := wifiJobsFor(t, "WIFI-TWICE"); len(jobs) != 1 {
		t.Fatalf("%d commands were queued, want exactly 1", len(jobs))
	}

	// AND THE OPERATOR IS STILL AUDITED TWICE. They pressed the button twice,
	// and an audit trail that recorded one would be describing the queue rather
	// than the person.
	var records int
	mustScan(t, `SELECT count(*) FROM audit_events
	              WHERE action = 'TERMINAL_WIFI_RECOVERY_REQUESTED'`, &records)
	if records != 2 {
		t.Errorf("%d audit records for two requests, want 2", records)
	}
}

// TestChangeWifiCanBeSentAgainOnceTheFirstIsDone.
//
// The one-outstanding rule is about PENDING commands. A customer who changes
// router twice in a month is doing nothing wrong, and a completed command must
// not block them for ever.
func TestChangeWifiCanBeSentAgainOnceTheFirstIsDone(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := onlineTerminal(t, env, "WIFI-AGAIN")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "again-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-AGAIN", token, csrf); code != http.StatusAccepted {
		t.Fatalf("first Change Wi-Fi = %d (%v)", code, body)
	}

	jobID := ackWifiRecovery(t, env, key, "WIFI-AGAIN")

	code, again := changeWifi(t, env, "WIFI-AGAIN", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("second Change Wi-Fi after completion = %d, want 202 (%v)", code, again)
	}
	if again["already_queued"] == true {
		t.Error("a completed command was reported as still waiting")
	}

	jobs := wifiJobsFor(t, "WIFI-AGAIN")
	if len(jobs) != 2 {
		t.Fatalf("%d commands exist, want 2", len(jobs))
	}
	if jobs[0].ID != jobID || jobs[0].Status != "COMPLETED" {
		t.Errorf("the first command is %s, want COMPLETED", jobs[0].Status)
	}
	if jobs[1].Status != "PENDING" {
		t.Errorf("the second command is %s, want PENDING", jobs[1].Status)
	}
}

// ackWifiRecovery acknowledges a terminal's outstanding command over the device
// credential, the way the firmware does, and returns the job id it retired.
//
// The id is read from the database rather than by polling again, because a
// second poll would NOT return it: fetching takes a sixty-second delivery lease
// and the terminal is expected to acknowledge from the id it already holds. A
// helper that re-polled would only work for a job nobody had collected yet,
// which is the opposite of the case it is used in.
func ackWifiRecovery(t *testing.T, env *testEnv, deviceKey, serial string) int64 {
	t.Helper()

	var jobID int64
	if err := database.DB.QueryRow(`
		SELECT j.id FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.job_type = 'WIFI_RECOVERY'
		   AND j.status = 'PENDING'
		 ORDER BY j.id DESC LIMIT 1`, serial).Scan(&jobID); err != nil {
		t.Fatalf("%s has no outstanding command to acknowledge: %v", serial, err)
	}

	// An empty body means COMPLETED, which is what constrained firmware sends.
	res := env.do(http.MethodPost, jobPath(jobID), map[string]any{}, deviceAuth(deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("acknowledging the command = %d, want 200 (%s)", res.Code, res.Raw)
	}
	return jobID
}

// TestChangeWifiStatusTracksTheTerminalAndNotTheConsole.
//
// THE PROMISE THE SCREEN MAKES. QUEUED, DELIVERED and ACCEPTED are three
// different facts, and the console must not report the last one until the DEVICE
// has said so. Reporting "changed" on the strength of having queued something is
// the single most misleading thing this feature could do.
func TestChangeWifiStatusTracksTheTerminalAndNotTheConsole(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := onlineTerminal(t, env, "WIFI-STATES")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "states-admin@example.com", models.RoleAdmin)

	// Nothing has ever been sent.
	if got := wifiRecoveryState(t, env, "WIFI-STATES", token); got["state"] != models.WifiRecoveryNone {
		t.Errorf("state before any request = %v, want %s", got["state"], models.WifiRecoveryNone)
	}

	if code, body := changeWifi(t, env, "WIFI-STATES", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d (%v)", code, body)
	}

	// Queued, and NOT accepted. Waiting for terminal…
	queued := wifiRecoveryState(t, env, "WIFI-STATES", token)
	if queued["state"] != models.WifiRecoveryQueued {
		t.Errorf("state after queueing = %v, want %s", queued["state"], models.WifiRecoveryQueued)
	}
	if queued["acknowledged_at"] != nil {
		t.Error("a queued command reports an acknowledgement time")
	}
	if queued["online"] != true {
		t.Errorf("online = %v, want true for a terminal that just registered", queued["online"])
	}

	// The terminal collects it. Still not done: fetching takes a lease and
	// changes no status, so this is evidence it HAS the command and nothing more.
	env.jobs(key)
	delivered := wifiRecoveryState(t, env, "WIFI-STATES", token)
	if delivered["state"] != models.WifiRecoveryDelivered {
		t.Errorf("state after the terminal collected it = %v, want %s",
			delivered["state"], models.WifiRecoveryDelivered)
	}
	if delivered["acknowledged_at"] != nil {
		t.Error("a collected command reports an acknowledgement time")
	}

	// The terminal acknowledges. ONLY NOW.
	ackWifiRecovery(t, env, key, "WIFI-STATES")
	accepted := wifiRecoveryState(t, env, "WIFI-STATES", token)
	if accepted["state"] != models.WifiRecoveryAccepted {
		t.Errorf("state after acknowledgement = %v, want %s",
			accepted["state"], models.WifiRecoveryAccepted)
	}
	if accepted["acknowledged_at"] == nil {
		t.Error("an accepted command reports no acknowledgement time")
	}
}

// TestALapsedChangeWifiCommandIsNeverDelivered.
//
// The hazard the firmware's own commit names: a command that sat in the queue
// while the customer recovered the terminal by hand arrives after they have
// re-provisioned it and wipes the Wi-Fi they just typed in. Every other job type
// describes state and may safely arrive late; this one describes an act.
func TestALapsedChangeWifiCommandIsNeverDelivered(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := onlineTerminal(t, env, "WIFI-LAPSED")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "lapsed-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-LAPSED", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d (%v)", code, body)
	}

	// Age it past the validity window. The terminal was unreachable for the
	// whole of it -- which is the situation, not a contrivance.
	mustExec(t, fmt.Sprintf(`
		UPDATE sync_jobs SET created_at = CURRENT_TIMESTAMP - INTERVAL '%d seconds'
		 WHERE job_type = 'WIFI_RECOVERY'`, models.WifiRecoveryValiditySeconds+60))

	for _, job := range env.jobs(key) {
		if job["job_type"] == models.WifiRecoveryJobType {
			t.Fatal("a lapsed command was handed to the terminal")
		}
	}

	// And the console says so rather than saying "waiting for terminal" for ever.
	if got := wifiRecoveryState(t, env, "WIFI-LAPSED", token); got["state"] != models.WifiRecoveryExpired {
		t.Errorf("state of a lapsed command = %v, want %s", got["state"], models.WifiRecoveryExpired)
	}

	// A fresh request retires the lapsed row and queues a new one, so the
	// one-outstanding index does not become a permanent block.
	code, again := changeWifi(t, env, "WIFI-LAPSED", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi after a lapse = %d, want 202 (%v)", code, again)
	}
	if again["already_queued"] == true {
		t.Error("a lapsed command was reported as still waiting")
	}

	jobs := wifiJobsFor(t, "WIFI-LAPSED")
	if len(jobs) != 2 {
		t.Fatalf("%d commands exist, want 2", len(jobs))
	}
	if jobs[0].Status != "CANCELLED" {
		t.Errorf("the lapsed command is %s, want CANCELLED", jobs[0].Status)
	}
}

// TestAResyncDoesNotCancelAWaitingChangeWifiCommand.
//
// Compaction replaces STATE with a snapshot. A command is not state -- it is an
// instruction somebody typed -- and cancelling it because the terminal happened
// to cross the compaction threshold would make an operator's Change Wi-Fi vanish
// with the console still showing "waiting for terminal".
func TestAResyncDoesNotCancelAWaitingChangeWifiCommand(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-RESYNC")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "resync-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-RESYNC", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d (%v)", code, body)
	}

	if code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/WIFI-RESYNC/resync", "", token, csrf); code != http.StatusOK {
		t.Fatalf("resync = %d (%v)", code, body)
	}

	jobs := wifiJobsFor(t, "WIFI-RESYNC")
	if len(jobs) != 1 || jobs[0].Status != "PENDING" {
		t.Fatalf("the command is %v after a resync, want one still PENDING", jobs)
	}
	if got := wifiRecoveryState(t, env, "WIFI-RESYNC", token); got["state"] != models.WifiRecoveryQueued {
		t.Errorf("state after a resync = %v, want %s", got["state"], models.WifiRecoveryQueued)
	}
}

// TestRevokingATerminalCancelsItsWaitingChangeWifiCommand, which is the other
// half of the rule above.
//
// A revoked terminal will never apply anything queued for it, and a command left
// PENDING against hardware the operator has declared stolen would be delivered
// if it were ever re-provisioned.
func TestRevokingATerminalCancelsItsWaitingChangeWifiCommand(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-REVOKED")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "revoked-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-REVOKED", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d (%v)", code, body)
	}
	if code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/WIFI-REVOKED/revoke", `{"reason":"stolen"}`,
		token, csrf); code != http.StatusOK {
		t.Fatalf("revoke = %d (%v)", code, body)
	}

	jobs := wifiJobsFor(t, "WIFI-REVOKED")
	if len(jobs) != 1 || jobs[0].Status != "CANCELLED" {
		t.Fatalf("the command is %v after revocation, want CANCELLED", jobs)
	}
	if got := wifiRecoveryState(t, env, "WIFI-REVOKED", token); got["state"] != models.WifiRecoveryCancelled {
		t.Errorf("state after revocation = %v, want %s", got["state"], models.WifiRecoveryCancelled)
	}
}

// ---------------------------------------------------------------------------
// The audit trail
// ---------------------------------------------------------------------------

// TestChangeWifiIsAudited.
//
// SEC-07: an operator action nobody can attribute afterwards. This one takes a
// door out of service until somebody visits it, so "who asked for this" is the
// first question anybody will have.
func TestChangeWifiIsAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-AUDIT")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "audit-admin@example.com", models.RoleAdmin)

	if code, body := changeWifi(t, env, "WIFI-AUDIT", token, csrf); code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d (%v)", code, body)
	}

	var action, actorEmail, actorRole, targetType, targetLabel string
	mustScan(t, `SELECT action, actor_email, actor_role, target_type, target_label
	               FROM audit_events ORDER BY id DESC LIMIT 1`,
		&action, &actorEmail, &actorRole, &targetType, &targetLabel)

	if action != "TERMINAL_WIFI_RECOVERY_REQUESTED" {
		t.Errorf("action = %q, want TERMINAL_WIFI_RECOVERY_REQUESTED", action)
	}
	if actorEmail != "audit-admin@example.com" || actorRole != models.RoleAdmin {
		t.Errorf("actor = %q/%q, want the administrator who asked", actorEmail, actorRole)
	}
	if targetType != "TERMINAL" || targetLabel != "WIFI-AUDIT" {
		t.Errorf("target = %s/%s, want TERMINAL/WIFI-AUDIT", targetType, targetLabel)
	}

	// And an administrator can read it back through the console, which is where
	// somebody actually asks the question.
	_, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/audit?action=TERMINAL_WIFI_RECOVERY_REQUESTED", "", token, "")
	entries := listOf(t, body, "entries")
	if len(entries) != 1 {
		t.Fatalf("the audit trail shows %d Change Wi-Fi requests, want 1", len(entries))
	}
}

// TestARefusedChangeWifiIsNotAudited.
//
// The trail records what HAPPENED. A refusal queued nothing and changed nothing,
// and recording it as an action would make the trail read as though a door had
// been put into setup mode when it had not -- an audit trail full of attempted
// actions is worse than one a developer has to remember, because it looks
// complete.
func TestARefusedChangeWifiIsNotAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	onlineTerminal(t, env, "WIFI-NOAUDIT")
	mustExec(t, `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = 'WIFI-NOAUDIT'`)

	_, token, csrf := consoleOperatorSession(t, env.router, one, "noaudit-admin@example.com", models.RoleAdmin)
	if code, _ := changeWifi(t, env, "WIFI-NOAUDIT", token, csrf); code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on an offline terminal = %d, want 409", code)
	}

	var records int
	mustScan(t, `SELECT count(*) FROM audit_events
	              WHERE action = 'TERMINAL_WIFI_RECOVERY_REQUESTED'`, &records)
	if records != 0 {
		t.Errorf("%d audit records for a refused request, want 0", records)
	}
}
