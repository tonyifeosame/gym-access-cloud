package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator authentication middleware (middleware/operator_auth.go).
//
// These go through a real gin engine rather than calling the middleware
// functions directly. An auth boundary that is only correct because a test
// invoked the handler itself is not a boundary -- the same reasoning NewRouter
// is built for in main_test.go.
//
// There are no console handlers yet, so the routes below are stubs that exist
// only to be reached or not reached. What is under test is the chain in front
// of them.

// consoleTestRouter mounts every gate in the shapes a console route will use.
func consoleTestRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.RequestIDMiddleware())

	// Echoes what the middleware put in the context, so a test can assert the
	// identity a handler would actually see.
	echo := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id":        c.GetInt64(middleware.ContextUserID),
			"user_public_id": c.GetString(middleware.ContextUserPublicID),
			"email":          c.GetString(middleware.ContextUserEmail),
			"role":           c.GetString(middleware.ContextRole),
			"session_id":     c.GetInt64(middleware.ContextSessionID),
			"company_id":     c.GetInt64(middleware.ContextCompanyID),
			"auth_actor":     c.GetString(middleware.ContextAuthActor),
			"site_id":        c.GetInt64("site_id"),
			"site_name":      c.GetString("site_name"),
		})
	}

	console := r.Group("/api/v1/console")
	console.Use(middleware.OperatorAuthMiddleware())
	{
		console.GET("/whoami", echo)

		guarded := console.Group("")
		guarded.Use(middleware.RequireCSRF())
		guarded.POST("/write", echo)
		guarded.PUT("/write", echo)

		for _, role := range []string{
			models.RoleViewer, models.RoleManager, models.RoleAdmin, models.RoleOwner,
		} {
			minimum := role
			console.GET("/gate/"+strings.ToLower(minimum),
				middleware.RequireRole(minimum), echo)
		}

		console.GET("/sites/:site_id/thing",
			middleware.RequireSiteGrant("site_id"), echo)
	}

	// RequireCSRF with no operator authentication in front of it. Not a shape
	// any real route uses; mounted to prove the gate fails closed if one ever
	// were built that way.
	r.POST("/unguarded", middleware.RequireCSRF(), echo)

	return r
}

// consoleRequest issues a request with an optional session cookie, CSRF token
// and site API key.
func consoleRequest(t *testing.T, router *gin.Engine, method, path string,
	token, csrf, siteKey string) (int, map[string]any) {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: token})
	}
	if csrf != "" {
		req.Header.Set(middleware.CSRFHeader, csrf)
	}
	if siteKey != "" {
		req.Header.Set("X-API-Key", siteKey)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &body)
	}
	return w.Code, body
}

// ---------------------------------------------------------------------------
// Session resolution
// ---------------------------------------------------------------------------

func TestConsoleSessionResolution(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	router := consoleTestRouter()
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	// A valid session reaches the handler with the operator in context.
	user := mustCreateOperator(t, one, "valid@example.com", models.RoleManager)
	creds, identity := mustOpenSession(t, user.ID)

	code, body := consoleRequest(t, router, http.MethodGet, "/api/v1/console/whoami",
		creds.Token, "", "")
	if code != http.StatusOK {
		t.Fatalf("valid session = %d, want 200 (%v)", code, body)
	}
	if int64(body["user_id"].(float64)) != user.ID {
		t.Errorf("context user_id = %v, want %d", body["user_id"], user.ID)
	}
	if int64(body["company_id"].(float64)) != one {
		t.Errorf("context company_id = %v, want %d", body["company_id"], one)
	}
	if int64(body["session_id"].(float64)) != identity.SessionID {
		t.Errorf("context session_id = %v, want %d", body["session_id"], identity.SessionID)
	}
	if body["role"] != models.RoleManager || body["email"] != "valid@example.com" {
		t.Errorf("context role/email = %v/%v", body["role"], body["email"])
	}
	if body["user_public_id"] != user.PublicID {
		t.Errorf("context user_public_id = %v, want %s", body["user_public_id"], user.PublicID)
	}
	if body["auth_actor"] != middleware.ActorOperator {
		t.Errorf("auth_actor = %v, want %q", body["auth_actor"], middleware.ActorOperator)
	}
	// site_id belongs to the site-key credential, not to an operator. A handler
	// that needs a site must be told which one in the path.
	if body["site_id"].(float64) != 0 || body["site_name"] != "" {
		t.Errorf("operator context carries a site: %v / %v", body["site_id"], body["site_name"])
	}

	// Every refusal looks the same from outside.
	rejections := []struct {
		name    string
		email   string
		company int64
		break_  func(t *testing.T, userID, sessionID int64)
		token   func(valid string) string
	}{
		{name: "missing session", token: func(string) string { return "" }},
		{name: "invalid session", token: func(string) string {
			return "ats_" + strings.Repeat("0", 64)
		}},
		{name: "malformed session", token: func(string) string { return "garbage" }},
		{
			name: "expired session", email: "expired@example.com", company: one,
			break_: func(t *testing.T, _, sessionID int64) {
				mustExec(t, `UPDATE user_sessions
				                SET idle_expires_at = CURRENT_TIMESTAMP - interval '1 second'
				              WHERE id = $1`, sessionID)
			},
		},
		{
			name: "revoked session", email: "revoked-mw@example.com", company: one,
			break_: func(t *testing.T, _, sessionID int64) {
				mustExec(t, `UPDATE user_sessions SET revoked_at = CURRENT_TIMESTAMP
				              WHERE id = $1`, sessionID)
			},
		},
		{
			name: "disabled operator", email: "disabled-mw@example.com", company: one,
			break_: func(t *testing.T, userID, _ int64) {
				mustExec(t, `UPDATE users SET active = FALSE WHERE id = $1`, userID)
			},
		},
		{
			// Company two, so switching it off does not take the other cases
			// down with it.
			name: "disabled company", email: "companyoff-mw@example.com", company: two,
			break_: func(t *testing.T, userID, _ int64) {
				mustExec(t, `UPDATE companies SET active = FALSE
				              WHERE id = (SELECT company_id FROM users WHERE id = $1)`, userID)
			},
		},
	}

	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			token := ""
			if tc.email != "" {
				u := mustCreateOperator(t, tc.company, tc.email, models.RoleViewer)
				creds, identity := mustOpenSession(t, u.ID)
				tc.break_(t, u.ID, identity.SessionID)
				token = creds.Token
			}
			if tc.token != nil {
				token = tc.token(token)
			}

			code, body := consoleRequest(t, router, http.MethodGet,
				"/api/v1/console/whoami", token, "", "")
			if code != http.StatusUnauthorized {
				t.Fatalf("%s = %d, want 401 (%v)", tc.name, code, body)
			}
			if _, hasError := body["error"]; !hasError {
				t.Errorf("%s produced no error message", tc.name)
			}
			// The refusal must not say which of the reasons applied.
			if message, _ := body["error"].(string); strings.Contains(strings.ToLower(message), "disabled") ||
				strings.Contains(strings.ToLower(message), "revoked") {
				t.Errorf("%s discloses why it failed: %q", tc.name, message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The two credential classes must not cross
// ---------------------------------------------------------------------------

func TestSiteAPIKeyIsNotBrowserAuthentication(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	router := consoleTestRouter()
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "crossed@example.com", models.RoleOwner)
	creds, _ := mustOpenSession(t, user.ID)

	// A site API key is the provisioning secret. It authenticates nothing in the
	// console, with or without a serial, and cannot substitute for a session.
	for _, path := range []string{"/api/v1/console/whoami", "/api/v1/console/gate/viewer"} {
		code, body := consoleRequest(t, router, http.MethodGet, path, "", "", env.siteAKey)
		if code != http.StatusUnauthorized {
			t.Errorf("site key on %s = %d, want 401 (%v)", path, code, body)
		}
	}

	// And a session cookie authenticates nothing on the site-key or device APIs.
	// This is not hypothetical: the cookie is Path=/, so a browser really does
	// send it to these routes.
	deviceRoutes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/members"},
		{http.MethodGet, "/api/v1/devices"},
		{http.MethodPost, "/api/v1/devices/register"},
		{http.MethodPost, "/api/v1/devices/heartbeat"},
		{http.MethodGet, "/api/v1/devices/jobs"},
	}
	for _, route := range deviceRoutes {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: creds.Token})

		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("session cookie on %s %s = %d, want 401 (%s)",
				route.method, route.path, w.Code, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func TestConsoleCSRFProtection(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	router := consoleTestRouter()
	one := operatorCompanyID(t, "one")

	user := mustCreateOperator(t, one, "csrf@example.com", models.RoleAdmin)
	creds, _ := mustOpenSession(t, user.ID)

	// A second session, whose token is valid but belongs to somebody else's
	// session -- the case a per-session token exists to catch.
	other := mustCreateOperator(t, one, "csrf-other@example.com", models.RoleAdmin)
	otherCreds, _ := mustOpenSession(t, other.ID)

	// Safe methods are exempt: they are not supposed to change anything.
	if code, body := consoleRequest(t, router, http.MethodGet,
		"/api/v1/console/whoami", creds.Token, "", ""); code != http.StatusOK {
		t.Errorf("GET without a CSRF token = %d, want 200 (%v)", code, body)
	}

	cases := []struct {
		name, method, csrf string
		want               int
		wantMessage        string
	}{
		{"missing token", http.MethodPost, "", http.StatusForbidden, "CSRF token required"},
		{"wrong token", http.MethodPost, "not-the-token", http.StatusForbidden, "Invalid CSRF token"},
		{"another session's token", http.MethodPost, otherCreds.CSRFToken,
			http.StatusForbidden, "Invalid CSRF token"},
		{"the session token itself", http.MethodPost, creds.Token,
			http.StatusForbidden, "Invalid CSRF token"},
		{"valid token", http.MethodPost, creds.CSRFToken, http.StatusOK, ""},
		{"valid token on PUT", http.MethodPut, creds.CSRFToken, http.StatusOK, ""},
		{"missing token on PUT", http.MethodPut, "", http.StatusForbidden, "CSRF token required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := consoleRequest(t, router, tc.method,
				"/api/v1/console/write", creds.Token, tc.csrf, "")
			if code != tc.want {
				t.Fatalf("%s = %d, want %d (%v)", tc.name, code, tc.want, body)
			}
			if tc.wantMessage != "" && body["error"] != tc.wantMessage {
				t.Errorf("%s error = %v, want %q", tc.name, body["error"], tc.wantMessage)
			}
		})
	}

	// A valid CSRF token cannot stand in for a session.
	if code, _ := consoleRequest(t, router, http.MethodPost,
		"/api/v1/console/write", "", creds.CSRFToken, ""); code != http.StatusUnauthorized {
		t.Errorf("CSRF token without a session = %d, want 401", code)
	}

	// The gate fails closed when there is no authenticated operator to compare
	// against, rather than treating "no session" as "no CSRF needed".
	if code, _ := consoleRequest(t, router, http.MethodPost,
		"/unguarded", "", creds.CSRFToken, ""); code != http.StatusUnauthorized {
		t.Errorf("RequireCSRF without authentication = %d, want 401", code)
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

func TestConsoleRoleBoundaries(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	router := consoleTestRouter()
	one := operatorCompanyID(t, "one")

	roles := []string{models.RoleViewer, models.RoleManager, models.RoleAdmin, models.RoleOwner}
	rank := map[string]int{
		models.RoleViewer: 1, models.RoleManager: 2, models.RoleAdmin: 3, models.RoleOwner: 4,
	}

	tokens := map[string]string{}
	for _, role := range roles {
		user := mustCreateOperator(t, one, strings.ToLower(role)+"@example.com", role)
		creds, _ := mustOpenSession(t, user.ID)
		tokens[role] = creds.Token
	}

	// Every actor against every gate: allowed exactly when the actor's role
	// ranks at or above the gate's minimum.
	for _, actor := range roles {
		for _, gate := range roles {
			name := actor + "_at_" + gate + "_gate"
			t.Run(name, func(t *testing.T) {
				want := http.StatusForbidden
				if rank[actor] >= rank[gate] {
					want = http.StatusOK
				}

				code, body := consoleRequest(t, router, http.MethodGet,
					"/api/v1/console/gate/"+strings.ToLower(gate), tokens[actor], "", "")
				if code != want {
					t.Errorf("%s reaching the %s gate = %d, want %d (%v)",
						actor, gate, code, want, body)
				}
				if want == http.StatusForbidden && body["error"] != "Insufficient permissions" {
					t.Errorf("refusal message = %v", body["error"])
				}
			})
		}
	}

	// Unauthenticated requests are 401 at a role gate, not 403: the caller has
	// not failed an authorization check, it has not authenticated at all.
	if code, _ := consoleRequest(t, router, http.MethodGet,
		"/api/v1/console/gate/viewer", "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("role gate without a session = %d, want 401", code)
	}

	// The ordering helper itself, including the values that must fail closed.
	unit := []struct {
		role, minimum string
		want          bool
	}{
		{models.RoleOwner, models.RoleViewer, true},
		{models.RoleViewer, models.RoleViewer, true},
		{models.RoleViewer, models.RoleManager, false},
		{models.RoleAdmin, models.RoleOwner, false},
		{"", models.RoleViewer, false},
		{"SUPERUSER", models.RoleViewer, false},
		{models.RoleOwner, "SUPERUSER", false},
		{models.RoleOwner, "", false},
	}
	for _, tc := range unit {
		if got := middleware.RoleAtLeast(tc.role, tc.minimum); got != tc.want {
			t.Errorf("RoleAtLeast(%q, %q) = %v, want %v", tc.role, tc.minimum, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Site grants
// ---------------------------------------------------------------------------

func TestConsoleSiteGrantAuthorization(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	router := consoleTestRouter()
	one := operatorCompanyID(t, "one")

	siteA := operatorSitePublicID(t, "Site A") // company one
	siteB := operatorSitePublicID(t, "Site B") // company one
	siteC := operatorSitePublicID(t, "Site C") // company TWO

	sitePath := func(publicID string) string {
		return "/api/v1/console/sites/" + publicID + "/thing"
	}

	// A scoped MANAGER: granted Site A only.
	scoped := mustCreateOperator(t, one, "scoped@example.com", models.RoleManager)
	if err := database.ReplaceSiteGrants(one, scoped.ID, []string{siteA}); err != nil {
		t.Fatalf("granting site A: %v", err)
	}
	scopedCreds, _ := mustOpenSession(t, scoped.ID)

	code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteA), scopedCreds.Token, "", "")
	if code != http.StatusOK {
		t.Fatalf("granted site = %d, want 200 (%v)", code, body)
	}
	if body["site_name"] != "Site A" || body["site_id"].(float64) == 0 {
		t.Errorf("the resolved site was not put in the context: %v / %v",
			body["site_id"], body["site_name"])
	}

	// Same company, no grant: it exists and they may know it does, so 403.
	if code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteB),
		scopedCreds.Token, "", ""); code != http.StatusForbidden {
		t.Errorf("ungranted site in the same company = %d, want 403 (%v)", code, body)
	}

	// Another company: 404, never 403. A 403 would confirm the id exists
	// somewhere, which is the disclosure every cross-tenant read avoids.
	if code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteC),
		scopedCreds.Token, "", ""); code != http.StatusNotFound {
		t.Errorf("cross-company site = %d, want 404 (%v)", code, body)
	}

	// An unscoped operator reaches every site in their own company: an empty
	// grant set is the documented default, not a denial.
	unscoped := mustCreateOperator(t, one, "unscoped@example.com", models.RoleViewer)
	unscopedCreds, _ := mustOpenSession(t, unscoped.ID)
	for _, site := range []string{siteA, siteB} {
		if code, body := consoleRequest(t, router, http.MethodGet, sitePath(site),
			unscopedCreds.Token, "", ""); code != http.StatusOK {
			t.Errorf("unscoped operator on their own company's site = %d, want 200 (%v)", code, body)
		}
	}
	// But still not into another company.
	if code, _ := consoleRequest(t, router, http.MethodGet, sitePath(siteC),
		unscopedCreds.Token, "", ""); code != http.StatusNotFound {
		t.Error("an unscoped operator reached another company's site")
	}

	// ADMIN and OWNER bypass grants inside their company -- and are still
	// refused another company's site. This is the case that matters most: the
	// most privileged role must not be the one that crosses the tenant line.
	for _, role := range []string{models.RoleAdmin, models.RoleOwner} {
		user := mustCreateOperator(t, one, "bypass-"+strings.ToLower(role)+"@example.com", role)
		if err := database.ReplaceSiteGrants(one, user.ID, []string{siteA}); err != nil {
			t.Fatalf("granting site A to %s: %v", role, err)
		}
		creds, _ := mustOpenSession(t, user.ID)

		if code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteB),
			creds.Token, "", ""); code != http.StatusOK {
			t.Errorf("%s on an ungranted site in their company = %d, want 200 (%v)",
				role, code, body)
		}
		if code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteC),
			creds.Token, "", ""); code != http.StatusNotFound {
			t.Errorf("%s reached another company's site: %d (%v)", role, code, body)
		}
	}

	// A malformed id is not found, not a 500 from a uuid syntax error.
	if code, _ := consoleRequest(t, router, http.MethodGet, sitePath("not-a-uuid"),
		scopedCreds.Token, "", ""); code != http.StatusNotFound {
		t.Error("a malformed site id was not reported as not found")
	}

	// A retired site is gone for everyone, including the operator granted it.
	mustExec(t, `UPDATE sites SET deleted_at = CURRENT_TIMESTAMP WHERE site_name = 'Site A'`)
	if code, _ := consoleRequest(t, router, http.MethodGet, sitePath(siteA),
		scopedCreds.Token, "", ""); code != http.StatusNotFound {
		t.Error("a soft-deleted site is still reachable")
	}

	// And retiring it must not WIDEN the scoped operator's reach. Their only
	// grant now points at a dead site; they must stay scoped to nothing rather
	// than fall back to "no grants means every site".
	if code, body := consoleRequest(t, router, http.MethodGet, sitePath(siteB),
		scopedCreds.Token, "", ""); code != http.StatusForbidden {
		t.Errorf("retiring an operator's only granted site widened their access: %d (%v)",
			code, body)
	}

	// A site gate without a session is 401.
	if code, _ := consoleRequest(t, router, http.MethodGet, sitePath(siteB), "", "", ""); code != http.StatusUnauthorized {
		t.Errorf("site gate without a session = %d, want 401", code)
	}
}
