package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/models"
)

// Phase 2a's own guarantees, without a database: every write names what it
// changes, every hand-off is one of five routes, window text parses into
// exactly the route's shape, projections drop what they must, and a wait
// is bounded by the polling allowance rather than the call budget.

func TestEveryWriteDeclaresDomainsAndNoReadDoes(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	for _, d := range r.ForRole(models.RoleOwner) {
		tool, _ := r.Get(d.Name)
		switch {
		case tool.ReadOnly && len(tool.Domains) > 0:
			t.Errorf("%s is read-only but declares domains %v", d.Name, tool.Domains)
		case !tool.ReadOnly && len(tool.Domains) == 0:
			t.Errorf("%s writes but declares no domains", d.Name)
		}
		for _, dom := range tool.Domains {
			if !knownDomains[dom] {
				t.Errorf("%s declares unknown domain %q", d.Name, dom)
			}
		}
	}
	// The console's fallback map is the write tools and only them.
	effects := r.Effects(models.RoleOwner)
	for name, domains := range effects {
		tool, _ := r.Get(name)
		if tool.ReadOnly || len(domains) == 0 {
			t.Errorf("Effects lists %s with %v", name, domains)
		}
	}
	if _, ok := effects["search_people"]; ok {
		t.Errorf("Effects lists a read")
	}
	// Each write's domains cover the screens it changes.
	want := map[string][]string{
		"create_person":      {DomainPeople, DomainOnboarding, DomainAudit},
		"update_person":      {DomainPeople, DomainAudit},
		"set_person_active":  {DomainPeople, DomainOnboarding, DomainAudit},
		"grant_access":       {DomainPermissions, DomainOnboarding, DomainAudit},
		"revoke_access":      {DomainPermissions, DomainOnboarding, DomainAudit},
		"create_schedule":    {DomainSchedules, DomainAudit},
		"update_schedule":    {DomainSchedules, DomainPermissions, DomainAudit},
		"start_enrollment":   {DomainPeople, DomainAudit},
		"cancel_enrollment":  {DomainPeople, DomainAudit},
		"resync_terminal":    {DomainTerminals, DomainAudit},
		"request_diagnostic": {DomainTerminals, DomainAudit},
	}
	for name, domains := range want {
		if got := strings.Join(r.Domains(name), ","); got != strings.Join(domains, ",") {
			t.Errorf("%s domains = %s, want %s", name, got, strings.Join(domains, ","))
		}
	}
	if r.Domains("evaluate_access") != nil || r.Domains("explain_denial") != nil {
		t.Errorf("the evaluation preview must not be treated as a write")
	}
	// A VIEWER's map is empty: they see no write.
	if len(r.Effects(models.RoleViewer)) != 0 {
		t.Errorf("VIEWER effects = %v", r.Effects(models.RoleViewer))
	}
}

func TestRegistryRefusesAWriteWithoutDomains(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a write without domains was registered")
		}
	}()
	r := NewRegistry()
	r.Register(&Tool{Name: "bad_write", MinRole: models.RoleManager, Run: func(*Turn, Args) Outcome { return Outcome{} }})
}

func TestRegistryRefusesAReadWithDomains(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a read with domains was registered")
		}
	}()
	r := NewRegistry()
	r.Register(&Tool{Name: "bad_read", ReadOnly: true, Domains: []string{DomainPeople}, MinRole: models.RoleViewer,
		Run: func(*Turn, Args) Outcome { return Outcome{} }})
}

var handoffShapes = []*regexp.Regexp{
	regexp.MustCompile(`^/people/[A-Za-z0-9._%-]+$`),
	regexp.MustCompile(`^/people/[A-Za-z0-9._%-]+\?enrol=1$`),
	regexp.MustCompile(`^/terminals/[A-Za-z0-9._%-]+$`),
	regexp.MustCompile(`^/terminals\?pending=1$`),
	regexp.MustCompile(`^/sites/[A-Za-z0-9._%-]+$`),
}

func matchesAHandoffShape(route string) bool {
	for _, re := range handoffShapes {
		if re.MatchString(route) {
			return true
		}
	}
	return false
}

func TestHandoffsAreTheFiveKnownRoutes(t *testing.T) {
	cases := []*models.AssistantHandoff{
		handoffPerson("P-1", "Ada"),
		handoffEnrolment("P-1", "Ada"),
		handoffTerminal("AT-1", "Reception"),
		handoffPendingTerminals(),
		handoffSite("a7f3c2e1-0000-4000-8000-000000000000", "Lagos"),
		// An identifier the validator would refuse is still escaped here, so
		// even a bypass could not leave the route's shape.
		handoffPerson("P 1/../x", "x"),
	}
	for _, h := range cases {
		if !matchesAHandoffShape(h.Route) {
			t.Errorf("hand-off %q is not one of the known routes", h.Route)
		}
		if h.Kind == "" || h.Label == "" {
			t.Errorf("hand-off %q lacks kind or label", h.Route)
		}
	}
	// No tool builds a route from anything but these constructors: there is
	// no Param that names a route or a path.
	r := NewRegistry()
	registerTools(r)
	for _, d := range r.ForRole(models.RoleOwner) {
		tool, _ := r.Get(d.Name)
		for _, p := range tool.Params {
			if strings.Contains(p.Name, "route") || strings.Contains(p.Name, "path") || strings.Contains(p.Name, "url") {
				t.Errorf("%s takes a %q argument", d.Name, p.Name)
			}
		}
	}
}

func TestWindowTextParsesIntoTheRouteShape(t *testing.T) {
	cases := map[string][]object{
		"Mon-Fri 08:00-18:00": {{"days_of_week": 31, "start_time": "08:00", "end_time": "18:00"}},
		"Mon-Fri 08:00-18:00; Sat 09:00-13:00": {
			{"days_of_week": 31, "start_time": "08:00", "end_time": "18:00"},
			{"days_of_week": 32, "start_time": "09:00", "end_time": "13:00"},
		},
		"Daily 06:00-22:00":           {{"days_of_week": 127, "start_time": "06:00", "end_time": "22:00"}},
		"every day 06:00-22:00":       {{"days_of_week": 127, "start_time": "06:00", "end_time": "22:00"}},
		"Weekdays 08:00-17:00":        {{"days_of_week": 31, "start_time": "08:00", "end_time": "17:00"}},
		"Weekends 10:00-16:00":        {{"days_of_week": 96, "start_time": "10:00", "end_time": "16:00"}},
		"Fri 22:00-06:00":             {{"days_of_week": 16, "start_time": "22:00", "end_time": "06:00"}},
		"Sat,Sun 9:00-17:30":          {{"days_of_week": 96, "start_time": "09:00", "end_time": "17:30"}},
		"Saturday, Sunday 9:00-17:30": {{"days_of_week": 96, "start_time": "09:00", "end_time": "17:30"}},
		"Friday-Monday 08:00-12:00":   {{"days_of_week": 16 | 32 | 64 | 1, "start_time": "08:00", "end_time": "12:00"}},
		"MONDAY-friday 08:00-18:00":   {{"days_of_week": 31, "start_time": "08:00", "end_time": "18:00"}},
		"Wednesday 08:00-24:00":       {{"days_of_week": 4, "start_time": "08:00", "end_time": "23:59"}},
		"Daily 00:00-24:00":           {{"days_of_week": 127, "start_time": "00:00", "end_time": "23:59"}},
		"Tue,Thu 07:30-08:00; Sun 0:05-0:10": {
			{"days_of_week": 2 | 8, "start_time": "07:30", "end_time": "08:00"},
			{"days_of_week": 64, "start_time": "00:05", "end_time": "00:10"},
		},
	}
	for text, want := range cases {
		got, err := parseWindows(text)
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%q = %v, want %v", text, got, want)
		}
	}
}

// The review's cases: text the old parser silently misread. Each must be
// REFUSED, never parsed into something else.
func TestWindowTextRefusesWhatItCannotReadExactly(t *testing.T) {
	refused := map[string]string{
		// Day forms that used to collapse to one day.
		"Monday to Friday 08:00-18:00": "write a range as Mon-Fri",
		"Mon–Fri 08:00-18:00":          "write a range as Mon-Fri",
		"Mon—Fri 08:00-18:00":          "write a range as Mon-Fri",
		"Sat & Sun 09:00-13:00":        "write a range as Mon-Fri",
		"Sat and Sun 09:00-13:00":      "write a range as Mon-Fri",
		"Sat/Sun 09:00-13:00":          "write a range as Mon-Fri",
		"Mondays 08:00-18:00":          "unknown day",
		"Tues 08:00-18:00":             "unknown day",
		"Mo 08:00-18:00":               "unknown day",
		"Funday 08:00-18:00":           "unknown day",
		"Mon-Wed-Fri 08:00-18:00":      "one hyphen",
		"Mon, 08:00-18:00":             "empty entry",
		"Mon-Fri":                      "expected days then a time range",
		"08:00-18:00":                  "expected days then a time range",
		// Times that used to be accepted with a different meaning.
		"Mon-Fri 9:00am-5:00pm":     "24-hour form",
		"Mon-Fri 9am-5pm":           "24-hour form",
		"Mon-Fri 08:00-18:00:00":    "24-hour form",
		"Mon-Fri 8-18":              "24-hour form",
		"Mon-Fri 08:00-18:60":       "24-hour form",
		"Mon-Fri 25:00-26:00":       "24-hour form",
		"Mon-Fri 24:00-08:00":       "cannot start at 24:00",
		"Mon-Fri 08:00-24:30":       "past the end of the day",
		"Mon-Fri 08:00-18:00-19:00": "must be HH:MM-HH:MM",
		"":                          "windows is empty",
		`{"days_of_week":1}`:        "expected days then a time range",
	}
	for text, want := range refused {
		got, err := parseWindows(text)
		if err == nil {
			t.Errorf("%q parsed to %v; want a refusal mentioning %q", text, got, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q does not mention %q", text, err.Error(), want)
		}
	}
	// 24:00 never becomes a zero-length or overnight window.
	got, err := parseWindows("Daily 00:00-24:00")
	if err != nil || got[0]["end_time"] != "23:59" {
		t.Fatalf("00:00-24:00 = %v, %v", got, err)
	}
}

func TestTimezonesAreCheckedBeforeTheRouteSeesThem(t *testing.T) {
	for _, ok := range []string{"", "  ", "UTC", "Africa/Lagos", "Europe/London", "America/New_York", "Asia/Kolkata"} {
		if err := validTimezone(ok); err != nil {
			t.Errorf("valid zone %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"Lagos", "WAT", "GMT+1", "Africa/Lago", "Local", "local", "Europe/London; DROP", "+01:00"} {
		err := validTimezone(bad)
		if err == nil {
			t.Errorf("bad zone %q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "Africa/Lagos") {
			t.Errorf("bad zone %q: error %q does not show the form to use", bad, err.Error())
		}
	}
	// The tools refuse the zone before any request: no registry, no router.
	r := NewRegistry()
	registerTools(r)
	create, _ := r.Get("create_schedule")
	args, err := create.ValidateArgs(json.RawMessage(`{"name":"X","windows":"Mon 08:00-09:00","timezone":"Lagos"}`))
	if err != nil {
		t.Fatalf("args: %v", err)
	}
	out := create.Run(&Turn{ctx: context.Background()}, args)
	if !out.IsError || out.Status != models.ToolCallInvalid || !strings.Contains(fmt.Sprint(out.Result), "not a known IANA zone") {
		t.Fatalf("create_schedule with a bad zone = %+v", out)
	}
	update, _ := r.Get("update_schedule")
	args, _ = update.ValidateArgs(json.RawMessage(`{"schedule_id":"s1","timezone":"WAT"}`))
	if _, err := update.Confirm(&Turn{ctx: context.Background()}, args); err == nil || !strings.Contains(err.Error(), "not a known IANA zone") {
		t.Fatalf("update_schedule with a bad zone confirmed: %v", err)
	}
	// The description tells the truth about a blank zone.
	for _, p := range create.Params {
		if p.Name == "timezone" && !strings.Contains(p.Description, "site's own zone") {
			t.Errorf("timezone description = %q", p.Description)
		}
	}
}

func TestExplainDenialDistinguishesLegacyEnrolment(t *testing.T) {
	none := object{"credentials": []object{}, "enrolment_source": models.EnrolmentSourceNone}
	if f := credentialFinding(none, "AT-1"); len(f) != 1 || !strings.Contains(f[0], "No fingerprint is enrolled") {
		t.Fatalf("unenrolled finding = %v", f)
	}
	legacy := object{"credentials": []object{}, "enrolment_source": models.EnrolmentSourceLegacy}
	f := credentialFinding(legacy, "AT-1")
	if len(f) != 1 || strings.Contains(f[0], "No fingerprint is enrolled") || !strings.Contains(f[0], "older enrolment record") ||
		!strings.Contains(f[0], "do not treat them as unenrolled") {
		t.Fatalf("legacy finding = %v", f)
	}
	enrolledHere := object{"credentials": []object{{"state": models.CredentialActive, "terminal": object{"serial_number": "AT-1"}}}, "enrolment_source": models.EnrolmentSourceCredential}
	if f := credentialFinding(enrolledHere, "AT-1"); len(f) != 0 {
		t.Fatalf("enrolled-here finding = %v", f)
	}
	elsewhere := object{"credentials": []object{{"state": models.CredentialActive, "terminal": object{"serial_number": "AT-2"}}}, "enrolment_source": models.EnrolmentSourceCredential}
	if f := credentialFinding(elsewhere, "AT-1"); len(f) != 1 || !strings.Contains(f[0], "not enrolled at AT-1") {
		t.Fatalf("elsewhere finding = %v", f)
	}
}

func TestDiagnosticProjectionDropsNetworkIdentifiers(t *testing.T) {
	raw := object{
		"network":        object{"associated": true, "ssid": "Office-5G", "ip": "192.168.4.22", "rssi_dbm": -61.0, "mac": "aa:bb"},
		"outbound_queue": object{"enrolments": 0.0, "access_logs": 3.0, "dropped_access_logs": 0.0},
		"worklist":       object{"missing": 2.0},
		"storage":        object{"members": 40.0, "capacity": 200.0, "orphaned_slots": 0.0, "write_failed": false, "fallback_store": false},
		"extra":          object{"anything": "else"},
	}
	got := diagnosticResult(raw)
	encoded, _ := json.Marshal(got)
	for _, forbidden := range []string{"ssid", "Office-5G", "192.168", `"ip"`, "mac", "extra"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("projection carries %q: %s", forbidden, encoded)
		}
	}
	for _, wanted := range []string{`"associated":true`, `"rssi_dbm":-61`, `"access_logs":3`, `"missing":2`, `"capacity":200`} {
		if !strings.Contains(string(encoded), wanted) {
			t.Errorf("projection lacks %s: %s", wanted, encoded)
		}
	}
	// A command carrying a result that is not a snapshot shows no result.
	c := commandView(object{"id": "c1", "type": "DEVICE_TEST", "state": "ACCEPTED", "result": object{"ip": "10.0.0.1"}, "params": object{"target": "buzzer"}})
	if _, ok := c["result"]; ok {
		t.Errorf("device test result was projected: %v", c)
	}
	if _, ok := c["params"]; ok {
		t.Errorf("params were projected: %v", c)
	}
	if _, ok := c["requested_by_email"]; ok {
		t.Errorf("the requesting operator's email was projected")
	}
}

func TestProjectionFieldListsNameNoSecretOrInfrastructure(t *testing.T) {
	forbidden := []string{"pairing_code", "api_key", "first_seen_ip", "last_seen_ip", "adopted_by", "ip_address",
		"hardware_revision", "template", "digest", "key_id", "slot", "locator", "vendor", "settings", "password",
		"requested_by_email", "params", "member_capacity", "provisioned_via"}
	lists := map[string][]string{
		"person": personFields, "rule": ruleFields, "enrol": enrolFields, "terminal": terminalFields,
		"site": siteFields, "schedule": scheduleFields, "event": eventFields, "credential": credentialFields,
		"command": commandFields, "offer": commandOfferFields, "pending": pendingTerminalFields,
		"siteSettings": siteSettingsFields, "decision": decisionFields,
	}
	for name, fields := range lists {
		for _, f := range fields {
			for _, bad := range forbidden {
				if f == bad {
					t.Errorf("%s projection names %q", name, f)
				}
			}
		}
	}
}

// A router that answers every read with a fixed body, for the poll tests.
type fixedRouter struct {
	body  string
	calls int
}

func (f *fixedRouter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.calls++
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(f.body))
}

func TestPollingIsChargedToItsOwnAllowanceAndStopsAtIt(t *testing.T) {
	router := &fixedRouter{body: `{"external_id":"P-1","biometric_enrolled":false,"enrollment":{"status":"PENDING"}}`}
	turn := &Turn{ID: "t", Role: models.RoleViewer, router: router, ctx: context.Background()}

	// One second of polling at the minimum cadence: two reads, none charged
	// to the ordinary budget.
	out := pollUntil(turn, 1100*time.Millisecond, time.Second, func(p *Turn) (Outcome, bool) {
		o := enrollmentState(p, "P-1")
		return o, false
	})
	if out.IsError {
		t.Fatalf("poll failed: %v", out.Result)
	}
	if turn.calls != 0 {
		t.Fatalf("polls were charged to the call budget: calls=%d", turn.calls)
	}
	if turn.polls < 2 || turn.polls > 3 {
		t.Fatalf("polls = %d, want about 2", turn.polls)
	}

	// The allowance runs out mid-wait: the wait returns what it has, with a
	// note, and never touches the ordinary budget.
	turn.polls = maxPollCallsPerTurn - 1
	out = pollUntil(turn, 10*time.Second, time.Second, func(p *Turn) (Outcome, bool) {
		return enrollmentState(p, "P-1"), false
	})
	result, _ := out.Result.(object)
	if str(result, "note") != pollBudgetNote {
		t.Fatalf("no budget note on an exhausted wait: %v", out.Result)
	}
	if turn.polls != maxPollCallsPerTurn {
		t.Fatalf("polls = %d, want the cap %d", turn.polls, maxPollCallsPerTurn)
	}

	// Spent before it starts: one ordinary read and the note, no loop.
	before := router.calls
	start := time.Now()
	out = pollUntil(turn, 10*time.Second, time.Second, func(p *Turn) (Outcome, bool) {
		return enrollmentState(p, "P-1"), false
	})
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("an exhausted wait still waited")
	}
	if router.calls != before+1 || turn.calls != 1 {
		t.Fatalf("exhausted wait made %d requests, calls=%d", router.calls-before, turn.calls)
	}
	result, _ = out.Result.(object)
	if str(result, "note") != pollBudgetNote {
		t.Fatalf("no budget note: %v", out.Result)
	}

	// A poll may only read: the mode refuses a write outright.
	pt := *turn
	pt.polling = true
	if _, err := pt.Call(http.MethodPost, "/api/v1/console/people", nil); err == nil {
		t.Fatalf("a poll performed a write")
	}
}

func TestPollingStopsWhenTheTurnIsCancelled(t *testing.T) {
	router := &fixedRouter{body: `{"enrollment":{"status":"PENDING"}}`}
	ctx, cancel := context.WithCancel(context.Background())
	turn := &Turn{ID: "t", Role: models.RoleViewer, router: router, ctx: ctx}
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	pollUntil(turn, 30*time.Second, 5*time.Second, func(p *Turn) (Outcome, bool) {
		return enrollmentState(p, "P-1"), false
	})
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a cancelled turn kept polling")
	}
}

func TestWaitTimeoutsAreClamped(t *testing.T) {
	r := NewRegistry()
	registerTools(r)
	for _, name := range []string{"wait_for_enrollment", "wait_for_command"} {
		tool, _ := r.Get(name)
		if tool.MaxDuration == 0 || tool.MaxDuration > 2*maxWaitSeconds*time.Second {
			t.Errorf("%s MaxDuration = %v", name, tool.MaxDuration)
		}
		for _, p := range tool.Params {
			if p.Name == "timeout_s" && (p.Max == nil || *p.Max > maxWaitSeconds) {
				t.Errorf("%s timeout_s is not bounded to %d", name, maxWaitSeconds)
			}
		}
	}
}

func TestAssistantReasonKeepsTheOperatorsWords(t *testing.T) {
	if got := assistantReason("  door keeps beeping "); got != "assistant: door keeps beeping" {
		t.Fatalf("reason = %q", got)
	}
	if got := assistantReason(""); got != "assistant" {
		t.Fatalf("empty reason = %q", got)
	}
}

func TestNewConsequenceWordingIsTheDialogsWording(t *testing.T) {
	d := deactivateConsequence("Ada")
	if d.Title != "Deactivate Ada?" || !strings.Contains(d.Body, "Every terminal in your company will be told to stop admitting them.") {
		t.Fatalf("deactivate consequence = %+v", d)
	}
	s := scheduleChangeConsequence("Weekend", 3, "This will set its windows to Sat 08:00-14:00.")
	if !strings.Contains(strings.Join(s.Warnings, " "), "3 access rules refer to this schedule") {
		t.Fatalf("schedule consequence = %+v", s)
	}
}
