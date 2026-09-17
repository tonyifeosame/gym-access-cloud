package assistant

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// The registry's own guarantees: the model's arguments are checked, the tool
// list is fixed and role-filtered, and nothing secret-shaped passes the
// result guard.

// phase1Tools, phase2aTools and phase2bTools are the three approved
// catalogues. The union is what the registry holds; nothing else may appear.
var (
	phase1Tools = []string{
		"create_person", "get_fleet_summary", "get_person", "get_person_enrollment", "get_site",
		"get_terminal", "grant_access", "list_events", "list_schedules", "list_sites",
		"list_terminals", "revoke_access", "search_people", "start_enrollment", "wait_for_enrollment",
	}
	phase2aTools = []string{
		"cancel_enrollment", "create_schedule", "evaluate_access", "explain_denial", "get_command",
		"get_site_settings", "get_terminal_capabilities", "list_pending_terminals",
		"list_people_without_access", "list_person_credentials", "list_terminal_commands",
		"request_diagnostic", "resync_terminal", "set_person_active", "update_person",
		"update_schedule", "wait_for_command",
	}
	// Phase 2b: one read, one safe write, four that ask first.
	phase2bTools = []string{
		"approve_pending_terminal", "delete_schedule", "list_audit", "reject_pending_terminal",
		"run_device_test", "withdraw_command",
	}
	// Named in the audit as later, high-risk or never: none may be registered.
	// Phase 2b's six have moved out of this list and into phase2bTools; what
	// remains is what the assistant still must not be able to do.
	notInPhase2b = []string{
		"delete_person", "set_terminal_mode", "set_terminal_enabled", "move_terminal", "request_wifi_recovery",
		"revoke_terminal_credential", "retire_terminal", "release_terminal", "cancel_terminal_release",
		"force_terminal_release", "adopt_terminal", "issue_claim_code", "create_site", "update_site",
		"retire_site", "rotate_site_key", "set_site_offline_policy", "list_operators", "create_operator",
		"update_operator", "delete_operator", "set_operator_sites", "invite_operator", "reset_operator_password",
		"list_firmware", "publish_firmware", "set_current_firmware", "set_application",
		"list_api_credentials", "create_api_credential", "rotate_api_credential", "revoke_api_credential",
		"issue_command", "run_command", "call_endpoint", "summarise_events",
	}
)

func TestCatalogueIsExactlyWhatWasApproved(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	want := append(append(append([]string{}, phase1Tools...), phase2aTools...), phase2bTools...)
	sort.Strings(want)
	got := r.Names(models.RoleOwner)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalogue = %v\nwant       %v", got, want)
	}
	for _, name := range notInPhase2b {
		if _, ok := r.Get(name); ok {
			t.Errorf("%s is registered but is outside Phase 2b", name)
		}
	}
	// Consequential tools all confirm; read tools never do.
	for _, name := range []string{"grant_access", "revoke_access", "start_enrollment", "set_person_active",
		"update_schedule", "delete_schedule", "run_device_test", "approve_pending_terminal", "reject_pending_terminal"} {
		if tool, _ := r.Get(name); tool.Confirm == nil {
			t.Errorf("%s does not require confirmation", name)
		}
	}
	// Safe writes run without a card.
	for _, name := range []string{"create_person", "update_person", "create_schedule", "resync_terminal",
		"cancel_enrollment", "request_diagnostic", "withdraw_command"} {
		if tool, _ := r.Get(name); tool.Confirm != nil {
			t.Errorf("%s requires confirmation but is a safe write", name)
		}
	}
	for _, d := range r.ForRole(models.RoleOwner) {
		tool, _ := r.Get(d.Name)
		if tool.ReadOnly && tool.Confirm != nil {
			t.Errorf("%s is read-only yet confirms", d.Name)
		}
		// Every tool the model sees is a closed schema.
		if d.InputSchema["additionalProperties"] != false {
			t.Errorf("%s allows unknown arguments", d.Name)
		}
	}
	// Adding a person is not the same tool as enrolling one.
	if tool, _ := r.Get("create_person"); tool.Confirm != nil {
		t.Errorf("create_person must not require confirmation")
	}
}

func TestRegistryHidesByRoleButKeepsOrderStable(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	viewer := r.Names(models.RoleViewer)
	for _, hidden := range []string{"create_person", "grant_access", "revoke_access", "start_enrollment",
		"update_person", "set_person_active", "create_schedule", "update_schedule", "delete_schedule",
		"resync_terminal", "cancel_enrollment", "request_diagnostic", "run_device_test", "withdraw_command",
		"evaluate_access", "explain_denial", "list_pending_terminals", "list_audit",
		"approve_pending_terminal", "reject_pending_terminal"} {
		if contains(viewer, hidden) {
			t.Errorf("VIEWER sees %s", hidden)
		}
	}
	// PHASE 2B SPLITS MANAGER FROM ADMIN, which Phase 2a did not. The audit
	// trail names colleagues and the two terminal decisions authorise
	// hardware: all three are ADMIN on their routes, so a MANAGER is not
	// shown them and a MANAGER who called one anyway gets the route's 403.
	manager := r.Names(models.RoleManager)
	for _, hidden := range []string{"list_audit", "approve_pending_terminal", "reject_pending_terminal"} {
		if contains(manager, hidden) {
			t.Errorf("MANAGER sees %s", hidden)
		}
	}
	for _, shown := range []string{"delete_schedule", "run_device_test", "withdraw_command"} {
		if !contains(manager, shown) {
			t.Errorf("MANAGER is not shown %s", shown)
		}
	}
	admin := r.Names(models.RoleAdmin)
	owner := r.Names(models.RoleOwner)
	if strings.Join(admin, ",") != strings.Join(owner, ",") {
		t.Fatalf("ADMIN and OWNER see different tools: %v vs %v", admin, owner)
	}
	if len(owner) != len(manager)+3 {
		t.Fatalf("ADMIN sees %d tools and MANAGER %d; the difference should be the three ADMIN ones",
			len(owner), len(manager))
	}
	if again := r.Names(models.RoleOwner); strings.Join(again, ",") != strings.Join(owner, ",") {
		t.Fatalf("tool order is not stable between calls")
	}
}

func TestValidateArgsRefusesWhatTheSchemaDoesNotAllow(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	grant, _ := r.Get("grant_access")
	cases := map[string]string{
		`{"external_id":"P-1","effect":"MAYBE","scope_type":"SITE","site_id":"s"}`:       "effect must be one of",
		`{"external_id":"P-1","effect":"ALLOW","scope_type":"SITE","site_id":"s","x":1}`: "unknown argument",
		`{"effect":"ALLOW","scope_type":"SITE"}`:                                         "external_id is required",
		`{"external_id":"P-1","effect":"ALLOW","scope_type":"SITE","first_day":"soon"}`:  "first_day must be a date",
		`[1,2]`: "not a JSON object",
	}
	for raw, want := range cases {
		if _, err := grant.ValidateArgs(json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", raw, err, want)
		}
	}
	people, _ := r.Get("search_people")
	if _, err := people.ValidateArgs(json.RawMessage(`{"limit": 500}`)); err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Errorf("limit bound not enforced: %v", err)
	}
	args, err := people.ValidateArgs(json.RawMessage(`{"query":"  Ada ","limit":10}`))
	if err != nil || args.String("query") != "Ada" || args.Int("limit") != 10 {
		t.Fatalf("valid args = %v, %v", args, err)
	}
}

func TestResultGuardWithholdsSecretShapes(t *testing.T) {
	leaks := []any{
		map[string]any{"name": "Site", "key": "ats_" + strings.Repeat("ab", 20)},
		map[string]any{"device_key": "atd_" + strings.Repeat("cd", 20)},
		map[string]any{"credential": "atp_live_" + strings.Repeat("e", 24)},
		map[string]any{"authorization": "Bearer " + strings.Repeat("x", 30)},
		map[string]any{"pairing_code": "K7M2-P4QX"},
		map[string]any{"password": "hunter2hunter2"},
		map[string]any{"pem": "-----BEGIN PRIVATE KEY-----"},
	}
	for _, leak := range leaks {
		if _, err := resultBytes("test_tool", leak); err == nil {
			t.Errorf("guard passed %v", leak)
		}
	}
	ok := map[string]any{"full_name": "Ada", "external_id": "P-1", "note": "the site key was rotated"}
	if _, err := resultBytes("test_tool", ok); err != nil {
		t.Fatalf("guard refused an ordinary result: %v", err)
	}
}

func TestConsequenceWordingIsTheDialogsWording(t *testing.T) {
	// The sentences the console's own dialogs show, kept here as the single
	// source the assistant reads. If a dialog's wording changes, this is the
	// test that says the assistant must change with it.
	c := grantConsequence("Ada", "DENY", "Reception (Lagos)", "at any time", "")
	if !strings.Contains(c.Body, "Keep out always wins, even if another rule lets them in.") {
		t.Fatalf("keep-out consequence = %q", c.Body)
	}
	r := revokeConsequence("Ada", "ALLOW", "everywhere")
	if !strings.Contains(r.Body, "If no other rule covers that place, they will be refused.") {
		t.Fatalf("revoke consequence = %q", r.Body)
	}
	e := enrollmentConsequence("Ada", "Reception", "Lagos")
	if !strings.HasPrefix(e.Title, "Ready to start fingerprint enrollment at Reception") ||
		!strings.Contains(e.Body, "AccessLink never keeps a copy") {
		t.Fatalf("enrolment consequence = %+v", e)
	}
}

func TestIdentifierArgumentsRefusePathDelimiters(t *testing.T) {
	// Anything a tool puts in a request path is checked here, before it can
	// change which route the path reaches. The router decodes %2F before it
	// matches, so an escaped slash is as good as a bare one.
	r := NewRegistry()
	registerTools(r)
	for _, tool := range []string{"get_person", "get_person_enrollment", "create_person", "grant_access", "revoke_access", "start_enrollment", "wait_for_enrollment", "list_events"} {
		def, _ := r.Get(tool)
		for _, p := range def.Params {
			if p.Name == "external_id" && !p.Identifier {
				t.Errorf("%s: external_id is placed in a path but is not marked Identifier", tool)
			}
		}
	}
	for _, tool := range []string{"get_terminal", "start_enrollment", "grant_access", "list_events"} {
		def, _ := r.Get(tool)
		for _, p := range def.Params {
			if p.Name == "serial" && !p.Identifier {
				t.Errorf("%s: serial is not marked Identifier", tool)
			}
		}
	}
	get, _ := r.Get("get_person")
	for _, bad := range []string{"P-1/permissions", "P-1%2Fpermissions", "../company", `P\1`, "P-1?x=1", "P-1#f", "P 1", "P\n1"} {
		raw, _ := json.Marshal(map[string]any{"external_id": bad})
		if _, err := get.ValidateArgs(raw); err == nil {
			t.Errorf("get_person accepted external_id %q", bad)
		}
	}
	for _, ok := range []string{"P-1", "EMP_0042", "badge.7", "AT-E05A1B38AA38", "a7f3c2e1-0000-4000-8000-000000000000"} {
		raw, _ := json.Marshal(map[string]any{"external_id": ok})
		if _, err := get.ValidateArgs(raw); err != nil {
			t.Errorf("get_person refused external_id %q: %v", ok, err)
		}
	}
	revoke, _ := r.Get("revoke_access")
	if _, err := revoke.ValidateArgs(json.RawMessage(`{"external_id":"P-1","rule_id":"x/../../people/P-2"}`)); err == nil {
		t.Errorf("revoke_access accepted a rule id with slashes")
	}
}
