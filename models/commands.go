package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The remote command plane (migrations/028_command_plane.sql).
//
// ---------------------------------------------------------------------------
// WHAT THIS FILE IS FOR
// ---------------------------------------------------------------------------
//
// One place where a command type declares everything about itself: which
// capability a terminal must have reported before it may be sent one, how long
// it stays deliverable, who may issue it, whether it is safe to repeat, and
// what its parameters may contain.
//
// THE REGISTRY IS THE POINT. Before this, each command type carried its
// properties in whichever file happened to implement it, so a new one inherited
// nothing: WIFI_RECOVERY (024) has a capability gate, a validity window and a
// one-outstanding index; ENROLL_FINGERPRINT (027), added three migrations later
// by the same reasoning in the same file, has none of the three. A registry is
// what makes the seventh command type get them without anybody remembering.
//
// A type that is not in CommandSpecs cannot be issued at all -- IssueCommand
// resolves the spec before it does anything else, so "we forgot to think about
// this one" fails at the request rather than at a door.
//
// ---------------------------------------------------------------------------
// STATE VERSUS COMMAND
// ---------------------------------------------------------------------------
//
// A STATE job is declarative and re-applying it is a no-op by construction. A
// COMMAND job is an event: applying it twice does it twice, and delivering it
// late is not merely late but wrong. Only COMMAND-class types are in this file;
// the STATE types keep the paths they have always used.

// Command classes, as stored in sync_jobs.command_class.
const (
	CommandClassState   = "STATE"
	CommandClassCommand = "COMMAND"
)

// CommandEnvelopeVersion is the version of the `command` object carried inside
// a job, which is NOT SyncProtocolVersion.
//
// Two numbers on purpose. SyncProtocolVersion describes the transport -- the
// shape of the jobs response and of an acknowledgement -- and deliberately does
// not move for an additive job type, which is the property that lets a newer
// server serve an older fleet. This describes the shape of one job's command
// block, so a command's parameters can change without renegotiating the
// protocol with every terminal in the field.
const CommandEnvelopeVersion = 1

// The command types this platform can issue.
//
// Spelled exactly as the firmware's syncJobTypeFromName compares them, with
// strcmp. Anything else parses as kUnknown, which is acknowledged and
// discarded -- safe, and indistinguishable from success, which is why the
// capability gate exists.
const (
	// CommandDiagnosticSnapshot asks a terminal to report what it knows about
	// itself: network, outbound queue, credential worklist, storage. Every part
	// of it is a read.
	CommandDiagnosticSnapshot = "DIAGNOSTIC_SNAPSHOT"

	// CommandDeviceTest exercises one piece of hardware and reports what
	// happened. The target is a parameter rather than three job types because
	// they share a result shape and a risk tier.
	//
	// IT CANNOT TARGET THE RELAY. See DeviceTestTargets.
	CommandDeviceTest = "DEVICE_TEST"
)

// Command types inherited from before the plane existed. Registered so the
// status and listing paths can describe them, and so the classification in the
// schema has a Go counterpart -- NOT so they can be issued through it. Both
// keep their own issuing paths and their own behaviour, unchanged.
const (
	CommandWifiRecovery      = WifiRecoveryJobType
	CommandEnrollFingerprint = SyncJobEnrollFingerprint
)

// SyncJobReserved names the job types that exist in the schema's CHECK
// constraint and nowhere else.
//
// NO GO CODE ENQUEUES THEM AND NO FIRMWARE PARSES THEM. syncJobTypeFromName has
// no case for any of them, so one would be read as kUnknown, acknowledged, and
// thrown away. They are Sprint-2 vocabulary that outlived its design.
//
// Kept in the constraint because dropping a value from a CHECK is irreversible
// against rows that already hold it. Named here so that the next person to
// reach for FIRMWARE_UPDATE or LOG_PULL finds out from a constant rather than
// from a fleet that silently ignored them.
var SyncJobReserved = []string{
	"INCREMENTAL_SYNC", "PERMISSION_PUSH", "TEMPLATE_PUSH",
	"FIRMWARE_UPDATE", "LOG_PULL",
}

// SyncJobStateTypes are the declarative job types. Canonical list, asserted
// against the live CHECK constraint by command_plane_test.go.
var SyncJobStateTypes = []string{
	"CREATE", "UPDATE", "DELETE", "SETTINGS", "FULL_SYNC",
}

// ---------------------------------------------------------------------------
// Capability tokens
// ---------------------------------------------------------------------------

// CommandCapability derives the 025 capability token a command type needs.
//
// MECHANICAL RATHER THAN A TABLE, following the spelling WIFI_RECOVERY ->
// `wifi_recovery` already established. A `cmd_` prefix marks the tokens that
// gate a command, so a reader of a terminal's capability list can tell what is
// a feature the unit has from what is a command it will accept.
//
// The firmware derives the same string from the same rule (device_info.h), and
// a fixture asserts the two agree -- which is the only thing standing between
// this and a gate that silently never matches.
func CommandCapability(jobType string) string {
	return "cmd_" + strings.ToLower(jobType)
}

// The tokens, spelled out. Derived values are convenient and constants are
// greppable; both exist so a search for the literal finds the firmware's copy.
const (
	CapabilityCmdDiagnosticSnapshot = "cmd_diagnostic_snapshot"
	CapabilityCmdDeviceTest         = "cmd_device_test"
)

// ---------------------------------------------------------------------------
// The registry
// ---------------------------------------------------------------------------

// CommandSpec is everything a command type declares about itself.
type CommandSpec struct {
	Type string

	// Capability is the 025 token a terminal must have reported. Never empty:
	// the schema refuses a COMMAND row without one, so a spec that left it
	// blank would fail at the insert rather than being quietly ungated.
	Capability string

	// Validity is how long the command stays deliverable once queued.
	//
	// ZERO MEANS "DOES NOT LAPSE", and it is legal for exactly the two
	// inherited types. Every type added since must declare one -- asserted by
	// TestEveryNewCommandDeclaresValidity -- because a command that describes
	// an ACT and never lapses is the failure 024 was written about: it arrives
	// after the situation it was issued for has been resolved by hand.
	Validity time.Duration

	// MinRole is the lowest operator role that may issue it.
	MinRole string

	// Repeatable says whether issuing the same command again while one is
	// outstanding is meaningful.
	//
	// FALSE is enforced by the partial unique index, not by this field -- Go
	// cannot enforce it against a browser that retried a request whose response
	// it never saw. The field is what the console reads to explain the 409.
	Repeatable bool

	// Issuable says whether this type may be created through the command plane.
	//
	// FALSE for the two inherited types. They are registered so the status and
	// listing paths can describe rows that already exist, and refused at the
	// issue path so that a shipped feature keeps exactly one entry point. Two
	// ways to queue a Change Wi-Fi is two places for its rules to diverge.
	Issuable bool

	// ReadOnly says the command changes nothing at the terminal.
	//
	// Not a permission -- MinRole is that -- but it is what lets the console
	// present a control without a confirmation, and it is what makes a type
	// safe to include in the first phase of a rollout.
	ReadOnly bool

	// ValidateParams checks the caller's parameters and returns the object that
	// will be stored and sent. Nil means the command takes none, and a caller
	// that sends some anyway is refused rather than having them dropped: a
	// parameter the platform ignores is one an operator believes took effect.
	ValidateParams func(raw json.RawMessage) (json.RawMessage, error)
}

// CommandSpecs is the registry. Keyed by job type.
var CommandSpecs = map[string]CommandSpec{
	CommandDiagnosticSnapshot: {
		Type:       CommandDiagnosticSnapshot,
		Capability: CapabilityCmdDiagnosticSnapshot,

		// TEN MINUTES. Long enough to survive several poll intervals at the
		// default sixty seconds and a terminal that is briefly unreachable;
		// short enough that a snapshot collected long after an operator stopped
		// looking is not presented as current. A diagnostic's value decays.
		Validity:   10 * time.Minute,
		MinRole:    RoleManager,
		Repeatable: false,
		Issuable:   true,
		ReadOnly:   true,

		ValidateParams: validateDiagnosticParams,
	},

	CommandDeviceTest: {
		Type:       CommandDeviceTest,
		Capability: CapabilityCmdDeviceTest,

		// FIVE MINUTES, and shorter than a snapshot deliberately. This one
		// makes a noise or lights a panel in a room with people in it. A tone
		// sounding twenty minutes after somebody pressed a button is at best
		// confusing and at worst sends a technician looking for a fault.
		Validity:   5 * time.Minute,
		MinRole:    RoleManager,
		Repeatable: false,
		Issuable:   true,

		// NOT ReadOnly, and the distinction is real even though nothing is
		// stored: it has a physical effect somebody in the room perceives.
		ReadOnly: false,

		ValidateParams: validateDeviceTestParams,
	},

	CommandWifiRecovery: {
		Type:       CommandWifiRecovery,
		Capability: CapabilityWifiRecovery,
		Validity:   time.Duration(WifiRecoveryValiditySeconds) * time.Second,
		MinRole:    RoleAdmin,
		Repeatable: false,

		// Issued by database/wifi_recovery.go, which owns its rules. Registered
		// here for description only.
		Issuable: false,
		ReadOnly: false,
	},

	CommandEnrollFingerprint: {
		Type:       CommandEnrollFingerprint,
		Capability: "enroll_fingerprint",

		// ZERO -- IT HAS NEVER LAPSED, and this records that rather than
		// changing it. An enrolment job with no window sits in the queue until
		// it is collected or the terminal's backlog is compacted. Giving it one
		// is a behavioural change to a shipped feature with its own tests, and
		// smuggling it in through a registry entry is how a working feature
		// breaks. Phase 0 of the plan closes it deliberately.
		Validity:   0,
		MinRole:    RoleManager,
		Repeatable: true,
		Issuable:   false,
		ReadOnly:   false,
	},
}

// CommandSpecFor resolves a type, reporting whether it is a known command.
func CommandSpecFor(jobType string) (CommandSpec, bool) {
	spec, ok := CommandSpecs[jobType]
	return spec, ok
}

// IssuableCommandTypes lists what the command plane will accept, sorted so the
// order is stable for clients and for tests.
func IssuableCommandTypes() []string {
	out := make([]string, 0, len(CommandSpecs))
	for name, spec := range CommandSpecs {
		if spec.Issuable {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// IsCommandType reports whether a job type is COMMAND-class.
func IsCommandType(jobType string) bool {
	_, ok := CommandSpecs[jobType]
	return ok
}

// CommandClassFor answers what the schema's command_class column should hold.
func CommandClassFor(jobType string) string {
	if IsCommandType(jobType) {
		return CommandClassCommand
	}
	return CommandClassState
}

// ---------------------------------------------------------------------------
// Parameters
// ---------------------------------------------------------------------------

// DiagnosticSection names one part of a terminal's self-report.
const (
	DiagnosticNetwork  = "network"
	DiagnosticOutbound = "outbound_queue"
	DiagnosticWorklist = "worklist"
	DiagnosticStorage  = "storage"
)

// DiagnosticSections is the closed set. Closed rather than free-form because
// the terminal branches on it: an unrecognised section on a constrained device
// is either a silent omission or a parse failure, and neither is a thing to
// discover from a door.
var DiagnosticSections = []string{
	DiagnosticNetwork, DiagnosticOutbound, DiagnosticWorklist, DiagnosticStorage,
}

// DiagnosticParams is what DIAGNOSTIC_SNAPSHOT takes.
type DiagnosticParams struct {
	// Include narrows the report. Empty means every section, which is what a
	// console that has not been taught the sections yet will send.
	Include []string `json:"include,omitempty"`
}

// strictUnmarshalParams decodes a command's parameter object and REFUSES a
// field it does not know.
//
// Go's default is to ignore unknown fields, and for a command that is the wrong
// default by a wide margin. `{"target":"display"}` on a DIAGNOSTIC_SNAPSHOT was
// accepted and normalised to the default section list, so an integrator who had
// confused two commands got 202 Accepted and a snapshot that ignored them --
// the same silent-drop failure the handler already refuses with "This command
// takes no parameters" one level up, arriving through a different door.
//
// The refusal names the field, because "params must be an object" sends someone
// to check their braces rather than their spelling.
func strictUnmarshalParams(raw json.RawMessage, into interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("params rejected: %w", err)
	}

	// DisallowUnknownFields does not stop trailing content: `{} {}` decodes the
	// first object and returns. A second value means the caller sent something
	// other than the one object this takes.
	if dec.More() {
		return fmt.Errorf("params must be a single object")
	}
	return nil
}

func validateDiagnosticParams(raw json.RawMessage) (json.RawMessage, error) {
	params := DiagnosticParams{}
	if len(raw) > 0 {
		if err := strictUnmarshalParams(raw, &params); err != nil {
			return nil, err
		}
	}

	// Absent means everything, spelled out on the way in rather than defaulted
	// on the way out. The terminal then receives an explicit list, so what it
	// reports cannot depend on how it read an empty field.
	if len(params.Include) == 0 {
		params.Include = append([]string(nil), DiagnosticSections...)
	}

	seen := map[string]bool{}
	clean := make([]string, 0, len(params.Include))
	for _, section := range params.Include {
		if !isDiagnosticSection(section) {
			return nil, fmt.Errorf("unknown diagnostic section %q", section)
		}
		if seen[section] {
			continue
		}
		seen[section] = true
		clean = append(clean, section)
	}
	params.Include = clean

	return json.Marshal(params)
}

func isDiagnosticSection(name string) bool {
	for _, known := range DiagnosticSections {
		if known == name {
			return true
		}
	}
	return false
}

// Device test targets.
//
// THE RELAY IS NOT HERE AND MUST NOT BE ADDED. Pulsing a strike is a door
// opening, which is its own command with its own tier: a site opt-in, a step-up
// re-authentication, a reason, a rate limit and a sixty-second window. Reaching
// it through a test target would put a door behind the safeguards appropriate
// to a buzzer, and the firmware's own four-part factory fence exists because
// that distinction has been got wrong before.
const (
	DeviceTestBuzzer   = "buzzer"
	DeviceTestDisplay  = "display"
	DeviceTestSelfTest = "self_test"
)

// DeviceTestTargets is the closed set of things DEVICE_TEST may exercise.
var DeviceTestTargets = []string{
	DeviceTestBuzzer, DeviceTestDisplay, DeviceTestSelfTest,
}

// DeviceTestParams is what DEVICE_TEST takes.
type DeviceTestParams struct {
	Target string `json:"target"`
}

func validateDeviceTestParams(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("target is required: one of %s",
			strings.Join(DeviceTestTargets, ", "))
	}

	params := DeviceTestParams{}
	if err := strictUnmarshalParams(raw, &params); err != nil {
		return nil, err
	}

	for _, known := range DeviceTestTargets {
		if params.Target == known {
			return json.Marshal(params)
		}
	}

	// The refusal names the relay explicitly when somebody asks for it. A
	// generic "unknown target" would send an integrator to check their
	// spelling; this sends them to the command that actually opens a door.
	if params.Target == "relay" || params.Target == "unlock" {
		return nil, fmt.Errorf("a device test cannot operate the relay: " +
			"opening a door is its own command, with its own authorization")
	}
	return nil, fmt.Errorf("unknown test target %q: one of %s",
		params.Target, strings.Join(DeviceTestTargets, ", "))
}

// ---------------------------------------------------------------------------
// Command states, as the console reads them
// ---------------------------------------------------------------------------
//
// THE SAME VOCABULARY 024 ESTABLISHED, generalised. It was right and it was
// hard-won: a queued command and an applied one look identical in the database
// unless the difference is modelled, and that difference is the whole product
// promise. Reusing the words means the Change Wi-Fi dialog and every future
// command panel say the same thing about the same row.
const (
	// CommandStateNone means no command of this type has ever been sent.
	CommandStateNone = "NONE"

	// CommandStateQueued is enqueued and not yet collected.
	CommandStateQueued = "QUEUED"

	// CommandStateDelivered is in the terminal's hands and not yet
	// acknowledged. Evidence that it was collected, and nothing more.
	CommandStateDelivered = "DELIVERED"

	// CommandStateAccepted is ACKNOWLEDGED BY THE DEVICE.
	//
	// THE STRONGEST EVIDENCE THE PLATFORM EVER HAS, AND IT IS NOT PROOF THE
	// TERMINAL ACTED. Firmware that does not recognise a job type acknowledges
	// it as applied -- deliberately, so a newer server's types are not
	// redelivered for ever. The capability gate is what makes a false ACCEPTED
	// unreachable through this API; the honesty of the word is what makes it
	// safe when the gate is wrong. Any surface rendering this must say the
	// terminal ACKNOWLEDGED the command.
	CommandStateAccepted = "ACCEPTED"

	// CommandStateFailed is a command the terminal reported it could not apply.
	CommandStateFailed = "FAILED"

	// CommandStateExpired was never collected inside its validity window. It
	// will not be delivered -- which is a safety property, not a shortfall.
	CommandStateExpired = "EXPIRED"

	// CommandStateCancelled was withdrawn, or superseded by something that
	// retires a terminal's whole queue.
	CommandStateCancelled = "CANCELLED"
)

// CommandStateIsTerminal reports whether a state can still change.
func CommandStateIsTerminal(state string) bool {
	switch state {
	case CommandStateAccepted, CommandStateFailed,
		CommandStateExpired, CommandStateCancelled:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Refusal codes
// ---------------------------------------------------------------------------
//
// Machine-readable and stable, unlike the message beside them: the console
// branches on these to choose which recovery it explains, and one that matched
// on prose would break the first time the prose improved.
//
// The four terminal-reachability codes are 024's, reused verbatim rather than
// renamed -- an existing client already branches on them, and a second spelling
// of the same fact is how two surfaces start disagreeing.
const (
	// CommandRefusedIncapable is a terminal that has not reported the
	// capability this command needs. Covers both "reports, and cannot" and
	// "has never reported", which the Detail distinguishes for the human.
	CommandRefusedIncapable = "TERMINAL_INCAPABLE"

	// CommandRefusedAlreadyPending is one outstanding of this type already.
	// Not an error condition -- the response carries the existing command.
	CommandRefusedAlreadyPending = "COMMAND_ALREADY_PENDING"

	// CommandRefusedUnknownType is a type the registry does not carry.
	CommandRefusedUnknownType = "COMMAND_UNKNOWN_TYPE"

	// CommandRefusedNotIssuable is a registered type that has its own entry
	// point -- Change Wi-Fi and enrolment both do.
	CommandRefusedNotIssuable = "COMMAND_NOT_ISSUABLE_HERE"

	// CommandRefusedBadParams is a parameter object the spec rejected.
	CommandRefusedBadParams = "COMMAND_INVALID_PARAMS"
)

// The terminal-reachability refusals, aliased from 024 rather than respelled.
//
// SAME STRINGS ON PURPOSE. The Change Wi-Fi dialog already branches on these
// values and the console already knows which recovery each one calls for; a
// second spelling of "this terminal is offline" is how two surfaces start
// disagreeing about the same fact. The aliases exist so a reader of the command
// plane is not sent to a file about Wi-Fi to find out what a refusal means.
const (
	CommandRefusedOffline      = WifiRecoveryTerminalOffline
	CommandRefusedDisabled     = WifiRecoveryTerminalDisabled
	CommandRefusedNoCredential = WifiRecoveryTerminalNoCredential
)

// ErrCommandNotFound is a command id that does not resolve for this terminal.
//
// Scoped to the terminal deliberately: a public id belonging to another door is
// not found rather than forbidden, so the answer cannot confirm that it exists
// somewhere else.
var ErrCommandNotFound = errors.New("command not found")

// ErrCommandNotWithdrawable is a command that can no longer be recalled.
//
// The terminal already has it, or it has already finished. There is no honest
// way to stop it: the whole point of the DELIVERED state is that the platform
// says what it can prove, and "cancelled" for a command a door is executing
// would be the exact lie the state machine exists to prevent.
var ErrCommandNotWithdrawable = errors.New("command cannot be withdrawn")

// ---------------------------------------------------------------------------
// Result codes
// ---------------------------------------------------------------------------
//
// What a terminal may say about what happened. Reported by the device, stored
// verbatim, and bounded -- a device-writable column with no ceiling is a growth
// vector with a credential behind it.
const (
	// CommandResultExpiredAtDevice is the terminal's own expiry check firing.
	//
	// A SECOND, INDEPENDENT CHECK, and it has to be: the server's delivery
	// filter cannot account for a job fetched a second before it lapsed and
	// applied a minute later. The device refuses it and says which check
	// refused it, so a command that expired in transit is distinguishable from
	// one that failed.
	CommandResultExpiredAtDevice = "EXPIRED_AT_DEVICE"

	// CommandResultUnsupported is a terminal that parsed the command and does
	// not implement it. Should be unreachable behind the capability gate, and
	// is recorded rather than assumed impossible.
	CommandResultUnsupported = "UNSUPPORTED"

	// CommandResultBadParams is a terminal that could not read the parameters.
	CommandResultBadParams = "INVALID_PARAMS"

	// CommandResultOK is the generic success. A command with something to say
	// sends its own code instead.
	CommandResultOK = "OK"

	// CommandResultBusy is a terminal that already had a command in its slot.
	//
	// NOT A FAILURE OF THE COMMAND, and treated as one for a while: the
	// firmware reported UNSUPPORTED here, which means "this image does not
	// implement that", and sent operators looking for a firmware mismatch that
	// did not exist. The condition is ordinary -- the one-outstanding index is
	// keyed (device_id, job_type) so one command of each type may be
	// outstanding, while the terminal has a single slot for all types.
	CommandResultBusy = "TERMINAL_BUSY"

	// CommandResultDuplicateSuppressed is the terminal reporting that a command
	// it had already run was delivered to it again and was NOT run twice.
	//
	// Delivery is at-least-once, so this is the expected report after a dropped
	// acknowledgement rather than a fault. It is a distinct code because "ran
	// once" and "ran twice" are different facts about a door.
	CommandResultDuplicateSuppressed = "DUPLICATE_SUPPRESSED"
)

// MaxCommandResultBytes bounds the encoded result a device may report.
//
// TWO KILOBYTES, and the number comes from the firmware rather than from taste:
// the network task runs on an 8 KB stack with roughly 4 KB of measured
// headroom, and the result is built there. A ceiling the device cannot exceed
// is worth more than one the server merely rejects, so both sides carry it --
// this is the half that holds when a device is not the one this firmware built.
const MaxCommandResultBytes = 2048

// MaxCommandReasonLength bounds the operator's free-text reason.
const MaxCommandReasonLength = 500

// ---------------------------------------------------------------------------
// The console's view
// ---------------------------------------------------------------------------

// ConsoleCommand is one command, as the console reads it.
//
// ONE SHAPE FROM EVERY ROUTE on purpose, exactly as ConsoleWifiRecovery is: the
// POST's answer is the first reading of a status the GET then polls, and two
// shapes would let the "just issued" screen and the "still waiting" screen
// disagree about what they are showing.
type ConsoleCommand struct {
	// ID is the sync job's public id, so an operator reporting a problem and a
	// developer reading sync_jobs are naming the same row.
	ID string `json:"id"`

	SerialNumber string `json:"serial_number"`
	Type         string `json:"type"`

	// State is one of the CommandState* constants.
	State string `json:"state"`

	Params json.RawMessage `json:"params,omitempty"`
	Reason string          `json:"reason,omitempty"`

	// RequestedByEmail is denormalised so it survives the operator account
	// being deleted, on exactly the terms AuditRecord denormalises its actor.
	RequestedByEmail string `json:"requested_by_email,omitempty"`

	// AlreadyPending reports that this request found one outstanding and did
	// NOT queue a second. Present so the console can say "already waiting"
	// rather than silently implying it queued something new.
	AlreadyPending bool `json:"already_pending,omitempty"`

	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`

	// AcknowledgedAt is when the TERMINAL said it had the command. Nothing else
	// in this struct is evidence that anything happened at the door.
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`

	// ResultCode and Result are what the terminal reported.
	ResultCode string          `json:"result_code,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`

	// Error is the terminal's own words about a failure, which is the useful
	// half of one -- "sensor error" and "the window closed" send somebody to
	// two different places.
	Error string `json:"error,omitempty"`

	Attempts int `json:"attempts"`

	// TerminalStatus and Online are the device state machine's own values,
	// resolved in one place so the command view and the fleet page cannot
	// disagree about the same terminal.
	TerminalStatus  string     `json:"terminal_status,omitempty"`
	Online          bool       `json:"online"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

// ConsoleCommandList is a terminal's command history.
type ConsoleCommandList struct {
	SerialNumber string           `json:"serial_number"`
	Count        int              `json:"count"`
	Commands     []ConsoleCommand `json:"commands"`
}

// ConsoleTerminalCapabilities is what a terminal says it can do, and what the
// platform will therefore let an operator ask of it.
//
// TWO LISTS RATHER THAN ONE. `capabilities` is the terminal's raw report and
// may contain tokens this server has never heard of -- 025 stores what it is
// given. `commands` is this platform's answer to "which controls should the app
// draw", which is the intersection of what the terminal claims with what the
// registry can issue. A console rendering the first would offer buttons the
// server will refuse.
type ConsoleTerminalCapabilities struct {
	SerialNumber string `json:"serial_number"`

	// Reported is nil when the terminal has never told us, and non-nil-empty
	// when it reported and has none. The distinction is load-bearing and
	// survives encoding/json exactly: an absent key decodes to nil, `[]` to a
	// non-nil empty slice.
	Reported []string `json:"capabilities"`

	// ReportedAt is when the list last CHANGED, not when it was last received.
	ReportedAt *time.Time `json:"capabilities_reported_at,omitempty"`

	// Commands is what this terminal will accept, each with the properties the
	// console needs to render and explain it.
	Commands []ConsoleCommandOffer `json:"commands"`

	FirmwareVersion string     `json:"firmware_version,omitempty"`
	TerminalStatus  string     `json:"terminal_status,omitempty"`
	Online          bool       `json:"online"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
}

// ConsoleCommandOffer describes one command a terminal will accept.
type ConsoleCommandOffer struct {
	Type       string `json:"type"`
	Capability string `json:"capability"`

	// Supported is whether the terminal reported the capability. Present as a
	// field rather than by omission so the console can grey a control and say
	// why, instead of silently not drawing it -- "this terminal's firmware does
	// not support that" is a better answer than a missing button.
	Supported bool `json:"supported"`

	MinRole    string `json:"min_role"`
	ReadOnly   bool   `json:"read_only"`
	Repeatable bool   `json:"repeatable"`

	// ValiditySeconds is 0 when the command does not lapse.
	ValiditySeconds int `json:"validity_seconds"`
}

// CommandIssueRequest is the body of POST /console/terminals/:serial/commands.
type CommandIssueRequest struct {
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params,omitempty"`
	Reason string          `json:"reason,omitempty"`

	// IdempotencyKey is the caller's retry token. Optional, and a UUID when
	// present -- validated at the handler so a malformed one is a 400 rather
	// than a constraint violation the caller cannot tell from an outage.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
