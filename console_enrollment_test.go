package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Operator-driven fingerprint enrolment, end to end.
//
// ---------------------------------------------------------------------------
// THE WORKFLOW UNDER TEST
// ---------------------------------------------------------------------------
//
//	an operator adds a person        -> a member row, and NO credential
//	an operator picks a TERMINAL     -> an ENROLL_FINGERPRINT job addressed to it
//	that terminal, and only that one -> fetches it and reports what happened
//	on success                       -> the credential becomes Enrolled
//
// ---------------------------------------------------------------------------
// THE PROPERTY THIS FILE EXISTS FOR
// ---------------------------------------------------------------------------
//
// ONLY THE SELECTED TERMINAL MAY EXECUTE THE JOB. Every other assertion here
// supports that one. It is enforced in four places that predate this feature --
// the fetch filters on device_id, both acknowledgement paths carry
// `AND device_id = $2`, and the schema refuses an unaddressed row -- and this
// suite proves the enrolment workflow actually rides on them rather than
// arranging its own weaker version.
//
// Exercised through NewRouter, so the session, CSRF, role gate and site grant
// all run. A boundary that is only correct because a test called the handler
// directly is not a boundary.

// enrolFixture is one company, one person, and two terminals to choose between.
type enrolFixture struct {
	env       *testEnv
	token     string
	csrf      string
	companyID int64

	// Two terminals at the same site, so "the operator picked A" and "B must
	// not act" are about the choice rather than about tenancy.
	serialA, keyA string
	serialB, keyB string
}

func newEnrolFixture(t *testing.T) *enrolFixture {
	t.Helper()
	cheapBcrypt(t)

	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"manager@example.com", models.RoleManager)

	f := &enrolFixture{
		env:       env,
		token:     token,
		csrf:      csrf,
		companyID: companyID,
		serialA:   "AT-000A",
		serialB:   "AT-000B",
	}
	f.keyA = env.registerDevice(env.siteAKey, f.serialA)
	f.keyB = env.registerDevice(env.siteAKey, f.serialB)

	f.createPerson(t, "P-0001", "Ada Okonkwo")
	return f
}

func (f *enrolFixture) createPerson(t *testing.T, externalID, name string) {
	t.Helper()
	code, body := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/people",
		fmt.Sprintf(`{"external_id":%q,"full_name":%q}`, externalID, name), f.token, f.csrf)
	if code != http.StatusCreated {
		t.Fatalf("creating %s = %d (%v)", externalID, code, body)
	}
}

// start asks one terminal, by serial, to enrol a person.
func (f *enrolFixture) start(t *testing.T, serial, externalID string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminals/"+serial+"/enrollments",
		fmt.Sprintf(`{"external_id":%q}`, externalID), f.token, f.csrf)
}

func (f *enrolFixture) readEnrollment(t *testing.T, externalID string) map[string]any {
	t.Helper()
	code, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/people/"+externalID+"/enrollment", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading enrolment for %s = %d (%v)", externalID, code, body)
	}
	return body
}

func (f *enrolFixture) enrollmentStatus(t *testing.T, externalID string) string {
	t.Helper()
	body := f.readEnrollment(t, externalID)
	enrollment, ok := body["enrollment"].(map[string]any)
	if !ok {
		return ""
	}
	status, _ := enrollment["status"].(string)
	return status
}

func (f *enrolFixture) personIsEnrolled(t *testing.T, externalID string) bool {
	t.Helper()
	code, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/people/"+externalID, "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading person %s = %d (%v)", externalID, code, body)
	}
	enrolled, _ := body["biometric_enrolled"].(bool)
	return enrolled
}

// enrolmentJobs returns the ENROLL_FINGERPRINT jobs a device is offered.
func (f *enrolFixture) enrolmentJobs(t *testing.T, deviceKey string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, job := range f.env.jobs(deviceKey) {
		if jobType, _ := job["job_type"].(string); jobType == models.SyncJobEnrollFingerprint {
			out = append(out, job)
		}
	}
	return out
}

// reportSuccess is the terminal doing what the firmware does on a good capture:
// the placement report, then the job acknowledgement.
//
// THE BODY IS THE FIRMWARE'S, FIELD FOR FIELD. buildEnrollmentResultBody() emits
// the locator and the `credential` object together, and the object is what makes
// the platform write a real credential and placement rather than only a string
// in a column. A fixture that sent the locator alone would exercise a request no
// deployed terminal makes, and would silently stop covering the placement.
func (f *enrolFixture) reportSuccess(t *testing.T, deviceKey, serial, externalID string, jobID int64) {
	t.Helper()

	result := f.env.do(http.MethodPost, "/api/v1/devices/enrollment/result", map[string]any{
		"member_id":            externalID,
		"fingerprint_template": "terminal:" + serial + ":slot:5",
		"credential": map[string]any{
			"credential_type": "FINGERPRINT",
			"template_format": "SENSOR_LOCAL",
			"vendor":          "ZFM",
			"terminal":        serial,
			"slot":            5,
		},
	}, deviceAuth(deviceKey))
	if result.Code != http.StatusOK {
		t.Fatalf("enrolment result = %d (body %s)", result.Code, result.Raw)
	}

	ack := f.env.do(http.MethodPost, jobPath(jobID),
		map[string]any{"status": "COMPLETED"}, deviceAuth(deviceKey))
	if ack.Code != http.StatusOK {
		t.Fatalf("acknowledging job %d = %d (body %s)", jobID, ack.Code, ack.Raw)
	}
}

// placementFor reads the placement state the platform holds for a person at a
// terminal, which is what "Enrolled" means underneath the boolean the console
// shows. Empty when there is no placement at all.
func (f *enrolFixture) placementFor(t *testing.T, externalID, serial string) (state string, slot int) {
	t.Helper()

	var slotValue sql.NullInt64
	err := database.DB.QueryRow(`
		SELECT cp.state, cp.slot
		  FROM credential_placements cp
		  JOIN credentials c ON c.id = cp.credential_id
		  JOIN people p ON p.id = c.person_id
		  JOIN devices d ON d.id = cp.device_id
		 WHERE p.external_id = $1 AND d.serial_number = $2
		 ORDER BY cp.id DESC
		 LIMIT 1`, externalID, serial).Scan(&state, &slotValue)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0
	}
	if err != nil {
		t.Fatalf("reading placement for %s at %s: %v", externalID, serial, err)
	}
	return state, int(slotValue.Int64)
}

// ---------------------------------------------------------------------------
// 1. Adding a person enrols nobody
// ---------------------------------------------------------------------------

func TestCreatingAPersonEnrolsNobodyAndChoosesNoTerminal(t *testing.T) {
	f := newEnrolFixture(t)

	if f.personIsEnrolled(t, "P-0001") {
		t.Error("a newly created person has a biometric credential")
	}

	// No enrolment exists, which is a different fact from a failed one.
	body := f.readEnrollment(t, "P-0001")
	if body["enrollment"] != nil {
		t.Errorf("creating a person produced an enrolment: %v", body["enrollment"])
	}

	// AND NO TERMINAL WAS ARMED. This is the half that matters: a CREATE fans
	// out to every terminal, and if it carried an enrolment with it, every door
	// in the company would be waiting to bind the next finger it saw.
	for _, key := range []string{f.keyA, f.keyB} {
		if jobs := f.enrolmentJobs(t, key); len(jobs) != 0 {
			t.Errorf("creating a person queued %d enrolment job(s) to a terminal", len(jobs))
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Starting an enrolment, and where the job goes
// ---------------------------------------------------------------------------

func TestEnrolmentJobIsAddressedToTheSelectedTerminalOnly(t *testing.T) {
	f := newEnrolFixture(t)

	code, body := f.start(t, f.serialA, "P-0001")
	if code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}
	if body["terminal_serial"] != f.serialA {
		t.Errorf("enrolment names terminal %v, want %s", body["terminal_serial"], f.serialA)
	}
	if body["status"] != models.EnrollmentPending {
		t.Errorf("new enrolment status = %v, want PENDING", body["status"])
	}

	// THE SELECTED TERMINAL IS OFFERED IT.
	jobsA := f.enrolmentJobs(t, f.keyA)
	if len(jobsA) != 1 {
		t.Fatalf("selected terminal was offered %d enrolment jobs, want 1", len(jobsA))
	}

	// EVERY OTHER TERMINAL IS NOT. This is requirement 8, and it is the
	// difference between a workflow and a fleet of doors racing to capture one
	// person's finger.
	if jobsB := f.enrolmentJobs(t, f.keyB); len(jobsB) != 0 {
		t.Errorf("an unselected terminal was offered %d enrolment jobs, want 0", len(jobsB))
	}
}

func TestEnrolmentJobCarriesThePersonAndTheChosenSerial(t *testing.T) {
	f := newEnrolFixture(t)

	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	jobs := f.enrolmentJobs(t, f.keyA)
	if len(jobs) != 1 {
		t.Fatalf("got %d enrolment jobs, want 1", len(jobs))
	}

	payload, ok := jobs[0]["payload"].(map[string]any)
	if !ok {
		t.Fatalf("job carried no payload: %v", jobs[0])
	}

	// EVERY FIELD NAME HERE IS PART OF A SHIPPED CONTRACT. The firmware's
	// parseEnrollmentPayload reads exactly these and refuses the job without the
	// first two, so a rename breaks doors rather than a build.
	if payload["member_id"] != "P-0001" {
		t.Errorf("payload member_id = %v, want P-0001", payload["member_id"])
	}
	if payload["serial_number"] != f.serialA {
		t.Errorf("payload serial_number = %v, want %s", payload["serial_number"], f.serialA)
	}
	if payload["full_name"] != "Ada Okonkwo" {
		t.Errorf("payload full_name = %v, want the person's name", payload["full_name"])
	}
	window, _ := payload["expires_in_seconds"].(float64)
	if int(window) != models.DefaultEnrollmentWindowSeconds {
		t.Errorf("payload expires_in_seconds = %v, want %d", payload["expires_in_seconds"],
			models.DefaultEnrollmentWindowSeconds)
	}

	// NO BIOMETRIC MATERIAL TRAVELS TOWARDS A TERMINAL EITHER. The job asks for
	// a capture; it never delivers one.
	encoded, _ := json.Marshal(payload)
	for _, forbidden := range []string{"template", "fingerprint_template", "biometric"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("enrolment payload carries %q: %s", forbidden, encoded)
		}
	}
}

func TestTheWindowIsClampedToWhatTheHardwareWillHold(t *testing.T) {
	f := newEnrolFixture(t)

	code, body := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminals/"+f.serialA+"/enrollments",
		`{"external_id":"P-0001","expires_in_seconds":999999}`, f.token, f.csrf)
	if code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	jobs := f.enrolmentJobs(t, f.keyA)
	payload := jobs[0]["payload"].(map[string]any)
	window, _ := payload["expires_in_seconds"].(float64)

	// Clamped rather than refused. An operator asking for ten hours has made a
	// judgement about their own site; the honest answer is the longest window
	// the terminal will actually honour, not an error.
	if int(window) != models.MaxEnrollmentWindowSeconds {
		t.Errorf("window = %v, want it clamped to %d", window, models.MaxEnrollmentWindowSeconds)
	}
}

// ---------------------------------------------------------------------------
// 3. Terminal selection: which terminals may be chosen
// ---------------------------------------------------------------------------

func TestADisabledTerminalCannotBeChosenForAnEnrolment(t *testing.T) {
	f := newEnrolFixture(t)

	// An ADMIN disables it, through the route an operator would use.
	_, adminToken, adminCSRF := consoleOperatorSession(t, f.env.router, f.companyID,
		"admin@example.com", models.RoleAdmin)
	code, body := consoleCall(t, f.env.router, http.MethodPut,
		"/api/v1/console/terminals/"+f.serialA+"/state",
		`{"disabled":true,"reason":"out for repair"}`, adminToken, adminCSRF)
	if code != http.StatusOK {
		t.Fatalf("disabling the terminal = %d (%v)", code, body)
	}

	code, body = f.start(t, f.serialA, "P-0001")
	if code != http.StatusConflict {
		t.Fatalf("enrolling at a disabled terminal = %d, want 409 (%v)", code, body)
	}

	// AND NOTHING WAS STARTED. A refusal that left a half-open enrolment would
	// hold the person's one-live slot against a terminal that cannot answer.
	if body := f.readEnrollment(t, "P-0001"); body["enrollment"] != nil {
		t.Errorf("a refused start left an enrolment behind: %v", body["enrollment"])
	}
}

func TestAnOfflineTerminalCanStillBeChosen(t *testing.T) {
	f := newEnrolFixture(t)

	// OFFLINE is not a refusal. A terminal that is merely unreachable now picks
	// the job up when it reconnects, and refusing to queue one would make the
	// feature unusable at exactly the sites that most need it.
	mustExec(t, `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = $1`, f.serialA)

	code, body := f.start(t, f.serialA, "P-0001")
	if code != http.StatusCreated {
		t.Fatalf("enrolling at an offline terminal = %d, want 201 (%v)", code, body)
	}
	if body["status"] != models.EnrollmentPending {
		t.Errorf("status = %v, want PENDING while the terminal is away", body["status"])
	}

	// The job is waiting for it. Nothing has been delivered, and nothing has
	// failed -- the enrolment simply has not started.
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentPending {
		t.Errorf("enrolment status = %s, want PENDING", got)
	}
}

func TestAnEnrolmentForAnUnknownPersonIsRefused(t *testing.T) {
	f := newEnrolFixture(t)

	code, body := f.start(t, f.serialA, "NOBODY")
	if code != http.StatusNotFound {
		t.Errorf("enrolling an unknown person = %d, want 404 (%v)", code, body)
	}
}

func TestAnEnrolmentAtAnotherTenantsTerminalIsNotFound(t *testing.T) {
	f := newEnrolFixture(t)

	// Site C belongs to Company Two. Naming its terminal from Company One's
	// session must be a 404 -- not a 403, which would confirm it exists.
	other := f.env.registerDevice(f.env.siteCKey, "AT-OTHER")
	_ = other

	code, body := f.start(t, "AT-OTHER", "P-0001")
	if code != http.StatusNotFound {
		t.Errorf("enrolling at another tenant's terminal = %d, want 404 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// 4. The terminal fetches it, and the state moves
// ---------------------------------------------------------------------------

func TestFetchingTheJobMovesTheEnrolmentToInProgress(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// "Waiting for terminal" and "the terminal is showing the prompt" are the
	// two states an operator most needs to tell apart, and nothing else could
	// distinguish them: a job stays PENDING while it is being applied.
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentPending {
		t.Fatalf("before the fetch, status = %s, want PENDING", got)
	}

	f.enrolmentJobs(t, f.keyA)

	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentInProgress {
		t.Errorf("after the terminal fetched it, status = %s, want IN_PROGRESS", got)
	}
}

func TestFetchingByAnotherTerminalMovesNothing(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// Terminal B polls. It is offered nothing, and the enrolment addressed to A
	// is untouched by B having asked.
	f.env.jobs(f.keyB)

	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentPending {
		t.Errorf("another terminal's poll moved the enrolment to %s", got)
	}
}

// ---------------------------------------------------------------------------
// 5. Success: the credential becomes Enrolled
// ---------------------------------------------------------------------------

func TestASuccessfulEnrolmentMarksTheCredentialEnrolled(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	jobs := f.enrolmentJobs(t, f.keyA)
	if len(jobs) != 1 {
		t.Fatalf("got %d enrolment jobs, want 1", len(jobs))
	}

	// NOT ENROLLED UNTIL THE TERMINAL SAYS SO. The credential must not flip when
	// the job is created or delivered -- only when a finger has actually been
	// captured.
	if f.personIsEnrolled(t, "P-0001") {
		t.Fatal("the credential was Enrolled before the terminal reported anything")
	}

	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, jobs[0]))

	if !f.personIsEnrolled(t, "P-0001") {
		t.Error("the credential is still Not enrolled after a successful report")
	}
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentCompleted {
		t.Errorf("enrolment status = %s, want COMPLETED", got)
	}

	// And the console can say WHICH terminal captured it.
	enrollment := f.readEnrollment(t, "P-0001")["enrollment"].(map[string]any)
	if enrollment["terminal_serial"] != f.serialA {
		t.Errorf("completed enrolment names %v, want %s", enrollment["terminal_serial"], f.serialA)
	}
	if enrollment["completed_at"] == nil {
		t.Error("a completed enrolment carries no completion time")
	}

	// THE PLACEMENT IS WHAT "ENROLLED" MEANS UNDERNEATH. The console shows a
	// boolean, but the row behind it records which terminal holds the finger and
	// in which slot -- and that is the fact an operator needs when one door
	// admits somebody and another does not.
	state, slot := f.placementFor(t, "P-0001", f.serialA)
	if state != models.PlacementPlaced {
		t.Errorf("placement state = %q, want PLACED", state)
	}
	if slot != 5 {
		t.Errorf("placement slot = %d, want the slot the terminal reported", slot)
	}

	// AND ONLY AT THAT TERMINAL. A placement written against the wrong door
	// would tell an operator to send somebody to a terminal that has never seen
	// their finger.
	if other, _ := f.placementFor(t, "P-0001", f.serialB); other != "" {
		t.Errorf("a placement was written against the terminal that did not enrol (%q)", other)
	}
}

func TestASuccessfulEnrolmentIsRecordedAsAFieldEvent(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)
	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, jobs[0]))

	code, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/events?event_type="+models.EventEnrolled, "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading events = %d (%v)", code, body)
	}

	events := listOf(t, body, "events")
	if len(events) != 1 {
		t.Fatalf("got %d enrolment events, want 1 (%v)", len(events), body)
	}

	event := events[0].(map[string]any)

	// RECORDED, NOT GRANTED. Nothing was admitted or refused and no door moved.
	// An enrolment showing up as a grant would put a door opening into the trail
	// an attendance report is built from.
	if event["decision"] != models.DecisionRecorded {
		t.Errorf("enrolment event decision = %v, want RECORDED", event["decision"])
	}
}

func TestStartingAnEnrolmentIsAuditedAsAnOperatorAction(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// THE AUDIT TRAIL AND THE EVENT TRAIL ARE SEPARATE, and this is why: an
	// operator deciding which door a person should walk to is administrative
	// information about a colleague's action. What the terminal then did is a
	// field event. Two authors, two trails.
	_, adminToken, _ := consoleOperatorSession(t, f.env.router, f.companyID,
		"auditor@example.com", models.RoleAdmin)

	code, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/audit?action=ENROLMENT_STARTED", "", adminToken, "")
	if code != http.StatusOK {
		t.Fatalf("reading the audit trail = %d (%v)", code, body)
	}
	entries := listOf(t, body, "entries")
	if len(entries) != 1 {
		t.Fatalf("got %d ENROLMENT_STARTED audit entries, want 1 (%v)", len(entries), body)
	}
	entry := entries[0].(map[string]any)
	if entry["actor_email"] != "manager@example.com" {
		t.Errorf("audit actor = %v, want the operator who started it", entry["actor_email"])
	}
}

// OFFLINE DELIVERY. The terminal was unreachable when the operator started the
// enrolment, and picks the job up when it reconnects. Nothing retries on the
// platform side and nothing needs to: the job is simply still there.
func TestAJobQueuedForAnOfflineTerminalIsDeliveredWhenItReturns(t *testing.T) {
	f := newEnrolFixture(t)

	mustExec(t, `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = $1`, f.serialA)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment at an offline terminal = %d (%v)", code, body)
	}

	// It is queued and untouched while the terminal is away.
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentPending {
		t.Fatalf("while offline, status = %s, want PENDING", got)
	}

	// The terminal comes back and polls, which is the whole of "delivery".
	jobs := f.enrolmentJobs(t, f.keyA)
	if len(jobs) != 1 {
		t.Fatalf("a returning terminal was offered %d enrolment jobs, want 1", len(jobs))
	}
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentInProgress {
		t.Errorf("after the terminal returned, status = %s, want IN_PROGRESS", got)
	}

	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, jobs[0]))
	if !f.personIsEnrolled(t, "P-0001") {
		t.Error("the enrolment did not complete after the terminal returned")
	}
}

// REDELIVERY. A terminal that fetched the job and died before acknowledging gets
// it again once the delivery lease expires -- the property the outbox has always
// had, asserted here because an enrolment is the job type where losing one
// silently would leave an operator watching a screen for ever.
func TestAnUnacknowledgedEnrolmentJobIsRedelivered(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	first := f.enrolmentJobs(t, f.keyA)
	if len(first) != 1 {
		t.Fatalf("first fetch got %d enrolment jobs, want 1", len(first))
	}

	// Within the lease it is hidden, so a second poll does not hand the same
	// work out twice.
	if again := f.enrolmentJobs(t, f.keyA); len(again) != 0 {
		t.Errorf("the job was handed out twice inside its delivery lease")
	}

	// The terminal never acknowledged. Once the lease is up it is offered again.
	mustExec(t, `UPDATE sync_jobs
	                SET next_attempt_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
	              WHERE job_type = 'ENROLL_FINGERPRINT'`)

	redelivered := f.enrolmentJobs(t, f.keyA)
	if len(redelivered) != 1 {
		t.Fatalf("after the lease expired the job was not redelivered (%d jobs)", len(redelivered))
	}
	if jobID(t, redelivered[0]) != jobID(t, first[0]) {
		t.Error("redelivery produced a different job rather than the same one")
	}

	// AND STILL ONLY TO THE SELECTED TERMINAL.
	if jobsB := f.enrolmentJobs(t, f.keyB); len(jobsB) != 0 {
		t.Errorf("redelivery offered the job to another terminal (%d jobs)", len(jobsB))
	}
}

// ---------------------------------------------------------------------------
// 6. Misaddressed and unauthorised reports
// ---------------------------------------------------------------------------

func TestAnotherTerminalCannotAcknowledgeThisTerminalsEnrolmentJob(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	jobs := f.enrolmentJobs(t, f.keyA)
	id := jobID(t, jobs[0])

	// TERMINAL B TRIES TO CLAIM IT. It never received the job -- the fetch
	// filters on device_id -- but a terminal that guessed an id, or a
	// compromised one replaying another's traffic, must still be refused.
	ack := f.env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "COMPLETED"}, deviceAuth(f.keyB))
	if ack.Code != http.StatusNotFound {
		t.Errorf("terminal B acknowledging terminal A's job = %d, want 404 (body %s)",
			ack.Code, ack.Raw)
	}

	// The enrolment is untouched: still addressed to A, still not completed.
	if got := f.enrollmentStatus(t, "P-0001"); got == models.EnrollmentCompleted {
		t.Error("another terminal's acknowledgement completed the enrolment")
	}
	if f.personIsEnrolled(t, "P-0001") {
		t.Error("another terminal's acknowledgement enrolled the person")
	}
}

func TestAnotherTerminalCannotFailThisTerminalsEnrolmentJob(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)
	id := jobID(t, jobs[0])

	ack := f.env.do(http.MethodPost, jobPath(id),
		map[string]any{"status": "FAILED", "error": "not mine"}, deviceAuth(f.keyB))
	if ack.Code != http.StatusNotFound {
		t.Errorf("terminal B failing terminal A's job = %d, want 404 (body %s)", ack.Code, ack.Raw)
	}

	// Failing somebody else's enrolment is as damaging as completing it: the
	// operator would be told the wrong door could not capture a finger.
	if got := f.enrollmentStatus(t, "P-0001"); got == models.EnrollmentFailed {
		t.Error("another terminal's report failed the enrolment")
	}
}

// ---------------------------------------------------------------------------
// 7. Failure, expiry, cancellation
// ---------------------------------------------------------------------------

func TestAFailedEnrolmentKeepsThePersonAndTheirCredentialState(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)

	ack := f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": "the sensor did not respond"},
		deviceAuth(f.keyA))
	if ack.Code != http.StatusOK {
		t.Fatalf("failing the job = %d (body %s)", ack.Code, ack.Raw)
	}

	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentFailed {
		t.Errorf("enrolment status = %s, want FAILED", got)
	}
	if f.personIsEnrolled(t, "P-0001") {
		t.Error("a failed enrolment produced a credential")
	}

	// The person is still there and still active. Losing somebody because a
	// finger did not read would be the worst possible answer to a failed
	// capture.
	code, person := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/people/P-0001", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the person after a failure = %d (%v)", code, person)
	}
	if person["active"] != true {
		t.Error("a failed enrolment deactivated the person")
	}

	// THE TERMINAL'S OWN WORDS ARE KEPT. "Sensor error" and "nobody came" send
	// an operator to two different places.
	enrollment := f.readEnrollment(t, "P-0001")["enrollment"].(map[string]any)
	if !strings.Contains(fmt.Sprint(enrollment["error_message"]), "sensor") {
		t.Errorf("error_message = %v, want the terminal's own words",
			enrollment["error_message"])
	}
}

func TestAFailedEnrolmentIsNotAutomaticallyRetried(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)

	f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": "no finger"}, deviceAuth(f.keyA))

	// max_attempts is 1 for this job type. Re-offering it on a backoff would
	// re-arm the reader minutes later with nobody there, for a person the
	// operator may by then have enrolled somewhere else. Retrying is an operator
	// decision.
	var status string
	mustScan(t, `SELECT status FROM sync_jobs WHERE job_type = 'ENROLL_FINGERPRINT'`, &status)
	if status != "FAILED" {
		t.Errorf("the enrolment job is %s, want it parked FAILED rather than requeued", status)
	}
}

func TestAnExpiredEnrolmentClosesTheWindowAndTheJob(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// The window closes with nobody at the door.
	mustExec(t, `UPDATE enrollment_requests
	                SET expires_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
	              WHERE status IN ('PENDING','IN_PROGRESS')`)

	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentExpired {
		t.Errorf("enrolment status = %s, want EXPIRED", got)
	}
	if f.personIsEnrolled(t, "P-0001") {
		t.Error("an expired enrolment produced a credential")
	}

	// THE JOB GOES WITH IT. A window that closed on the platform while the job
	// was still deliverable would leave a terminal entering enrolment mode for
	// an appointment nobody is coming to.
	if jobs := f.enrolmentJobs(t, f.keyA); len(jobs) != 0 {
		t.Errorf("an expired enrolment was still offered to the terminal (%d jobs)", len(jobs))
	}
}

func TestCancellingStopsTheJobReachingTheTerminal(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	code, body := consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/people/P-0001/enrollment", "", f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("cancelling = %d (%v)", code, body)
	}
	if body["status"] != models.EnrollmentCancelled {
		t.Errorf("status after cancelling = %v, want CANCELLED", body["status"])
	}

	// THE CLEAN CASE: the terminal had not polled yet, so it never enters
	// enrolment mode at all.
	if jobs := f.enrolmentJobs(t, f.keyA); len(jobs) != 0 {
		t.Errorf("a cancelled enrolment was still delivered (%d jobs)", len(jobs))
	}
}

func TestACancelledEnrolmentNeverBindsAFingerprint(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// THE HARD CASE. The terminal already has the job and is showing the prompt
	// when the operator cancels, and somebody presents a finger anyway.
	jobs := f.enrolmentJobs(t, f.keyA)
	if len(jobs) != 1 {
		t.Fatalf("got %d enrolment jobs, want 1", len(jobs))
	}

	code, body := consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/people/P-0001/enrollment", "", f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("cancelling = %d (%v)", code, body)
	}

	result := f.env.do(http.MethodPost, "/api/v1/devices/enrollment/result", map[string]any{
		"member_id":            "P-0001",
		"fingerprint_template": "terminal:" + f.serialA + ":slot:5",
	}, deviceAuth(f.keyA))

	// 200, not an error: the terminal did nothing wrong and there is nothing for
	// it to retry. What matters is that nothing was bound.
	if result.Code != http.StatusOK {
		t.Fatalf("late report after a cancellation = %d, want 200 (body %s)",
			result.Code, result.Raw)
	}
	if bound, _ := result.Body["bound"].(bool); bound {
		t.Error("a cancelled enrolment reported bound: true")
	}

	// REQUIREMENT 7, DIRECTLY. Cancelled means cancelled.
	if f.personIsEnrolled(t, "P-0001") {
		t.Error("a cancelled enrolment bound a fingerprint anyway")
	}
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentCancelled {
		t.Errorf("enrolment status = %s, want it to stay CANCELLED", got)
	}

	// And the acknowledgement is refused too, so the job cannot be completed
	// behind the cancellation.
	ack := f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "COMPLETED"}, deviceAuth(f.keyA))
	if ack.Code == http.StatusOK {
		if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentCancelled {
			t.Errorf("acknowledging a cancelled job moved the enrolment to %s", got)
		}
	}
}

func TestCancellingWhenNothingIsLiveIsRefusedPlainly(t *testing.T) {
	f := newEnrolFixture(t)

	code, body := consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/people/P-0001/enrollment", "", f.token, f.csrf)
	if code != http.StatusConflict {
		t.Errorf("cancelling with nothing live = %d, want 409 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// 8. Retry
// ---------------------------------------------------------------------------

func TestARetryAtTheSameTerminalSupersedesTheFailedAttempt(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("first start = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)
	f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": "no finger"}, deviceAuth(f.keyA))

	// The operator tries again at the same door.
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("retry = %d (%v)", code, body)
	}

	retryJobs := f.enrolmentJobs(t, f.keyA)
	if len(retryJobs) != 1 {
		t.Fatalf("after a retry the terminal was offered %d jobs, want 1", len(retryJobs))
	}
	if jobID(t, retryJobs[0]) == jobID(t, jobs[0]) {
		t.Error("the retry re-offered the failed job rather than a new one")
	}

	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, retryJobs[0]))
	if !f.personIsEnrolled(t, "P-0001") {
		t.Error("the retry did not enrol the person")
	}
}

func TestARetryAtADifferentTerminalMovesTheJob(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("first start = %d (%v)", code, body)
	}
	f.enrolmentJobs(t, f.keyA)

	// The customer walks to the other door instead.
	if code, body := f.start(t, f.serialB, "P-0001"); code != http.StatusCreated {
		t.Fatalf("retry at another terminal = %d (%v)", code, body)
	}

	// TERMINAL B NOW HAS IT.
	jobsB := f.enrolmentJobs(t, f.keyB)
	if len(jobsB) != 1 {
		t.Fatalf("the second terminal was offered %d enrolment jobs, want 1", len(jobsB))
	}
	payload := jobsB[0]["payload"].(map[string]any)
	if payload["serial_number"] != f.serialB {
		t.Errorf("the retry names %v, want %s", payload["serial_number"], f.serialB)
	}

	// AND TERMINAL A NO LONGER DOES. A superseded enrolment that stayed live at
	// the first door would leave a reader armed for somebody who has walked away
	// from it.
	if jobsA := f.enrolmentJobs(t, f.keyA); len(jobsA) != 0 {
		t.Errorf("the first terminal is still offered %d enrolment jobs", len(jobsA))
	}

	f.reportSuccess(t, f.keyB, f.serialB, "P-0001", jobID(t, jobsB[0]))

	enrollment := f.readEnrollment(t, "P-0001")["enrollment"].(map[string]any)
	if enrollment["terminal_serial"] != f.serialB {
		t.Errorf("the completed enrolment names %v, want %s",
			enrollment["terminal_serial"], f.serialB)
	}
}

func TestOnlyOneEnrolmentIsLivePerPersonAtATime(t *testing.T) {
	f := newEnrolFixture(t)
	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("first start = %d (%v)", code, body)
	}
	if code, body := f.start(t, f.serialB, "P-0001"); code != http.StatusCreated {
		t.Fatalf("second start = %d (%v)", code, body)
	}

	// Two live enrolments would be two terminals waiting to bind the same
	// finger, and whichever captured first would leave the other armed for
	// somebody who is no longer coming.
	var live int
	mustScan(t, `SELECT count(*) FROM enrollment_requests
	              WHERE status IN ('PENDING','IN_PROGRESS')`, &live)
	if live != 1 {
		t.Errorf("%d live enrolments for one person, want 1", live)
	}
}

// ---------------------------------------------------------------------------
// 9. Authorization and tenancy
// ---------------------------------------------------------------------------

func TestEnrolmentRoutesRequireTheRightRoleAndASession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	env.registerDevice(env.siteAKey, "AT-000A")

	routes := []struct {
		method, path, body, minimumRole string
	}{
		{http.MethodPost, "/api/v1/console/terminals/AT-000A/enrollments",
			`{"external_id":"P-0001"}`, models.RoleManager},
		{http.MethodGet, "/api/v1/console/people/P-0001/enrollment", "", models.RoleViewer},
		{http.MethodDelete, "/api/v1/console/people/P-0001/enrollment", "", models.RoleManager},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			// No session at all.
			code, body := consoleCall(t, env.router, route.method, route.path, route.body, "", "")
			if code != http.StatusUnauthorized {
				t.Errorf("without a session = %d, want 401 (%v)", code, body)
			}

			// A SITE API KEY IS NOT BROWSER AUTHENTICATION. It is the
			// provisioning secret, and presenting it here must achieve nothing.
			req := newRequestWithSiteKey(t, route.method, route.path, route.body, env.siteAKey)
			if got := serve(env.router, req); got != http.StatusUnauthorized {
				t.Errorf("with a site API key = %d, want 401", got)
			}

			// A VIEWER may read and may not write.
			if route.minimumRole == models.RoleManager {
				_, token, csrf := consoleOperatorSession(t, env.router, companyID,
					fmt.Sprintf("viewer-%s@example.com", strings.ToLower(route.method)),
					models.RoleViewer)
				code, body := consoleCall(t, env.router, route.method, route.path,
					route.body, token, csrf)
				if code != http.StatusForbidden {
					t.Errorf("as a VIEWER = %d, want 403 (%v)", code, body)
				}
			}
		})
	}
}

func TestAScopedOperatorCannotEnrolAtAnUngrantedSite(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")

	// Site B's terminal, and an operator granted only Site A.
	env.registerDevice(env.siteBKey, "AT-SITEB")

	user := mustCreateOperator(t, companyID, "scoped@example.com", models.RoleManager)
	siteA := operatorSitePublicID(t, "Site A")
	if err := database.ReplaceSiteGrants(companyID, user.ID, []string{siteA}); err != nil {
		t.Fatalf("granting site A: %v", err)
	}
	token, csrf := login(t, env.router, "scoped@example.com", testPassword)

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/terminals/AT-SITEB/enrollments",
		`{"external_id":"P-0001"}`, token, csrf)
	if code != http.StatusForbidden {
		t.Errorf("enrolling at an ungranted site = %d, want 403 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// 10. The access decision is unchanged by any of this
// ---------------------------------------------------------------------------

// ENROLMENT IS NOT AUTHORIZATION, and this is the regression that matters most.
//
// The two are strictly separate: a credential says "we can recognise this
// person", and a permission says "this person may come in". Enrolling somebody
// must not admit them, and it must not change an answer the authorization engine
// was already giving. CompleteEnrollment once set `active = true`, which made an
// enrolment report a way for a terminal to restore access an operator had
// revoked -- the failure this asserts against.
func TestEnrolmentDoesNotChangeTheAccessDecision(t *testing.T) {
	f := newEnrolFixture(t)

	evaluate := func() (bool, string) {
		code, body := consoleCall(t, f.env.router, http.MethodPost,
			"/api/v1/console/terminals/"+f.serialA+"/evaluate",
			`{"external_id":"P-0001"}`, f.token, f.csrf)
		if code != http.StatusOK {
			t.Fatalf("evaluating access = %d (%v)", code, body)
		}
		granted, _ := body["granted"].(bool)
		reason, _ := body["reason"].(string)
		return granted, reason
	}

	beforeGranted, beforeReason := evaluate()

	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment = %d (%v)", code, body)
	}

	// Arming a terminal changes nothing about who may come in.
	if granted, reason := evaluate(); granted != beforeGranted || reason != beforeReason {
		t.Errorf("starting an enrolment changed the access decision from (%v, %s) to (%v, %s)",
			beforeGranted, beforeReason, granted, reason)
	}

	jobs := f.enrolmentJobs(t, f.keyA)
	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, jobs[0]))

	if !f.personIsEnrolled(t, "P-0001") {
		t.Fatal("the enrolment did not complete")
	}

	// NOR DOES COMPLETING ONE. A person with a credential and no permission
	// still reaches nothing, which is the property that keeps enrolment from
	// being a back door into the authorization model.
	if granted, reason := evaluate(); granted != beforeGranted || reason != beforeReason {
		t.Errorf("a completed enrolment changed the access decision from (%v, %s) to (%v, %s)",
			beforeGranted, beforeReason, granted, reason)
	}
}

// A suspended person can still be enrolled -- useful, since the finger is
// captured ready for their return -- and stays suspended.
func TestEnrolmentNeverReactivatesASuspendedPerson(t *testing.T) {
	f := newEnrolFixture(t)

	code, body := consoleCall(t, f.env.router, http.MethodPut,
		"/api/v1/console/people/P-0001",
		`{"full_name":"Ada Okonkwo","active":false}`, f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("deactivating = %d (%v)", code, body)
	}

	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting an enrolment for a suspended person = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)
	f.reportSuccess(t, f.keyA, f.serialA, "P-0001", jobID(t, jobs[0]))

	code, person := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/people/P-0001", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the person = %d (%v)", code, person)
	}
	if person["active"] != false {
		t.Error("enrolling a suspended person reactivated them")
	}
	if person["biometric_enrolled"] != true {
		t.Error("a suspended person was not enrolled")
	}
}

// ---------------------------------------------------------------------------
// 10. The bench path still works
// ---------------------------------------------------------------------------

func TestATerminalCanStillEnrolWithNoEnrolmentRequestAtAll(t *testing.T) {
	f := newEnrolFixture(t)

	// THE TECHNICIAN'S PATH, and it must keep working. A terminal enrolling at
	// its own console with no platform involvement is the documented fallback
	// when the network is down, and gating the result on a live enrolment would
	// take away the only way to enrol during an outage.
	result := f.env.do(http.MethodPost, "/api/v1/devices/enrollment/result", map[string]any{
		"member_id":            "P-0001",
		"fingerprint_template": "terminal:" + f.serialA + ":slot:9",
	}, deviceAuth(f.keyA))
	if result.Code != http.StatusOK {
		t.Fatalf("bench enrolment = %d, want 200 (body %s)", result.Code, result.Raw)
	}

	if !f.personIsEnrolled(t, "P-0001") {
		t.Error("a bench enrolment did not record the credential")
	}
}
