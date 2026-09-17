package assistant

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Projection helpers.
//
// A tool decodes the API body into a plain map and copies the fields it names
// -- nothing else. `pick` and `pickList` are the whole mechanism, and their
// key lists are the allow-list the model is held to. There is deliberately no
// "copy everything" helper.

type object = map[string]any

func decodeObject(resp Response) (object, error) {
	var m object
	if err := resp.JSON(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func pick(m object, keys ...string) object {
	out := object{}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

func pickList(m object, key string, keys ...string) []object {
	out := []object{}
	items, _ := m[key].([]any)
	for _, item := range items {
		if o, ok := item.(object); ok {
			out = append(out, pick(o, keys...))
		}
	}
	return out
}

func str(m object, key string) string {
	s, _ := m[key].(string)
	return s
}

func boolOf(m object, key string) bool {
	b, _ := m[key].(bool)
	return b
}

func num(m object, key string) int {
	f, _ := m[key].(float64)
	return int(f)
}

// get performs a GET and decodes the body, or returns the failed outcome.
func get(t *Turn, path string) (object, Response, *Outcome) {
	resp, err := t.Call(http.MethodGet, path, nil)
	if err != nil {
		o := Outcome{IsError: true, Status: "FAILED", Result: "That is temporarily unavailable."}
		return nil, resp, &o
	}
	if resp.Status < 200 || resp.Status >= 300 {
		o := failed(resp)
		return nil, resp, &o
	}
	m, err := decodeObject(resp)
	if err != nil {
		o := Outcome{IsError: true, Status: "FAILED", HTTPStatus: resp.Status, Route: resp.Route,
			RequestID: resp.RequestID, Result: "The response could not be read."}
		return nil, resp, &o
	}
	return m, resp, nil
}

// post performs a write and decodes the body, or returns the failed outcome.
func post(t *Turn, method, path string, body any) (object, Response, *Outcome) {
	resp, err := t.Call(method, path, body)
	if err != nil {
		o := Outcome{IsError: true, Status: "FAILED", Result: "That is temporarily unavailable."}
		return nil, resp, &o
	}
	if resp.Status < 200 || resp.Status >= 300 {
		o := failed(resp)
		return nil, resp, &o
	}
	if resp.Status == http.StatusNoContent || len(resp.Body) == 0 {
		return object{}, resp, nil
	}
	m, err := decodeObject(resp)
	if err != nil {
		o := Outcome{IsError: true, Status: "FAILED", HTTPStatus: resp.Status, Route: resp.Route,
			RequestID: resp.RequestID, Result: "The response could not be read."}
		return nil, resp, &o
	}
	return m, resp, nil
}

// The field allow-lists, one place each.
var (
	personFields   = []string{"external_id", "full_name", "category", "active", "biometric_enrolled", "created_at"}
	ruleFields     = []string{"id", "effect", "scope_type", "site_id", "site_name", "device_serial", "device_name", "application", "schedule_id", "schedule_name", "starts_at", "ends_at", "active"}
	enrolFields    = []string{"status", "terminal_serial", "terminal_name", "site_name", "terminal_status", "error_message", "expires_at", "completed_at", "created_at"}
	terminalFields = []string{"serial_number", "device_name", "site_public_id", "site_name", "status", "active", "last_heartbeat_at", "last_sync_at", "firmware_version", "firmware_outdated", "application_mode", "effective_applications"}
	siteFields     = []string{"id", "name", "address", "timezone", "active", "terminal_count", "offline_policy", "offline_grace_minutes"}
	scheduleFields = []string{"id", "name", "description", "timezone", "active", "windows", "permission_count"}
	eventFields    = []string{"id", "occurred_at", "occurred_at_trusted", "event_type", "decision", "reason", "person_name", "subject_external_id", "device_serial", "device_name", "site_name", "application", "direction"}

	// Phase 2a. Each list is the whole of what its tool may say.

	// A credential's state and place. No template, no digest, no key, no slot,
	// no locator, no vendor: the route carries none and this names none.
	credentialFields = []string{"id", "type", "state", "enrolled_at", "usable_at_terminal_count"}

	// A command: what was asked, where it got to, and what came back. The
	// `result` payload is projected separately (diagnosticResult) because it
	// is the terminal's own document and carries network detail.
	commandFields = []string{"id", "type", "state", "reason", "already_pending", "queued_at", "delivered_at",
		"acknowledged_at", "expires_at", "result_code", "error", "attempts", "terminal_status", "online"}
	commandOfferFields = []string{"type", "supported", "read_only", "repeatable", "min_role"}

	// A terminal waiting to be set up. NOT pairing_code, first_seen_ip,
	// last_seen_ip, adopted_by, hardware_revision or capabilities.
	pendingTerminalFields = []string{"id", "serial_number", "state", "verdict", "firmware_version", "announced_at", "last_seen_at"}

	// The effective offline policy. Not the free-form settings blob.
	siteSettingsFields = []string{"offline_policy", "offline_grace_minutes", "settings_version"}

	// An access decision.
	decisionFields = []string{"granted", "reason", "person_name", "external_id", "application", "matched_permission", "decided_at"}
)

// diagnosticResult projects a DIAGNOSTIC_SNAPSHOT's result: every section
// the firmware reports, less the network identifiers (ssid, ip). Signal
// strength and whether it is associated are what an operator needs to hear;
// the network's name and address are infrastructure and stay in the console.
func diagnosticResult(raw any) object {
	m, ok := raw.(object)
	if !ok {
		return nil
	}
	out := object{}
	if net, ok := m["network"].(object); ok {
		out["network"] = pick(net, "associated", "rssi_dbm")
	}
	if q, ok := m["outbound_queue"].(object); ok {
		out["outbound_queue"] = pick(q, "enrolments", "access_logs", "dropped_access_logs")
	}
	if w, ok := m["worklist"].(object); ok {
		out["worklist"] = pick(w, "missing")
	}
	if s, ok := m["storage"].(object); ok {
		out["storage"] = pick(s, "members", "capacity", "orphaned_slots", "write_failed", "fallback_store")
	}
	return out
}

// commandView projects one command row, with its result through the
// diagnostic allow-list when it is a snapshot and dropped otherwise (the
// only other issuable command, a device test, returns nothing to show).
func commandView(c object) object {
	out := pick(c, commandFields...)
	if str(c, "type") == "DIAGNOSTIC_SNAPSHOT" {
		if r := diagnosticResult(c["result"]); r != nil {
			out["result"] = r
		}
	}
	return out
}

func personLabel(p object) string {
	if name := str(p, "full_name"); name != "" {
		return name
	}
	return str(p, "external_id")
}

func terminalLabel(t object) string {
	if name := str(t, "device_name"); name != "" {
		return name
	}
	return str(t, "serial_number")
}

func scopeLabel(rule object) string {
	switch str(rule, "scope_type") {
	case "SITE":
		if s := str(rule, "site_name"); s != "" {
			return s
		}
		return "one site"
	case "TERMINAL":
		if d := str(rule, "device_name"); d != "" {
			return d
		}
		if d := str(rule, "device_serial"); d != "" {
			return d
		}
		return "one terminal"
	default:
		return "everywhere"
	}
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func fmtCount(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
