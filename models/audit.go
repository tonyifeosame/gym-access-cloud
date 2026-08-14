package models

import (
	"encoding/json"
	"time"
)

// The operator audit trail as the console sees it.

// AuditRecord is one operator action.
//
// The actor is an EMAIL AND A ROLE, denormalised, not a reference. An operator
// account can be deleted, and when it is, the record of what they did must
// survive with enough identity to be readable -- a foreign key alone would leave
// a null and lose the answer to the question somebody is asking.
type AuditRecord struct {
	ID     string `json:"id"`
	Action string `json:"action"`

	ActorEmail string `json:"actor_email,omitempty"`
	ActorRole  string `json:"actor_role,omitempty"`

	TargetType  string `json:"target_type,omitempty"`
	TargetID    string `json:"target_id,omitempty"`
	TargetLabel string `json:"target_label,omitempty"`

	IPAddress string `json:"ip_address,omitempty"`

	// Changes is redacted before it is stored. Nothing secret reaches this
	// column: password hashes never leave the user store, site and device keys
	// exist only in the local variable that generated them, and sealed
	// biometric material is never loaded by a console handler at all.
	Changes json.RawMessage `json:"changes,omitempty"`

	OccurredAt time.Time `json:"occurred_at"`
}

// ConsoleAuditPage is one page of the audit trail.
type ConsoleAuditPage struct {
	Count   int           `json:"count"`
	Total   int           `json:"total"`
	Limit   int           `json:"limit"`
	Offset  int           `json:"offset"`
	HasMore bool          `json:"has_more"`
	Entries []AuditRecord `json:"entries"`
}

// ---------------------------------------------------------------------------
// Field events
// ---------------------------------------------------------------------------

// Platform event types. An application may define its own -- the column is
// deliberately unconstrained -- but these are the ones the platform itself
// produces and the console knows how to label.
const (
	EventAccessGranted   = "ACCESS_GRANTED"
	EventAccessDenied    = "ACCESS_DENIED"
	EventPresence        = "PRESENCE"
	EventEnrolled        = "CREDENTIAL_ENROLLED"
	EventEnrolFailed     = "CREDENTIAL_ENROLMENT_FAILED"
	EventTamper          = "TAMPER"
	EventTerminalOnline  = "TERMINAL_ONLINE"
	EventTerminalOffline = "TERMINAL_OFFLINE"
)

// Event decisions. A CLOSED set, unlike the type, because the authorization path
// branches on it.
const (
	DecisionGranted  = "GRANTED"
	DecisionDenied   = "DENIED"
	DecisionRecorded = "RECORDED"
	DecisionError    = "ERROR"
)

// EventDecisions is the closed set, validated in Go so a bad value is a 400
// rather than a constraint violation the handler cannot tell from an outage.
var EventDecisions = map[string]bool{
	DecisionGranted:  true,
	DecisionDenied:   true,
	DecisionRecorded: true,
	DecisionError:    true,
}

// IsEventDecision reports whether a value is one of the platform's four.
func IsEventDecision(code string) bool { return EventDecisions[code] }

// Event is one thing that happened in the field.
type Event struct {
	ID string `json:"id"`

	Type        string `json:"event_type"`
	Application string `json:"application,omitempty"`
	Decision    string `json:"decision"`

	// Reason is present on EVERY outcome including grants. An audit trail that
	// records why somebody was refused but not why they were admitted answers
	// half the question a security review asks.
	Reason string `json:"reason,omitempty"`

	Direction string `json:"direction,omitempty"`

	SiteName     string `json:"site_name,omitempty"`
	DeviceSerial string `json:"device_serial,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`

	// PersonID is empty when the presentation matched nobody, which is a real
	// outcome and the more interesting half of an audit trail. SubjectExternalID
	// keeps what the terminal actually read, so the attempt is still traceable.
	PersonID          string `json:"person_id,omitempty"`
	PersonName        string `json:"person_name,omitempty"`
	SubjectExternalID string `json:"subject_external_id,omitempty"`
	CredentialID      string `json:"credential_id,omitempty"`
	CredentialType    string `json:"credential_type,omitempty"`

	Payload json.RawMessage `json:"payload,omitempty"`

	// TWO TIMES, and the difference matters. OccurredAt is when it happened at
	// the terminal; RecordedAt is when the server heard. A queued event uploaded
	// hours later has an OccurredAt hours before its RecordedAt.
	OccurredAt time.Time `json:"occurred_at"`
	RecordedAt time.Time `json:"recorded_at"`

	// OccurredAtTrusted says whether the terminal's clock was believable when it
	// stamped OccurredAt. A terminal that has never reached NTP sends nothing
	// and the server stamps its own arrival time; saying so is more useful than
	// presenting a server time as a door time.
	OccurredAtTrusted bool `json:"occurred_at_trusted"`
}

// ConsoleEventPage is one page of the event trail.
type ConsoleEventPage struct {
	Count   int     `json:"count"`
	Total   int     `json:"total"`
	Limit   int     `json:"limit"`
	Offset  int     `json:"offset"`
	HasMore bool    `json:"has_more"`
	Events  []Event `json:"events"`
}
