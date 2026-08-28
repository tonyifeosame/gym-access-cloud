package main

import (
	"net/http"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The setup state the overview cannot derive for itself (P0-3).
//
// ---------------------------------------------------------------------------
// WHY THIS FIGURE EXISTS
// ---------------------------------------------------------------------------
//
// Absence of permission is not permission. A person with no access rule reaches
// NOTHING -- deliberately, and it is the opposite of what an operator assumes
// coming from other products. So a customer who adds a terminal and a roster and
// stops has a deployment that admits nobody, and the console's onboarding
// guidance had no way to tell them: rules are readable one person at a time, so
// a browser would need one request per person to find out.
//
// WHAT IS ASSERTED HERE IS AGREEMENT, and about the right question. The figure
// is rendered as "Nobody can get in yet" and "3 people have no access", which
// are claims about PEOPLE GETTING IN -- so it counts rules that are IN FORCE,
// not rules that merely exist. The console has always graded them that way:
// accessVocabulary.standingOf() returns IN_FORCE, NOT_YET, EXPIRED or INACTIVE
// and the Access panel badges the last three.
//
// So the tests below walk every state a rule can be in, including the ones that
// are easy to get subtly wrong: a rule that starts next week has not granted
// anybody anything yet, a rule that expired last month has stopped, a rule
// somebody switched off is off, an inactive PERSON is not counted at all, and a
// rule at any scope counts while it is live.

// denyByDefault puts a company on the policy a REAL new customer gets.
//
// THIS IS NOT TEST CONVENIENCE, it is the difference between the two tenants
// that exist in production (018_default_person_access.sql):
//
//	NONE           what createCompany writes, so what SELF-SERVICE SIGNUP and
//	               platform-created tenants start with. A person added here gets
//	               no rule and opens nothing until somebody says otherwise. This
//	               is the customer the onboarding item exists for.
//	COMPANY_ALLOW  the column default, which existing tenants were migrated to so
//	               that nothing they had built changed underneath them. A person
//	               added here is granted a company-wide ALLOW at creation.
//
// `newTestEnv` inserts its companies with raw SQL and therefore inherits the
// COLUMN default, COMPANY_ALLOW -- which is the older of the two and not what a
// new customer sees. Tests about the onboarding figure have to say which tenant
// they are, and both are covered below.
func denyByDefault(t *testing.T, companyID int64) {
	t.Helper()
	if _, err := database.DB.Exec(
		`UPDATE companies SET default_person_access = 'NONE' WHERE id = $1`,
		companyID); err != nil {
		t.Fatalf("setting deny-by-default: %v", err)
	}
}

// onboardingSite creates a site and returns its public id, so a rule has
// somewhere to point.
func onboardingSite(t *testing.T, env *testEnv, token, csrf, name string) string {
	t.Helper()
	code, body := createSite(t, env, token, csrf, name)
	if code != http.StatusCreated {
		t.Fatalf("creating a site = %d (%v)", code, body)
	}
	// The create response nests the site beside its one-time credential, which
	// this test has no use for and must not touch.
	site, _ := body["site"].(map[string]any)
	id, _ := site["id"].(string)
	if id == "" {
		t.Fatalf("the created site carries no id: %v", body)
	}
	return id
}

// onboardingCount reads people_without_access for the signed-in company.
func onboardingCount(t *testing.T, env *testEnv, token string) int {
	t.Helper()
	code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/onboarding", "", token, "")
	if code != http.StatusOK {
		t.Fatalf("GET /console/onboarding = %d (%v)", code, body)
	}
	value, ok := body["people_without_access"].(float64)
	if !ok {
		t.Fatalf("no people_without_access in %v", body)
	}
	return int(value)
}

// THE CASE THE WHOLE ITEM EXISTS FOR: a roster with nobody granted anything.
func TestOnboardingCountsPeopleWithNoAccessRule(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
	denyByDefault(t, company)

	for _, id := range []string{"P-1", "P-2", "P-3"} {
		code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
			jsonBody(t, map[string]any{"external_id": id, "full_name": "Somebody " + id}),
			token, csrf)
		if code != http.StatusCreated {
			t.Fatalf("creating %s = %d (%v)", id, code, body)
		}
	}

	if got := onboardingCount(t, env, token); got != 3 {
		t.Errorf("people_without_access = %d, want 3 -- nobody has been granted anything", got)
	}
}

// Granting one rule takes exactly one person out of the count, and the count
// agrees with what that person's own permissions read returns.
func TestGrantingAccessRemovesSomebodyFromTheCount(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
	denyByDefault(t, company)
	site := onboardingSite(t, env, token, csrf, "Main Site")

	for _, id := range []string{"P-1", "P-2"} {
		if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
			jsonBody(t, map[string]any{"external_id": id, "full_name": "Somebody"}),
			token, csrf); code != http.StatusCreated {
			t.Fatalf("creating %s = %d (%v)", id, code, body)
		}
	}
	if got := onboardingCount(t, env, token); got != 2 {
		t.Fatalf("before granting = %d, want 2", got)
	}

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/people/P-1/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    site,
			"effect":     models.EffectAllow,
		}), token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("granting = %d (%v)", code, body)
	}

	if got := onboardingCount(t, env, token); got != 1 {
		t.Errorf("after granting to one of two = %d, want 1", got)
	}

	// AND IT AGREES WITH THE PERSON'S OWN PAGE, which is the property that makes
	// the figure safe to show. Two screens computing "has access" differently is
	// how an operator ends up with no way to tell which one is wrong.
	_, granted := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/people/P-1/permissions", "", token, "")
	if len(listOf(t, granted, "permissions")) == 0 {
		t.Error("P-1 is out of the count but their own page lists no rules")
	}
	_, ungranted := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/people/P-2/permissions", "", token, "")
	if len(listOf(t, ungranted, "permissions")) != 0 {
		t.Error("P-2 is in the count but their own page lists a rule")
	}
}

// A DEACTIVATED PERSON IS NOT A CHORE. They are deliberately not admitted, so
// counting them would turn a decision the customer made into something the
// console nags them about -- the same reasoning that keeps a deactivated site
// out of the attention list.
func TestOnboardingIgnoresInactivePeople(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
	denyByDefault(t, company)

	if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
		jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
		token, csrf); code != http.StatusCreated {
		t.Fatalf("creating = %d (%v)", code, body)
	}
	if got := onboardingCount(t, env, token); got != 1 {
		t.Fatalf("active person with no rule = %d, want 1", got)
	}

	if code, body := consoleCall(t, env.router, http.MethodPut, "/api/v1/console/people/P-1",
		jsonBody(t, map[string]any{"full_name": "Somebody", "active": false}),
		token, csrf); code != http.StatusOK {
		t.Fatalf("deactivating = %d (%v)", code, body)
	}

	if got := onboardingCount(t, env, token); got != 0 {
		t.Errorf("after deactivating = %d, want 0", got)
	}
}

// EVERY STATE A RULE CAN BE IN, driven through the granting endpoint rather than
// through SQL, so what is measured is the API a customer's console actually
// calls.
//
// The three refusing cases are the P1 this replaced: each one left somebody
// unable to get in while the overview reported the roster as covered.
func TestOnboardingCountsOnlyRulesInForce(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name    string
		rule    map[string]any
		covered bool
	}{
		{
			name:    "a rule with no dates at all, which is what one click grants",
			rule:    map[string]any{},
			covered: true,
		},
		{
			name: "a rule whose window is open right now",
			rule: map[string]any{
				"starts_at": now.Add(-24 * time.Hour).Format(time.RFC3339),
				"ends_at":   now.Add(24 * time.Hour).Format(time.RFC3339),
			},
			covered: true,
		},
		{
			name: "a rule that does not start until next week",
			rule: map[string]any{
				"starts_at": now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
			},
			covered: false,
		},
		{
			name: "a rule that expired last month",
			rule: map[string]any{
				"starts_at": now.Add(-60 * 24 * time.Hour).Format(time.RFC3339),
				"ends_at":   now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
			},
			covered: false,
		},
		{
			name:    "a rule granted already switched off",
			rule:    map[string]any{"active": false},
			covered: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cheapBcrypt(t)
			env := newTestEnv(t)
			company := operatorCompanyID(t, "one")
			token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
			denyByDefault(t, company)
			site := onboardingSite(t, env, token, csrf, "Main Site")

			if code, body := consoleCall(t, env.router, http.MethodPost,
				"/api/v1/console/people",
				jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
				token, csrf); code != http.StatusCreated {
				t.Fatalf("creating = %d (%v)", code, body)
			}
			if got := onboardingCount(t, env, token); got != 1 {
				t.Fatalf("before granting = %d, want 1", got)
			}

			rule := map[string]any{
				"scope_type": models.ScopeSite,
				"site_id":    site,
				"effect":     models.EffectAllow,
			}
			for key, value := range testCase.rule {
				rule[key] = value
			}
			if code, body := consoleCall(t, env.router, http.MethodPost,
				"/api/v1/console/people/P-1/permissions",
				jsonBody(t, rule), token, csrf); code != http.StatusCreated {
				t.Fatalf("granting = %d (%v)", code, body)
			}

			// THE RULE EXISTS EITHER WAY. Whatever the count says, the person's
			// own page lists what was granted -- the two screens answer different
			// questions about the same row, and neither may hide it.
			_, listed := consoleCall(t, env.router, http.MethodGet,
				"/api/v1/console/people/P-1/permissions", "", token, "")
			if len(listOf(t, listed, "permissions")) != 1 {
				t.Fatal("the person page lists no rule after granting one")
			}

			want := 1
			if testCase.covered {
				want = 0
			}
			if got := onboardingCount(t, env, token); got != want {
				t.Errorf("people_without_access = %d, want %d for %s",
					got, want, testCase.name)
			}
		})
	}
}

// THE EDGE OF THE WINDOW, which is where an off-by-one lives.
//
// Exact equality with CURRENT_TIMESTAMP cannot be tested -- the clock moves
// between the insert and the count -- so this pins the two seconds either side
// of it. A rule that started a moment ago is in force; one that ended a moment
// ago is not.
func TestOnboardingReadsTheEdgesOfAValidityWindow(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
	denyByDefault(t, company)
	site := onboardingSite(t, env, token, csrf, "Main Site")
	now := time.Now().UTC()

	if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
		jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
		token, csrf); code != http.StatusCreated {
		t.Fatalf("creating = %d (%v)", code, body)
	}

	// Started two seconds ago, no end: in force.
	if code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/people/P-1/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    site,
			"effect":     models.EffectAllow,
			"starts_at":  now.Add(-2 * time.Second).Format(time.RFC3339),
		}), token, csrf); code != http.StatusCreated {
		t.Fatalf("granting = %d (%v)", code, body)
	}
	if got := onboardingCount(t, env, token); got != 0 {
		t.Errorf("a rule that started two seconds ago leaves %d uncovered, want 0", got)
	}

	// Move its end two seconds into the past: no longer in force.
	if _, err := database.DB.Exec(
		`UPDATE permissions
		    SET ends_at = CURRENT_TIMESTAMP - INTERVAL '2 seconds'
		  WHERE person_id = (SELECT id FROM people WHERE external_id = 'P-1')`); err != nil {
		t.Fatalf("expiring the rule: %v", err)
	}
	if got := onboardingCount(t, env, token); got != 1 {
		t.Errorf("a rule that ended two seconds ago leaves %d uncovered, want 1", got)
	}
}

// A RULE SOMEBODY SWITCHED OFF IS NOT ACCESS, and this is the case that used to
// be asserted the other way round.
//
// The old reasoning was that the count should agree with ListPersonPermissions,
// which lists an inactive rule rather than hiding it. That is still true and it
// is still what the person page does -- but the panel LISTS AND GRADES, while
// the overview makes a claim about whether people can get in. Both are right at
// once: the rule is on the page, badged "Inactive", and its holder is in the
// count.
func TestOnboardingDoesNotCountADeactivatedRuleAsAccess(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")
	denyByDefault(t, company)
	site := onboardingSite(t, env, token, csrf, "Main Site")

	if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
		jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
		token, csrf); code != http.StatusCreated {
		t.Fatalf("creating = %d (%v)", code, body)
	}
	if code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/people/P-1/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    site,
			"effect":     models.EffectAllow,
		}), token, csrf); code != http.StatusCreated {
		t.Fatalf("granting = %d (%v)", code, body)
	}
	if got := onboardingCount(t, env, token); got != 0 {
		t.Fatalf("a live rule leaves %d in the count, want 0", got)
	}

	if _, err := database.DB.Exec(
		`UPDATE permissions SET active = FALSE WHERE person_id =
		   (SELECT id FROM people WHERE external_id = 'P-1')`); err != nil {
		t.Fatalf("deactivating the rule: %v", err)
	}

	// THE PANEL STILL LISTS IT. This is the half of the old assertion that was
	// correct and must not regress: hiding the rule would leave an operator
	// unable to find the thing they need to switch back on.
	_, listed := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/people/P-1/permissions", "", token, "")
	if len(listOf(t, listed, "permissions")) != 1 {
		t.Fatal("the person page stopped listing an inactive rule; this test's premise is gone")
	}

	// AND THE COUNT NOW SAYS THEY CANNOT GET IN, which is true: the engine
	// filters on `p.active` in its own WHERE clause, so this person is refused
	// at every terminal in the company.
	if got := onboardingCount(t, env, token); got != 1 {
		t.Errorf("people_without_access = %d after switching the only rule off, want 1", got)
	}
}

// The count is company-scoped like every other console read. A rule in one
// tenant must not make somebody in another look covered.
func TestOnboardingCountIsScopedToTheCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	tokenOne, csrfOne := consoleAdminSession(t, env.router, one, "admin-one@example.com")
	tokenTwo, csrfTwo := consoleAdminSession(t, env.router, two, "admin-two@example.com")
	denyByDefault(t, one)
	denyByDefault(t, two)

	site := onboardingSite(t, env, tokenOne, csrfOne, "Main Site")
	for _, seed := range []struct{ token, csrf string }{{tokenOne, csrfOne}, {tokenTwo, csrfTwo}} {
		if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
			jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
			seed.token, seed.csrf); code != http.StatusCreated {
			t.Fatalf("creating = %d (%v)", code, body)
		}
	}

	if code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/people/P-1/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    site,
			"effect":     models.EffectAllow,
		}), tokenOne, csrfOne); code != http.StatusCreated {
		t.Fatalf("granting in company one = %d (%v)", code, body)
	}

	if got := onboardingCount(t, env, tokenOne); got != 0 {
		t.Errorf("company one = %d, want 0", got)
	}
	if got := onboardingCount(t, env, tokenTwo); got != 1 {
		t.Errorf("company two = %d, want 1 -- another tenant's rule covered nobody here", got)
	}
}

// VIEWER, matching the per-person permissions read this aggregates. The operator
// most likely to open the overview is the one with the fewest privileges.
func TestOnboardingStateIsReadableByAViewer(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	_, token, _ := consoleOperatorSession(t, env.router, company,
		"viewer@example.com", models.RoleViewer)

	if code, body := consoleCall(t, env.router, http.MethodGet,
		"/api/v1/console/onboarding", "", token, ""); code != http.StatusOK {
		t.Errorf("viewer GET /console/onboarding = %d, want 200 (%v)", code, body)
	}
}

// Unauthenticated callers get nothing, like every other console read.
func TestOnboardingStateRequiresASession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/console/onboarding",
	})
	if code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401 (%v)", code, body)
	}
}

// A LEGACY TENANT NEVER SEES THE ITEM, and that is the figure doing its job
// rather than a gap in it.
//
// A company on COMPANY_ALLOW writes a company-wide rule at person creation
// (018_default_person_access.sql), so its roster is never without access and the
// count is always zero. The onboarding step is therefore self-calibrating: it
// appears for the deny-by-default tenants a new customer actually gets, and
// stays silent for the migrated ones where it would be nagging about a state
// they cannot be in.
func TestOnboardingSaysNothingForACompanyThatGrantsAccessOnCreation(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, company, "admin@example.com")

	// The column default, and what newTestEnv's raw INSERT already produces --
	// asserted rather than assumed, so this test still means something if the
	// fixture changes.
	policy := queryString(t,
		`SELECT default_person_access FROM companies WHERE id = $1`, company)
	if policy != "COMPANY_ALLOW" {
		t.Fatalf("fixture policy = %q, want COMPANY_ALLOW for this test", policy)
	}

	if code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/people",
		jsonBody(t, map[string]any{"external_id": "P-1", "full_name": "Somebody"}),
		token, csrf); code != http.StatusCreated {
		t.Fatalf("creating = %d (%v)", code, body)
	}

	if got := onboardingCount(t, env, token); got != 0 {
		t.Errorf("people_without_access = %d, want 0 -- this tenant grants access "+
			"at creation, so nobody is ever without it", got)
	}
}

// AND THE CUSTOMER SIGNUP CREATES IS THE DENY-BY-DEFAULT ONE. The onboarding
// item's entire premise is that a self-service customer's people start with no
// rule; if signup ever switched to COMPANY_ALLOW the item would become dead code
// and nothing else would notice.
func TestSelfServiceSignupCreatesADenyByDefaultCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	mustRegister(t, env.router, signupBody(nil))

	policy := queryString(t, `
		SELECT c.default_person_access
		  FROM companies c
		  JOIN users u ON u.company_id = c.id
		 WHERE u.email = $1`, "amaka@harbourfreight.com")
	if policy != "NONE" {
		t.Errorf("a signed-up company's default_person_access = %q, want NONE", policy)
	}
}
