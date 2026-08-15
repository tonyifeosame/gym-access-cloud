package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Relocation convergence.
//
// THE DEFECT. MoveTerminal updated devices.site_id, cancelled the outstanding
// queue and marked placements REMOVING -- and enqueued NOTHING. A relocated
// terminal was therefore left pointing at its new site with an EMPTY QUEUE and
// no durable instruction to change anything, so it went on admitting the OLD
// site's people out of its cached roster until the background roster reconciler
// happened to run, up to fifteen minutes later. The console reported the move as
// complete the moment it returned.
//
// THE FIX uses the mechanism that already existed rather than inventing another:
// a FULL_SYNC snapshot, enqueued in the SAME TRANSACTION as the move. FULL_SYNC
// is a set difference -- "your local set should be exactly this, remove anything
// else" -- so one job both withdraws the old site's people and establishes the
// new site's.
//
// The firmware already implements exactly that (sync_engine.cpp `reconcile`): a
// member absent from the roster is removed from the member table AND its
// template is erased from the sensor, and the removal is reported back as a
// REMOVED placement. No firmware change was needed or made.

// relocationFixture is one terminal at Site A, with people at both sites.
type relocationFixture struct {
	env       *testEnv
	companyID int64
	token     string
	csrf      string
	serial    string
	deviceID  int64
	siteAID   int64
	siteBID   int64
}

// newRelocationFixture builds the interesting case: a person who may open
// terminals at Site A only, and one who may open Site B only.
//
// Site-scoped rather than company-scoped on purpose. A company-scoped person is
// permitted at both sites, so they would stay on the roster either way and could
// not distinguish a working relocation from a broken one.
func newRelocationFixture(t *testing.T) *relocationFixture {
	t.Helper()

	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	// Deny-by-default, so every roster entry below is there because a
	// permission put it there.
	mustExec(t, `UPDATE companies SET default_person_access = 'NONE' WHERE id = $1`, companyID)

	env.registerDevice(env.siteAKey, "ESP32-MOVER")

	siteAID := siteIDOf(t, "Site A")
	siteBID := siteIDOf(t, "Site B")

	siteAPerson := seedPerson(t, companyID, "P-SITE-A", "Site A Person")
	siteBPerson := seedPerson(t, companyID, "P-SITE-B", "Site B Person")

	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active)
	             VALUES ($1, $2, 'SITE', $3, 'ALLOW', TRUE)`, companyID, siteAPerson, siteAID)
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active)
	             VALUES ($1, $2, 'SITE', $3, 'ALLOW', TRUE)`, companyID, siteBPerson, siteBID)

	deviceID := deviceIDOf(t, "ESP32-MOVER")

	// Converge the terminal on Site A first, so the relocation has something
	// real to undo.
	if _, _, err := database.ReconcileDeviceRoster(deviceID); err != nil {
		t.Fatalf("converging on Site A: %v", err)
	}

	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"relocation-admin@example.com", models.RoleAdmin)

	return &relocationFixture{
		env: env, companyID: companyID, token: token, csrf: csrf,
		serial: "ESP32-MOVER", deviceID: deviceID,
		siteAID: siteAID, siteBID: siteBID,
	}
}

// move relocates the terminal through the console, as an operator would.
func (f *relocationFixture) move(t *testing.T, siteName string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, "PUT",
		"/api/v1/console/terminals/"+f.serial+"/site",
		`{"site_id":"`+sitePublicIDByName(t, siteName)+`"}`, f.token, f.csrf)
}

// fullSyncRoster is the authoritative member set from the terminal's pending
// FULL_SYNC snapshot, or nil when it has not been sent one.
//
// This is the answer to "who will be able to open this terminal once it next
// syncs", because FULL_SYNC is a set difference: anything absent from it is
// removed from the device, template and all.
func fullSyncRoster(t *testing.T, serial string) []string {
	t.Helper()

	var payload string
	mustScan(t, `
		SELECT COALESCE(max(j.payload->>'member_ids'), '')
		  FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = '`+serial+`'
		   AND j.job_type = 'FULL_SYNC'
		   AND j.status = 'PENDING'`, &payload)

	return parseExternalIDs(payload)
}

// pendingPersonIDs is who the terminal has been told about through ordinary
// PERSON jobs -- what roster reconciliation produces, before any snapshot.
//
// Separate from fullSyncRoster because the two represent the roster
// DIFFERENTLY: reconciliation queues one CREATE per person, while a relocation
// queues one snapshot describing the whole set. A test asserting the state
// before a move has to read the first, and one asserting the state after it has
// to read the second.
func pendingPersonIDs(t *testing.T, serial string) []string {
	t.Helper()

	rows, err := database.DB.Query(`
		SELECT j.entity_external_id
		  FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1
		   AND j.entity_type = 'PERSON'
		   AND j.job_type <> 'DELETE'
		   AND j.status = 'PENDING'
		   AND j.entity_external_id IS NOT NULL`, serial)
	if err != nil {
		t.Fatalf("reading queued people: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scanning queued person: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

// parseExternalIDs reads the JSON array of ids a FULL_SYNC payload carries.
func parseExternalIDs(payload string) []string {
	if payload == "" || payload == "[]" {
		return nil
	}

	parts := strings.Split(strings.Trim(payload, "[]"), ",")
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		if id := strings.Trim(strings.TrimSpace(part), `"`); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// ---------------------------------------------------------------------------
// 1, 2 and 3: the move, the removal, and the new roster
// ---------------------------------------------------------------------------

// TestRelocationQueuesTheNewRosterImmediately is the regression.
//
// Requirements 1, 2 and 3 in one assertion set, because they are one behaviour:
// the snapshot that removes Site A's people is the same snapshot that adds Site
// B's, which is exactly why a set difference was the right instrument.
func TestRelocationQueuesTheNewRosterImmediately(t *testing.T) {
	f := newRelocationFixture(t)

	// Before: the terminal is converging on Site A's person, through the
	// ordinary PERSON jobs reconciliation queued.
	before := pendingPersonIDs(t, f.serial)
	if !contains(before, "P-SITE-A") {
		t.Fatalf("the terminal was not holding Site A's person before the move: %v", before)
	}
	if contains(before, "P-SITE-B") {
		t.Fatalf("Site B's person was already queued before the move: %v", before)
	}

	code, body := f.move(t, "Site B")
	if code != http.StatusOK {
		t.Fatalf("move = %d: %v", code, body)
	}

	// The work exists NOW -- not after a reconciler pass. This is the whole
	// defect: the queue used to be empty at this point.
	after := fullSyncRoster(t, f.serial)
	if after == nil {
		t.Fatal("no FULL_SYNC snapshot was queued by the move, so nothing tells the " +
			"terminal to stop honouring the old site's roster")
	}

	if contains(after, "P-SITE-A") {
		t.Errorf("Site A's person is still on the relocated terminal's roster: %v", after)
	}
	if !contains(after, "P-SITE-B") {
		t.Errorf("Site B's person was not synchronized to the moved terminal: %v", after)
	}
}

// TestRelocationEnqueuesDurableWorkInTheSameTransaction.
//
// Requirement 4: no window in which the terminal has moved but nothing durable
// says so. The move and the work that makes it true commit together, so a crash
// immediately afterwards cannot leave a moved terminal with an empty queue.
func TestRelocationEnqueuesDurableWorkInTheSameTransaction(t *testing.T) {
	f := newRelocationFixture(t)

	if code, body := f.move(t, "Site B"); code != http.StatusOK {
		t.Fatalf("move = %d: %v", code, body)
	}

	// A FULL_SYNC is queued, PENDING, and belongs to the NEW site.
	var jobs, fullSync int
	mustScan(t, `
		SELECT count(*),
		       count(*) FILTER (WHERE j.job_type = 'FULL_SYNC')
		  FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		  JOIN sites s ON s.id = j.site_id
		 WHERE d.serial_number = '`+f.serial+`'
		   AND j.status = 'PENDING'
		   AND s.site_name = 'Site B'`, &jobs, &fullSync)

	if fullSync != 1 {
		t.Fatalf("got %d pending FULL_SYNC jobs for the new site, want exactly 1", fullSync)
	}
	if jobs < 2 {
		t.Errorf("only %d jobs queued for the new site; the snapshot should also "+
			"carry the person records and the site's settings", jobs)
	}

	// NOTHING from the old site is still deliverable.
	var stale int
	mustScan(t, `
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		  JOIN sites s ON s.id = j.site_id
		 WHERE d.serial_number = '`+f.serial+`'
		   AND s.site_name = 'Site A'
		   AND j.status IN ('PENDING', 'FAILED')`, &stale)
	if stale != 0 {
		t.Errorf("%d jobs for the old site are still deliverable", stale)
	}
}

// TestRelocationDoesNotDependOnTheReconciler.
//
// The sharpest statement of the defect: with the background reconciler never
// running, the terminal must still be told to drop Site A's people. Before the
// fix this test would find an empty queue.
func TestRelocationDoesNotDependOnTheReconciler(t *testing.T) {
	f := newRelocationFixture(t)

	if code, _ := f.move(t, "Site B"); code != http.StatusOK {
		t.Fatalf("move failed")
	}

	// Deliberately NOT calling ReconcileDeviceRoster. What the terminal will
	// fetch on its very next poll is what matters.
	jobs, _, err := database.FetchDeviceWork(f.deviceID, 50)
	if err != nil {
		t.Fatalf("fetching device work: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("a moved terminal's next poll returns no work at all; it would keep " +
			"running on the old site's cached roster until the reconciler ran")
	}

	var sawFullSync bool
	for _, job := range jobs {
		if job.JobType == "FULL_SYNC" {
			sawFullSync = true
			if strings.Contains(string(job.Payload), "P-SITE-A") {
				t.Error("the relocation snapshot still names the old site's person")
			}
			if !strings.Contains(string(job.Payload), "P-SITE-B") {
				t.Error("the relocation snapshot does not name the new site's person")
			}
		}
	}
	if !sawFullSync {
		t.Error("the terminal's next poll carries no FULL_SYNC, so nothing tells it " +
			"to remove the people it should no longer hold")
	}
}

// TestRelocationMarksPlacementsForRemoval.
//
// The other half of "no stale access": the templates are physically still in
// that sensor. The FULL_SYNC makes the firmware erase them (sync_engine.cpp
// reconcile -> library.removeTemplate) and report each one back as REMOVED,
// which converges these rows. Marking them REMOVING is the server-side record
// of that intention.
func TestRelocationMarksPlacementsForRemoval(t *testing.T) {
	f := newRelocationFixture(t)

	// A credential physically placed on this terminal while it was at Site A.
	personID := seedPerson(t, f.companyID, "P-PLACED", "Placed Person")
	mustExec(t, `
		INSERT INTO credentials (company_id, person_id, credential_type,
		                         template_format, status)
		VALUES ($1, $2, 'FINGERPRINT', 'SENSOR_LOCAL', 'ACTIVE')`, f.companyID, personID)
	mustExec(t, `
		INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
		SELECT c.id, $1, 9, 'PLACED', CURRENT_TIMESTAMP
		  FROM credentials c WHERE c.person_id = $2`, f.deviceID, personID)

	if code, _ := f.move(t, "Site B"); code != http.StatusOK {
		t.Fatalf("move failed")
	}

	var state string
	mustScan(t, `SELECT state FROM credential_placements WHERE device_id = `+itoa(f.deviceID),
		&state)
	if state != models.PlacementRemoving {
		t.Errorf("placement state = %s after the move, want REMOVING", state)
	}
}

// ---------------------------------------------------------------------------
// 5: failure and rollback
// ---------------------------------------------------------------------------

// TestFailedRelocationLeavesNothingBehind.
//
// Requirement 5. A move naming a site that does not resolve must change nothing
// at all -- not the site, not the queue, not the placements. The defect's shape
// makes this worth asserting explicitly: the move now performs several writes,
// and a partial application would be far worse than the original bug, because it
// could cancel a terminal's queue without moving it.
func TestFailedRelocationLeavesNothingBehind(t *testing.T) {
	f := newRelocationFixture(t)

	before := pendingPersonIDs(t, f.serial)
	pendingBefore := queryInt(t, `
		SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.status = 'PENDING'`, f.serial)

	for _, tc := range []struct {
		name   string
		siteID string
		want   int
	}{
		{"a site in another tenant", sitePublicIDByName(t, "Site C"), http.StatusNotFound},
		{"a site that does not exist", "11111111-2222-3333-4444-555555555555",
			http.StatusNotFound},
		{"not a uuid at all", "not-a-uuid", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := consoleCall(t, f.env.router, "PUT",
				"/api/v1/console/terminals/"+f.serial+"/site",
				`{"site_id":"`+tc.siteID+`"}`, f.token, f.csrf)
			if code != tc.want {
				t.Fatalf("move = %d, want %d", code, tc.want)
			}

			// Still at Site A.
			var siteName string
			mustScan(t, `SELECT s.site_name FROM devices d JOIN sites s ON s.id = d.site_id
			              WHERE d.serial_number = '`+f.serial+`'`, &siteName)
			if siteName != "Site A" {
				t.Errorf("a refused move relocated the terminal to %q", siteName)
			}

			// Queue untouched: nothing cancelled, nothing added.
			pendingAfter := queryInt(t, `
				SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
				 WHERE d.serial_number = $1 AND j.status = 'PENDING'`, f.serial)
			if pendingAfter != pendingBefore {
				t.Errorf("a refused move changed the queue: %d -> %d",
					pendingBefore, pendingAfter)
			}

			// And the roster it would converge on is unchanged. In particular
			// no snapshot was queued: a refused move must not cancel the queue
			// and rebuild it, which would be worse than the original defect.
			after := pendingPersonIDs(t, f.serial)
			if len(after) != len(before) {
				t.Errorf("a refused move changed the queued roster: %v -> %v", before, after)
			}
			if snapshot := fullSyncRoster(t, f.serial); snapshot != nil {
				t.Errorf("a refused move queued a relocation snapshot: %v", snapshot)
			}
		})
	}
}

// TestRelocationCannotCrossCompanies. A device reaches its company only through
// its site, so a cross-tenant move would hand whatever roster it holds to
// another customer.
func TestRelocationCannotCrossCompanies(t *testing.T) {
	f := newRelocationFixture(t)

	// Site C belongs to company two.
	code, _ := consoleCall(t, f.env.router, "PUT",
		"/api/v1/console/terminals/"+f.serial+"/site",
		`{"site_id":"`+sitePublicIDByName(t, "Site C")+`"}`, f.token, f.csrf)
	if code != http.StatusNotFound {
		t.Fatalf("a cross-company move returned %d, want 404", code)
	}

	var companyID int64
	mustScan(t, `SELECT s.company_id FROM devices d JOIN sites s ON s.id = d.site_id
	              WHERE d.serial_number = '`+f.serial+`'`, &companyID)
	if companyID != f.companyID {
		t.Fatalf("the terminal ended up in company %d", companyID)
	}
}

// ---------------------------------------------------------------------------
// 6: idempotency, and the rest of the behaviour still holding
// ---------------------------------------------------------------------------

// TestRelocationIsIdempotentAndSafeAcrossRetries.
//
// A FULL_SYNC describes a SET, so applying it twice is a no-op once converged --
// which is what makes a retried or replayed relocation safe. Moving twice must
// leave exactly one live snapshot rather than accumulating them, and moving back
// must restore the original roster.
func TestRelocationIsIdempotentAndSafeAcrossRetries(t *testing.T) {
	f := newRelocationFixture(t)

	for i := 0; i < 3; i++ {
		if code, body := f.move(t, "Site B"); code != http.StatusOK {
			t.Fatalf("move %d = %d: %v", i, code, body)
		}
	}

	// One live snapshot, not three. Each move supersedes the previous one's.
	live := queryInt(t, `
		SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.job_type = 'FULL_SYNC' AND j.status = 'PENDING'`,
		f.serial)
	if live != 1 {
		t.Errorf("%d live FULL_SYNC snapshots after three moves, want 1", live)
	}

	roster := fullSyncRoster(t, f.serial)
	if contains(roster, "P-SITE-A") || !contains(roster, "P-SITE-B") {
		t.Errorf("repeated moves left the wrong roster: %v", roster)
	}

	// Moving back restores the original site's people and withdraws the other's.
	if code, _ := f.move(t, "Site A"); code != http.StatusOK {
		t.Fatal("moving back failed")
	}
	roster = fullSyncRoster(t, f.serial)
	if !contains(roster, "P-SITE-A") || contains(roster, "P-SITE-B") {
		t.Errorf("moving back did not restore the original roster: %v", roster)
	}
}

// TestRelocationDeliversTheNewSitesOfflinePolicy.
//
// The snapshot's SETTINGS job is built from the device's CURRENT site, so a
// relocated terminal receives the policy of the site it now stands at. Asserted
// because the offline policy is a safety control and the requirement is
// explicitly not to weaken it: a terminal moved into a DENY_ALL site must not
// keep running the permissive policy of the one it left.
func TestRelocationDeliversTheNewSitesOfflinePolicy(t *testing.T) {
	f := newRelocationFixture(t)

	mustExec(t, `UPDATE sites SET offline_policy = 'CACHED_INDEFINITE' WHERE site_name = 'Site A'`)
	mustExec(t, `UPDATE sites SET offline_policy = 'DENY_ALL', offline_grace_minutes = 0
	              WHERE site_name = 'Site B'`)

	if code, _ := f.move(t, "Site B"); code != http.StatusOK {
		t.Fatal("move failed")
	}

	var payload string
	mustScan(t, `
		SELECT payload::text FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = '`+f.serial+`'
		   AND j.job_type = 'SETTINGS' AND j.status = 'PENDING'
		 ORDER BY j.id DESC LIMIT 1`, &payload)

	if !strings.Contains(payload, "DENY_ALL") {
		t.Errorf("the relocated terminal was not given its new site's offline policy: %s",
			payload)
	}
	if strings.Contains(payload, "CACHED_INDEFINITE") {
		t.Errorf("the relocated terminal kept the old site's offline policy: %s", payload)
	}
}

// TestRelocationStillReportsWhatItCancelled. The existing contract: an operator
// is told how much queued work was discarded, so a backlog does not disappear
// silently.
func TestRelocationStillReportsWhatItCancelled(t *testing.T) {
	f := newRelocationFixture(t)

	code, body := f.move(t, "Site B")
	if code != http.StatusOK {
		t.Fatalf("move = %d: %v", code, body)
	}

	if _, present := body["pending_jobs_cancelled"]; !present {
		t.Error("the move response no longer reports what it cancelled")
	}
	if body["moved"] != true {
		t.Errorf("the move response no longer reports `moved`: %v", body)
	}
	// New, and the reason it is worth having: "moved" and "moved, and its
	// roster is being rebuilt" are different facts to an operator deciding
	// whether the old site's people can still open it.
	if body["roster_resynced"] != true {
		t.Errorf("the move did not report that the roster was resynced: %v", body)
	}
}

// TestRelocationLeavesJobCountersCorrect.
//
// devices.pending_job_count is denormalised and is what the terminal list
// renders, so a move that left it stale would show a relocated terminal as
// having no work outstanding while a snapshot sat in its queue.
//
// Worth asserting because the previous implementation SET IT TO ZERO by hand
// (cancelQueuedWork does), which was right when a move enqueued nothing and is
// wrong now. The counters are maintained by the trigger 016 installed, so the
// manual zeroing is not merely unnecessary here -- it would be incorrect.
func TestRelocationLeavesJobCountersCorrect(t *testing.T) {
	f := newRelocationFixture(t)

	if code, _ := f.move(t, "Site B"); code != http.StatusOK {
		t.Fatal("move failed")
	}

	stored := queryInt(t, `SELECT pending_job_count FROM devices WHERE serial_number = $1`,
		f.serial)
	actual := queryInt(t, `
		SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.status = 'PENDING'`, f.serial)

	if stored != actual {
		t.Errorf("pending_job_count is %d but %d jobs are actually queued", stored, actual)
	}
	if stored == 0 {
		t.Error("a relocated terminal reports no outstanding work while its " +
			"relocation snapshot is still queued")
	}
}
