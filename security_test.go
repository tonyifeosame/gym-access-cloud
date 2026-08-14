package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
)

// Authorization boundaries.
//
// Every test here is an attempt to reach across a boundary that must hold:
// device to device, site to site, tenant to tenant. They are written as attacks
// rather than as feature checks, because a boundary that is only exercised by
// well-behaved callers is not tested at all.

func TestDeviceCannotAcknowledgeAnotherDevicesJob(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-AAA")
	keyB := env.registerDevice(env.siteAKey, "ESP32-BBB")
	env.createMember(env.siteAKey, "M-1", "Ada")

	// Both terminals get their own copy of the change.
	jobsA := env.jobs(keyA)
	if len(jobsA) != 1 {
		t.Fatalf("device A got %d jobs, want 1", len(jobsA))
	}
	idA := jobID(t, jobsA[0])

	res := env.do(http.MethodPost, jobPath(idA), map[string]any{"status": "COMPLETED"}, deviceAuth(keyB))
	if res.Code != http.StatusNotFound {
		t.Errorf("device B acking device A's job got %d, want 404 (body %s)", res.Code, res.Raw)
	}

	// A's work must be untouched by B's attempt.
	if s := jobStatus(t, idA); s != "PENDING" {
		t.Errorf("device A's job became %s after device B acked it", s)
	}
}

func TestDeviceOfAnotherTenantCannotAcknowledgeJob(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-AAA")
	keyC := env.registerDevice(env.siteCKey, "ESP32-CCC") // different company
	env.createMember(env.siteAKey, "M-1", "Ada")

	idA := jobID(t, env.jobs(keyA)[0])

	res := env.do(http.MethodPost, jobPath(idA), map[string]any{"status": "COMPLETED"}, deviceAuth(keyC))
	if res.Code != http.StatusNotFound {
		t.Errorf("cross-tenant ack got %d, want 404 (body %s)", res.Code, res.Raw)
	}
	if s := jobStatus(t, idA); s != "PENDING" {
		t.Errorf("job became %s after a cross-tenant ack", s)
	}
}

func TestSiteCannotResyncAnotherSitesDevice(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-AAA")

	// Site B is the same tenant but a different site: still not its terminal.
	res := env.do(http.MethodPost, "/api/v1/devices/ESP32-AAA/resync", nil, siteAuth(env.siteBKey))
	if res.Code != http.StatusNotFound {
		t.Errorf("cross-site resync got %d, want 404 (body %s)", res.Code, res.Raw)
	}
}

func TestSerialCannotBeStolenByAnotherSite(t *testing.T) {
	env := newTestEnv(t)
	originalKey := env.registerDevice(env.siteAKey, "ESP32-AAA")

	// Registering is how a factory-reset terminal recovers, so it rotates the
	// credential. If another site could do that, holding any site key would let
	// you take over any terminal by guessing its serial.
	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-AAA"}, siteAuth(env.siteBKey))
	if res.Code != http.StatusConflict {
		t.Errorf("cross-site registration got %d, want 409 (body %s)", res.Code, res.Raw)
	}

	// The original credential must still work: the rejected attempt must not
	// have rotated it as a side effect.
	check := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(originalKey))
	if check.Code != http.StatusOK {
		t.Errorf("original credential stopped working after a rejected takeover: got %d", check.Code)
	}
}

func TestLegacyAuthCannotClaimAnotherSitesSerial(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-AAA")

	// The deprecated path trusts a claimed serial, so it must still be checked
	// against the site the key belongs to.
	res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, map[string]string{
		"X-API-Key": env.siteBKey, "X-Device-Serial": "ESP32-AAA",
	})
	if res.Code != http.StatusNotFound {
		t.Errorf("legacy cross-site auth got %d, want 404 (body %s)", res.Code, res.Raw)
	}
}

func TestCredentialRotationInvalidatesTheOldKey(t *testing.T) {
	env := newTestEnv(t)
	oldKey := env.registerDevice(env.siteAKey, "ESP32-AAA")

	newKey := env.registerDevice(env.siteAKey, "ESP32-AAA")
	if newKey == oldKey {
		t.Fatal("re-registration returned the same credential, so rotation does nothing")
	}

	// The point of rotation is that a leaked key stops working.
	res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(oldKey))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("rotated-away key got %d, want 401 (body %s)", res.Code, res.Raw)
	}

	if res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(newKey)); res.Code != http.StatusOK {
		t.Errorf("new key got %d, want 200 (body %s)", res.Code, res.Raw)
	}
}

func TestDeviceCredentialIsNotStoredInPlaintext(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-AAA")

	if count := queryInt(t, `SELECT count(*) FROM devices WHERE api_key_hash = $1`, key); count != 0 {
		t.Error("the issued credential is stored verbatim; a database leak would hand over every terminal")
	}
}

func TestMemberDataIsScopedToTheTenant(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M-1", "Ada")

	// Site C belongs to a different company.
	code, members := env.list(http.MethodGet, "/api/v1/members", siteAuth(env.siteCKey))
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if len(members) != 0 {
		t.Errorf("another tenant sees %d members, want 0: %v", len(members), members)
	}

	res := env.do(http.MethodGet, "/api/v1/members/M-1", nil, siteAuth(env.siteCKey))
	if res.Code != http.StatusNotFound {
		t.Errorf("cross-tenant member read got %d, want 404 (body %s)", res.Code, res.Raw)
	}

	// Nor may it modify one it cannot see.
	del := env.do(http.MethodDelete, "/api/v1/members/M-1", nil, siteAuth(env.siteCKey))
	if del.Code == http.StatusOK {
		stillThere := queryBool(t,
			`SELECT EXISTS (SELECT 1 FROM people WHERE external_id = 'M-1' AND deleted_at IS NULL)`)
		if !stillThere {
			t.Error("another tenant deleted a member it cannot even read")
		}
	}
}

func TestFirmwareCatalogIsScopedToTheTenant(t *testing.T) {
	env := newTestEnv(t)

	// Company two publishes a build.
	created := env.do(http.MethodPost, "/api/v1/firmware", map[string]any{
		"version": "9.9.9", "device_type": "TERMINAL", "release_channel": "STABLE",
		"download_url": "http://example.invalid/firmware.bin",
	}, siteAuth(env.siteCKey))
	if created.Code != http.StatusCreated {
		t.Fatalf("creating firmware got %d, want 201 (body %s)", created.Code, created.Raw)
	}

	// Company one must not see it. The catalog carries download URLs and
	// checksums, and once OTA exists it is what a terminal would be pointed at.
	res := env.do(http.MethodGet, "/api/v1/firmware", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("listing firmware got %d, want 200", res.Code)
	}
	if got := res.Body["count"]; got != float64(0) {
		t.Errorf("company one sees %v firmware versions belonging to company two (body %s)", got, res.Raw)
	}
}

func TestTenantCannotRetargetAnotherTenantsFleet(t *testing.T) {
	env := newTestEnv(t)

	created := env.do(http.MethodPost, "/api/v1/firmware", map[string]any{
		"version": "9.9.9", "device_type": "TERMINAL", "release_channel": "STABLE",
	}, siteAuth(env.siteCKey))
	id, _ := created.Body["id"].(float64)
	if id == 0 {
		t.Fatalf("firmware id missing from %s", created.Raw)
	}

	// `is_current` defines what "outdated" means for a fleet. If one tenant can
	// set another's, it can misreport every terminal they own -- and once OTA
	// exists, choose what they install.
	res := env.do(http.MethodPut, "/api/v1/firmware/"+itoa(int64(id))+"/current", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusNotFound {
		t.Errorf("cross-tenant retarget got %d, want 404 (body %s)", res.Code, res.Raw)
	}

	if queryBool(t, `SELECT is_current FROM firmware_versions WHERE version = '9.9.9'`) {
		t.Error("another tenant's build was marked current")
	}
}

func TestSiteSettingsAreScopedToTheSite(t *testing.T) {
	env := newTestEnv(t)

	env.do(http.MethodPut, "/api/v1/sites/settings",
		map[string]any{"unlock_duration_seconds": 3}, siteAuth(env.siteAKey))
	env.do(http.MethodPut, "/api/v1/sites/settings",
		map[string]any{"unlock_duration_seconds": 9}, siteAuth(env.siteBKey))

	res := env.do(http.MethodGet, "/api/v1/sites/settings", nil, siteAuth(env.siteAKey))
	settings, _ := res.Body["settings"].(map[string]any)
	if settings["unlock_duration_seconds"] != float64(3) {
		t.Errorf("site A sees %v, want 3 -- site B's write leaked across",
			settings["unlock_duration_seconds"])
	}
}

func TestAuthenticationIsRequired(t *testing.T) {
	env := newTestEnv(t)

	protected := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/members"},
		{http.MethodPost, "/api/v1/members"},
		{http.MethodGet, "/api/v1/access/logs"},
		{http.MethodGet, "/api/v1/sites/settings"},
		{http.MethodGet, "/api/v1/devices"},
		{http.MethodGet, "/api/v1/firmware"},
		{http.MethodPost, "/api/v1/devices/register"},
	}

	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			res := env.do(route.method, route.path, nil, nil)
			if res.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401 without a key", res.Code)
			}

			res = env.do(route.method, route.path, nil, siteAuth("not-a-real-key"))
			if res.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401 with a bogus key", res.Code)
			}
		})
	}
}

func TestDeactivatedSiteCannotUseItsKey(t *testing.T) {
	env := newTestEnv(t)
	mustExec(t, `UPDATE sites SET active = FALSE WHERE api_key_hash = $1`,
		database.HashSiteKey(env.siteAKey))

	res := env.do(http.MethodGet, "/api/v1/members", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("deactivated site got %d, want 401 (body %s)", res.Code, res.Raw)
	}
}

func TestCORSDoesNotPairWildcardWithCredentials(t *testing.T) {
	env := newTestEnv(t)

	rec := env.raw(http.MethodGet, "/health", nil, map[string]string{"Origin": "http://evil.example"})

	origin := rec.Header().Get("Access-Control-Allow-Origin")
	credentials := rec.Header().Get("Access-Control-Allow-Credentials")
	if origin == "*" && credentials == "true" {
		t.Error("wildcard origin is paired with credentials, which advertises the API as open to every origin")
	}
}

// A disabled terminal is the only revocation this system has -- there is no
// revoke endpoint -- so registration must not be a way around it.
//
// Before this was fixed, the upsert set `status = 'ONLINE'` unconditionally. An
// operator disabled a stolen unit, anyone holding the site key re-registered
// its serial, and the unit came back into service with a fresh working
// credential in the response.
func TestRegisteringADisabledDeviceDoesNotReviveIt(t *testing.T) {
	env := newTestEnv(t)
	originalKey := env.registerDevice(env.siteAKey, "ESP32-AAA")

	mustExec(t, `UPDATE devices SET status = 'DISABLED' WHERE serial_number = 'ESP32-AAA'`)

	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-AAA"}, siteAuth(env.siteAKey))
	if res.Code != http.StatusForbidden {
		t.Errorf("registering a disabled device got %d, want 403 (body %s)", res.Code, res.Raw)
	}

	// No credential may be handed out on that path.
	if key, _ := res.Body["api_key"].(string); key != "" {
		t.Error("a disabled device was issued a credential")
	}

	// Still disabled, and still locked out.
	var status string
	if err := database.DB.QueryRow(
		`SELECT status FROM devices WHERE serial_number = 'ESP32-AAA'`).Scan(&status); err != nil {
		t.Fatalf("reading device status: %v", err)
	}
	if status != "DISABLED" {
		t.Errorf("device status is %q after a refused registration, want DISABLED", status)
	}

	check := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(originalKey))
	if check.Code != http.StatusForbidden {
		t.Errorf("disabled device still authenticates: got %d, want 403", check.Code)
	}
}

// `active` is set independently of `status`, and either one is an operator
// saying no.
func TestRegisteringAnInactiveDeviceIsRefused(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-AAA")

	mustExec(t, `UPDATE devices SET active = FALSE WHERE serial_number = 'ESP32-AAA'`)

	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-AAA"}, siteAuth(env.siteAKey))
	if res.Code != http.StatusForbidden {
		t.Errorf("registering an inactive device got %d, want 403 (body %s)", res.Code, res.Raw)
	}
}

// The refusal must roll back the credential the upsert had already written,
// not merely decline to report it. Otherwise the live terminal's key is
// silently rotated to one nobody holds and it drops off the fleet.
func TestARefusedRegistrationDoesNotRotateTheCredential(t *testing.T) {
	env := newTestEnv(t)
	originalKey := env.registerDevice(env.siteAKey, "ESP32-AAA")

	var before string
	if err := database.DB.QueryRow(
		`SELECT api_key_hash FROM devices WHERE serial_number = 'ESP32-AAA'`).Scan(&before); err != nil {
		t.Fatalf("reading credential hash: %v", err)
	}

	mustExec(t, `UPDATE devices SET status = 'DISABLED' WHERE serial_number = 'ESP32-AAA'`)
	env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-AAA"}, siteAuth(env.siteAKey))

	var after string
	if err := database.DB.QueryRow(
		`SELECT api_key_hash FROM devices WHERE serial_number = 'ESP32-AAA'`).Scan(&after); err != nil {
		t.Fatalf("reading credential hash: %v", err)
	}
	if before != after {
		t.Error("a refused registration rotated the stored credential anyway")
	}

	// Re-enabled, the original key still works -- nothing was lost.
	mustExec(t, `UPDATE devices SET status = 'ONLINE' WHERE serial_number = 'ESP32-AAA'`)
	check := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(originalKey))
	if check.Code != http.StatusOK {
		t.Errorf("original credential stopped working after a refused registration: got %d", check.Code)
	}
}

// An unknown enum is the caller's mistake, not the server's. Reporting it as a
// 500 told a terminal to retry something that will never succeed.
func TestInvalidRegistrationEnumsAreRejectedAsBadRequests(t *testing.T) {
	env := newTestEnv(t)

	for _, body := range []map[string]any{
		{"serial_number": "ESP32-BBB", "device_type": "ESP32"},
		{"serial_number": "ESP32-BBB", "release_channel": "NIGHTLY"},
	} {
		res := env.do(http.MethodPost, "/api/v1/devices/register", body, siteAuth(env.siteAKey))
		if res.Code != http.StatusBadRequest {
			t.Errorf("registration with %v got %d, want 400 (body %s)", body, res.Code, res.Raw)
		}
	}

	// The valid values still work, and omitting them still defaults.
	for _, body := range []map[string]any{
		{"serial_number": "ESP32-CCC"},
		{"serial_number": "ESP32-DDD", "device_type": "READER", "release_channel": "BETA"},
	} {
		res := env.do(http.MethodPost, "/api/v1/devices/register", body, siteAuth(env.siteAKey))
		if res.Code != http.StatusCreated {
			t.Errorf("registration with %v got %d, want 201 (body %s)", body, res.Code, res.Raw)
		}
	}
}

// The credential is committed the moment registration succeeds, so the roster
// it is seeded with has to be committed in the same transaction. If seeding
// could fail after the commit, the caller would get a 500 and the terminal
// would hold a rotated key nobody ever saw.
func TestRegistrationSeedsTheRosterAtomically(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M001", "Alice")
	env.createMember(env.siteAKey, "M002", "Bob")

	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-AAA"}, siteAuth(env.siteAKey))
	if res.Code != http.StatusCreated {
		t.Fatalf("registration got %d, want 201 (body %s)", res.Code, res.Raw)
	}

	bootstrapped, _ := res.Body["bootstrap_jobs"].(float64)
	if int(bootstrapped) != 2 {
		t.Errorf("bootstrap_jobs is %v, want 2 (body %s)", bootstrapped, res.Raw)
	}

	// Reported and actually queued are the same number.
	var queued int
	if err := database.DB.QueryRow(`
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-AAA' AND j.status = 'PENDING'`).Scan(&queued); err != nil {
		t.Fatalf("counting queued jobs: %v", err)
	}
	if queued != int(bootstrapped) {
		t.Errorf("registration reported %v bootstrap jobs but %d are queued", bootstrapped, queued)
	}
}
