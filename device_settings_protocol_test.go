package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// The protocol contract with the firmware.
//
// Every assertion here is against docs/firmware-protocol-requirements.md in the
// FIRMWARE repository, which is the contract of record between the two sides.
// The field names, the enum spellings and the shapes are the ones the terminal
// actually parses -- they were read out of the firmware source
// (sync_job.cpp, heartbeat.cpp, provisioning.cpp, credential_ref.h), not
// invented here, and a test that asserted anything else would be asserting
// against a document rather than against the device.

// settingsObject pulls the nested `settings` object out of a device-facing
// payload, which is where the firmware's parseSettingsPayload looks.
func settingsObject(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	raw, present := body["settings"]
	if !present {
		t.Fatalf("payload carries no `settings` object: %v", body)
	}
	settings, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("`settings` is %T, want an object: %v", raw, body)
	}
	return settings
}

// ---------------------------------------------------------------------------
// §1 The site's offline policy must reach the device -- BLOCKING
// ---------------------------------------------------------------------------

// TestOfflinePolicyReachesTheDevice is the blocking finding, stated as a test.
//
// sites.offline_policy has existed since 016 and nothing ever put it into the
// object a terminal receives, so EVERY TERMINAL IN THE FIELD behaved as
// CACHED_INDEFINITE regardless of what the site had chosen -- while the console
// reported the policy it was not enforcing.
func TestOfflinePolicyReachesTheDevice(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-POLICY")

	res := env.do("GET", "/api/v1/devices/settings", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /devices/settings = %d: %s", res.Code, res.Raw)
	}

	settings := settingsObject(t, res.Body)

	policy, present := settings[models.SettingsKeyOfflinePolicy]
	if !present {
		t.Fatalf("the settings object carries no %q; every terminal would run "+
			"CACHED_INDEFINITE no matter what the site chose: %v",
			models.SettingsKeyOfflinePolicy, settings)
	}
	// The default from 016. What matters is that the column arrives, not which
	// value it happens to hold.
	if policy != models.OfflineCachedGrace {
		t.Errorf("offline_policy = %v, want the site's stored %s",
			policy, models.OfflineCachedGrace)
	}

	grace, present := settings[models.SettingsKeyOfflineGraceMinutes]
	if !present {
		t.Fatalf("the settings object carries no %q", models.SettingsKeyOfflineGraceMinutes)
	}
	if grace != float64(720) {
		t.Errorf("offline_grace_minutes = %v, want 720", grace)
	}
}

// TestDenyAllActuallyReachesTheTerminal is the case the finding is really about.
//
// A site that selects the strictest policy is making a safety decision. If it
// does not arrive, the door keeps admitting whoever it last heard about.
func TestDenyAllActuallyReachesTheTerminal(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-DENY")
	companyID := companyIDBySlug(t, "one")

	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"policy-admin@example.com", models.RoleAdmin)
	sitePublicID := sitePublicIDByName(t, "Site A")

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/sites/"+sitePublicID,
		`{"offline_policy":"DENY_ALL","offline_grace_minutes":0}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("setting the policy = %d: %v", code, body)
	}

	res := env.do("GET", "/api/v1/devices/settings", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /devices/settings = %d", res.Code)
	}

	settings := settingsObject(t, res.Body)
	if settings[models.SettingsKeyOfflinePolicy] != models.OfflineDenyAll {
		t.Fatalf("offline_policy = %v, want DENY_ALL to actually reach the door",
			settings[models.SettingsKeyOfflinePolicy])
	}
	if settings[models.SettingsKeyOfflineGraceMinutes] != float64(0) {
		t.Errorf("offline_grace_minutes = %v, want 0",
			settings[models.SettingsKeyOfflineGraceMinutes])
	}
}

// TestPolicyChangeBumpsTheVersionAndQueuesAJob.
//
// The requirements document asks for this by name, and the reason is exact: the
// terminal gates every settings push behind a STRICTLY GREATER version check, so
// a policy change written without incrementing settings_version is discarded as
// a replay. It would not be a slow change -- it would be a silent no-op that the
// console reported as applied.
func TestPolicyChangeBumpsTheVersionAndQueuesAJob(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-VERSION")
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDOf(t, "Site A")

	before := queryInt(t, `SELECT settings_version FROM sites WHERE id = $1`, siteID)

	// Registration seeds jobs; count only what the policy change adds.
	jobsBefore := queryInt(t, `
		SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-VERSION' AND j.job_type = 'SETTINGS'`)

	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"version-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A"),
		`{"offline_policy":"DENY_ALL"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("setting the policy = %d: %v", code, body)
	}

	after := queryInt(t, `SELECT settings_version FROM sites WHERE id = $1`, siteID)
	if after <= before {
		t.Fatalf("settings_version went %d -> %d; the terminal discards a push "+
			"whose version is not strictly greater, so this change would never apply",
			before, after)
	}

	jobsAfter := queryInt(t, `
		SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-VERSION' AND j.job_type = 'SETTINGS'`)
	if jobsAfter <= jobsBefore {
		t.Fatalf("SETTINGS jobs went %d -> %d; nothing would tell the terminal",
			jobsBefore, jobsAfter)
	}

	// And the QUEUED payload carries the policy, not just the pull endpoint.
	var payload string
	mustScan(t, `
		SELECT payload::text FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-VERSION' AND j.job_type = 'SETTINGS'
		 ORDER BY j.id DESC LIMIT 1`, &payload)
	if !strings.Contains(payload, `"offline_policy"`) ||
		!strings.Contains(payload, "DENY_ALL") {
		t.Errorf("the queued SETTINGS payload does not carry the policy: %s", payload)
	}
}

// TestEditingFreeFormSettingsKeepsThePolicy.
//
// The regression this guards: the SETTINGS job payload used to be built from
// the stored blob alone, so an operator editing unlock_duration_seconds pushed a
// payload with NO offline policy in it. The terminal treats absent keys as
// "leave the stored policy alone", so it would have survived -- but the pull
// endpoint and the push would have been describing different configurations,
// which is the kind of drift that shows up only on a terminal that missed a job.
func TestEditingFreeFormSettingsKeepsThePolicy(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-MERGE")
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `UPDATE sites SET offline_policy = 'DENY_ALL' WHERE site_name = 'Site A'`)

	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"merge-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/settings",
		`{"unlock_duration_seconds":7}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("updating settings = %d: %v", code, body)
	}

	res := env.do("GET", "/api/v1/devices/settings", nil, deviceAuth(key))
	settings := settingsObject(t, res.Body)

	if settings["unlock_duration_seconds"] != float64(7) {
		t.Errorf("the operator's own setting was lost: %v", settings)
	}
	if settings[models.SettingsKeyOfflinePolicy] != models.OfflineDenyAll {
		t.Errorf("editing the settings blob dropped the offline policy: %v", settings)
	}

	var payload string
	mustScan(t, `
		SELECT payload::text FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-MERGE' AND j.job_type = 'SETTINGS'
		 ORDER BY j.id DESC LIMIT 1`, &payload)
	if !strings.Contains(payload, "DENY_ALL") {
		t.Errorf("the pushed payload disagrees with the pull endpoint: %s", payload)
	}
}

// TestTheColumnBeatsTheBlob. An operator must not be able to override a
// validated safety control by writing raw JSON into the free-form object.
func TestTheColumnBeatsTheBlob(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-OVERRIDE")

	mustExec(t, `
		UPDATE sites
		   SET offline_policy = 'DENY_ALL',
		       settings = '{"offline_policy":"CACHED_INDEFINITE","offline_grace_minutes":43200}'::jsonb
		 WHERE site_name = 'Site A'`)

	res := env.do("GET", "/api/v1/devices/settings", nil, deviceAuth(key))
	settings := settingsObject(t, res.Body)

	if settings[models.SettingsKeyOfflinePolicy] != models.OfflineDenyAll {
		t.Fatalf("raw JSON in the settings blob overrode the validated column: %v", settings)
	}
}

// TestOfflinePolicyIsValidated. An unrecognised name is dropped by the firmware
// rather than mapped to anything, so a server that accepted one would silently
// leave the terminal on whatever it already had.
func TestOfflinePolicyIsValidated(t *testing.T) {
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"validate-admin@example.com", models.RoleAdmin)

	sitePath := "/api/v1/console/sites/" + sitePublicIDByName(t, "Site A")

	for _, body := range []string{
		`{"offline_policy":"SOMETIMES"}`,
		`{"offline_grace_minutes":-1}`,
		`{"offline_grace_minutes":43201}`,
	} {
		code, response := consoleCall(t, env.router, "PUT", sitePath, body, token, csrf)
		if code != http.StatusBadRequest {
			t.Errorf("%s returned %d, want 400: %v", body, code, response)
		}
	}
}

// TestOfflinePolicyRequiresAdmin. It decides what a door does during an outage.
func TestOfflinePolicyRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"policy-manager@example.com", models.RoleManager)

	code, _ := consoleCall(t, env.router, "PUT",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A"),
		`{"offline_policy":"DENY_ALL"}`, token, csrf)
	if code != http.StatusForbidden {
		t.Errorf("a MANAGER set a site's offline policy (got %d)", code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sitePublicIDByName(t *testing.T, name string) string {
	t.Helper()
	var id string
	mustScan(t, `SELECT public_id FROM sites WHERE site_name = '`+name+`'`, &id)
	return id
}

// decodeJSON is used where a test needs the raw body rather than the decoded
// object the helper returns.
func decodeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return out
}
