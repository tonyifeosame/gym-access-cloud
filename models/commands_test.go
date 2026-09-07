package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// The command registry's own rules (028).
//
// EVERY TEST HERE RUNS WITHOUT A DATABASE, deliberately. These are the
// properties that decide whether a command may exist at all -- does it declare
// a capability, does it declare a window, can its parameters name a door -- and
// they are worth asserting on a machine with no PostgreSQL, because they are
// the ones a reviewer most wants to see checked before a command type lands.
//
// The integration half is command_plane_test.go in the root package.

// TestEveryCommandDeclaresACapability.
//
// THE GATE IS THE WHOLE REASON A COMMAND PLANE IS SAFE. Firmware that does not
// recognise a job type acknowledges it as applied -- deliberately, so a newer
// server's types are not redelivered for ever -- which makes an old unit's
// acknowledgement indistinguishable from a new one's. Without a capability
// token there is nothing to gate on, and the console would report ACCEPTED for
// a command that was parsed as kUnknown and thrown away.
//
// The schema enforces this too (sync_jobs_command_complete_check), but a
// constraint violation surfaces as a 500 at the moment somebody presses a
// button. This surfaces at `go test`.
func TestEveryCommandDeclaresACapability(t *testing.T) {
	for name, spec := range CommandSpecs {
		if spec.Capability == "" {
			t.Errorf("command %s declares no capability: the platform would "+
				"queue it for firmware that cannot carry it out", name)
		}
		if spec.Type != name {
			t.Errorf("command %s is registered under the wrong key (Type=%q)",
				name, spec.Type)
		}
		if spec.MinRole == "" {
			t.Errorf("command %s declares no minimum role", name)
		}
		if !OperatorRoles[spec.MinRole] {
			t.Errorf("command %s declares MinRole=%q, which is not a role",
				name, spec.MinRole)
		}
	}
}

// TestEveryNewCommandDeclaresAValidity.
//
// A command that describes an ACT and never lapses is the failure 024 was
// written about: it arrives after the situation it was issued for has been
// resolved by hand, and performs it again. Zero validity is legal for exactly
// the two commands inherited from 024 and 027, and this test is what stops that
// exemption from spreading to a third.
//
// THE ALLOW-LIST IS THE POINT. Adding a name to it is a deliberate act with a
// reviewer attached; forgetting to set a Validity is not.
func TestEveryNewCommandDeclaresAValidity(t *testing.T) {
	// The two that predate the plane. WIFI_RECOVERY computes its window from
	// created_at in Go rather than from expires_at, and ENROLL_FINGERPRINT has
	// never had one at all -- recorded here rather than changed, because
	// changing it is a behavioural change to a shipped feature.
	mayNotLapse := map[string]bool{
		CommandEnrollFingerprint: true,
	}

	for name, spec := range CommandSpecs {
		if spec.Validity > 0 || mayNotLapse[name] {
			continue
		}
		// WIFI_RECOVERY does declare one, from its own constant, so it should
		// not need the exemption. Assert that rather than exempting it.
		t.Errorf("command %s declares no validity window. A command that does "+
			"not lapse is delivered after the situation it was issued for has "+
			"been resolved by hand -- see migrations/024.", name)
	}
}

// TestOnlyIssuableCommandsAreOfferedToClients.
//
// The two inherited types are registered so the status and listing paths can
// DESCRIBE their rows, and refused at the issue path so each keeps exactly one
// entry point. Two ways to queue a Change Wi-Fi is two places for its rules to
// diverge, and 027 is the standing evidence that they do.
func TestOnlyIssuableCommandsAreOfferedToClients(t *testing.T) {
	offered := IssuableCommandTypes()

	for _, name := range offered {
		if !CommandSpecs[name].Issuable {
			t.Errorf("%s is offered to clients but is not issuable", name)
		}
	}

	for _, inherited := range []string{CommandWifiRecovery, CommandEnrollFingerprint} {
		if CommandSpecs[inherited].Issuable {
			t.Errorf("%s must keep its own endpoint: issuing it through the "+
				"command plane would give one command two sets of rules", inherited)
		}
		for _, name := range offered {
			if name == inherited {
				t.Errorf("%s is offered through the command plane", inherited)
			}
		}
	}
}

// TestCapabilityTokensMatchTheDerivationRule.
//
// THREE INDEPENDENT IMPLEMENTATIONS OF ONE RULE, which is two more than this
// codebase would normally tolerate: CommandCapability here, the CASE expression
// in 028's trigger, and the constants in the firmware's device_info.h. This
// test pins the Go half against the constants; the firmware half is pinned by
// its own suite; the SQL half is pinned by command_plane_test.go.
//
// A gate that silently never matches would refuse every command with a message
// about the firmware being out of date, which is the least debuggable failure
// this feature has available.
func TestCapabilityTokensMatchTheDerivationRule(t *testing.T) {
	cases := map[string]string{
		CommandDiagnosticSnapshot: CapabilityCmdDiagnosticSnapshot,
		CommandDeviceTest:         CapabilityCmdDeviceTest,
	}

	for jobType, want := range cases {
		if got := CommandCapability(jobType); got != want {
			t.Errorf("CommandCapability(%s) = %q, want %q", jobType, got, want)
		}
		if got := CommandSpecs[jobType].Capability; got != want {
			t.Errorf("%s registers capability %q, want %q", jobType, got, want)
		}
	}

	// The inherited pair keep their UNPREFIXED spelling, because that is the
	// token already deployed and matched by the shipped gate in
	// database/wifi_recovery.go. Renaming either would refuse the command on
	// every terminal in the field.
	if CommandSpecs[CommandWifiRecovery].Capability != CapabilityWifiRecovery {
		t.Errorf("WIFI_RECOVERY's capability was renamed; every terminal in "+
			"the field reports %q", CapabilityWifiRecovery)
	}
	if strings.HasPrefix(CommandSpecs[CommandWifiRecovery].Capability, "cmd_") {
		t.Error("WIFI_RECOVERY must keep the token its firmware advertises")
	}
}

// ---------------------------------------------------------------------------
// Parameter validation
// ---------------------------------------------------------------------------

// TestADeviceTestCannotOperateTheRelay.
//
// THE ASSERTION THIS PHASE IS FENCED BY, on the platform side. Pulsing a strike
// is a door opening: it belongs to its own command, with a site opt-in, a
// step-up re-authentication, a reason, a rate limit and a sixty-second window.
// Reaching it through a test target would put a door behind the safeguards
// appropriate to a buzzer.
//
// The firmware refuses the same names independently (parseDeviceTestTarget), so
// this is one of two locks rather than the only one.
func TestADeviceTestCannotOperateTheRelay(t *testing.T) {
	spec := CommandSpecs[CommandDeviceTest]

	for _, target := range []string{"relay", "unlock", "door", "strike", "RELAY"} {
		raw := json.RawMessage(`{"target":"` + target + `"}`)
		if _, err := spec.ValidateParams(raw); err == nil {
			t.Fatalf("a device test accepted target %q: a door must not be "+
				"openable through a hardware test", target)
		}
	}

	// The refusal for `relay` NAMES the real answer rather than saying
	// "unknown target", which would send an integrator to check their spelling
	// instead of to the command that actually opens a door.
	_, err := spec.ValidateParams(json.RawMessage(`{"target":"relay"}`))
	if err == nil || !strings.Contains(err.Error(), "own command") {
		t.Errorf("the relay refusal should point at the right command, got %v", err)
	}
}

func TestDeviceTestAcceptsOnlyTheKnownTargets(t *testing.T) {
	spec := CommandSpecs[CommandDeviceTest]

	for _, target := range DeviceTestTargets {
		out, err := spec.ValidateParams(json.RawMessage(`{"target":"` + target + `"}`))
		if err != nil {
			t.Fatalf("target %q was refused: %v", target, err)
		}
		var parsed DeviceTestParams
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("validated params for %q do not decode: %v", target, err)
		}
		if parsed.Target != target {
			t.Errorf("target %q came back as %q", target, parsed.Target)
		}
	}

	// A missing target is refused rather than defaulted. The target is the
	// WHOLE of what this command asks for, so running nothing and reporting
	// success would be a lie.
	if _, err := spec.ValidateParams(nil); err == nil {
		t.Error("a device test with no target was accepted")
	}
	if _, err := spec.ValidateParams(json.RawMessage(`{}`)); err == nil {
		t.Error("a device test with an empty object was accepted")
	}
}

// TestDiagnosticSectionsAreExpandedOnTheWayIn.
//
// An absent list is expanded to every section HERE rather than defaulted at the
// terminal, so what a door reports cannot depend on how its firmware read a
// missing field. The firmware implements the same fallback as the second half
// of the agreement, for the case where this server is older than the rule.
func TestDiagnosticSectionsAreExpandedOnTheWayIn(t *testing.T) {
	spec := CommandSpecs[CommandDiagnosticSnapshot]

	for _, input := range []json.RawMessage{nil, json.RawMessage(`{}`),
		json.RawMessage(`{"include":[]}`)} {

		out, err := spec.ValidateParams(input)
		if err != nil {
			t.Fatalf("input %s was refused: %v", input, err)
		}
		var parsed DiagnosticParams
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatalf("validated params do not decode: %v", err)
		}
		if len(parsed.Include) != len(DiagnosticSections) {
			t.Errorf("input %s expanded to %v, want every section",
				input, parsed.Include)
		}
	}
}

// An unknown SECTION is refused at the platform, even though the firmware
// ignores one. The asymmetry is deliberate and is not an inconsistency: the
// platform knows exactly which sections it can ask for, so a typo is a client
// bug worth reporting; the firmware may legitimately be older than the server
// and must degrade rather than fail.
func TestUnknownDiagnosticSectionsAreRefused(t *testing.T) {
	spec := CommandSpecs[CommandDiagnosticSnapshot]

	if _, err := spec.ValidateParams(
		json.RawMessage(`{"include":["network","quantum_flux"]}`)); err == nil {
		t.Error("an unknown diagnostic section was accepted")
	}
}

func TestDiagnosticSectionsAreDeduplicated(t *testing.T) {
	spec := CommandSpecs[CommandDiagnosticSnapshot]

	out, err := spec.ValidateParams(
		json.RawMessage(`{"include":["network","network","storage"]}`))
	if err != nil {
		t.Fatalf("a duplicated section was refused: %v", err)
	}
	var parsed DiagnosticParams
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Include) != 2 {
		t.Errorf("include = %v, want the duplicate collapsed", parsed.Include)
	}
}

func TestMalformedParamsAreRefusedRatherThanIgnored(t *testing.T) {
	for _, name := range IssuableCommandTypes() {
		spec := CommandSpecs[name]
		if spec.ValidateParams == nil {
			continue
		}
		if _, err := spec.ValidateParams(json.RawMessage(`"not an object"`)); err == nil {
			t.Errorf("%s accepted a non-object parameter block", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

// TestStateAndCommandTypesDoNotOverlap.
//
// The distinction is the whole architecture: a STATE job is declarative and
// re-applying it is a no-op by construction; a COMMAND is an event, and
// applying it twice does it twice. A type in both lists would be one the
// platform believed it could safely redeliver.
func TestStateAndCommandTypesDoNotOverlap(t *testing.T) {
	for _, state := range SyncJobStateTypes {
		if IsCommandType(state) {
			t.Errorf("%s is registered as both STATE and COMMAND", state)
		}
		if got := CommandClassFor(state); got != CommandClassState {
			t.Errorf("CommandClassFor(%s) = %s, want STATE", state, got)
		}
	}
	for name := range CommandSpecs {
		if got := CommandClassFor(name); got != CommandClassCommand {
			t.Errorf("CommandClassFor(%s) = %s, want COMMAND", name, got)
		}
	}
}

// TestReservedJobTypesAreNotCommands.
//
// INCREMENTAL_SYNC, PERMISSION_PUSH, TEMPLATE_PUSH, FIRMWARE_UPDATE and
// LOG_PULL exist in the schema's CHECK constraint and nowhere else -- no Go
// code enqueues them and no firmware parses them. Named so the next person to
// reach for FIRMWARE_UPDATE finds out from a constant rather than from a fleet
// that silently ignored them.
func TestReservedJobTypesAreNotCommands(t *testing.T) {
	for _, reserved := range SyncJobReserved {
		if IsCommandType(reserved) {
			t.Errorf("%s is reserved vocabulary and must not be issuable "+
				"until both sides implement it", reserved)
		}
	}
}

func TestTerminalStatesAreClosed(t *testing.T) {
	open := []string{CommandStateNone, CommandStateQueued, CommandStateDelivered}
	closed := []string{CommandStateAccepted, CommandStateFailed,
		CommandStateExpired, CommandStateCancelled}

	for _, state := range open {
		if CommandStateIsTerminal(state) {
			t.Errorf("%s is reported terminal but can still change", state)
		}
	}
	for _, state := range closed {
		if !CommandStateIsTerminal(state) {
			t.Errorf("%s can no longer change but is not reported terminal", state)
		}
	}
}
