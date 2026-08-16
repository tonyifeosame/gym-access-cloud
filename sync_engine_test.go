package main

import (
	"net/http"
	"sync"
	"testing"
)

// Synchronization engine behaviour.
//
// The tests below are regressions for failures found while reviewing this code,
// plus the invariants the engine's design rests on. They matter more than usual
// because the consequence of getting them wrong is a physical door: a person
// removed from the roster who is still admitted, or a terminal that will not let
// anyone in because its queue never drains.

// A device that fails a job which has since been superseded must not resurrect
// it. Compaction cancels the outstanding queue and replaces it with a snapshot;
// putting a cancelled job back into PENDING re-applies pre-snapshot state, which
// can recreate people who were deleted before the snapshot was taken.
func TestFailedAckDoesNotResurrectSupersededJob(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	// The operator resyncs the terminal, superseding what was queued.
	if res := env.do(http.MethodPost, "/api/v1/devices/ESP32-0001/resync", nil,
		siteAuth(env.siteAKey)); res.Code != http.StatusOK {
		t.Fatalf("resync got %d, want 200", res.Code)
	}
	if s := jobStatus(t, id); s != "CANCELLED" {
		t.Fatalf("job status = %s, want CANCELLED after resync", s)
	}

	// The terminal, still working through the batch it fetched before the
	// resync, reports the old job as failed.
	res := env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "FAILED", "error": "late failure"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("late failure report got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	if s := jobStatus(t, id); s != "CANCELLED" {
		t.Fatalf("superseded job was reopened as %s", s)
	}

	// And it must not come back on the wire.
	makeJobDue(t, id)
	for _, job := range env.jobs(key) {
		if jobID(t, job) == id {
			t.Fatal("superseded job was redelivered to the device")
		}
	}
}

// A device retrying an acknowledgement it already sent must not be able to
// reopen a completed job. The schema enforces that COMPLETED implies
// acknowledged, so reopening one also fails the constraint and surfaces to the
// device as a 500 it retries indefinitely.
func TestFailedAckDoesNotReopenCompletedJob(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	if res := env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "COMPLETED"}, deviceAuth(key)); res.Code != http.StatusOK {
		t.Fatalf("first ack got %d, want 200", res.Code)
	}

	res := env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "FAILED", "error": "duplicate report"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Errorf("late failure for a completed job got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if s := jobStatus(t, id); s != "COMPLETED" {
		t.Errorf("completed job was reopened as %s", s)
	}
}

// Fetching takes a delivery lease rather than retiring the job, so a device that
// dies mid-apply gets the work again -- but a device polling twice in quick
// succession is not handed the same job twice.
func TestFetchTakesADeliveryLease(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	// The CREATE, plus the settings push registration seeded.
	first := env.jobs(key)
	if len(first) != 2 {
		t.Fatalf("first poll returned %d jobs, want 2 (%v)", len(first), jobTypes(first))
	}

	second := env.jobs(key)
	if len(second) != 0 {
		t.Errorf("second poll returned %d jobs, want 0 while the lease is held", len(second))
	}

	// Unacknowledged work is still owed, so it returns once the lease lapses.
	// One job is released rather than both, which is what makes this about the
	// lease and not about the batch.
	creates := jobsOfType(first, "CREATE")
	if len(creates) != 1 {
		t.Fatalf("expected one CREATE in the first poll, got %v", jobTypes(first))
	}
	makeJobDue(t, jobID(t, creates[0]))
	if third := env.jobs(key); len(third) != 1 {
		t.Errorf("after the lease expired the poll returned %d jobs, want 1", len(third))
	}
}

// Two concurrent polls from the same device must not both be handed the same
// job, or the terminal applies it twice and the second acknowledgement races.
func TestConcurrentPollsDoNotDoubleDeliver(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	const members = 20
	for i := 0; i < members; i++ {
		env.createMember(env.siteAKey, "M-"+itoa(int64(i)), "Member")
	}

	const pollers = 6
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[int64]int{}

	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, job := range env.jobs(key) {
				id := jobID(t, job)
				mu.Lock()
				seen[id]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for id, count := range seen {
		if count > 1 {
			t.Errorf("job %d was delivered to %d concurrent polls", id, count)
		}
	}
	// The people, plus the settings push registration seeded.
	if len(seen) != members+1 {
		t.Errorf("%d distinct jobs delivered, want %d", len(seen), members+1)
	}
}

// A change fans out to every device that must hold it, and each device owns its
// own copy: one terminal acknowledging must not retire another's work.
func TestChangesFanOutPerDevice(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-AAA")
	keyB := env.registerDevice(env.siteAKey, "ESP32-BBB")

	env.createMember(env.siteAKey, "M-1", "Ada")

	// Each terminal also holds the settings push its own registration seeded,
	// so the person job is selected by type rather than by position.
	createsA := jobsOfType(env.jobs(keyA), "CREATE")
	createsB := jobsOfType(env.jobs(keyB), "CREATE")
	if len(createsA) != 1 || len(createsB) != 1 {
		t.Fatalf("device A got %d CREATE jobs and device B got %d, want 1 each",
			len(createsA), len(createsB))
	}

	idA, idB := jobID(t, createsA[0]), jobID(t, createsB[0])
	if idA == idB {
		t.Fatal("both devices were handed the same job row")
	}

	env.do(http.MethodPost, jobPath(idA), nil, deviceAuth(keyA))

	if s := jobStatus(t, idB); s != "PENDING" {
		t.Errorf("device B's job became %s when device A acknowledged its own", s)
	}
}

// Settings belong to a site, so a change must reach that site's terminals and
// no others -- unlike people, who belong to the whole tenant.
func TestSettingsFanOutToTheSiteOnly(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-AAA")
	keyB := env.registerDevice(env.siteBKey, "ESP32-BBB")

	res := env.do(http.MethodPut, "/api/v1/sites/settings",
		map[string]any{"unlock_duration_seconds": 4}, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("updating settings got %d, want 200", res.Code)
	}

	if types := jobTypes(env.jobs(keyA)); !contains(types, "SETTINGS") {
		t.Errorf("site A's device got %v, want a SETTINGS job", types)
	}

	// SITE B'S DEVICE HOLDS A SETTINGS JOB TOO -- the one its own registration
	// seeded -- so counting types no longer answers the question this test
	// asks. What it must show is that SITE A'S CHANGE did not reach it, and the
	// only way to show that is to read the payload.
	for _, job := range jobsOfType(env.jobs(keyB), "SETTINGS") {
		payload, _ := job["payload"].(map[string]any)
		inner, _ := payload["settings"].(map[string]any)
		if got := inner["unlock_duration_seconds"]; got == float64(4) {
			t.Errorf("site B's device received site A's settings: %v", inner)
		}
	}
}

// People belong to the tenant, so every terminal in the company holds them --
// including terminals at other sites.
func TestPeopleFanOutAcrossTheTenant(t *testing.T) {
	env := newTestEnv(t)
	keyA := env.registerDevice(env.siteAKey, "ESP32-AAA")
	keyB := env.registerDevice(env.siteBKey, "ESP32-BBB")
	keyC := env.registerDevice(env.siteCKey, "ESP32-CCC") // other tenant

	env.createMember(env.siteAKey, "M-1", "Ada")

	if types := jobTypes(env.jobs(keyA)); !contains(types, "CREATE") {
		t.Errorf("site A device got %v, want a CREATE", types)
	}
	if types := jobTypes(env.jobs(keyB)); !contains(types, "CREATE") {
		t.Errorf("site B device got %v, want a CREATE -- people are tenant-wide", types)
	}
	// The other tenant's terminal holds only the settings push its own
	// registration seeded. What must never reach it is the PERSON.
	jobsC := env.jobs(keyC)
	if creates := jobsOfType(jobsC, "CREATE"); len(creates) != 0 {
		t.Errorf("another tenant's device received %d CREATE job(s): %v",
			len(creates), jobTypes(jobsC))
	}
}

// A disabled terminal is out of service, so it must not accumulate a backlog
// that would all land at once if it were ever re-enabled.
func TestDisabledDeviceDoesNotAccumulateWork(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-AAA")
	mustExec(t, `UPDATE devices SET status = 'DISABLED' WHERE serial_number = 'ESP32-AAA'`)

	// Measured FROM THE MOMENT IT WENT OUT OF SERVICE, which is what the
	// property is about. Registration legitimately queued a settings push while
	// the terminal was still in service; what must not happen is work piling up
	// AFTER an operator took it out.
	before := queryInt(t, `SELECT count(*) FROM sync_jobs sj
	                        JOIN devices d ON d.id = sj.device_id
	                       WHERE d.serial_number = 'ESP32-AAA'`)

	env.createMember(env.siteAKey, "M-1", "Ada")

	after := queryInt(t, `SELECT count(*) FROM sync_jobs sj
	                       JOIN devices d ON d.id = sj.device_id
	                      WHERE d.serial_number = 'ESP32-AAA'`)
	if after != before {
		t.Errorf("disabled device accumulated %d job(s) while out of service, want 0",
			after-before)
	}
}

// Deleting a member the terminal never heard about still has to be expressible,
// because GET /members/changes cannot report a removal -- the row simply stops
// appearing. Repeating the delete must not queue a second job.
func TestRepeatedDeleteIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	for _, job := range env.jobs(key) {
		env.do(http.MethodPost, jobPath(jobID(t, job)), nil, deviceAuth(key))
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if res := env.do(http.MethodDelete, "/api/v1/members/M-1", nil,
			siteAuth(env.siteAKey)); res.Code != http.StatusOK {
			t.Fatalf("delete attempt %d got %d, want 200", attempt, res.Code)
		}
	}

	deletes := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DELETE'`)
	if deletes != 1 {
		t.Errorf("%d DELETE jobs queued for three deletes, want 1", deletes)
	}
}

// The backlog a device is told about must exclude jobs that were superseded.
// Counting cancelled rows both overstates the work and -- because compaction
// creates cancelled rows -- would let a compacted device immediately re-cross
// the compaction threshold and compact forever.
func TestBacklogExcludesSupersededJobs(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	for i := 0; i < 5; i++ {
		env.createMember(env.siteAKey, "M-"+itoa(int64(i)), "Member")
	}

	env.do(http.MethodPost, "/api/v1/devices/ESP32-0001/resync", nil, siteAuth(env.siteAKey))

	cancelled := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE status = 'CANCELLED'`)
	if cancelled == 0 {
		t.Fatal("resync cancelled nothing, so this test proves nothing")
	}

	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", nil, deviceAuth(key))
	pending, _ := res.Body["pending_jobs"].(float64)

	live := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE status IN ('PENDING', 'FAILED')`)
	if int(pending) != live {
		t.Errorf("heartbeat reported %v pending jobs, want %d (cancelled rows are being counted)",
			pending, live)
	}
}
