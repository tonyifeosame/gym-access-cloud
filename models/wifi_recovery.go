package models

import "time"

// The console's Change Wi-Fi command (migrations/024_wifi_recovery_command.sql).
//
// WHAT THE OPERATOR IS ACTUALLY ASKING FOR. Not "put this terminal on network
// X" -- the platform never learns the customer's Wi-Fi password and has nowhere
// to put one. The request is "hand this terminal back to its setup portal", and
// somebody standing next to it then connects a phone and types the new password
// into the terminal itself. Every shape in this file is deliberately incapable
// of carrying a credential: there is no SSID field, no passphrase field, and the
// request body is empty.
//
// WHY THE STATES ARE NAMED THE WAY THEY ARE. The console must not tell anybody
// that a terminal has changed network until the terminal has said so. A queued
// command and an applied one look identical from the database's point of view
// unless the difference is modelled, and the difference is the whole product
// promise here -- so QUEUED, DELIVERED and ACCEPTED are three states and not one
// boolean.

// WifiRecoveryJobType is the job type firmware 15caf88 recognises.
//
// The exact token matters: syncJobTypeFromName in the firmware's sync_job.cpp
// compares it with strcmp, and anything else parses as kUnknown -- which is
// acknowledged and ignored rather than acted on.
const WifiRecoveryJobType = "WIFI_RECOVERY"

// WifiRecoveryValiditySeconds is how long a queued command stays deliverable.
//
// A COMMAND IS NOT STATE, AND STALE ONES ARE DANGEROUS. Everything else in the
// outbox describes what a terminal should hold, so delivering it late is merely
// late. This describes something to DO, and doing it late is destructive: a
// command that sat in the queue while the customer recovered the terminal by
// hand would arrive after they had re-provisioned it and wipe the Wi-Fi they
// had just typed in -- and, because a wipe puts the terminal back offline, it
// could do it again. Fifteen minutes is comfortably longer than a poll cycle
// and far shorter than a support call.
const WifiRecoveryValiditySeconds = 15 * 60

// Command states, as the console shows them.
const (
	// WifiRecoveryNone means this terminal has never been sent the command.
	WifiRecoveryNone = "NONE"

	// WifiRecoveryQueued is enqueued and not yet collected. "Waiting for
	// terminal…" on screen.
	WifiRecoveryQueued = "QUEUED"

	// WifiRecoveryDelivered is collected by the terminal and not yet
	// acknowledged. It is in the terminal's hands and is not yet done.
	WifiRecoveryDelivered = "DELIVERED"

	// WifiRecoveryAccepted is ACKNOWLEDGED BY THE DEVICE, which is the strongest
	// evidence the platform ever has. The firmware produces the acknowledgement
	// before it drops the link, precisely so this state is reachable.
	//
	// IT IS NOT PROOF THE TERMINAL ACTED. An acknowledgement is produced by every
	// build, including one that did not understand the command: firmware
	// predating the feature parses an unrecognised job type as kUnknown and
	// acknowledges it as applied, deliberately, so that a newer server's job
	// types are not redelivered for ever.
	//
	// WHAT CHANGED (025): the platform no longer QUEUES this command for a
	// terminal that has not reported the wifi_recovery capability, so a false
	// ACCEPTED from an unaware build is no longer reachable through the console.
	// The evidence is now positive rather than inferred -- see
	// WifiRecoveryTerminalIncapable.
	//
	// IT IS STILL NOT "THE DOOR HAS CHANGED NETWORK", and it never will be: the
	// terminal acknowledges before it drops the link, precisely so this state is
	// reachable at all, and what happens after that is beyond anything the
	// platform can observe. A unit downgraded between its last heartbeat and
	// this command is also still possible, if vanishingly. Any surface rendering
	// this state must therefore say the terminal ACKNOWLEDGED the command, never
	// that it has changed network, and must offer the local recovery for the
	// case where nothing appears.
	WifiRecoveryAccepted = "ACCEPTED"

	// WifiRecoveryFailed is a command the terminal reported it could not apply,
	// and which has spent its retries.
	WifiRecoveryFailed = "FAILED"

	// WifiRecoveryExpired is a command that was never collected inside its
	// validity window. It will not be delivered -- see the note on
	// WifiRecoveryValiditySeconds for why that is a safety property.
	WifiRecoveryExpired = "EXPIRED"

	// WifiRecoveryCancelled is a command superseded by something that retires a
	// terminal's whole queue -- a revocation, or a retirement.
	WifiRecoveryCancelled = "CANCELLED"
)

// Refusal codes for a terminal that cannot be sent a command.
//
// Machine-readable and stable, unlike the message beside them: the console
// branches on these to choose which recovery it explains, and a console that
// matched on prose would break the first time the prose improved.
const (
	// WifiRecoveryTerminalOffline is the one that matters, because it is the
	// common case AND the self-referential one: a terminal whose Wi-Fi is
	// already broken is offline, which is exactly why somebody is reaching for
	// this button. The answer is the terminal's LOCAL recovery -- hold BOOT for
	// five seconds -- and the console says so.
	WifiRecoveryTerminalOffline = "TERMINAL_OFFLINE"

	// WifiRecoveryTerminalDisabled is an administratively disabled terminal.
	// Device authentication refuses it, so it will never collect the command.
	WifiRecoveryTerminalDisabled = "TERMINAL_DISABLED"

	// WifiRecoveryTerminalNoCredential is a terminal that holds no device key --
	// never provisioned, or revoked. It cannot poll at all.
	WifiRecoveryTerminalNoCredential = "TERMINAL_NOT_PROVISIONED"

	// WifiRecoveryTerminalIncapable is a terminal that has not told the platform
	// it can carry this command out (025).
	//
	// THE REFUSAL THAT REPLACED A LIE. Firmware predating the feature parses an
	// unrecognised job type as kUnknown and acknowledges it as applied --
	// deliberately, so a newer server's job types are not redelivered for ever --
	// so queueing this for an old unit produced an ACCEPTED state, a console
	// saying the terminal had confirmed the request, and a customer standing at
	// a door waiting for a setup network that was never going to appear.
	//
	// COVERS BOTH "REPORTS AND CANNOT" AND "HAS NEVER REPORTED", and refuses in
	// both cases. The second is the whole fleet in the field today, and treating
	// silence as consent is exactly what produced the false ACCEPTED. What the
	// console says has to distinguish them -- "cannot" and "we do not know" send
	// somebody to different places -- but neither may queue a command.
	WifiRecoveryTerminalIncapable = "TERMINAL_CANNOT_CHANGE_WIFI"
)

// ConsoleWifiRecovery is what the console reads and what the request returns.
//
// The same shape from both routes on purpose: the POST's answer is the first
// reading of a status the GET then polls, and two shapes would let the "just
// requested" screen and the "still waiting" screen disagree about what they are
// showing.
type ConsoleWifiRecovery struct {
	SerialNumber string `json:"serial_number"`

	// State is one of the WifiRecovery* constants above.
	State string `json:"state"`

	// RequestID is the sync job's public id, so an operator reporting a problem
	// and a developer reading sync_jobs are talking about the same row. Empty
	// when no command has ever been sent.
	RequestID string `json:"request_id,omitempty"`

	// AlreadyQueued reports that this request found a command already waiting
	// and did NOT queue a second one. Present so the console can say "already
	// waiting" rather than silently implying it queued something new.
	AlreadyQueued bool `json:"already_queued,omitempty"`

	// TerminalStatus is the device state machine's own value -- ONLINE,
	// OFFLINE, DISABLED and so on -- and Online is the single question the
	// console asks of it, resolved here so the two cannot disagree.
	TerminalStatus string `json:"terminal_status"`
	Online         bool   `json:"online"`

	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`

	// AcknowledgedAt is when the TERMINAL said it had the command. Nothing else
	// in this struct is evidence that anything happened at the door.
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`

	// ExpiresAt is when a still-uncollected command stops being deliverable.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

// WifiRecoveryLocalInstruction is what a customer does when the console cannot
// reach the terminal, and it is a constant for the same reason
// TerminalRecoveryInstruction is: there must be ONE answer rather than one per
// surface that needs to say it.
//
// It describes the firmware's local path from 15caf88 and must not drift from
// it. NOT VERIFIED ON HARDWARE by that commit -- no physical button was
// available -- which is why the sentence names what to do rather than promising
// what will happen.
const WifiRecoveryLocalInstruction = "Hold the button on the terminal for five " +
	"seconds. It returns to Wi-Fi setup mode, and a phone or computer nearby can " +
	"then connect it to the new network."
