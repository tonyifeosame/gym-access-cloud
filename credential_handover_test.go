package main

import (
	"net/http"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator credential handover (PPL-02, SEC-10).
//
// PPL-02: creating an operator required the CALLER to choose the new operator's
// password and hand it over themselves. There was no invitation, no single-use
// link, and no "must change at first sign-in" flag -- users.password_changed_at
// existed and nothing read it as a policy. The realistic consequence is not
// hypothetical: initial credentials travel by chat in plain text, typically stay
// unchanged, and the administrator who created the account knows the password
// indefinitely with nothing recording that they do.
//
// SEC-10: at the other end there was no reset at all. A sole OWNER who forgot
// their password needed database access to recover.
//
// These are the same mechanism at two ends of an account's life, so they are
// tested together: a single-use, short-lived, high-entropy token authorising a
// password set without knowing the current one.

// consoleAdminSession signs in an ADMIN and returns its session and CSRF tokens.
//
// The handover routes are all ADMIN-gated, and every test here needs one.
func consoleAdminSession(t *testing.T, router *gin.Engine, companyID int64,
	email string) (string, string) {
	t.Helper()
	_, token, csrf := consoleOperatorSession(t, router, companyID, email, models.RoleAdmin)
	return token, csrf
}

// operatorPublicID resolves an operator's UUID from its address.
func operatorPublicID(t *testing.T, email string) string {
	t.Helper()
	return queryString(t, `SELECT public_id FROM users WHERE email = $1`, email)
}

// ---------------------------------------------------------------------------
// PPL-02: an operator can be created without anybody choosing their password
// ---------------------------------------------------------------------------

// THE TEST THAT WOULD HAVE CAUGHT PPL-02. Creating an operator with no password
// used to be a 400; now it mints an account nobody holds a credential for and a
// link that only its owner can use.
func TestCreatingAnOperatorWithoutAPasswordIssuesAnInvitation(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	code, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email":     "invited@example.com",
			"full_name": "Invited Operator",
			"role":      models.RoleManager,
		}), token, csrf)

	if code != http.StatusCreated {
		t.Fatalf("creating an operator without a password = %d (%v)", code, body)
	}

	invitation, _ := body["invitation"].(map[string]any)
	if invitation == nil {
		t.Fatalf("no invitation in %v", body)
	}
	link, _ := invitation["token"].(string)
	if link == "" {
		t.Fatal("the invitation carries no token")
	}
	if invitation["shown_once"] != true {
		t.Error("the invitation does not say it is shown once")
	}

	// The account exists, is flagged, and cannot be signed in to.
	if !queryBool(t,
		`SELECT must_change_password FROM users WHERE email = 'invited@example.com'`) {
		t.Error("an invited operator is not flagged must_change_password")
	}

	// The plaintext exists in that response and nowhere else.
	if got := queryInt(t, `
		SELECT count(*) FROM user_credential_tokens t
		  JOIN users u ON u.id = t.user_id
		 WHERE u.email = 'invited@example.com'
		   AND t.purpose = 'INVITE' AND t.redeemed_at IS NULL`); got != 1 {
		t.Errorf("outstanding invitations = %d, want 1", got)
	}
}

// Redeeming is the whole point: it sets a password the account's owner chose,
// and clears the flag.
func TestRedeemingAnInvitationSetsAPassword(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	_, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email": "newcomer@example.com", "full_name": "Newcomer",
			"role": models.RoleViewer,
		}), token, csrf)
	invitation, _ := body["invitation"].(map[string]any)
	link, _ := invitation["token"].(string)

	const chosen = "the-password-they-chose-1"
	code, redeemed, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{"token": link, "new_password": chosen}),
	})
	if code != http.StatusNoContent {
		t.Fatalf("redeeming = %d (%v)", code, redeemed)
	}

	// It signs in, and the flag is gone -- the credential is now theirs.
	sessionToken, _ := login(t, env.router, "newcomer@example.com", chosen)
	if sessionToken == "" {
		t.Fatal("the redeemed account could not sign in")
	}
	if queryBool(t,
		`SELECT must_change_password FROM users WHERE email = 'newcomer@example.com'`) {
		t.Error("must_change_password survived a redemption")
	}
}

// SINGLE USE, enforced by the UPDATE's own predicate rather than by a
// check-then-write.
func TestACredentialTokenIsSingleUse(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	_, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email": "once@example.com", "full_name": "Once", "role": models.RoleViewer,
		}), token, csrf)
	invitation, _ := body["invitation"].(map[string]any)
	link, _ := invitation["token"].(string)

	redeem := func(password string) int {
		code, _, _ := doAuth(t, env.router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/redeem",
			body: jsonBody(t, map[string]string{"token": link, "new_password": password}),
		})
		return code
	}

	if code := redeem("first-choice-password-1"); code != http.StatusNoContent {
		t.Fatalf("first redemption = %d, want 204", code)
	}
	if code := redeem("second-choice-password-1"); code != http.StatusConflict {
		t.Errorf("second redemption = %d, want 409 -- the link is single use", code)
	}

	// The first password still works, so the second attempt changed nothing.
	if _, _ = login(t, env.router, "once@example.com", "first-choice-password-1"); false {
		t.Fatal("unreachable")
	}
}

// ISSUING SUPERSEDES. An administrator re-sending an invitation because the
// first "did not arrive" must not leave two live links that two different people
// could each redeem.
func TestIssuingASecondInvitationInvalidatesTheFirst(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	_, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email": "resent@example.com", "full_name": "Resent", "role": models.RoleViewer,
		}), token, csrf)
	first, _ := body["invitation"].(map[string]any)
	firstLink, _ := first["token"].(string)

	code, reissued := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/operators/"+operatorPublicID(t, "resent@example.com")+"/invite",
		"", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("re-inviting = %d (%v)", code, reissued)
	}
	second, _ := reissued["invitation"].(map[string]any)
	secondLink, _ := second["token"].(string)
	if secondLink == "" || secondLink == firstLink {
		t.Fatal("re-inviting did not mint a distinct link")
	}

	// The superseded one is dead.
	code, _, _ = doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": firstLink, "new_password": "should-not-work-here-1"}),
	})
	if code != http.StatusConflict {
		t.Errorf("the superseded invitation returned %d, want 409", code)
	}

	// And the current one is not.
	code, _, _ = doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": secondLink, "new_password": "this-one-works-here-1"}),
	})
	if code != http.StatusNoContent {
		t.Errorf("the current invitation returned %d, want 204", code)
	}
}

// An INVITE is for an account that has never signed in. Redeeming one against a
// live account would be a way to take over somebody's history with a link issued
// before they ever used it.
func TestAnInvitationCannotBeRedeemedAgainstALiveAccount(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "established@example.com", models.RoleManager)

	// Minted directly, so the test can exercise the store's rule rather than the
	// handler's -- the handler refuses this case before it gets here, which is
	// the second half of the same guarantee and is asserted below.
	invite, err := database.IssueCredentialToken(user.ID, models.TokenPurposeInvite, 0, "")
	if err != nil {
		t.Fatalf("issuing an invitation: %v", err)
	}

	// The account signs in, so it is now live.
	login(t, env.router, "established@example.com", testPassword)

	code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": invite.Token, "new_password": "hijacked-password-here-1"}),
	})
	if code != http.StatusConflict {
		t.Errorf("redeeming an INVITE against a live account = %d, want 409", code)
	}

	// The original password still works.
	login(t, env.router, "established@example.com", testPassword)
}

// And the console refuses to issue one in the first place, pointing at the reset
// route instead of quietly issuing a reset wearing an invitation's name.
func TestInvitingAnOperatorThatHasSignedInIsRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	mustCreateOperator(t, one, "active@example.com", models.RoleViewer)
	login(t, env.router, "active@example.com", testPassword)

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/operators/"+operatorPublicID(t, "active@example.com")+"/invite",
		"", token, csrf)
	if code != http.StatusConflict {
		t.Errorf("inviting a signed-in operator = %d (%v), want 409", code, body)
	}
}

// ---------------------------------------------------------------------------
// SEC-10: a locked-out operator can be recovered without database access
// ---------------------------------------------------------------------------

// THE TEST THAT WOULD HAVE CAUGHT SEC-10. There was no reset route at all.
func TestAdministrativeResetIssuesALinkRatherThanAPassword(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	mustCreateOperator(t, one, "forgot@example.com", models.RoleManager)
	login(t, env.router, "forgot@example.com", testPassword)

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/operators/"+operatorPublicID(t, "forgot@example.com")+"/reset",
		"", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing a reset = %d (%v)", code, body)
	}

	reset, _ := body["reset"].(map[string]any)
	link, _ := reset["token"].(string)
	if link == "" {
		t.Fatal("the reset carries no token")
	}
	// THE ADMINISTRATOR NEVER LEARNS A PASSWORD. Nothing in this response is one.
	if _, present := body["password"]; present {
		t.Error("the reset response carries a password")
	}

	const chosen = "recovered-password-here-1"
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{"token": link, "new_password": chosen}),
	}); code != http.StatusNoContent {
		t.Fatalf("redeeming the reset = %d", code)
	}

	login(t, env.router, "forgot@example.com", chosen)
}

// Somebody setting a password through a reset link usually believes the old one
// was known to somebody else. Leaving the sessions that password opened alive
// would make the change cosmetic.
func TestRedeemingARevokesEverySessionForTheAccount(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "compromised@example.com", models.RoleManager)

	// Two live sessions, as an operator signed in on two machines would have.
	first, _ := login(t, env.router, "compromised@example.com", testPassword)
	second, _ := login(t, env.router, "compromised@example.com", testPassword)

	reset, err := database.IssueCredentialToken(user.ID, models.TokenPurposeReset, 0, "")
	if err != nil {
		t.Fatalf("issuing a reset: %v", err)
	}

	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": reset.Token, "new_password": "brand-new-password-here-1"}),
	}); code != http.StatusNoContent {
		t.Fatalf("redeeming = %d", code)
	}

	for i, token := range []string{first, second} {
		if code, _, _ := doAuth(t, env.router, authCall{
			method: http.MethodGet, path: "/api/v1/auth/me", token: token,
		}); code != http.StatusUnauthorized {
			t.Errorf("session %d survived the reset = %d, want 401", i, code)
		}
	}
}

// An expired link is reported as expired rather than as unknown: the caller
// already holds the secret, so this is not an enumeration oracle, and the
// difference is between requesting a new link and concluding the platform is
// broken.
func TestAnExpiredTokenIsRefusedAsExpired(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "stale@example.com", models.RoleViewer)

	reset, err := database.IssueCredentialToken(user.ID, models.TokenPurposeReset, 0, "")
	if err != nil {
		t.Fatalf("issuing a reset: %v", err)
	}
	// created_at moves with expires_at: the table's expiry_check constraint
	// requires expires_at > created_at, so a row cannot be aged by moving one of
	// them alone. That constraint is doing its job -- a token that expired before
	// it was issued is not a state the platform should be able to represent.
	mustExec(t, `
		UPDATE user_credential_tokens
		   SET created_at = CURRENT_TIMESTAMP - INTERVAL '2 hours',
		       expires_at = CURRENT_TIMESTAMP - INTERVAL '1 minute'
		 WHERE user_id = $1`, user.ID)

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": reset.Token, "new_password": "too-late-password-here-1"}),
	})
	if code != http.StatusGone {
		t.Errorf("an expired token = %d (%v), want 410", code, body)
	}

	// The original password is untouched.
	login(t, env.router, "stale@example.com", testPassword)
}

func TestAnUnknownTokenIsRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token":        "ali_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"new_password": "does-not-matter-here-1"}),
	})
	if code != http.StatusNotFound {
		t.Errorf("an unknown token = %d, want 404", code)
	}
}

// The self-service reset is UNAUTHENTICATED BY NECESSITY, which makes it an
// enumeration surface unless every answer is identical.
func TestForgotPasswordAnswersIdenticallyForKnownAndUnknownAddresses(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "known@example.com", models.RoleViewer)

	request := func(email string) (int, map[string]any) {
		code, body, _ := doAuth(t, env.router, authCall{
			method: http.MethodPost, path: "/api/v1/auth/forgot-password",
			body: jsonBody(t, map[string]string{"email": email}),
		})
		return code, body
	}

	knownCode, knownBody := request("known@example.com")
	unknownCode, unknownBody := request("nobody-at-all@example.com")

	if knownCode != http.StatusAccepted || unknownCode != http.StatusAccepted {
		t.Fatalf("codes = %d and %d, want 202 for both", knownCode, unknownCode)
	}
	if knownBody["message"] != unknownBody["message"] {
		t.Errorf("bodies differ:\n known: %v\n unknown: %v -- that is an enumeration oracle",
			knownBody["message"], unknownBody["message"])
	}
	if knownBody["status"] != unknownBody["status"] {
		t.Errorf("statuses differ: %v vs %v", knownBody["status"], unknownBody["status"])
	}

	// THE TOKEN IS NOT IN THE RESPONSE. This is the one place a minted token must
	// not reach its caller: the caller is unauthenticated and has proved nothing.
	for _, field := range []string{"token", "reset", "invitation", "link"} {
		if _, present := knownBody[field]; present {
			t.Errorf("the forgot-password response carries %q", field)
		}
	}

	// A reset was nonetheless issued for the address that exists.
	if got := queryInt(t, `
		SELECT count(*) FROM user_credential_tokens t
		  JOIN users u ON u.id = t.user_id
		 WHERE u.email = 'known@example.com' AND t.purpose = 'RESET'`); got != 1 {
		t.Errorf("resets issued for the known address = %d, want 1", got)
	}
	if got := queryInt(t, `SELECT count(*) FROM user_credential_tokens`); got != 1 {
		t.Errorf("total tokens = %d -- the unknown address minted one", got)
	}
}

// ---------------------------------------------------------------------------
// The forced first change
// ---------------------------------------------------------------------------

// users.password_changed_at existed and nothing read it as a policy. The console
// cannot insist on a change it is never told about, so the session body carries
// it.
func TestTheSessionReportsThatAPasswordMustBeChanged(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	// An ordinary operator whose password they chose themselves.
	mustCreateOperator(t, one, "self@example.com", models.RoleViewer)
	_, _, ordinary := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("self@example.com", testPassword),
	})
	_ = ordinary

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("self@example.com", testPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
	if body["must_change_password"] != false {
		t.Errorf("must_change_password = %v for a self-chosen password, want false",
			body["must_change_password"])
	}

	// And one whose credential an administrator chose.
	consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email": "handed@example.com", "full_name": "Handed",
			"role": models.RoleViewer, "password": testPassword,
		}), token, csrf)
	mustExec(t, `UPDATE users SET must_change_password = TRUE WHERE email = 'handed@example.com'`)

	code, body, _ = doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("handed@example.com", testPassword),
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d (%v)", code, body)
	}
	if body["must_change_password"] != true {
		t.Errorf("must_change_password = %v for an administrator-chosen password, want true",
			body["must_change_password"])
	}

	// REPORTED, NOT ENFORCED. An account refused every request could not reach
	// the endpoint that fixes it.
	flagged, _ := login(t, env.router, "handed@example.com", testPassword)
	if code, _, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: flagged,
	}); code != http.StatusOK {
		t.Errorf("a flagged operator was refused /me = %d, want 200", code)
	}
}

// ---------------------------------------------------------------------------
// Authorisation and audit
// ---------------------------------------------------------------------------

// The handover routes are ADMIN, and the role matrix applies to the TARGET: a
// caller may not reset somebody above them.
func TestHandoverRoutesRespectTheRoleMatrix(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")
	mustCreateOperator(t, one, "owner@example.com", models.RoleOwner)

	ownerID := operatorPublicID(t, "owner@example.com")
	for _, route := range []string{"/invite", "/reset"} {
		code, _ := consoleCall(t, env.router, http.MethodPost,
			"/api/v1/console/operators/"+ownerID+route, "", token, csrf)
		if code != http.StatusForbidden {
			t.Errorf("ADMIN calling %s on an OWNER = %d, want 403", route, code)
		}
	}

	// A MANAGER cannot reach the routes at all -- they are ADMIN-gated.
	mustCreateOperator(t, one, "manager@example.com", models.RoleManager)
	managerToken, managerCSRF := login(t, env.router, "manager@example.com", testPassword)
	code, _ := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/operators/"+ownerID+"/reset", "", managerToken, managerCSRF)
	if code != http.StatusForbidden {
		t.Errorf("MANAGER issuing a reset = %d, want 403", code)
	}
}

// A tenant boundary, checked on the route that hands out credentials.
func TestHandoverCannotReachAnotherTenantsOperator(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")
	mustCreateOperator(t, two, "elsewhere@example.com", models.RoleViewer)

	foreign := operatorPublicID(t, "elsewhere@example.com")
	for _, route := range []string{"/invite", "/reset"} {
		code, _ := consoleCall(t, env.router, http.MethodPost,
			"/api/v1/console/operators/"+foreign+route, "", token, csrf)
		if code != http.StatusNotFound {
			t.Errorf("reaching another tenant's operator via %s = %d, want 404", route, code)
		}
	}

	if got := queryInt(t, `SELECT count(*) FROM user_credential_tokens`); got != 0 {
		t.Errorf("tokens minted across a tenant boundary: %d", got)
	}
}

// SEC-07: who handed out a credential is exactly the kind of thing an audit
// trail exists for.
func TestCredentialHandoverIsAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	token, csrf := consoleAdminSession(t, env.router, one, "admin@example.com")

	// Creation by invitation.
	_, body := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]any{
			"email": "audited@example.com", "full_name": "Audited", "role": models.RoleViewer,
		}), token, csrf)
	invitation, _ := body["invitation"].(map[string]any)
	link, _ := invitation["token"].(string)
	if link == "" {
		t.Fatalf("no invitation token in %v", body)
	}

	// An administrative reset on somebody else.
	mustCreateOperator(t, one, "reset-me@example.com", models.RoleViewer)
	login(t, env.router, "reset-me@example.com", testPassword)
	consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/operators/"+operatorPublicID(t, "reset-me@example.com")+"/reset",
		"", token, csrf)

	// And a redemption, which has no operator session behind it.
	doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": link, "new_password": "audited-password-here-1"}),
	})

	for _, action := range []string{
		"OPERATOR_INVITED", "OPERATOR_RESET_ISSUED", "OPERATOR_CREDENTIAL_REDEEMED",
	} {
		if got := queryInt(t,
			`SELECT count(*) FROM audit_events WHERE action = $1 AND company_id = $2`,
			action, one); got != 1 {
			t.Errorf("audit rows for %s = %d, want 1", action, got)
		}
	}

	// NOTHING SECRET REACHED THE TABLE. The link is the whole authorisation, and
	// an audit trail that recorded it would be a place to read live credentials.
	if got := queryInt(t,
		`SELECT count(*) FROM audit_events WHERE changes::text LIKE '%' || $1 || '%'`,
		link); got != 0 {
		t.Errorf("a live credential token appears in %d audit rows", got)
	}
}

// The expiry sweep, for the maintenance task.
func TestSpentTokensArePurgedAfterTheirRetentionWindow(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "purge@example.com", models.RoleViewer)

	if _, err := database.IssueCredentialToken(user.ID, models.TokenPurposeReset, 0, ""); err != nil {
		t.Fatalf("issuing: %v", err)
	}

	// Live tokens are never purged.
	removed, err := database.PurgeExpiredCredentialTokens(30)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if removed != 0 {
		t.Errorf("a live token was purged: %d", removed)
	}

	// Both timestamps move together; see the note in the expiry test.
	mustExec(t, `
		UPDATE user_credential_tokens
		   SET created_at = CURRENT_TIMESTAMP - INTERVAL '38 days',
		       expires_at = CURRENT_TIMESTAMP - INTERVAL '31 days'
		 WHERE user_id = $1`, user.ID)

	removed, err = database.PurgeExpiredCredentialTokens(30)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if removed != 1 {
		t.Errorf("purged %d rows, want 1", removed)
	}
}

// The invitation window is longer than the reset window, and both are
// configurable: an invitation is often sent before somebody starts, while a
// reset is a live account and the window is an attack surface.
func TestTokenLifetimesAreConfigurableAndDistinct(t *testing.T) {
	if database.InviteTokenTTL() <= database.ResetTokenTTL() {
		t.Errorf("invite TTL %v is not longer than reset TTL %v",
			database.InviteTokenTTL(), database.ResetTokenTTL())
	}

	t.Setenv("RESET_TOKEN_TTL_SECONDS", "300")
	if got := database.ResetTokenTTL(); got != 5*time.Minute {
		t.Errorf("RESET_TOKEN_TTL_SECONDS=300 gave %v, want 5m", got)
	}
}
