package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/models"
)

// A newly provisioned terminal must be told its site's offline policy.
//
// ---------------------------------------------------------------------------
// THE GAP, TRACED ACROSS BOTH REPOSITORIES
// ---------------------------------------------------------------------------
//
// The offline policy is a SAFETY CONTROL. `database/device_settings.go` says so
// at length: before it existed every terminal behaved as CACHED_INDEFINITE
// regardless of what the site chose, "which is worse than not offering the
// setting at all, because somebody made a safety decision and was told it had
// taken effect".
//
// It reaches a terminal by exactly one route: a SETTINGS job. And there is a
// second fact, on the other side of the wire, that turns that into a hole --
// THE FIRMWARE NEVER PULLS SETTINGS. `GET /api/v1/devices/settings` exists and
// is documented, and no shipped build calls it; the eight routes the terminal
// uses are heartbeat, jobs, jobs/complete, access/log, enrollment/result,
// credentials/pending, credentials/placement and claim. Settings arrive when
// they are PUSHED or they do not arrive.
//
// enqueueBootstrapJobs seeds a newly registered device with CREATE PERSON jobs
// and nothing else. So a terminal claimed at a DENY_ALL site:
//
//   - receives its roster;
//   - receives no policy;
//   - runs the firmware default, which is CACHED_INDEFINITE (chosen so that
//     upgrading a deployed unit could not change what its door did);
//   - and the console shows the site as DENY_ALL.
//
// It corrects itself only when something else pushes settings to that device --
// the next edit to the site, a backlog compaction, a relocation, or an operator
// resync. At a small site none of those need ever happen, so the window is not
// bounded by anything.
//
// CompactDeviceBacklog already gets this right: it queues the current settings
// alongside the snapshot, with the comment "since the queued SETTINGS job was
// just cancelled". Registration is the one path that seeds a device and does
// not.

// TestANewlyRegisteredTerminalIsSeededWithItsSitesPolicy is the whole finding.
func TestANewlyRegisteredTerminalIsSeededWithItsSitesPolicy(t *testing.T) {
	env := newTestEnv(t)
	siteID := siteIDByKey(t, env.siteAKey)

	// The site makes a safety decision BEFORE the terminal is installed, which
	// is the ordinary order: an operator configures a location and an installer
	// then goes and mounts the hardware.
	mustExec(t, `UPDATE sites
	                SET offline_policy = $2, offline_grace_minutes = $3
	              WHERE id = $1`,
		siteID, models.OfflineDenyAll, 0)

	deviceKey := env.registerDevice(env.siteAKey, "AT-SEED-01")

	// Everything the terminal is handed on its first poll.
	var settings map[string]any
	for _, job := range env.jobs(deviceKey) {
		if job["job_type"] == "SETTINGS" {
			settings, _ = job["payload"].(map[string]any)
			break
		}
	}

	if settings == nil {
		t.Fatal("a newly registered terminal received no SETTINGS job, so it " +
			"never learns its site's offline policy: the firmware does not " +
			"pull settings, and its default is CACHED_INDEFINITE")
	}

	// The version has to be there, because the firmware gates the whole payload
	// behind a strictly-greater comparison. A settings push with no usable
	// version is discarded by the terminal, which would make this seeding look
	// present and do nothing.
	if _, ok := settings["settings_version"]; !ok {
		t.Fatalf("the seeded SETTINGS job carried no settings_version, so the "+
			"terminal discards it (payload %v)", settings)
	}

	inner, _ := settings["settings"].(map[string]any)
	if inner == nil {
		t.Fatalf("the seeded SETTINGS job carried no settings object (payload %v)", settings)
	}

	if got := inner[models.SettingsKeyOfflinePolicy]; got != models.OfflineDenyAll {
		t.Errorf("offline_policy = %v, want %q -- the terminal would run its "+
			"CACHED_INDEFINITE default at a site that chose DENY_ALL",
			got, models.OfflineDenyAll)
	}
	if _, ok := inner[models.SettingsKeyOfflineGraceMinutes]; !ok {
		t.Errorf("offline_grace_minutes is absent (settings %v)", inner)
	}
}

// TestTheSeededPolicyIsTheColumnAndNotTheFreeFormBlob.
//
// The same precedence every other delivery path applies. An operator cannot be
// allowed to defeat a safety control by writing raw JSON into the site's
// free-form settings object -- and the seeding path is a new place that rule
// has to hold, not an exception to it.
func TestTheSeededPolicyIsTheColumnAndNotTheFreeFormBlob(t *testing.T) {
	env := newTestEnv(t)
	siteID := siteIDByKey(t, env.siteAKey)

	// Written directly, because the API refuses these keys in the blob. What is
	// under test is what happens when a row already holds them -- from an older
	// installation, or a hand-edited database.
	mustExec(t, `UPDATE sites
	                SET offline_policy = $2,
	                    offline_grace_minutes = $3,
	                    settings = $4::jsonb
	              WHERE id = $1`,
		siteID, models.OfflineDenyAll, 15,
		`{"unlock_duration_seconds": 7, "offline_policy": "CACHED_INDEFINITE", "offline_grace_minutes": 43200}`)

	deviceKey := env.registerDevice(env.siteAKey, "AT-SEED-02")

	var inner map[string]any
	for _, job := range env.jobs(deviceKey) {
		if job["job_type"] == "SETTINGS" {
			payload, _ := job["payload"].(map[string]any)
			inner, _ = payload["settings"].(map[string]any)
			break
		}
	}
	if inner == nil {
		t.Fatal("no SETTINGS job was seeded")
	}

	if got := inner[models.SettingsKeyOfflinePolicy]; got != models.OfflineDenyAll {
		t.Errorf("offline_policy = %v, want %q from the column", got, models.OfflineDenyAll)
	}
	if got := inner[models.SettingsKeyOfflineGraceMinutes]; got != float64(15) {
		t.Errorf("offline_grace_minutes = %v, want 15 from the column", got)
	}

	// And the operator's own keys still arrive. The columns are layered OVER
	// the blob, not instead of it.
	if got := inner["unlock_duration_seconds"]; got != float64(7) {
		t.Errorf("unlock_duration_seconds = %v, want 7 -- the free-form keys "+
			"must survive the merge", got)
	}
}

// TestSeedingSettingsDoesNotDisturbTheRoster.
//
// The bootstrap's existing job is to deliver people. Adding a settings push
// must not change how many PERSON jobs a terminal gets, or their content.
func TestSeedingSettingsDoesNotDisturbTheRoster(t *testing.T) {
	env := newTestEnv(t)

	env.createMember(env.siteAKey, "SEED001", "A Person")
	env.createMember(env.siteAKey, "SEED002", "Another Person")

	deviceKey := env.registerDevice(env.siteAKey, "AT-SEED-03")

	people, settings := 0, 0
	for _, job := range env.jobs(deviceKey) {
		switch job["job_type"] {
		case "CREATE":
			people++
		case "SETTINGS":
			settings++
		}
	}

	if people != 2 {
		t.Errorf("got %d CREATE jobs, want 2", people)
	}
	// EXACTLY ONE. A second would be redundant work at every registration, and
	// a re-registration that stacked them would be a backlog nobody asked for.
	if settings != 1 {
		t.Errorf("got %d SETTINGS jobs, want exactly 1", settings)
	}
}

// TestReRegisteringDoesNotStackSettingsJobs.
//
// Re-registration is the documented recovery for a lost credential, and it runs
// the same seeding. A terminal that came back three times must not be holding
// three identical settings pushes -- the firmware would apply the first and
// discard the rest on the version gate, but they occupy the backlog the
// capacity guard reads.
func TestReRegisteringDoesNotStackSettingsJobs(t *testing.T) {
	env := newTestEnv(t)

	env.registerDevice(env.siteAKey, "AT-SEED-04")
	env.registerDevice(env.siteAKey, "AT-SEED-04")
	deviceKey := env.registerDevice(env.siteAKey, "AT-SEED-04")

	settings := 0
	for _, job := range env.jobs(deviceKey) {
		if job["job_type"] == "SETTINGS" {
			settings++
		}
	}

	if settings != 1 {
		t.Errorf("got %d SETTINGS jobs after three registrations, want 1", settings)
	}
}

// TestAClaimedTerminalIsSeededTheSameWay.
//
// The claim-code flow is the one an installer actually uses -- it exists so the
// site key never reaches a laptop -- and it must not be the path that skips the
// safety control. It goes through the same registration, and this asserts that
// rather than assuming it.
func TestAClaimedTerminalIsSeededTheSameWay(t *testing.T) {
	f := newClaimFixture(t)
	siteID := siteIDByKey(t, f.env.siteAKey)

	mustExec(t, `UPDATE sites SET offline_policy = $2 WHERE id = $1`,
		siteID, models.OfflineCachedGrace)

	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-SEED-05")

	res := f.claim(t, code, "AT-SEED-05")
	if res.Code != http.StatusOK {
		t.Fatalf("claiming: got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	deviceKey, _ := res.Body["api_key"].(string)
	if deviceKey == "" {
		t.Fatalf("claim returned no api_key (body %s)", res.Raw)
	}

	var inner map[string]any
	for _, job := range f.env.jobs(deviceKey) {
		if job["job_type"] == "SETTINGS" {
			payload, _ := job["payload"].(map[string]any)
			inner, _ = payload["settings"].(map[string]any)
			break
		}
	}
	if inner == nil {
		t.Fatal("a CLAIMED terminal received no SETTINGS job")
	}
	if got := inner[models.SettingsKeyOfflinePolicy]; got != models.OfflineCachedGrace {
		t.Errorf("offline_policy = %v, want %q", got, models.OfflineCachedGrace)
	}
}
