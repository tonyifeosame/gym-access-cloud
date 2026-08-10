package main

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
)

// Door events reported with the device's own credential.
//
// POST /access/log takes the SITE key, and that key is the provisioning secret
// -- it can register terminals and rotate their credentials. Requiring it on
// every door so a terminal could write an audit line would have made the audit
// trail the weakest thing in the system: steal one terminal, get the key that
// controls the whole site.
//
// These cover the device-authenticated endpoint that replaces it. The
// assertions that matter are the ones about what a terminal CANNOT do -- name
// another site, duplicate an event, or authenticate with anything but its own
// credential.

const testEventID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

func TestADeviceCanReportADoorEventWithItsOwnCredential(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M001", "Ada")
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	res := env.do(http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": testEventID, "member_id": "M001", "granted": true,
		"source": "FINGERPRINT", "occurred_at": "2026-08-10T09:15:00Z",
	}, deviceAuth(key))

	if res.Code != http.StatusOK {
		t.Fatalf("device access log got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if recorded, _ := res.Body["recorded"].(bool); !recorded {
		t.Errorf("event was not recorded (body %s)", res.Raw)
	}

	// Attributed to the device AND its site, both taken from the credential.
	var siteID, deviceID int64
	var granted bool
	if err := database.DB.QueryRow(
		`SELECT site_id, device_id, granted FROM access_logs WHERE public_id = $1`,
		testEventID).Scan(&siteID, &deviceID, &granted); err != nil {
		t.Fatalf("reading stored log: %v", err)
	}
	if siteID == 0 || deviceID == 0 {
		t.Errorf("log is not attributed to a site (%d) and a device (%d)", siteID, deviceID)
	}
	if !granted {
		t.Error("granted flag was not stored")
	}
}

// A denial is the more interesting half of an audit trail, and an unrecognised
// finger has no member to reference.
func TestADeviceCanReportADenialWithNoMember(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	res := env.do(http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": testEventID, "granted": false, "source": "FINGERPRINT",
		"message": "unknown finger",
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("denial log got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	var personID sql.NullInt64
	var granted bool
	if err := database.DB.QueryRow(
		`SELECT person_id, granted FROM access_logs WHERE public_id = $1`,
		testEventID).Scan(&personID, &granted); err != nil {
		t.Fatalf("reading stored log: %v", err)
	}
	if personID.Valid {
		t.Error("an unrecognised finger was attributed to a real person")
	}
	if granted {
		t.Error("a denial was stored as granted")
	}
}

func TestDeviceAccessLogRejectsAnythingButADeviceKey(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-0001")

	body := map[string]any{
		"event_id": testEventID, "granted": true, "source": "FINGERPRINT",
	}

	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"no credential", nil},
		{"invalid device key", deviceAuth("atd_deadbeef")},
		{"site key used as a device key", deviceAuth(env.siteAKey)},
	} {
		res := env.do(http.MethodPost, "/api/v1/devices/access/log", body, tc.headers)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401 (body %s)", tc.name, res.Code, res.Raw)
		}
	}
}

// THE isolation property. There is no parameter through which a device could
// name another site, and supplying one anyway must change nothing.
func TestADeviceCannotWriteALogAgainstAnotherSite(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-A")
	env.registerDevice(env.siteBKey, "ESP32-B")

	var siteA, siteB int64
	if err := database.DB.QueryRow(
		`SELECT id FROM sites WHERE api_key = $1`, env.siteAKey).Scan(&siteA); err != nil {
		t.Fatalf("reading site A: %v", err)
	}
	if err := database.DB.QueryRow(
		`SELECT id FROM sites WHERE api_key = $1`, env.siteBKey).Scan(&siteB); err != nil {
		t.Fatalf("reading site B: %v", err)
	}

	// Site A's terminal tries every way of claiming Site B at once.
	res := env.do(http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": testEventID, "granted": true, "source": "FINGERPRINT",
		"site_id": siteB, "site_name": "Site B", "company_id": 999, "device_id": 999,
	}, deviceAuth(keyA))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	var storedSite int64
	var storedName string
	if err := database.DB.QueryRow(
		`SELECT site_id, site_name FROM access_logs WHERE public_id = $1`,
		testEventID).Scan(&storedSite, &storedName); err != nil {
		t.Fatalf("reading stored log: %v", err)
	}
	if storedSite != siteA {
		t.Errorf("log landed on site %d, want site A (%d) -- a device wrote across a site boundary",
			storedSite, siteA)
	}
	if storedName == "Site B" {
		t.Error("a device labelled its event with another site's name")
	}
}

// Retries are the NORMAL case for a terminal that queued events while offline.
// A replay must not duplicate the audit line.
func TestReplayingADoorEventDoesNotDuplicateIt(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M001", "Ada")
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	body := map[string]any{
		"event_id": testEventID, "member_id": "M001", "granted": true,
		"source": "FINGERPRINT", "occurred_at": "2026-08-10T09:15:00Z",
	}

	first := env.do(http.MethodPost, "/api/v1/devices/access/log", body, deviceAuth(key))
	if recorded, _ := first.Body["recorded"].(bool); !recorded {
		t.Fatalf("first upload was not recorded (body %s)", first.Raw)
	}

	// The terminal never heard the answer, so it tries again. Twice.
	for i := 0; i < 2; i++ {
		again := env.do(http.MethodPost, "/api/v1/devices/access/log", body, deviceAuth(key))

		// 200, NOT an error. A terminal told "failed" would retry for ever.
		if again.Code != http.StatusOK {
			t.Errorf("replay got %d, want 200 (body %s)", again.Code, again.Raw)
		}
		if duplicate, _ := again.Body["duplicate"].(bool); !duplicate {
			t.Errorf("replay was not reported as a duplicate (body %s)", again.Raw)
		}
	}

	var count int
	if err := database.DB.QueryRow(
		`SELECT count(*) FROM access_logs WHERE public_id = $1`, testEventID).Scan(&count); err != nil {
		t.Fatalf("counting logs: %v", err)
	}
	if count != 1 {
		t.Errorf("%d audit lines for one door event, want 1", count)
	}
}

// An event queued offline and uploaded hours later must record when the DOOR
// opened, not when the network came back.
func TestAQueuedEventKeepsItsOriginalTimestamp(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	env.do(http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": testEventID, "granted": true, "source": "FINGERPRINT",
		"occurred_at": "2026-08-10T09:15:00Z",
	}, deviceAuth(key))

	var occurred time.Time
	if err := database.DB.QueryRow(
		`SELECT occurred_at FROM access_logs WHERE public_id = $1`,
		testEventID).Scan(&occurred); err != nil {
		t.Fatalf("reading occurred_at: %v", err)
	}
	if got := occurred.UTC().Format(time.RFC3339); got != "2026-08-10T09:15:00Z" {
		t.Errorf("occurred_at = %s, want the device's time 2026-08-10T09:15:00Z", got)
	}
}

// A terminal whose clock has never synced sends no timestamp, and a broken
// clock must not be able to stop its own events being recorded.
func TestAnEventWithoutATimestampIsStillRecorded(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	res := env.do(http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": testEventID, "granted": true, "source": "FINGERPRINT",
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if recorded, _ := res.Body["recorded"].(bool); !recorded {
		t.Error("an event with no timestamp was not recorded")
	}
}

// Without a usable event id every retry would become a new audit line.
func TestADoorEventWithoutAnEventIdIsRefused(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	for _, body := range []map[string]any{
		{"granted": true, "source": "FINGERPRINT"},
		{"event_id": "not-a-uuid", "granted": true, "source": "FINGERPRINT"},
	} {
		res := env.do(http.MethodPost, "/api/v1/devices/access/log", body, deviceAuth(key))
		if res.Code != http.StatusBadRequest {
			t.Errorf("body %v got %d, want 400 (body %s)", body, res.Code, res.Raw)
		}
	}
}
