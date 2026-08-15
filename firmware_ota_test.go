package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
)

// Platform-driven OTA (firmware-protocol-requirements.md section 5).
//
// The catalogue held everything needed and API_SPEC.md said plainly that
// "nothing here downloads, schedules, or applies firmware". These assert the
// exact payload the firmware's FirmwareOffer expects, and the four hard rules
// it refuses an offer for -- enforced on the server as well so the failure is a
// log line naming the catalogue row rather than a fleet that silently will not
// update.

// publishFirmware inserts a catalogue row and makes it current.
func publishFirmware(t *testing.T, companyID int64, version, url, checksum string,
	size int64, mandatory bool) {

	t.Helper()
	if _, err := database.DB.Exec(`
		INSERT INTO firmware_versions
		    (company_id, version, device_type, release_channel, download_url,
		     checksum_sha256, size_bytes, is_mandatory, is_current, published_at)
		VALUES ($1, $2, 'TERMINAL', 'STABLE', NULLIF($3, ''), NULLIF($4, ''),
		        NULLIF($5, 0)::bigint, $6, TRUE, CURRENT_TIMESTAMP)`,
		companyID, version, url, checksum, size, mandatory); err != nil {
		t.Fatalf("publishing firmware %s: %v", version, err)
	}
}

const validDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// TestHeartbeatOffersFirmware asserts the exact payload shape from §5.
func TestHeartbeatOffersFirmware(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-OTA")
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `UPDATE devices SET firmware_version = '1.0.0' WHERE serial_number = 'ESP32-OTA'`)
	publishFirmware(t, companyID, "1.2.0",
		"https://updates.example.com/at-1.2.0.bin", validDigest, 1003664, false)

	res := env.do("POST", "/api/v1/devices/heartbeat",
		map[string]any{"firmware_version": "1.0.0"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", res.Code, res.Raw)
	}

	raw, present := res.Body["firmware_update"]
	if !present {
		t.Fatalf("the heartbeat response carries no firmware_update: %s", res.Raw)
	}
	offer := raw.(map[string]any)

	// The FIELD NAMES are the contract. A rename here is an OTA that silently
	// never happens.
	for field, want := range map[string]any{
		"version":         "1.2.0",
		"download_url":    "https://updates.example.com/at-1.2.0.bin",
		"checksum_sha256": validDigest,
		"size_bytes":      float64(1003664),
		"is_mandatory":    false,
	} {
		if offer[field] != want {
			t.Errorf("firmware_update.%s = %v, want %v", field, offer[field], want)
		}
	}
}

// TestHeartbeatWithholdsAnUnusableOffer covers the four hard rules in §5.
//
// The DEVICE refuses each of these too -- and its refusal is the one that
// matters, because it is the one an attacker would have to defeat. The server
// withholds as well so the failure is visible in a log naming the catalogue row,
// rather than as a fleet that silently will not update.
func TestHeartbeatWithholdsAnUnusableOffer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		url      string
		checksum string
		size     int64
	}{
		{"no digest", "https://updates.example.com/a.bin", "", 1000},
		{"upper-case digest", "https://updates.example.com/a.bin",
			strings.ToUpper(validDigest), 1000},
		{"short digest", "https://updates.example.com/a.bin", "9f86d081", 1000},
		{"no size", "https://updates.example.com/a.bin", validDigest, 0},
		{"plaintext url", "http://updates.example.com/a.bin", validDigest, 1000},
		{"no url", "", validDigest, 1000},
		{"url too long for the device buffer",
			"https://updates.example.com/" + strings.Repeat("a", 120) + ".bin",
			validDigest, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			key := env.registerDevice(env.siteAKey, "ESP32-BAD-OTA")
			companyID := companyIDBySlug(t, "one")

			mustExec(t, `UPDATE devices SET firmware_version = '1.0.0'
			             WHERE serial_number = 'ESP32-BAD-OTA'`)
			publishFirmware(t, companyID, "1.2.0", tc.url, tc.checksum, tc.size, false)

			res := env.do("POST", "/api/v1/devices/heartbeat", nil, deviceAuth(key))
			if res.Code != http.StatusOK {
				t.Fatalf("heartbeat = %d", res.Code)
			}
			if _, present := res.Body["firmware_update"]; present {
				t.Errorf("an unusable build was offered anyway: %s", res.Raw)
			}
		})
	}
}

// TestHeartbeatDoesNotOfferWhatIsAlreadyRunning.
func TestHeartbeatDoesNotOfferWhatIsAlreadyRunning(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-CURRENT")
	companyID := companyIDBySlug(t, "one")

	publishFirmware(t, companyID, "1.2.0",
		"https://updates.example.com/at-1.2.0.bin", validDigest, 1003664, false)
	mustExec(t, `UPDATE devices SET firmware_version = '1.2.0'
	             WHERE serial_number = 'ESP32-CURRENT'`)

	res := env.do("POST", "/api/v1/devices/heartbeat", nil, deviceAuth(key))
	if _, present := res.Body["firmware_update"]; present {
		t.Errorf("a terminal was offered the build it is already running: %s", res.Raw)
	}
}

// TestFirmwareOfferDoesNotCrossTenants. "Current" is a per-tenant target.
func TestFirmwareOfferDoesNotCrossTenants(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-TENANT-OTA")

	// Published by company TWO only.
	publishFirmware(t, companyIDBySlug(t, "two"), "9.9.9",
		"https://updates.example.com/other.bin", validDigest, 1000, true)
	mustExec(t, `UPDATE devices SET firmware_version = '1.0.0'
	             WHERE serial_number = 'ESP32-TENANT-OTA'`)

	res := env.do("POST", "/api/v1/devices/heartbeat", nil, deviceAuth(key))
	if _, present := res.Body["firmware_update"]; present {
		t.Fatalf("a terminal was offered another tenant's firmware: %s", res.Raw)
	}
}

// TestHeartbeatStillAnswersWithoutAnOffer. The response a fleet that is up to
// date sees must be exactly what it saw before this field existed.
func TestHeartbeatStillAnswersWithoutAnOffer(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-NOOTA")

	res := env.do("POST", "/api/v1/devices/heartbeat", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d", res.Code)
	}
	if _, present := res.Body["firmware_update"]; present {
		t.Error("firmware_update is present with nothing to offer; it must be omitted")
	}
	// The three fields the firmware actually parses.
	for _, field := range []string{"protocol_version", "pending_jobs", "server_time"} {
		if _, present := res.Body[field]; !present {
			t.Errorf("the heartbeat response lost %q", field)
		}
	}
}

// TestServerTimeIsUTCWithAZ.
//
// The firmware adopts server_time as a clock when it cannot reach NTP, and it
// REFUSES a numeric offset rather than mis-parsing it -- so a change that made
// this a local time would take the fleet's clock with it, and every queued door
// event would be wrong by a fixed amount.
func TestServerTimeIsUTCWithAZ(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-CLOCK")

	res := env.do("POST", "/api/v1/devices/heartbeat", nil, deviceAuth(key))

	serverTime, ok := res.Body["server_time"].(string)
	if !ok {
		t.Fatalf("server_time is not a string: %v", res.Body["server_time"])
	}
	if !strings.HasSuffix(serverTime, "Z") {
		t.Errorf("server_time = %q; the terminal refuses anything but a Z suffix", serverTime)
	}
}
