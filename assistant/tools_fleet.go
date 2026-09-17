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
// ONE COMMAND IS ISSUABLE FROM HERE: DIAGNOSTIC_SNAPSHOT, a read of the
// terminal's own state. The registry (models.CommandSpecs) gates it on the
// terminal's reported capability and on MANAGER, and the route enforces
// both; the tool adds nothing. DEVICE_TEST, which makes a noise in a room,
// is not offered in this phase. Nothing that stops a door is here at all.

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
