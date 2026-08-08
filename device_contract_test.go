package main

import (
	"net/http"
	"testing"
)

// The ESP32 device contract.
//
// Each test here corresponds to a step a terminal actually performs, in the
// order it performs them: enrol, authenticate, report in, read configuration,
// collect work, acknowledge it. A change that breaks one of these breaks
// deployed firmware, so they are written against the wire format rather than
// against internal helpers.

func TestDeviceRegistration(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodPost, "/api/v1/devices/register", map[string]any{
		"serial_number":    "ESP32-0001",
		"device_name":      "Front Door",
		"firmware_version": "1.2.0",
	}, siteAuth(env.siteAKey))

	if res.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body %s)", res.Code, res.Raw)
	}

	// The fields firmware depends on
	for _, field := range []string{"protocol_version", "device_id", "serial_number", "api_key", "bootstrap_jobs"} {
		if _, ok := res.Body[field]; !ok {
			t.Errorf("response is missing %q, which the firmware reads (body %s)", field, res.Raw)
		}
	}

	if got := res.Body["serial_number"]; got != "ESP32-0001" {
		t.Errorf("serial_number = %v, want ESP32-0001", got)
	}
	if key, _ := res.Body["api_key"].(string); len(key) < 32 {
		t.Errorf("api_key %q is too short to be a 256-bit credential", key)
	}
}

func TestRegistrationRequiresSiteKey(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-0001"}, nil)

	if res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated registration got %d, want 401", res.Code)
	}
}

func TestRegistrationSeedsExistingMembers(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")

	res := env.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": "ESP32-0001"}, siteAuth(env.siteAKey))

	// A terminal enrolled after the roster exists must still converge, so it is
	// seeded with the current members rather than only future changes.
	if got := res.Body["bootstrap_jobs"]; got != float64(2) {
		t.Errorf("bootstrap_jobs = %v, want 2 (body %s)", got, res.Raw)
	}
}

func TestDeviceAuthentication(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"valid device key", deviceAuth(key), http.StatusOK},
		{"no credential", nil, http.StatusUnauthorized},
		{"unknown device key", deviceAuth("atd_" + "0"), http.StatusUnauthorized},
		{"site key without serial", siteAuth(env.siteAKey), http.StatusUnauthorized},
		{"legacy site key plus serial", map[string]string{
			"X-API-Key": env.siteAKey, "X-Device-Serial": "ESP32-0001",
		}, http.StatusOK},
		{"legacy pair naming an unknown serial", map[string]string{
			"X-API-Key": env.siteAKey, "X-Device-Serial": "ESP32-NOPE",
		}, http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, tc.headers)
			if res.Code != tc.want {
				t.Errorf("got %d, want %d (body %s)", res.Code, tc.want, res.Raw)
			}
		})
	}
}

func TestHeartbeat(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{
		"status": "ONLINE", "firmware_version": "1.2.0", "boot_count": 4,
	}, deviceAuth(key))

	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	for _, field := range []string{"protocol_version", "device_id", "server_time", "pending_jobs"} {
		if _, ok := res.Body[field]; !ok {
			t.Errorf("heartbeat response is missing %q (body %s)", field, res.Raw)
		}
	}
	if got := res.Body["device_id"]; got != "ESP32-0001" {
		t.Errorf("device_id = %v, want the serial ESP32-0001", got)
	}
}

func TestHeartbeatAcceptsEmptyBody(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	// Constrained firmware sends no body at all. That must not be a 400.
	res := env.do(http.MethodPost, "/api/v1/devices/heartbeat", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Errorf("bodyless heartbeat got %d, want 200 (body %s)", res.Code, res.Raw)
	}
}

func TestHeartbeatCannotClaimAdministrativeState(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	// DISABLED is an operator decision and OFFLINE is inferred by the server.
	// A device asserting either would let a terminal take itself out of service
	// or quietly put itself back into it.
	for _, claimed := range []string{"DISABLED", "OFFLINE", "PROVISIONING"} {
		res := env.do(http.MethodPost, "/api/v1/devices/heartbeat",
			map[string]any{"status": claimed}, deviceAuth(key))
		if res.Code != http.StatusOK {
			t.Fatalf("heartbeat claiming %s got %d, want 200", claimed, res.Code)
		}

		if status := deviceStatus(t, "ESP32-0001"); status == claimed {
			t.Errorf("device was allowed to put itself into %s", claimed)
		}
	}
}

func TestDisabledDeviceIsLockedOut(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	mustExec(t, `UPDATE devices SET status = 'DISABLED' WHERE serial_number = 'ESP32-0001'`)

	res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(key))
	if res.Code != http.StatusForbidden {
		t.Errorf("disabled device got %d, want 403 (body %s)", res.Code, res.Raw)
	}
}

func TestDeviceOfDeactivatedSiteIsLockedOut(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	mustExec(t, `UPDATE sites SET active = FALSE WHERE api_key = $1`, env.siteAKey)

	// Deactivating a site has to stop its terminals, not just its dashboard.
	res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(key))
	if res.Code != http.StatusUnauthorized {
		t.Errorf("device at an inactive site got %d, want 401 (body %s)", res.Code, res.Raw)
	}
}

func TestDeviceSettings(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	env.do(http.MethodPut, "/api/v1/sites/settings",
		map[string]any{"unlock_duration_seconds": 7}, siteAuth(env.siteAKey))

	res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	settings, ok := res.Body["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings is not an object (body %s)", res.Raw)
	}
	if settings["unlock_duration_seconds"] != float64(7) {
		t.Errorf("unlock_duration_seconds = %v, want 7", settings["unlock_duration_seconds"])
	}
	// The version is what lets a device discard a stale push.
	if _, ok := res.Body["settings_version"]; !ok {
		t.Errorf("response carries no settings_version (body %s)", res.Raw)
	}
}

func TestProtocolNegotiation(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	cases := []struct {
		version string
		want    int
	}{
		{"", http.StatusOK},           // firmware predating the header
		{"1", http.StatusOK},          // current
		{"99", http.StatusBadRequest}, // newer than the server speaks
		{"abc", http.StatusBadRequest},
	}

	for _, tc := range cases {
		headers := deviceAuth(key)
		if tc.version != "" {
			headers["X-Protocol-Version"] = tc.version
		}

		res := env.do(http.MethodGet, "/api/v1/devices/settings", nil, headers)
		if res.Code != tc.want {
			t.Errorf("X-Protocol-Version=%q got %d, want %d (body %s)",
				tc.version, res.Code, tc.want, res.Raw)
		}
	}
}

func TestJobDeliveryAndAcknowledgement(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	jobs := env.jobs(key)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1: %v", len(jobs), jobTypes(jobs))
	}
	if got := jobs[0]["job_type"]; got != "CREATE" {
		t.Errorf("job_type = %v, want CREATE", got)
	}

	id := jobID(t, jobs[0])
	res := env.do(http.MethodPost, jobPath(id), map[string]any{"status": "COMPLETED"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("ack got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if got := res.Body["pending_jobs"]; got != float64(0) {
		t.Errorf("pending_jobs = %v, want 0 after the only job was acked", got)
	}
	if s := jobStatus(t, id); s != "COMPLETED" {
		t.Errorf("job status = %s, want COMPLETED", s)
	}
}

func TestBareAcknowledgementMeansSuccess(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	// Firmware that cannot spare the bytes for a body still has to be able to
	// acknowledge.
	res := env.do(http.MethodPost, jobPath(id), nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("bodyless ack got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if s := jobStatus(t, id); s != "COMPLETED" {
		t.Errorf("job status = %s, want COMPLETED", s)
	}
}

func TestAcknowledgementIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	// A device whose first acknowledgement was lost in transit retries it. The
	// retry must not be an error, or the device retries forever.
	for attempt := 1; attempt <= 3; attempt++ {
		res := env.do(http.MethodPost, jobPath(id), map[string]any{"status": "COMPLETED"}, deviceAuth(key))
		if res.Code != http.StatusOK {
			t.Fatalf("ack attempt %d got %d, want 200 (body %s)", attempt, res.Code, res.Raw)
		}
	}
}

func TestFailedAcknowledgementSchedulesRetry(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	res := env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "FAILED", "error": "flash write error"}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("failed-ack got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	// Still owed to the device, and backed off rather than retried immediately.
	if s := jobStatus(t, id); s != "PENDING" {
		t.Errorf("job status = %s, want PENDING so it is redelivered", s)
	}

	attempts, backedOff := jobRetryState(t, id)
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if !backedOff {
		t.Error("job was not backed off, so a broken job would spin")
	}
}

func TestRejectsUnknownAckStatus(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	id := jobID(t, env.jobs(key)[0])

	res := env.do(http.MethodPost, jobPath(id), map[string]any{"status": "MAYBE"}, deviceAuth(key))
	if res.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 (body %s)", res.Code, res.Raw)
	}
}

func TestDeleteJobIsDelivered(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")

	// Drain the CREATE so only the deletion is outstanding.
	for _, job := range env.jobs(key) {
		env.do(http.MethodPost, jobPath(jobID(t, job)), nil, deviceAuth(key))
	}

	res := env.do(http.MethodDelete, "/api/v1/members/M-1", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("delete got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	jobs := env.jobs(key)
	if len(jobs) != 1 || jobs[0]["job_type"] != "DELETE" {
		t.Fatalf("got %v, want a single DELETE job", jobTypes(jobs))
	}

	// A DELETE is the only way a terminal learns about a removal, so it must
	// carry enough to act on without having seen the person before.
	if jobs[0]["entity_external_id"] != "M-1" {
		t.Errorf("entity_external_id = %v, want M-1", jobs[0]["entity_external_id"])
	}
	payload, ok := jobs[0]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("DELETE job has no payload object: %v", jobs[0])
	}
	if payload["deleted"] != true {
		t.Errorf("payload.deleted = %v, want true", payload["deleted"])
	}
	if payload["member_id"] != "M-1" {
		t.Errorf("payload.member_id = %v, want M-1", payload["member_id"])
	}
}

func TestFullSyncSnapshotReplacesBacklog(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")
	env.createMember(env.siteAKey, "M-1", "Ada")
	env.createMember(env.siteAKey, "M-2", "Grace")

	res := env.do(http.MethodPost, "/api/v1/devices/ESP32-0001/resync", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("resync got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if got := res.Body["superseded_jobs"]; got != float64(2) {
		t.Errorf("superseded_jobs = %v, want the 2 queued CREATEs", got)
	}

	jobs := env.jobs(key)
	types := jobTypes(jobs)
	if !contains(types, "FULL_SYNC") {
		t.Fatalf("no FULL_SYNC in %v", types)
	}

	// FULL_SYNC must come first: it is the instruction to converge on the
	// roster, and the CREATEs that follow fill it in.
	if types[0] != "FULL_SYNC" {
		t.Errorf("first job is %s, want FULL_SYNC", types[0])
	}

	payload, ok := jobs[0]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("FULL_SYNC has no payload object: %v", jobs[0])
	}
	ids, _ := payload["member_ids"].([]any)
	if len(ids) != 2 {
		t.Errorf("roster carries %d member ids, want 2 (payload %v)", len(ids), payload)
	}
	if payload["count"] != float64(2) {
		t.Errorf("roster count = %v, want 2", payload["count"])
	}
}

// jobPath builds the acknowledgement URL for a job
func jobPath(id int64) string {
	return "/api/v1/devices/jobs/" + itoa(id) + "/complete"
}
