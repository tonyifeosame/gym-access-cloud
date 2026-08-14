package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/maintenance"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// Production configuration: CORS for the dashboard, and the session purge.
//
// The deployment these are written against:
//
//	frontend  https://app.accesslink.store
//	API       https://api.accesslink.store
//
// Same registrable domain, so the SameSite=Lax session cookie is sent; CORS is
// what allows the cross-origin read on top of that.

const (
	prodDashboardOrigin = "https://app.accesslink.store"
	prodAPIOrigin       = "https://api.accesslink.store"
)

// corsResponse issues a request with an Origin and returns the CORS headers.
func corsResponse(t *testing.T, method, path, origin string) http.Header {
	t.Helper()

	router := NewRouter()
	req := httptest.NewRequest(method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type,x-csrf-token")
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Result().Header
}

func TestCORSAllowsTheProductionDashboardWithCredentials(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", prodDashboardOrigin)

	// The preflight the browser sends before a console mutation.
	headers := corsResponse(t, http.MethodOptions, "/api/v1/console/people", prodDashboardOrigin)

	if got := headers.Get("Access-Control-Allow-Origin"); got != prodDashboardOrigin {
		t.Errorf("Allow-Origin = %q, want the dashboard's exact origin", got)
	}
	// Without this the browser will not send the session cookie at all.
	if got := headers.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q, want true", got)
	}
	// A wildcard with credentials is rejected outright by browsers, so the
	// exact origin must be echoed rather than "*".
	if headers.Get("Access-Control-Allow-Origin") == "*" {
		t.Error("the credentialed response used a wildcard origin")
	}
	if !strings.Contains(headers.Get("Vary"), "Origin") {
		t.Error("Vary: Origin is missing, so a cache could serve one origin's response to another")
	}

	// The dashboard's own headers must be permitted, or the preflight fails.
	allowHeaders := strings.ToLower(headers.Get("Access-Control-Allow-Headers"))
	for _, header := range []string{"content-type", "x-csrf-token"} {
		if !strings.Contains(allowHeaders, header) {
			t.Errorf("Allow-Headers omits %q: %q", header, allowHeaders)
		}
	}
	allowMethods := headers.Get("Access-Control-Allow-Methods")
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		if !strings.Contains(allowMethods, method) {
			t.Errorf("Allow-Methods omits %s: %q", method, allowMethods)
		}
	}
	// So a cross-origin dashboard can quote a request id when reporting a fault.
	if got := headers.Get("Access-Control-Expose-Headers"); !strings.Contains(got, middleware.RequestIDHeader) {
		t.Errorf("Expose-Headers = %q, want it to include %s", got, middleware.RequestIDHeader)
	}
	if headers.Get("Access-Control-Max-Age") == "" {
		t.Error("no Access-Control-Max-Age, so every mutation re-preflights")
	}

	// The same headers apply to a real request, not just the preflight.
	headers = corsResponse(t, http.MethodGet, "/api/v1/console/company", prodDashboardOrigin)
	if headers.Get("Access-Control-Allow-Origin") != prodDashboardOrigin ||
		headers.Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("a non-preflight request did not carry credentialed CORS headers")
	}
}

func TestCORSRefusesArbitraryOriginsInProduction(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", prodDashboardOrigin)

	// Near-misses, each of which is a different origin to a browser.
	hostile := []string{
		"https://evil.example.com",
		"http://app.accesslink.store",           // wrong scheme
		"https://app.accesslink.store.evil.com", // suffix attack
		"https://accesslink.store",              // parent domain
		prodAPIOrigin,                           // the API itself is not the dashboard
		"null",
	}

	for _, origin := range hostile {
		t.Run(origin, func(t *testing.T) {
			headers := corsResponse(t, http.MethodGet, "/api/v1/console/company", origin)

			// The origin must not be echoed, and no wildcard may appear either:
			// a wildcard would still let any site READ responses.
			if got := headers.Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("Allow-Origin = %q for %s, want no header at all", got, origin)
			}
			if headers.Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("credentials were allowed for %s", origin)
			}
		})
	}

	// Several origins may be listed, and each is matched exactly.
	t.Setenv("CORS_ALLOWED_ORIGINS", prodDashboardOrigin+",http://localhost:5173")
	for _, origin := range []string{prodDashboardOrigin, "http://localhost:5173"} {
		headers := corsResponse(t, http.MethodGet, "/api/v1/console/company", origin)
		if headers.Get("Access-Control-Allow-Origin") != origin {
			t.Errorf("%s was not allowed from a multi-origin list", origin)
		}
	}
	if headers := corsResponse(t, http.MethodGet, "/api/v1/console/company",
		"http://localhost:5174"); headers.Get("Access-Control-Allow-Origin") != "" {
		t.Error("an origin one port away was allowed")
	}
}

func TestCORSWithoutAnAllowlistNeverGrantsCredentials(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	headers := corsResponse(t, http.MethodGet, "/api/v1/console/company", prodDashboardOrigin)

	// The historical behaviour is preserved -- readable from anywhere -- but a
	// wildcard can never carry credentials, so a dashboard cannot sign in. The
	// server warns about this at startup rather than leaving it to be found in
	// a browser console.
	if got := headers.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Allow-Origin = %q with no allowlist, want *", got)
	}
	if got := headers.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Allow-Credentials = %q with a wildcard origin, want none", got)
	}
}

// TestSessionPurgeLeavesLiveSessionsAlone is the assertion that matters: the
// sweep runs unattended, so a mistake in its predicate would log the whole
// company out.
func TestSessionPurgeLeavesLiveSessionsAlone(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "purge-task@example.com", models.RoleOwner)

	live, liveIdentity := mustOpenSession(t, user.ID)
	idle, idleIdentity := mustOpenSession(t, user.ID)
	expired, expiredIdentity := mustOpenSession(t, user.ID)
	revoked, revokedIdentity := mustOpenSession(t, user.ID)

	// Idle-expired but within its absolute window: dead to authentication, but
	// deliberately NOT purged, because the row is what an incident review reads.
	mustExec(t, `UPDATE user_sessions SET idle_expires_at = CURRENT_TIMESTAMP - interval '1 hour'
	              WHERE id = $1`, idleIdentity.SessionID)
	// Past its hard cap, and older than any retention window.
	mustExec(t, `UPDATE user_sessions
	                SET issued_at = CURRENT_TIMESTAMP - interval '99 days',
	                    idle_expires_at = CURRENT_TIMESTAMP - interval '90 days',
	                    absolute_expires_at = CURRENT_TIMESTAMP - interval '90 days'
	              WHERE id = $1`, expiredIdentity.SessionID)
	mustExec(t, `UPDATE user_sessions SET revoked_at = CURRENT_TIMESTAMP - interval '90 days'
	              WHERE id = $1`, revokedIdentity.SessionID)

	// The task the scheduler runs, built from the real configuration.
	t.Setenv("SESSION_RETENTION_DAYS", "30")
	cfg := maintenance.LoadConfig()
	if cfg.SessionRetentionDays != 30 {
		t.Fatalf("SessionRetentionDays = %d, want 30", cfg.SessionRetentionDays)
	}

	var purge func() (string, error)
	for _, task := range cfg.Tasks() {
		if task.Name == "session_purge" {
			run := task.Run
			purge = func() (string, error) { return run(context.Background()) }
		}
	}
	if purge == nil {
		t.Fatal("the maintenance configuration registers no session_purge task")
	}

	message, err := purge()
	if err != nil {
		t.Fatalf("session purge: %v", err)
	}
	if !strings.Contains(message, "2") {
		t.Errorf("purge reported %q, want the two dead sessions", message)
	}

	// THE LIVE SESSION STILL AUTHENTICATES.
	identity, err := database.AuthenticateSession(live.Token)
	if err != nil {
		t.Fatalf("authenticating after the purge: %v", err)
	}
	if identity == nil || identity.SessionID != liveIdentity.SessionID {
		t.Fatal("the purge deleted a live session")
	}

	// The idle-expired row survives for the audit trail, even though it can no
	// longer authenticate.
	if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE id = $1`, idleIdentity.SessionID); n != 1 {
		t.Error("an idle-expired session was purged before its absolute expiry")
	}
	if got, _ := database.AuthenticateSession(idle.Token); got != nil {
		t.Error("an idle-expired session still authenticates")
	}

	// The dead ones are gone.
	for name, id := range map[string]int64{
		"absolute-expired": expiredIdentity.SessionID,
		"revoked":          revokedIdentity.SessionID,
	} {
		if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE id = $1`, id); n != 0 {
			t.Errorf("the %s session was not purged", name)
		}
	}
	_ = expired
	_ = revoked

	// Within the retention window nothing is removed at all.
	before := queryInt(t, `SELECT count(*) FROM user_sessions`)
	if _, err := database.PurgeExpiredSessionsContext(context.Background(), 365); err != nil {
		t.Fatalf("purge with a long retention: %v", err)
	}
	if after := queryInt(t, `SELECT count(*) FROM user_sessions`); after != before {
		t.Errorf("a long retention window still deleted rows: %d -> %d", before, after)
	}

	// And a retention of zero -- the most aggressive setting -- still cannot
	// reach a live session.
	if _, err := database.PurgeExpiredSessionsContext(context.Background(), 0); err != nil {
		t.Fatalf("purge with zero retention: %v", err)
	}
	if got, _ := database.AuthenticateSession(live.Token); got == nil {
		t.Error("a zero-day retention purge deleted the live session")
	}
}
