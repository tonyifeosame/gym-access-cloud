package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// The registry's own guarantees: the model's arguments are checked, the tool
// list is fixed and role-filtered, and nothing secret-shaped passes the
// result guard.

func TestPhase1CatalogueIsExactlyWhatWasApproved(t *testing.T) {
	r := NewRegistry()
	registerPhase1Tools(r)
	want := []string{
		"create_person", "get_fleet_summary", "get_person", "get_person_enrollment", "get_site",
		"get_terminal", "grant_access", "list_events", "list_schedules", "list_sites",
		"list_terminals", "revoke_access", "search_people", "start_enrollment", "wait_for_enrollment",
	}
	got := r.Names(models.RoleOwner)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalogue = %v\nwant       %v", got, want)
	}
	// Consequential tools all confirm; read tools never do.
	for _, name := range []string{"grant_access", "revoke_access", "start_enrollment"} {
		if tool, _ := r.Get(name); tool.Confirm == nil {
			t.Errorf("%s does not require confirmation", name)
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
	registerPhase1Tools(r)
	viewer := r.Names(models.RoleViewer)
	for _, hidden := range []string{"create_person", "grant_access", "revoke_access", "start_enrollment"} {
		for _, name := range viewer {
			if name == hidden {
				t.Errorf("VIEWER sees %s", hidden)
			}
		}
	}
	manager := r.Names(models.RoleManager)
	owner := r.Names(models.RoleOwner)
	if strings.Join(manager, ",") != strings.Join(owner, ",") {
		t.Fatalf("MANAGER and OWNER see different Phase 1 tools: %v vs %v", manager, owner)
	}
	if again := r.Names(models.RoleOwner); strings.Join(again, ",") != strings.Join(owner, ",") {
		t.Fatalf("tool order is not stable between calls")
	}
}

func TestValidateArgsRefusesWhatTheSchemaDoesNotAllow(t *testing.T) {
	r := NewRegistry()
	registerPhase1Tools(r)
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
	registerPhase1Tools(r)
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
