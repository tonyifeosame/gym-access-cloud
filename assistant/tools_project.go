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
	scheduleFields = []string{"id", "name", "timezone", "windows", "permission_count"}
	eventFields    = []string{"id", "occurred_at", "occurred_at_trusted", "event_type", "decision", "reason", "person_name", "subject_external_id", "device_serial", "device_name", "site_name"}
)

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
