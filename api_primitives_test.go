package main

import (
	"context"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The remaining P1 primitives: idempotency records and the tenant-scoped
// transaction wrapper.
//
// NEITHER IS MOUNTED ON A ROUTE. Both are exercised directly here, because the
// public endpoints they exist for are not in this build -- and a primitive that
// arrives untested alongside the first thing that depends on it is a primitive
// whose bugs are discovered as endpoint bugs.

// seedCredential mints a credential through the store, for tests that need an
// integration identity without going through the console.
func seedIntegrationCredential(t *testing.T, companyID int64, name string) *models.APICredentialIssued {
	t.Helper()
	issued, err := database.IssueAPICredential(database.APICredentialIssueInput{
		CompanyID:   companyID,
		Name:        name,
		Environment: models.APIEnvironmentLive,
		Scopes:      []string{models.ScopeMembersRead},
	})
	if err != nil {
		t.Fatalf("seeding credential %q: %v", name, err)
	}
	return issued
}

// credentialRowID resolves the internal id of a seeded credential.
func integrationCredentialRowID(t *testing.T, publicID string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM api_credentials WHERE public_id::text = $1`, publicID).Scan(&id); err != nil {
		t.Fatalf("resolving credential row: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// The P1 boundary
// ---------------------------------------------------------------------------

// P1 SHIPPED PRIMITIVES AND NO PUBLIC SURFACE; P3 mounts exactly the routes
// API_SPEC.md section 18 specifies, and nothing else. A public route that is
// not in the specification -- added by accident, or by somebody continuing the
// work without reading the plan -- fails here rather than arriving in
// production ahead of its contract. Every addition to this list is preceded
// by an addition to section 18.
func TestPublicAPIMountsExactlyTheSpecifiedRoutes(t *testing.T) {
	env := newTestEnv(t)

	found := map[string]bool{}
	for _, route := range env.router.Routes() {
		if strings.HasPrefix(route.Path, "/api/public") {
			found[route.Method+" "+route.Path] = true
		}
	}

	want := []string{
		"GET /api/public/v1/members",
		"GET /api/public/v1/members/:member_id",
		"POST /api/public/v1/members",
		"PATCH /api/public/v1/members/:member_id",
		"DELETE /api/public/v1/members/:member_id",
		"GET /api/public/v1/members/:member_id/access",
		"GET /api/public/v1/sites",
		"GET /api/public/v1/sites/:site_id",
		"GET /api/public/v1/events",
	}
	for _, w := range want {
		if !found[w] {
			t.Errorf("specified public route is not mounted: %s", w)
		}
		delete(found, w)
	}
	for extra := range found {
		t.Errorf("public route mounted without a section 18 contract: %s", extra)
	}
}

// The credential-management routes are the ONLY console surface P1 adds. If this
// count changes, either a route was added outside the plan or one was lost.
func TestP1AddsOnlyTheCredentialConsoleRoutes(t *testing.T) {
	env := newTestEnv(t)

	found := map[string]bool{}
	for _, route := range env.router.Routes() {
		if strings.HasPrefix(route.Path, "/api/v1/console/api-credentials") {
			found[route.Method+" "+route.Path] = true
		}
	}

	want := []string{
		"GET /api/v1/console/api-credentials",
		"POST /api/v1/console/api-credentials",
		"GET /api/v1/console/api-credentials/:id",
		"GET /api/v1/console/api-credentials/:id/usage",
		"POST /api/v1/console/api-credentials/:id/rotate",
		"DELETE /api/v1/console/api-credentials/:id",
		"POST /api/v1/console/api-credentials/revoke-all",
	}

	for _, route := range want {
		if !found[route] {
			t.Errorf("missing route: %s", route)
		}
	}
	if len(found) != len(want) {
		t.Errorf("the credential tree has %d routes, want %d: %v", len(found), len(want), found)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestAnUnseenIdempotencyKeyProceeds(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Idem").ID)

	result, err := database.BeginIdempotent(context.Background(), one, credential,
		"key-1", database.FingerprintRequest("POST", "/v1/members", []byte(`{"a":1}`)), 0)
	if err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if result.Outcome != database.IdempotencyProceed {
		t.Errorf("outcome = %v, want Proceed", result.Outcome)
	}
}

func TestAnIdenticalRetryReplaysTheStoredResponse(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Replay").ID)

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M1"}`))
	ctx := context.Background()

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-2", fingerprint, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	stored := []byte(`{"object":"member","id":"abc"}`)
	if err := database.CompleteIdempotent(ctx, one, credential, "key-2", 201, stored); err != nil {
		t.Fatalf("completing: %v", err)
	}

	result, err := database.BeginIdempotent(ctx, one, credential, "key-2", fingerprint, 0)
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if result.Outcome != database.IdempotencyReplay {
		t.Fatalf("outcome = %v, want Replay", result.Outcome)
	}
	if result.ResponseStatus != 201 {
		t.Errorf("replayed status = %d, want 201", result.ResponseStatus)
	}
	// The stored bytes, not a re-rendered answer: a replay must be
	// indistinguishable from the original, including a field the handler
	// computed from something that has since changed.
	if !strings.Contains(string(result.ResponseBody), `"id"`) {
		t.Errorf("replayed body = %s", result.ResponseBody)
	}
}

// SAME KEY, DIFFERENT REQUEST is a caller bug. Answering with the first response
// would confirm a mutation the caller did not ask for this time, which is worse
// than refusing.
func TestTheSameKeyWithADifferentRequestIsRefused(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Reuse").ID)
	ctx := context.Background()

	first := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M1"}`))
	second := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M2"}`))

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-3", first, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if err := database.CompleteIdempotent(ctx, one, credential, "key-3", 201, []byte(`{}`)); err != nil {
		t.Fatalf("completing: %v", err)
	}

	result, err := database.BeginIdempotent(ctx, one, credential, "key-3", second, 0)
	if err != nil {
		t.Fatalf("reusing: %v", err)
	}
	if result.Outcome != database.IdempotencyReuse {
		t.Errorf("outcome = %v, want Reuse", result.Outcome)
	}
}

// The fingerprint is checked BEFORE the state, so a different request under a
// key that is still running is a reuse rather than an in-progress.
func TestADifferentRequestUnderAnInFlightKeyIsAReuse(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "InflightReuse").ID)
	ctx := context.Background()

	first := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M1"}`))
	second := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M2"}`))

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-4", first, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}

	result, err := database.BeginIdempotent(ctx, one, credential, "key-4", second, 0)
	if err != nil {
		t.Fatalf("claiming again: %v", err)
	}
	if result.Outcome != database.IdempotencyReuse {
		t.Errorf("outcome = %v, want Reuse", result.Outcome)
	}
}

func TestAnIdenticalRequestStillRunningIsInProgress(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Inflight").ID)
	ctx := context.Background()

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M1"}`))

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-5", fingerprint, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}

	result, err := database.BeginIdempotent(ctx, one, credential, "key-5", fingerprint, 0)
	if err != nil {
		t.Fatalf("claiming again: %v", err)
	}
	if result.Outcome != database.IdempotencyInProgress {
		t.Errorf("outcome = %v, want InProgress", result.Outcome)
	}
}

// A 500 is not a decision. Storing it would turn one transient failure into a
// permanent one for that key; releasing the claim makes a retry a fresh attempt.
func TestAServerErrorReleasesTheClaimRatherThanStoringIt(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Failed").ID)
	ctx := context.Background()

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{}`))

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-6", fingerprint, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if err := database.CompleteIdempotent(ctx, one, credential, "key-6", 500,
		[]byte(`{"error":{}}`)); err != nil {
		t.Fatalf("completing: %v", err)
	}

	result, err := database.BeginIdempotent(ctx, one, credential, "key-6", fingerprint, 0)
	if err != nil {
		t.Fatalf("retrying: %v", err)
	}
	if result.Outcome != database.IdempotencyProceed {
		t.Errorf("outcome after a 500 = %v, want Proceed", result.Outcome)
	}
}

func TestAnExpiredRecordIsReclaimedRatherThanConflicting(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Expired").ID)
	ctx := context.Background()

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{}`))

	if _, err := database.BeginIdempotent(ctx, one, credential, "key-7", fingerprint, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if err := database.CompleteIdempotent(ctx, one, credential, "key-7", 201, []byte(`{}`)); err != nil {
		t.Fatalf("completing: %v", err)
	}

	// created_at moves with it: idempotency_records_expiry_check refuses a row
	// whose expiry precedes its creation, so an expiry alone is not a state this
	// table can hold. A really-expired record was written a day ago.
	mustExec(t, `UPDATE idempotency_records
	                SET created_at = CURRENT_TIMESTAMP - interval '2 days',
	                    expires_at = CURRENT_TIMESTAMP - interval '1 minute'`)

	// The retention window has passed, so the key is free again -- reclaimed in
	// place rather than deleted and re-inserted, which would race the same way
	// the original insert did.
	result, err := database.BeginIdempotent(ctx, one, credential, "key-7", fingerprint, 0)
	if err != nil {
		t.Fatalf("reclaiming: %v", err)
	}
	if result.Outcome != database.IdempotencyProceed {
		t.Errorf("outcome for an expired record = %v, want Proceed", result.Outcome)
	}
}

// TWO CUSTOMERS MUST NEVER REACH EACH OTHER'S STORED RESPONSE, and two
// integrations at one customer must not collide on a key one of them generated
// carelessly. Both are the unique index, and both are asserted here.
func TestIdempotencyRecordsAreIsolatedByCompanyAndCredential(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	ctx := context.Background()

	oneA := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "One A").ID)
	oneB := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "One B").ID)
	twoA := integrationCredentialRowID(t, seedIntegrationCredential(t, two, "Two A").ID)

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{"member_id":"M1"}`))

	if _, err := database.BeginIdempotent(ctx, one, oneA, "shared-key", fingerprint, 0); err != nil {
		t.Fatalf("claiming for one/A: %v", err)
	}
	if err := database.CompleteIdempotent(ctx, one, oneA, "shared-key", 201,
		[]byte(`{"who":"one-a"}`)); err != nil {
		t.Fatalf("completing for one/A: %v", err)
	}

	// A second credential in the SAME company: an independent record.
	result, err := database.BeginIdempotent(ctx, one, oneB, "shared-key", fingerprint, 0)
	if err != nil {
		t.Fatalf("claiming for one/B: %v", err)
	}
	if result.Outcome != database.IdempotencyProceed {
		t.Errorf("a second credential in the same company got %v, want Proceed", result.Outcome)
	}

	// A different company entirely.
	result, err = database.BeginIdempotent(ctx, two, twoA, "shared-key", fingerprint, 0)
	if err != nil {
		t.Fatalf("claiming for two/A: %v", err)
	}
	if result.Outcome != database.IdempotencyProceed {
		t.Errorf("another company got %v, want Proceed", result.Outcome)
	}

	// Three independent records, no replay between them.
	if n := queryInt(t, `SELECT count(*) FROM idempotency_records`); n != 3 {
		t.Errorf("%d records exist, want 3", n)
	}
}

func TestExpiredIdempotencyRecordsArePurged(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	credential := integrationCredentialRowID(t, seedIntegrationCredential(t, one, "Purge").ID)
	ctx := context.Background()

	fingerprint := database.FingerprintRequest("POST", "/v1/members", []byte(`{}`))
	if _, err := database.BeginIdempotent(ctx, one, credential, "key-8", fingerprint, 0); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	// created_at moves with it: idempotency_records_expiry_check refuses a row
	// whose expiry precedes its creation, so an expiry alone is not a state this
	// table can hold. A really-expired record was written a day ago.
	mustExec(t, `UPDATE idempotency_records
	                SET created_at = CURRENT_TIMESTAMP - interval '2 days',
	                    expires_at = CURRENT_TIMESTAMP - interval '1 minute'`)

	purged, err := database.PurgeExpiredIdempotencyRecords(ctx)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged %d records, want 1", purged)
	}
}

// ---------------------------------------------------------------------------
// The scoped transaction wrapper
// ---------------------------------------------------------------------------

func TestAScopedTransactionAppliesTheTenantAndTheTimeout(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	err := database.WithTenant(context.Background(), one, 0, func(tx *database.ScopedTx) error {
		setting, err := database.TenantSetting(context.Background(), tx)
		if err != nil {
			return err
		}
		if setting == "" {
			t.Error("app.company_id is not set inside a scoped transaction")
		}
		if setting != itoa(one) {
			t.Errorf("app.company_id = %q, want %q", setting, itoa(one))
		}

		var timeout string
		if err := tx.QueryRow(`SHOW statement_timeout`).Scan(&timeout); err != nil {
			return err
		}
		if timeout == "0" {
			t.Error("statement_timeout is unset inside a scoped transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scoped transaction: %v", err)
	}
}

// THE LEAKAGE TEST. database/sql hands out pooled connections, and a connection
// carries session state back to the pool with it -- so `SET` or `SET SESSION`
// would leak a tenant id into whichever request drew the same connection next.
// That is a cross-tenant defect with no code path anybody could point at.
//
// SET LOCAL is reverted by PostgreSQL on COMMIT, so the leak is not merely
// avoided by convention. This is the proof.
func TestTheTenantSettingDoesNotLeakToAPooledConnection(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// Pin the pool to a single connection, so the query afterwards is
	// GUARANTEED to be the same one the transaction used. Without this the test
	// could pass by drawing a different connection and prove nothing.
	previousOpen := 25
	database.DB.SetMaxOpenConns(1)
	t.Cleanup(func() { database.DB.SetMaxOpenConns(previousOpen) })

	if err := database.WithTenant(context.Background(), one, 0,
		func(tx *database.ScopedTx) error { return nil }); err != nil {
		t.Fatalf("scoped transaction: %v", err)
	}

	setting, err := database.TenantSetting(context.Background(), database.DB)
	if err != nil {
		t.Fatalf("reading the setting after commit: %v", err)
	}
	if setting != "" {
		t.Fatalf("app.company_id is %q on a pooled connection after the transaction "+
			"committed; SET LOCAL was expected to revert it", setting)
	}

	// And after a rollback, which is the path an error takes.
	if err := database.WithTenant(context.Background(), one, 0,
		func(tx *database.ScopedTx) error { return context.Canceled }); err == nil {
		t.Fatal("a failing scoped transaction reported success")
	}

	setting, err = database.TenantSetting(context.Background(), database.DB)
	if err != nil {
		t.Fatalf("reading the setting after rollback: %v", err)
	}
	if setting != "" {
		t.Errorf("app.company_id is %q after a rollback", setting)
	}
}

// current_setting(..., true) returns NULL when unset, and NULL = company_id is
// not true -- so a transaction that forgot to scope itself will see NOTHING once
// the policies exist, rather than everything. That is the correct failure
// direction and it is worth asserting before anything depends on it.
func TestAnUnscopedConnectionHasNoTenantSetting(t *testing.T) {
	newTestEnv(t)

	setting, err := database.TenantSetting(context.Background(), database.DB)
	if err != nil {
		t.Fatalf("reading the setting: %v", err)
	}
	if setting != "" {
		t.Errorf("an unscoped connection reports app.company_id = %q", setting)
	}
}

func TestAScopedTransactionRequiresATenant(t *testing.T) {
	newTestEnv(t)

	if _, err := database.BeginScoped(context.Background(), 0, 0); err == nil {
		t.Error("a scoped transaction was opened with no company id")
	}
}

// The wrapper commits on success and rolls back on failure, so neither the
// settings nor the transaction can be left behind.
func TestWithTenantRollsBackOnError(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	err := database.WithTenant(context.Background(), one, 0, func(tx *database.ScopedTx) error {
		if _, err := tx.Exec(
			`INSERT INTO companies (name, slug) VALUES ('Rolled back', 'rolled-back')`); err != nil {
			return err
		}
		return context.Canceled
	})
	if err == nil {
		t.Fatal("the wrapper reported success for a failing function")
	}

	if n := queryInt(t, `SELECT count(*) FROM companies WHERE slug = 'rolled-back'`); n != 0 {
		t.Error("the transaction was not rolled back")
	}
}
