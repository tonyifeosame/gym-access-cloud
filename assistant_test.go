package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/assistant"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// The in-console assistant, end to end, against the real router.
//
// THE MODEL IS SCRIPTED. Every test hands the service a fake that returns a
// fixed sequence of responses -- "call search_people", then "say what you
// found" -- so what is under test is everything around the model: that a tool
// call runs through the router as the operator, that roles and site grants
// and tenancy hold, that nothing secret-shaped is ever returned, that a
// confirmation is required and honoured exactly once, that the audit trail
// links to the tool call, and that limits and session expiry stop a turn.

// --- the scripted model -----------------------------------------------------------

type modelStep struct {
	blocks []models.AssistantBlock
	stop   string
	before func()
}

type scriptedModel struct {
	steps    []modelStep
	calls    int
	requests []assistant.ModelRequest
	usage    models.AssistantUsage
}

func (m *scriptedModel) Name() string { return "scripted-test-model" }

func (m *scriptedModel) Complete(_ context.Context, req assistant.ModelRequest, onText func(string)) (*assistant.ModelResponse, error) {
	m.requests = append(m.requests, req)
	m.calls++
	if len(m.steps) == 0 {
		return &assistant.ModelResponse{
			Blocks: []models.AssistantBlock{{Type: "text", Text: "Done."}}, StopReason: "end_turn", Usage: m.usage,
		}, nil
	}
	step := m.steps[0]
	m.steps = m.steps[1:]
	if step.before != nil {
		step.before()
	}
	for _, b := range step.blocks {
		if b.Type == "text" && onText != nil {
			onText(b.Text)
		}
	}
	stop := step.stop
	if stop == "" {
		stop = "end_turn"
		for _, b := range step.blocks {
			if b.Type == "tool_use" {
				stop = "tool_use"
			}
		}
	}
	return &assistant.ModelResponse{Blocks: step.blocks, StopReason: stop, Usage: m.usage}, nil
}

func say(text string) modelStep {
	return modelStep{blocks: []models.AssistantBlock{{Type: "text", Text: text}}}
}

func call(id, tool string, args map[string]any) modelStep {
	raw, _ := json.Marshal(args)
	return modelStep{blocks: []models.AssistantBlock{{Type: "tool_use", ID: id, Name: tool, Input: raw}}}
}

// --- fixture ----------------------------------------------------------------------

type assistantFixture struct {
	env       *testEnv
	companyID int64
	user      *models.User
	token     string
	csrf      string
	model     *scriptedModel
	service   *assistant.Service
}

const testConfirmationSecret = "test-confirmation-secret-0123456789"

func newAssistantFixture(t *testing.T, role string) *assistantFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	user, token, csrf := consoleOperatorSession(t, env.router, companyID, "assistant-"+strings.ToLower(role)+"@example.com", role)
	f := &assistantFixture{env: env, companyID: companyID, user: user, token: token, csrf: csrf, model: &scriptedModel{}}
	f.install(assistant.DefaultLimits())
	t.Cleanup(func() { handlers.SetAssistant(nil) })
	return f
}

func (f *assistantFixture) install(limits assistant.Limits) {
	f.service = assistant.New(assistant.Options{
		Router:             f.env.router,
		Model:              f.model,
		Enabled:            true,
		ConfirmationSecret: []byte(testConfirmationSecret),
		Limits:             limits,
	})
	handlers.SetAssistant(f.service)
}

func (f *assistantFixture) headers() map[string]string {
	h := sessionHeaders(f.token)
	h[middleware.CSRFHeader] = f.csrf
	h["Content-Type"] = "application/json"
	h["User-Agent"] = "assistant-test-browser/1.0"
	return h
}

func (f *assistantFixture) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range f.headers() {
		req.Header.Set(k, v)
	}
	req.RemoteAddr = "203.0.113.9:4444"
	w := httptest.NewRecorder()
	f.env.router.ServeHTTP(w, req)
	return w
}

func (f *assistantFixture) newConversation(t *testing.T) string {
	t.Helper()
	w := f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations", "{}")
	if w.Code != http.StatusCreated {
		t.Fatalf("create conversation = %d: %s", w.Code, w.Body.String())
	}
	var conv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &conv)
	return conv.ID
}

// scanRow is mustScan with query arguments.
func scanRow(t *testing.T, query string, args []any, dest ...any) {
	t.Helper()
	if err := database.DB.QueryRow(query, args...).Scan(dest...); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
}

type sseEvent struct {
	Type string
	Data map[string]any
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := map[string]any{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatalf("bad event data %q: %v", line, err)
			}
			current.Data = data
		case line == "":
			if current.Type != "" {
				events = append(events, current)
			}
			current = sseEvent{}
		}
	}
	return events
}

func (f *assistantFixture) send(t *testing.T, conversationID, text string) []sseEvent {
	t.Helper()
	body := fmt.Sprintf(`{"text":%q}`, text)
	w := f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conversationID+"/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("send = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	return parseSSE(t, w.Body.String())
}

func findEvents(events []sseEvent, typ string) []sseEvent {
	var out []sseEvent
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func onlyEvent(t *testing.T, events []sseEvent, typ string) sseEvent {
	t.Helper()
	found := findEvents(events, typ)
	if len(found) != 1 {
		t.Fatalf("want exactly one %s event, got %d in %v", typ, len(found), eventTypes(events))
	}
	return found[0]
}

func eventTypes(events []sseEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// lastToolResultContent reads what the model was actually told by the last
// tool result in the conversation's transcript.
func lastToolResultContent(t *testing.T, conversationPublicID string) string {
	t.Helper()
	var content []byte
	scanRow(t, `SELECT m.content FROM assistant_messages m
	               JOIN assistant_conversations c ON c.id = m.conversation_id
	              WHERE c.public_id = $1::uuid AND m.role = 'user'
	              ORDER BY m.seq DESC LIMIT 1`, []any{conversationPublicID}, &content)
	var blocks []models.AssistantBlock
	_ = json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return b.Content
		}
	}
	return ""
}

func toolCallStatuses(t *testing.T, companyID int64) map[string]int {
	t.Helper()
	rows, err := database.DB.Query(`SELECT status, count(*) FROM assistant_tool_calls WHERE company_id = $1 GROUP BY status`, companyID)
	if err != nil {
		t.Fatalf("reading tool calls: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		_ = rows.Scan(&status, &n)
		out[status] = n
	}
	return out
}

// --- disabled by default --------------------------------------------------------------

func TestAssistantIsAbsentUntilEnabled(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID, "nobody@example.com", models.RoleOwner)
	handlers.SetAssistant(nil)

	code, body := consoleCall(t, env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", token, csrf)
	if code != http.StatusOK || body["enabled"] != false {
		t.Fatalf("capabilities = %d %v, want enabled=false", code, body)
	}
	code, _ = consoleCall(t, env.router, http.MethodPost, "/api/v1/console/assistant/conversations", "{}", token, csrf)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("create conversation while disabled = %d, want 503", code)
	}

	// Enabled in name only: no model means no assistant, whatever the flag says.
	handlers.SetAssistant(assistant.New(assistant.Options{Router: env.router, Model: nil, Enabled: true,
		ConfirmationSecret: []byte(testConfirmationSecret)}))
	t.Cleanup(func() { handlers.SetAssistant(nil) })
	code, body = consoleCall(t, env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", token, csrf)
	if body["enabled"] != false {
		t.Fatalf("capabilities with no model = %d %v, want enabled=false", code, body)
	}
}

func TestAssistantHidesToolsAboveTheRole(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	code, body := consoleCall(t, f.env.router, http.MethodGet, "/api/v1/console/assistant/capabilities", "", f.token, f.csrf)
	if code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("capabilities = %d %v", code, body)
	}
	tools := fmt.Sprint(body["tools"])
	for _, hidden := range []string{"create_person", "grant_access", "revoke_access", "start_enrollment"} {
		if strings.Contains(tools, hidden) {
			t.Errorf("a VIEWER is offered %s: %s", hidden, tools)
		}
	}
	for _, shown := range []string{"search_people", "get_person", "list_terminals", "list_events", "wait_for_enrollment"} {
		if !strings.Contains(tools, shown) {
			t.Errorf("a VIEWER is not offered %s: %s", shown, tools)
		}
	}
}

// --- a normal read ----------------------------------------------------------------------

func TestAssistantReadsThroughTheRouterAsTheOperator(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-ADA", "Ada Okonkwo")
	f.model.steps = []modelStep{
		call("toolu_1", "search_people", map[string]any{"query": "Ada"}),
		say("I found Ada Okonkwo."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "Find Ada")

	if got := eventTypes(events); got[0] != assistant.EventTurnStarted || got[len(got)-1] != assistant.EventTurnCompleted {
		t.Fatalf("event order = %v", got)
	}
	callEvent := onlyEvent(t, events, assistant.EventToolCall)
	if callEvent.Data["tool"] != "search_people" {
		t.Fatalf("tool.call = %v", callEvent.Data)
	}
	result := onlyEvent(t, events, assistant.EventToolResult)
	if result.Data["status"] != models.ToolCallExecuted {
		t.Fatalf("tool.result = %v", result.Data)
	}
	if msg := onlyEvent(t, events, assistant.EventAssistantMessage); msg.Data["text"] != "I found Ada Okonkwo." {
		t.Fatalf("assistant.message = %v", msg.Data)
	}
	// The stream carried the text as it was produced, not only at the end.
	if len(findEvents(events, assistant.EventAssistantDelta)) == 0 {
		t.Fatalf("no assistant.delta events: %v", eventTypes(events))
	}

	// What the model was told is the projection, and only the projection.
	content := lastToolResultContent(t, conv)
	if !strings.Contains(content, `"full_name":"Ada Okonkwo"`) || !strings.Contains(content, `"external_id":"P-ADA"`) {
		t.Fatalf("projected result missing the person: %s", content)
	}
	for _, internal := range []string{`"id":`, `"company_id"`, `"created_by"`} {
		if strings.Contains(content, internal) {
			t.Errorf("projection leaked %s: %s", internal, content)
		}
	}

	// Recorded, as an executed call with the request id the router saw.
	var requestID, ua string
	scanRow(t, `SELECT request_id, user_agent FROM assistant_tool_calls WHERE company_id = $1 AND tool_name = 'search_people'`, []any{f.companyID}, &requestID, &ua)
	_ = requestID
	if len(requestID) < 8 {
		t.Fatalf("tool call recorded no request id")
	}
	if ua == "" {
		t.Fatalf("tool call recorded no user agent")
	}
	// The model's view of the operator: the prompt names them and their role.
	if !strings.Contains(f.model.requests[0].System, "role: Manager") {
		t.Fatalf("system prompt does not name the role: %.200s", f.model.requests[0].System)
	}
	// The turn's usage landed on the company ledger.
	usage, err := database.AssistantUsageThisMonth(f.companyID)
	if err != nil || usage.Turns != 1 || usage.ToolCalls != 1 {
		t.Fatalf("usage = %+v (%v), want 1 turn and 1 tool call", usage, err)
	}
}

func TestAssistantDispatchIsFlatInStatements(t *testing.T) {
	// The people read the assistant makes is the console's own paged read:
	// thirty more people must not mean more statements.
	f := newAssistantFixture(t, models.RoleManager)
	measure := func() int64 {
		f.model.steps = []modelStep{call("toolu_1", "search_people", map[string]any{"query": "Bulk", "limit": 50}), say("ok")}
		conv := f.newConversation(t)
		return countStatements(func() { f.send(t, conv, "list") })
	}
	for i := 0; i < 3; i++ {
		seedPerson(t, f.companyID, fmt.Sprintf("P-BULK-A-%d", i), "Bulk Person")
	}
	small := measure()
	for i := 0; i < 30; i++ {
		seedPerson(t, f.companyID, fmt.Sprintf("P-BULK-B-%d", i), "Bulk Person")
	}
	large := measure()
	t.Logf("assistant search_people turn: %d statements at 3 rows, %d at 33", small, large)
	if large > small {
		t.Fatalf("the assistant's read grows with the result: %d -> %d statements", small, large)
	}
}

// --- authorization --------------------------------------------------------------------------

func TestAssistantRefusesWhatTheRoleCannotDo(t *testing.T) {
	f := newAssistantFixture(t, models.RoleViewer)
	// The model was never shown create_person; a call is refused without
	// touching the router, and nothing is created.
	f.model.steps = []modelStep{
		call("toolu_1", "create_person", map[string]any{"external_id": "P-NOPE", "full_name": "Nope"}),
		say("Sorry."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "add nope")
	result := onlyEvent(t, events, assistant.EventToolResult)
	if result.Data["status"] != models.ToolCallInvalid {
		t.Fatalf("viewer create_person = %v, want INVALID", result.Data)
	}
	var n int
	scanRow(t, `SELECT count(*) FROM people WHERE company_id = $1 AND external_id = 'P-NOPE'`, []any{f.companyID}, &n)
	if n != 0 {
		t.Fatalf("a VIEWER created a person through the assistant")
	}
}

func TestAssistantWriteRunsTheRouteAndIsAudited(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	f.model.steps = []modelStep{
		call("toolu_1", "create_person", map[string]any{"external_id": "P-NEW", "full_name": "New Person", "category": "staff"}),
		say("Added New Person."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "add new person")

	result := onlyEvent(t, events, assistant.EventToolResult)
	if result.Data["status"] != models.ToolCallExecuted {
		t.Fatalf("create_person = %v", result.Data)
	}
	// Adding somebody hands off to their page and does NOT start enrolment.
	handoff := onlyEvent(t, events, assistant.EventHandoff)
	if handoff.Data["kind"] != "person" || handoff.Data["route"] != "/people/P-NEW" {
		t.Fatalf("handoff = %v", handoff.Data)
	}
	if len(findEvents(events, assistant.EventConfirmationRequired)) != 0 {
		t.Fatalf("adding a person asked for a confirmation: %v", eventTypes(events))
	}
	var enrolments int
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if enrolments != 0 {
		t.Fatalf("adding a person started an enrolment")
	}

	// The audit row is the route's own, linked by request id, marked as the
	// assistant's, with the operator as actor.
	var requestID string
	scanRow(t, `SELECT request_id FROM assistant_tool_calls WHERE tool_name = 'create_person' AND company_id = $1`, []any{f.companyID}, &requestID)
	var action, actorEmail, ua string
	scanRow(t, `SELECT action, actor_email, user_agent FROM audit_events WHERE request_id = $1`, []any{requestID}, &action, &actorEmail, &ua)
	if action != "PERSON_CREATED" || actorEmail != f.user.Email || !strings.HasPrefix(ua, "AccessLink-Assistant/1") {
		t.Fatalf("audit row = %s by %s via %q", action, actorEmail, ua)
	}
}

func TestAssistantHonoursSiteGrants(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	siteA := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))
	if err := database.ReplaceSiteGrants(f.companyID, f.user.ID, []string{siteA}); err != nil {
		t.Fatalf("granting site A: %v", err)
	}
	f.env.registerDevice(f.env.siteAKey, "AT-AS-A")
	f.env.registerDevice(f.env.siteBKey, "AT-AS-B")

	f.model.steps = []modelStep{
		call("toolu_1", "get_terminal", map[string]any{"serial": "AT-AS-B"}),
		say("no"),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "terminal B")
	if status := onlyEvent(t, events, assistant.EventToolResult).Data["status"]; status != models.ToolCallRefusedScope {
		t.Fatalf("site-scoped manager reading another site's terminal = %v, want REFUSED_SCOPE", status)
	}

	// And the list is the operator's scoped fleet, as the Terminals page shows it.
	f.model.steps = []modelStep{call("toolu_2", "list_terminals", map[string]any{}), say("ok")}
	f.send(t, conv, "list terminals")
	content := lastToolResultContent(t, conv)
	if !strings.Contains(content, "AT-AS-A") || strings.Contains(content, "AT-AS-B") {
		t.Fatalf("scoped list = %s", content)
	}
}

// --- tenant isolation -----------------------------------------------------------------

func TestAssistantCannotReachAnotherCompany(t *testing.T) {
	f := newAssistantFixture(t, models.RoleOwner)
	other := operatorCompanyID(t, "two")
	seedPerson(t, other, "P-THEIRS", "Their Person")

	f.model.steps = []modelStep{call("toolu_1", "get_person", map[string]any{"external_id": "P-THEIRS"}), say("no")}
	conv := f.newConversation(t)
	events := f.send(t, conv, "theirs")
	if status := onlyEvent(t, events, assistant.EventToolResult).Data["status"]; status != models.ToolCallNotFound {
		t.Fatalf("another company's person = %v, want NOT_FOUND", status)
	}
	if strings.Contains(lastToolResultContent(t, conv), "Their Person") {
		t.Fatalf("another company's data reached the model")
	}

	// Their operator cannot read our conversation, by id.
	_, theirToken, theirCSRF := consoleOperatorSession(t, f.env.router, other, "them@example.com", models.RoleOwner)
	code, _ := consoleCall(t, f.env.router, http.MethodGet, "/api/v1/console/assistant/conversations/"+conv, "", theirToken, theirCSRF)
	if code != http.StatusNotFound {
		t.Fatalf("another company reading our conversation = %d, want 404", code)
	}
}

// --- secrets never reach the model ---------------------------------------------------------

func TestAssistantProjectionsCarryNoSecrets(t *testing.T) {
	f := newAssistantFixture(t, models.RoleOwner)
	siteID := siteIDByKey(t, f.env.siteAKey)
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteID)
	serial := f.env.registerDevice(f.env.siteAKey, "AT-SEC-1")
	_ = serial
	seedPerson(t, f.companyID, "P-SEC", "Secret Test")
	// Every secret the platform mints, minted: a rotated site key, a claim
	// code, an integration credential.
	code, _ := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/sites/"+sitePub+"/api-key", "", f.token, f.csrf)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("rotating the site key = %d", code)
	}
	code, _ = consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/sites/"+sitePub+"/claim-codes", `{"serial_number":"AT-SEC-2"}`, f.token, f.csrf)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("issuing a claim code = %d", code)
	}
	seedIntegrationCredential(t, f.companyID, "secret-test")

	reads := []modelStep{
		call("t1", "list_sites", map[string]any{}),
		call("t2", "get_site", map[string]any{"site_id": sitePub}),
		call("t3", "list_terminals", map[string]any{}),
		call("t4", "get_terminal", map[string]any{"serial": "AT-SEC-1"}),
		call("t5", "get_fleet_summary", map[string]any{}),
		call("t6", "search_people", map[string]any{}),
		call("t7", "get_person", map[string]any{"external_id": "P-SEC"}),
		call("t8", "get_person_enrollment", map[string]any{"external_id": "P-SEC"}),
		call("t9", "list_schedules", map[string]any{}),
		call("t10", "list_events", map[string]any{}),
	}
	conv := f.newConversation(t)
	for _, step := range reads {
		f.model.steps = []modelStep{step, say("ok")}
		events := f.send(t, conv, "read")
		if status := onlyEvent(t, events, assistant.EventToolResult).Data["status"]; status != models.ToolCallExecuted {
			t.Fatalf("%s = %v", step.blocks[0].Name, status)
		}
	}

	var transcript string
	scanRow(t, `SELECT string_agg(m.content::text, ' ') FROM assistant_messages m
	               JOIN assistant_conversations c ON c.id = m.conversation_id WHERE c.public_id = $1::uuid`, []any{conv}, &transcript)
	for _, forbidden := range []string{"ats_", "atd_", "atp_", "api_key", "pairing_code", "claim_code", "announce_token", "csrf", "password", "provisioning key", "api_key_prefix"} {
		if strings.Contains(strings.ToLower(transcript), forbidden) {
			t.Errorf("the transcript contains %q", forbidden)
		}
	}
}

// --- confirmations ---------------------------------------------------------------------------

func (f *assistantFixture) confirm(t *testing.T, conv, token string, approve bool) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"token":%q,"approve":%v}`, token, approve)
	return f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/confirmations", body)
}

func TestAssistantGrantRequiresApprovalAndRunsOnce(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-GRANT", "Grant Person")
	sitePub := queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, siteIDByKey(t, f.env.siteAKey))

	f.model.steps = []modelStep{
		call("toolu_1", "grant_access", map[string]any{"external_id": "P-GRANT", "effect": "ALLOW", "scope_type": "SITE", "site_id": sitePub}),
		say("Waiting for your approval."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "let Grant Person in at site A")

	confirmation := onlyEvent(t, events, assistant.EventConfirmationRequired)
	token, _ := confirmation.Data["token"].(string)
	if !strings.HasPrefix(token, "v1.") {
		t.Fatalf("no token in confirmation.required: %v", confirmation.Data)
	}
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	if !strings.Contains(fmt.Sprint(consequence["title"]), "Let Grant Person in") {
		t.Fatalf("consequence = %v", consequence)
	}
	if onlyEvent(t, events, assistant.EventTurnCompleted).Data["stop_reason"] != "confirmation" {
		t.Fatalf("turn did not stop for confirmation: %v", eventTypes(events))
	}
	// The model never sees the token; it sees only that a confirmation was requested.
	if content := lastToolResultContent(t, conv); strings.Contains(content, token) || !strings.Contains(content, "confirmation_required") {
		t.Fatalf("tool result shown to the model = %s", content)
	}
	var rules int
	scanRow(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND action = 'PERMISSION_CREATED'`, []any{f.companyID}, &rules)
	if rules != 0 {
		t.Fatalf("the rule was created before approval")
	}

	// A tampered token is refused.
	if w := f.confirm(t, conv, token[:len(token)-2]+"xx", true); w.Code != http.StatusBadRequest {
		t.Fatalf("tampered token = %d: %s", w.Code, w.Body.String())
	}

	// Approval runs it, exactly once, and the assistant narrates.
	f.model.steps = []modelStep{say("Done — Grant Person can get in at Site A.")}
	w := f.confirm(t, conv, token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	approved := parseSSE(t, w.Body.String())
	if status := onlyEvent(t, approved, assistant.EventToolResult).Data["status"]; status != models.ToolCallConfirmedExecuted {
		t.Fatalf("approved grant = %v", status)
	}
	scanRow(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND action = 'PERMISSION_CREATED'`, []any{f.companyID}, &rules)
	if rules != 1 {
		t.Fatalf("rules created = %d, want 1", rules)
	}
	// Linked: the audit row's request id is the confirmed tool call's.
	var linked int
	scanRow(t, `SELECT count(*) FROM audit_events a JOIN assistant_tool_calls c ON c.request_id = a.request_id
	              WHERE a.action = 'PERMISSION_CREATED' AND c.status = 'CONFIRMED_EXECUTED' AND c.company_id = $1`, []any{f.companyID}, &linked)
	if linked != 1 {
		t.Fatalf("audit row not linked to the confirmed tool call")
	}

	// A second approval of the same token does nothing.
	if w := f.confirm(t, conv, token, true); w.Code != http.StatusConflict {
		t.Fatalf("second approval = %d: %s", w.Code, w.Body.String())
	}
	scanRow(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND action = 'PERMISSION_CREATED'`, []any{f.companyID}, &rules)
	if rules != 1 {
		t.Fatalf("a token ran twice: %d rules", rules)
	}
	statuses := toolCallStatuses(t, f.companyID)
	if statuses[models.ToolCallConfirmationRequested] != 1 || statuses[models.ToolCallConfirmedExecuted] != 1 {
		t.Fatalf("tool call statuses = %v", statuses)
	}
}

func TestAssistantRejectedConfirmationRunsNothing(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-REJ", "Rejected Person")
	f.model.steps = []modelStep{
		call("toolu_1", "grant_access", map[string]any{"external_id": "P-REJ", "effect": "DENY", "scope_type": "COMPANY"}),
		say("Waiting."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "keep them out everywhere")
	confirmation := onlyEvent(t, events, assistant.EventConfirmationRequired)
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	// The company-wide warning the dialog shows is shown here too.
	if !strings.Contains(fmt.Sprint(consequence["warnings"]), "do not exist yet") {
		t.Fatalf("company-wide warning missing: %v", consequence)
	}
	token, _ := confirmation.Data["token"].(string)

	f.model.steps = []modelStep{say("Understood, nothing was changed.")}
	w := f.confirm(t, conv, token, false)
	if w.Code != http.StatusOK {
		t.Fatalf("reject = %d: %s", w.Code, w.Body.String())
	}
	var rules int
	scanRow(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND action = 'PERMISSION_CREATED'`, []any{f.companyID}, &rules)
	if rules != 0 {
		t.Fatalf("a rejected confirmation created a rule")
	}
	if statuses := toolCallStatuses(t, f.companyID); statuses[models.ToolCallConfirmationRejected] != 1 {
		t.Fatalf("tool call statuses = %v", statuses)
	}
	// And the token is spent.
	if w := f.confirm(t, conv, token, true); w.Code != http.StatusConflict {
		t.Fatalf("approving after rejecting = %d", w.Code)
	}
}

func TestAssistantConfirmationExpiresAndIsBoundToTheSession(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-EXP", "Expiring Person")
	f.model.steps = []modelStep{
		call("toolu_1", "grant_access", map[string]any{"external_id": "P-EXP", "effect": "ALLOW", "scope_type": "COMPANY"}),
		say("Waiting."),
	}
	conv := f.newConversation(t)
	token, _ := onlyEvent(t, f.send(t, conv, "grant"), assistant.EventConfirmationRequired).Data["token"].(string)

	// Another session of the same operator cannot use it.
	otherToken, otherCSRF := login(t, f.env.router, f.user.Email, testPassword)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/confirmations",
		strings.NewReader(fmt.Sprintf(`{"token":%q,"approve":true}`, token)))
	for k, v := range sessionHeaders(otherToken) {
		req.Header.Set(k, v)
	}
	req.Header.Set(middleware.CSRFHeader, otherCSRF)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.env.router.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("another session approving = %d: %s", w.Code, w.Body.String())
	}

	// Expired: refused, recorded as such, and still never run.
	mustExec(t, `UPDATE assistant_confirmations SET expires_at = CURRENT_TIMESTAMP - interval '1 minute'`)
	if w := f.confirm(t, conv, token, true); w.Code != http.StatusGone {
		t.Fatalf("expired approval = %d: %s", w.Code, w.Body.String())
	}
	var rules int
	scanRow(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND action = 'PERMISSION_CREATED'`, []any{f.companyID}, &rules)
	if rules != 0 {
		t.Fatalf("an expired confirmation ran")
	}
	if statuses := toolCallStatuses(t, f.companyID); statuses[models.ToolCallConfirmationExpired] != 1 {
		t.Fatalf("tool call statuses = %v", statuses)
	}
}

func TestAssistantRevokeRequiresApproval(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-REV", "Revoke Person")
	rule, err := database.GrantPermission(f.companyID, "P-REV", models.PermissionRequest{ScopeType: "COMPANY", Effect: "ALLOW"})
	if err != nil {
		t.Fatalf("granting: %v", err)
	}
	f.model.steps = []modelStep{
		call("toolu_1", "revoke_access", map[string]any{"external_id": "P-REV", "rule_id": rule.ID}),
		say("Waiting."),
	}
	conv := f.newConversation(t)
	confirmation := onlyEvent(t, f.send(t, conv, "remove it"), assistant.EventConfirmationRequired)
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	if !strings.Contains(fmt.Sprint(consequence["title"]), "Remove Revoke Person's access to everywhere") {
		t.Fatalf("consequence = %v", consequence)
	}
	token, _ := confirmation.Data["token"].(string)
	f.model.steps = []modelStep{say("Removed.")}
	if w := f.confirm(t, conv, token, true); w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	var remaining int
	scanRow(t, `SELECT count(*) FROM permissions WHERE public_id = $1::uuid AND deleted_at IS NULL`, []any{rule.ID}, &remaining)
	if remaining != 0 {
		t.Fatalf("the rule is still active after an approved revoke")
	}
}

// --- enrolment: never automatic, always handed off -------------------------------------------

func TestAssistantEnrollmentIsConfirmedThenHandedOff(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	seedPerson(t, f.companyID, "P-ENR", "Enrol Person")
	f.env.registerDevice(f.env.siteAKey, "AT-ENR-1")
	mustExec(t, `UPDATE devices SET device_name = 'Reception' WHERE serial_number = 'AT-ENR-1'`)

	f.model.steps = []modelStep{
		call("toolu_1", "start_enrollment", map[string]any{"external_id": "P-ENR", "serial": "AT-ENR-1"}),
		say("Ready when you are."),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "enrol them at Reception")
	confirmation := onlyEvent(t, events, assistant.EventConfirmationRequired)
	consequence, _ := confirmation.Data["consequence"].(map[string]any)
	if !strings.HasPrefix(fmt.Sprint(consequence["title"]), "Ready to start fingerprint enrollment at Reception") {
		t.Fatalf("consequence = %v", consequence)
	}
	var enrolments int
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if enrolments != 0 {
		t.Fatalf("an enrolment started before approval")
	}

	token, _ := confirmation.Data["token"].(string)
	f.model.steps = []modelStep{say("Reception is waiting for their finger.")}
	w := f.confirm(t, conv, token, true)
	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d: %s", w.Code, w.Body.String())
	}
	approved := parseSSE(t, w.Body.String())
	handoff := onlyEvent(t, approved, assistant.EventHandoff)
	if handoff.Data["kind"] != "enrolment" || handoff.Data["route"] != "/people/P-ENR?enrol=1" {
		t.Fatalf("handoff = %v", handoff.Data)
	}
	mustScan(t, `SELECT count(*) FROM enrollment_requests`, &enrolments)
	if enrolments != 1 {
		t.Fatalf("enrolments after approval = %d", enrolments)
	}

	// wait_for_enrollment reports the live state and keeps the hand-off.
	f.model.steps = []modelStep{call("toolu_2", "wait_for_enrollment", map[string]any{"external_id": "P-ENR", "timeout_s": 1}), say("Still waiting.")}
	events = f.send(t, conv, "how is it going")
	if status := onlyEvent(t, events, assistant.EventToolResult).Data["status"]; status != models.ToolCallExecuted {
		t.Fatalf("wait_for_enrollment = %v", status)
	}
	if !strings.Contains(lastToolResultContent(t, conv), `"status":"PENDING"`) {
		t.Fatalf("wait_for_enrollment result = %s", lastToolResultContent(t, conv))
	}
}

// --- limits, budget, session expiry --------------------------------------------------------------

func TestAssistantStopsAtTheToolLimit(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	limits := assistant.DefaultLimits()
	limits.MaxToolRounds = 3
	f.install(limits)
	for i := 0; i < 10; i++ {
		f.model.steps = append(f.model.steps, call(fmt.Sprintf("toolu_%d", i), "get_fleet_summary", map[string]any{}))
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "loop")
	failed := onlyEvent(t, events, assistant.EventTurnFailed)
	if failed.Data["code"] != assistant.FailToolLimit {
		t.Fatalf("turn.failed = %v", failed.Data)
	}
	if f.model.calls != 3 {
		t.Fatalf("model called %d times, want 3", f.model.calls)
	}
}

func TestAssistantRefusesWhenTheBudgetIsSpent(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	limits := assistant.DefaultLimits()
	limits.CompanyMonthlyTokens = 1000
	f.install(limits)
	mustExec(t, `INSERT INTO assistant_company_usage (company_id, period, input_tokens, output_tokens)
	             VALUES ($1, date_trunc('month', CURRENT_DATE)::date, 900, 200)`, f.companyID)
	conv := f.newConversation(t)
	events := f.send(t, conv, "anything")
	if failed := onlyEvent(t, events, assistant.EventTurnFailed); failed.Data["code"] != assistant.FailBudgetExhausted {
		t.Fatalf("turn.failed = %v", failed.Data)
	}
	if f.model.calls != 0 {
		t.Fatalf("the model was called %d times over budget", f.model.calls)
	}
}

func TestAssistantOneTurnAtATime(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	conv := f.newConversation(t)
	release, ok := f.service.BeginTurn(f.user.ID)
	if !ok {
		t.Fatalf("could not claim the turn slot")
	}
	defer release()
	w := f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/messages", `{"text":"hi"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("second concurrent turn = %d, want 409", w.Code)
	}
}

func TestAssistantIsRateLimitedPerSession(t *testing.T) {
	t.Setenv("ASSISTANT_RATE_PER_MINUTE", "3")
	f := newAssistantFixture(t, models.RoleManager)
	conv := f.newConversation(t)
	codes := []int{}
	for i := 0; i < 5; i++ {
		f.model.steps = []modelStep{say("ok")}
		w := f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/messages", `{"text":"hi"}`)
		codes = append(codes, w.Code)
	}
	if codes[4] != http.StatusTooManyRequests {
		t.Fatalf("fifth message in a minute = %v, want a 429", codes)
	}
}

func TestAssistantStopsWhenTheSessionEnds(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	f.model.steps = []modelStep{
		call("toolu_1", "get_fleet_summary", map[string]any{}),
		{blocks: []models.AssistantBlock{{Type: "tool_use", ID: "toolu_2", Name: "get_fleet_summary", Input: json.RawMessage(`{}`)}},
			before: func() {
				mustExec(t, `UPDATE user_sessions SET idle_expires_at = CURRENT_TIMESTAMP - interval '1 hour'`)
			}},
		say("should not be reached"),
	}
	conv := f.newConversation(t)
	events := f.send(t, conv, "summary twice")
	results := findEvents(events, assistant.EventToolResult)
	if len(results) != 2 || results[0].Data["status"] != models.ToolCallExecuted || results[1].Data["status"] != models.ToolCallFailed {
		t.Fatalf("tool results = %v", results)
	}
	if failed := onlyEvent(t, events, assistant.EventTurnFailed); failed.Data["code"] != assistant.FailSessionExpired {
		t.Fatalf("turn.failed = %v", failed.Data)
	}
	if f.model.calls != 2 {
		t.Fatalf("the model was called %d times after the session ended, want 2", f.model.calls)
	}
}

func TestAssistantReplaysAResentMessage(t *testing.T) {
	f := newAssistantFixture(t, models.RoleManager)
	f.model.steps = []modelStep{say("First answer.")}
	conv := f.newConversation(t)
	body := `{"text":"hello","client_message_id":"cm-1"}`
	w := f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/messages", body)
	if w.Code != http.StatusOK {
		t.Fatalf("send = %d", w.Code)
	}
	w = f.request(t, http.MethodPost, "/api/v1/console/assistant/conversations/"+conv+"/messages", body)
	events := parseSSE(t, w.Body.String())
	if msg := onlyEvent(t, events, assistant.EventAssistantMessage); msg.Data["text"] != "First answer." {
		t.Fatalf("replay = %v", msg.Data)
	}
	if completed := onlyEvent(t, events, assistant.EventTurnCompleted); completed.Data["replayed"] != true {
		t.Fatalf("replay not marked: %v", completed.Data)
	}
	if f.model.calls != 1 {
		t.Fatalf("the model ran %d times for one message", f.model.calls)
	}
}
