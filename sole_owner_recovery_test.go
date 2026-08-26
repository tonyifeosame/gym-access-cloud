package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Recovery of last resort for a company of ONE (P0-1).
//
// ---------------------------------------------------------------------------
// THE HOLE THESE TESTS EXIST TO KEEP SHUT
// ---------------------------------------------------------------------------
//
// Self-service signup creates a company, a site and exactly one account -- an
// OWNER. That customer then had NO route back into their own console if they
// forgot their password:
//
//	POST /auth/forgot-password           mints a token this platform cannot
//	                                     deliver. It reaches the log and stops.
//	POST /console/operators/:id/reset    is ADMIN-gated and needs a SECOND
//	                                     administrator. There is not one.
//	POST /platform/.../operators         is refused into a company that already
//	                                     has an operator.
//
// The console's forgot-password screen told them to "ask an administrator or
// owner in your own company", which for the most common new customer on the
// installation names nobody at all.
//
// ---------------------------------------------------------------------------
// WHAT IS ASSERTED, AND WHY MOST OF IT IS REFUSAL
// ---------------------------------------------------------------------------
//
// The route that closes the hole is a vendor credential that resets a customer's
// owner account, which is the most dangerous shape of endpoint on this
// installation. So the recovery working is one test and the boundary holding is
// four: it must refuse a company that can recover itself, refuse a company with
// no administrator, refuse an unauthenticated caller, and never be reachable
// from a tenant's own session.
//
// The enumeration guarantee on the PUBLIC forgot-password route is asserted
// separately in credential_handover_test.go and is untouched by any of this --
// nothing here changes what an anonymous caller can learn.

// signupCompanyOfOne registers a customer through the real signup route and
// returns the company's public id.
//
// THROUGH THE ACTUAL ENDPOINT rather than by seeding rows, because the shape
// this is about -- one company, one site, one OWNER, no colleagues -- is
// something signup produces and a fixture would only imitate.
func signupCompanyOfOne(t *testing.T, router *gin.Engine, body string) string {
	t.Helper()
	mustRegister(t, router, body)
	return queryString(t,
		`SELECT c.public_id
		   FROM companies c
		   JOIN users u ON u.company_id = c.id
		  WHERE u.email = $1`, "amaka@harbourfreight.com")
}

// platformAdminSession seeds the installation's administrator and signs in.
func platformAdminSession(t *testing.T, router *gin.Engine) (string, string) {
	t.Helper()
	mustCreatePlatformAdmin(t, "platform@example.com")
	return platformLogin(t, router, "platform@example.com", testPlatformPassword)
}

// ---------------------------------------------------------------------------
// The company of one can be recovered
// ---------------------------------------------------------------------------

// THE TEST THAT WOULD HAVE CAUGHT P0-1. Before this route existed there was no
// request anybody could make that got a sole owner back into their account.
func TestASoleOwnerLockedOutCanBeRecovered(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))
	token, csrf := platformAdminSession(t, env.router)

	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing recovery for a company of one = %d (%v)", code, body)
	}

	// The link is returned to the platform administrator who asked for it, once.
	reset, _ := body["reset"].(map[string]any)
	if reset == nil {
		t.Fatalf("no reset in the response: %v", body)
	}
	link, _ := reset["token"].(string)
	if link == "" {
		t.Fatalf("the reset carries no token: %v", reset)
	}
	if reset["purpose"] != models.TokenPurposeReset {
		t.Errorf("purpose = %v, want %s", reset["purpose"], models.TokenPurposeReset)
	}

	// And it names the account it is for, so nobody sends it to the wrong person.
	operator, _ := body["operator"].(map[string]any)
	if operator == nil || operator["email"] != "amaka@harbourfreight.com" {
		t.Errorf("operator = %v, want the company's owner", operator)
	}
	if operator["role"] != models.RoleOwner {
		t.Errorf("role = %v, want OWNER", operator["role"])
	}

	// THE POINT OF THE WHOLE EXERCISE: the owner can now set a password and sign
	// in with it. A route that minted a token nobody could redeem would pass
	// every assertion above and fix nothing.
	code, redeemBody, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token":        link,
			"new_password": "a-brand-new-password-99",
		}),
	})
	if code != http.StatusNoContent {
		t.Fatalf("redeeming the recovery link = %d (%v)", code, redeemBody)
	}

	code, loginBodyOut, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("amaka@harbourfreight.com", "a-brand-new-password-99"),
	})
	if code != http.StatusOK {
		t.Fatalf("signing in after recovery = %d (%v)", code, loginBodyOut)
	}
}

// The old password must not survive a recovery. Somebody using this believes
// their credential was lost or known to somebody else.
func TestRecoveryInvalidatesTheOldPassword(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))
	token, csrf := platformAdminSession(t, env.router)

	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing recovery = %d (%v)", code, body)
	}
	reset, _ := body["reset"].(map[string]any)
	link, _ := reset["token"].(string)

	code, _, _ = doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/redeem",
		body: jsonBody(t, map[string]string{
			"token": link, "new_password": "a-brand-new-password-99",
		}),
	})
	if code != http.StatusNoContent {
		t.Fatalf("redeeming = %d", code)
	}

	code, _, _ = doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("amaka@harbourfreight.com", signupPassword),
	})
	if code == http.StatusOK {
		t.Fatal("the password from before the recovery still signs in")
	}
}

// ---------------------------------------------------------------------------
// The boundary: this must not become a way into a healthy tenant
// ---------------------------------------------------------------------------

// A company that can recover ITSELF must be refused. This is the predicate that
// stops the route being a standing back door into every customer.
func TestRecoveryIsRefusedForACompanyWithASecondAdministrator(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))

	// The owner adds an administrator, exactly as a growing customer would.
	ownerToken, ownerCSRF := login(t, env.router,
		"amaka@harbourfreight.com", signupPassword)
	code, created := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]string{
			"email":     "deputy@harbourfreight.com",
			"full_name": "Deputy",
			"role":      models.RoleAdmin,
		}), ownerToken, ownerCSRF)
	if code != http.StatusCreated {
		t.Fatalf("adding a second administrator = %d (%v)", code, created)
	}

	token, csrf := platformAdminSession(t, env.router)
	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("recovery for a company with two administrators = %d, want 409 (%v)",
			code, body)
	}
}

// A MANAGER or a VIEWER is NOT a second administrator. The console's own reset
// is ADMIN-gated, so an owner whose only colleague is a viewer is exactly as
// stranded as one with no colleague -- and counting them would refuse recovery
// to somebody who genuinely has none.
func TestAViewerDoesNotCountAsASecondAdministrator(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))

	ownerToken, ownerCSRF := login(t, env.router,
		"amaka@harbourfreight.com", signupPassword)
	code, created := consoleCall(t, env.router, http.MethodPost, "/api/v1/console/operators",
		jsonBody(t, map[string]string{
			"email":     "frontdesk@harbourfreight.com",
			"full_name": "Front Desk",
			"role":      models.RoleViewer,
		}), ownerToken, ownerCSRF)
	if code != http.StatusCreated {
		t.Fatalf("adding a viewer = %d (%v)", code, created)
	}

	token, csrf := platformAdminSession(t, env.router)
	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("recovery for an owner whose only colleague is a viewer = %d, "+
			"want 201 (%v)", code, body)
	}
}

// An unauthenticated caller must get nothing. This route returns a credential.
func TestRecoveryIsRefusedWithoutAPlatformSession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))

	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost,
		path:   "/api/v1/platform/companies/" + companyID + "/recovery",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous recovery = %d, want 401 (%v)", code, body)
	}
}

// A TENANT'S OWN SESSION MUST NOT REACH IT. Both cookies are Path=/, so a
// browser genuinely offers an operator session to this route; the only thing
// keeping it out is that the platform middleware reads a different cookie name.
// If that ever changes, an owner could mint a reset for their own company --
// and, worse, the route would be reachable with an ordinary tenant credential.
func TestATenantSessionCannotReachRecovery(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))
	ownerToken, ownerCSRF := login(t, env.router,
		"amaka@harbourfreight.com", signupPassword)

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", ownerToken, ownerCSRF)
	if code != http.StatusUnauthorized {
		t.Fatalf("a tenant session reaching platform recovery = %d, want 401 (%v)",
			code, body)
	}
}

// ---------------------------------------------------------------------------
// The customer can see it happened
// ---------------------------------------------------------------------------

// A recovery mechanism whose use is visible only to the party using it is one
// nobody can hold to account. The action lands in the TENANT'S trail.
func TestRecoveryIsAuditedIntoTheCustomersOwnTrail(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))
	token, csrf := platformAdminSession(t, env.router)

	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing recovery = %d (%v)", code, body)
	}

	got := queryString(t, `
		SELECT actor_role FROM audit_events
		 WHERE action = 'COMPANY_OWNER_RECOVERY_ISSUED'
		   AND company_id = (SELECT id FROM companies WHERE public_id = $1)`,
		companyID)
	if got != "PLATFORM" {
		t.Errorf("actor_role = %q, want PLATFORM -- the trail must not read as "+
			"though somebody inside the company did this", got)
	}
}

// ---------------------------------------------------------------------------
// The store's predicate, directly
// ---------------------------------------------------------------------------

// A company with no active administrator at all is refused rather than served.
// Resetting a manager would hand back an account that still cannot administer
// anything, which looks like a recovery and fixes nothing.
func TestRecoveryIsRefusedWhenThereIsNoAdministrator(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	companyID := signupCompanyOfOne(t, env.router, signupBody(nil))

	// Deactivate the only owner, leaving the company administratively empty.
	if _, err := database.DB.Exec(
		`UPDATE users SET active = FALSE WHERE email = $1`,
		"amaka@harbourfreight.com"); err != nil {
		t.Fatalf("deactivating the owner: %v", err)
	}

	if _, err := database.SoleAdministratorForRecovery(companyID); err == nil {
		t.Fatal("a company with no active administrator resolved one")
	} else if err != database.ErrCompanyHasNoAdmin {
		t.Errorf("error = %v, want ErrCompanyHasNoAdmin", err)
	}

	token, csrf := platformAdminSession(t, env.router)
	code, body := platformCall(t, env.router, http.MethodPost,
		"/api/v1/platform/companies/"+companyID+"/recovery", "", token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("recovery with no administrator = %d, want 409 (%v)", code, body)
	}
}
