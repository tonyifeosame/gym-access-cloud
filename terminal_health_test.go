package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// M-7: the terminal health projection nothing mounted.
//
// THE DEFECT THESE WOULD HAVE CAUGHT. `database.GetTerminalHealth` and
// `models.ConsoleTerminalHealth` both existed, both were fully written, and no
// route in router.go produced either -- so `pending_jobs`, `failed_jobs`,
// `last_apply_error` and `credential_active` were unreachable from every
// operator surface. SYN-04 was recorded PARTIAL on the strength of counters an
// operator could not see, and a terminal quietly failing to apply a roster
// looked exactly like a healthy one.
//
// The counters were the whole remediation for "table-full is a silent permanent
// failure". Unreachable, they are the same silence with more columns.

// terminalHealth reads the health object off the terminal detail response.
//
// Read through the API rather than from the store, because the finding is that
// the store's answer never reached a caller. A test that called
// GetTerminalHealth directly would have passed throughout the period the bug
// existed -- and one does exist in terminal_lifecycle_test.go, which is exactly
// how this went unnoticed.
func terminalHealth(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	health, ok := body["health"].(map[string]any)
	if !ok {
		t.Fatalf("the terminal detail carried no `health` object: %v", body)
	}
	return health
}

// TestTerminalDetailCarriesHealth is the fix, stated as the operator's question:
// is this terminal behind, is it failing, and can it still authenticate.
func TestTerminalDetailCarriesHealth(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one,
		"health-read@example.com", models.RoleViewer)

	env.registerDevice(env.siteAKey, "ESP32-HEALTHY")

	code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-HEALTHY", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading a terminal in the operator's own company = %d, want 200 (%v)",
			code, body)
	}

	health := terminalHealth(t, body)

	// Every field the console needs, present rather than merely defined. A
	// missing key and a zero value are indistinguishable to a browser, so the
	// keys are asserted separately from the values below.
	for _, field := range []string{
		"pending_jobs", "failed_jobs", "credential_active",
		"offline_policy", "offline_grace_minutes",
	} {
		if _, present := health[field]; !present {
			t.Errorf("health is missing %q (%v)", field, health)
		}
	}

	// A freshly registered terminal HAS a credential, and that is the value
	// that lets the console agree with the door about whether it can still
	// authenticate.
	if health["credential_active"] != true {
		t.Errorf("credential_active = %v for a terminal that just registered",
			health["credential_active"])
	}

	// VIEWER is enough. Health is a read, and an operator who can see a
	// terminal at all needs to be able to see whether it is working.
}

// TestTerminalHealthIsPopulatedFromTheDatabase.
//
// The counters are maintained by trigger from real sync_jobs, and
// last_apply_error is written by the path that records an apply failure. Both
// are driven here through the machinery that maintains them rather than by
// setting the columns, so the test would fail if the projection read the right
// columns from the wrong row -- or if it returned a zero-valued struct, which is
// what an unmounted projection looks like.
func TestTerminalHealthIsPopulatedFromTheDatabase(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one,
		"health-values@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-BEHIND")

	// Real queued work: three people fan out to the terminal as CREATE jobs.
	for _, id := range []string{"M-1", "M-2", "M-3"} {
		env.createMember(env.siteAKey, id, "Person "+id)
	}

	deviceID := deviceIDBySerial(t, "ESP32-BEHIND")
	if err := database.RefreshTerminalJobCounters(deviceID); err != nil {
		t.Fatalf("refreshing counters: %v", err)
	}
	// The terminal's own words about why it is stuck, through the function that
	// records them.
	if err := database.RecordTerminalApplyFailure(deviceID, "member table full"); err != nil {
		t.Fatalf("recording an apply failure: %v", err)
	}

	code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-BEHIND", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 (%v)", code, body)
	}

	health := terminalHealth(t, body)

	pending := queryInt(t, `SELECT pending_job_count FROM devices WHERE id = $1`, deviceID)
	if pending == 0 {
		t.Fatal("fixture queued no work, so this test would prove nothing")
	}
	if got := health["pending_jobs"]; got != float64(pending) {
		t.Errorf("pending_jobs = %v, want %d (the value in the database)", got, pending)
	}

	// The message, not a boolean. "member table full" is what makes the console
	// actionable; "failed" would not be.
	if got, _ := health["last_apply_error"].(string); got != "member table full" {
		t.Errorf("last_apply_error = %q, want the terminal's own words", got)
	}
	if _, present := health["last_apply_error_at"]; !present {
		t.Error("last_apply_error_at is absent, so an error from four months ago " +
			"and one from four minutes ago read identically")
	}

	// And the credential state follows the credential, not a status column.
	// Revoking clears the key hash, which is what authentication probes.
	if _, err := database.RevokeTerminalCredential(one, "ESP32-BEHIND",
		"reported stolen", 0); err != nil {
		t.Fatalf("revoking: %v", err)
	}

	code, body = consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-BEHIND", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading a revoked terminal = %d, want 200 (%v)", code, body)
	}
	if got := terminalHealth(t, body)["credential_active"]; got != false {
		t.Errorf("credential_active = %v after revocation, want false", got)
	}
}

// TestTerminalHealthStaysTenantScoped.
//
// The health read must not become a way around the scoping the detail endpoint
// already has. It is the SAME endpoint, so the answer must be the existing one:
// another tenant's terminal is not found, and is indistinguishable from a serial
// that does not exist.
func TestTerminalHealthStaysTenantScoped(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site C", "OTHER-HEALTH-1") // company two

	_, token, _ := consoleOperatorSession(t, env.router, one,
		"health-scope@example.com", models.RoleOwner)

	for _, serial := range []string{"OTHER-HEALTH-1", "NO-SUCH-TERMINAL"} {
		code, body := consoleCall(t, env.router, http.MethodGet,
			"/api/v1/console/terminals/"+serial, "", token, "")
		if code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (%v)", serial, code, body)
		}
		// Not merely refused -- nothing about the other tenant's terminal comes
		// back in the body either.
		if _, present := body["health"]; present {
			t.Errorf("the 404 for %s still carried a health object: %v", serial, body)
		}
	}
}

// TestTerminalHealthExposesNoCredentials.
//
// The whole point of `credential_active` is that it is a BOOLEAN. A browser is
// told whether a terminal can authenticate and never with what -- and the site's
// provisioning key, which registers every terminal at that site, must not appear
// on a read that any VIEWER can make.
func TestTerminalHealthExposesNoCredentials(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one,
		"health-secrets@example.com", models.RoleViewer)

	deviceKey := env.registerDevice(env.siteAKey, "ESP32-SECRETS")

	code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-SECRETS", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 (%v)", code, body)
	}

	// Asserted against the WHOLE serialised response rather than field by
	// field: a credential added to any nested object later would slip past a
	// list of known keys, and this is the check that has to keep holding.
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("re-encoding the response: %v", err)
	}
	raw := string(encoded)

	if strings.Contains(raw, deviceKey) {
		t.Error("the terminal detail carried the device's own credential")
	}
	if strings.Contains(raw, env.siteAKey) {
		t.Error("the terminal detail carried the SITE PROVISIONING KEY, which " +
			"registers every terminal at that site")
	}
	for _, forbidden := range []string{"api_key", "api_key_hash", "atd_", "ats_"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the terminal detail mentions %q: %s", forbidden, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// M-4: the recovery instruction
// ---------------------------------------------------------------------------

// TestRevokeTellsTheOperatorHowToRecover.
//
// THE DEFECT THIS WOULD HAVE CAUGHT. The response said "This terminal must
// re-register with the site provisioning key before it can authenticate again",
// and the console appends that VERBATIM to what an operator sees. It was wrong
// twice: revocation also sets the terminal DISABLED, and both registration and
// claim redemption refuse a disabled terminal -- so following it produced a 403
// -- and it named the credential that enrols every terminal at the site when a
// single-use claim code is the supported route.
func TestRevokeTellsTheOperatorHowToRecover(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"recovery@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-RECOVER")

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/terminals/ESP32-RECOVER/revoke",
		`{"reason":"reported stolen"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200 (%v)", code, body)
	}

	recovery, _ := body["recovery"].(string)
	if recovery == "" {
		t.Fatal("the revoke response carried no recovery instruction")
	}

	// It is the constant, so there is one answer rather than one per surface.
	if recovery != models.TerminalRecoveryInstruction {
		t.Errorf("recovery = %q, want models.TerminalRecoveryInstruction", recovery)
	}

	lower := strings.ToLower(recovery)

	// THE STEP THAT WAS MISSING, and the reason the old instruction failed.
	if !strings.Contains(lower, "re-enable") {
		t.Errorf("recovery %q does not tell the operator to re-enable the terminal, "+
			"which provisioning refuses to proceed without", recovery)
	}
	if !strings.Contains(lower, "claim code") {
		t.Errorf("recovery %q does not name the claim code, which is the supported "+
			"way to give one terminal a fresh credential", recovery)
	}
}

// TestRecoveryInstructionCannotReturnToTheSiteKey.
//
// A regression guard on the WORDING, which is unusual and is deliberate: this
// string is an instruction an operator follows while looking at hardware they
// have just disabled, and the previous one sent them to a 403 by way of the most
// dangerous credential in the system. Asserted against the constant so it holds
// wherever the constant is used.
func TestRecoveryInstructionCannotReturnToTheSiteKey(t *testing.T) {
	lower := strings.ToLower(models.TerminalRecoveryInstruction)

	for _, banned := range []string{
		"must re-register with the site provisioning key",
		"re-register with the site",
	} {
		if strings.Contains(lower, banned) {
			t.Errorf("the recovery instruction has returned to the site-key wording: %q",
				models.TerminalRecoveryInstruction)
		}
	}

	// The site key may be MENTIONED -- the corrected text says it is not needed
	// -- so the guard is on instructing its use, not on the phrase existing.
	if strings.Contains(lower, "site provisioning key") &&
		!strings.Contains(lower, "not needed") {
		t.Errorf("the recovery instruction mentions the site provisioning key "+
			"without saying it is not needed: %q", models.TerminalRecoveryInstruction)
	}
}

// TestRecoveryPathActuallyWorks is the instruction executed as a test.
//
// A message that reads well and does not work is the failure being fixed, so
// the sequence it describes is performed end to end: revoke, then re-enable,
// then claim. The claim BEFORE re-enabling is attempted first and must fail --
// otherwise the "re-enable first" step this instruction adds would be advice
// nobody needs.
func TestRecoveryPathActuallyWorks(t *testing.T) {
	cheapBcrypt(t)
	f := newClaimFixture(t)
	env := f.env

	env.registerDevice(env.siteAKey, "AT-RECOVER")

	if code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/terminals/AT-RECOVER/revoke", `{"reason":"stolen"}`,
		f.token, f.csrf); code != http.StatusOK {
		t.Fatalf("revoke = %d (%v)", code, body)
	}

	// STEP SKIPPED ON PURPOSE. Claiming a terminal that is still disabled must
	// be refused, which is what makes "re-enable first" a required step rather
	// than a courtesy -- and it is the assertion that stops this whole test
	// from becoming a way to prove revocation can be walked around.
	skipped := issueClaimCode(t, env, f.token, f.csrf, "Site A", "AT-RECOVER")
	res := f.claim(t, skipped, "AT-RECOVER")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("claiming a disabled terminal = %d, want 401 -- revocation would "+
			"not be holding (%s)", res.Code, res.Raw)
	}
	if key, _ := res.Body["api_key"].(string); key != "" {
		t.Fatal("a refused claim still handed out a credential")
	}
	// And the refusal left the row alone rather than half-applying.
	if queryBool(t, `SELECT api_key_hash IS NOT NULL FROM devices
	                  WHERE serial_number = 'AT-RECOVER'`) {
		t.Error("a refused claim wrote a credential hash onto a revoked terminal")
	}

	// Step one: re-enable.
	if code, body := consoleCall(t, env.router, http.MethodPut,
		"/api/v1/console/terminals/AT-RECOVER/state", `{"disabled":false}`,
		f.token, f.csrf); code != http.StatusOK {
		t.Fatalf("re-enable = %d (%v)", code, body)
	}

	// Step two: a fresh single-use code, redeemed at the unit. The site
	// provisioning key is never presented.
	code := issueClaimCode(t, env, f.token, f.csrf, "Site A", "AT-RECOVER")
	res = f.claim(t, code, "AT-RECOVER")
	if res.Code != http.StatusOK {
		t.Fatalf("claiming after re-enabling = %d, want 200 -- the recovery "+
			"instruction describes a path that does not work (%s)", res.Code, res.Raw)
	}

	newKey, _ := res.Body["api_key"].(string)
	if newKey == "" {
		t.Fatal("recovery produced no credential")
	}
	if poll := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		deviceAuth(newKey)); poll.Code != http.StatusOK {
		t.Errorf("the recovered terminal cannot authenticate: %d (%s)",
			poll.Code, poll.Raw)
	}

	// And the console agrees that it is credentialed again.
	_, detail := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/AT-RECOVER", "", f.token, "")
	if got := terminalHealth(t, detail)["credential_active"]; got != true {
		t.Errorf("credential_active = %v after recovery, want true", got)
	}
}
