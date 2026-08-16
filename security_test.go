package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
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

	// Both terminals get their own copy of the change. Each also holds the
	// settings push its registration seeded, so the person job is selected by
	// type rather than by position.
	createsA := jobsOfType(env.jobs(keyA), "CREATE")
	if len(createsA) != 1 {
		t.Fatalf("device A got %d CREATE jobs, want 1", len(createsA))
	}
	idA := jobID(t, createsA[0])

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

	crossSite := map[string]string{
		"X-API-Key": env.siteBKey, "X-Device-Serial": "ESP32-AAA",
	}

	// SEC-05: by default the pair is refused before the serial is even looked
	// up, so this cannot be used to discover what is registered where.
	t.Run("refused outright by default", func(t *testing.T) {
		defaultDeviceAuth(t)
		res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, crossSite)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("legacy cross-site auth got %d, want 401 (body %s)", res.Code, res.Raw)
		}
	})

	// And when a deployment mid-upgrade re-opens the path, the ORIGINAL property
	// still holds: the path trusts a claimed serial, so the serial must be
	// checked against the site the key belongs to.
	t.Run("still scoped to the key's own site when enabled", func(t *testing.T) {
		t.Setenv(middleware.LegacyDeviceAuthEnv, "1")
		res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, crossSite)
		if res.Code != http.StatusNotFound {
			t.Errorf("legacy cross-site auth got %d, want 404 (body %s)", res.Code, res.Raw)
		}
	})
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

// The firmware catalogue tenancy tests exercise /console/firmware rather than
// the site-key routes they were written against.
//
// The WRITES moved there deliberately (SEC-02): any site provisioning key could
// publish a build and move the `is_current` target, which is what every "is this
// terminal outdated" report is measured against and, once OTA exists, the row a
// terminal would be pointed at. A secret installed on hardware at one location
// must not control that.
//
// The PROPERTY under test is unchanged and is the one that matters: one tenant
// must not be able to read, publish into, or retarget another's catalogue. Only
// the door has moved.

func TestFirmwareCatalogIsScopedToTheTenant(t *testing.T) {
	env := newTestEnv(t)

	two := companyIDBySlug(t, "two")
	_, twoToken, twoCSRF := consoleOperatorSession(t, env.router, two,
		"two-fw@example.com", models.RoleAdmin)

	// Company two publishes a build.
	code, created := consoleCall(t, env.router, "POST", "/api/v1/console/firmware",
		`{"version":"9.9.9","device_type":"TERMINAL","release_channel":"STABLE",
		  "download_url":"http://example.invalid/firmware.bin"}`, twoToken, twoCSRF)
	if code != http.StatusCreated {
		t.Fatalf("creating firmware got %d, want 201 (body %v)", code, created)
	}

	// Company one must not see it. The catalogue carries download URLs and
	// checksums, and once OTA exists it is what a terminal would be pointed at.
	one := companyIDBySlug(t, "one")
	_, oneToken, oneCSRF := consoleOperatorSession(t, env.router, one,
		"one-fw@example.com", models.RoleAdmin)

	code, listing := consoleCall(t, env.router, "GET", "/api/v1/console/firmware",
		``, oneToken, oneCSRF)
	if code != http.StatusOK {
		t.Fatalf("listing firmware got %d, want 200", code)
	}
	if got := listing["count"]; got != float64(0) {
		t.Errorf("company one sees %v firmware versions belonging to company two (body %v)", got, listing)
	}

	// The site-key read is likewise scoped, and is the one path a terminal uses.
	res := env.do(http.MethodGet, "/api/v1/firmware", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("site-key firmware read got %d, want 200", res.Code)
	}
	if got := res.Body["count"]; got != float64(0) {
		t.Errorf("company one's site key sees %v of company two's builds (body %s)", got, res.Raw)
	}
}

func TestTenantCannotRetargetAnotherTenantsFleet(t *testing.T) {
	env := newTestEnv(t)

	two := companyIDBySlug(t, "two")
	_, twoToken, twoCSRF := consoleOperatorSession(t, env.router, two,
		"two-target@example.com", models.RoleAdmin)

	code, created := consoleCall(t, env.router, "POST", "/api/v1/console/firmware",
		`{"version":"9.9.9","device_type":"TERMINAL","release_channel":"STABLE"}`,
		twoToken, twoCSRF)
	if code != http.StatusCreated {
		t.Fatalf("creating firmware got %d, want 201 (body %v)", code, created)
	}
	id, _ := created["id"].(float64)
	if id == 0 {
		t.Fatalf("firmware id missing from %v", created)
	}

	// `is_current` defines what "outdated" means for a fleet. If one tenant can
	// set another's, it can misreport every terminal they own -- and once OTA
	// exists, choose what they install.
	one := companyIDBySlug(t, "one")
	_, oneToken, oneCSRF := consoleOperatorSession(t, env.router, one,
		"one-target@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "PUT",
		"/api/v1/console/firmware/"+itoa(int64(id))+"/current", ``, oneToken, oneCSRF)
	if code != http.StatusNotFound {
		t.Errorf("cross-tenant retarget got %d, want 404 (body %v)", code, body)
	}

	if queryBool(t, `SELECT is_current FROM firmware_versions WHERE version = '9.9.9'`) {
		t.Error("another tenant's build was marked current")
	}
}

// TestFirmwareWritesRequireAnOperator proves the capability MOVED rather than
// being deleted, and that a lower role cannot reach it.
func TestFirmwareWritesRequireAnOperator(t *testing.T) {
	env := newTestEnv(t)
	one := companyIDBySlug(t, "one")

	_, mgrToken, mgrCSRF := consoleOperatorSession(t, env.router, one,
		"fw-manager@example.com", models.RoleManager)

	code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/firmware",
		`{"version":"1.2.3","device_type":"TERMINAL"}`, mgrToken, mgrCSRF)
	if code != http.StatusForbidden {
		t.Errorf("MANAGER publishing firmware got %d, want 403", code)
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
	//
	// `bootstrap_jobs` counts PEOPLE, which is what it has always meant and what
	// a client reading it expects. Registration also seeds one SETTINGS push --
	// the site's offline policy has no other route to a terminal -- and that is
	// asserted separately rather than folded into a number with an established
	// meaning.
	var queued int
	if err := database.DB.QueryRow(`
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-AAA' AND j.status = 'PENDING'
		   AND j.job_type = 'CREATE'`).Scan(&queued); err != nil {
		t.Fatalf("counting queued jobs: %v", err)
	}
	if queued != int(bootstrapped) {
		t.Errorf("registration reported %v bootstrap jobs but %d are queued", bootstrapped, queued)
	}

	// And the settings push committed in the same transaction. A terminal
	// seeded with people and no policy runs the firmware default, which is
	// CACHED_INDEFINITE, at a site that may have chosen otherwise.
	var settings int
	if err := database.DB.QueryRow(`
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = 'ESP32-AAA' AND j.status = 'PENDING'
		   AND j.job_type = 'SETTINGS'`).Scan(&settings); err != nil {
		t.Fatalf("counting settings jobs: %v", err)
	}
	if settings != 1 {
		t.Errorf("registration queued %d SETTINGS jobs, want exactly 1", settings)
	}
}
