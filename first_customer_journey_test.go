package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/models"
)

// The whole first-customer journey, from an empty form to somebody getting in.
//
// ---------------------------------------------------------------------------
// WHY THIS EXISTS WHEN EVERY STEP IS ALREADY TESTED
// ---------------------------------------------------------------------------
//
// Signup, announcement, the onboarding count, the authorization engine and the
// decision preview each have their own suite, and every one of them passes on
// its own fixture. None of them starts where a real customer starts. They begin
// from `newTestEnv`, whose companies are inserted with raw SQL and therefore
// inherit the COLUMN default for `default_person_access` -- COMPANY_ALLOW, the
// legacy tenant shape, in which a person is granted a company-wide rule the
// moment they are created.
//
// A SELF-SERVICE TENANT IS THE OPPOSITE. handlers/signup.go writes NONE, so a
// new customer's first person is granted nothing and reaches nothing. That is
// the correct behaviour and it is the behaviour every screen in the console has
// to be honest about -- and it is precisely the path no test walked end to end.
//
// So this asserts the seam between the three fixes rather than any one of them:
//
//	P0-3  the onboarding count is non-zero exactly while somebody cannot get in,
//	      so the overview's checklist cannot fall silent on a deployment that
//	      admits nobody.
//	P0-2  a terminal with NO feature assigned and NO feature enabled is not
//	      inert. The console tells a new customer their multi-purpose terminal is
//	      ready to use; if the engine disagreed, that copy would be a lie.
//	      Step 6 grants access with nothing turned on and expects GRANTED.
//	P0-1  is the recovery path and is covered by sole_owner_recovery_test.go --
//	      it is the journey a customer takes when this one has already happened.
//
// DENY-BY-DEFAULT IS ASSERTED IN BOTH DIRECTIONS. Step 5 expects a refusal for a
// person who exists, is active, and has been added by the customer themselves --
// the case operators coming from other products assume is a grant. A test that
// only checked the happy path would pass just as well against a platform that
// admitted everybody.

// journey drives the console exactly as the browser does: session cookie, CSRF
// header, one call per thing a customer clicks.
type journey struct {
	env    *testEnv
	token  string
	csrf   string
	serial string
	site   string
}

func (j *journey) console(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, j.env.router, method, path, body, j.token, j.csrf)
}

// withoutAccess reads the figure the overview's last checklist item is built on.
func (j *journey) withoutAccess(t *testing.T) int {
	t.Helper()
	code, body := j.console(t, http.MethodGet, "/api/v1/console/onboarding", "")
	if code != http.StatusOK {
		t.Fatalf("GET /console/onboarding = %d (%v)", code, body)
	}
	count, ok := body["people_without_access"].(float64)
	if !ok {
		t.Fatalf("no people_without_access in %v", body)
	}
	return int(count)
}

// wouldGetIn asks the engine the question the console's "Check access" asks.
func (j *journey) wouldGetIn(t *testing.T, externalID string) (bool, string) {
	t.Helper()
	code, body := j.console(t, http.MethodPost,
		"/api/v1/console/terminals/"+j.serial+"/evaluate",
		jsonBody(t, map[string]any{"external_id": externalID}))
	if code != http.StatusOK {
		t.Fatalf("evaluating %s = %d (%v)", externalID, code, body)
	}
	granted, _ := body["granted"].(bool)
	reason, _ := body["reason"].(string)
	return granted, reason
}

func TestFirstCustomerJourneyFromSignupToFirstAccess(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	// --- 1 · register -------------------------------------------------------
	//
	// One form. The tenant, its Main Site and its OWNER all come from this, and
	// nobody has touched a database.
	session, token := mustRegister(t, env.router, signupBody(nil))
	csrf, _ := session["csrf_token"].(string)
	if csrf == "" {
		t.Fatal("signup returned no CSRF token, so the new owner cannot write anything")
	}
	company := signupCompanyID(t, "harbour-freight-ltd")

	// THE TENANT SHAPE THE REST OF THIS TEST DEPENDS ON. Asserted rather than
	// assumed: if signup ever started writing COMPANY_ALLOW, every step below
	// would still pass and would be measuring the legacy tenant instead.
	if got := queryString(t,
		`SELECT default_person_access FROM companies WHERE id = $1`, company); got != "NONE" {
		t.Fatalf("a self-registered company is %q, want NONE -- this test measures "+
			"the deny-by-default tenant", got)
	}

	j := &journey{env: env, token: token, csrf: csrf, serial: "AT-JOURNEY"}
	j.site = sitePublicIDByName(t, "Main Site")

	// NOBODY IS MISSING ACCESS YET, because nobody exists. The checklist item
	// must not fire for an empty roster -- "nobody can get in" is not a useful
	// thing to say about nobody, and a new customer meeting it on their first
	// screen would be told off for a step they have not reached.
	if got := j.withoutAccess(t); got != 0 {
		t.Errorf("people_without_access = %d on a brand-new company, want 0", got)
	}

	// --- 2 · the terminal announces itself ----------------------------------
	//
	// The path the console tells a new customer to take: power it on, read the
	// code off its screen. No serial typed, no cable, no provisioning key --
	// which signup deliberately never disclosed.
	announced := env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":     j.serial,
		"firmware_version":  "1.4.0",
		"hardware_revision": "rev-C",
	}, nil)
	if announced.Code != http.StatusCreated {
		t.Fatalf("announcing = %d: %s", announced.Code, announced.Raw)
	}
	pairingCode, _ := announced.Body["pairing_code"].(string)
	announceToken, _ := announced.Body["announce_token"].(string)
	if pairingCode == "" || announceToken == "" {
		t.Fatalf("announce returned no code or token: %s", announced.Raw)
	}

	// --- 3 · the customer adopts and approves it ----------------------------
	code, body := j.console(t, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		jsonBody(t, map[string]any{"pairing_code": pairingCode}))
	if code != http.StatusOK {
		t.Fatalf("adopting = %d (%v)", code, body)
	}
	announcementID, _ := body["id"].(string)

	if code, body := j.console(t, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+announcementID+"/approve",
		jsonBody(t, map[string]any{
			"site_id":     j.site,
			"device_name": "Front Entrance",
		})); code != http.StatusOK {
		t.Fatalf("approving = %d (%v)", code, body)
	}

	// The terminal collects its own credential on its next poll. Until this
	// happens it is approved but not yet working, which is a distinction the
	// console draws and therefore one this journey has to actually reach.
	collected := env.do(http.MethodGet, "/api/v1/devices/announce", nil,
		map[string]string{"X-Announce-Token": announceToken})
	if key, _ := collected.Body["api_key"].(string); key == "" {
		t.Fatalf("the terminal collected no credential: %s", collected.Raw)
	}

	// --- 4 · what the customer now has --------------------------------------
	//
	// A LIVE TERMINAL SERVING NOTHING IN PARTICULAR, and this is the state the
	// console used to call broken. Both halves are asserted because the console's
	// reassurance depends on both: the terminal is MULTI_PURPOSE, and the company
	// has turned no feature on.
	if mode := queryString(t,
		`SELECT application_mode FROM devices WHERE serial_number = $1`,
		j.serial); mode != models.AppMultiPurpose {
		t.Errorf("a newly approved terminal is %q, want %q -- the console promises "+
			"multi-purpose is the default", mode, models.AppMultiPurpose)
	}
	if n := queryInt(t,
		`SELECT count(*) FROM company_applications
		  WHERE company_id = $1 AND enabled`, company); n != 0 {
		t.Fatalf("a new company has %d features enabled, want 0 -- this test is "+
			"about the terminal that serves nothing in particular", n)
	}

	// --- 5 · a person is added, and cannot get in ---------------------------
	//
	// THE STEP THE CHECKLIST USED TO STOP SHORT OF. The customer has hardware and
	// a roster, every other onboarding item has gone quiet, and their deployment
	// admits nobody.
	if code, body := j.console(t, http.MethodPost, "/api/v1/console/people",
		jsonBody(t, map[string]any{
			"external_id": "STAFF-001",
			"full_name":   "Amaka Obi",
		})); code != http.StatusCreated {
		t.Fatalf("adding a person = %d (%v)", code, body)
	}

	if got := j.withoutAccess(t); got != 1 {
		t.Errorf("people_without_access = %d after adding one person with no rule, "+
			"want 1 -- the overview would show a finished checklist over a "+
			"deployment that admits nobody", got)
	}

	// ABSENCE OF PERMISSION IS NOT PERMISSION, at the engine and not merely in
	// the copy. The person exists, is active, and was added by the customer
	// themselves -- the case an operator coming from another product assumes is
	// a grant.
	if granted, reason := j.wouldGetIn(t, "STAFF-001"); granted {
		t.Errorf("a person with no rule was GRANTED (%q) -- deny-by-default is the "+
			"whole authorization model", reason)
	}
	if !queryBool(t, `SELECT active FROM people WHERE external_id = 'STAFF-001'`) {
		t.Fatal("fixture error: the person should be active, which is what makes " +
			"the refusal above meaningful")
	}

	// --- 6 · one rule, and the product does its job -------------------------
	if code, body := j.console(t, http.MethodPost,
		"/api/v1/console/people/STAFF-001/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    j.site,
			"effect":     models.EffectAllow,
		})); code != http.StatusCreated {
		t.Fatalf("granting access = %d (%v)", code, body)
	}

	// The checklist falls silent HERE and not before -- when the deployment
	// genuinely admits somebody.
	if got := j.withoutAccess(t); got != 0 {
		t.Errorf("people_without_access = %d after granting the only person a rule, "+
			"want 0", got)
	}

	// FIRST ACCESS, with no feature turned on and the terminal assigned to
	// nothing. This is the assertion that makes the console's "Multi-purpose,
	// which is ready to use" honest: if the capability gate ran here, a new
	// customer's first terminal would refuse everybody and the reassurance would
	// be the bug.
	granted, reason := j.wouldGetIn(t, "STAFF-001")
	if !granted {
		t.Fatalf("the first person granted access at the first terminal was REFUSED "+
			"(%q) -- a multi-purpose terminal at a company with no features "+
			"enabled must admit normally", reason)
	}
}

// The other half of step 5, and the reason the count is a count rather than a
// boolean: a customer part-way through granting access is neither finished nor
// at the beginning, and the overview says which.
func TestJourneyCountsOnlyThePeopleStillWithoutAccess(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	session, token := mustRegister(t, env.router, signupBody(nil))
	csrf, _ := session["csrf_token"].(string)
	j := &journey{env: env, token: token, csrf: csrf}
	j.site = sitePublicIDByName(t, "Main Site")

	for _, id := range []string{"STAFF-001", "STAFF-002", "STAFF-003"} {
		if code, body := j.console(t, http.MethodPost, "/api/v1/console/people",
			jsonBody(t, map[string]any{"external_id": id, "full_name": "Somebody"})); code != http.StatusCreated {
			t.Fatalf("adding %s = %d (%v)", id, code, body)
		}
	}
	if got := j.withoutAccess(t); got != 3 {
		t.Fatalf("people_without_access = %d, want 3", got)
	}

	if code, body := j.console(t, http.MethodPost,
		"/api/v1/console/people/STAFF-002/permissions",
		jsonBody(t, map[string]any{
			"scope_type": models.ScopeSite,
			"site_id":    j.site,
			"effect":     models.EffectAllow,
		})); code != http.StatusCreated {
		t.Fatalf("granting = %d (%v)", code, body)
	}

	// Two, not zero and not three. The overview names the number, so a wrong one
	// here is a wrong sentence on the customer's first screen.
	if got := j.withoutAccess(t); got != 2 {
		t.Errorf("people_without_access = %d after granting one of three, want 2", got)
	}
}
