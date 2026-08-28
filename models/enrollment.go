package models

import "time"

// Operator-driven fingerprint enrolment.
//
// The workflow this describes, end to end:
//
//	an operator adds a person        -> a member row, and NO credential
//	an operator picks a TERMINAL     -> an ENROLL_FINGERPRINT job addressed to it
//	that terminal, and only that one -> enters enrolment mode, prompts, captures
//	the terminal reports the result  -> the credential becomes Enrolled
//
// WHAT IS DELIBERATELY NOT HERE: anything biometric. The job carries a person,
// a name and a terminal serial. The result carries a locator -- which terminal,
// which slot -- and never a template. That is the same boundary the placement
// model already draws, and this workflow does not move it.

// SyncJobEnrollFingerprint is the job type the firmware reads.
//
// The spelling is FROZEN: it is matched byte for byte by
// syncJobTypeFromName() in the firmware, and deployed hardware is the half of
// this contract that cannot be redeployed on a whim.
const SyncJobEnrollFingerprint = "ENROLL_FINGERPRINT"

// Enrolment lifecycle states.
//
// SIX, AND EACH ONE SENDS AN OPERATOR SOMEWHERE DIFFERENT:
//
//   - PENDING     queued; the selected terminal has not polled yet. Waiting on
//     a network round trip, or on a terminal that is offline.
//   - IN_PROGRESS the terminal has the job and is showing the prompt. The
//     person should be at that door now.
//   - COMPLETED   a finger was captured and bound. The credential is Enrolled.
//   - FAILED      the terminal tried and could not. Its own words are in
//     ErrorMessage; retrying here or at another door is the next step.
//   - EXPIRED     the window closed with nobody at the door. Not a fault, and
//     it reads differently from FAILED for exactly that reason.
//   - CANCELLED   an operator stopped it.
//
// The last three all leave the person exactly as they were: present, active,
// and not enrolled.
const (
	EnrollmentPending    = "PENDING"
	EnrollmentInProgress = "IN_PROGRESS"
	EnrollmentCompleted  = "COMPLETED"
	EnrollmentFailed     = "FAILED"
	EnrollmentExpired    = "EXPIRED"
	EnrollmentCancelled  = "CANCELLED"
)

// EnrollmentIsLive reports whether an enrolment is still expected to happen.
//
// The predicate the "one live enrolment per person" rule is written against, in
// Go as well as in the partial unique index, so the two cannot drift into
// disagreeing about what "live" means.
func EnrollmentIsLive(status string) bool {
	return status == EnrollmentPending || status == EnrollmentInProgress
}

// Enrolment window bounds.
//
// THE WINDOW IS NOT ONLY AN APPOINTMENT. A terminal waiting to bind a finger is
// not checking fingers against the roster -- there is one sensor -- so an open
// window is a door out of service for its duration. Five minutes is long enough
// to walk across a building and short enough that a job nobody actioned reports
// back rather than leaving a reader armed.
//
// The upper bound matches the firmware's own clamp
// (kMaxEnrollmentWindowSeconds). Sending more would be asking for something the
// terminal will silently reduce, and the console would then display a deadline
// the hardware never held.
const (
	DefaultEnrollmentWindowSeconds = 300
	MinEnrollmentWindowSeconds     = 30
	MaxEnrollmentWindowSeconds     = 3600
)

// EnrollmentJobPayload is the body of an ENROLL_FINGERPRINT job.
//
// EVERY FIELD NAME IS PART OF A SHIPPED CONTRACT. parseEnrollmentPayload() in
// the firmware reads `member_id`, `serial_number`, `full_name` and
// `expires_in_seconds`, refuses the job outright if the first two are missing or
// too long, and ignores anything else. Renaming one here breaks doors.
type EnrollmentJobPayload struct {
	MemberID string `json:"member_id"`
	FullName string `json:"full_name,omitempty"`

	// SerialNumber IS THE ADDRESSING. The job is already queued against one
	// device_id, and the terminal checks this field against its own serial
	// before it will act -- two independent statements of the same fact, so a
	// mis-routed job cannot make a door capture somebody else's finger.
	SerialNumber string `json:"serial_number"`

	ExpiresInSeconds int `json:"expires_in_seconds,omitempty"`
}

// ConsoleEnrollmentRequest starts an enrolment. The terminal is named by the
// route, not by this body -- see the note on the route in router.go.
type ConsoleEnrollmentRequest struct {
	ExternalID string `json:"external_id" binding:"required"`

	// ExpiresInSeconds is optional. Absent takes
	// DefaultEnrollmentWindowSeconds; out of range is clamped rather than
	// refused, because an operator asking for ten hours has made a judgement
	// about their own site and the honest answer is the longest window the
	// hardware will actually hold.
	ExpiresInSeconds int `json:"expires_in_seconds,omitempty"`
}

// ConsoleEnrollment is one enrolment as the console sees it.
//
// It names the TERMINAL in full -- serial, name and site -- rather than by id.
// "Waiting for terminal" is not a useful thing to read; "waiting for Front Door
// (AT-000123) at Victoria Island" is, and it is what an operator needs in order
// to decide whether to keep waiting or send the person somewhere else.
type ConsoleEnrollment struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	ExternalID string `json:"external_id"`
	FullName   string `json:"full_name,omitempty"`

	TerminalSerial string `json:"terminal_serial"`
	TerminalName   string `json:"terminal_name,omitempty"`
	SiteName       string `json:"site_name,omitempty"`
	SitePublicID   string `json:"site_public_id,omitempty"`

	// TerminalStatus is the terminal's reported state at the moment this was
	// read, carried so a screen showing "waiting" can say whether it is waiting
	// on a door that is currently offline. Not stored -- read live with the row.
	TerminalStatus string `json:"terminal_status,omitempty"`

	// ErrorMessage is the TERMINAL'S own words about what went wrong, not a
	// re-statement by the platform. "Sensor error" and "the window closed" send
	// somebody to two different places.
	ErrorMessage string `json:"error_message,omitempty"`

	RequestedByEmail string `json:"requested_by_email,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// BiometricEnrolled is the person's credential state as it stands now.
	//
	// ON THE ENROLMENT RATHER THAN ONLY ON THE PERSON, because this is the
	// object a screen polls while it waits and the credential flipping is the
	// thing it is waiting for. A client that had to poll two endpoints and
	// reconcile them could render "Enrolled successfully" beside "Not enrolled"
	// for one refresh interval.
	BiometricEnrolled bool `json:"biometric_enrolled"`
}

// ConsoleEnrollmentResponse wraps the current enrolment for a person.
//
// Enrollment is a POINTER and is null when the person has never had one. That
// is a different fact from "not enrolled" and the console renders it
// differently: one offers the action, the other reports an outcome.
type ConsoleEnrollmentResponse struct {
	ExternalID        string             `json:"external_id"`
	BiometricEnrolled bool               `json:"biometric_enrolled"`
	Enrollment        *ConsoleEnrollment `json:"enrollment"`
}
