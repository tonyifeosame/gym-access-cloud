package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// What a terminal says it can do, and what the platform does with that (025).
//
// ---------------------------------------------------------------------------
// THE PROBLEM THIS COLUMN EXISTS FOR
// ---------------------------------------------------------------------------
//
// Firmware that predates a job type parses it as kUnknown and ACKNOWLEDGES it
// as applied -- deliberately, so that a newer server's job types are not
// redelivered for ever. That single decision is what makes an additive job type
// safe to serve to an old fleet, and it is also what makes an old unit's
// acknowledgement indistinguishable from a new one's.
//
// The platform could not tell them apart by version either: DEVICE_FIRMWARE_
// VERSION defaults to "1.0.0" and the build flag that would override it was
// commented out, so every image ever produced reported the same string. So the
// console queued Change Wi-Fi for terminals that could not carry it out, the
// terminals acknowledged it, and the console reported that the door had
// confirmed the request while a customer stood in front of it waiting for a
// setup network that was never going to appear.
//
// These tests are that failure, written as the thing that must not be possible.

// capabilitiesOf reads what the platform has stored for one serial.
//
// Returns nil for a terminal that has never reported, which is NOT the same as
// one that reported an empty list -- the whole column turns on that difference,
// so the helper preserves it rather than flattening both to an empty slice.
func capabilitiesOf(t *testing.T, serial string) []string {
	t.Helper()

	var raw []byte
	err := database.DB.QueryRow(
		`SELECT capabilities FROM devices WHERE serial_number = $1`, serial).Scan(&raw)
	if err != nil {
		t.Fatalf("reading capabilities for %s: %v", serial, err)
	}
	if len(raw) == 0 {
		return nil
	}

	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding capabilities for %s: %v", serial, err)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

func TestATerminalThatHasNeverReportedIsNotTakenToHaveNone(t *testing.T) {
	env := newTestEnv(t)

	env.registerDevice(env.siteAKey, "AT-CAP-001")

	// NIL, not empty. Every unit in the field today is here, and so is every
	// brand-new one until its first heartbeat -- and those two deserve different
	// words from a console, which they cannot get if the platform has already
	// collapsed them into "has none".
	if got := capabilitiesOf(t, "AT-CAP-001"); got != nil {
		t.Fatalf("a terminal that has never reported has capabilities %v, want nil", got)
	}
}

func TestAHeartbeatRecordsWhatTheTerminalCanDo(t *testing.T) {
	env := newTestEnv(t)

	key := env.registerDevice(env.siteAKey, "AT-CAP-002")
	reportCapabilities(t, env, key,
		models.CapabilityWifiProvisioning, models.CapabilityWifiRecovery)

	got := capabilitiesOf(t, "AT-CAP-002")
	if len(got) != 2 {
		t.Fatalf("stored capabilities = %v, want two", got)
	}
	if !database.DeviceHasCapability(got, models.CapabilityWifiRecovery) {
		t.Fatalf("stored capabilities %v do not include wifi_recovery", got)
	}
}

func TestAHeartbeatWithoutCapabilitiesDoesNotEraseThem(t *testing.T) {
	env := newTestEnv(t)

	key := env.registerDevice(env.siteAKey, "AT-CAP-003")
	reportCapabilities(t, env, key, models.CapabilityWifiRecovery)

	// THE MERGE RULE, and the reason it is COALESCE rather than a plain
	// assignment. A build that does not report the field must not switch a
	// gated feature off for that door -- otherwise one heartbeat from an older
	// image, or one request that dropped the field, would silently take Change
	// Wi-Fi away from a terminal that can perfectly well do it.
	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat",
		map[string]any{"status": "ONLINE"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("plain heartbeat: got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	got := capabilitiesOf(t, "AT-CAP-003")
	if !database.DeviceHasCapability(got, models.CapabilityWifiRecovery) {
		t.Fatalf("a heartbeat with no capability field erased them: %v", got)
	}
}

func TestATerminalCanReportThatItHasNoCapabilities(t *testing.T) {
	env := newTestEnv(t)

	key := env.registerDevice(env.siteAKey, "AT-CAP-004")
	reportCapabilities(t, env, key, models.CapabilityWifiRecovery)

	// AN EMPTY LIST IS A REAL ANSWER and replaces what was stored: "I report my
	// capabilities, and I have none of them". That is what a downgrade looks
	// like, and it has to be able to take a capability away -- otherwise the
	// merge rule above would make every capability permanent once claimed.
	reportCapabilities(t, env, key)

	got := capabilitiesOf(t, "AT-CAP-004")
	if got == nil {
		t.Fatal("an explicit empty list read back as never-reported")
	}
	if len(got) != 0 {
		t.Fatalf("an explicit empty list read back as %v", got)
	}
}

func TestAGarbageCapabilityListCannotFailAHeartbeat(t *testing.T) {
	env := newTestEnv(t)

	key := env.registerDevice(env.siteAKey, "AT-CAP-005")

	// Blanks and duplicates are dropped rather than stored. They cannot come
	// from the firmware's compile-time table, but this arrives over the wire and
	// a list with three copies of one token is a list the console renders three
	// times.
	//
	// WHAT MUST NOT HAPPEN is the heartbeat failing: a terminal that cannot beat
	// reads on the console as one that has gone offline, and a door being drawn
	// as dead because of a cosmetic field would be the worst possible trade.
	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{
		"status": "ONLINE",
		"capabilities": []string{
			"wifi_recovery", "wifi_recovery", "", "   ", "wifi_provisioning",
		},
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat with a messy list: got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	got := capabilitiesOf(t, "AT-CAP-005")
	if len(got) != 2 {
		t.Fatalf("stored capabilities = %v, want the two distinct tokens", got)
	}
}

func TestAnUnknownCapabilityTokenIsKeptRatherThanRefused(t *testing.T) {
	env := newTestEnv(t)

	key := env.registerDevice(env.siteAKey, "AT-CAP-006")

	// The vocabulary is the FIRMWARE's, and it grows there first. A server that
	// refused a token it had not heard of would mean every new firmware
	// capability needed a platform release before any terminal could report it
	// -- which is the coupling the whole additive design exists to avoid.
	reportCapabilities(t, env, key, models.CapabilityWifiRecovery, "template_sealing")

	got := capabilitiesOf(t, "AT-CAP-006")
	if !database.DeviceHasCapability(got, "template_sealing") {
		t.Fatalf("an unrecognised token was dropped: %v", got)
	}
}

// ---------------------------------------------------------------------------
// The gate
// ---------------------------------------------------------------------------

func TestChangeWifiIsRefusedForATerminalThatCannotDoIt(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)

	key := env.registerDevice(env.siteAKey, "AT-CAP-010")
	reportCapabilities(t, env, key, models.CapabilityWifiProvisioning)

	code, body := changeWifi(t, env, "AT-CAP-010", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on an incapable terminal: got %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.WifiRecoveryTerminalIncapable {
		t.Fatalf("refusal code = %v, want %s", body["code"],
			models.WifiRecoveryTerminalIncapable)
	}

	// AND NOTHING WAS QUEUED. This is the whole point: the old behaviour queued
	// the job, the terminal acknowledged a type it did not recognise, and the
	// console reported that the door had confirmed the request.
	if jobs := wifiJobsFor(t, "AT-CAP-010"); len(jobs) != 0 {
		t.Fatalf("a command was queued for a terminal that cannot carry it out: %v", jobs)
	}
}

func TestChangeWifiIsRefusedForATerminalThatHasNeverReported(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)

	// Registered, online, credentialled -- and silent about what it can do,
	// which is the entire fleet in the field. SILENCE IS NOT CONSENT: assuming
	// capability is exactly what produced the false ACCEPTED this refusal
	// replaced.
	env.registerDevice(env.siteAKey, "AT-CAP-011")

	code, body := changeWifi(t, env, "AT-CAP-011", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("Change Wi-Fi on a silent terminal: got %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.WifiRecoveryTerminalIncapable {
		t.Fatalf("refusal code = %v, want %s", body["code"],
			models.WifiRecoveryTerminalIncapable)
	}
	if jobs := wifiJobsFor(t, "AT-CAP-011"); len(jobs) != 0 {
		t.Fatalf("a command was queued for a terminal that never reported: %v", jobs)
	}
}

func TestTheTwoWaysOfNotBeingCapableReadDifferently(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "AT-CAP-012")
	_, silent := changeWifi(t, env, "AT-CAP-012", token, csrf)

	key := env.registerDevice(env.siteAKey, "AT-CAP-013")
	reportCapabilities(t, env, key, models.CapabilityWifiProvisioning)
	_, reported := changeWifi(t, env, "AT-CAP-013", token, csrf)

	// SAME CODE -- a client branches on that and the remedy is the same firmware
	// update either way -- and DIFFERENT PROSE, because "it told us it cannot"
	// and "it has never told us anything" send an operator to different places:
	// one to the firmware catalogue, the other to check whether the unit has
	// checked in since it was updated.
	if silent["code"] != reported["code"] {
		t.Fatalf("the two incapable cases carry different codes: %v vs %v",
			silent["code"], reported["code"])
	}
	if silent["error"] == reported["error"] {
		t.Fatalf("the two incapable cases are indistinguishable to an operator: %v",
			silent["error"])
	}
}

func TestChangeWifiStillWorksForATerminalThatSaysItCan(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)
	onlineTerminal(t, env, "AT-CAP-014")

	code, body := changeWifi(t, env, "AT-CAP-014", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi on a capable terminal: got %d, want 202 (%v)", code, body)
	}

	jobs := wifiJobsFor(t, "AT-CAP-014")
	if len(jobs) != 1 {
		t.Fatalf("queued %d commands, want exactly one", len(jobs))
	}
}

func TestCapabilityIsReportedBeforeOfflineBecauseTheRemedyIsDifferent(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)

	// Neither capable NOR online. Both are true; only one of them is worth
	// telling somebody, because bringing this terminal back online would leave
	// them exactly where they started.
	env.registerDevice(env.siteAKey, "AT-CAP-015")
	if _, err := database.DB.Exec(
		`UPDATE devices SET status = 'OFFLINE' WHERE serial_number = $1`,
		"AT-CAP-015"); err != nil {
		t.Fatalf("taking the terminal offline: %v", err)
	}

	_, body := changeWifi(t, env, "AT-CAP-015", token, csrf)
	if body["code"] != models.WifiRecoveryTerminalIncapable {
		t.Fatalf("refusal code = %v, want the capability refusal rather than offline",
			body["code"])
	}
}

func TestADisabledTerminalIsStillNamedAsDisabled(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-admin@example.com", models.RoleAdmin)

	// The capability check must not have jumped the two administrative refusals
	// ahead of it. Those are one click away in this console; a firmware version
	// is not, and telling somebody to update firmware when the real problem is
	// a switch they can flick here would be the ordering inverted.
	env.registerDevice(env.siteAKey, "AT-CAP-016")
	if _, err := database.DB.Exec(
		`UPDATE devices SET status = 'DISABLED', active = FALSE WHERE serial_number = $1`,
		"AT-CAP-016"); err != nil {
		t.Fatalf("disabling the terminal: %v", err)
	}

	_, body := changeWifi(t, env, "AT-CAP-016", token, csrf)
	if body["code"] != models.WifiRecoveryTerminalDisabled {
		t.Fatalf("refusal code = %v, want the disabled refusal", body["code"])
	}
}

// ---------------------------------------------------------------------------
// What the console reads
// ---------------------------------------------------------------------------

func TestTheConsoleSeesWhatATerminalCanDo(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, _ := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-manager@example.com", models.RoleManager)
	key := env.registerDevice(env.siteAKey, "AT-CAP-020")
	reportCapabilities(t, env, key, models.CapabilityWifiRecovery)

	code, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/AT-CAP-020", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the terminal: got %d, want 200 (%v)", code, body)
	}

	list, ok := body["capabilities"].([]any)
	if !ok {
		t.Fatalf("the terminal read carries no capability list: %v", body)
	}
	if len(list) != 1 || list[0] != models.CapabilityWifiRecovery {
		t.Fatalf("capabilities = %v, want [wifi_recovery]", list)
	}
}

func TestATerminalThatHasNeverReportedOmitsTheListEntirely(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, _ := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "caps-manager@example.com", models.RoleManager)
	env.registerDevice(env.siteAKey, "AT-CAP-021")

	code, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/AT-CAP-021", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the terminal: got %d, want 200 (%v)", code, body)
	}

	// OMITTED, not []. A console that received an empty array would be entitled
	// to say "this terminal can do nothing", which is a claim the platform
	// cannot support about a unit it has never heard from.
	if _, present := body["capabilities"]; present {
		t.Fatalf("a terminal that never reported carries a capability list: %v", body)
	}
}
