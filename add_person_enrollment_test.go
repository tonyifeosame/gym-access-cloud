package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// Adding a person, then enrolling them, as the console now runs it: one
// request creates the record, a second addresses an enrolment to a terminal.
//
// console_enrollment_test.go covers the enrolment protocol in depth. This file
// covers the seam the Add Person workflow rests on -- that the first request
// stands on its own whatever the second does -- and the two outcomes that
// workflow surfaces which were not asserted before: a terminal reporting that
// the finger is already somebody's, and a terminal that cannot be chosen
// because it was never provisioned or has been retired.

func TestCreatingAPersonStandsAloneWhateverTheEnrolmentDoes(t *testing.T) {
	f := newEnrolFixture(t)

	// The create response already says what the console needs to draw the
	// enrolment step: who, and that they hold no credential yet.
	code, body := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/people",
		`{"external_id":"P-0002","full_name":"Chidi Okafor"}`, f.token, f.csrf)
	if code != http.StatusCreated {
		t.Fatalf("creating the person = %d (%v)", code, body)
	}
	if body["biometric_enrolled"] != false {
		t.Errorf("a freshly created person reports biometric_enrolled=%v", body["biometric_enrolled"])
	}

	// Nothing about enrolment rode along with the create: no enrolment row,
	// no job addressed to any terminal.
	if enrollment := f.readEnrollment(t, "P-0002")["enrollment"]; enrollment != nil {
		t.Errorf("creating a person started an enrolment: %v", enrollment)
	}
	if n := len(f.enrolmentJobs(t, f.keyA)) + len(f.enrolmentJobs(t, f.keyB)); n != 0 {
		t.Errorf("creating a person queued %d enrolment jobs", n)
	}

	// The second request fails part way: the terminal takes the job and
	// cannot capture. The person is exactly as the first request left them.
	if code, body := f.start(t, f.serialA, "P-0002"); code != http.StatusCreated {
		t.Fatalf("starting the enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)
	if len(jobs) != 1 {
		t.Fatalf("terminal A was offered %d enrolment jobs, want 1", len(jobs))
	}
	ack := f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": "no usable fingerprint could be read"},
		deviceAuth(f.keyA))
	if ack.Code != http.StatusOK {
		t.Fatalf("failing the job = %d (body %s)", ack.Code, ack.Raw)
	}

	code, person := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/people/P-0002", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the person after the failure = %d (%v)", code, person)
	}
	if person["active"] != true || person["biometric_enrolled"] != false ||
		person["full_name"] != "Chidi Okafor" {
		t.Errorf("the failed enrolment changed the person: %v", person)
	}
	if state, _ := f.placementFor(t, "P-0002", f.serialA); state != "" {
		t.Errorf("a failed enrolment wrote a placement in state %q", state)
	}
	if n := queryInt(t, `SELECT count(*) FROM credentials c JOIN people p ON p.id = c.person_id
	                     WHERE p.external_id = 'P-0002'`); n != 0 {
		t.Errorf("a failed enrolment wrote %d credential rows", n)
	}
}

func TestATerminalReportingAnAlreadyEnrolledFingerLeavesThePersonUnenrolled(t *testing.T) {
	f := newEnrolFixture(t)

	if code, body := f.start(t, f.serialA, "P-0001"); code != http.StatusCreated {
		t.Fatalf("starting the enrolment = %d (%v)", code, body)
	}
	jobs := f.enrolmentJobs(t, f.keyA)

	// The firmware refuses to bind a finger that already resolves to a
	// different slot, and fails the job in its own words. It never posts an
	// enrolment result, so there is nothing for the platform to bind.
	reason := "that finger is already enrolled at slot 7"
	ack := f.env.do(http.MethodPost, jobPath(jobID(t, jobs[0])),
		map[string]any{"status": "FAILED", "error": reason}, deviceAuth(f.keyA))
	if ack.Code != http.StatusOK {
		t.Fatalf("failing the job = %d (body %s)", ack.Code, ack.Raw)
	}

	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentFailed {
		t.Errorf("enrolment status = %s, want FAILED", got)
	}
	enrollment := f.readEnrollment(t, "P-0001")["enrollment"].(map[string]any)
	if !strings.Contains(fmt.Sprint(enrollment["error_message"]), "already enrolled") {
		t.Errorf("error_message = %v, want the terminal's own words", enrollment["error_message"])
	}
	if f.personIsEnrolled(t, "P-0001") {
		t.Error("a refused duplicate finger produced a credential")
	}
	if state, _ := f.placementFor(t, "P-0001", f.serialA); state != "" {
		t.Errorf("a refused duplicate finger wrote a placement in state %q", state)
	}

	// The operator tries again at the other door. The retry supersedes the
	// failed attempt and is addressed to B only.
	if code, body := f.start(t, f.serialB, "P-0001"); code != http.StatusCreated {
		t.Fatalf("retrying at terminal B = %d (%v)", code, body)
	}
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentPending {
		t.Errorf("enrolment status after retry = %s, want PENDING", got)
	}
	if n := len(f.enrolmentJobs(t, f.keyB)); n != 1 {
		t.Errorf("terminal B was offered %d enrolment jobs after the retry, want 1", n)
	}
	if n := len(f.enrolmentJobs(t, f.keyA)); n != 0 {
		t.Errorf("terminal A was re-offered %d enrolment jobs after a retry elsewhere", n)
	}
	// Fetching is what moves it to IN_PROGRESS -- B has the prompt up now.
	if got := f.enrollmentStatus(t, "P-0001"); got != models.EnrollmentInProgress {
		t.Errorf("enrolment status after B fetched it = %s, want IN_PROGRESS", got)
	}
}

func TestATerminalThatIsNotAvailableCannotBeChosen(t *testing.T) {
	f := newEnrolFixture(t)

	// Never provisioned: a device row with no credential of its own, which is
	// what a terminal looks like between being named and being claimed.
	mustExec(t, `INSERT INTO devices (site_id, serial_number, device_name, device_type, status)
	             VALUES ($1, 'AT-NEVER', 'Unclaimed', 'TERMINAL', 'PROVISIONING')`,
		siteIDByKey(t, f.env.siteAKey))

	code, body := f.start(t, "AT-NEVER", "P-0001")
	if code != http.StatusConflict {
		t.Errorf("enrolling at an unprovisioned terminal = %d, want 409 (%v)", code, body)
	}
	if enrollment := f.readEnrollment(t, "P-0001")["enrollment"]; enrollment != nil {
		t.Errorf("a refused start left an enrolment behind: %v", enrollment)
	}

	// Retired: an ADMIN removes terminal A, through the route an operator uses.
	_, adminToken, adminCSRF := consoleOperatorSession(t, f.env.router, f.companyID,
		"admin@example.com", models.RoleAdmin)
	code, body = consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/terminals/"+f.serialA, `{"reason":"decommissioned"}`, adminToken, adminCSRF)
	if code != http.StatusOK && code != http.StatusNoContent {
		t.Fatalf("retiring terminal A = %d (%v)", code, body)
	}

	code, body = f.start(t, f.serialA, "P-0001")
	if code != http.StatusNotFound {
		t.Errorf("enrolling at a retired terminal = %d, want 404 (%v)", code, body)
	}
	if enrollment := f.readEnrollment(t, "P-0001")["enrollment"]; enrollment != nil {
		t.Errorf("a refused start left an enrolment behind: %v", enrollment)
	}

	// The other terminal is unaffected, so the operator has somewhere to go.
	if code, body := f.start(t, f.serialB, "P-0001"); code != http.StatusCreated {
		t.Errorf("enrolling at the remaining terminal = %d (%v)", code, body)
	}
}
