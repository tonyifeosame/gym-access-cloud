package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/models"
)

// S4: GET /access/{member_id} used to answer an authorization question without
// evaluating authorization.
//
// THE DEFECT THESE WOULD HAVE CAUGHT. It returned
// `{"granted": true, "message": "Access Granted"}` for any person who existed
// and had `active = TRUE`. It ignored permissions, schedules, validity windows,
// credential state, the terminal, the terminal's application mode and whether
// the site was even in service -- every input APP-02 added.
//
// The word "granted" is what made it dangerous rather than merely incomplete.
// An integrator wiring a turnstile against this endpoint got a system that
// admitted everybody an operator had ever added, while the console showed a
// permission model being enforced.

// TestAccessCheckRequiresATerminal.
//
// Authorization is a question about a person AT A DOOR: permissions are scoped
// to companies, sites and terminals, schedules run in the site's timezone, and
// the terminal's application mode decides which capability the question is even
// about. Without one there is no truthful answer, so the request is refused
// rather than answered with a number that means nothing.
func TestAccessCheckRequiresATerminal(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M-1", "Ada")

	res := env.do(http.MethodGet, "/api/v1/access/M-1", nil, siteAuth(env.siteAKey))

	if res.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body %s)", res.Code, res.Raw)
	}
	// And critically: it did not answer the question anyway.
	if _, present := res.Body["granted"]; present {
		t.Errorf("the refusal still carried a `granted` verdict (body %s)", res.Raw)
	}
}

// TestAccessCheckIsScopedToTheAuthenticatedSite.
//
// The route takes the site key, which is installed on hardware at one location.
// It must not be able to ask what a door at another location would do.
func TestAccessCheckIsScopedToTheAuthenticatedSite(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "M-1", "Ada")
	env.registerDevice(env.siteBKey, "ESP32-SITE-B")

	res := env.do(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-SITE-B", nil,
		siteAuth(env.siteAKey))

	if res.Code != http.StatusNotFound {
		t.Fatalf("site A's key asking about site B's terminal got %d, want 404 (body %s)",
			res.Code, res.Raw)
	}
}

// TestAccessCheckAnswersFromTheAuthorizationEngine is the fix itself.
//
// A person with a live ALLOW is granted; the SAME person with the permission
// withdrawn is refused. Under the old implementation both answers were
// "granted", because both people were `active`.
func TestAccessCheckAnswersFromTheAuthorizationEngine(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-DOOR")
	env.createMember(env.siteAKey, "M-1", "Ada")

	res := env.do(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if got := res.Body["granted"]; got != true {
		t.Fatalf("granted = %v for a person with a live ALLOW (body %s)", got, res.Raw)
	}

	// Withdraw the rule. The person is untouched -- still present, still
	// `active` -- so this is precisely the case the old endpoint got wrong.
	mustExec(t, `UPDATE permissions SET deleted_at = CURRENT_TIMESTAMP
	              WHERE person_id = (SELECT id FROM people WHERE external_id = 'M-1')`)

	res = env.do(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))
	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if got := res.Body["granted"]; got != false {
		t.Errorf("granted = %v for a person with no permission -- absence of permission "+
			"is not permission (body %s)", got, res.Raw)
	}
	if !queryBool(t, `SELECT active FROM people WHERE external_id = 'M-1'`) {
		t.Fatal("fixture error: the person should still be active, which is the whole point")
	}
}

// TestAccessCheckDeniesAnUnknownPerson. Deny-by-default reaches the endpoint
// too: an id nobody holds is refused, and the refusal carries a reason rather
// than a bare false.
func TestAccessCheckDeniesAnUnknownPerson(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-DOOR")

	res := env.do(http.MethodGet, "/api/v1/access/NOBODY?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))

	if res.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %s)", res.Code, res.Raw)
	}
	if got := res.Body["granted"]; got != false {
		t.Errorf("granted = %v for an unknown person", got)
	}
	if reason, _ := res.Body["reason"].(string); reason == "" {
		t.Errorf("the denial carried no reason (body %s)", res.Raw)
	}
}

// TestADisabledTerminalGrantsNobody.
//
// The terminal is an input to the decision, not just a lookup key. A person who
// would be admitted at a working door is refused at a revoked one, and the old
// endpoint could not express that because it never knew which door.
func TestADisabledTerminalGrantsNobody(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"disable@example.com", models.RoleAdmin)

	env.registerDevice(env.siteAKey, "ESP32-DOOR")
	env.createMember(env.siteAKey, "M-1", "Ada")

	code, body := consoleCall(t, env.router, http.MethodPut,
		"/api/v1/console/terminals/ESP32-DOOR/state",
		`{"disabled":true,"reason":"under maintenance"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("disabling the terminal got %d (body %v)", code, body)
	}

	res := env.do(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))
	if got := res.Body["granted"]; got != false {
		t.Errorf("granted = %v at a disabled terminal (body %s)", got, res.Raw)
	}
}

// TestAccessCheckAdvertisesItsSuccessor.
//
// The endpoint is truthful now but still the wrong shape: it authenticates with
// a machine credential a browser must never hold. A client that never reads the
// documentation should still be told where the supported version is.
func TestAccessCheckAdvertisesItsSuccessor(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-DOOR")
	env.createMember(env.siteAKey, "M-1", "Ada")

	rec := env.raw(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))

	if rec.Header().Get("Deprecation") == "" {
		t.Error("no Deprecation header")
	}
	if link := rec.Header().Get("Link"); link == "" {
		t.Error("no Link header naming the successor")
	}
}

// TestAccessCheckKeepsTheLegacyResponseKeys.
//
// Something may be parsing `granted`, `message` and `status`. The change that
// matters is that the values are now true, not that the shape moved -- so an
// existing client keeps working and starts getting correct answers.
func TestAccessCheckKeepsTheLegacyResponseKeys(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-DOOR")
	env.createMember(env.siteAKey, "M-1", "Ada")

	res := env.do(http.MethodGet, "/api/v1/access/M-1?terminal=ESP32-DOOR", nil,
		siteAuth(env.siteAKey))

	for _, key := range []string{"granted", "message", "status"} {
		if _, present := res.Body[key]; !present {
			t.Errorf("response no longer carries %q (body %s)", key, res.Raw)
		}
	}
	if msg, _ := res.Body["message"].(string); msg != "Access Granted" {
		t.Errorf("message = %q for a granted decision, want the legacy string", msg)
	}
}
