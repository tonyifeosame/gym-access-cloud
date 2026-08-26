package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Commit 2.0: the terminal detail projection, and paginated/searchable people.
//
// Both exist because a console screen needed something the API could not
// answer: a detail view held less than the list row it was opened from, and a
// people list returned the entire roster on every page load.

// ---------------------------------------------------------------------------
// Terminal detail
// ---------------------------------------------------------------------------

func TestConsoleTerminalDetailCarriesTheInventoryRow(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site A", "DETAIL-1")

	// Give the terminal something to report, the way a heartbeat would.
	mustExec(t, `UPDATE devices
	                SET device_name = 'Front Desk Reader',
	                    firmware_version = '1.2.0',
	                    hardware_revision = 'rev-c',
	                    build_number = '456',
	                    boot_count = 12,
	                    last_heartbeat_at = CURRENT_TIMESTAMP,
	                    last_seen_at = CURRENT_TIMESTAMP
	              WHERE serial_number = 'DETAIL-1'`)

	_, token, csrf := consoleOperatorSession(t, env.router, one, "detail@example.com", models.RoleOwner)

	code, detail := consoleCall(t, env.router, "GET", "/api/v1/console/terminals/DETAIL-1", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET terminal detail = %d (%v)", code, detail)
	}

	// The three fields the endpoint returned BEFORE this change are all still
	// present, at the same paths. That is what makes the addition compatible.
	for _, field := range []string{"serial_number", "application_mode", "effective_applications"} {
		if _, present := detail[field]; !present {
			t.Errorf("detail lost the pre-existing field %q", field)
		}
	}

	// And it now carries everything the list row does.
	for _, field := range []string{
		"public_id", "site_id", "site_name", "device_name", "device_type", "status",
		"active", "release_channel", "firmware_version", "hardware_revision",
		"build_number", "current_firmware_version", "firmware_outdated",
	} {
		if _, present := detail[field]; !present {
			t.Errorf("detail is missing inventory field %q", field)
		}
	}

	if detail["device_name"] != "Front Desk Reader" || detail["site_name"] != "Site A" {
		t.Errorf("detail = %v", detail)
	}
	if detail["firmware_version"] != "1.2.0" || detail["hardware_revision"] != "rev-c" {
		t.Errorf("reported build = %v / %v", detail["firmware_version"], detail["hardware_revision"])
	}

	// The detail and the list must agree: they are the same projection, and a
	// screen opened from a row must not show something different.
	_, listBody := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	row := listOf(t, listBody, "terminals")[0].(map[string]any)
	for _, field := range []string{
		"public_id", "serial_number", "device_name", "site_name", "status",
		"firmware_version", "firmware_outdated", "current_firmware_version",
	} {
		if fmt.Sprint(row[field]) != fmt.Sprint(detail[field]) {
			t.Errorf("%s differs between list (%v) and detail (%v)", field, row[field], detail[field])
		}
	}

	// No credential material, in either shape.
	for _, forbidden := range []string{"api_key", "atd_", "fingerprint"} {
		if strings.Contains(strings.ToLower(mustJSON(t, detail)), forbidden) {
			t.Errorf("terminal detail exposes %q", forbidden)
		}
	}

	// The mode write returns the same shape, so a client can use the response
	// instead of refetching.
	if code, _ := consoleCall(t, env.router, "PUT", "/api/v1/console/applications/CHECK_IN",
		`{"enabled":true}`, token, csrf); code != http.StatusOK {
		t.Fatal("enabling CHECK_IN failed")
	}
	code, updated := consoleCall(t, env.router, "PUT",
		"/api/v1/console/terminals/DETAIL-1/application-mode",
		`{"application_mode":"CHECK_IN"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("mode write = %d (%v)", code, updated)
	}
	if updated["device_name"] != "Front Desk Reader" || updated["application_mode"] != models.AppCheckIn {
		t.Errorf("mode write response = %v", updated)
	}
	if len(listOf(t, updated, "effective_applications")) != 1 {
		t.Error("mode write response lost the effective applications")
	}
}

func TestConsoleTerminalDetailStaysTenantScoped(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateDevice(t, "Site C", "OTHER-DETAIL-1") // company two

	_, token, _ := consoleOperatorSession(t, env.router, one, "scoped-detail@example.com", models.RoleOwner)

	for _, serial := range []string{"OTHER-DETAIL-1", "NO-SUCH-TERMINAL"} {
		if code, _ := consoleCall(t, env.router, "GET",
			"/api/v1/console/terminals/"+serial, "", token, ""); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", serial, code)
		}
	}
}

// ---------------------------------------------------------------------------
// People: pagination and search
// ---------------------------------------------------------------------------

func seedPeople(t *testing.T, router any, token, csrf string, count int) {
	t.Helper()
	_ = router
	for i := 1; i <= count; i++ {
		mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, active)
		             VALUES ((SELECT id FROM companies WHERE slug = 'one'), $1, $2, 'STANDARD', TRUE)`,
			fmt.Sprintf("P-%03d", i), fmt.Sprintf("Person %03d", i))
	}
}

func TestConsolePeoplePagination(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "paging@example.com", models.RoleOwner)
	seedPeople(t, env.router, token, csrf, 120)

	// The default is a bounded page, not the whole roster.
	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/people", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET people = %d (%v)", code, body)
	}
	if got := len(listOf(t, body, "people")); got != 50 {
		t.Errorf("default page = %d people, want 50", got)
	}
	if body["total"] != float64(120) {
		t.Errorf("total = %v, want the whole match", body["total"])
	}
	if body["count"] != float64(50) || body["limit"] != float64(50) || body["offset"] != float64(0) {
		t.Errorf("envelope = %v", body)
	}
	if body["has_more"] != true {
		t.Error("has_more should be true with 120 matches and a page of 50")
	}

	// Paging walks the whole set exactly once: no repeats, no gaps. The id
	// tiebreak in the ordering is what makes that true when timestamps collide.
	seen := map[string]bool{}
	for offset := 0; offset < 120; offset += 50 {
		_, page := consoleCall(t, env.router, "GET",
			fmt.Sprintf("/api/v1/console/people?limit=50&offset=%d", offset), "", token, "")
		for _, entry := range listOf(t, page, "people") {
			id := entry.(map[string]any)["external_id"].(string)
			if seen[id] {
				t.Errorf("%s appeared on two pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 120 {
		t.Errorf("paging saw %d distinct people, want 120", len(seen))
	}

	// The last page reports that it is the last.
	_, last := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?limit=50&offset=100", "", token, "")
	if last["has_more"] != false || len(listOf(t, last, "people")) != 20 {
		t.Errorf("last page = %v", last)
	}

	// An offset past the end is an empty page, not an error.
	code, beyond := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?offset=500", "", token, "")
	if code != http.StatusOK || len(listOf(t, beyond, "people")) != 0 {
		t.Errorf("offset past the end = %d (%v)", code, beyond)
	}
	if beyond["total"] != float64(120) {
		t.Error("an empty page still reports the true total")
	}

	// Bounds are clamped rather than rejected: asking for more than the ceiling
	// is a caller wanting as much as it can have.
	_, capped := consoleCall(t, env.router, "GET", "/api/v1/console/people?limit=5000", "", token, "")
	if capped["limit"] != float64(200) {
		t.Errorf("limit = %v, want it clamped to 200", capped["limit"])
	}
	for _, nonsense := range []string{"limit=0", "limit=-3", "offset=-10", "limit=abc", "offset=x"} {
		if code, _ := consoleCall(t, env.router, "GET",
			"/api/v1/console/people?"+nonsense, "", token, ""); code != http.StatusOK {
			t.Errorf("?%s = %d, want it clamped to something sane", nonsense, code)
		}
	}
}

func TestConsolePeopleSearch(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "search@example.com", models.RoleOwner)

	for _, person := range []struct{ id, name string }{
		{"EMP-1001", "Adaeze Okonkwo"},
		{"EMP-1002", "Bilal Rahman"},
		{"VIS-2001", "Chen Wei"},
		{"VIS-2002", "adaeze lowercase"},
		{"ODD-100%", "Percent Person"},
		{"ODD-1_0", "Underscore Person"},
		{`ODD-A\B`, "Backslash Person"},
	} {
		if code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
			fmt.Sprintf(`{"external_id":%q,"full_name":%q}`, person.id, person.name),
			token, csrf); code != http.StatusCreated {
			t.Fatalf("creating %s = %d (%v)", person.id, code, body)
		}
	}

	search := func(term string) []string {
		t.Helper()
		// Properly encoded: the terms below deliberately contain a space and
		// SQL wildcard characters, which is the point of the test.
		_, body := consoleCall(t, env.router, "GET",
			"/api/v1/console/people?q="+url.QueryEscape(term), "", token, "")
		ids := []string{}
		for _, entry := range listOf(t, body, "people") {
			ids = append(ids, entry.(map[string]any)["external_id"].(string))
		}
		return ids
	}

	// Matches the external id, anywhere in it.
	if got := search("EMP"); len(got) != 2 {
		t.Errorf("q=EMP matched %v, want the two EMP ids", got)
	}
	// Matches the name, case-insensitively, and both spellings of it.
	if got := search("adaeze"); len(got) != 2 {
		t.Errorf("q=adaeze matched %v, want both regardless of case", got)
	}
	if got := search("ADAEZE"); len(got) != 2 {
		t.Errorf("q=ADAEZE matched %v, want the same as lowercase", got)
	}
	// Partial, mid-string.
	if got := search("hen We"); len(got) != 1 {
		t.Errorf("q='hen We' matched %v, want the one name", got)
	}

	// WILDCARDS IN THE TERM ARE LITERAL. Without escaping, "%" matches everyone
	// and "_" matches any single character, so a search box would silently
	// return the whole roster.
	if got := search("100%"); len(got) != 1 || got[0] != "ODD-100%" {
		t.Errorf("q='100%%' matched %v, want only the person whose id contains it", got)
	}
	if got := search("1_0"); len(got) != 1 || got[0] != "ODD-1_0" {
		t.Errorf("q='1_0' matched %v, want only the literal match", got)
	}
	// The backslash is the ESCAPE character itself, so it is the one most
	// likely to be handled wrongly: escaped in the wrong order it would either
	// swallow the character after it or produce a malformed pattern the
	// database rejects outright.
	if got := search(`A\B`); len(got) != 1 || got[0] != `ODD-A\B` {
		t.Errorf(`q='A\B' matched %v, want only the literal match`, got)
	}
	if got := search(`\`); len(got) != 1 || got[0] != `ODD-A\B` {
		t.Errorf(`q='\' matched %v, want only the person whose id contains one`, got)
	}
	// A trailing escape character is the classic way to produce an invalid
	// LIKE pattern. It must return no matches, not a 500.
	if got := search(`Z\`); len(got) != 0 {
		t.Errorf(`q='Z\' matched %v, want nothing`, got)
	}

	// No matches is an empty page, not an error.
	if got := search("nobody-by-that-name"); len(got) != 0 {
		t.Errorf("an unmatched search returned %v", got)
	}

	// Search is company-scoped like every other read.
	mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, active)
	             VALUES ($1, 'EMP-9999', 'Other Tenant Person', 'STANDARD', TRUE)`, two)
	if got := search("EMP"); len(got) != 2 {
		t.Errorf("search crossed a tenant boundary: %v", got)
	}

	// Total reflects the search, not the roster.
	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/people?q=EMP", "", token, "")
	if body["total"] != float64(2) {
		t.Errorf("total = %v, want the size of the match", body["total"])
	}

	// Search combines with paging.
	// Three people carry an ODD- prefix; a page of one reports the other two as
	// still to come, so total reflects the SEARCH and not the page.
	_, paged := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?q=ODD&limit=1", "", token, "")
	if len(listOf(t, paged, "people")) != 1 || paged["total"] != float64(3) || paged["has_more"] != true {
		t.Errorf("searched page = %v", paged)
	}
}

func TestConsolePeopleListStillHidesCredentials(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "creds@example.com", models.RoleOwner)

	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"P-1","full_name":"Enrolled Person"}`, token, csrf); code != http.StatusCreated {
		t.Fatal("creating a person failed")
	}
	mustExec(t, `UPDATE people SET fingerprint_template = 'terminal:T-1:slot:9'
	              WHERE external_id = 'P-1' AND company_id = $1`, one)

	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/people?q=P-1", "", token, "")
	person := listOf(t, body, "people")[0].(map[string]any)

	if person["biometric_enrolled"] != true {
		t.Error("an enrolled person is not reported as enrolled")
	}
	raw := mustJSON(t, body)
	for _, forbidden := range []string{"slot:9", "fingerprint", "terminal:T-1"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the paginated list leaked %q", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// The site-key API is deliberately unchanged
// ---------------------------------------------------------------------------

func TestSiteKeyMemberListIsNotPaginated(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "unchanged@example.com", models.RoleOwner)
	seedPeople(t, env.router, token, csrf, 60)

	// Terminals and existing tooling speak this contract. Bounding it here would
	// silently truncate a roster somebody depends on being complete -- so the
	// console got its own endpoint instead, and this one still returns a bare
	// array of everything.
	status, people := env.list("GET", "/api/v1/members", map[string]string{"X-API-Key": env.siteAKey})
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/members = %d", status)
	}
	if len(people) != 60 {
		t.Errorf("site-key member list returned %d, want all 60", len(people))
	}

	// And it is still a bare array, not an envelope.
	if _, err := database.ListConsolePeople(operatorCompanyID(t, "one"),
		database.PeopleQuery{Limit: 10}); err != nil {
		t.Fatalf("console query: %v", err)
	}
}

// ---------------------------------------------------------------------------
// D2: terminal paging and search
// ---------------------------------------------------------------------------

// seedTerminals registers `count` terminals at site A.
func seedTerminals(t *testing.T, env *testEnv, count int) {
	t.Helper()
	for i := 1; i <= count; i++ {
		env.registerDevice(env.siteAKey, fmt.Sprintf("AT-%03d", i))
	}
}

func TestConsoleTerminalsPagination(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "fleet@example.com", models.RoleOwner)
	seedTerminals(t, env, 120)

	// The default is a bounded page, not the whole fleet. A list with no upper
	// bound degrades quietly as a customer grows, which is what this closes.
	code, body := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET terminals = %d (%v)", code, body)
	}
	if got := len(listOf(t, body, "terminals")); got != 50 {
		t.Errorf("default page = %d terminals, want 50", got)
	}

	// THE SAME ENVELOPE PEOPLE USES, field for field.
	if body["count"] != float64(50) || body["total"] != float64(120) ||
		body["limit"] != float64(50) || body["offset"] != float64(0) ||
		body["has_more"] != true {
		t.Errorf("envelope = %v", body)
	}

	_, last := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals?limit=50&offset=100", "", token, "")
	if got := len(listOf(t, last, "terminals")); got != 20 {
		t.Errorf("final page = %d terminals, want 20", got)
	}
	if last["has_more"] != false {
		t.Errorf("has_more = %v on the last page", last["has_more"])
	}
	if last["total"] != float64(120) {
		t.Errorf("total = %v, want the whole match on every page", last["total"])
	}
}

// TestConsoleTerminalsPagingIsStableAcrossPages.
//
// Two terminals sharing a site can order identically without a unique final sort
// key, and the symptom is a row appearing on two consecutive pages while another
// is skipped -- silently, and only once a customer has enough hardware to page.
func TestConsoleTerminalsPagingIsStableAcrossPages(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "stable@example.com", models.RoleOwner)
	seedTerminals(t, env, 30)

	seen := map[string]int{}
	for offset := 0; offset < 30; offset += 10 {
		_, body := consoleCall(t, env.router, "GET",
			fmt.Sprintf("/api/v1/console/terminals?limit=10&offset=%d", offset), "", token, "")
		for _, row := range listOf(t, body, "terminals") {
			seen[row.(map[string]any)["serial_number"].(string)]++
		}
	}
	if len(seen) != 30 {
		t.Errorf("paged over 30 terminals and saw %d distinct", len(seen))
	}
	for serial, times := range seen {
		if times != 1 {
			t.Errorf("%s appeared %d times across the pages", serial, times)
		}
	}
}

func TestConsoleTerminalsSearch(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "search@example.com", models.RoleOwner)

	env.registerDevice(env.siteAKey, "AT-LOADING-BAY")
	env.registerDevice(env.siteAKey, "AT-RECEPTION")
	env.registerDevice(env.siteBKey, "AT-STOREROOM")
	mustExec(t, `UPDATE devices SET device_name = 'Front Desk' WHERE serial_number = 'AT-RECEPTION'`)

	cases := []struct {
		name, query string
		want        []string
	}{
		{"by serial", "LOADING", []string{"AT-LOADING-BAY"}},
		{"by terminal name", "Front Desk", []string{"AT-RECEPTION"}},
		{"by site name", "Site B", []string{"AT-STOREROOM"}},
		{"case insensitive", "loading", []string{"AT-LOADING-BAY"}},
		{"matches nothing", "nowhere", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := consoleCall(t, env.router, "GET",
				"/api/v1/console/terminals?q="+url.QueryEscape(tc.query), "", token, "")
			rows := listOf(t, body, "terminals")
			if len(rows) != len(tc.want) {
				t.Fatalf("%q matched %d, want %d (%v)", tc.query, len(rows), len(tc.want), body)
			}
			for i, serial := range tc.want {
				if got := rows[i].(map[string]any)["serial_number"]; got != serial {
					t.Errorf("match %d = %v, want %v", i, got, serial)
				}
			}
			// `total` describes the MATCH, not the fleet.
			if body["total"] != float64(len(tc.want)) {
				t.Errorf("total = %v, want %d", body["total"], len(tc.want))
			}
		})
	}
}

// TestConsoleTerminalSearchWildcardsAreLiteral.
//
// Without ESCAPE a term of "%" matches the whole fleet, so a search box becomes
// a way to page through everything while appearing to filter.
func TestConsoleTerminalSearchWildcardsAreLiteral(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "wild@example.com", models.RoleOwner)
	seedTerminals(t, env, 5)

	_, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals?q="+url.QueryEscape("%"), "", token, "")
	if body["total"] != float64(0) {
		t.Errorf("a literal percent sign matched %v terminals, want 0", body["total"])
	}
}

func TestConsoleTerminalSearchComposesWithOutdated(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "compose@example.com", models.RoleOwner)

	env.registerDevice(env.siteAKey, "AT-OLD-ONE")
	env.registerDevice(env.siteAKey, "AT-OLD-TWO")
	// TERMINAL / STABLE are what RegisterDevice defaults a new unit to, and the
	// schema constrains both -- the outdated join matches on device_type and
	// release_channel, so a catalogue row that does not agree matches nothing.
	mustExec(t, `INSERT INTO firmware_versions
	                 (company_id, version, device_type, release_channel, is_current)
	             VALUES ($1, '9.9.9', 'TERMINAL', 'STABLE', TRUE)`, one)
	mustExec(t, `UPDATE devices SET firmware_version = '1.0.0'`)

	_, body := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals?outdated=true&q=OLD-ONE", "", token, "")
	if body["total"] != float64(1) {
		t.Errorf("outdated + search matched %v, want 1 (%v)", body["total"], body)
	}
}

// TestConsoleTerminalsRespectSiteGrants.
//
// A scoped operator's `total` must describe THEIR scope. A company-wide count
// above a narrowed list both reads as a bug and discloses how much hardware
// exists at sites they were deliberately not given.
func TestConsoleTerminalsRespectSiteGrants(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	env.registerDevice(env.siteAKey, "AT-SITE-A-1")
	env.registerDevice(env.siteAKey, "AT-SITE-A-2")
	env.registerDevice(env.siteBKey, "AT-SITE-B-1")

	operator, token, _ := consoleOperatorSession(t, env.router, one,
		"scoped@example.com", models.RoleViewer)
	mustExec(t, `INSERT INTO user_site_grants (user_id, site_id)
	             VALUES ($1, (SELECT id FROM sites WHERE site_name = 'Site A'))`, operator.ID)

	_, body := consoleCall(t, env.router, "GET", "/api/v1/console/terminals", "", token, "")
	if body["total"] != float64(2) {
		t.Errorf("scoped total = %v, want 2 -- the caller's own scope", body["total"])
	}
	for _, row := range listOf(t, body, "terminals") {
		if serial := row.(map[string]any)["serial_number"]; serial == "AT-SITE-B-1" {
			t.Errorf("a scoped operator was shown %v", serial)
		}
	}

	// AND THE PLACEHOLDER NUMBERING HELD. siteScopeClause injects an argument
	// only when a scope is present, so search and paging shift position for a
	// scoped caller and not for an unscoped one. This is the case a literal
	// $3/$4/$5 would have mis-bound while every unscoped test passed.
	_, searched := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals?q=SITE-A-1&limit=10", "", token, "")
	if searched["total"] != float64(1) {
		t.Errorf("scoped search total = %v, want 1 (%v)", searched["total"], searched)
	}
}

// TestFleetSummaryIsNotNarrowedByPagingOrSearch.
//
// The summary sits ABOVE the list and answers a different question. If paging
// narrowed it, an operator would read "1 online" over a page of one while eleven
// doors were up.
func TestFleetSummaryIsNotNarrowedByPagingOrSearch(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, one, "summary@example.com", models.RoleOwner)
	seedTerminals(t, env, 12)

	_, page := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals?limit=1&q=AT-001", "", token, "")
	if page["count"] != float64(1) {
		t.Fatalf("expected a single-row page, got %v", page)
	}

	_, summary := consoleCall(t, env.router, "GET",
		"/api/v1/console/terminals/summary", "", token, "")
	if summary["total"] != float64(12) {
		t.Errorf("summary total = %v, want 12 -- the whole scope", summary["total"])
	}
}

// TestSiteKeyTerminalListIsNotPaginated.
//
// The sibling of TestSiteKeyMemberListIsNotPaginated, for the same reason:
// deployed tooling reads GET /api/v1/devices as a COMPLETE inventory, and
// quietly bounding it would truncate a fleet somebody depends on being whole. The
// console got its own paged store function instead.
func TestSiteKeyTerminalListIsNotPaginated(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	seedTerminals(t, env, 60)

	res := env.do("GET", "/api/v1/devices", nil, siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/devices = %d", res.Code)
	}
	devices, ok := res.Body["devices"].([]any)
	if !ok {
		t.Fatalf("site-key device list shape changed: %v", res.Body)
	}
	if len(devices) != 60 {
		t.Errorf("site-key device list returned %d, want all 60", len(devices))
	}
	if _, bounded := res.Body["has_more"]; bounded {
		t.Error("the site-key device list grew a pagination envelope")
	}
}

// ---------------------------------------------------------------------------
// D3: people filtered by enrolment and activity
// ---------------------------------------------------------------------------

// enrolPerson gives somebody a credential the way the placement endpoint does:
// a row in `credentials`, and NOTHING in the legacy column.
func enrolPerson(t *testing.T, companyID int64, externalID string) {
	t.Helper()
	mustExec(t, `
		INSERT INTO credentials (company_id, person_id, credential_type,
		                         template_format, status)
		VALUES ($1, (SELECT id FROM people WHERE company_id = $1 AND external_id = $2),
		        'FINGERPRINT', 'SENSOR_LOCAL', 'ACTIVE')`, companyID, externalID)
}

func TestConsolePeopleEnrolledFilter(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "enrolled@example.com", models.RoleOwner)

	// 120 people so the filter has to run in SQL: filtering a fetched page would
	// filter the page rather than the roster, which is silently wrong past the
	// first one and silently right in every test with three fixtures.
	seedPeople(t, env.router, token, csrf, 120)
	enrolPerson(t, one, "P-001")
	enrolPerson(t, one, "P-060")
	// And one enrolled only the legacy way, which must still count as enrolled.
	mustExec(t, `UPDATE people SET fingerprint_template = 'terminal:AT-OLD:slot:2'
	              WHERE company_id = $1 AND external_id = 'P-119'`, one)

	_, enrolled := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?enrolled=true", "", token, "")
	if enrolled["total"] != float64(3) {
		t.Errorf("enrolled=true total = %v, want 3", enrolled["total"])
	}

	_, unenrolled := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?enrolled=false", "", token, "")
	if unenrolled["total"] != float64(117) {
		t.Errorf("enrolled=false total = %v, want 117", unenrolled["total"])
	}

	// THE FILTER AND THE BADGE AGREE. A row the filter calls unenrolled must not
	// carry biometric_enrolled: true, or the list contradicts itself.
	for _, row := range listOf(t, unenrolled, "people") {
		person := row.(map[string]any)
		if person["biometric_enrolled"] != false {
			t.Errorf("%v is in the unenrolled list and reads as enrolled",
				person["external_id"])
		}
	}

	// P-060 is on the second page unfiltered, so finding it proves the filter
	// ran in SQL rather than over the first fifty rows.
	_, second := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?enrolled=true&q=P-060", "", token, "")
	if second["total"] != float64(1) {
		t.Errorf("a filter that only saw page one would miss P-060 (%v)", second)
	}
}

// TestConsolePeopleAbsentFilterIsNotFalse.
//
// Tri-state. Absent means "everybody"; collapsing it to false would hide every
// enrolled person from an unfiltered list.
func TestConsolePeopleAbsentFilterIsNotFalse(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "tri@example.com", models.RoleOwner)
	seedPeople(t, env.router, token, csrf, 4)
	enrolPerson(t, one, "P-001")

	for _, query := range []string{"", "?enrolled=", "?enrolled=maybe"} {
		_, body := consoleCall(t, env.router, "GET",
			"/api/v1/console/people"+query, "", token, "")
		if body["total"] != float64(4) {
			t.Errorf("%q total = %v, want everybody (4)", query, body["total"])
		}
	}
}

func TestConsolePeopleActiveFilter(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "active@example.com", models.RoleOwner)
	seedPeople(t, env.router, token, csrf, 10)
	mustExec(t, `UPDATE people SET active = FALSE
	              WHERE company_id = $1 AND external_id IN ('P-001','P-002')`, one)

	_, inactive := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?active=false", "", token, "")
	if inactive["total"] != float64(2) {
		t.Errorf("active=false total = %v, want 2", inactive["total"])
	}

	_, live := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?active=true", "", token, "")
	if live["total"] != float64(8) {
		t.Errorf("active=true total = %v, want 8", live["total"])
	}

	// The filters compose with each other.
	enrolPerson(t, one, "P-003")
	_, both := consoleCall(t, env.router, "GET",
		"/api/v1/console/people?active=true&enrolled=true", "", token, "")
	if both["total"] != float64(1) {
		t.Errorf("active+enrolled total = %v, want 1", both["total"])
	}
}

// TestConsolePersonEditPreservesEnrolment.
//
// Correcting somebody's name must never unenrol them. The update path looks the
// credential up rather than assuming; defaulting to false there would have
// reported every edited person as newly unenrolled.
func TestConsolePersonEditPreservesEnrolment(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, "edit@example.com", models.RoleOwner)

	if code, _ := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"P-EDIT","full_name":"Before"}`, token, csrf); code != http.StatusCreated {
		t.Fatal("creating a person failed")
	}
	enrolPerson(t, one, "P-EDIT")

	code, body := consoleCall(t, env.router, "PUT", "/api/v1/console/people/P-EDIT",
		`{"full_name":"After"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("PUT person = %d (%v)", code, body)
	}
	if body["biometric_enrolled"] != true {
		t.Error("editing a name unenrolled the person")
	}
	if body["enrolment_source"] != models.EnrolmentSourceCredential {
		t.Errorf("enrolment_source = %v after an edit", body["enrolment_source"])
	}
}
