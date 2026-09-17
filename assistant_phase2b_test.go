package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"access-terminal-cloud-api/assistant"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Phase 2b, end to end, against the real router and PostgreSQL. The model is
// scripted as in assistant_test.go; what is under test is that the six new
// tools run the console's own routes as the operator, that ADMIN is enforced
// where the route requires it, that the site and tenant rules hold, that the
// four consequential ones ask first and run the STORED arguments, that every
// write lands in the route's own audit trail linked to the tool call, and
// that no address, code, credential or command parameter reaches the model.

// --- fixture additions ----------------------------------------------------------------

// runToolAwaitingCard scripts one tool call that is expected to pause, and
// returns the confirmation event.
func (f *assistantFixture) runToolAwaitingCard(t *testing.T, conv, tool string, args map[string]any) sseEvent {
	t.Helper()
	f.model.steps = []modelStep{call("toolu_"+tool, tool, args), say("Waiting for your approval.")}
	events := f.send(t, conv, tool)
	return onlyEvent(t, events, assistant.EventConfirmationRequired)
}

// approveCard answers a card and returns the tool.result the approval
// produced, with what the model was told about it.
//
// AN APPROVED TOOL'S RESULT REACHES THE MODEL AS NARRATION, not as a
// tool_result block: the tool_use it answers belongs to a turn that has
// already ended, so loop.go appends "The operator approved X and it ran.
// Result: ..." as the next user message. That text is what this returns.
func (f *assistantFixture) approveCard(t *testing.T, conv string, card sseEvent) (sseEvent, string) {
	t.Helper()
	token, _ := card.Data["token"].(string)
	f.model.steps = []modelStep{say("Done.")}
	w := f.confirm(t, conv, token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	return onlyEvent(t, parseSSE(t, w.Body.String()), assistant.EventToolResult), settlementNarration(t, conv)
}

// settlementNarration reads what the model was told after a confirmation was
// answered.
func settlementNarration(t *testing.T, conversationPublicID string) string {
	t.Helper()
	var content []byte
	scanRow(t, `SELECT m.content FROM assistant_messages m
	               JOIN assistant_conversations c ON c.id = m.conversation_id
	              WHERE c.public_id = $1::uuid AND m.role = 'user'
	                AND m.content::text LIKE '%The operator%'
	              ORDER BY m.seq DESC LIMIT 1`, []any{conversationPublicID}, &content)
	var blocks []models.AssistantBlock
	_ = json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// cardTitle reads the title an operator is shown.
func cardTitle(card sseEvent) string {
	consequence, _ := card.Data["consequence"].(map[string]any)
	return fmt.Sprint(consequence["title"])
}

// cardText is the whole of the wording, for asserting on a warning.
func cardText(card sseEvent) string {
	return fmt.Sprint(card.Data["consequence"])
}

// auditLinkedToTool counts audit rows the route wrote whose request id is the
// one the assistant's internal call carried -- the linkage that proves the
// write went through the console's own handler as this operator.
func auditLinkedToTool(t *testing.T, companyID int64, action, tool string) int {
	t.Helper()
	var n int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = $1 AND c.tool_name = $2 AND a.company_id = $3
	                AND a.user_agent LIKE 'AccessLink-Assistant/1%'`, []any{action, tool, companyID}, &n)
	return n
}

// --- the audit trail ------------------------------------------------------------------

func TestAssistantReadsTheAuditTrailWithoutAddressesOrChangedValues(t *testing.T) {
	f := newAssistantFixture(t, models.RoleAdmin)

	// A row of the shape that makes the two withheld columns dangerous: an
	// operator's address, and a changes column carrying a credential prefix
	// and an announcing terminal's address.
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID: f.companyID, ActorEmail: "ops@example.com", ActorRole: models.RoleAdmin,
		IPAddress: "203.0.113.9", UserAgent: "browser/1", RequestID: "req-audit-1",
		Action: "SITE_KEY_ROTATED", TargetType: "SITE", TargetLabel: "Lagos",
		Changes: map[string]any{"api_key_prefix": "ats_9f2c", "first_seen_ip": "198.51.100.7"},
	})
	// Another company's row, to prove the route's tenancy holds through the tool.
	other := operatorCompanyID(t, "two")
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID: other, ActorEmail: "them@example.com", ActorRole: models.RoleOwner,
		Action: "PERSON_CREATED", TargetType: "PERSON", TargetLabel: "Their Person",
	})

	conv := f.newConversation(t)
	result, content, _ := f.runTool(t, conv, "list_audit", map[string]any{})
	expectStatus(t, "list_audit", result, models.ToolCallExecuted)
	for _, wanted := range []string{"SITE_KEY_ROTATED", "ops@example.com", "Lagos"} {
		if !strings.Contains(content, wanted) {
			t.Errorf("the trail lacks %s: %s", wanted, content)
		}
	}
	for _, forbidden := range []string{"203.0.113.9", "ip_address", "changes", "api_key_prefix",
		"ats_9f2c", "198.51.100.7", "Their Person", "them@example.com"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("the trail carries %q: %s", forbidden, content)
		}
	}

	// Filtering runs on the route, not on the page.
	result, content, _ = f.runTool(t, conv, "list_audit", map[string]any{"action": "SITE_KEY_ROTATED", "limit": 5})
	expectStatus(t, "list_audit by action", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"total":1`) {
		t.Fatalf("filtered trail = %s", content)
	}
	result, content, _ = f.runTool(t, conv, "list_audit", map[string]any{"action": "NOTHING_DID_THIS"})
	expectStatus(t, "list_audit with no matches", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"total":0`) {
		t.Fatalf("empty trail = %s", content)
	}

	// A time filter it cannot read is refused before the route sees it, and
	// the refusal names which argument was wrong.
	result, content, _ = f.runTool(t, conv, "list_audit", map[string]any{"since": "last week"})
	expectStatus(t, "list_audit with a bad since", result, models.ToolCallInvalid)
	if !strings.Contains(content, "since must be an RFC 3339 instant") {
		t.Fatalf("bad since = %s", content)
	}

	// A MANAGER is never shown it, and a call is refused without a request.
	m := newAssistantFixture(t, models.RoleManager)
	mconv := m.newConversation(t)
	result, _, _ = m.runTool(t, mconv, "list_audit", map[string]any{})
	expectStatus(t, "list_audit as MANAGER", result, models.ToolCallInvalid)
	var reads int
	scanRow(t, `SELECT count(*) FROM assistant_tool_calls WHERE tool_name = 'list_audit' AND company_id = $1 AND http_status > 0`,
		[]any{m.companyID}, &reads)
	if reads != 0 {
		t.Fatalf("a MANAGER's refused list_audit still reached the router: %d", reads)
	}
}

// --- capabilities ---------------------------------------------------------------------

func TestAssistantCapabilitiesCarryThePhase2bEffects(t *testing.T) {
	// The console falls back to this map when a tool.result reaches it
	// without domains, so a new write that is missing from it would leave a
	// screen stale after a replay.
	f := newAssistantFixture(t, models.RoleAdmin)
	code, body := consoleCall(t, f.env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("capabilities = %d", code)
	}
	effects, _ := body["effects"].(map[string]any)
	for tool, want := range map[string][]string{
		"withdraw_command":         {"terminals", "audit"},
		"run_device_test":          {"terminals", "audit"},
		"delete_schedule":          {"schedules", "permissions", "audit"},
		"approve_pending_terminal": {"pending_terminals", "terminals", "sites", "audit"},
		"reject_pending_terminal":  {"pending_terminals", "audit"},
	} {
		got := fmt.Sprint(effects[tool])
		for _, domain := range want {
			if !strings.Contains(got, domain) {
				t.Errorf("effects[%s] = %v, want %s", tool, effects[tool], domain)
			}
		}
	}
	if _, ok := effects["list_audit"]; ok {
		t.Errorf("effects lists the read list_audit")
	}
	tools := fmt.Sprint(body["tools"])
	for _, shown := range []string{"list_audit", "withdraw_command", "run_device_test", "delete_schedule",
		"approve_pending_terminal", "reject_pending_terminal"} {
		if !strings.Contains(tools, shown) {
			t.Errorf("an ADMIN is not offered %s", shown)
		}
	}

	// A MANAGER gets the three that are theirs and none of the three that
	// are not, in the capabilities response as well as in the registry.
	m := newAssistantFixture(t, models.RoleManager)
	_, mbody := consoleCall(t, m.env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", m.token, m.csrf)
	mtools := fmt.Sprint(mbody["tools"])
	for _, shown := range []string{"withdraw_command", "run_device_test", "delete_schedule"} {
		if !strings.Contains(mtools, shown) {
			t.Errorf("a MANAGER is not offered %s", shown)
		}
	}
	for _, hidden := range []string{"list_audit", "approve_pending_terminal", "reject_pending_terminal"} {
		if strings.Contains(mtools, hidden) {
			t.Errorf("a MANAGER is offered %s", hidden)
		}
	}
}

// --- withdrawing a command ------------------------------------------------------------

func TestAssistantWithdrawsOnlyACommandTheTerminalHasNotCollected(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	key := f.env.registerDevice(f.env.siteAKey, "AT-WD-1")
	reportCapabilities(t, f.env, key, models.CapabilityCmdDiagnosticSnapshot)
	conv := f.newConversation(t)

	result, _, _ := f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-WD-1"})
	expectStatus(t, "request_diagnostic", result, models.ToolCallExecuted)
	commandID := queryString(t, `SELECT public_id::text FROM sync_jobs WHERE job_type = 'DIAGNOSTIC_SNAPSHOT' ORDER BY id DESC LIMIT 1`)

	// Withdrawing runs without a card: it makes the terminal do less.
	result, content, events := f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-WD-1", "command_id": commandID})
	expectStatus(t, "withdraw_command", result, models.ToolCallExecuted)
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("withdraw_command asked for approval")
	}
	if !strings.Contains(content, `"state":"CANCELLED"`) || !strings.Contains(content, "never be offered it") {
		t.Fatalf("withdrawn command = %s", content)
	}
	if strings.Contains(content, "params") || strings.Contains(content, f.user.Email) {
		t.Fatalf("the withdrawal carried parameters or the operator's email: %s", content)
	}
	if d := fmt.Sprint(result.Data["domains"]); !strings.Contains(d, "terminals") || !strings.Contains(d, "audit") {
		t.Fatalf("withdraw domains = %v", result.Data["domains"])
	}
	var state string
	scanRow(t, `SELECT status FROM sync_jobs WHERE public_id = $1::uuid`, []any{commandID}, &state)
	if state != "CANCELLED" {
		t.Fatalf("the command is %s in the database", state)
	}
	if n := auditLinkedToTool(t, f.companyID, "TERMINAL_COMMAND_WITHDRAWN", "withdraw_command"); n != 1 {
		t.Fatalf("audited withdrawals = %d, want 1", n)
	}

	// The same one again: refused in the route's words rather than reported
	// as a second success.
	result, content, _ = f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-WD-1", "command_id": commandID})
	if s := result.Data["status"]; s != models.ToolCallInvalid && s != models.ToolCallNotFound {
		t.Fatalf("withdrawing twice = %v (%s)", s, content)
	}

	// One the terminal has already collected cannot be recalled.
	result, _, _ = f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-WD-1"})
	expectStatus(t, "second request_diagnostic", result, models.ToolCallExecuted)
	delivered := queryString(t, `SELECT public_id::text FROM sync_jobs WHERE job_type = 'DIAGNOSTIC_SNAPSHOT' ORDER BY id DESC LIMIT 1`)
	f.env.jobs(key) // the terminal collects its work
	result, content, _ = f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-WD-1", "command_id": delivered})
	expectStatus(t, "withdrawing a collected command", result, models.ToolCallInvalid)
	if !strings.Contains(content, "already been collected") {
		t.Fatalf("collected command = %s", content)
	}
	scanRow(t, `SELECT status FROM sync_jobs WHERE public_id = $1::uuid`, []any{delivered}, &state)
	if state == "CANCELLED" {
		t.Fatalf("a delivered command was cancelled anyway")
	}

	// A command id that is not this terminal's is not found, and says nothing
	// about whose it is.
	result, content, _ = f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-WD-1", "command_id": "a7f3c2e1-0000-4000-8000-000000000000"})
	expectStatus(t, "withdrawing an unknown command", result, models.ToolCallNotFound)
	if strings.Contains(content, "company") && strings.Contains(content, "other") {
		t.Fatalf("the refusal described another tenant: %s", content)
	}
}

// --- the device test ------------------------------------------------------------------

func TestAssistantDeviceTestAsksFirstAndCannotReachTheRelay(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	key := f.env.registerDevice(f.env.siteAKey, "AT-TEST-1")
	mustExec(t, `UPDATE devices SET device_name = 'Reception' WHERE serial_number = 'AT-TEST-1'`)
	reportCapabilities(t, f.env, key, models.CapabilityCmdDeviceTest)
	conv := f.newConversation(t)

	// The relay is not expressible: refused at the schema, before any request.
	result, content, events := f.runTool(t, conv, "run_device_test",
		map[string]any{"serial": "AT-TEST-1", "target": "relay"})
	expectStatus(t, "run_device_test targeting the relay", result, models.ToolCallInvalid)
	if !strings.Contains(content, "must be one of") || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("relay = %s", content)
	}
	var queued int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`, []any{}, &queued)
	if queued != 0 {
		t.Fatalf("a refused target still queued work: %d", queued)
	}

	// A supported target asks first, and the card names the terminal and the
	// place, because the point of the question is the room.
	card := f.runToolAwaitingCard(t, conv, "run_device_test",
		map[string]any{"serial": "AT-TEST-1", "target": "buzzer", "reason": "checking the tone"})
	if !strings.Contains(cardTitle(card), "Have Reception") || !strings.Contains(cardTitle(card), "sound its buzzer") {
		t.Fatalf("card = %v", card.Data["consequence"])
	}
	if !strings.Contains(cardText(card), "Anybody standing at it will notice") {
		t.Fatalf("card body = %v", card.Data["consequence"])
	}
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`, []any{}, &queued)
	if queued != 0 {
		t.Fatalf("the test was queued before approval: %d", queued)
	}

	result, content = f.approveCard(t, conv, card)
	expectStatus(t, "approved device test", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, `"type":"DEVICE_TEST"`) || !strings.Contains(content, `"state":"QUEUED"`) {
		t.Fatalf("queued test = %s", content)
	}
	if !strings.Contains(content, `"reason":"assistant: checking the tone"`) {
		t.Fatalf("the operator's words are not on the command: %s", content)
	}
	// The stored parameters are the route's business, not the model's.
	if strings.Contains(content, `"params"`) {
		t.Fatalf("the command's parameters reached the model: %s", content)
	}
	var target string
	scanRow(t, `SELECT payload->>'target' FROM sync_jobs WHERE job_type = 'DEVICE_TEST' ORDER BY id DESC LIMIT 1`, []any{}, &target)
	if target != models.DeviceTestBuzzer {
		t.Fatalf("the queued test targets %q", target)
	}
	if n := auditLinkedToTool(t, f.companyID, "TERMINAL_COMMAND_ISSUED", "run_device_test"); n != 1 {
		t.Fatalf("audited device tests = %d, want 1", n)
	}

	// A rejected card runs nothing.
	card = f.runToolAwaitingCard(t, conv, "run_device_test",
		map[string]any{"serial": "AT-TEST-1", "target": "display"})
	token, _ := card.Data["token"].(string)
	f.model.steps = []modelStep{say("Left alone.")}
	if w := f.confirm(t, conv, token, false); w.Code != http.StatusOK {
		t.Fatalf("reject = %d: %s", w.Code, w.Body.String())
	}
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND payload->>'target' = 'display'`, []any{}, &queued)
	if queued != 0 {
		t.Fatalf("a rejected device test ran: %d", queued)
	}

	// A terminal that has not reported the capability NEVER REACHES A CARD.
	//
	// This assertion used to say the opposite -- a card, an approval, and the
	// route refusing afterwards -- and that was the defect: the operator spent
	// a single-use confirmation to be told no. The preflight in
	// tools_fleet.go now answers first, in the store's own words. The route is
	// still the gate, and every state it refuses is covered by
	// TestAssistantDeviceTestRefusesWhatTheTerminalCannotCollect.
	f.env.registerDevice(f.env.siteAKey, "AT-TEST-2")
	result, content, events = f.runTool(t, conv, "run_device_test",
		map[string]any{"serial": "AT-TEST-2", "target": "self_test"})
	expectStatus(t, "run_device_test on an incapable terminal", result, models.ToolCallFailed)
	if !strings.Contains(content, "never reported") {
		t.Fatalf("incapable terminal = %s", content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("a card was shown for a terminal that cannot run the test")
	}
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND device_id = $1`,
		[]any{deviceIDBySerial(t, "AT-TEST-2")}, &queued)
	if queued != 0 {
		t.Fatalf("a refused device test queued work: %d", queued)
	}
}

// --- deleting a schedule --------------------------------------------------------------

func TestAssistantDeletesOnlyAScheduleNothingUses(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	conv := f.newConversation(t)

	result, _, _ := f.runTool(t, conv, "create_schedule",
		map[string]any{"name": "Weekend", "windows": "Sat,Sun 09:00-13:00"})
	expectStatus(t, "create_schedule", result, models.ToolCallExecuted)
	var scheduleID int64
	scanRow(t, `SELECT id FROM schedules WHERE company_id = $1 AND name = 'Weekend'`, []any{f.companyID}, &scheduleID)
	publicID := queryString(t, `SELECT public_id::text FROM schedules WHERE id = $1`, scheduleID)

	// A rule depends on it: refused in the tool's own words, WITHOUT a card,
	// because the route would turn the deletion down anyway.
	personID := seedPerson(t, f.companyID, "P-DEL", "Del Person")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, schedule_id) VALUES ($1, $2, 'COMPANY', 'ALLOW', $3)`,
		f.companyID, personID, scheduleID)
	result, content, events := f.runTool(t, conv, "delete_schedule", map[string]any{"schedule_id": publicID})
	expectStatus(t, "delete_schedule while in use", result, models.ToolCallFailed)
	if !strings.Contains(content, "1 access rule still use") {
		t.Fatalf("in-use refusal does not state the count: %s", content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("a schedule in use was offered for approval")
	}
	var deleted int
	scanRow(t, `SELECT count(*) FROM schedules WHERE id = $1 AND deleted_at IS NOT NULL`, []any{scheduleID}, &deleted)
	if deleted != 0 {
		t.Fatalf("the schedule was deleted while a rule used it")
	}

	// The rule goes: now it asks, and the card states the count the approval
	// rests on.
	mustExec(t, `UPDATE permissions SET deleted_at = CURRENT_TIMESTAMP WHERE schedule_id = $1`, scheduleID)
	card := f.runToolAwaitingCard(t, conv, "delete_schedule", map[string]any{"schedule_id": publicID})
	if cardTitle(card) != "Delete Weekend?" || !strings.Contains(cardText(card), "0 access rules refer to this schedule") {
		t.Fatalf("card = %v", card.Data["consequence"])
	}
	scanRow(t, `SELECT count(*) FROM schedules WHERE id = $1 AND deleted_at IS NOT NULL`, []any{scheduleID}, &deleted)
	if deleted != 0 {
		t.Fatalf("the schedule was deleted before approval")
	}

	result, content = f.approveCard(t, conv, card)
	expectStatus(t, "approved delete_schedule", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, `"deleted":true`) || !strings.Contains(content, "Weekend") {
		t.Fatalf("deleted = %s", content)
	}
	scanRow(t, `SELECT count(*) FROM schedules WHERE id = $1 AND deleted_at IS NOT NULL`, []any{scheduleID}, &deleted)
	if deleted != 1 {
		t.Fatalf("the schedule was not deleted")
	}
	if n := auditLinkedToTool(t, f.companyID, "SCHEDULE_CONFIGURED", "delete_schedule"); n != 1 {
		t.Fatalf("audited deletions = %d, want 1", n)
	}
	// The screens the deletion touches are named on the result.
	for _, domain := range []string{"schedules", "permissions", "audit"} {
		if d := fmt.Sprint(result.Data["domains"]); !strings.Contains(d, domain) {
			t.Errorf("delete domains = %v, want %s", result.Data["domains"], domain)
		}
	}

	// An id that is not a schedule of this company's never reaches a card.
	result, content, events = f.runTool(t, conv, "delete_schedule",
		map[string]any{"schedule_id": "a7f3c2e1-0000-4000-8000-000000000000"})
	expectStatus(t, "delete_schedule of an unknown id", result, models.ToolCallFailed)
	if !strings.Contains(content, "No schedule has that id") || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("unknown schedule = %s", content)
	}
}

// --- deciding about a terminal that is waiting ------------------------------------------

func TestAssistantApprovesAndThenUndoesATerminalSetup(t *testing.T) {
	f := newAssistantFixture(t, models.RoleAdmin)
	seedAdoptedAnnouncement(t, f.companyID, "AT-APR-1")
	mustExec(t, `UPDATE terminal_announcements SET first_seen_ip = '198.51.100.7', last_seen_ip = '198.51.100.7',
	                    firmware_version = '1.3.5', hardware_revision = 'rev-c' WHERE serial_number = 'AT-APR-1'`)
	pendingID := queryString(t, `SELECT public_id::text FROM terminal_announcements WHERE serial_number = 'AT-APR-1'`)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	siteName := queryString(t, `SELECT site_name FROM sites WHERE public_id = $1::uuid`, sitePub)
	conv := f.newConversation(t)

	card := f.runToolAwaitingCard(t, conv, "approve_pending_terminal",
		map[string]any{"pending_id": pendingID, "site_id": sitePub, "device_name": "Reception"})
	if !strings.Contains(cardTitle(card), "Set AT-APR-1 up at "+siteName) {
		t.Fatalf("card = %v", card.Data["consequence"])
	}
	if !strings.Contains(cardText(card), "as Reception at "+siteName) {
		t.Fatalf("the card does not say what it will be called: %v", card.Data["consequence"])
	}
	// Nothing is decided by asking.
	var state string
	scanRow(t, `SELECT state FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &state)
	if state != "ADOPTED" {
		t.Fatalf("state before approval = %s", state)
	}

	result, content := f.approveCard(t, conv, card)
	expectStatus(t, "approved terminal", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, `"serial_number":"AT-APR-1"`) || !strings.Contains(content, `"device_name":"Reception"`) {
		t.Fatalf("approval = %s", content)
	}
	for _, forbidden := range []string{"198.51.100.7", "first_seen_ip", "last_seen_ip", "pairing_code",
		"announce_token", "hardware_revision", "adopted_by", "capabilities"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("the approval carried %q: %s", forbidden, content)
		}
	}
	scanRow(t, `SELECT state FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &state)
	if state != "APPROVED" {
		t.Fatalf("state after approval = %s", state)
	}
	var approvedSite int64
	scanRow(t, `SELECT site_id FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &approvedSite)
	if approvedSite != siteIDByKey(t, f.env.siteAKey) {
		t.Fatalf("approved into site %d", approvedSite)
	}
	if n := auditLinkedToTool(t, f.companyID, "TERMINAL_APPROVED", "approve_pending_terminal"); n != 1 {
		t.Fatalf("audited approvals = %d, want 1", n)
	}
	// The fleet, the waiting list and the site all move.
	for _, domain := range []string{"pending_terminals", "terminals", "sites", "audit"} {
		if d := fmt.Sprint(result.Data["domains"]); !strings.Contains(d, domain) {
			t.Errorf("approve domains = %v, want %s", result.Data["domains"], domain)
		}
	}
	// Approving again is refused in the state's own words, without a card.
	result, content, events := f.runTool(t, conv, "approve_pending_terminal",
		map[string]any{"pending_id": pendingID, "site_id": sitePub})
	expectStatus(t, "approving twice", result, models.ToolCallFailed)
	if !strings.Contains(content, "already been approved") || len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("second approval = %s", content)
	}

	// Undoing it is the reject tool, and the card says so rather than
	// describing a refusal.
	card = f.runToolAwaitingCard(t, conv, "reject_pending_terminal",
		map[string]any{"pending_id": pendingID, "reason": "wrong site"})
	if cardTitle(card) != "Undo the approval of AT-APR-1?" {
		t.Fatalf("undo card = %v", card.Data["consequence"])
	}
	result, content = f.approveCard(t, conv, card)
	expectStatus(t, "approved rejection", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, "announces itself afresh") {
		t.Fatalf("rejection = %s", content)
	}
	scanRow(t, `SELECT state FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &state)
	if state != "REJECTED" {
		t.Fatalf("state after rejection = %s", state)
	}
	var reason string
	scanRow(t, `SELECT COALESCE(rejected_reason, '') FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &reason)
	if reason != "assistant: wrong site" {
		t.Fatalf("stored reason = %q", reason)
	}
	if n := auditLinkedToTool(t, f.companyID, "TERMINAL_SETUP_REJECTED", "reject_pending_terminal"); n != 1 {
		t.Fatalf("audited rejections = %d, want 1", n)
	}
}

func TestAssistantTerminalDecisionsAreAdminOnlyAndTenantScoped(t *testing.T) {
	// A MANAGER is never shown either decision, and a call is refused before
	// any request reaches the router.
	m := newAssistantFixture(t, models.RoleManager)
	seedAdoptedAnnouncement(t, m.companyID, "AT-MGR-1")
	pendingID := queryString(t, `SELECT public_id::text FROM terminal_announcements WHERE serial_number = 'AT-MGR-1'`)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, m.env.siteAKey))
	mconv := m.newConversation(t)
	for tool, args := range map[string]map[string]any{
		"approve_pending_terminal": {"pending_id": pendingID, "site_id": sitePub},
		"reject_pending_terminal":  {"pending_id": pendingID},
	} {
		result, _, _ := m.runTool(t, mconv, tool, args)
		expectStatus(t, tool+" as MANAGER", result, models.ToolCallInvalid)
	}
	var state string
	scanRow(t, `SELECT state FROM terminal_announcements WHERE public_id = $1::uuid`, []any{pendingID}, &state)
	if state != "ADOPTED" {
		t.Fatalf("a MANAGER's refused call changed the state to %s", state)
	}

	// Another company's waiting terminal is not found, and nothing of theirs
	// reaches the transcript.
	f := newAssistantFixture(t, models.RoleAdmin)
	other := operatorCompanyID(t, "two")
	seedAdoptedAnnouncement(t, other, "AT-THEIRS-1")
	theirs := queryString(t, `SELECT public_id::text FROM terminal_announcements WHERE serial_number = 'AT-THEIRS-1'`)
	ourSite := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	conv := f.newConversation(t)
	for tool, args := range map[string]map[string]any{
		"approve_pending_terminal": {"pending_id": theirs, "site_id": ourSite},
		"reject_pending_terminal":  {"pending_id": theirs},
	} {
		result, content, events := f.runTool(t, conv, tool, args)
		if s := result.Data["status"]; s != models.ToolCallNotFound && s != models.ToolCallFailed {
			t.Errorf("%s on another company's terminal = %v", tool, s)
		}
		if strings.Contains(content, "AT-THEIRS-1") {
			t.Errorf("%s leaked another company's serial: %s", tool, content)
		}
		if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
			t.Errorf("%s offered another company's terminal for approval", tool)
		}
	}
	scanRow(t, `SELECT state FROM terminal_announcements WHERE public_id = $1::uuid`, []any{theirs}, &state)
	if state != "ADOPTED" {
		t.Fatalf("another company's announcement became %s", state)
	}

	// A site that is not this company's is refused at the confirmation, so no
	// approval is ever offered for it.
	seedAdoptedAnnouncement(t, f.companyID, "AT-ADM-1")
	mine := queryString(t, `SELECT public_id::text FROM terminal_announcements WHERE serial_number = 'AT-ADM-1'`)
	result, content, events := f.runTool(t, conv, "approve_pending_terminal",
		map[string]any{"pending_id": mine, "site_id": "a7f3c2e1-0000-4000-8000-000000000000"})
	if s := result.Data["status"]; s != models.ToolCallNotFound && s != models.ToolCallFailed {
		t.Fatalf("approving into an unknown site = %v (%s)", s, content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("an unknown site was offered for approval")
	}
}

// --- scope and tenancy across the new fleet writes ---------------------------------------

func TestAssistantPhase2bFleetWritesHonourSiteGrants(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	siteA := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	if err := database.ReplaceSiteGrants(f.companyID, f.user.ID, []string{siteA}); err != nil {
		t.Fatalf("granting site A: %v", err)
	}
	keyA := f.env.registerDevice(f.env.siteAKey, "AT-SC-A")
	keyB := f.env.registerDevice(f.env.siteBKey, "AT-SC-B")
	reportCapabilities(t, f.env, keyA, models.CapabilityCmdDeviceTest)
	reportCapabilities(t, f.env, keyB, models.CapabilityCmdDeviceTest)
	conv := f.newConversation(t)

	// A device test at an ungranted site: the CARD is refused, so the
	// operator is never asked to approve something they cannot do.
	result, content, events := f.runTool(t, conv, "run_device_test",
		map[string]any{"serial": "AT-SC-B", "target": "buzzer"})
	if s := result.Data["status"]; s != models.ToolCallFailed && s != models.ToolCallRefusedScope {
		t.Fatalf("run_device_test at an ungranted site = %v (%s)", s, content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("an ungranted site was offered for approval")
	}

	// Withdrawing at an ungranted site is refused on scope, and queues nothing.
	result, content, _ = f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-SC-B", "command_id": "a7f3c2e1-0000-4000-8000-000000000000"})
	expectStatus(t, "withdraw_command at an ungranted site", result, models.ToolCallRefusedScope)
	if strings.Contains(content, "AT-SC-B") && strings.Contains(content, "site_id") {
		t.Fatalf("the refusal described the ungranted site: %s", content)
	}
	var jobs int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND device_id = $1`,
		[]any{deviceIDBySerial(t, "AT-SC-B")}, &jobs)
	if jobs != 0 {
		t.Fatalf("a refused fleet write still queued work: %d", jobs)
	}

	// The granted site works, through the same tools.
	card := f.runToolAwaitingCard(t, conv, "run_device_test",
		map[string]any{"serial": "AT-SC-A", "target": "buzzer"})
	result, _ = f.approveCard(t, conv, card)
	expectStatus(t, "run_device_test at the granted site", result, models.ToolCallConfirmedExecuted)
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND device_id = $1`,
		[]any{deviceIDBySerial(t, "AT-SC-A")}, &jobs)
	if jobs != 1 {
		t.Fatalf("the granted site queued %d tests", jobs)
	}
}

// --- the sweep ---------------------------------------------------------------------------

func TestAssistantPhase2bProjectionsCarryNoSecretsAndHandOffToKnownRoutes(t *testing.T) {
	f := newAssistantFixture(t, models.RoleAdmin)
	key := f.env.registerDevice(f.env.siteAKey, "AT-SWEEP-1")
	reportCapabilities(t, f.env, key, models.CapabilityCmdDeviceTest, models.CapabilityCmdDiagnosticSnapshot)
	seedAdoptedAnnouncement(t, f.companyID, "AT-SWEEP-2")
	mustExec(t, `UPDATE terminal_announcements SET first_seen_ip = '198.51.100.7', last_seen_ip = '198.51.100.7' WHERE serial_number = 'AT-SWEEP-2'`)
	mustExec(t, `UPDATE sites SET settings = '{"secret_note": "sk-should-not-leak"}'::jsonb WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID: f.companyID, ActorEmail: "ops@example.com", ActorRole: models.RoleAdmin,
		IPAddress: "203.0.113.9", Action: "SITE_KEY_ROTATED", TargetType: "SITE", TargetLabel: "Lagos",
		Changes: map[string]any{"api_key_prefix": "ats_deadbeef"},
	})
	conv := f.newConversation(t)

	// Every read Phase 2b added, plus the safe write, run in one conversation.
	f.runTool(t, conv, "list_audit", map[string]any{})
	f.runTool(t, conv, "list_pending_terminals", map[string]any{})
	f.runTool(t, conv, "request_diagnostic", map[string]any{"serial": "AT-SWEEP-1"})
	commandID := queryString(t, `SELECT public_id::text FROM sync_jobs WHERE job_type = 'DIAGNOSTIC_SNAPSHOT' ORDER BY id DESC LIMIT 1`)
	_, _, events := f.runTool(t, conv, "withdraw_command", map[string]any{"serial": "AT-SWEEP-1", "command_id": commandID})

	// Every hand-off any Phase 2b tool emitted is one of the five known routes.
	for _, handoff := range findEvents(events, assistant.EventHandoff) {
		route := fmt.Sprint(handoff.Data["route"])
		if !strings.HasPrefix(route, "/terminals") && !strings.HasPrefix(route, "/people") && !strings.HasPrefix(route, "/sites") {
			t.Errorf("hand-off to %q", route)
		}
	}

	// Nothing in the whole transcript is a secret, an address or a code.
	var transcript string
	scanRow(t, `SELECT string_agg(m.content::text, ' ') FROM assistant_messages m
	               JOIN assistant_conversations c ON c.id = m.conversation_id
	              WHERE c.public_id = $1::uuid`, []any{conv}, &transcript)
	for _, forbidden := range []string{"198.51.100.7", "203.0.113.9", "ats_deadbeef", "api_key_prefix",
		"should-not-leak", "pairing_code", "announce_token", "first_seen_ip", "ip_address",
		"atd_", "ats_", "password"} {
		if strings.Contains(strings.ToLower(transcript), strings.ToLower(forbidden)) {
			t.Errorf("the transcript contains %q", forbidden)
		}
	}
}

// --- the device-test preflight, against the real route -------------------------------------

// EVERY STATE THE ROUTE WOULD REFUSE IS REFUSED BEFORE A CARD IS BUILT, in
// the store's own order, and none of them queues anything. The review that
// asked for this found the opposite: a card promising a queued test, an
// approval that consumed the operator's single-use confirmation, and a refusal
// from the route afterwards.
func TestAssistantDeviceTestRefusesWhatTheTerminalCannotCollect(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	key := f.env.registerDevice(f.env.siteAKey, "AT-PF-1")
	mustExec(t, `UPDATE devices SET device_name = 'Reception' WHERE serial_number = 'AT-PF-1'`)
	reportCapabilities(t, f.env, key, models.CapabilityCmdDeviceTest)
	conv := f.newConversation(t)

	healthy := func() {
		t.Helper()
		mustExec(t, `UPDATE devices SET status = 'ONLINE', active = TRUE,
		                    api_key_hash = COALESCE(api_key_hash, repeat('a', 64)),
		                    credential_revoked_at = NULL
		              WHERE serial_number = 'AT-PF-1'`)
	}

	refused := []struct {
		name  string
		setup string
		want  string
	}{
		{"disabled by status", `UPDATE devices SET status = 'DISABLED' WHERE serial_number = 'AT-PF-1'`, "is disabled"},
		{"disabled by the active flag", `UPDATE devices SET active = FALSE WHERE serial_number = 'AT-PF-1'`, "is disabled"},
		{"credential revoked",
			`UPDATE devices SET api_key_hash = NULL, credential_revoked_at = CURRENT_TIMESTAMP WHERE serial_number = 'AT-PF-1'`,
			"holds no credential"},
		{"offline", `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = 'AT-PF-1'`, "not in contact"},
		{"provisioning", `UPDATE devices SET status = 'PROVISIONING' WHERE serial_number = 'AT-PF-1'`, "not in contact"},
	}
	for _, c := range refused {
		healthy()
		mustExec(t, c.setup)

		result, content, events := f.runTool(t, conv, "run_device_test",
			map[string]any{"serial": "AT-PF-1", "target": "buzzer"})
		expectStatus(t, "run_device_test on a terminal that is "+c.name, result, models.ToolCallFailed)
		if !strings.Contains(content, c.want) {
			t.Errorf("%s: refusal %q does not say %q", c.name, content, c.want)
		}
		if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
			t.Errorf("%s: a card was shown for a test the route would refuse", c.name)
		}
		var queued int
		scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`, []any{}, &queued)
		if queued != 0 {
			t.Fatalf("%s: %d device tests were queued", c.name, queued)
		}
	}

	// THE CAPABILITY CHECK COMES BEFORE THE CONTACT CHECK, matching the store:
	// a terminal that is both out of contact and has never said what it can do
	// is told the thing somebody has to fix first.
	f.env.registerDevice(f.env.siteAKey, "AT-PF-2")
	mustExec(t, `UPDATE devices SET status = 'OFFLINE' WHERE serial_number = 'AT-PF-2'`)
	result, content, events := f.runTool(t, conv, "run_device_test",
		map[string]any{"serial": "AT-PF-2", "target": "buzzer"})
	expectStatus(t, "run_device_test on a silent terminal", result, models.ToolCallFailed)
	if !strings.Contains(content, "never reported") || !strings.Contains(content, "nothing would be sent") {
		t.Fatalf("silent terminal = %s", content)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("a card was shown for a terminal that has never reported")
	}

	// A terminal that reported and cannot is told so differently.
	keyThree := f.env.registerDevice(f.env.siteAKey, "AT-PF-3")
	reportCapabilities(t, f.env, keyThree, models.CapabilityCmdDiagnosticSnapshot)
	result, content, _ = f.runTool(t, conv, "run_device_test",
		map[string]any{"serial": "AT-PF-3", "target": "buzzer"})
	expectStatus(t, "run_device_test on an incapable terminal", result, models.ToolCallFailed)
	if !strings.Contains(content, "reported what it can do") || strings.Contains(content, "never reported") {
		t.Fatalf("incapable terminal = %s", content)
	}

	// THE POSITIVE CONTROL. Healthy and capable: a card, and the wording
	// carries no warning about collecting it.
	healthy()
	card := f.runToolAwaitingCard(t, conv, "run_device_test",
		map[string]any{"serial": "AT-PF-1", "target": "buzzer"})
	if !strings.Contains(cardTitle(card), "Have Reception") {
		t.Fatalf("card = %v", card.Data["consequence"])
	}
	if text := cardText(card); strings.Contains(text, "may not collect") || strings.Contains(text, "rather than online") {
		t.Fatalf("the card still warns about collection: %s", text)
	}
	result, _ = f.approveCard(t, conv, card)
	expectStatus(t, "approved device test on a healthy terminal", result, models.ToolCallConfirmedExecuted)
	var queued int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`, []any{}, &queued)
	if queued != 1 {
		t.Fatalf("the healthy terminal queued %d tests", queued)
	}

	// ERROR and UPDATING are accepted by the store, so they still get a card,
	// and the note says the test will reach the terminal rather than warning
	// that it might not.
	mustExec(t, `DELETE FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`)
	for status, want := range map[string]string{"ERROR": "reporting a fault", "UPDATING": "installing software"} {
		mustExec(t, `UPDATE devices SET status = $1 WHERE serial_number = 'AT-PF-1'`, status)
		card = f.runToolAwaitingCard(t, conv, "run_device_test",
			map[string]any{"serial": "AT-PF-1", "target": "buzzer"})
		text := cardText(card)
		if !strings.Contains(text, want) || !strings.Contains(text, "the test will reach it") {
			t.Errorf("%s card = %s", status, text)
		}
		result, _ = f.approveCard(t, conv, card)
		expectStatus(t, "approved device test on a terminal that is "+status, result, models.ToolCallConfirmedExecuted)
		mustExec(t, `DELETE FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`)
	}
}

// --- repeatability --------------------------------------------------------------------------

// DEVICE_TEST is Repeatable: false, and the store distinguishes a repeated
// button press from a different request. Both answers reach the operator
// through the assistant, and the second one names the tool that clears it.
func TestAssistantDeviceTestRepeatabilityAndWithdrawal(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	key := f.env.registerDevice(f.env.siteAKey, "AT-RPT-1")
	mustExec(t, `UPDATE devices SET device_name = 'Side Gate' WHERE serial_number = 'AT-RPT-1'`)
	reportCapabilities(t, f.env, key, models.CapabilityCmdDeviceTest)
	conv := f.newConversation(t)

	// The first one queues.
	card := f.runToolAwaitingCard(t, conv, "run_device_test", map[string]any{"serial": "AT-RPT-1", "target": "buzzer"})
	result, content := f.approveCard(t, conv, card)
	expectStatus(t, "first device test", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, `"state":"QUEUED"`) {
		t.Fatalf("first test = %s", content)
	}
	commandID := queryString(t, `SELECT public_id::text FROM sync_jobs WHERE job_type = 'DEVICE_TEST' ORDER BY id DESC LIMIT 1`)

	// THE SAME TARGET AGAIN is the repeated button press: the one already
	// waiting is returned, and no second row is written.
	card = f.runToolAwaitingCard(t, conv, "run_device_test", map[string]any{"serial": "AT-RPT-1", "target": "buzzer"})
	result, content = f.approveCard(t, conv, card)
	expectStatus(t, "same target again", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, "already waiting") {
		t.Fatalf("repeat = %s", content)
	}
	var queued int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND status = 'PENDING'`, []any{}, &queued)
	if queued != 1 {
		t.Fatalf("a repeat wrote a second row: %d pending", queued)
	}

	// A DIFFERENT TARGET is a different command, and the slot is taken. The
	// route refuses, and its words tell the operator to withdraw the one
	// waiting -- which is the tool beside this one.
	card = f.runToolAwaitingCard(t, conv, "run_device_test", map[string]any{"serial": "AT-RPT-1", "target": "display"})
	result, content = f.approveCard(t, conv, card)
	if s := result.Data["status"]; s != models.ToolCallInvalid {
		t.Fatalf("different target while one is waiting = %v (%s)", s, content)
	}
	if !strings.Contains(content, "already waiting") || !strings.Contains(content, "withdraw") {
		t.Fatalf("the refusal does not say to withdraw: %s", content)
	}
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE job_type = 'DEVICE_TEST'`, []any{}, &queued)
	if queued != 1 {
		t.Fatalf("a refused different target still wrote a row: %d", queued)
	}

	// Withdrawing clears the slot, and the different target then queues.
	result, content, _ = f.runTool(t, conv, "withdraw_command",
		map[string]any{"serial": "AT-RPT-1", "command_id": commandID})
	expectStatus(t, "withdraw the waiting test", result, models.ToolCallExecuted)
	if !strings.Contains(content, `"state":"CANCELLED"`) {
		t.Fatalf("withdrawal = %s", content)
	}
	card = f.runToolAwaitingCard(t, conv, "run_device_test", map[string]any{"serial": "AT-RPT-1", "target": "display"})
	result, content = f.approveCard(t, conv, card)
	expectStatus(t, "different target after withdrawal", result, models.ToolCallConfirmedExecuted)
	if !strings.Contains(content, `"state":"QUEUED"`) {
		t.Fatalf("after withdrawal = %s", content)
	}
	var target string
	scanRow(t, `SELECT payload->>'target' FROM sync_jobs WHERE job_type = 'DEVICE_TEST' AND status = 'PENDING'`, []any{}, &target)
	if target != models.DeviceTestDisplay {
		t.Fatalf("the pending test targets %q", target)
	}
	// Still no command parameters in anything the model was told.
	if strings.Contains(content, `"params"`) {
		t.Fatalf("the command parameters reached the model: %s", content)
	}
}

// --- another tenant ---------------------------------------------------------------------------

// THE THREE MANAGER TOOLS, AGAINST COMPANY TWO. Site C belongs to the other
// tenant, so a terminal registered there is one this operator must not be able
// to reach by naming its serial -- which is printed on the hardware and so is
// exactly what a model might be handed.
func TestAssistantPhase2bToolsCannotReachAnotherTenant(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	other := operatorCompanyID(t, "two")

	// Their terminal, capable and healthy, with a command of their own waiting.
	theirKey := f.env.registerDevice(f.env.siteCKey, "AT-THEIRS-CMD")
	mustExec(t, `UPDATE devices SET device_name = 'Their Gate' WHERE serial_number = 'AT-THEIRS-CMD'`)
	reportCapabilities(t, f.env, theirKey, models.CapabilityCmdDeviceTest, models.CapabilityCmdDiagnosticSnapshot)
	theirDevice := deviceIDBySerial(t, "AT-THEIRS-CMD")

	// Queued BY THEIR OWN OPERATOR through the console route, so the row is
	// exactly what the platform writes rather than a hand-made one.
	_, theirToken, theirCSRF := consoleOperatorSession(t, f.env.router, other, "them-cmd@example.com", models.RoleManager)
	code, _ := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/terminals/AT-THEIRS-CMD/commands",
		`{"type":"DIAGNOSTIC_SNAPSHOT","reason":"theirs"}`, theirToken, theirCSRF)
	if code != http.StatusAccepted {
		t.Fatalf("queueing another tenant own command = %d", code)
	}
	theirCommand := queryString(t, `SELECT public_id::text FROM sync_jobs WHERE device_id = $1 AND command_class = 'COMMAND'`, theirDevice)

	// Their schedule.
	mustExec(t, `INSERT INTO schedules (company_id, name, timezone) VALUES ($1, 'Their Weekend', 'UTC')`, other)
	theirSchedule := queryString(t, `SELECT public_id::text FROM schedules WHERE company_id = $1 AND name = 'Their Weekend'`, other)

	conv := f.newConversation(t)

	for tool, args := range map[string]map[string]any{
		"withdraw_command": {"serial": "AT-THEIRS-CMD", "command_id": theirCommand},
		"run_device_test":  {"serial": "AT-THEIRS-CMD", "target": "buzzer"},
		"delete_schedule":  {"schedule_id": theirSchedule},
	} {
		result, content, events := f.runTool(t, conv, tool, args)
		if s := result.Data["status"]; s != models.ToolCallNotFound && s != models.ToolCallFailed {
			t.Errorf("%s against another tenant = %v (%s)", tool, s, content)
		}
		// Nothing of theirs is described, and no card is ever offered for it.
		for _, leaked := range []string{"Their Gate", "Their Weekend"} {
			if strings.Contains(content, leaked) {
				t.Errorf("%s leaked another tenant's data: %s", tool, content)
			}
		}
		if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
			t.Errorf("%s offered another tenant's resource for approval", tool)
		}
	}

	// Nothing of theirs moved: the command still waits, no test was queued for
	// their terminal, and their schedule is intact.
	var status string
	scanRow(t, `SELECT status FROM sync_jobs WHERE public_id = $1::uuid`, []any{theirCommand}, &status)
	if status != "PENDING" {
		t.Fatalf("another tenant's command became %s", status)
	}
	var queued int
	scanRow(t, `SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND job_type = 'DEVICE_TEST'`, []any{theirDevice}, &queued)
	if queued != 0 {
		t.Fatalf("a device test was queued for another tenant: %d", queued)
	}
	var deleted int
	scanRow(t, `SELECT count(*) FROM schedules WHERE public_id = $1::uuid AND deleted_at IS NOT NULL`, []any{theirSchedule}, &deleted)
	if deleted != 0 {
		t.Fatalf("another tenant's schedule was deleted")
	}

	// And the two ADMIN decisions, from an administrator of this company, are
	// already covered against another tenant by
	// TestAssistantTerminalDecisionsAreAdminOnlyAndTenantScoped.
}

// --- the reject reason ---------------------------------------------------------------------

// The route truncates a reason longer than 200 with a BYTE slice, so a
// multi-byte one cut there would reach Postgres as an invalid sequence and
// fail the rejection. The assistant sends no more than the route stores.
func TestAssistantRejectReasonSurvivesMultibyteWords(t *testing.T) {
	f := newAssistantFixture(t, models.RoleAdmin)
	seedAdoptedAnnouncement(t, f.companyID, "AT-UTF-1")
	pendingID := queryString(t, `SELECT public_id::text FROM terminal_announcements WHERE serial_number = 'AT-UTF-1'`)
	conv := f.newConversation(t)

	// 200 bytes of two- and three-byte runes, which a byte cut at 200 after an
	// eleven-byte prefix lands inside.
	words := strings.Repeat("é", 50) + strings.Repeat("→", 33) + "x"
	card := f.runToolAwaitingCard(t, conv, "reject_pending_terminal",
		map[string]any{"pending_id": pendingID, "reason": words})
	if cardTitle(card) != "Refuse AT-UTF-1?" {
		t.Fatalf("card = %v", card.Data["consequence"])
	}
	result, _ := f.approveCard(t, conv, card)
	expectStatus(t, "rejection with a multibyte reason", result, models.ToolCallConfirmedExecuted)

	var state, stored string
	scanRow(t, `SELECT state, COALESCE(rejected_reason, '') FROM terminal_announcements WHERE public_id = $1::uuid`,
		[]any{pendingID}, &state, &stored)
	if state != "REJECTED" {
		t.Fatalf("state = %s", state)
	}
	if len(stored) > 200 {
		t.Fatalf("the stored reason is %d bytes", len(stored))
	}
	if !utf8.ValidString(stored) {
		t.Fatalf("the stored reason is not valid UTF-8: %q", stored)
	}
	if !strings.HasPrefix(stored, "assistant") {
		t.Fatalf("provenance lost: %q", stored)
	}
	// It was actually long enough to be shortened, so this is not passing by
	// never reaching the limit.
	if len(assistantReasonForTest(words)) <= 200 {
		t.Fatalf("the fixture no longer exceeds the limit: %d bytes", len(assistantReasonForTest(words)))
	}
}

// assistantReasonForTest mirrors the prefix the assistant adds, so the test
// above can assert its fixture is genuinely over the limit without exporting
// anything from the assistant package.
func assistantReasonForTest(words string) string { return "assistant: " + words }
