package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// FW-01 / SYN-01: a roster larger than the terminal that has to hold it.
//
// THE DEFECT THESE WOULD HAVE CAUGHT. The server built a FULL_SYNC snapshot
// carrying every permitted person without ever asking how many the terminal can
// store. The firmware refuses an oversized roster WHOLESALE rather than
// truncating it -- which is the right call, because a short roster reads as a
// list of deletions -- so the job failed, retried ten times, parked FAILED, and
// the terminal carried on serving whatever roster it already had. Nobody was
// told. The door worked, for the wrong set of people, indefinitely.
//
// THE PROPERTY BEING PROTECTED, and it cuts both ways:
//
//   - a KNOWN capacity that is exceeded must stop the snapshot and say so;
//   - an UNKNOWN capacity must change nothing at all, because every terminal in
//     the field today reports none and a server that guessed a ceiling would
//     break working installations.

// reportCapacity sends a heartbeat carrying what the terminal can hold, which is
// the contract AI #2 has to implement. Using the real endpoint rather than an
// UPDATE keeps the test honest about how the number is meant to arrive.
func reportCapacity(t *testing.T, env *testEnv, deviceKey string, capacity int) {
	t.Helper()

	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{
		"status": "ONLINE", "member_capacity": capacity,
	}, deviceAuth(deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat with capacity %d: got %d, want 200 (body %s)",
			capacity, res.Code, res.Raw)
	}
}

func deviceIDBySerial(t *testing.T, serial string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM devices WHERE serial_number = $1`, serial).Scan(&id); err != nil {
		t.Fatalf("resolving device %s: %v", serial, err)
	}
	return id
}

func fullSyncJobCount(t *testing.T, deviceID int64) int {
	t.Helper()
	return queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND job_type = 'FULL_SYNC'`,
		deviceID)
}

// TestHeartbeatRecordsReportedCapacity is the server half of the contract.
//
// Additive and optional: the field is absent from every heartbeat the current
// fleet sends, and a terminal that starts sending it needs no other change.
func TestHeartbeatRecordsReportedCapacity(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-CAP")

	// Before it reports, the platform knows nothing -- which is NOT zero.
	if queryBool(t, `SELECT member_capacity IS NULL FROM devices WHERE serial_number = 'ESP32-CAP'`) != true {
		t.Fatal("a terminal that has never reported a capacity should have none stored")
	}

	reportCapacity(t, env, key, 256)

	if got := queryInt(t,
		`SELECT member_capacity FROM devices WHERE serial_number = 'ESP32-CAP'`); got != 256 {
		t.Errorf("member_capacity = %d, want 256", got)
	}
	if !queryBool(t,
		`SELECT member_capacity_reported_at IS NOT NULL FROM devices WHERE serial_number = 'ESP32-CAP'`) {
		t.Error("the reporting time was not stamped, so the age of the figure is unknowable")
	}
}

// TestAReportedCapacityIsNotForgotten covers the downgrade case.
//
// A terminal flashed back to a build without the field must not silently revert
// to "unknown", because that switches the guard off for a door whose hardware
// has not changed.
func TestAReportedCapacityIsNotForgotten(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-CAP")

	reportCapacity(t, env, key, 64)

	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat",
		map[string]any{"status": "ONLINE"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat without the field: got %d, want 200", res.Code)
	}

	if got := queryInt(t,
		`SELECT member_capacity FROM devices WHERE serial_number = 'ESP32-CAP'`); got != 64 {
		t.Errorf("member_capacity = %d after a heartbeat omitting it, want 64", got)
	}
}

// TestAnImpossibleCapacityIsNotStored. Zero would mean a terminal that can hold
// nobody, which the firmware cannot be in and which would take that door out of
// service through a field nobody validates by eye.
func TestAnImpossibleCapacityIsNotStored(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-CAP")

	for _, bad := range []int{0, -1} {
		res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{
			"status": "ONLINE", "member_capacity": bad,
		}, deviceAuth(key))

		// The heartbeat still succeeds. A garbage capacity must not cost a
		// terminal its liveness reporting.
		if res.Code != http.StatusOK {
			t.Errorf("heartbeat reporting %d: got %d, want 200", bad, res.Code)
		}
		if !queryBool(t,
			`SELECT member_capacity IS NULL FROM devices WHERE serial_number = 'ESP32-CAP'`) {
			t.Errorf("a capacity of %d was stored", bad)
		}
	}
}

// TestOversizedRosterIsRefusedRatherThanQueued is the finding itself.
func TestOversizedRosterIsRefusedRatherThanQueued(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-SMALL")
	reportCapacity(t, env, key, 2)

	for _, id := range []string{"M-1", "M-2", "M-3", "M-4", "M-5"} {
		env.createMember(env.siteAKey, id, "Person "+id)
	}

	deviceID := deviceIDBySerial(t, "ESP32-SMALL")
	before := fullSyncJobCount(t, deviceID)

	res := env.do(http.MethodPost, "/api/v1/devices/ESP32-SMALL/resync", nil,
		siteAuth(env.siteAKey))

	// 409, not 500: nothing failed. The platform is declining to promise
	// something it cannot deliver.
	if res.Code != http.StatusConflict {
		t.Fatalf("resync got %d, want 409 (body %s)", res.Code, res.Raw)
	}

	// THE NUMBERS ARE IN THE RESPONSE. "Over capacity" tells an operator to
	// open a ticket; "holds 2, needs 5" tells them what to buy.
	if got := res.Body["capacity"]; got != float64(2) {
		t.Errorf("capacity = %v, want 2 (body %s)", got, res.Raw)
	}
	if got := res.Body["roster_size"]; got != float64(5) {
		t.Errorf("roster_size = %v, want 5 (body %s)", got, res.Raw)
	}

	if after := fullSyncJobCount(t, deviceID); after != before {
		t.Errorf("%d FULL_SYNC jobs were queued for a terminal that would refuse them",
			after-before)
	}
}

// TestARefusedSnapshotLeavesTheExistingQueueAlone.
//
// Compaction cancels the outstanding queue and replaces it with a snapshot. If
// the refusal happened after the cancellation, the terminal would be left with
// no queued work AND a roster it can never be brought in line with -- strictly
// worse than the backlog it started with. The order is the safety property.
func TestARefusedSnapshotLeavesTheExistingQueueAlone(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-SMALL")
	reportCapacity(t, env, key, 1)

	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")

	deviceID := deviceIDBySerial(t, "ESP32-SMALL")
	pendingBefore := queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND status = 'PENDING'`, deviceID)
	if pendingBefore == 0 {
		t.Fatal("fixture built no pending work, so this test would prove nothing")
	}

	env.do(http.MethodPost, "/api/v1/devices/ESP32-SMALL/resync", nil, siteAuth(env.siteAKey))

	pendingAfter := queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND status = 'PENDING'`, deviceID)
	if pendingAfter != pendingBefore {
		t.Errorf("pending jobs went from %d to %d: the queue was cancelled and not replaced",
			pendingBefore, pendingAfter)
	}
	if n := queryInt(t,
		`SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND status = 'CANCELLED'`,
		deviceID); n != 0 {
		t.Errorf("%d jobs were cancelled by a refused compaction", n)
	}
}

// TestRosterOverflowIsVisibleToAnOperator.
//
// SYN-01's complaint was not that the failure existed but that nothing said so.
// Three surfaces, because they answer different questions: the terminal's own
// state, the field an operator already reads when a terminal is unhappy, and
// the trail that can be alarmed on.
func TestRosterOverflowIsVisibleToAnOperator(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-SMALL")
	reportCapacity(t, env, key, 1)

	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")
	env.do(http.MethodPost, "/api/v1/devices/ESP32-SMALL/resync", nil, siteAuth(env.siteAKey))

	deviceID := deviceIDBySerial(t, "ESP32-SMALL")

	if !queryBool(t, `SELECT roster_overflow_at IS NOT NULL FROM devices WHERE id = $1`, deviceID) {
		t.Error("the terminal is not marked as over capacity")
	}
	if got := queryInt(t, `SELECT roster_overflow_count FROM devices WHERE id = $1`, deviceID); got != 2 {
		t.Errorf("roster_overflow_count = %d, want 2", got)
	}
	if !queryBool(t, `SELECT last_apply_error IS NOT NULL FROM devices WHERE id = $1`, deviceID) {
		t.Error("last_apply_error is empty, so the console's 'why is this terminal unhappy' " +
			"field says nothing about the reason it is stuck")
	}
	if n := queryInt(t,
		`SELECT count(*) FROM events WHERE device_id = $1 AND event_type = $2`,
		deviceID, models.EventRosterOverflow); n != 1 {
		t.Errorf("%d %s events, want 1", n, models.EventRosterOverflow)
	}
}

// TestOverflowClearsOnceTheRosterFits. A stale "over capacity" badge is its own
// kind of lie -- an operator who removed people would keep being told to buy
// hardware they no longer need.
func TestOverflowClearsOnceTheRosterFits(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-SMALL")
	reportCapacity(t, env, key, 1)

	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")
	env.do(http.MethodPost, "/api/v1/devices/ESP32-SMALL/resync", nil, siteAuth(env.siteAKey))

	deviceID := deviceIDBySerial(t, "ESP32-SMALL")
	if !queryBool(t, `SELECT roster_overflow_at IS NOT NULL FROM devices WHERE id = $1`, deviceID) {
		t.Fatal("fixture did not produce an overflow")
	}

	// Raise the ceiling rather than removing people: same convergence, and it
	// is the remedy an operator actually applies (they replace the hardware).
	reportCapacity(t, env, key, 64)

	res := env.do(http.MethodPost, "/api/v1/devices/ESP32-SMALL/resync", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("resync after raising capacity: got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if queryBool(t, `SELECT roster_overflow_at IS NOT NULL FROM devices WHERE id = $1`, deviceID) {
		t.Error("the terminal is still flagged as over capacity after it fits")
	}
}

// TestAnUnreportedCapacityChangesNothing is the compatibility guarantee, and it
// is the most important test in this file.
//
// Every terminal in the field reports no capacity. If an unknown ceiling were
// enforced against ANY assumed number, this is the test that would fail -- and
// the failure in production would be a working installation whose terminals
// stopped being given rosters after a server upgrade.
func TestAnUnreportedCapacityChangesNothing(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-SILENT")

	// Comfortably past AssumedMemberCapacity, which is the number the server
	// warns on and must never refuse on.
	for i := 0; i < database.AssumedMemberCapacity+5; i++ {
		env.createMember(env.siteAKey, "M-"+itoa(int64(i)), "Person")
	}

	res := env.do(http.MethodPost, "/api/v1/devices/ESP32-SILENT/resync", nil,
		siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("resync of a terminal that reported no capacity: got %d, want 200 (body %s)",
			res.Code, res.Raw)
	}

	deviceID := deviceIDBySerial(t, "ESP32-SILENT")
	if fullSyncJobCount(t, deviceID) == 0 {
		t.Error("no snapshot was queued for a terminal whose ceiling is unknown")
	}
	if queryBool(t, `SELECT roster_overflow_at IS NOT NULL FROM devices WHERE id = $1`, deviceID) {
		t.Error("a terminal that never reported a capacity was marked over capacity")
	}
}

// TestPollingSurvivesAnUnfittableRoster.
//
// Automatic compaction runs on the device's own poll. Failing that poll would
// take away the work the terminal CAN do on top of the work it cannot, and turn
// a capacity problem into a terminal that has stopped talking to the platform.
func TestPollingSurvivesAnUnfittableRoster(t *testing.T) {
	// Compaction normally needs a 500-job backlog. Lowering the threshold makes
	// the automatic path reachable without building one.
	t.Setenv("SYNC_COMPACTION_THRESHOLD", "1")

	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-SMALL")
	reportCapacity(t, env, key, 1)

	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")
	env.createMember(env.siteAKey, "M-3", "Alan")

	res := env.do(http.MethodGet, "/api/v1/devices/jobs?limit=200", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("job poll got %d, want 200 -- a terminal over capacity must still "+
			"be able to collect the work it can apply (body %s)", res.Code, res.Raw)
	}
	if got, _ := res.Body["count"].(float64); got == 0 {
		t.Error("the poll returned no jobs, so the terminal cannot converge at all")
	}
}

// TestRelocationIsRefusedWhenTheDestinationDoesNotFit.
//
// A terminal carried to a building whose roster it cannot hold would arrive
// unable to be told who is allowed. The refusal happens while the operator is
// still looking at the screen, and the move is rolled back whole -- the unit is
// still at the site it started at.
func TestRelocationIsRefusedWhenTheDestinationDoesNotFit(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"relocate@example.com", models.RoleAdmin)

	key := env.registerDevice(env.siteAKey, "ESP32-MOVE")
	reportCapacity(t, env, key, 1)

	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")

	siteA := siteIDByKey(t, env.siteAKey)
	siteB := siteIDByKey(t, env.siteBKey)
	var siteBPublic string
	if err := database.DB.QueryRow(
		`SELECT public_id FROM sites WHERE id = $1`, siteB).Scan(&siteBPublic); err != nil {
		t.Fatalf("resolving site B: %v", err)
	}

	code, body := consoleCall(t, env.router, http.MethodPut,
		"/api/v1/console/terminals/ESP32-MOVE/site",
		`{"site_id":"`+siteBPublic+`"}`, token, csrf)

	if code != http.StatusConflict {
		t.Fatalf("move got %d, want 409 (body %v)", code, body)
	}

	if got := queryInt(t,
		`SELECT site_id FROM devices WHERE serial_number = 'ESP32-MOVE'`); int64(got) != siteA {
		t.Errorf("the terminal moved anyway: site_id = %d, want %d (site A)", got, siteA)
	}
}

// TestTerminalDetailReportsCapacityHeadroom.
//
// The console read is where an operator plans, so it carries the live roster
// size beside the ceiling. `over_capacity` stays false for a terminal that has
// reported nothing, because the platform does not know its ceiling and must not
// imply that it does.
func TestTerminalDetailReportsCapacityHeadroom(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"detail@example.com", models.RoleAdmin)

	key := env.registerDevice(env.siteAKey, "ESP32-DETAIL")
	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")

	code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-DETAIL", "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("terminal detail got %d, want 200 (body %v)", code, body)
	}

	if got := body["roster_size"]; got != float64(2) {
		t.Errorf("roster_size = %v, want 2", got)
	}
	if _, present := body["member_capacity"]; present {
		t.Error("member_capacity is present for a terminal that has never reported one; " +
			"absent is what makes 'unknown' distinguishable from a number")
	}
	if got := body["over_capacity"]; got != false {
		t.Errorf("over_capacity = %v for a terminal with an unknown ceiling, want false", got)
	}

	reportCapacity(t, env, key, 128)

	code, body = consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/terminals/ESP32-DETAIL", "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("terminal detail after reporting: got %d (body %v)", code, body)
	}
	if got := body["member_capacity"]; got != float64(128) {
		t.Errorf("member_capacity = %v, want 128", got)
	}
}
