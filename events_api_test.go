package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The event trail and the authorization write surface, through the router.
//
// SEC-08: an operator could not read a door event at all. The only route that
// returned access logs authenticated with the SITE API KEY -- the provisioning
// secret installed on hardware -- so seeing who came in meant holding the
// credential that registers terminals and rotates their keys.
//
// APP-04: there was no generic event model, so nothing but a door open/close
// could be recorded in the first place.
//
// These go through NewRouter, so the session, CSRF, role and grant chain all run.

// seedEvent writes one event directly, for the read paths to find.
func seedEvent(t *testing.T, companyID, siteID, deviceID, personID int64,
	eventType, decision, reason string, occurredAt time.Time) string {

	t.Helper()
	id, err := database.RecordAccessEvent(database.AccessEvent{
		CompanyID: companyID, SiteID: siteID, DeviceID: deviceID, PersonID: personID,
		EventType: eventType, Decision: decision, ReasonCode: reason,
		SubjectExternalID: "P-AUTH",
		OccurredAt:        occurredAt, OccurredAtTrusted: true,
	})
	if err != nil {
		t.Fatalf("seeding event: %v", err)
	}
	return id
}

func TestConsoleEventsAreReadableByAnOperator(t *testing.T) {
	f := newAuthFixture(t)

	now := time.Now().UTC()
	seedEvent(t, f.companyID, f.siteID, f.deviceID, f.personID,
		models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, now)
	seedEvent(t, f.companyID, f.siteID, f.deviceID, f.personID,
		models.EventAccessDenied, models.DecisionDenied, models.ReasonNoPermission,
		now.Add(-time.Hour))

	_, token, _ := consoleOperatorSession(t, f.env.router, f.companyID,
		"events-viewer@example.com", models.RoleViewer)

	code, body := consoleCall(t, f.env.router, "GET", "/api/v1/console/events", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /console/events = %d, want 200: %v", code, body)
	}

	events := listOf(t, body, "events")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %v", len(events), body)
	}

	// Newest first, and every field an operator needs to act on the refusal.
	first := events[0].(map[string]any)
	if first["decision"] != models.DecisionGranted {
		t.Errorf("first event decision = %v, want the newest (%s)",
			first["decision"], models.DecisionGranted)
	}

	second := events[1].(map[string]any)
	if second["reason"] != models.ReasonNoPermission {
		t.Errorf("denied event reason = %v, want %s -- a denial without a reason "+
			"is not actionable", second["reason"], models.ReasonNoPermission)
	}
	for _, field := range []string{"device_serial", "site_name", "person_id", "occurred_at"} {
		if second[field] == nil || second[field] == "" {
			t.Errorf("event is missing %q: %v", field, second)
		}
	}
}

// TestConsoleEventsDoNotCrossCompanies is the tenancy assertion: an operator must
// never see another tenant's door traffic.
func TestConsoleEventsDoNotCrossCompanies(t *testing.T) {
	f := newAuthFixture(t)

	otherCompany := companyIDBySlug(t, "two")
	f.env.registerDevice(f.env.siteCKey, "ESP32-TENANT-2")
	var otherSiteID, otherDeviceID int64
	mustScan(t, `SELECT site_id, id FROM devices WHERE serial_number = 'ESP32-TENANT-2'`,
		&otherSiteID, &otherDeviceID)
	otherPerson := seedPerson(t, otherCompany, "P-TWO", "Other Tenant Person")

	seedEvent(t, otherCompany, otherSiteID, otherDeviceID, otherPerson,
		models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed,
		time.Now().UTC())

	_, token, _ := consoleOperatorSession(t, f.env.router, f.companyID,
		"tenant-one@example.com", models.RoleAdmin)

	code, body := consoleCall(t, f.env.router, "GET", "/api/v1/console/events", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /console/events = %d: %v", code, body)
	}
	if events := listOf(t, body, "events"); len(events) != 0 {
		t.Fatalf("an operator saw %d events belonging to another tenant", len(events))
	}
}

// TestConsoleEventsRespectSiteGrants. A site-scoped operator who cannot open a
// terminal's detail page must not be able to read every presentation at it.
func TestConsoleEventsRespectSiteGrants(t *testing.T) {
	f := newAuthFixture(t)

	// A terminal at Site B, and an event there.
	f.env.registerDevice(f.env.siteBKey, "ESP32-SITE-B")
	var siteBID, deviceBID int64
	mustScan(t, `SELECT site_id, id FROM devices WHERE serial_number = 'ESP32-SITE-B'`,
		&siteBID, &deviceBID)

	now := time.Now().UTC()
	seedEvent(t, f.companyID, f.siteID, f.deviceID, f.personID,
		models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, now)
	seedEvent(t, f.companyID, siteBID, deviceBID, f.personID,
		models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, now)

	// A MANAGER scoped to Site A only. ADMIN and OWNER are never site-scoped, so
	// the role has to be below that for the grant to mean anything.
	user, token, _ := consoleOperatorSession(t, f.env.router, f.companyID,
		"scoped@example.com", models.RoleManager)
	mustExec(t, `INSERT INTO user_site_grants (user_id, site_id) VALUES ($1, $2)`,
		user.ID, f.siteID)

	code, body := consoleCall(t, f.env.router, "GET", "/api/v1/console/events", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /console/events = %d: %v", code, body)
	}

	events := listOf(t, body, "events")
	if len(events) != 1 {
		t.Fatalf("a Site A operator saw %d events, want only Site A's one", len(events))
	}
	if serial := events[0].(map[string]any)["device_serial"]; serial != f.serial {
		t.Errorf("saw an event from %v, want only %s", serial, f.serial)
	}
}

// TestConsoleEventFiltersNarrowRatherThanMislead.
//
// A malformed timestamp is REJECTED rather than ignored. Silently dropping
// `from=lastweek` and answering with the whole trail looks like an answer to the
// question that was asked, and an operator reading a year of events believing
// they are reading a week is worse off than one who gets an error.
func TestConsoleEventFiltersNarrowRatherThanMislead(t *testing.T) {
	f := newAuthFixture(t)

	now := time.Now().UTC()
	seedEvent(t, f.companyID, f.siteID, f.deviceID, f.personID,
		models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, now)
	seedEvent(t, f.companyID, f.siteID, f.deviceID, f.personID,
		models.EventAccessDenied, models.DecisionDenied, models.ReasonNoPermission,
		now.Add(-48*time.Hour))

	_, token, _ := consoleOperatorSession(t, f.env.router, f.companyID,
		"filters@example.com", models.RoleAdmin)

	// Decision filter.
	code, body := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/events?decision=DENIED", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("filtered read = %d: %v", code, body)
	}
	events := listOf(t, body, "events")
	if len(events) != 1 || events[0].(map[string]any)["decision"] != models.DecisionDenied {
		t.Fatalf("decision filter returned %v", events)
	}

	// Time filter, on occurred_at: what happened at the door, not when the
	// server heard about it.
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	code, body = consoleCall(t, f.env.router, "GET",
		"/api/v1/console/events?from="+since, "", token, "")
	if code != http.StatusOK {
		t.Fatalf("time-filtered read = %d: %v", code, body)
	}
	if events := listOf(t, body, "events"); len(events) != 1 {
		t.Fatalf("the `from` filter returned %d events, want 1", len(events))
	}

	// A malformed one is an error, not a silently unfiltered answer.
	code, body = consoleCall(t, f.env.router, "GET",
		"/api/v1/console/events?from=lastweek", "", token, "")
	if code != http.StatusBadRequest {
		t.Fatalf("a malformed `from` returned %d, want 400: %v", code, body)
	}

	// An unknown decision is refused rather than matching nothing silently.
	code, _ = consoleCall(t, f.env.router, "GET",
		"/api/v1/console/events?decision=MAYBE", "", token, "")
	if code != http.StatusBadRequest {
		t.Errorf("an unknown decision returned %d, want 400", code)
	}
}

// ---------------------------------------------------------------------------
// The device door-event path
// ---------------------------------------------------------------------------

// TestDeviceAccessLogAlsoWritesTheEventTrail.
//
// access_logs is a FROZEN wire contract that deployed firmware uploads to, so it
// keeps working; the typed event is written beside it from the same handler, so
// the two can never disagree about what a terminal reported.
func TestDeviceAccessLogAlsoWritesTheEventTrail(t *testing.T) {
	f := newAuthFixture(t)
	key := f.env.registerDevice(f.env.siteAKey, "ESP32-LOGGER")

	body := map[string]any{
		"event_id":  "11111111-2222-3333-4444-555555555555",
		"member_id": "P-AUTH",
		"granted":   true,
		"source":    "FINGERPRINT",
		"message":   "ok",
	}

	res := f.env.do("POST", "/api/v1/devices/access/log", body, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("device access log = %d, want 200: %s", res.Code, res.Raw)
	}

	// Both tables carry it.
	if logs := queryInt(t, `SELECT count(*) FROM access_logs WHERE company_id = $1`, f.companyID); logs != 1 {
		t.Errorf("access_logs rows = %d, want 1 -- the frozen contract must keep working", logs)
	}
	if events := queryInt(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID); events != 1 {
		t.Errorf("events rows = %d, want 1", events)
	}

	// The device's event id is the idempotency key for BOTH, so a retry --
	// which the terminal makes precisely because it did not hear the answer --
	// duplicates neither.
	res = f.env.do("POST", "/api/v1/devices/access/log", body, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("replayed device access log = %d, want 200", res.Code)
	}
	if events := queryInt(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID); events != 1 {
		t.Errorf("a replayed upload produced %d events, want 1", events)
	}
}

// TestDivergenceBetweenTerminalAndPlatformIsRecorded.
//
// THE SIGNAL THIS PLATFORM PREVIOUSLY COULD NOT PRODUCE. A terminal running on a
// cache it synced before a permission was revoked will admit somebody the
// platform would now refuse. The event records what actually happened -- the
// person went through -- and flags that the platform disagreed, which is exactly
// what an offline grace window is a trade against.
func TestDivergenceBetweenTerminalAndPlatformIsRecorded(t *testing.T) {
	f := newAuthFixture(t)
	key := f.env.registerDevice(f.env.siteAKey, "ESP32-STALE")

	// The person has NO permission, so the platform would refuse. The terminal
	// reports that it let them in anyway, which is what a stale cache does.
	res := f.env.do("POST", "/api/v1/devices/access/log", map[string]any{
		"event_id":  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"member_id": "P-AUTH",
		"granted":   true,
		"source":    "FINGERPRINT",
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("device access log = %d: %s", res.Code, res.Raw)
	}

	var decision, reason, payload string
	mustScan(t, `SELECT decision, COALESCE(reason_code, ''), COALESCE(payload::text, '')
	               FROM events LIMIT 1`, &decision, &reason, &payload)

	// The recorded decision is the TERMINAL'S, because that is the one that
	// actually released the lock. Substituting the server's re-evaluation would
	// produce a trail that disagrees with reality -- an operator reading
	// "DENIED" about somebody who demonstrably walked in.
	if decision != models.DecisionGranted {
		t.Errorf("recorded decision = %q, want the terminal's own (%s)",
			decision, models.DecisionGranted)
	}
	if reason != models.ReasonNoPermission {
		t.Errorf("reason = %q, want the platform's verdict (%s)",
			reason, models.ReasonNoPermission)
	}
	// jsonb re-serialises with a space after the colon, so the assertion
	// matches the key rather than a formatting of the whole pair.
	if !strings.Contains(payload, `"diverged"`) {
		t.Errorf("the divergence was not flagged: %s", payload)
	}
	if !strings.Contains(payload, `"NO_PERMISSION"`) {
		t.Errorf("the platform's own verdict was not recorded beside it: %s", payload)
	}

	// And when the two AGREE, nothing is flagged -- otherwise the signal would
	// be noise and nobody would act on it.
	f.allowCompanyWide(t)
	res = f.env.do("POST", "/api/v1/devices/access/log", map[string]any{
		"event_id":  "bbbbbbbb-cccc-dddd-eeee-ffffffffffff",
		"member_id": "P-AUTH",
		"granted":   true,
		"source":    "FINGERPRINT",
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("second device access log = %d", res.Code)
	}

	var agreed string
	mustScan(t, `SELECT COALESCE(payload::text, '') FROM events
	              WHERE public_id = 'bbbbbbbb-cccc-dddd-eeee-ffffffffffff'`, &agreed)
	if strings.Contains(agreed, `"diverged"`) {
		t.Errorf("an agreeing decision was flagged as divergent: %s", agreed)
	}
}
