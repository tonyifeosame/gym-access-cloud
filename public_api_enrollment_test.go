package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Terminal listing and remote fingerprint enrolment on the public API
// (API_SPEC.md section 18, "Terminals" and "Enrolment"), end to end through the
// real router. The store underneath is the console's and is covered by
// console_enrollment_test.go; what is under test here is the public boundary:
// scopes, the credential's site restriction, the section 18 envelope,
// idempotency, and that the lifecycle a terminal drives reads back correctly.

const (
	enrolScopes  = `"terminals:read","members:read","enrollments:write"`
	enrolMember  = "PE-001"
	termA1       = "PE-A1"
	termA2       = "PE-A2"
	termB1       = "PE-B1"
	termC1       = "PE-C1"
	startPathFmt = "/api/public/v1/terminals/%s/enrollments"
	statusPath   = "/api/public/v1/members/" + enrolMember + "/enrollment"
)

// pubEnrolFixture: company one has two terminals at Site A and one at Site B;
// company two has one at Site C. One member in company one.
type pubEnrolFixture struct {
	env                    *testEnv
	keyA1, keyA2, keyB1    string
	full, siteAOnly        string
	siteA, siteB, memberID string
}

func newPubEnrolFixture(t *testing.T) *pubEnrolFixture {
	t.Helper()
	env := newTestEnv(t)
	f := &pubEnrolFixture{
		env:   env,
		keyA1: env.registerDevice(env.siteAKey, termA1),
		keyA2: env.registerDevice(env.siteAKey, termA2),
		keyB1: env.registerDevice(env.siteBKey, termB1),
		siteA: operatorSitePublicID(t, "Site A"),
		siteB: operatorSitePublicID(t, "Site B"),
	}
	env.registerDevice(env.siteCKey, termC1)
	env.createMember(env.siteAKey, enrolMember, "Ada Okonkwo")
	f.full = publicCredential(t, env, "one", "pe-full@example.com", `{"name":"enrol","scopes":[`+enrolScopes+`]}`)
	f.siteAOnly = publicCredential(t, env, "one", "pe-sitea@example.com",
		`{"name":"enrol site a","scopes":[`+enrolScopes+`],"site_ids":["`+f.siteA+`"]}`)
	_, _, member, _ := publicGet(t, env, f.full, "/api/public/v1/members/"+enrolMember)
	f.memberID, _ = member["id"].(string)
	return f
}

func (f *pubEnrolFixture) start(t *testing.T, secret, serial, body, key string) (int, http.Header, map[string]any, string) {
	t.Helper()
	if body == "" {
		body = `{"member_id":"` + enrolMember + `"}`
	}
	return publicCall(t, f.env, secret, http.MethodPost, fmt.Sprintf(startPathFmt, serial), body, key)
}

func (f *pubEnrolFixture) mustStart(t *testing.T, serial string) map[string]any {
	t.Helper()
	status, _, body, raw := f.start(t, f.full, serial, "", "")
	if status != http.StatusCreated {
		t.Fatalf("starting at %s = %d %s", serial, status, raw)
	}
	return body
}

func (f *pubEnrolFixture) read(t *testing.T) map[string]any {
	t.Helper()
	status, _, body, raw := publicGet(t, f.env, f.full, statusPath)
	if status != http.StatusOK {
		t.Fatalf("reading enrolment = %d %s", status, raw)
	}
	return body
}

// terminal plays the firmware against the device routes, reusing the console
// suite's firmware-shaped fixture.
func (f *pubEnrolFixture) terminal() *enrolFixture { return &enrolFixture{env: f.env} }

func liveEnrollments(t *testing.T) int {
	t.Helper()
	return queryInt(t, `SELECT count(*) FROM enrollment_requests WHERE status IN ('PENDING','IN_PROGRESS')`)
}

func expectPublicError(t *testing.T, label string, status int, headers http.Header, body map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	if code := publicError(t, status, headers, body, wantStatus); code != wantCode {
		t.Errorf("%s: code %s, want %s", label, code, wantCode)
	}
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

func TestPublicTerminalsListIsTenantScopedAndSiteRestricted(t *testing.T) {
	f := newPubEnrolFixture(t)

	status, _, body, raw := publicGet(t, f.env, f.full, "/api/public/v1/terminals")
	if status != http.StatusOK || len(body) != 1 {
		t.Fatalf("list = %d %s", status, raw)
	}
	data := listOf(t, body, "data")
	var serials []string
	for _, d := range data {
		serials = append(serials, d.(map[string]any)["serial"].(string))
	}
	// Company one only, ordered by site name then serial.
	if strings.Join(serials, ",") != termA1+","+termA2+","+termB1 {
		t.Errorf("terminals = %v, want company one's three in site/serial order", serials)
	}
	first := data[0].(map[string]any)
	for _, k := range []string{"serial", "name", "site_id", "site_name", "status", "last_seen_at", "enrollable"} {
		if _, ok := first[k]; !ok {
			t.Errorf("terminal lacks %s: %v", k, first)
		}
	}
	if len(first) != 7 || first["site_id"] != f.siteA || first["site_name"] != "Site A" || first["enrollable"] != true {
		t.Errorf("terminal object = %v", first)
	}
	for _, forbidden := range []string{"api_key", "key_hash", "device_id", `"id"`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("terminal list leaks %s: %s", forbidden, raw)
		}
	}

	// The site-restricted credential sees Site A's terminals only.
	_, _, body, raw = publicGet(t, f.env, f.siteAOnly, "/api/public/v1/terminals")
	if data := listOf(t, body, "data"); len(data) != 2 || strings.Contains(raw, termB1) {
		t.Errorf("site-restricted list = %s", raw)
	}

	// enrollable mirrors the start's own predicate.
	mustExec(t, `UPDATE devices SET status = 'DISABLED' WHERE serial_number = $1`, termA1)
	mustExec(t, `UPDATE devices SET api_key_hash = NULL, credential_revoked_at = CURRENT_TIMESTAMP WHERE serial_number = $1`, termA2)
	_, _, body, _ = publicGet(t, f.env, f.full, "/api/public/v1/terminals")
	for _, d := range listOf(t, body, "data") {
		term := d.(map[string]any)
		if want := term["serial"] == termB1; term["enrollable"] != want {
			t.Errorf("%v enrollable = %v, want %v", term["serial"], term["enrollable"], want)
		}
	}

	// Scope, and the query contract.
	noTerminals := publicCredential(t, f.env, "one", "pe-noterm@example.com", `{"name":"members","scopes":["members:read"]}`)
	status, headers, body, _ := publicGet(t, f.env, noTerminals, "/api/public/v1/terminals")
	expectPublicError(t, "without terminals:read", status, headers, body, 403, models.CodeInsufficientScope)
	status, headers, body, _ = publicGet(t, f.env, f.full, "/api/public/v1/terminals?site_id="+f.siteA)
	expectPublicError(t, "unknown parameter", status, headers, body, 400, models.CodeUnknownParameter)
}

// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------

func TestPublicStartEnrollmentQueuesAJobForTheChosenTerminalOnly(t *testing.T) {
	f := newPubEnrolFixture(t)
	before := time.Now()

	status, _, body, raw := f.start(t, f.full, termA1, `{"member_id":"`+enrolMember+`","expires_in_seconds":120}`, "")
	if status != http.StatusCreated {
		t.Fatalf("start = %d %s", status, raw)
	}
	if body["member_id"] != enrolMember || body["member"] != f.memberID || body["status"] != models.EnrollmentPending ||
		body["terminal_serial"] != termA1 || body["site_id"] != f.siteA || body["id"] == "" {
		t.Errorf("started enrolment = %s", raw)
	}
	if body["started_at"] != nil || body["completed_at"] != nil {
		t.Errorf("a new enrolment has lifecycle timestamps: %s", raw)
	}
	expires, err := time.Parse(time.RFC3339Nano, body["expires_at"].(string))
	if err != nil || expires.Before(before.Add(110*time.Second)) || expires.After(time.Now().Add(130*time.Second)) {
		t.Errorf("expires_at = %v, want about two minutes out (err %v)", body["expires_at"], err)
	}
	// Identifiers only: no name, no operator, nothing biometric.
	for _, forbidden := range []string{"Okonkwo", "full_name", "fingerprint", "template", "actor", "@"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("enrolment leaks %q: %s", forbidden, raw)
		}
	}

	// The job reaches the chosen terminal and no other.
	tf := f.terminal()
	if jobs := tf.enrolmentJobs(t, f.keyA2); len(jobs) != 0 {
		t.Errorf("an unchosen terminal was offered %d enrolment jobs", len(jobs))
	}
	if jobs := tf.enrolmentJobs(t, f.keyA1); len(jobs) != 1 {
		t.Fatalf("the chosen terminal was offered %d enrolment jobs, want 1", len(jobs))
	}

	// Audited as the integration, not as an operator.
	if n := auditCount(t, "ENROLMENT_STARTED", f.full[:17]); n != 1 {
		t.Errorf("ENROLMENT_STARTED audit rows = %d, want 1", n)
	}
	if changes := queryString(t, `SELECT changes::text FROM audit_events WHERE action = 'ENROLMENT_STARTED'`); !strings.Contains(changes, termA1) ||
		!strings.Contains(changes, `"via": "public_api"`) {
		t.Errorf("audit changes = %s", changes)
	}

	// Starting again elsewhere supersedes: one live enrolment, at the new terminal.
	if again := f.mustStart(t, termB1); again["terminal_serial"] != termB1 {
		t.Errorf("restart = %v", again)
	}
	if n := liveEnrollments(t); n != 1 {
		t.Errorf("live enrolments after a restart = %d, want 1", n)
	}
	if jobs := tf.enrolmentJobs(t, f.keyA1); len(jobs) != 0 {
		t.Errorf("the superseded terminal is still offered %d jobs", len(jobs))
	}
}

func TestPublicStartEnrollmentRefusals(t *testing.T) {
	f := newPubEnrolFixture(t)
	f.env.createMember(f.env.siteCKey, "PE-OTHER", "Other Company")
	readOnly := publicCredential(t, f.env, "one", "pe-ro@example.com", `{"name":"ro","scopes":["members:read","terminals:read"]}`)
	memberWriter := publicCredential(t, f.env, "one", "pe-mw@example.com", `{"name":"mw","scopes":["members:write"]}`)

	cases := []struct {
		name, secret, serial, body string
		status                     int
		code, param                string
	}{
		{"missing member_id", f.full, termA1, `{}`, 400, models.CodeMissingField, "member_id"},
		{"window too short", f.full, termA1, `{"member_id":"PE-001","expires_in_seconds":29}`, 400, models.CodeInvalidField, "expires_in_seconds"},
		{"window too long", f.full, termA1, `{"member_id":"PE-001","expires_in_seconds":3601}`, 400, models.CodeInvalidField, "expires_in_seconds"},
		{"window wrong type", f.full, termA1, `{"member_id":"PE-001","expires_in_seconds":"60"}`, 400, models.CodeInvalidField, "expires_in_seconds"},
		{"unknown field", f.full, termA1, `{"member_id":"PE-001","terminal":"x"}`, 400, models.CodeUnknownField, "terminal"},
		{"malformed json", f.full, termA1, `{"member_id":`, 400, models.CodeInvalidField, "body"},
		{"member not found", f.full, termA1, `{"member_id":"PE-NOPE"}`, 404, models.CodeResourceNotFound, ""},
		{"member in another company", f.full, termA1, `{"member_id":"PE-OTHER"}`, 404, models.CodeResourceNotFound, ""},
		{"terminal not found", f.full, "PE-NOPE", "", 404, models.CodeResourceNotFound, ""},
		{"terminal in another company", f.full, termC1, "", 404, models.CodeResourceNotFound, ""},
		{"terminal outside the site restriction", f.siteAOnly, termB1, "", 404, models.CodeResourceNotFound, ""},
		{"read-only credential", readOnly, termA1, "", 403, models.CodeInsufficientScope, ""},
		{"members:write is not enough", memberWriter, termA1, "", 403, models.CodeInsufficientScope, ""},
		// Scope is checked before any lookup: a missing terminal is still 403.
		{"scope before lookup", readOnly, "PE-NOPE", "", 403, models.CodeInsufficientScope, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, headers, body, _ := f.start(t, tc.secret, tc.serial, tc.body, "")
			expectPublicError(t, tc.name, status, headers, body, tc.status, tc.code)
			if tc.param != "" {
				if got := body["error"].(map[string]any)["param"]; got != tc.param {
					t.Errorf("param = %v, want %s", got, tc.param)
				}
			}
		})
	}
	if n := queryInt(t, `SELECT count(*) FROM enrollment_requests`); n != 0 {
		t.Errorf("refused starts left %d enrolments", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'ENROLL_FINGERPRINT'`); n != 0 {
		t.Errorf("refused starts queued %d jobs", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events WHERE actor_role = 'INTEGRATION'`); n != 0 {
		t.Errorf("refused starts wrote %d audit rows", n)
	}

	// The restricted credential still works inside its restriction.
	if status, _, _, raw := f.start(t, f.siteAOnly, termA2, "", ""); status != http.StatusCreated {
		t.Errorf("restricted credential at its own site = %d %s", status, raw)
	}
}

func TestPublicStartEnrollmentAtATerminalThatCannotEnrol(t *testing.T) {
	for _, tc := range []struct{ name, update string }{
		{"disabled", `UPDATE devices SET status = 'DISABLED' WHERE serial_number = $1`},
		{"retired", `UPDATE devices SET active = FALSE WHERE serial_number = $1`},
		{"credential revoked", `UPDATE devices SET api_key_hash = NULL, credential_revoked_at = CURRENT_TIMESTAMP WHERE serial_number = $1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPubEnrolFixture(t)
			mustExec(t, tc.update, termA1)
			status, headers, body, _ := f.start(t, f.full, termA1, "", "")
			expectPublicError(t, tc.name, status, headers, body, 409, models.CodeTerminalNotEnrollable)
			if n := queryInt(t, `SELECT count(*) FROM enrollment_requests`); n != 0 {
				t.Errorf("a refused start left %d enrolments", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestPublicEnrollmentWritesReplayUnderAnIdempotencyKey(t *testing.T) {
	f := newPubEnrolFixture(t)

	_, _, first, raw := f.start(t, f.full, termA1, "", "start-1")
	if first["id"] == nil {
		t.Fatalf("first start = %s", raw)
	}
	// The retry is the original response. Without the key it would supersede
	// the first enrolment and re-arm the reader with a new job.
	status, headers, second, _ := f.start(t, f.full, termA1, "", "start-1")
	if status != http.StatusCreated || headers.Get("Idempotent-Replay") != "true" || second["id"] != first["id"] {
		t.Errorf("replay = %d replay=%q same id=%v", status, headers.Get("Idempotent-Replay"), second["id"] == first["id"])
	}
	if n := queryInt(t, `SELECT count(*) FROM enrollment_requests`); n != 1 {
		t.Errorf("enrolments after a replay = %d, want 1", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'ENROLL_FINGERPRINT'`); n != 1 {
		t.Errorf("jobs after a replay = %d, want 1", n)
	}
	if n := auditCount(t, "ENROLMENT_STARTED", f.full[:17]); n != 1 {
		t.Errorf("audit rows after a replay = %d, want 1", n)
	}

	// Same key, different terminal: the path is part of the fingerprint.
	status, headers, body, _ := f.start(t, f.full, termA2, "", "start-1")
	expectPublicError(t, "same key at another terminal", status, headers, body, 409, models.CodeIdempotencyKeyReuse)

	// A cancel under a key replays its 200 rather than answering 404 the
	// second time, so a client retrying a timed-out cancel sees success.
	status, _, cancelled, raw := publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "cancel-1")
	if status != http.StatusOK || cancelled["status"] != models.EnrollmentCancelled {
		t.Fatalf("cancel = %d %s", status, raw)
	}
	status, headers, again, _ := publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "cancel-1")
	if status != http.StatusOK || headers.Get("Idempotent-Replay") != "true" || again["id"] != cancelled["id"] {
		t.Errorf("cancel replay = %d replay=%q", status, headers.Get("Idempotent-Replay"))
	}
	status, headers, body, _ = publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "")
	expectPublicError(t, "keyless repeat cancel", status, headers, body, 404, models.CodeResourceNotFound)
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestPublicGetEnrollmentRefusals(t *testing.T) {
	f := newPubEnrolFixture(t)
	writeOnly := publicCredential(t, f.env, "one", "pe-wo@example.com", `{"name":"wo","scopes":["enrollments:write"]}`)

	status, headers, body, _ := publicGet(t, f.env, f.full, statusPath)
	expectPublicError(t, "never enrolled", status, headers, body, 404, models.CodeResourceNotFound)
	status, headers, body, _ = publicGet(t, f.env, f.full, "/api/public/v1/members/PE-NOPE/enrollment")
	expectPublicError(t, "unknown member", status, headers, body, 404, models.CodeResourceNotFound)

	f.mustStart(t, termB1)
	// Reading is a members:read question; enrollments:write does not imply it.
	status, headers, body, _ = publicGet(t, f.env, writeOnly, statusPath)
	expectPublicError(t, "enrollments:write only", status, headers, body, 403, models.CodeInsufficientScope)
	// An enrolment at a site the credential cannot see is as if there were none.
	status, headers, body, _ = publicGet(t, f.env, f.siteAOnly, statusPath)
	expectPublicError(t, "outside the restriction", status, headers, body, 404, models.CodeResourceNotFound)

	f.mustStart(t, termA1)
	if status, _, body, raw := publicGet(t, f.env, f.siteAOnly, statusPath); status != 200 || body["terminal_serial"] != termA1 {
		t.Errorf("inside the restriction = %d %s", status, raw)
	}
	status, headers, body, _ = publicGet(t, f.env, f.full, statusPath+"?expand=1")
	expectPublicError(t, "unknown parameter", status, headers, body, 400, models.CodeUnknownParameter)
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestPublicCancelEnrollment(t *testing.T) {
	f := newPubEnrolFixture(t)
	readOnly := publicCredential(t, f.env, "one", "pe-ro@example.com", `{"name":"ro","scopes":["members:read"]}`)
	f.mustStart(t, termB1)

	// Refusals leave the enrolment live.
	status, headers, body, _ := publicCall(t, f.env, readOnly, http.MethodDelete, statusPath, "", "")
	expectPublicError(t, "read-only credential", status, headers, body, 403, models.CodeInsufficientScope)
	status, headers, body, _ = publicCall(t, f.env, f.siteAOnly, http.MethodDelete, statusPath, "", "")
	expectPublicError(t, "outside the restriction", status, headers, body, 404, models.CodeResourceNotFound)
	status, headers, body, _ = publicCall(t, f.env, f.full, http.MethodDelete, "/api/public/v1/members/PE-NOPE/enrollment", "", "")
	expectPublicError(t, "unknown member", status, headers, body, 404, models.CodeResourceNotFound)
	if n := liveEnrollments(t); n != 1 {
		t.Fatalf("refused cancels changed the live enrolment count to %d", n)
	}

	status, _, body, raw := publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "")
	if status != http.StatusOK || body["status"] != models.EnrollmentCancelled || body["terminal_serial"] != termB1 {
		t.Fatalf("cancel = %d %s", status, raw)
	}
	// The terminal never enters enrolment mode, and the door keeps checking.
	if jobs := f.terminal().enrolmentJobs(t, f.keyB1); len(jobs) != 0 {
		t.Errorf("a cancelled enrolment was still delivered (%d jobs)", len(jobs))
	}
	if n := auditCount(t, "ENROLMENT_CANCELLED", f.full[:17]); n != 1 {
		t.Errorf("ENROLMENT_CANCELLED audit rows = %d, want 1", n)
	}
	// The status read shows the cancellation; a second cancel has nothing live.
	if got := f.read(t)["status"]; got != models.EnrollmentCancelled {
		t.Errorf("status after cancel = %v", got)
	}
	status, headers, body, _ = publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "")
	expectPublicError(t, "nothing live", status, headers, body, 404, models.CodeResourceNotFound)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// PENDING -> IN_PROGRESS -> COMPLETED, then FAILED and EXPIRED on later
// attempts, each read back through the public status route.
func TestPublicEnrollmentLifecycleReadsBackWhatTheTerminalDid(t *testing.T) {
	f := newPubEnrolFixture(t)
	tf := f.terminal()

	started := f.mustStart(t, termA1)
	if got := f.read(t); got["status"] != models.EnrollmentPending || got["id"] != started["id"] {
		t.Errorf("before the fetch = %v", got)
	}
	jobs := tf.enrolmentJobs(t, f.keyA1)
	if len(jobs) != 1 {
		t.Fatalf("offered %d jobs, want 1", len(jobs))
	}
	inProgress := f.read(t)
	if inProgress["status"] != models.EnrollmentInProgress || inProgress["started_at"] == nil {
		t.Errorf("after the fetch = %v", inProgress)
	}
	tf.reportSuccess(t, f.keyA1, termA1, enrolMember, jobID(t, jobs[0]))
	done := f.read(t)
	if done["status"] != models.EnrollmentCompleted || done["completed_at"] == nil {
		t.Errorf("after the capture = %v", done)
	}
	// Cancelling a finished enrolment is refused and un-enrols nobody.
	status, headers, body, _ := publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "")
	expectPublicError(t, "cancel after completion", status, headers, body, 404, models.CodeResourceNotFound)
	if state, _ := tf.placementFor(t, enrolMember, termA1); state == "" {
		t.Error("the completed enrolment left no placement")
	}

	// A failed capture keeps the terminal's own words.
	f.mustStart(t, termA1)
	jobs = tf.enrolmentJobs(t, f.keyA1)
	if ack := f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": "the sensor did not respond"}, deviceAuth(f.keyA1)); ack.Code != http.StatusOK {
		t.Fatalf("failing the job = %d %s", ack.Code, ack.Raw)
	}
	failed := f.read(t)
	if failed["status"] != models.EnrollmentFailed || !strings.Contains(fmt.Sprint(failed["error"]), "sensor") {
		t.Errorf("after a failed capture = %v", failed)
	}

	// A window that runs out with nobody at the door reads as EXPIRED, and
	// the job goes with it.
	f.mustStart(t, termA2)
	mustExec(t, `UPDATE enrollment_requests SET expires_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
	              WHERE status IN ('PENDING','IN_PROGRESS')`)
	if got := f.read(t); got["status"] != models.EnrollmentExpired || got["terminal_serial"] != termA2 {
		t.Errorf("after the window closed = %v", got)
	}
	if jobs := tf.enrolmentJobs(t, f.keyA2); len(jobs) != 0 {
		t.Errorf("an expired enrolment was still offered (%d jobs)", len(jobs))
	}
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

func TestPublicEnrollmentRoutesRefuseWithoutACredential(t *testing.T) {
	env := newTestEnv(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/public/v1/terminals"},
		{http.MethodPost, fmt.Sprintf(startPathFmt, termA1)},
		{http.MethodGet, statusPath},
		{http.MethodDelete, statusPath},
	} {
		status, headers, body, _ := publicCall(t, env, "", tc.method, tc.path, "", "")
		expectPublicError(t, tc.method+" "+tc.path, status, headers, body, 401, models.CodeCredentialMissing)
		if headers.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s: no WWW-Authenticate", tc.method, tc.path)
		}
	}
}

// ---------------------------------------------------------------------------
// Cross-site supersede
// ---------------------------------------------------------------------------

func enrollmentRow(t *testing.T, id string) (status string, jobStatus string) {
	t.Helper()
	if err := database.DB.QueryRow(`SELECT er.status, sj.status FROM enrollment_requests er JOIN sync_jobs sj ON sj.id = er.sync_job_id
	              WHERE er.public_id::text = $1`, id).Scan(&status, &jobStatus); err != nil {
		t.Fatalf("reading enrolment %s: %v", id, err)
	}
	return status, jobStatus
}

// A credential restricted to Site A must not cancel, by superseding, a live
// enrolment at a Site B terminal it cannot see -- whether the terminal has
// picked the job up or not.
func TestASiteRestrictedStartNeverSupersedesAnEnrolmentOutsideItsSites(t *testing.T) {
	for _, fetched := range []bool{false, true} {
		t.Run(fmt.Sprintf("fetched=%v", fetched), func(t *testing.T) {
			f := newPubEnrolFixture(t)
			siteB := f.mustStart(t, termB1)
			id := siteB["id"].(string)
			wantStatus := models.EnrollmentPending
			if fetched {
				f.terminal().enrolmentJobs(t, f.keyB1)
				wantStatus = models.EnrollmentInProgress
			}
			_, jobBefore := enrollmentRow(t, id)

			for _, key := range []string{"", "cross-1"} {
				status, headers, body, raw := f.start(t, f.siteAOnly, termA1, "", key)
				expectPublicError(t, "start at A over a live B", status, headers, body, 409, models.CodeEnrollmentOutOfReach)
				// Nothing about the other site is disclosed.
				for _, leak := range []string{termB1, f.siteB, id, "Site B"} {
					if strings.Contains(raw, leak) {
						t.Errorf("refusal discloses %q: %s", leak, raw)
					}
				}
			}

			// The Site B enrolment and its job are exactly as they were.
			if st, job := enrollmentRow(t, id); st != wantStatus || job != jobBefore {
				t.Errorf("site B enrolment = %s / job %s, want %s / %s", st, job, wantStatus, jobBefore)
			}
			if n := queryInt(t, `SELECT count(*) FROM enrollment_requests`); n != 1 {
				t.Errorf("enrolments = %d, want only the site B one", n)
			}
			if jobs := f.terminal().enrolmentJobs(t, f.keyA1); len(jobs) != 0 {
				t.Errorf("site A terminal was offered %d jobs", len(jobs))
			}
			if n := auditCount(t, "ENROLMENT_STARTED", f.siteAOnly[:17]); n != 0 {
				t.Errorf("refused start was audited %d times", n)
			}
			// The unrestricted credential still reads it, live, at B.
			if got := f.read(t); got["id"] != id || got["terminal_serial"] != termB1 || got["status"] != wantStatus {
				t.Errorf("status read = %v", got)
			}
			// And site B's terminal can still finish it.
			if !fetched {
				jobs := f.terminal().enrolmentJobs(t, f.keyB1)
				if len(jobs) != 1 {
					t.Fatalf("site B terminal offered %d jobs, want 1", len(jobs))
				}
				f.terminal().reportSuccess(t, f.keyB1, termB1, enrolMember, jobID(t, jobs[0]))
				if st, _ := enrollmentRow(t, id); st != models.EnrollmentCompleted {
					t.Errorf("site B enrolment after capture = %s", st)
				}
			}
		})
	}
}

func TestASiteRestrictedStartStillSupersedesInsideItsSites(t *testing.T) {
	f := newPubEnrolFixture(t)

	// Its own earlier start, and one made by an unrestricted credential: both
	// at Site A, both replaceable.
	for _, starter := range []string{f.siteAOnly, f.full} {
		_, _, first, raw := f.start(t, starter, termA1, "", "")
		if first["id"] == nil {
			t.Fatalf("first start = %s", raw)
		}
		status, _, second, raw := f.start(t, f.siteAOnly, termA2, "", "")
		if status != http.StatusCreated || second["terminal_serial"] != termA2 {
			t.Fatalf("same-site replacement = %d %s", status, raw)
		}
		if st, job := enrollmentRow(t, first["id"].(string)); st != models.EnrollmentCancelled || job != "CANCELLED" {
			t.Errorf("replaced enrolment = %s / job %s, want CANCELLED / CANCELLED", st, job)
		}
		if n := liveEnrollments(t); n != 1 {
			t.Errorf("live enrolments = %d, want 1", n)
		}
		if jobs := f.terminal().enrolmentJobs(t, f.keyA1); len(jobs) != 0 {
			t.Errorf("the replaced terminal is still offered %d jobs", len(jobs))
		}
		f.terminal().enrolmentJobs(t, f.keyA2) // drain
		publicCall(t, f.env, f.full, http.MethodDelete, statusPath, "", "")
	}

	// An unrestricted credential keeps the documented behaviour across sites.
	b := f.mustStart(t, termB1)
	if a := f.mustStart(t, termA1); a["terminal_serial"] != termA1 {
		t.Errorf("unrestricted start = %v", a)
	}
	if st, _ := enrollmentRow(t, b["id"].(string)); st != models.EnrollmentCancelled {
		t.Errorf("unrestricted supersede left site B at %s", st)
	}
}

// A Site B window that has already lapsed is not live: it reads EXPIRED and
// does not block Site A -- and it is never relabelled CANCELLED.
func TestALapsedEnrolmentOutsideTheSitesDoesNotBlockAndIsNotRelabelled(t *testing.T) {
	f := newPubEnrolFixture(t)
	b := f.mustStart(t, termB1)
	mustExec(t, `UPDATE enrollment_requests SET expires_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'`)

	if status, _, _, raw := f.start(t, f.siteAOnly, termA1, "", ""); status != http.StatusCreated {
		t.Fatalf("start after site B lapsed = %d %s", status, raw)
	}
	if st, _ := enrollmentRow(t, b["id"].(string)); st != models.EnrollmentExpired {
		t.Errorf("lapsed site B enrolment = %s, want EXPIRED", st)
	}
}
