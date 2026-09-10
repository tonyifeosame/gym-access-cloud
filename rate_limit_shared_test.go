package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
)

// The shared rate-limit store (031), closing SEC-09 for the credential
// endpoints.
//
// THE PROPERTY UNDER TEST IS THAT THE ALLOWANCE IS SHARED. The in-process
// limiter was correct for one instance and multiplied by the instance count for
// more than one; what these tests prove is that two limiters which have never
// met draw on the same tokens.
//
// TWO INDEPENDENTLY CONSTRUCTED STORES, ONE DATABASE. That is the whole of the
// multi-instance property: the store holds no state of its own, so two structs
// in one process are indistinguishable from two processes as far as the
// arithmetic is concerned. A second operating-system process would prove nothing
// further and would need orchestration this harness has no pattern for.

func TestTwoIndependentStoresShareOneAllowance(t *testing.T) {
	newTestEnv(t) // truncates api_rate_buckets

	first := database.NewPostgresRateStore(database.DB)
	second := database.NewPostgresRateStore(database.DB)

	const capacity = 6
	const perSecond = 0.0001 // effectively no refill during the test

	ctx := context.Background()
	allowed := 0

	// Alternate between the two stores. If each held its own buckets, twelve
	// requests would all be permitted; sharing means six.
	for i := 0; i < capacity*2; i++ {
		store := first
		if i%2 == 1 {
			store = second
		}
		decision, err := store.Allow(ctx, "address", "198.51.100.1", "login", capacity, perSecond)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if decision.Allowed {
			allowed++
		}
	}

	if allowed != capacity {
		t.Errorf("two stores permitted %d requests against a capacity of %d; "+
			"the allowance is not shared", allowed, capacity)
	}

	// One row, not two: the subject is the same however many stores address it.
	rows := queryInt(t, `SELECT count(*) FROM api_rate_buckets
	                      WHERE subject_key = '198.51.100.1' AND class = 'login'`)
	if rows != 1 {
		t.Errorf("the two stores created %d bucket rows, want 1", rows)
	}
}

// CONTINUOUS REFILL, NOT A FIXED WINDOW. A fixed window lets a caller spend a
// full allowance at the end of one window and another at the start of the next,
// which is twice the intended rate at exactly the moment it matters. The
// in-process limiter already made this choice; the shared one must make the same
// one, or moving a limiter would change its behaviour.
func TestTheSharedStoreRefillsContinuously(t *testing.T) {
	newTestEnv(t)

	store := database.NewPostgresRateStore(database.DB)
	ctx := context.Background()

	const capacity = 2
	const perSecond = 20 // a token every 50ms

	for i := 0; i < capacity; i++ {
		if decision, err := store.Allow(ctx, "address", "198.51.100.2", "login",
			capacity, perSecond); err != nil || !decision.Allowed {
			t.Fatalf("spending the initial capacity: %v %v", decision, err)
		}
	}

	decision, err := store.Allow(ctx, "address", "198.51.100.2", "login", capacity, perSecond)
	if err != nil {
		t.Fatalf("exhausted request: %v", err)
	}
	if decision.Allowed {
		t.Fatal("the bucket did not run out")
	}
	// A truthful Retry-After, computed from the balance rather than guessed.
	if decision.RetryAfter <= 0 || decision.RetryAfter > time.Second {
		t.Errorf("retry-after = %v, want a short positive duration", decision.RetryAfter)
	}

	// Wait for a token to accrue. Nothing resets: the balance climbs.
	time.Sleep(150 * time.Millisecond)

	if decision, err := store.Allow(ctx, "address", "198.51.100.2", "login",
		capacity, perSecond); err != nil || !decision.Allowed {
		t.Errorf("after refilling, the request was still refused: %v %v", decision, err)
	}
}

func TestTheSharedStoreSeparatesSubjectsAndClasses(t *testing.T) {
	newTestEnv(t)

	store := database.NewPostgresRateStore(database.DB)
	ctx := context.Background()

	// Exhaust one address on one class.
	for i := 0; i < 3; i++ {
		if _, err := store.Allow(ctx, "address", "198.51.100.3", "login", 3, 0.0001); err != nil {
			t.Fatalf("spending: %v", err)
		}
	}
	if decision, _ := store.Allow(ctx, "address", "198.51.100.3", "login", 3, 0.0001); decision.Allowed {
		t.Fatal("the bucket did not run out")
	}

	// A different class for the same address is a different allowance. This is
	// what keeps an installer retrying a claim code from exhausting the budget
	// an operator needs to sign in.
	if decision, err := store.Allow(ctx, "address", "198.51.100.3", "claim",
		3, 0.0001); err != nil || !decision.Allowed {
		t.Errorf("a different class was refused: %v %v", decision, err)
	}

	// And a different address on the same class.
	if decision, err := store.Allow(ctx, "address", "198.51.100.4", "login",
		3, 0.0001); err != nil || !decision.Allowed {
		t.Errorf("a different address was refused: %v %v", decision, err)
	}
}

func TestIdleAddressBucketsArePruned(t *testing.T) {
	newTestEnv(t)

	store := database.NewPostgresRateStore(database.DB)
	ctx := context.Background()

	if _, err := store.Allow(ctx, "address", "198.51.100.5", "login", 5, 1); err != nil {
		t.Fatalf("spending: %v", err)
	}
	if _, err := store.Allow(ctx, "credential", "1", "read", 5, 1); err != nil {
		t.Fatalf("spending: %v", err)
	}

	mustExec(t, `UPDATE api_rate_buckets
	                SET last_refill = CURRENT_TIMESTAMP - interval '1 hour'`)

	pruned, err := database.PruneIdleRateBuckets(ctx, time.Minute)
	if err != nil {
		t.Fatalf("pruning: %v", err)
	}
	if pruned != 1 {
		t.Errorf("pruned %d buckets, want 1", pruned)
	}

	// CREDENTIAL AND COMPANY SUBJECTS ARE NOT SWEPT. There is one row per
	// credential per class, bounded by the tenant's own size; only address
	// subjects grow with traffic, and only those need bounding.
	remaining := queryInt(t, `SELECT count(*) FROM api_rate_buckets WHERE subject_type = 'credential'`)
	if remaining != 1 {
		t.Errorf("a credential bucket was swept (%d remain)", remaining)
	}
}

// ---------------------------------------------------------------------------
// The mounted limiters
// ---------------------------------------------------------------------------

// Login, claim, platform login and adopt move onto the shared store. Announce
// deliberately does not -- see the exception recorded in middleware/rate_limit.go
// -- and these two tests are what stop either half of that decision drifting.
func TestCredentialEndpointsUseTheSharedStore(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	// A failed login still spends a token, which is the point.
	body := `{"email":"nobody@example.com","password":"wrong-password-here"}`
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(httptest.NewRecorder(), req)

	if n := queryInt(t, `SELECT count(*) FROM api_rate_buckets WHERE class = 'login'`); n == 0 {
		t.Error("a login attempt left no bucket in the shared store")
	}
}

func TestClaimAndLoginDrawOnSeparateAllowances(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	body := `{"email":"nobody@example.com","password":"wrong-password-here"}`
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(httptest.NewRecorder(), req)

	claim := httptest.NewRequest("POST", "/api/v1/devices/claim",
		strings.NewReader(`{"serial_number":"AT-TEST","claim_code":"AAAABBBB"}`))
	claim.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(httptest.NewRecorder(), claim)

	// Separate CLASSES, which is what preserves the isolation the router already
	// had from separate in-process instances -- across instances rather than
	// only within one.
	classes := map[string]bool{}
	rows, err := database.DB.Query(`SELECT DISTINCT class FROM api_rate_buckets`)
	if err != nil {
		t.Fatalf("reading classes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		if err := rows.Scan(&class); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		classes[class] = true
	}

	if !classes["login"] || !classes["claim"] {
		t.Errorf("classes present = %v, want both login and claim", classes)
	}
}

// The announce path stays in process, by design. If it is ever moved, this test
// is the one that should be deleted deliberately rather than the one that starts
// failing mysteriously.
func TestAnnounceRemainsInProcess(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest("POST", "/api/v1/devices/announce",
		strings.NewReader(`{"serial_number":"AT-ANNOUNCE-1"}`))
	req.Header.Set("Content-Type", "application/json")
	env.router.ServeHTTP(httptest.NewRecorder(), req)

	n := queryInt(t, `SELECT count(*) FROM api_rate_buckets WHERE class LIKE 'announce%'`)
	if n != 0 {
		t.Errorf("the announce limiter wrote %d row(s) to the shared store; it is "+
			"meant to stay in process (see middleware/rate_limit.go)", n)
	}
}

// ---------------------------------------------------------------------------
// Fail closed
// ---------------------------------------------------------------------------

// failingStore reports an error for every decision, standing in for a database
// that cannot answer.
type failingStore struct{}

func (failingStore) Allow(context.Context, string, string, string, float64, float64) (database.RateDecision, error) {
	return database.RateDecision{}, errors.New("store unavailable")
}

// A LIMITER THAT CANNOT ANSWER MUST NOT BE READ AS "YES". The store is the
// database, and a database that cannot serve the limiter cannot serve the
// request behind it either -- so the refusal is a 503 with a Retry-After rather
// than a silent permit.
func TestTheLimiterFailsClosedWhenTheStoreFails(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	middleware.UseSharedRateStore(failingStore{})
	t.Cleanup(func() {
		middleware.UseSharedRateStore(database.NewPostgresRateStore(database.DB))
	})

	body := `{"email":"nobody@example.com","password":"wrong-password-here"}`
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("login with a failing rate store = %d, want 503", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("the refusal carries no Retry-After")
	}
	// It must not be reported as a credential problem: that would send an
	// operator looking for a password fault that does not exist.
	if strings.Contains(strings.ToLower(w.Body.String()), "password") {
		t.Errorf("the refusal mentions credentials: %s", w.Body.String())
	}
}

// A subject with no identity is permitted rather than sharing one bucket.
// Inventing a bucket for "unidentifiable" would put every such request in the
// same allowance, which an attacker could exhaust on a customer's behalf.
func TestAnEmptySubjectIsNotBucketed(t *testing.T) {
	newTestEnv(t)

	store := database.NewPostgresRateStore(database.DB)
	for i := 0; i < 5; i++ {
		decision, err := store.Allow(context.Background(), "address", "", "login", 1, 0.0001)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("request %d with no subject was refused", i)
		}
	}
	if n := queryInt(t, `SELECT count(*) FROM api_rate_buckets`); n != 0 {
		t.Errorf("an empty subject created %d bucket row(s)", n)
	}
}

// An over-long subject key is truncated rather than refused, so a caller cannot
// choose how much of the index it occupies.
func TestAnOverlongSubjectKeyIsBounded(t *testing.T) {
	newTestEnv(t)

	store := database.NewPostgresRateStore(database.DB)
	long := strings.Repeat("x", 200)

	if _, err := store.Allow(context.Background(), "address", long, "login", 5, 1); err != nil {
		t.Fatalf("spending with a long key: %v", err)
	}

	var stored string
	if err := database.DB.QueryRow(
		`SELECT subject_key FROM api_rate_buckets LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("reading the bucket: %v", err)
	}
	if len(stored) > 64 {
		t.Errorf("stored subject key is %d characters, want at most 64", len(stored))
	}
}

// The existing login lockout and per-address limit still behave as documented
// after the move. This is the regression the migration is most likely to break.
func TestLoginIsStillRateLimitedAfterTheMove(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	limit := middleware.LoginRateLimitPerMinute()
	refused := false

	for i := 0; i < limit+2; i++ {
		body := fmt.Sprintf(`{"email":"nobody%d@example.com","password":"wrong-password-here"}`, i)
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)

		if w.Code == http.StatusTooManyRequests {
			refused = true
			if w.Header().Get("Retry-After") == "" {
				t.Error("a 429 carried no Retry-After")
			}
			break
		}
	}

	if !refused {
		t.Errorf("login was never rate limited within %d attempts", limit+2)
	}
}
