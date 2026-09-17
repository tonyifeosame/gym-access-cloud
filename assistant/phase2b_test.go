package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"access-terminal-cloud-api/models"
)

// Phase 2b's own guarantees, without a database: the command plane stays
// closed, the audit projection drops the two columns it must, the four new
// cards say what will happen, and nothing here grew an argument that could
// widen what a tool reaches.

// --- the command plane stays closed -------------------------------------------------

func TestDeviceTestOffersExactlyTheSupportedTargetsAndNeverTheRelay(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	tool, ok := r.Get("run_device_test")
	if !ok {
		t.Fatal("run_device_test is not registered")
	}
	var target Param
	for _, p := range tool.Params {
		if p.Name == "target" {
			target = p
		}
	}
	// THE ENUM IS THE PLATFORM'S OWN LIST, not a copy that can drift.
	if strings.Join(target.Enum, ",") != strings.Join(models.DeviceTestTargets, ",") {
		t.Fatalf("target enum = %v, want models.DeviceTestTargets %v", target.Enum, models.DeviceTestTargets)
	}
	if !target.Required {
		t.Error("target is optional; a device test with no target must not be expressible")
	}
	for _, forbidden := range []string{"relay", "strike", "lock", "door", "unlock"} {
		for _, allowed := range target.Enum {
			if strings.Contains(allowed, forbidden) {
				t.Errorf("run_device_test offers %q", allowed)
			}
		}
	}
	// A target outside the set is refused before any request.
	for _, bad := range []string{"relay", "RELAY", "self-test", ""} {
		raw, _ := json.Marshal(map[string]any{"serial": "AT-1", "target": bad})
		if _, err := tool.ValidateArgs(raw); err == nil {
			t.Errorf("run_device_test accepted target %q", bad)
		}
	}
	for _, good := range models.DeviceTestTargets {
		raw, _ := json.Marshal(map[string]any{"serial": "AT-1", "target": good})
		if _, err := tool.ValidateArgs(raw); err != nil {
			t.Errorf("run_device_test refused target %q: %v", good, err)
		}
	}
}

func TestNoToolTakesACommandTypeOrArbitraryParameters(t *testing.T) {
	// THE GENERIC COMMAND PLANE IS NOT EXPOSED. A tool that took a command
	// name, a parameter object or a free-form body would turn the registry's
	// two named operations back into "issue anything the router accepts".
	r := NewRegistry()
	registerTools(r)
	freeText := map[string]bool{"search_people": true, "list_events": true}
	for _, d := range r.ForRole(models.RoleOwner) {
		tool, _ := r.Get(d.Name)
		for _, p := range tool.Params {
			switch p.Name {
			case "query":
				// Free text over names and serials, not a query language.
				if !freeText[d.Name] {
					t.Errorf("%s takes a %q argument", d.Name, p.Name)
				}
			case "type", "command_type", "command", "params", "parameters", "body", "payload",
				"method", "endpoint", "sql", "route", "path", "url":
				t.Errorf("%s takes a %q argument", d.Name, p.Name)
			}
			if p.Type != "string" && p.Type != "integer" && p.Type != "boolean" {
				t.Errorf("%s: %s has type %q", d.Name, p.Name, p.Type)
			}
		}
	}
}

func TestWithdrawIsASafeWriteAndTheTwoIssuersAreNotReads(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	withdraw, _ := r.Get("withdraw_command")
	if withdraw.ReadOnly {
		t.Error("withdraw_command is marked read-only but it writes")
	}
	if withdraw.Confirm != nil {
		t.Error("withdraw_command asks for approval; making a terminal do less should not")
	}
	if !withdraw.Idempotent {
		t.Error("withdraw_command should be idempotent: withdrawing twice is the same state")
	}
	for _, name := range []string{"request_diagnostic", "run_device_test"} {
		tool, _ := r.Get(name)
		if tool.ReadOnly {
			t.Errorf("%s is marked read-only", name)
		}
	}
	// A device test asks; a diagnostic, which nobody in the room perceives,
	// does not. That distinction is the whole of why they are two tools.
	if test, _ := r.Get("run_device_test"); test.Confirm == nil {
		t.Error("run_device_test does not ask first")
	}
	if diag, _ := r.Get("request_diagnostic"); diag.Confirm != nil {
		t.Error("request_diagnostic asks first; it is invisible at the terminal")
	}
}

// --- the audit projection -----------------------------------------------------------

func TestAuditProjectionDropsAddressesAndTheFreeFormChangesColumn(t *testing.T) {
	row := object{
		"id":           "aud-1",
		"action":       "SITE_KEY_ROTATED",
		"actor_email":  "ops@example.com",
		"actor_role":   "ADMIN",
		"target_type":  "SITE",
		"target_id":    "a7f3c2e1-0000-4000-8000-000000000000",
		"target_label": "Lagos",
		"occurred_at":  "2026-09-17T09:00:00Z",
		// The two that must not come through, with the shapes that make them
		// dangerous: an operator's address, and a changes column carrying a
		// credential prefix and an announcing terminal's address.
		"ip_address": "203.0.113.9",
		"changes": object{
			"api_key_prefix": "ats_9f2c",
			"code_prefix":    "K7M2",
			"first_seen_ip":  "198.51.100.7",
		},
	}
	got := pick(row, auditFields...)
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"ip_address", "203.0.113.9", "changes", "api_key_prefix",
		"ats_9f2c", "code_prefix", "K7M2", "first_seen_ip", "198.51.100.7"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("the audit projection carries %q: %s", forbidden, encoded)
		}
	}
	for _, wanted := range []string{"SITE_KEY_ROTATED", "ops@example.com", "ADMIN", "Lagos"} {
		if !strings.Contains(string(encoded), wanted) {
			t.Errorf("the audit projection lacks %s: %s", wanted, encoded)
		}
	}
}

func TestPendingDecisionProjectionAddsThePlaceAndNothingElse(t *testing.T) {
	row := object{
		"id": "p-1", "serial_number": "AT-1", "state": "APPROVED", "verdict": "NEW",
		"firmware_version": "1.3.5", "announced_at": "2026-09-17T09:00:00Z",
		"site_name": "Lagos", "device_name": "Reception",
		// Everything the announcement carries that must not be shown.
		"pairing_code": "K7M2-P4QX", "announce_token": "tok", "first_seen_ip": "198.51.100.7",
		"last_seen_ip": "198.51.100.7", "adopted_by": "ops@example.com",
		"hardware_revision": "rev-c", "capabilities": []any{"cmd_device_test"},
		"approved_by": "ops@example.com",
	}
	encoded, _ := json.Marshal(pick(row, pendingDecisionFields...))
	for _, forbidden := range []string{"pairing_code", "K7M2", "announce_token", "first_seen_ip",
		"last_seen_ip", "198.51.100.7", "adopted_by", "hardware_revision", "capabilities", "approved_by"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("the pending decision projection carries %q: %s", forbidden, encoded)
		}
	}
	for _, wanted := range []string{"AT-1", "Lagos", "Reception", "APPROVED"} {
		if !strings.Contains(string(encoded), wanted) {
			t.Errorf("the pending decision projection lacks %s: %s", wanted, encoded)
		}
	}
}

// --- what the four new cards say -----------------------------------------------------

func TestPhase2bConsequenceWording(t *testing.T) {
	// A device test names the room and says nobody is admitted by it.
	c := deviceTestConsequence("Reception", "Lagos", models.DeviceTestBuzzer, "ONLINE")
	if c.Title != "Have Reception (Lagos) sound its buzzer?" {
		t.Errorf("device test title = %q", c.Title)
	}
	if !strings.Contains(c.Body, "Anybody standing at it will notice") ||
		!strings.Contains(c.Body, "admits nobody, refuses nobody and opens nothing") {
		t.Errorf("device test body = %q", c.Body)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("an online terminal warned: %v", c.Warnings)
	}
	selfTest := deviceTestConsequence("Loading Bay", "", models.DeviceTestSelfTest, "ONLINE")
	if !strings.Contains(selfTest.Title, "self-test of its buzzer and display") {
		t.Errorf("self-test title = %q", selfTest.Title)
	}
	if selfTest.Title != "Have Loading Bay run a self-test of its buzzer and display?" {
		t.Errorf("a terminal with no site name = %q", selfTest.Title)
	}

	// THE CARD NEVER WARNS THAT A TEST MAY NOT BE COLLECTED. A terminal that
	// could not collect one never reaches a card (the preflight below), and
	// the two states the store does accept are described as what they are:
	// still in contact, the test will arrive.
	for _, refused := range []string{"OFFLINE", "PROVISIONING", "DISABLED"} {
		c := deviceTestConsequence("Loading Bay", "", models.DeviceTestBuzzer, refused)
		if len(c.Warnings) != 0 {
			t.Errorf("%s produced a card warning at all: %v", refused, c.Warnings)
		}
	}
	for status, want := range map[string]string{
		"ERROR":    "the test will reach it",
		"UPDATING": "the test will reach it",
	} {
		c := deviceTestConsequence("Loading Bay", "", models.DeviceTestBuzzer, status)
		joined := strings.Join(c.Warnings, " ")
		if !strings.Contains(joined, want) {
			t.Errorf("%s note = %v", status, c.Warnings)
		}
		if strings.Contains(joined, "may not collect") || strings.Contains(joined, "rather than online") {
			t.Errorf("%s is accepted by the store but the card warns it might not be: %v", status, c.Warnings)
		}
	}

	// Deleting a schedule states the count the approval rests on.
	d := deleteScheduleConsequence("Weekend", 0)
	if d.Title != "Delete Weekend?" || !strings.Contains(d.Body, "0 access rules refer to this schedule") {
		t.Errorf("delete consequence = %+v", d)
	}
	if !strings.Contains(strings.Join(d.Warnings, " "), "refused rather than quietly widening") {
		t.Errorf("delete warnings = %v", d.Warnings)
	}

	// Approving names the serial, the site and -- for a re-provision -- what
	// it replaces, and says no credential is issued here.
	a := approveTerminalConsequence("AT-1", "Lagos", "Reception", "NEW", "")
	if a.Title != "Set AT-1 up at Lagos?" || !strings.Contains(a.Body, "as Reception at Lagos") ||
		!strings.Contains(a.Body, "no credential is shown here") {
		t.Errorf("approve consequence = %+v", a)
	}
	if strings.Contains(strings.Join(a.Warnings, " "), "re-provisions") {
		t.Errorf("a NEW terminal carried a re-provision warning: %v", a.Warnings)
	}
	re := approveTerminalConsequence("AT-1", "Lagos", "", "RE_PROVISION", "Old Reception")
	joined := strings.Join(re.Warnings, " ")
	if !strings.Contains(joined, "Old Reception") || !strings.Contains(joined, "stops working") {
		t.Errorf("re-provision warnings = %v", re.Warnings)
	}
	if !strings.Contains(re.Body, "as AT-1 at Lagos") {
		t.Errorf("a blank device name should fall back to the serial: %q", re.Body)
	}

	// Rejecting is two different sentences, and says so.
	waiting := rejectTerminalConsequence("AT-1", "ADOPTED")
	if waiting.Title != "Refuse AT-1?" || !strings.Contains(waiting.Body, "nothing it could do is taken away") {
		t.Errorf("reject-waiting consequence = %+v", waiting)
	}
	undo := rejectTerminalConsequence("AT-1", "APPROVED")
	if undo.Title != "Undo the approval of AT-1?" || !strings.Contains(undo.Body, "releases the serial") {
		t.Errorf("reject-approved consequence = %+v", undo)
	}
	if !strings.Contains(strings.Join(undo.Warnings, " "), "will not come into service") {
		t.Errorf("undo warnings = %v", undo.Warnings)
	}
}

func TestAnnouncementStatesAreExplainedRatherThanNumbered(t *testing.T) {
	// Every state the route can be sitting in has words of its own, and the
	// one that means "type the pairing code" says the assistant cannot.
	cases := map[string]string{
		"PENDING":    "type the pairing",
		"APPROVED":   "already been approved",
		"REJECTED":   "already been rejected",
		"EXPIRED":    "pairing code has lapsed",
		"COLLECTED":  "already been set up",
		"SUPERSEDED": "announced itself again",
		"WHATEVER":   "not waiting to be set up",
	}
	for state, want := range cases {
		got := unapprovableText("AT-1", state)
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q does not mention %q", state, got, want)
		}
		if !strings.Contains(got, "AT-1") {
			t.Errorf("%s: %q does not name the terminal", state, got)
		}
	}
	if !strings.Contains(unapprovableText("AT-1", "PENDING"), "not something the assistant can do") {
		t.Error("a PENDING row must say adoption is not the assistant's to do")
	}
}

// --- arguments ------------------------------------------------------------------------

func TestPhase2bIdentifiersCannotRerouteARequest(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	// Everything Phase 2b puts in a path is marked Identifier.
	inPath := map[string][]string{
		"withdraw_command":         {"serial", "command_id"},
		"run_device_test":          {"serial"},
		"delete_schedule":          {"schedule_id"},
		"approve_pending_terminal": {"pending_id", "site_id"},
		"reject_pending_terminal":  {"pending_id"},
	}
	for name, params := range inPath {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		for _, want := range params {
			found := false
			for _, p := range tool.Params {
				if p.Name != want {
					continue
				}
				found = true
				if !p.Identifier {
					t.Errorf("%s: %s is placed in a path but is not marked Identifier", name, want)
				}
			}
			if !found {
				t.Errorf("%s has no %s parameter", name, want)
			}
		}
	}
	// And the validator refuses anything that would change which route is reached.
	withdraw, _ := r.Get("withdraw_command")
	for _, bad := range []string{"c1/../../people/P-1", "c1%2F..%2Fx", "c1?x=1", "c1#f", "c 1"} {
		raw, _ := json.Marshal(map[string]any{"serial": "AT-1", "command_id": bad})
		if _, err := withdraw.ValidateArgs(raw); err == nil {
			t.Errorf("withdraw_command accepted command_id %q", bad)
		}
	}
	approve, _ := r.Get("approve_pending_terminal")
	for _, bad := range []string{"p-1/approve", "p-1%2Freject", "../terminals"} {
		raw, _ := json.Marshal(map[string]any{"pending_id": bad, "site_id": "s-1"})
		if _, err := approve.ValidateArgs(raw); err == nil {
			t.Errorf("approve_pending_terminal accepted pending_id %q", bad)
		}
	}
	// A device name is free text, so it is NOT an identifier -- it never
	// reaches a path -- but it is bounded.
	for _, p := range approve.Params {
		if p.Name == "device_name" && (p.Identifier || p.MaxLen == 0) {
			t.Errorf("device_name = %+v", p)
		}
	}
}

func TestListAuditRefusesAnInstantItCannotRead(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	audit, _ := r.Get("list_audit")
	if audit.MinRole != models.RoleAdmin {
		t.Errorf("list_audit MinRole = %q, want ADMIN", audit.MinRole)
	}
	if !audit.ReadOnly {
		t.Error("list_audit is not marked read-only")
	}
	for _, bad := range []string{"last week", "2026-09-15", "2026-09-15 00:00:00"} {
		if err := validInstant("since", bad); err == nil {
			t.Errorf("since accepted %q", bad)
		} else if !strings.Contains(err.Error(), "since must be an RFC 3339 instant") {
			t.Errorf("since %q: %v", bad, err)
		}
	}
	for _, ok := range []string{"", "  ", "2026-09-15T00:00:00Z", "2026-09-15T00:00:00+01:00"} {
		if err := validInstant("until", ok); err != nil {
			t.Errorf("until refused %q: %v", ok, err)
		}
	}
	// The bound the route clamps to is the bound the schema states.
	for _, p := range audit.Params {
		if p.Name == "limit" && (p.Max == nil || *p.Max != 200) {
			t.Errorf("limit bound = %+v", p)
		}
	}
	if _, err := audit.ValidateArgs(json.RawMessage(`{"limit":500}`)); err == nil {
		t.Error("list_audit accepted limit 500")
	}
}

// --- the device-test preflight ----------------------------------------------------------

// The four conditions database/commands.go's commandTarget.deliverable
// refuses, checked here in its order, so a card is never built for a test the
// route would turn down. Each case is the shape of the console body the tool
// actually reads.

func TestDeviceTestPreflightMirrorsTheStoresRefusals(t *testing.T) {
	online := func(extra object) object {
		m := object{"serial_number": "AT-1", "device_name": "Reception", "status": "ONLINE", "active": true,
			"health": object{"credential_active": true}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// Switched off, by either column.
	for _, off := range []object{{"status": "DISABLED"}, {"active": false}} {
		if why := terminalCannotBeCommanded(online(off), "Reception"); !strings.Contains(why, "is disabled") {
			t.Errorf("%v = %q, want a disabled refusal", off, why)
		}
	}
	// No credential.
	noKey := online(object{"health": object{"credential_active": false}})
	if why := terminalCannotBeCommanded(noKey, "Reception"); !strings.Contains(why, "holds no credential") {
		t.Errorf("uncredentialed = %q", why)
	}
	// ABSENT IS NOT FALSE: a body without the health block, or without
	// `active`, must not be refused here -- the route still checks.
	bare := object{"serial_number": "AT-1", "status": "ONLINE"}
	if why := terminalCannotBeCommanded(bare, "Reception"); why != "" {
		t.Errorf("a body with no health block was refused: %q", why)
	}
	if why := terminalCannotBeCommanded(online(nil), "Reception"); why != "" {
		t.Errorf("a healthy terminal was refused: %q", why)
	}

	// Not in contact. UPDATING and ERROR are accepted by the store and so are
	// accepted here; OFFLINE and PROVISIONING are not.
	for _, ok := range []string{"ONLINE", "UPDATING", "ERROR"} {
		if why := terminalIsNotInContact(object{"status": ok}, "Reception"); why != "" {
			t.Errorf("%s was refused as out of contact: %q", ok, why)
		}
	}
	for _, refused := range []string{"OFFLINE", "PROVISIONING", ""} {
		why := terminalIsNotInContact(object{"status": refused}, "Reception")
		if !strings.Contains(why, "not in contact") || !strings.Contains(why, "nothing would be sent") {
			t.Errorf("%q = %q, want an out-of-contact refusal", refused, why)
		}
	}
}

func TestDeviceTestPreflightDistinguishesNeverReportedFromCannot(t *testing.T) {
	offer := func(supported bool) []any {
		return []any{object{"type": models.CommandDeviceTest, "supported": supported},
			object{"type": models.CommandDiagnosticSnapshot, "supported": true}}
	}
	// Reported, and can.
	can := object{"capabilities_reported_at": "2026-09-17T09:00:00Z", "commands": offer(true)}
	if why := terminalCannotRunDeviceTest(can, "Reception"); why != "" {
		t.Errorf("a capable terminal was refused: %q", why)
	}
	// Reported, and cannot.
	cannot := object{"capabilities_reported_at": "2026-09-17T09:00:00Z", "commands": offer(false)}
	why := terminalCannotRunDeviceTest(cannot, "Reception")
	if !strings.Contains(why, "reported what it can do") || !strings.Contains(why, "nothing would be sent") {
		t.Errorf("an incapable terminal = %q", why)
	}
	// Never reported. SILENCE IS NOT CONSENT, and it is not the same message.
	silent := object{"commands": offer(false)}
	why = terminalCannotRunDeviceTest(silent, "Reception")
	if !strings.Contains(why, "never reported") || !strings.Contains(why, "nothing would be sent") {
		t.Errorf("a silent terminal = %q", why)
	}
	if why == terminalCannotRunDeviceTest(cannot, "Reception") {
		t.Error("never-reported and cannot are told apart by the store and must be here too")
	}
	// The command missing from the offer list entirely is a refusal, not a card.
	if why := terminalCannotRunDeviceTest(object{"commands": []any{}}, "Reception"); why == "" {
		t.Error("a platform offering no hardware test still built a card")
	}
}

// --- the reject reason --------------------------------------------------------------------

func TestRejectReasonNeverExceedsTheRouteOrSplitsARune(t *testing.T) {
	// The route stores at most maxRejectReasonBytes and cuts by BYTE, so the
	// assistant must arrive inside the limit and on a rune boundary.
	cases := []string{
		"",
		"wrong site",
		strings.Repeat("a", 500),
		// Multi-byte, at the lengths that straddle the cut. "é" is two bytes
		// and "→" is three, so at least one of these lands mid-rune under a
		// naive byte slice.
		strings.Repeat("é", 200),
		strings.Repeat("→", 200),
		strings.Repeat("a", 188) + strings.Repeat("é", 10),
		strings.Repeat("a", 187) + strings.Repeat("→", 10),
		strings.Repeat("🚪", 60),
	}
	for _, words := range cases {
		got := assistantReasonWithin(words, maxRejectReasonBytes)
		if len(got) > maxRejectReasonBytes {
			t.Errorf("%d-byte reason produced %d bytes", len(words), len(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("%d-byte reason produced invalid UTF-8: %q", len(words), got)
		}
		if !strings.HasPrefix(got, "assistant") {
			t.Errorf("provenance lost: %q", got)
		}
	}
	// Inside the limit it is exactly assistantReason, unchanged.
	if got := assistantReasonWithin("wrong site", maxRejectReasonBytes); got != "assistant: wrong site" {
		t.Errorf("short reason = %q", got)
	}
	if got := assistantReasonWithin("", maxRejectReasonBytes); got != "assistant" {
		t.Errorf("empty reason = %q", got)
	}
	// A naive byte cut of the same input WOULD split a rune -- which is the
	// bug this guards, stated so the test fails if the helper stops helping.
	naive := assistantReason(strings.Repeat("é", 200))[:maxRejectReasonBytes]
	if utf8.ValidString(naive) {
		t.Error("this fixture no longer straddles the cut, so it proves nothing; choose one that does")
	}
}
