package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// Public API rate limiting -- API_SPEC.md section 18, "Rate limits". Through
// the real router, with credentials issued through the console.

// rateHeaders reads the four RateLimit fields, failing if any is missing.
func rateHeaders(t *testing.T, h http.Header) (limit, remaining, reset int, policy string) {
	t.Helper()
	get := func(name string) int {
		v := h.Get(name)
		if v == "" {
			t.Fatalf("%s header missing (headers: %v)", name, h)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("%s = %q, not an integer", name, v)
		}
		return n
	}
	policy = h.Get("RateLimit-Policy")
	if policy == "" {
		t.Fatal("RateLimit-Policy header missing")
	}
	return get("RateLimit-Limit"), get("RateLimit-Remaining"), get("RateLimit-Reset"), policy
}

func expect429(t *testing.T, status int, headers http.Header, body map[string]any) {
	t.Helper()
	if code := publicError(t, status, headers, body, http.StatusTooManyRequests); code != models.CodeRateLimitExceeded {
		t.Fatalf("code %s, want rate_limit_exceeded", code)
	}
	ra, err := strconv.Atoi(headers.Get("Retry-After"))
	if err != nil || ra < 1 {
		t.Errorf("Retry-After = %q, want an integer >= 1", headers.Get("Retry-After"))
	}
	if headers.Get("WWW-Authenticate") != "" {
		t.Error("a 429 is not an authentication challenge")
	}
}

func credentialRowID(t *testing.T, secret string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(`SELECT id FROM api_credentials WHERE key_prefix = $1`, secret[:17]).Scan(&id); err != nil {
		t.Fatalf("credential row: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Credential bucket: burst, headers, refill
// ---------------------------------------------------------------------------

func TestPublicReadsAreLimitedPerCredentialWithHeaders(t *testing.T) {
	t.Setenv("PUBLIC_READ_RATE_BURST", "5")
	// One token a second: the six requests below must all land inside the
	// refill of a single token for the sixth to be refused, and a full second
	// is a margin a loaded CI runner meets; the 200 ms that 300/min would
	// allow is not.
	t.Setenv("PUBLIC_READ_RATE_LIMIT_PER_MINUTE", "60")
	env := newTestEnv(t)
	secret := publicCredential(t, env, "one", "rl-cred@example.com", `{"name":"rl","scopes":["sites:read"]}`)

	for i := 1; i <= 5; i++ {
		status, headers, _, raw := publicGet(t, env, secret, "/api/public/v1/sites")
		if status != 200 {
			t.Fatalf("request %d = %d %s", i, status, raw)
		}
		limit, remaining, reset, policy := rateHeaders(t, headers)
		if limit != 5 || remaining != 5-i || policy != "60;w=60" {
			t.Errorf("request %d: Limit=%d Remaining=%d Policy=%q, want 5/%d/60;w=60", i, limit, remaining, policy, 5-i)
		}
		// Seconds to a full bucket: i tokens spent at one a second, less
		// whatever has refilled since the first request.
		if reset < 0 || reset > i {
			t.Errorf("request %d: Reset=%d, want the seconds to a full bucket (<= %d at 1/s)", i, reset, i)
		}
	}

	// The sixth is refused with the envelope, Retry-After, and Remaining 0.
	status, headers, body, _ := publicGet(t, env, secret, "/api/public/v1/sites")
	expect429(t, status, headers, body)
	if _, remaining, _, _ := rateHeaders(t, headers); remaining != 0 {
		t.Errorf("Remaining on a 429 = %d, want 0", remaining)
	}

	// Continuous refill: at one a second, a token is back within 1.2 s.
	time.Sleep(1200 * time.Millisecond)
	if status, _, _, raw := publicGet(t, env, secret, "/api/public/v1/sites"); status != 200 {
		t.Errorf("after refill = %d %s", status, raw)
	}
}

// Section 18: an authenticated request spends its token "whatever the
// response -- a 403, 404 or 400 did the same work as a 200". The spend happens
// before the handler runs, so a refactor that moved it after would pass every
// other test here and silently hand out free refusals; this pins it.
//
// Refill is one token a MINUTE, so the balance cannot drift by a whole token
// inside the test and the Remaining values below are exact without a sleep.
func TestAuthenticatedRefusalsConsumeReadQuota(t *testing.T) {
	t.Setenv("PUBLIC_READ_RATE_BURST", "10")
	t.Setenv("PUBLIC_READ_RATE_LIMIT_PER_MINUTE", "1")
	env := newTestEnv(t)
	sitesOnly := publicCredential(t, env, "one", "rl-refusals@example.com", `{"name":"refusals","scopes":["sites:read"]}`)

	for i, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/public/v1/members", http.StatusForbidden, models.CodeInsufficientScope},  // scope refused at the edge
		{"/api/public/v1/sites/NOPE", http.StatusNotFound, models.CodeResourceNotFound}, // lookup missed
		{"/api/public/v1/sites?bogus=1", http.StatusBadRequest, models.CodeUnknownParameter},
		{"/api/public/v1/sites", http.StatusOK, ""},
	} {
		status, headers, body, raw := publicGet(t, env, sitesOnly, tc.path)
		if tc.code != "" {
			if code := publicError(t, status, headers, body, tc.status); code != tc.code {
				t.Fatalf("%s: code %s, want %s", tc.path, code, tc.code)
			}
		} else if status != tc.status {
			t.Fatalf("%s = %d %s", tc.path, status, raw)
		}
		limit, remaining, _, _ := rateHeaders(t, headers)
		if limit != 10 || remaining != 10-(i+1) {
			t.Errorf("%s (%d): Limit=%d Remaining=%d, want 10/%d -- the refusal must have spent a token",
				tc.path, status, limit, remaining, 10-(i+1))
		}
	}
}

// ---------------------------------------------------------------------------
// Company aggregate across two credentials
// ---------------------------------------------------------------------------

func TestPublicReadsAreCappedPerCompanyAcrossCredentials(t *testing.T) {
	t.Setenv("PUBLIC_COMPANY_RATE_BURST", "4")
	t.Setenv("PUBLIC_COMPANY_RATE_LIMIT_PER_MINUTE", "60")
	env := newTestEnv(t)
	first := publicCredential(t, env, "one", "rl-a@example.com", `{"name":"a","scopes":["sites:read"]}`)
	second := publicCredential(t, env, "one", "rl-b@example.com", `{"name":"b","scopes":["sites:read"]}`)
	other := publicCredential(t, env, "two", "rl-c@example.com", `{"name":"c","scopes":["sites:read"]}`)

	// Four requests split across the two keys spend the company's burst...
	for i, secret := range []string{first, second, first, second} {
		if status, _, _, raw := publicGet(t, env, secret, "/api/public/v1/sites"); status != 200 {
			t.Fatalf("request %d = %d %s", i+1, status, raw)
		}
	}
	// ...and the fifth is refused although the credential's own bucket
	// (default burst 60) is nowhere near empty -- the headers say so.
	status, headers, body, _ := publicGet(t, env, first, "/api/public/v1/sites")
	expect429(t, status, headers, body)
	if _, remaining, _, _ := rateHeaders(t, headers); remaining < 50 {
		t.Errorf("credential Remaining on a company refusal = %d; the credential bucket should be nearly full", remaining)
	}
	// Another company is unaffected.
	if status, _, _, raw := publicGet(t, env, other, "/api/public/v1/sites"); status != 200 {
		t.Errorf("company two = %d %s", status, raw)
	}
}

// ---------------------------------------------------------------------------
// Authentication failures are charged to their own bucket only
// ---------------------------------------------------------------------------

func TestAuthFailuresDoNotConsumeACredentialsReadQuota(t *testing.T) {
	t.Setenv("PUBLIC_AUTH_FAILURE_RATE_BURST", "3")
	t.Setenv("PUBLIC_AUTH_FAILURE_RATE_LIMIT_PER_MINUTE", "60")
	env := newTestEnv(t)
	secret := publicCredential(t, env, "one", "rl-af@example.com", `{"name":"af","scopes":["sites:read"]}`)

	// Three failures of assorted kinds are 401s with the challenge...
	for i, auth := range []string{"", "Bearer nope", "Bearer atp_live_" + strings.Repeat("1", 64)} {
		req := httptest.NewRequest(http.MethodGet, "/api/public/v1/sites", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("failure %d = %d (WWW-Authenticate %q)", i+1, w.Code, w.Header().Get("WWW-Authenticate"))
		}
		if w.Header().Get("RateLimit-Limit") != "" {
			t.Error("a 401 must not carry a credential's RateLimit headers")
		}
	}
	// ...the fourth is a 429 in place of the 401: the caller learns nothing
	// about the key it presented, only that it must slow down.
	status, headers, body, _ := publicGet(t, env, "", "/api/public/v1/sites")
	expect429(t, status, headers, body)

	// The valid key's allowance is untouched by any of that.
	status, headers, _, raw := publicGet(t, env, secret, "/api/public/v1/sites")
	if status != 200 {
		t.Fatalf("valid key after the spray = %d %s", status, raw)
	}
	if limit, remaining, _, _ := rateHeaders(t, headers); remaining != limit-1 {
		t.Errorf("valid key Remaining = %d, want %d (the spray must not have charged it)", remaining, limit-1)
	}
}

// ---------------------------------------------------------------------------
// Fail closed
// ---------------------------------------------------------------------------

func TestPublicRoutesFailClosedWhenTheRateStoreFails(t *testing.T) {
	env := newTestEnv(t)
	secret := publicCredential(t, env, "one", "rl-fc@example.com", `{"name":"fc","scopes":["sites:read"]}`)

	middleware.UseSharedRateStore(failingStore{})
	t.Cleanup(func() { middleware.UseSharedRateStore(database.NewPostgresRateStore(database.DB)) })

	// Authenticated: the read bucket cannot be consulted -> 503 envelope.
	status, headers, body, _ := publicGet(t, env, secret, "/api/public/v1/sites")
	if code := publicError(t, status, headers, body, http.StatusServiceUnavailable); code != models.CodeServiceUnavailable {
		t.Errorf("code %s, want service_unavailable", code)
	}
	if headers.Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
	// Unauthenticated: the auth-failure bucket cannot be consulted -> 503, and
	// NOT a 401 -- the caller must not go looking for a credential problem.
	status, headers, body, _ = publicGet(t, env, "", "/api/public/v1/sites")
	if code := publicError(t, status, headers, body, http.StatusServiceUnavailable); code != models.CodeServiceUnavailable {
		t.Errorf("unauthenticated with a failing store: %s, want service_unavailable", code)
	}
}

// ---------------------------------------------------------------------------
// Shared across instances
// ---------------------------------------------------------------------------

// Two routers stand in for two instances: each has its own in-process refusal
// cache, so a refusal seen by the second can only have come from the shared
// store the first drained.
func TestPublicAllowanceIsSharedAcrossRouterInstances(t *testing.T) {
	t.Setenv("PUBLIC_READ_RATE_BURST", "3")
	t.Setenv("PUBLIC_READ_RATE_LIMIT_PER_MINUTE", "60") // one a second: see the headers test
	env := newTestEnv(t)
	secret := publicCredential(t, env, "one", "rl-two@example.com", `{"name":"two","scopes":["sites:read"]}`)
	second := &testEnv{t: t, router: NewRouter(), siteAKey: env.siteAKey, siteBKey: env.siteBKey, siteCKey: env.siteCKey}

	for i := 1; i <= 3; i++ {
		if status, _, _, raw := publicGet(t, env, secret, "/api/public/v1/sites"); status != 200 {
			t.Fatalf("instance one, request %d = %d %s", i, status, raw)
		}
	}
	status, headers, body, _ := publicGet(t, second, secret, "/api/public/v1/sites")
	expect429(t, status, headers, body)

	// And the store itself, asked twice through independent handles, agrees
	// (the P1 proof, restated for the public class).
	a := database.NewPostgresRateStore(database.DB)
	b := database.NewPostgresRateStore(database.DB)
	ctx := context.Background()
	if d, err := a.Allow(ctx, middleware.RateSubjectCredential, "shared-proof", middleware.RateClassRead, 1, 0); err != nil || !d.Allowed {
		t.Fatalf("first store: %v %v", d, err)
	}
	if d, err := b.Allow(ctx, middleware.RateSubjectCredential, "shared-proof", middleware.RateClassRead, 1, 0); err != nil || d.Allowed {
		t.Fatalf("second store did not see the first's spend: %v %v", d, err)
	}
}

// ---------------------------------------------------------------------------
// Usage accounting
// ---------------------------------------------------------------------------

func TestPublicReadsProduceUsageDataAfterFlush(t *testing.T) {
	t.Setenv("PUBLIC_READ_RATE_BURST", "2")
	t.Setenv("PUBLIC_READ_RATE_LIMIT_PER_MINUTE", "60") // one a second: see the headers test
	env := newTestEnv(t)
	// The usage buffer is process-wide and the fixture restarts identities, so
	// an earlier test's notes could be attributed to this test's credential.
	// Drain first; the flush tolerates rows that no longer exist.
	if _, err := database.FlushAPICredentialUse(context.Background()); err != nil {
		t.Fatalf("draining stale usage: %v", err)
	}
	cheapBcrypt(t)
	_, token, csrf := consoleOperatorSession(t, env.router, operatorCompanyID(t, "one"), "rl-usage@example.com", models.RoleAdmin)
	issued := issueCredential(t, env, token, csrf, `{"name":"usage","scopes":["sites:read"]}`)
	secret := secretOf(t, issued)

	// Two served, one refused.
	for i := 1; i <= 3; i++ {
		publicGet(t, env, secret, "/api/public/v1/sites")
	}
	if _, err := database.FlushAPICredentialUse(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	id := credentialRowID(t, secret)
	var requests, refusals int
	if err := database.DB.QueryRow(`SELECT requests, refusals FROM api_usage_daily WHERE credential_id = $1 AND class = 'read'`, id).
		Scan(&requests, &refusals); err != nil {
		t.Fatalf("usage row: %v", err)
	}
	// P1 semantics: `requests` counts what was served, `refusals` what the
	// limiter turned away; the two are disjoint.
	if requests != 2 || refusals != 1 {
		t.Errorf("usage requests=%d refusals=%d, want 2 served and 1 refused", requests, refusals)
	}
	if queryString(t, fmt.Sprintf(`SELECT coalesce(last_used_ip, '') FROM api_credentials WHERE id = %d`, id)) == "" {
		t.Error("last_used_ip was not recorded")
	}

	// And the console usage endpoint reports it.
	code, usage := consoleCall(t, env.router, "GET", credentialsPath+"/"+issued["id"].(string)+"/usage?days=7", "", token, "")
	if code != 200 {
		t.Fatalf("usage endpoint = %d", code)
	}
	days, _ := usage["days"].([]any)
	if len(days) == 0 {
		t.Fatalf("usage endpoint reports no days: %v", usage)
	}
	day := days[0].(map[string]any)
	if day["class"] != "read" || day["requests"] != float64(2) || day["refusals"] != float64(1) {
		t.Errorf("usage day = %v", day)
	}
}
