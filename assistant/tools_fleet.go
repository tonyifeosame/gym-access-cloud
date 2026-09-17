package assistant

import (
	"net/http"
	"time"

	"access-terminal-cloud-api/models"
)

// The fleet: what a terminal can do, what it has been asked to do, and the
// two day-to-day writes an operator uses to fix one.
//
// Routes: GET /console/terminals/:serial/capabilities
// (ConsoleTerminalCapabilities), GET /console/terminals/:serial/commands[/:id]
// (ConsoleListCommands, ConsoleGetCommand), POST /console/terminals/:serial/commands
// (ConsoleIssueCommand), POST /console/terminals/:serial/resync
// (ConsoleResyncTerminal), GET /console/terminal-announcements
// (ConsoleListPendingTerminals), GET /console/sites/:id/settings (GetSiteSettings).
//
// TWO COMMANDS ARE ISSUABLE FROM HERE, EACH BY ITS OWN NAMED TOOL:
// DIAGNOSTIC_SNAPSHOT, a read of the terminal's own state, and DEVICE_TEST,
// which exercises one piece of hardware. There is no tool that takes a
// command type: the type is written into the tool, and DEVICE_TEST's only
// argument is a target from models.DeviceTestTargets -- a closed set that
// does not contain the relay and must not. The registry (models.CommandSpecs)
// gates both on the terminal's reported capability and on MANAGER, and the
// route enforces both; the tools add nothing to that.
//
// A DEVICE TEST ASKS FIRST. It is not ReadOnly in the registry and the
// reason is physical: somebody in the room hears a tone or sees a panel
// light, and an operator who did not intend it sends a technician looking
// for a fault. A diagnostic, which nobody can perceive, does not ask.
//
// WITHDRAWING IS SAFE AND SO DOES NOT ASK. It makes the terminal do LESS
// than it was already going to, the route refuses anything already
// collected, and the result says which it was. Nothing that stops a door is
// reachable from here at all.

func registerFleetTools(r *Registry) {
	r.Register(&Tool{
		Name:        "get_terminal_capabilities",
		Description: "What one terminal can do: the commands it supports, so you know before asking.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, consoleTerminals+"/"+Segment(a.String("serial"))+"/capabilities")
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"serial_number":            str(m, "serial_number"),
				"capabilities_reported_at": m["capabilities_reported_at"],
				"commands":                 pickList(m, "commands", commandOfferFields...),
			})
		},
	})

	r.Register(&Tool{
		Name:        "list_terminal_commands",
		Description: "Commands sent to one terminal and what became of each, newest first.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, consoleTerminals+"/"+Segment(a.String("serial"))+"/commands")
			if fail != nil {
				return *fail
			}
			items, _ := m["commands"].([]any)
			out := []object{}
			for _, item := range items {
				if c, ok := item.(object); ok {
					out = append(out, commandView(c))
				}
			}
			return succeeded(resp, object{"serial_number": str(m, "serial_number"), "count": num(m, "count"), "commands": out})
		},
	})

	r.Register(&Tool{
		Name:        "get_command",
		Description: "One command's state and, once the terminal has answered, its result.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "command_id", Type: "string", Description: "The command's id.", Required: true, MaxLen: 64, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			return commandState(t, a.String("serial"), a.String("command_id"))
		},
	})

	r.Register(&Tool{
		Name: "request_diagnostic",
		Description: "Ask a terminal to report on itself: signal strength, queued work, storage. Changes " +
			"nothing on the terminal. Use wait_for_command to collect the report; it can take a minute.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "reason", Type: "string", Description: "Optional: the operator's own words on why.", MaxLen: 200},
		},
		Idempotent: false,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainTerminals, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			serial := a.String("serial")
			m, resp, fail := post(t, http.MethodPost, consoleTerminals+"/"+Segment(serial)+"/commands", object{
				"type":   models.CommandDiagnosticSnapshot,
				"reason": assistantReason(a.String("reason")),
			})
			if fail != nil {
				return *fail
			}
			note := "Queued. The terminal collects it on its next poll; use wait_for_command with this command id."
			if boolOf(m, "already_pending") {
				note = "A diagnostic was already waiting for this terminal; that one is returned. Use wait_for_command with its id."
			}
			out := succeeded(resp, object{"command": commandView(m), "note": note})
			out.Handoff = handoffTerminal(serial, serial)
			return out
		},
	})

	r.Register(&Tool{
		Name: "run_device_test",
		Description: "Ask a terminal to exercise one piece of its hardware and report what happened: the " +
			"buzzer, the display, or a self-test of both. Somebody standing at the terminal will hear " +
			"or see it, so the operator is asked to approve first. It cannot open a door. Use " +
			"wait_for_command to collect the result.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "target", Type: "string", Description: "What to exercise: buzzer sounds a tone, display lights the panel, self_test runs both.", Required: true, Enum: models.DeviceTestTargets},
			{Name: "reason", Type: "string", Description: "Optional: the operator's own words on why.", MaxLen: 200},
		},
		// NOT Destructive, AND IT STILL ASKS. The MCP annotation says whether
		// an operation destroys state, and this destroys none: no roster
		// changes, no rule changes, nothing is stored. It asks because it is
		// PERCEPTIBLE -- the two are different properties and the registry
		// treats them as such.
		Destructive: false, Idempotent: false,
		MinRole: models.RoleManager,
		Domains: []string{DomainTerminals, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			// A CARD IS ONLY SHOWN FOR A TEST THAT CAN ACTUALLY RUN.
			//
			// The route refuses a command a terminal could not collect --
			// disabled, no credential, incapable, not in contact -- and a card
			// for one of those would be asking the operator to approve
			// something that cannot happen, spending their single-use
			// confirmation to be told no. So the same four conditions the
			// store checks are checked here first, in the store's own order,
			// from the two reads the console's own command panel already
			// uses: the terminal detail and its capabilities.
			//
			// THE ROUTE REMAINS THE GATE. Nothing here authorises anything;
			// a terminal that goes offline between this and the approval is
			// refused by the route exactly as before.
			serial := a.String("serial")
			terminal, _, fail := get(t, consoleTerminals+"/"+Segment(serial))
			if fail != nil {
				return nil, failError(fail)
			}
			label := terminalLabel(terminal)
			if why := terminalCannotBeCommanded(terminal, label); why != "" {
				return nil, errText(why)
			}
			capabilities, _, fail := get(t, consoleTerminals+"/"+Segment(serial)+"/capabilities")
			if fail != nil {
				return nil, failError(fail)
			}
			if why := terminalCannotRunDeviceTest(capabilities, label); why != "" {
				return nil, errText(why)
			}
			if why := terminalIsNotInContact(terminal, label); why != "" {
				return nil, errText(why)
			}
			// The card names the terminal and the place, which is what makes
			// "somebody will hear this" a fact the operator can check rather
			// than a warning about a serial number.
			return &ConfirmationPlan{
				Consequence: deviceTestConsequence(label, str(terminal, "site_name"),
					a.String("target"), str(terminal, "status")),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			serial, target := a.String("serial"), a.String("target")
			// Checked again here, not because the schema's enum could be
			// bypassed, but because the enum is built from the model
			// registry's own list: if that list ever grew a target this tool
			// should not offer, the tool would start offering it silently.
			if !contains(models.DeviceTestTargets, target) {
				return Outcome{IsError: true, Status: models.ToolCallInvalid,
					Result: "That is not a test this platform runs."}
			}
			m, resp, fail := post(t, http.MethodPost, consoleTerminals+"/"+Segment(serial)+"/commands", object{
				"type":   models.CommandDeviceTest,
				"params": object{"target": target},
				"reason": assistantReason(a.String("reason")),
			})
			if fail != nil {
				return *fail
			}
			note := "Queued. The terminal runs it on its next poll, and it lapses if it is not collected " +
				"within five minutes; use wait_for_command with this command id."
			if boolOf(m, "already_pending") {
				note = "A test was already waiting for this terminal; that one is returned. Use wait_for_command with its id."
			}
			out := succeeded(resp, object{"command": commandView(m), "note": note})
			out.Handoff = handoffTerminal(serial, serial)
			return out
		},
	})

	r.Register(&Tool{
		Name: "withdraw_command",
		Description: "Cancel a command that is still waiting to be collected, so the terminal never runs " +
			"it. A command the terminal has already collected cannot be recalled and the platform " +
			"refuses rather than pretending.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "command_id", Type: "string", Description: "The command's id, from list_terminal_commands.", Required: true, MaxLen: 64, Identifier: true},
		},
		Idempotent: true,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainTerminals, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			serial := a.String("serial")
			m, resp, fail := post(t, http.MethodDelete, consoleTerminals+"/"+Segment(serial)+"/commands/"+
				Segment(a.String("command_id")), nil)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"command": commandView(m),
				"note":    "Withdrawn. The terminal will never be offered it.",
			})
			out.Handoff = handoffTerminal(serial, serial)
			return out
		},
	})

	r.Register(&Tool{
		Name: "wait_for_command",
		Description: "Wait up to timeout_s seconds (default 30, max 60) for a command to be answered, fail, " +
			"expire or be cancelled, and return it. Returns the current state at the timeout otherwise.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "command_id", Type: "string", Description: "The command's id.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "timeout_s", Type: "integer", Description: "Seconds to wait (1-60).", Min: intPtr(1), Max: intPtr(maxWaitSeconds)},
		},
		ReadOnly: true, Idempotent: true,
		MinRole:     models.RoleViewer,
		MaxDuration: (maxWaitSeconds + 5) * time.Second,
		Run: func(t *Turn, a Args) Outcome {
			timeout := a.Int("timeout_s")
			if timeout == 0 {
				timeout = 30
			}
			serial, id := a.String("serial"), a.String("command_id")
			return pollUntil(t, time.Duration(timeout)*time.Second, 3*time.Second, func(p *Turn) (Outcome, bool) {
				o := commandState(p, serial, id)
				if o.IsError {
					return o, true
				}
				result, _ := o.Result.(object)
				c, _ := result["command"].(object)
				switch str(c, "state") {
				case models.CommandStateAccepted, models.CommandStateFailed, models.CommandStateExpired, models.CommandStateCancelled:
					return o, true
				}
				return o, false
			})
		},
	})

	r.Register(&Tool{
		Name: "resync_terminal",
		Description: "Send a terminal a fresh copy of everything it should hold (people, rules, settings). " +
			"The usual fix when a terminal does not recognise a recent change. Safe to repeat.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
		},
		Idempotent: true,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainTerminals, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			serial := a.String("serial")
			m, resp, fail := post(t, http.MethodPost, consoleTerminals+"/"+Segment(serial)+"/resync", nil)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"serial_number": str(m, "serial_number"),
				"pending_jobs":  num(m, "pending_jobs"),
				"note":          "Queued. The terminal picks it up on its next poll; check last_sync_at with get_terminal afterwards.",
			})
			out.Handoff = handoffTerminal(serial, serial)
			return out
		},
	})

	r.Register(&Tool{
		Name: "list_pending_terminals",
		Description: "Terminals that have announced themselves and are waiting to be set up. Setting one " +
			"up (typing its pairing code, choosing its site) happens in the console.",
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleManager,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/terminal-announcements")
			if fail != nil {
				return *fail
			}
			items, _ := m["pending"].([]any)
			out := []object{}
			for _, item := range items {
				p, ok := item.(object)
				if !ok {
					continue
				}
				row := pick(p, pendingTerminalFields...)
				if existing, ok := p["existing_terminal"].(object); ok {
					row["existing_terminal"] = pick(existing, "serial_number", "device_name", "site_name", "status")
				}
				out = append(out, row)
			}
			o := succeeded(resp, object{"count": num(m, "count"), "pending": out})
			if len(out) > 0 {
				o.Handoff = handoffPendingTerminals()
			}
			return o
		},
	})

	r.Register(&Tool{
		Name:        "get_site_settings",
		Description: "A site's offline policy: what its terminals do when they cannot reach the platform, and for how long.",
		Params: []Param{
			{Name: "site_id", Type: "string", Description: "The site's id, from list_sites.", Required: true, MaxLen: 64, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/sites/"+Segment(a.String("site_id"))+"/settings")
			if fail != nil {
				return *fail
			}
			return succeeded(resp, pick(m, siteSettingsFields...))
		},
	})
}

// commandState reads one command and projects it.
func commandState(t *Turn, serial, id string) Outcome {
	m, resp, fail := get(t, consoleTerminals+"/"+Segment(serial)+"/commands/"+Segment(id))
	if fail != nil {
		return *fail
	}
	return succeeded(resp, object{"command": commandView(m)})
}

// --- can this terminal collect a command at all? --------------------------------------
//
// THESE MIRROR database/commands.go's commandTarget.deliverable, IN ITS ORDER,
// and they are checked before a confirmation card is built rather than after
// the operator has approved one. The order matters and is the store's: a
// terminal that is both offline and incapable is told it is incapable,
// because that is the condition somebody has to fix first.
//
// The facts come from reads that already exist. `status`, `active` and
// `health.credential_active` are on GET /console/terminals/:serial --
// credential_active is derived from the key hash being present, which is the
// same test the store makes -- and `supported` is on
// GET /console/terminals/:serial/capabilities, the route the console's own
// command panel draws its buttons from. Nothing here reads the database, and
// nothing here decides whether a command may be issued; the route does that
// again, unchanged, when the operator approves.
//
// THE SENTENCES ARE THE CONSOLE'S OWN refusal wording
// (handlers/console_wifi_recovery.go, terminalUnreachableMessage), kept here
// because that function is unexported and a second wording for the same
// refusal is how two surfaces start disagreeing. Change them together.

// terminalCannotBeCommanded covers the two conditions that hold whatever the
// command is: switched off, and holding no credential.
func terminalCannotBeCommanded(terminal object, label string) string {
	if str(terminal, "status") == "DISABLED" || isExplicitlyFalse(terminal, "active") {
		return label + " is disabled, so it will not collect commands. Re-enable it first."
	}
	// ABSENT IS NOT FALSE. A body without the health block says nothing about
	// the credential, and refusing on a field that is missing would block a
	// working terminal; the route still checks.
	if health, ok := terminal["health"].(object); ok && isExplicitlyFalse(health, "credential_active") {
		return label + " holds no credential, so it cannot collect commands. Provision it first."
	}
	return ""
}

// terminalCannotRunDeviceTest reports whether the terminal has told the
// platform it can run one. The two messages are the store's, because "we have
// never been told" and "it told us it cannot" send somebody to different
// places.
func terminalCannotRunDeviceTest(capabilities object, label string) string {
	for _, offer := range pickList(capabilities, "commands", commandOfferFields...) {
		if str(offer, "type") != models.CommandDeviceTest {
			continue
		}
		if boolOf(offer, "supported") {
			return ""
		}
		if capabilities["capabilities_reported_at"] != nil {
			return label + " reported what it can do, and a hardware test is not among it, so nothing would be sent."
		}
		return label + " has never reported what it can do, so the platform cannot tell whether it would " +
			"carry out a hardware test, and nothing would be sent."
	}
	// The command is not offered by this platform at all. Unreachable while
	// DEVICE_TEST is registered and issuable, and a refusal rather than a
	// card if that ever stops being true.
	return "This platform is not offering hardware tests."
}

// terminalIsNotInContact is the LAST of the four, matching the store.
//
// UPDATING AND ERROR ARE NOT REFUSED, deliberately and to the store's letter:
// both are states a terminal reports while it is still heartbeating, and
// refusing one that is talking to us would invent a restriction the transport
// does not have.
func terminalIsNotInContact(terminal object, label string) string {
	switch str(terminal, "status") {
	case "ONLINE", "UPDATING", "ERROR":
		return ""
	}
	return label + " is not in contact with the platform, so the poll that would collect the test is not " +
		"happening and nothing would be sent. Bring it back online first."
}

// isExplicitlyFalse distinguishes a field that is present and false from one
// that is absent, which the checks above must not treat alike.
func isExplicitlyFalse(m object, key string) bool {
	value, ok := m[key].(bool)
	return ok && !value
}
