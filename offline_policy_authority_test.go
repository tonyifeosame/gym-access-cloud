package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"access-terminal-cloud-api/models"
)

// F2/F3/F4: one authoritative representation of the offline policy.
//
// THE SHAPE OF THE TRAP THESE CLOSE. `offline_policy` and
// `offline_grace_minutes` live in validated columns with a CHECK behind them,
// and the terminal is sent those columns LAYERED OVER the site's free-form
// settings object. So writing `offline_grace_minutes` into the free-form object
// used to:
//
//	1. succeed, with a 200;
//	2. read back, so the console displayed the number;
//	3. be silently overwritten on the way to every terminal.
//
// An operator who set a grace window that way was told it had taken effect and
// it had not -- which is worse than not offering the setting, because somebody
// made a safety decision and was given a receipt for it.
//
// Both halves are closed here: the write is refused out loud, and every read
// states what is actually in force.

// TestFreeFormSettingsCannotCarryTheOfflinePolicy is the trap itself.
func TestFreeFormSettingsCannotCarryTheOfflinePolicy(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"the policy", map[string]any{
			models.SettingsKeyOfflinePolicy: models.OfflineCachedIndefinite,
		}},
		{"the grace window", map[string]any{
			models.SettingsKeyOfflineGraceMinutes: 43200,
		}},
		{"both, beside legitimate keys", map[string]any{
			"unlock_duration_ms":                  5000,
			models.SettingsKeyOfflinePolicy:       models.OfflineDenyAll,
			models.SettingsKeyOfflineGraceMinutes: 60,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.do(http.MethodPut, "/api/v1/sites/settings", tc.body,
				siteAuth(env.siteAKey))

			if res.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 (body %s)", res.Code, res.Raw)
			}
			// The message has to name the field that DOES work. Somebody
			// sending this has a specific outcome in mind.
			if msg, _ := res.Body["error"].(string); msg == "" {
				t.Errorf("the refusal carried no explanation (body %s)", res.Raw)
			}
		})
	}
}

// TestARefusedSettingsWriteChangesNothing.
//
// A refusal that still bumped settings_version would fan a SETTINGS job out to
// every terminal at the site for a change that was rejected.
func TestARefusedSettingsWriteChangesNothing(t *testing.T) {
	env := newTestEnv(t)
	siteID := siteIDByKey(t, env.siteAKey)

	versionBefore := queryInt(t, `SELECT settings_version FROM sites WHERE id = $1`, siteID)
	jobsBefore := queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE site_id = $1 AND job_type = 'SETTINGS'`, siteID)

	env.do(http.MethodPut, "/api/v1/sites/settings", map[string]any{
		models.SettingsKeyOfflineGraceMinutes: 43200,
	}, siteAuth(env.siteAKey))

	if got := queryInt(t, `SELECT settings_version FROM sites WHERE id = $1`, siteID); got != versionBefore {
		t.Errorf("settings_version moved from %d to %d on a refused write", versionBefore, got)
	}
	if got := queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE site_id = $1 AND job_type = 'SETTINGS'`,
		siteID); got != jobsBefore {
		t.Errorf("%d SETTINGS jobs were queued by a refused write", got-jobsBefore)
	}
}

// TestOrdinarySettingsStillWrite is the regression guard. The free-form object
// is still free-form; exactly two keys are reserved.
func TestOrdinarySettingsStillWrite(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodPut, "/api/v1/sites/settings", map[string]any{
		"unlock_duration_ms": 5000,
		"door_name":          "Front",
	}, siteAuth(env.siteAKey))

	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
}

// TestSettingsReadsStateTheEffectivePolicy.
//
// The read used to return the free-form blob alone, so a caller had no way to
// learn what the terminals at that site are actually running. That is the other
// half of the trap: even with the write refused, a console showing only the
// blob shows nothing about the setting that matters most during an outage.
func TestSettingsReadsStateTheEffectivePolicy(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodGet, "/api/v1/sites/settings", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	policy, _ := res.Body[models.SettingsKeyOfflinePolicy].(string)
	if !models.IsOfflinePolicy(policy) {
		t.Errorf("offline_policy = %q, which is not a policy the platform understands (body %s)",
			policy, res.Raw)
	}
	if _, present := res.Body[models.SettingsKeyOfflineGraceMinutes]; !present {
		t.Errorf("the read does not state the grace window in force (body %s)", res.Raw)
	}
}

// TestTheReadAndTheDeviceAgree is the property the whole file is about.
//
// GET /sites/settings is what an operator sees. GET /devices/settings is what a
// terminal is sent. They must not be able to describe different configurations,
// because a disagreement between them IS the failure -- a console reporting a
// policy the doors are not running.
func TestTheReadAndTheDeviceAgree(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"policy@example.com", models.RoleAdmin)

	key := env.registerDevice(env.siteAKey, "ESP32-POLICY")

	// Set the policy the supported way.
	code, body := consoleCall(t, env.router, http.MethodPut,
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A"),
		`{"offline_policy":"DENY_ALL","offline_grace_minutes":0}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("setting the policy got %d (body %v)", code, body)
	}

	operatorView := env.do(http.MethodGet, "/api/v1/sites/settings", nil, siteAuth(env.siteAKey))
	deviceView := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(key))

	if got := operatorView.Body[models.SettingsKeyOfflinePolicy]; got != models.OfflineDenyAll {
		t.Errorf("the operator read says %v, want DENY_ALL (body %s)", got, operatorView.Raw)
	}

	// The device's copy is nested inside the settings object it applies.
	var settings map[string]any
	raw, _ := json.Marshal(deviceView.Body["settings"])
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("decoding the device's settings object: %v (body %s)", err, deviceView.Raw)
	}
	if got := settings[models.SettingsKeyOfflinePolicy]; got != models.OfflineDenyAll {
		t.Errorf("the terminal is sent %v, want DENY_ALL (body %s)", got, deviceView.Raw)
	}
}

// TestSiteProjectionsCarryThePolicy.
//
// F4: on every site read, not only the settings endpoint. An operator scanning
// a list of locations should be able to see which ones keep opening during an
// outage without visiting each one.
func TestSiteProjectionsCarryThePolicy(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"sites@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, http.MethodGet, "/api/v1/console/sites", "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("listing sites got %d (body %v)", code, body)
	}

	for _, entry := range listOf(t, body, "sites") {
		site, _ := entry.(map[string]any)
		policy, _ := site[models.SettingsKeyOfflinePolicy].(string)
		if !models.IsOfflinePolicy(policy) {
			t.Errorf("site %v carries offline_policy %q", site["name"], policy)
		}
		if _, present := site[models.SettingsKeyOfflineGraceMinutes]; !present {
			t.Errorf("site %v does not state its grace window", site["name"])
		}
	}
}
