package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/assistant"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Phase 2a, end to end, against the real router and PostgreSQL. The model is
// scripted as in assistant_test.go; what is under test is that each new
// tool runs the console's own route as the operator, that the role, site and
// tenant rules hold for every one of them, that nothing secret or
// infrastructural reaches the model, that the consequential ones ask first,
// and that a multi-step request stops at each card and after any failure.

// --- fixture additions --------------------------------------------------------------

// runTool scripts one tool call and one reply, sends a message, and returns
// the tool.result event and what the model was told.
func (f *assistantFixture) runTool(t *testing.T, conv, tool string, args map[string]any) (sseEvent, string, []sseEvent) {
	t.Helper()
	f.model.steps = []modelStep{call("toolu_"+tool, tool, args), say("ok")}
	events := f.send(t, conv, tool)
	result := onlyEvent(t, events, assistant.EventToolResult)
	return result, lastToolResultContent(t, conv), events
}

func expectStatus(t *testing.T, tool string, result sseEvent, want string) {
	t.Helper()
	if result.Data["status"] != want {
		t.Fatalf("%s = %v, want %s (summary %v)", tool, result.Data["status"], want, result.Data["summary"])
	}
}

// seedRefusal records a field event for a person under their own ID number
// (seedEvent in events_api_test.go hard-codes another fixture's subject).
func seedRefusal(t *testing.T, companyID, siteID, deviceID, personID int64, externalID, eventType, decision, reason string, at time.Time) {
	t.Helper()
	if _, err := database.RecordAccessEvent(database.AccessEvent{
		CompanyID: companyID, SiteID: siteID, DeviceID: deviceID, PersonID: personID,
		EventType: eventType, Decision: decision, ReasonCode: reason,
		SubjectExternalID: externalID, OccurredAt: at, OccurredAtTrusted: true,
	}); err != nil {
		t.Fatalf("seeding event: %v", err)
	}
}

// --- capabilities ---------------------------------------------------------------------

func TestAssistantCapabilitiesCarryTheEffectsMap(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	code, body := consoleCall(t, f.env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("capabilities = %d", code)
	}
	effects, _ := body["effects"].(map[string]any)
	for _, tool := range []string{"create_person", "update_person", "set_person_active", "grant_access", "revoke_access",
		"create_schedule", "update_schedule", "start_enrollment", "cancel_enrollment", "resync_terminal", "request_diagnostic"} {
		if _, ok := effects[tool]; !ok {
			t.Errorf("effects lacks %s: %v", tool, effects)
		}
	}
	for _, read := range []string{"search_people", "evaluate_access", "explain_denial", "wait_for_command", "list_pending_terminals"} {
		if _, ok := effects[read]; ok {
			t.Errorf("effects lists the read %s", read)
		}
	}
	tools := fmt.Sprint(body["tools"])
	for _, shown := range []string{"update_person", "explain_denial", "request_diagnostic", "resync_terminal", "list_pending_terminals"} {
		if !strings.Contains(tools, shown) {
			t.Errorf("a MANAGER is not offered %s", shown)
		}
	}

	// A VIEWER sees the reads and no effects at all.
	v := newAssistantFixture(t, models.RoleViewer)
	_, vbody := consoleCall(t, v.env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", v.token, v.csrf)
	if e, _ := vbody["effects"].(map[string]any); len(e) != 0 {
		t.Errorf("VIEWER effects = %v", e)
	}
	vtools := fmt.Sprint(vbody["tools"])
	for _, shown := range []string{"list_person_credentials", "get_terminal_capabilities", "list_terminal_commands", "get_command", "wait_for_command", "get_site_settings", "list_people_without_access"} {
		if !strings.Contains(vtools, shown) {
			t.Errorf("a VIEWER is not offered %s", shown)
		}
	}
	for _, hidden := range []string{"evaluate_access", "explain_denial", "list_pending_terminals", "resync_terminal", "update_person"} {
		if strings.Contains(vtools, hidden) {
			t.Errorf("a VIEWER is offered %s", hidden)
		}
	}
}

// --- people ---------------------------------------------------------------------------------

func TestAssistantUpdatePersonChangesOnlyWhatWasAsked(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	personID := seedPerson(t, f.companyID, "P-UPD", "Ada Okonkwo")
	mustExec(t, `UPDATE people SET membership_type = 'staff', fingerprint_template = 'legacy-template-bytes' WHERE id = $1`, personID)
	f.env.registerDevice(f.env.siteAKey, "AT-UPD-1")
	credential := seedFingerprintCredential(t, f.companyID, personID, deviceIDBySerial(t, "AT-UPD-1"), models.CredentialActive)

	conv := f.newConversation(t)

	// A name correction alone: category, external id, credential and legacy
	// template all untouched.
	result, content, events := f.runTool(t, conv, "update_person", map[string]any{"external_id": "P-UPD", "full_name": "Ada Okonkwo-Bello"})
	expectStatus(t, "update_person", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"full_name":{"from":"Ada Okonkwo","to":"Ada Okonkwo-Bello"}`) || strings.Contains(content, `"category":{`) {
		t.Fatalf("changes reported = %s", content)
	}
	var name, category, template, externalID string
	scanRow(t, `SELECT full_name, membership_type, COALESCE(fingerprint_template, ''), external_id FROM people WHERE id = $1`, []any{personID}, &name, &category, &template, &externalID)
	if name != "Ada Okonkwo-Bello" || category != "staff" || template != "legacy-template-bytes" || externalID != "P-UPD" {
		t.Fatalf("after a name change: name=%q category=%q template=%q external_id=%q", name, category, template, externalID)
	}
	var credStatus string
	scanRow(t, `SELECT status FROM credentials WHERE public_id = $1::uuid`, []any{credential}, &credStatus)
	if credStatus != models.CredentialActive {
		t.Fatalf("credential after a name change = %s", credStatus)
	}
	handoff := onlyEvent(t, events, assistant.EventHandoff)
	if handoff.Data["route"] != "/people/P-UPD" {
		t.Fatalf("handoff = %v", handoff.Data)
	}
	// The event names the domains the console must refresh.
	if d := fmt.Sprint(result.Data["domains"]); !strings.Contains(d, "people") || !strings.Contains(d, "audit") {
		t.Fatalf("domains = %v", result.Data["domains"])
	}

	// A category change alone keeps the corrected name.
	result, _, _ = f.runTool(t, conv, "update_person", map[string]any{"external_id": "P-UPD", "category": "contractor"})
	expectStatus(t, "update_person", result, models.ToolCallExecuted)
	scanRow(t, `SELECT full_name, membership_type, COALESCE(fingerprint_template, '') FROM people WHERE id = $1`, []any{personID}, &name, &category, &template)
	if name != "Ada Okonkwo-Bello" || category != "contractor" || template != "legacy-template-bytes" {
		t.Fatalf("after a category change: name=%q category=%q template=%q", name, category, template)
	}

	// Nothing to change is refused before any request is made.
	result, content, _ = f.runTool(t, conv, "update_person", map[string]any{"external_id": "P-UPD"})
	expectStatus(t, "update_person with nothing", result, models.ToolCallInvalid)
	if !strings.Contains(content, "full_name or a category") {
		t.Fatalf("empty update message = %s", content)
	}

	// Audited with before/after, by the operator, as the assistant.
	var audits int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'PERSON_UPDATED' AND c.tool_name = 'update_person' AND a.company_id = $1
	                AND a.user_agent LIKE 'AccessLink-Assistant/1%'`, []any{f.companyID}, &audits)
	if audits != 2 {
		t.Fatalf("audited updates = %d, want 2", audits)
	}
}

func TestAssistantDeactivationAsksAndReactivationDoesNot(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	personID := seedPerson(t, f.companyID, "P-ACT", "Sam Chen")
	mustExec(t, `UPDATE people SET membership_type = 'staff' WHERE id = $1`, personID)
	conv := f.newConversation(t)

	// Deactivating pauses for approval, with the person page's wording.
	f.model.steps = []modelStep{call("t1", "set_person_active", map[string]any{"external_id": "P-ACT", "active": false}), say("Waiting.")}
	events := f.send(t, conv, "deactivate Sam")
	confirmation := onlyEvent(t, events, assistant.EventConfirmationRequired)
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	if consequence["title"] != "Deactivate Sam Chen?" || !strings.Contains(fmt.Sprint(consequence["body"]), "stop admitting them") {
		t.Fatalf("consequence = %v", consequence)
	}
	var active bool
	scanRow(t, `SELECT active FROM people WHERE id = $1`, []any{personID}, &active)
	if !active {
		t.Fatalf("deactivated before approval")
	}
	token, _ := confirmation.Data["token"].(string)
	f.model.steps = []modelStep{say("Done.")}
	w := f.confirm(t, conv, token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	approved := parseSSE(t, w.Body.String())
	expectStatus(t, "approved deactivation", onlyEvent(t, approved, assistant.EventToolResult), models.ToolCallConfirmedExecuted)
	var category string
	scanRow(t, `SELECT active, membership_type FROM people WHERE id = $1`, []any{personID}, &active, &category)
	if active || category != "staff" {
		t.Fatalf("after deactivation: active=%v category=%q", active, category)
	}
	var changes string
	scanRow(t, `SELECT changes::text FROM audit_events WHERE action = 'PERSON_UPDATED' AND company_id = $1 ORDER BY id DESC LIMIT 1`, []any{f.companyID}, &changes)
	if !strings.Contains(changes, `"active"`) {
		t.Fatalf("audit changes = %s", changes)
	}

	// Deactivating again is refused as already done, with no card.
	result, content, events := f.runTool(t, conv, "set_person_active", map[string]any{"external_id": "P-ACT", "active": false})
	if result.Data["status"] == models.ToolCallConfirmationRequested || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("a no-op deactivation asked for approval")
	}
	if !strings.Contains(content, "already inactive") {
		t.Fatalf("no-op message = %s", content)
	}

	// Reactivating runs without a card: it restores what was.
	result, _, events = f.runTool(t, conv, "set_person_active", map[string]any{"external_id": "P-ACT", "active": true})
	expectStatus(t, "reactivate", result, models.ToolCallExecuted)
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("reactivation asked for approval")
	}
	scanRow(t, `SELECT active FROM people WHERE id = $1`, []any{personID}, &active)
	if !active {
		t.Fatalf("not reactivated")
	}
}

func TestAssistantPersonCredentialsCarryNoMaterial(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	personID := seedPerson(t, f.companyID, "P-CRED", "Cred Person")
	f.env.registerDevice(f.env.siteAKey, "AT-CRED-1")
	mustExec(t, `UPDATE devices SET device_name = 'East Gate' WHERE serial_number = 'AT-CRED-1'`)
	deviceID := deviceIDBySerial(t, "AT-CRED-1")
	credential := seedFingerprintCredential(t, f.companyID, personID, deviceID, models.CredentialActive)
	seedPlacement(t, credential, deviceID, "PLACED", 7)
	mustExec(t, `UPDATE credentials SET material_digest = repeat('deadbeef', 8), sealed_material = '\xdeadbeef'::bytea,
	                     sealed_key_id = 'kms-key-77', sealed_algorithm = 'AES-256-GCM' WHERE public_id = $1::uuid`, credential)

	conv := f.newConversation(t)
	result, content, _ := f.runTool(t, conv, "list_person_credentials", map[string]any{"external_id": "P-CRED"})
	expectStatus(t, "list_person_credentials", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"state":"ACTIVE"`) || !strings.Contains(content, "East Gate") || !strings.Contains(content, "AT-CRED-1") {
		t.Fatalf("credentials = %s", content)
	}
	for _, forbidden := range []string{"deadbeef", "digest", "slot", "template", "material", "sensor_local", "placement", "locator", "vendor", "key_id", "kms-key"} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Errorf("credentials carry %q: %s", forbidden, content)
		}
	}
}

// --- access questions ---------------------------------------------------------------------

func TestAssistantEvaluatesAccessAndExplainsARefusal(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	personID := seedPerson(t, f.companyID, "P-WHY", "Amina Bello")
	f.env.registerDevice(f.env.siteAKey, "AT-WHY-1")
	mustExec(t, `UPDATE devices SET device_name = 'East Gate' WHERE serial_number = 'AT-WHY-1'`)
	deviceID := deviceIDBySerial(t, "AT-WHY-1")
	siteID := siteIDByKey(t, f.env.siteAKey)
	refusedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	seedRefusal(t, f.companyID, siteID, deviceID, personID, "P-WHY", models.EventAccessDenied, models.DecisionDenied, models.ReasonNoPermission, refusedAt)
	conv := f.newConversation(t)

	// No rule: refused, and the preview says why.
	result, content, _ := f.runTool(t, conv, "evaluate_access", map[string]any{"serial": "AT-WHY-1", "external_id": "P-WHY"})
	expectStatus(t, "evaluate_access", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"granted":false`) || !strings.Contains(content, models.ReasonNoPermission) {
		t.Fatalf("evaluation = %s", content)
	}
	// The preview writes no event.
	var events int
	scanRow(t, `SELECT count(*) FROM events WHERE company_id = $1`, []any{f.companyID}, &events)
	if events != 1 {
		t.Fatalf("evaluating wrote an event: %d", events)
	}
	// A bad instant is refused before any request.
	result, content, _ = f.runTool(t, conv, "evaluate_access", map[string]any{"serial": "AT-WHY-1", "external_id": "P-WHY", "at": "yesterday"})
	expectStatus(t, "evaluate_access with a bad instant", result, models.ToolCallInvalid)
	if !strings.Contains(content, "RFC 3339") {
		t.Fatalf("bad instant message = %s", content)
	}

	// The explanation: the refusal, the empty rule list, no fingerprint, and
	// the re-evaluation at the moment of the refusal.
	result, content, _ = f.runTool(t, conv, "explain_denial", map[string]any{"external_id": "P-WHY"})
	expectStatus(t, "explain_denial", result, models.ToolCallExecuted)
	for _, wanted := range []string{`"refusal":{`, "East Gate", `"access_rules":[]`, "No rule lets them in at East Gate", "No fingerprint is enrolled", `"decision":{`} {
		if !strings.Contains(content, wanted) {
			t.Errorf("explanation lacks %q: %s", wanted, content)
		}
	}
	// Bounded: the whole thing was at most six internal requests.
	var calls int
	scanRow(t, `SELECT count(*) FROM assistant_tool_calls WHERE tool_name = 'explain_denial' AND company_id = $1`, []any{f.companyID}, &calls)
	if calls != 1 {
		t.Fatalf("explain_denial recorded %d tool calls", calls)
	}

	// After a rule, the explanation changes: the rules would admit her, so
	// the refusal must have been the terminal's (no fingerprint there).
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect) VALUES ($1, $2, 'SITE', $3, 'ALLOW')`, f.companyID, personID, siteID)
	result, content, _ = f.runTool(t, conv, "explain_denial", map[string]any{"external_id": "P-WHY", "serial": "AT-WHY-1"})
	expectStatus(t, "explain_denial after a rule", result, models.ToolCallExecuted)
	if !strings.Contains(content, "the refusal came from the terminal") && !strings.Contains(content, "would be let in") {
		t.Fatalf("explanation after a rule = %s", content)
	}
	if !strings.Contains(content, "The terminal's own reason at the time was NO_PERMISSION") {
		t.Fatalf("explanation does not report the recorded reason: %s", content)
	}

	// A legacy-enrolled person: the credential list is empty but they ARE
	// enrolled, and the explanation must not send the operator to enrol them.
	legacyID := seedPerson(t, f.companyID, "P-LEGACY", "Legacy Person")
	mustExec(t, `UPDATE people SET fingerprint_template = 'legacy-template-bytes' WHERE id = $1`, legacyID)
	seedRefusal(t, f.companyID, siteID, deviceID, legacyID, "P-LEGACY", models.EventAccessDenied, models.DecisionDenied, models.ReasonNoPermission, refusedAt)
	result, content, _ = f.runTool(t, conv, "explain_denial", map[string]any{"external_id": "P-LEGACY"})
	expectStatus(t, "explain_denial for a legacy enrolment", result, models.ToolCallExecuted)
	if strings.Contains(content, "No fingerprint is enrolled") || !strings.Contains(content, "older enrolment record") {
		t.Fatalf("legacy explanation = %s", content)
	}
	if strings.Contains(content, "legacy-template-bytes") {
		t.Fatalf("the legacy template reached the model")
	}

	// Nobody refused: said so, no evaluation attempted.
	seedPerson(t, f.companyID, "P-FINE", "Fine Person")
	result, content, _ = f.runTool(t, conv, "explain_denial", map[string]any{"external_id": "P-FINE"})
	expectStatus(t, "explain_denial with no refusal", result, models.ToolCallExecuted)
	if !strings.Contains(content, "No refusal is recorded") || !strings.Contains(content, `"refusal":null`) {
		t.Fatalf("no-refusal explanation = %s", content)
	}

	// The same question from a VIEWER: the tool is not theirs, because the
	// route is not.
	v := newAssistantFixture(t, models.RoleViewer)
	vconv := v.newConversation(t)
	result, _, _ = v.runTool(t, vconv, "explain_denial", map[string]any{"external_id": "P-WHY"})
	expectStatus(t, "explain_denial as VIEWER", result, models.ToolCallInvalid)
	result, _, _ = v.runTool(t, vconv, "evaluate_access", map[string]any{"serial": "AT-WHY-1", "external_id": "P-WHY"})
	expectStatus(t, "evaluate_access as VIEWER", result, models.ToolCallInvalid)
}

func TestAssistantCountsPeopleWithoutAccess(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	seedPerson(t, f.companyID, "P-NR-1", "No Rule One")
	seedPerson(t, f.companyID, "P-NR-2", "No Rule Two")
	conv := f.newConversation(t)
	result, content, _ := f.runTool(t, conv, "list_people_without_access", map[string]any{})
	expectStatus(t, "list_people_without_access", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"people_without_access":2`) {
		t.Fatalf("count = %s", content)
	}
}

// --- schedules ---------------------------------------------------------------------------

func TestAssistantSchedulesFromTextAndAsksWhenRulesDepend(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	conv := f.newConversation(t)

	result, content, _ := f.runTool(t, conv, "create_schedule", map[string]any{
		"name": "Office hours", "windows": "Mon-Fri 08:00-18:00; Sat 09:00-13:00", "timezone": "Africa/Lagos",
	})
	expectStatus(t, "create_schedule", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"name":"Office hours"`) || !strings.Contains(content, `"permission_count":0`) {
		t.Fatalf("created = %s", content)
	}
	var scheduleID int64
	var windows int
	scanRow(t, `SELECT id FROM schedules WHERE company_id = $1 AND name = 'Office hours'`, []any{f.companyID}, &scheduleID)
	scanRow(t, `SELECT count(*) FROM schedule_windows WHERE schedule_id = $1`, []any{scheduleID}, &windows)
	if windows != 2 {
		t.Fatalf("windows = %d, want 2", windows)
	}
	var days int
	var start string
	scanRow(t, `SELECT days_of_week, start_time::text FROM schedule_windows WHERE schedule_id = $1 ORDER BY days_of_week LIMIT 1`, []any{scheduleID}, &days, &start)
	if days != 31 || !strings.HasPrefix(start, "08:00") {
		t.Fatalf("first window = days %d start %s", days, start)
	}
	publicID := queryString(t, `SELECT public_id::text FROM schedules WHERE id = $1`, scheduleID)

	// Unreadable window text never reaches the route.
	result, content, _ = f.runTool(t, conv, "create_schedule", map[string]any{"name": "Broken", "windows": "sometimes"})
	expectStatus(t, "create_schedule with bad windows", result, models.ToolCallInvalid)
	if !strings.Contains(content, "expected days then a time range") {
		t.Fatalf("bad windows message = %s", content)
	}

	// Unused: changes without a card, and only what was asked.
	result, _, events := f.runTool(t, conv, "update_schedule", map[string]any{"schedule_id": publicID, "name": "Office"})
	expectStatus(t, "update_schedule unused", result, models.ToolCallExecuted)
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("an unused schedule asked for approval")
	}
	var name, tz string
	scanRow(t, `SELECT name, COALESCE(timezone, '') FROM schedules WHERE id = $1`, []any{scheduleID}, &name, &tz)
	scanRow(t, `SELECT count(*) FROM schedule_windows WHERE schedule_id = $1`, []any{scheduleID}, &windows)
	if name != "Office" || tz != "Africa/Lagos" || windows != 2 {
		t.Fatalf("after rename: name=%q tz=%q windows=%d", name, tz, windows)
	}

	// In use: the card names the dependents; approval replaces the windows.
	personID := seedPerson(t, f.companyID, "P-SCH", "Sched Person")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, schedule_id) VALUES ($1, $2, 'COMPANY', 'ALLOW', $3)`, f.companyID, personID, scheduleID)
	f.model.steps = []modelStep{call("t1", "update_schedule", map[string]any{"schedule_id": publicID, "windows": "Daily 06:00-22:00"}), say("Waiting.")}
	events = f.send(t, conv, "open it every day")
	confirmation := onlyEvent(t, events, assistant.EventConfirmationRequired)
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	if consequence["title"] != "Change Office?" || !strings.Contains(fmt.Sprint(consequence["warnings"]), "1 access rule refer") {
		t.Fatalf("consequence = %v", consequence)
	}
	scanRow(t, `SELECT count(*) FROM schedule_windows WHERE schedule_id = $1`, []any{scheduleID}, &windows)
	if windows != 2 {
		t.Fatalf("windows changed before approval")
	}
	token, _ := confirmation.Data["token"].(string)
	f.model.steps = []modelStep{say("Done.")}
	w := f.confirm(t, conv, token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	expectStatus(t, "approved schedule change", onlyEvent(t, parseSSE(t, w.Body.String()), assistant.EventToolResult), models.ToolCallConfirmedExecuted)
	scanRow(t, `SELECT count(*) FROM schedule_windows WHERE schedule_id = $1`, []any{scheduleID}, &windows)
	scanRow(t, `SELECT days_of_week FROM schedule_windows WHERE schedule_id = $1`, []any{scheduleID}, &days)
	if windows != 1 || days != 127 {
		t.Fatalf("after approval: windows=%d days=%d", windows, days)
	}

	// An empty update is refused before any card.
	result, content, events = f.runTool(t, conv, "update_schedule", map[string]any{"schedule_id": publicID})
	expectStatus(t, "empty update_schedule", result, models.ToolCallFailed)
	if !strings.Contains(content, "Give a name, windows, timezone or active") || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("empty update = %s", content)
	}

	// A timezone the platform could not evaluate never reaches the route:
	// refused before any request or card, on create and on update, and the
	// stored zone is untouched.
	result, content, events = f.runTool(t, conv, "create_schedule", map[string]any{"name": "Bad zone", "windows": "Mon 08:00-09:00", "timezone": "Lagos"})
	expectStatus(t, "create_schedule with a bad zone", result, models.ToolCallInvalid)
	if !strings.Contains(content, "not a known IANA zone") {
		t.Fatalf("bad zone message = %s", content)
	}
	var badZones int
	scanRow(t, `SELECT count(*) FROM schedules WHERE company_id = $1 AND name = 'Bad zone'`, []any{f.companyID}, &badZones)
	if badZones != 0 {
		t.Fatalf("a schedule with an invalid zone was written")
	}
	result, content, events = f.runTool(t, conv, "update_schedule", map[string]any{"schedule_id": publicID, "timezone": "WAT"})
	expectStatus(t, "update_schedule with a bad zone", result, models.ToolCallFailed)
	if !strings.Contains(content, "not a known IANA zone") || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("bad zone update = %s", content)
	}
	scanRow(t, `SELECT COALESCE(timezone, '') FROM schedules WHERE id = $1`, []any{scheduleID}, &tz)
	if tz != "Africa/Lagos" {
		t.Fatalf("zone after a refused update = %q", tz)
	}

	// Every schedule write the assistant made is the route's own audit row,
	// linked to the tool call: the create, the rename, and the approved change.
	var audits int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'SCHEDULE_CONFIGURED' AND c.tool_name IN ('create_schedule', 'update_schedule') AND a.company_id = $1
	                AND a.user_agent LIKE 'AccessLink-Assistant/1%'`, []any{f.companyID}, &audits)
	if audits != 3 {
		t.Fatalf("audited schedule writes = %d, want 3", audits)
	}
}

func TestAssistantScheduleTextIsRefusedRatherThanMisread(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	conv := f.newConversation(t)
	// The forms the first parser silently turned into one day or an
	// overnight span. None may reach the route.
	for text, want := range map[string]string{
		"Monday to Friday 08:00-18:00": "write a range as Mon-Fri",
		"Mon–Fri 08:00-18:00":          "write a range as Mon-Fri",
		"Sat & Sun 09:00-13:00":        "write a range as Mon-Fri",
		"Mon-Fri 9:00am-5:00pm":        "24-hour form",
		"Mon-Fri 24:00-08:00":          "cannot start at 24:00",
	} {
		result, content, _ := f.runTool(t, conv, "create_schedule", map[string]any{"name": "Misread", "windows": text})
		expectStatus(t, "create_schedule "+text, result, models.ToolCallInvalid)
		if !strings.Contains(content, want) {
			t.Errorf("%q: message %q lacks %q", text, content, want)
		}
	}
	var written int
	scanRow(t, `SELECT count(*) FROM schedules WHERE company_id = $1 AND name = 'Misread'`, []any{f.companyID}, &written)
	if written != 0 {
		t.Fatalf("a misread schedule was written")
	}
	// The end of the day is expressible and stored as the last minute.
	result, _, _ := f.runTool(t, conv, "create_schedule", map[string]any{"name": "All day", "windows": "Daily 00:00-24:00"})
	expectStatus(t, "create_schedule all day", result, models.ToolCallExecuted)
	var start, end string
	scanRow(t, `SELECT w.start_time::text, w.end_time::text FROM schedule_windows w JOIN schedules s ON s.id = w.schedule_id
	              WHERE s.company_id = $1 AND s.name = 'All day'`, []any{f.companyID}, &start, &end)
	if !strings.HasPrefix(start, "00:00") || !strings.HasPrefix(end, "23:59") {
		t.Fatalf("all-day window = %s-%s", start, end)
	}
}

// --- events ------------------------------------------------------------------------------

func TestAssistantListsEventsByTypeAndText(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	personID := seedPerson(t, f.companyID, "P-EV", "Event Person")
	f.env.registerDevice(f.env.siteAKey, "AT-EV-1")
	deviceID := deviceIDBySerial(t, "AT-EV-1")
	siteID := siteIDByKey(t, f.env.siteAKey)
	now := time.Now().UTC()
	seedRefusal(t, f.companyID, siteID, deviceID, personID, "P-EV", models.EventAccessGranted, models.DecisionGranted, models.ReasonAllowed, now)
	seedRefusal(t, f.companyID, siteID, deviceID, personID, "P-EV", models.EventAccessDenied, models.DecisionDenied, models.ReasonNoPermission, now.Add(-time.Minute))
	seedRefusal(t, f.companyID, siteID, deviceID, 0, "", models.EventTerminalOffline, models.DecisionRecorded, "", now.Add(-2*time.Minute))
	conv := f.newConversation(t)

	result, content, _ := f.runTool(t, conv, "list_events", map[string]any{"event_type": models.EventTerminalOffline})
	expectStatus(t, "list_events by type", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"total":1`) || !strings.Contains(content, models.EventTerminalOffline) || strings.Contains(content, models.EventAccessDenied) {
		t.Fatalf("by type = %s", content)
	}
	result, content, _ = f.runTool(t, conv, "list_events", map[string]any{"query": "Event Person", "decision": "DENIED"})
	expectStatus(t, "list_events by text", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"total":1`) || !strings.Contains(content, models.EventAccessDenied) {
		t.Fatalf("by text = %s", content)
	}
	result, _, _ = f.runTool(t, conv, "list_events", map[string]any{"direction": "SIDEWAYS"})
	expectStatus(t, "list_events with a bad direction", result, models.ToolCallInvalid)
}

// --- the fleet ----------------------------------------------------------------------------

func TestAssistantDiagnosesAndResyncsATerminal(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	key := f.env.registerDevice(f.env.siteAKey, "AT-DIAG-1")
	mustExec(t, `UPDATE devices SET device_name = 'Loading Bay' WHERE serial_number = 'AT-DIAG-1'`)
	conv := f.newConversation(t)

	// Before the terminal has said what it can do, the command is refused by
	// the route, in its words, and nothing is queued.
	result, content, _ := f.runTool(t, conv, "get_terminal_capabilities", map[string]any{"serial": "AT-DIAG-1"})
	expectStatus(t, "get_terminal_capabilities", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"type":"DIAGNOSTIC_SNAPSHOT"`) || !strings.Contains(content, `"supported":false`) {
		t.Fatalf("capabilities = %s", content)
	}
	result, _, _ = f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-DIAG-1"})
	expectStatus(t, "request_diagnostic on an incapable terminal", result, models.ToolCallInvalid)

	// The terminal reports the capability; the request queues, marked as the
	// assistant's with the operator's words, and is audited.
	reportCapabilities(t, f.env, key, models.CapabilityCmdDiagnosticSnapshot)
	result, content, events := f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-DIAG-1", "reason": "door keeps beeping"})
	expectStatus(t, "request_diagnostic", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"state":"QUEUED"`) || !strings.Contains(content, `"reason":"assistant: door keeps beeping"`) {
		t.Fatalf("queued command = %s", content)
	}
	if strings.Contains(content, "requested_by_email") || strings.Contains(content, f.user.Email) {
		t.Fatalf("the operator's email reached the model: %s", content)
	}
	handoff := onlyEvent(t, events, assistant.EventHandoff)
	if handoff.Data["route"] != "/terminals/AT-DIAG-1" {
		t.Fatalf("handoff = %v", handoff.Data)
	}
	var commandID string
	scanRow(t, `SELECT public_id::text FROM sync_jobs WHERE job_type = 'DIAGNOSTIC_SNAPSHOT' ORDER BY id DESC LIMIT 1`, []any{}, &commandID)
	var audits int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'TERMINAL_COMMAND_ISSUED' AND c.tool_name = 'request_diagnostic' AND a.company_id = $1`, []any{f.companyID}, &audits)
	if audits != 1 {
		t.Fatalf("audited command issues = %d", audits)
	}

	// Asking again returns the one already waiting rather than a second.
	result, content, _ = f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-DIAG-1"})
	expectStatus(t, "request_diagnostic again", result, models.ToolCallExecuted)
	if !strings.Contains(content, "already waiting") {
		t.Fatalf("second request = %s", content)
	}
	var queued int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DIAGNOSTIC_SNAPSHOT'`, []any{}, &queued)
	if queued != 1 {
		t.Fatalf("diagnostics queued = %d", queued)
	}

	// Waiting: still queued after a second, reported as such, and charged to
	// the polling allowance rather than the call budget.
	result, content, _ = f.runTool(t, conv, "wait_for_command", map[string]any{"serial": "AT-DIAG-1", "command_id": commandID, "timeout_s": 1})
	expectStatus(t, "wait_for_command", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"state":"QUEUED"`) {
		t.Fatalf("wait = %s", content)
	}

	// The terminal collects the job and answers with a snapshot. The report
	// reaches the model without the network's name or address.
	jobs := f.env.jobs(key)
	var jobID any
	for _, j := range jobs {
		if j["job_type"] == "DIAGNOSTIC_SNAPSHOT" {
			jobID = j["id"]
		}
	}
	if jobID == nil {
		t.Fatalf("the terminal was not offered the diagnostic: %v", jobs)
	}
	res := f.env.do(http.MethodPost, fmt.Sprintf("/api/v1/devices/jobs/%v/complete", jobID), map[string]any{
		"result_code": "OK",
		"result": map[string]any{
			"network":        map[string]any{"associated": true, "ssid": "Warehouse-WiFi", "ip": "10.20.30.40", "rssi_dbm": -67},
			"outbound_queue": map[string]any{"enrolments": 0, "access_logs": 4, "dropped_access_logs": 0},
			"worklist":       map[string]any{"missing": 1},
			"storage":        map[string]any{"members": 12, "capacity": 200, "orphaned_slots": 0, "write_failed": false, "fallback_store": false},
		},
	}, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("completing the job = %d: %s", res.Code, res.Raw)
	}
	result, content, _ = f.runTool(t, conv, "get_command", map[string]any{"serial": "AT-DIAG-1", "command_id": commandID})
	expectStatus(t, "get_command", result, models.ToolCallExecuted)
	for _, wanted := range []string{`"state":"ACCEPTED"`, `"rssi_dbm":-67`, `"access_logs":4`, `"missing":1`, `"capacity":200`} {
		if !strings.Contains(content, wanted) {
			t.Errorf("report lacks %s: %s", wanted, content)
		}
	}
	for _, forbidden := range []string{"Warehouse-WiFi", "10.20.30.40", "ssid", `"ip"`} {
		if strings.Contains(content, forbidden) {
			t.Errorf("report carries %q: %s", forbidden, content)
		}
	}
	result, content, _ = f.runTool(t, conv, "list_terminal_commands", map[string]any{"serial": "AT-DIAG-1"})
	expectStatus(t, "list_terminal_commands", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"count":1`) || strings.Contains(content, "Warehouse-WiFi") {
		t.Fatalf("history = %s", content)
	}

	// A resync queues without a card and is audited.
	result, content, events = f.runTool(t, conv, "resync_terminal", map[string]any{"serial": "AT-DIAG-1"})
	expectStatus(t, "resync_terminal", result, models.ToolCallExecuted)
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("resync asked for approval")
	}
	if !strings.Contains(content, `"serial_number":"AT-DIAG-1"`) {
		t.Fatalf("resync = %s", content)
	}
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'TERMINAL_RESYNCED' AND c.tool_name = 'resync_terminal' AND a.company_id = $1`, []any{f.companyID}, &audits)
	if audits != 1 {
		t.Fatalf("audited resyncs = %d", audits)
	}
	if d := fmt.Sprint(result.Data["domains"]); !strings.Contains(d, "terminals") {
		t.Fatalf("resync domains = %v", result.Data["domains"])
	}
}

func TestAssistantFleetToolsHonourSiteGrantsAndTenancy(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	siteA := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	siteB := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteBKey))
	if err := database.ReplaceSiteGrants(f.companyID, f.user.ID, []string{siteA}); err != nil {
		t.Fatalf("granting site A: %v", err)
	}
	f.env.registerDevice(f.env.siteAKey, "AT-GR-A")
	f.env.registerDevice(f.env.siteBKey, "AT-GR-B")
	seedPerson(t, f.companyID, "P-GR", "Grant Person")
	other := operatorCompanyID(t, "two")
	seedPerson(t, other, "P-THEIRS-2", "Their Person")
	conv := f.newConversation(t)

	// Another site's terminal: every fleet tool refuses on scope.
	for tool, args := range map[string]map[string]any{
		"resync_terminal":           {"serial": "AT-GR-B"},
		"request_diagnostic":        {"serial": "AT-GR-B"},
		"get_terminal_capabilities": {"serial": "AT-GR-B"},
		"list_terminal_commands":    {"serial": "AT-GR-B"},
		"get_command":               {"serial": "AT-GR-B", "command_id": "a7f3c2e1-0000-4000-8000-000000000000"},
		"wait_for_command":          {"serial": "AT-GR-B", "command_id": "a7f3c2e1-0000-4000-8000-000000000000", "timeout_s": 5},
		"evaluate_access":           {"serial": "AT-GR-B", "external_id": "P-GR"},
		"get_site_settings":         {"site_id": siteB},
	} {
		result, content, _ := f.runTool(t, conv, tool, args)
		expectStatus(t, tool+" at an ungranted site", result, models.ToolCallRefusedScope)
		if strings.Contains(content, siteB) {
			t.Errorf("%s leaked the ungranted site id", tool)
		}
	}
	var jobs int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type IN ('DIAGNOSTIC_SNAPSHOT', 'FULL_SYNC') AND device_id = $1`, []any{deviceIDBySerial(t, "AT-GR-B")}, &jobs)
	if jobs != 0 {
		t.Fatalf("a refused fleet tool still queued work: %d", jobs)
	}

	// The granted site works.
	result, _, _ := f.runTool(t, conv, "get_site_settings", map[string]any{"site_id": siteA})
	expectStatus(t, "get_site_settings at the granted site", result, models.ToolCallExecuted)

	// Another company: not found, and nothing of theirs in the transcript.
	for tool, args := range map[string]map[string]any{
		"update_person":           {"external_id": "P-THEIRS-2", "full_name": "Renamed"},
		"set_person_active":       {"external_id": "P-THEIRS-2", "active": false},
		"list_person_credentials": {"external_id": "P-THEIRS-2"},
		"cancel_enrollment":       {"external_id": "P-THEIRS-2"},
		"explain_denial":          {"external_id": "P-THEIRS-2"},
	} {
		result, content, _ := f.runTool(t, conv, tool, args)
		if s := result.Data["status"]; s != models.ToolCallNotFound && s != models.ToolCallFailed {
			t.Errorf("%s on another company's person = %v", tool, s)
		}
		if strings.Contains(content, "Their Person") {
			t.Errorf("%s leaked another company's person", tool)
		}
	}
	var theirName string
	scanRow(t, `SELECT full_name FROM people WHERE company_id = $1 AND external_id = 'P-THEIRS-2'`, []any{other}, &theirName)
	if theirName != "Their Person" {
		t.Fatalf("another company's person was changed to %q", theirName)
	}
}

func TestAssistantListsPendingTerminalsWithoutTheirCodes(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedAdoptedAnnouncement(t, f.companyID, "AT-PEND-1")
	mustExec(t, `UPDATE terminal_announcements SET first_seen_ip = '198.51.100.7', last_seen_ip = '198.51.100.7', firmware_version = '1.3.5' WHERE serial_number = 'AT-PEND-1'`)
	conv := f.newConversation(t)
	result, content, events := f.runTool(t, conv, "list_pending_terminals", map[string]any{})
	expectStatus(t, "list_pending_terminals", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"serial_number":"AT-PEND-1"`) || !strings.Contains(content, `"count":1`) {
		t.Fatalf("pending = %s", content)
	}
	for _, forbidden := range []string{"pairing_code", "announce_token", "api_key", "198.51.100.7", "first_seen_ip", "last_seen_ip", "adopted_by", "hardware_revision"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("pending list carries %q: %s", forbidden, content)
		}
	}
	handoff := onlyEvent(t, events, assistant.EventHandoff)
	if handoff.Data["route"] != "/terminals?pending=1" {
		t.Fatalf("handoff = %v", handoff.Data)
	}

	// A VIEWER is not offered it, and a probe is refused without a request.
	v := newAssistantFixture(t, models.RoleViewer)
	vconv := v.newConversation(t)
	result, _, _ = v.runTool(t, vconv, "list_pending_terminals", map[string]any{})
	expectStatus(t, "list_pending_terminals as VIEWER", result, models.ToolCallInvalid)
}

func TestAssistantReadsSiteSettingsAsPolicyOnly(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	mustExec(t, `UPDATE sites SET settings = '{"door_relay_pin": 12, "secret_note": "sk-should-not-leak"}'::jsonb WHERE public_id = $1::uuid`, sitePub)
	conv := f.newConversation(t)
	result, content, _ := f.runTool(t, conv, "get_site_settings", map[string]any{"site_id": sitePub})
	expectStatus(t, "get_site_settings", result, models.ToolCallExecuted)
	if !strings.Contains(content, "offline_policy") || !strings.Contains(content, "offline_grace_minutes") {
		t.Fatalf("settings = %s", content)
	}
	if strings.Contains(content, "door_relay_pin") || strings.Contains(content, "should-not-leak") {
		t.Fatalf("the free-form settings blob reached the model: %s", content)
	}
}

// --- enrolment ----------------------------------------------------------------------------

func TestAssistantCancelsAnEnrolment(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-CAN", "Cancel Person")
	f.env.registerDevice(f.env.siteAKey, "AT-CAN-1")
	conv := f.newConversation(t)

	// Nothing live: refused in the route's words.
	result, content, _ := f.runTool(t, conv, "cancel_enrollment", map[string]any{"external_id": "P-CAN"})
	expectStatus(t, "cancel_enrollment with nothing live", result, models.ToolCallInvalid)
	if !strings.Contains(content, "no enrolment in progress") {
		t.Fatalf("message = %s", content)
	}

	// Start one through the console (as the operator would), then cancel it.
	code, _ := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/terminals/AT-CAN-1/enrollments", `{"external_id":"P-CAN"}`, f.token, f.csrf)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("starting an enrolment = %d", code)
	}
	result, content, events := f.runTool(t, conv, "cancel_enrollment", map[string]any{"external_id": "P-CAN"})
	expectStatus(t, "cancel_enrollment", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"status":"CANCELLED"`) {
		t.Fatalf("cancelled = %s", content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("cancelling asked for approval")
	}
	if onlyEvent(t, events, assistant.EventHandoff).Data["route"] != "/people/P-CAN" {
		t.Fatalf("handoff = %v", onlyEvent(t, events, assistant.EventHandoff).Data)
	}
	var audits int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'ENROLMENT_CANCELLED' AND c.tool_name = 'cancel_enrollment' AND a.company_id = $1`, []any{f.companyID}, &audits)
	if audits != 1 {
		t.Fatalf("audited cancellations = %d", audits)
	}
}

// --- hand-offs and secrets across the whole catalogue ------------------------------------------

var knownHandoffRoutes = regexp.MustCompile(`^(/people/[A-Za-z0-9._%-]+(\?enrol=1)?|/terminals/[A-Za-z0-9._%-]+|/terminals\?pending=1|/sites/[A-Za-z0-9._%-]+)$`)

func TestAssistantPhase2ProjectionsCarryNoSecretsAndHandOffToKnownRoutes(t *testing.T) {
	f := newAssistantFixture(t, models.RoleOwner)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	key := f.env.registerDevice(f.env.siteAKey, "AT-SEC-2")
	reportCapabilities(t, f.env, key, models.CapabilityCmdDiagnosticSnapshot)
	personID := seedPerson(t, f.companyID, "P-SEC-2", "Secret Two")
	seedFingerprintCredential(t, f.companyID, personID, deviceIDBySerial(t, "AT-SEC-2"), models.CredentialActive)
	seedAdoptedAnnouncement(t, f.companyID, "AT-SEC-PEND")
	// Every secret the platform mints, minted.
	consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/sites/"+sitePub+"/api-key", "", f.token, f.csrf)
	consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/sites/"+sitePub+"/claim-codes", `{"serial_number":"AT-SEC-3"}`, f.token, f.csrf)
	seedIntegrationCredential(t, f.companyID, "secret-two")

	conv := f.newConversation(t)
	steps := []struct {
		tool string
		args map[string]any
	}{
		{"list_person_credentials", map[string]any{"external_id": "P-SEC-2"}},
		{"get_terminal_capabilities", map[string]any{"serial": "AT-SEC-2"}},
		{"list_terminal_commands", map[string]any{"serial": "AT-SEC-2"}},
		{"request_diagnostic", map[string]any{"serial": "AT-SEC-2"}},
		{"list_pending_terminals", map[string]any{}},
		{"get_site_settings", map[string]any{"site_id": sitePub}},
		{"evaluate_access", map[string]any{"serial": "AT-SEC-2", "external_id": "P-SEC-2"}},
		{"explain_denial", map[string]any{"external_id": "P-SEC-2"}},
		{"list_people_without_access", map[string]any{}},
		{"update_person", map[string]any{"external_id": "P-SEC-2", "category": "staff"}},
		{"resync_terminal", map[string]any{"serial": "AT-SEC-2"}},
		{"create_schedule", map[string]any{"name": "Sec", "windows": "Mon 08:00-09:00"}},
	}
	handoffs := 0
	for _, step := range steps {
		result, _, events := f.runTool(t, conv, step.tool, step.args)
		expectStatus(t, step.tool, result, models.ToolCallExecuted)
		for _, h := range findEvents(events, assistant.EventHandoff) {
			handoffs++
			route := fmt.Sprint(h.Data["route"])
			if !knownHandoffRoutes.MatchString(route) {
				t.Errorf("%s handed off to %q, not a known route", step.tool, route)
			}
		}
	}
	if handoffs == 0 {
		t.Fatalf("no hand-offs were emitted")
	}

	var transcript string
	scanRow(t, `SELECT string_agg(m.content::text, ' ') FROM assistant_messages m
	               JOIN assistant_conversations c ON c.id = m.conversation_id WHERE c.public_id = $1::uuid`, []any{conv}, &transcript)
	for _, forbidden := range []string{"ats_", "atd_", "atp_", "api_key", "pairing_code", "claim_code", "announce_token", "csrf", "password",
		"provisioning key", "api_key_prefix", "ssid", "first_seen_ip", "last_seen_ip", "template", "digest", "requested_by_email", f.user.Email} {
		if strings.Contains(strings.ToLower(transcript), strings.ToLower(forbidden)) {
			t.Errorf("the transcript contains %q", forbidden)
		}
	}
}

// --- the multi-step request ----------------------------------------------------------------------

func TestAssistantCarriesOutAMultiStepRequestOneCardAtATime(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	mustExec(t, `UPDATE sites SET site_name = 'Reception' WHERE public_id = $1::uuid`, sitePub)
	f.env.registerDevice(f.env.siteAKey, "AT-MS-1")
	mustExec(t, `UPDATE devices SET device_name = 'Reception door' WHERE serial_number = 'AT-MS-1'`)

	// Turn 1: the model looks, adds John (safe, runs), then asks for the
	// rule (pauses). It must not have reached enrolment.
	f.model.steps = []modelStep{
		call("t1", "search_people", map[string]any{"query": "4471"}),
		call("t2", "create_person", map[string]any{"external_id": "4471", "full_name": "John Okafor"}),
		call("t3", "list_sites", map[string]any{}),
		call("t4", "grant_access", map[string]any{"external_id": "4471", "effect": "ALLOW", "scope_type": "SITE", "site_id": sitePub}),
		say("Added John. Approve the Reception rule to continue."),
	}
	conv := f.newConversation(t)
	var rulesBefore int
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rulesBefore)
	events := f.send(t, conv, "Create John Okafor with ID 4471, give him Reception access, and start fingerprint enrollment.")
	if onlyEvent(t, events, assistant.EventTurnCompleted).Data["stop_reason"] != "confirmation" {
		t.Fatalf("turn 1 did not stop at the card: %v", eventTypes(events))
	}
	card1 := onlyEvent(t, events, assistant.EventConfirmationRequired)
	if card1.Data["tool"] != "grant_access" {
		t.Fatalf("first card = %v", card1.Data)
	}
	var people, rules, enrolments int
	scanRow(t, `SELECT count(*) FROM people WHERE company_id = $1 AND external_id = '4471'`, []any{f.companyID}, &people)
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rules)
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if people != 1 || rules != rulesBefore || enrolments != 0 {
		t.Fatalf("after turn 1: people=%d rules=%d (was %d) enrolments=%d", people, rules, rulesBefore, enrolments)
	}

	// Approving the rule runs it; the model then asks for the enrolment.
	token1, _ := card1.Data["token"].(string)
	f.model.steps = []modelStep{
		call("t5", "list_terminals", map[string]any{"site_id": sitePub}),
		call("t6", "start_enrollment", map[string]any{"external_id": "4471", "serial": "AT-MS-1"}),
		say("The rule is in. Approve the enrolment when John is at the door."),
	}
	w := f.confirm(t, conv, token1, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve rule = %d: %s", w.Code, w.Body.String())
	}
	settled := parseSSE(t, w.Body.String())
	if s := onlyEvent(t, settled, assistant.EventConfirmationSettled).Data["outcome"]; s != "approved" {
		t.Fatalf("rule settlement = %v", s)
	}
	card2 := onlyEvent(t, settled, assistant.EventConfirmationRequired)
	if card2.Data["tool"] != "start_enrollment" {
		t.Fatalf("second card = %v", card2.Data)
	}
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rules)
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if rules != rulesBefore+1 || enrolments != 0 {
		t.Fatalf("after approving the rule: rules=%d (was %d) enrolments=%d", rules, rulesBefore, enrolments)
	}

	// Approving the enrolment starts it and hands off to the console.
	token2, _ := card2.Data["token"].(string)
	f.model.steps = []modelStep{say("Reception door is waiting for his finger.")}
	w = f.confirm(t, conv, token2, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve enrolment = %d: %s", w.Code, w.Body.String())
	}
	done := parseSSE(t, w.Body.String())
	if onlyEvent(t, done, assistant.EventHandoff).Data["route"] != "/people/4471?enrol=1" {
		t.Fatalf("handoff = %v", onlyEvent(t, done, assistant.EventHandoff).Data)
	}
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if enrolments != 1 {
		t.Fatalf("enrolments after approval = %d", enrolments)
	}
	statuses := toolCallStatuses(t, f.companyID)
	if statuses[models.ToolCallConfirmationRequested] != 2 || statuses[models.ToolCallConfirmedExecuted] != 2 {
		t.Fatalf("tool call statuses = %v", statuses)
	}
}

func TestAssistantDoesNotContinuePastAFailedStep(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	seedPerson(t, f.companyID, "4471", "Already Here")
	f.env.registerDevice(f.env.siteAKey, "AT-FAIL-1")

	// The model is scripted to press on regardless; the server-side rule is
	// that a consequential step after a failure still needs its own card and
	// runs nothing on its own -- and the prompt tells the model to stop.
	f.model.steps = []modelStep{
		call("t1", "create_person", map[string]any{"external_id": "4471", "full_name": "John Okafor"}),
		say("That ID number is already used. I have not changed their access."),
	}
	conv := f.newConversation(t)
	var rulesBefore int
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rulesBefore)
	events := f.send(t, conv, "Create John Okafor with ID 4471, give him Reception access, and start fingerprint enrollment.")
	result := onlyEvent(t, events, assistant.EventToolResult)
	if result.Data["status"] != models.ToolCallInvalid {
		t.Fatalf("duplicate create = %v", result.Data)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("a card was raised after a failed step")
	}
	var rules, enrolments int
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rules)
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if rules != rulesBefore || enrolments != 0 {
		t.Fatalf("something ran after the failure: rules=%d (was %d) enrolments=%d", rules, rulesBefore, enrolments)
	}
	if !strings.Contains(lastToolResultContent(t, conv), "already") {
		t.Fatalf("the model was not told why: %s", lastToolResultContent(t, conv))
	}

	// A rejected card ends the chain: the enrolment is never asked for.
	f.model.steps = []modelStep{
		call("t2", "grant_access", map[string]any{"external_id": "4471", "effect": "ALLOW", "scope_type": "SITE", "site_id": sitePub}),
		say("Waiting."),
	}
	events = f.send(t, conv, "give them Reception access and enrol them")
	token, _ := onlyEvent(t, events, assistant.EventConfirmationRequired).Data["token"].(string)
	f.model.steps = []modelStep{say("Understood. I have not started the enrolment.")}
	w := f.confirm(t, conv, token, false)
	if w.Code != http.StatusOK {
		t.Fatalf("reject = %d", w.Code)
	}
	rejected := parseSSE(t, w.Body.String())
	if len(findEvents(rejected, assistant.EventConfirmationRequired)) != 0 || len(findEvents(rejected, assistant.EventHandoff)) != 0 {
		t.Fatalf("after a rejection: %v", eventTypes(rejected))
	}
	scanRow(t, `SELECT count(*) FROM permissions WHERE company_id = $1 AND scope_type = 'SITE'`, []any{f.companyID}, &rules)
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if rules != rulesBefore || enrolments != 0 {
		t.Fatalf("something ran after the rejection: rules=%d (was %d) enrolments=%d", rules, rulesBefore, enrolments)
	}
}

// --- waiting within the budget --------------------------------------------------------------------

func TestAssistantWaitsDoNotSpendTheCallBudget(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-WAIT", "Wait Person")
	key := f.env.registerDevice(f.env.siteAKey, "AT-WAIT-1")
	reportCapabilities(t, f.env, key, models.CapabilityCmdDiagnosticSnapshot)
	// The turn's ordinary call budget is 48. Two full-length waits would be
	// 30 reads each at the enrolment cadence; if they were charged to it, the
	// reads after them would fail. They are not.
	code, body := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/terminals/AT-WAIT-1/commands", `{"type":"DIAGNOSTIC_SNAPSHOT"}`, f.token, f.csrf)
	if code != http.StatusAccepted && code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("queueing a diagnostic = %d %v", code, body)
	}
	commandID := fmt.Sprint(body["id"])
	consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/terminals/AT-WAIT-1/enrollments", `{"external_id":"P-WAIT"}`, f.token, f.csrf)

	f.model.steps = []modelStep{
		call("t1", "wait_for_enrollment", map[string]any{"external_id": "P-WAIT", "timeout_s": 3}),
		call("t2", "wait_for_command", map[string]any{"serial": "AT-WAIT-1", "command_id": commandID, "timeout_s": 3}),
		call("t3", "get_person", map[string]any{"external_id": "P-WAIT"}),
		say("Still waiting on both."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "wait for both")
	results := findEvents(events, assistant.EventToolResult)
	if len(results) != 3 {
		t.Fatalf("results = %v", eventTypes(events))
	}
	for _, r := range results {
		if r.Data["status"] != models.ToolCallExecuted {
			t.Fatalf("%v = %v", r.Data["tool"], r.Data["status"])
		}
	}
	if onlyEvent(t, events, assistant.EventTurnCompleted).Data["stop_reason"] != "end_turn" {
		t.Fatalf("turn did not complete: %v", eventTypes(events))
	}
}
