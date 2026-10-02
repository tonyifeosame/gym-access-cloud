package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Erasure must never touch a LIVE member, an innocent row, or a terminal's
// last chance to forget somebody (review of 8f91938 / bf21e6a).
//
// The fixture these tests turn on is the shape production actually has: a
// member deleted before 039 whose member number now belongs to somebody live.

// reusedNumber is the production-shaped fixture: an old holder of P-100,
// soft-deleted long ago the pre-039 way, and a live member given P-100 since,
// each with their own history in every table erasure touches.
type reusedNumber struct {
	env              *testEnv
	companyID        int64
	oldID, liveID    int64
	oldDeletedAt     time.Time
	operatorID       int64
	liveConversation int64
	liveToolCall     int64
}

const reusedExternalID = "P-100"

func newReusedNumber(t *testing.T) *reusedNumber {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	company := companyIDBySlug(t, "one")
	operator := mustCreateOperator(t, company, "ops-reuse@example.com", models.RoleOwner)

	old := seedPerson(t, company, reusedExternalID, "Old Holder")
	mustExec(t, `UPDATE people SET email = 'old@example.com', phone = '+2340000000001',
	                               created_at = CURRENT_TIMESTAMP - interval '200 days',
	                               deleted_at = CURRENT_TIMESTAMP - interval '100 days'
	              WHERE id = $1`, old)
	var oldDeletedAt time.Time
	mustScan(t, `SELECT deleted_at FROM people WHERE id = `+itoa(old), &oldDeletedAt)

	live := seedPerson(t, company, reusedExternalID, "Live Holder")
	mustExec(t, `UPDATE people SET email = 'live@example.com', phone = '+2340000000002',
	                               created_at = CURRENT_TIMESTAMP - interval '50 days'
	              WHERE id = $1`, live)
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, company, live)

	era := func(personID int64, ago string, name string) {
		mustExec(t, `INSERT INTO events (company_id, person_id, subject_external_id, event_type, decision, occurred_at)
		             VALUES ($1, $2, $3, 'ACCESS_GRANTED', 'GRANTED', CURRENT_TIMESTAMP - $4::interval)`,
			company, personID, reusedExternalID, ago)
		mustExec(t, `INSERT INTO events (company_id, subject_external_id, event_type, decision, occurred_at)
		             VALUES ($1, $2, 'ACCESS_DENIED', 'DENIED', CURRENT_TIMESTAMP - $3::interval)`,
			company, reusedExternalID, ago)
		mustExec(t, `INSERT INTO access_logs (company_id, person_external_id, granted, source, site_name, occurred_at)
		             VALUES ($1, $2, FALSE, 'FINGERPRINT', 'Site A', CURRENT_TIMESTAMP - $3::interval)`,
			company, reusedExternalID, ago)
		mustExec(t, `INSERT INTO audit_events (company_id, actor_user_id, actor_email, action, target_type,
		                                       target_label, changes, occurred_at)
		             VALUES ($1, $2, 'ops-reuse@example.com', 'PERSON_CREATED', 'PERSON', $3,
		                     jsonb_build_object('full_name', $4::text), CURRENT_TIMESTAMP - $5::interval)`,
			company, operator.ID, reusedExternalID, name, ago)
	}
	era(old, "150 days", "Old Holder")
	era(live, "10 days", "Live Holder")

	f := &reusedNumber{env: env, companyID: company, oldID: old, liveID: live,
		oldDeletedAt: oldDeletedAt, operatorID: operator.ID}

	// The live holder's assistant records, in the live era.
	mustScan(t, `INSERT INTO assistant_conversations (company_id, user_id, model, prompt_hash)
	             VALUES (`+itoa(company)+`, `+itoa(operator.ID)+`, 'm', repeat('a', 64)) RETURNING id`,
		&f.liveConversation)
	mustExec(t, `INSERT INTO assistant_messages (conversation_id, seq, role, content, created_at)
	             VALUES ($1, 1, 'user', jsonb_build_object('text', 'why was P-100 Live Holder denied?'),
	                     CURRENT_TIMESTAMP - interval '5 days')`, f.liveConversation)
	mustScan(t, `INSERT INTO assistant_tool_calls (turn_id, company_id, user_id, session_id, tool_name,
	                                               arguments, route, status, created_at)
	             VALUES (gen_random_uuid(), `+itoa(company)+`, `+itoa(operator.ID)+`, 1, 'get_person',
	                     jsonb_build_object('external_id', 'P-100'), '/console/people/P-100', 'EXECUTED',
	                     CURRENT_TIMESTAMP - interval '5 days') RETURNING id`, &f.liveToolCall)
	return f
}

// liveSnapshot is everything about the live holder erasure could reach.
type liveSnapshot struct {
	name, email     string
	active, deleted bool
	permissions     int
	linkedEvents    int
	liveEraEvents   int
	liveEraLogs     int
	liveEraAudit    int
	conversation    int
	toolArgs, route string
}

func (f *reusedNumber) snapshot(t *testing.T) liveSnapshot {
	t.Helper()
	var s liveSnapshot
	var email *string
	var deletedAt *time.Time
	mustScan(t, `SELECT full_name, email, active, deleted_at FROM people WHERE id = `+itoa(f.liveID),
		&s.name, &email, &s.active, &deletedAt)
	if email != nil {
		s.email = *email
	}
	s.deleted = deletedAt != nil
	s.permissions = countWhere(t, `SELECT count(*) FROM permissions WHERE person_id = $1`, f.liveID)
	s.linkedEvents = countWhere(t, `SELECT count(*) FROM events WHERE person_id = $1 AND subject_external_id = $2`,
		f.liveID, reusedExternalID)
	s.liveEraEvents = countWhere(t, `SELECT count(*) FROM events WHERE subject_external_id = $1
	                                  AND occurred_at > CURRENT_TIMESTAMP - interval '30 days'`, reusedExternalID)
	s.liveEraLogs = countWhere(t, `SELECT count(*) FROM access_logs WHERE person_external_id = $1
	                                AND occurred_at > CURRENT_TIMESTAMP - interval '30 days'`, reusedExternalID)
	s.liveEraAudit = countWhere(t, `SELECT count(*) FROM audit_events WHERE target_label = $1
	                                 AND changes->>'full_name' = 'Live Holder'`, reusedExternalID)
	s.conversation = countWhere(t, `SELECT count(*) FROM assistant_conversations WHERE id = $1`, f.liveConversation)
	mustScan(t, `SELECT arguments::text, route FROM assistant_tool_calls WHERE id = `+itoa(f.liveToolCall),
		&s.toolArgs, &s.route)
	return s
}

// TestALiveMemberHoldingADeletedMembersNumberIsNeverTouched is the primary
// safety requirement, run through every erasure path that exists: the legacy
// sweep, finalisation, and replay (twice).
func TestALiveMemberHoldingADeletedMembersNumberIsNeverTouched(t *testing.T) {
	f := newReusedNumber(t)
	before := f.snapshot(t)
	if before.liveEraEvents != 2 || before.liveEraLogs != 1 || before.liveEraAudit != 1 || before.conversation != 1 {
		t.Fatalf("fixture is not what it claims: %+v", before)
	}

	ctx := context.Background()
	if n, err := database.EraseLegacyDeletedPeople(ctx); err != nil || n != 1 {
		t.Fatalf("legacy erasure = %d (err %v), want the 1 old holder", n, err)
	}
	if _, err := database.FinalizeErasedPeople(ctx, 0); err != nil {
		t.Fatalf("finalising: %v", err)
	}
	for i := 0; i < 2; i++ {
		if n, err := database.ReplayDeletedSubjects(ctx); err != nil || n != 0 {
			t.Fatalf("replay %d erased %d (err %v); the live holder was created after the deletion", i+1, n, err)
		}
	}

	after := f.snapshot(t)
	if after != before {
		t.Errorf("the LIVE holder of the reused number was modified:\n before %+v\n after  %+v", before, after)
	}

	// And the old holder's own era WAS erased.
	if n := countWhere(t, `SELECT count(*) FROM events WHERE subject_external_id = $1
	                        AND occurred_at < CURRENT_TIMESTAMP - interval '100 days'`, reusedExternalID); n != 0 {
		t.Errorf("%d event(s) from the old holder's era still carry the number", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events WHERE changes->>'full_name' = 'Old Holder'
	                          OR (target_label = $1 AND occurred_at < CURRENT_TIMESTAMP - interval '100 days')`,
		reusedExternalID); n != 0 {
		t.Errorf("%d audit row(s) from the old holder's era still name them", n)
	}

	// The ledger is dated by the ORIGINAL deletion, and replay did not grow it.
	var ledgerDeletedAt time.Time
	mustScan(t, `SELECT deleted_at FROM deleted_subjects WHERE company_id = `+itoa(f.companyID), &ledgerDeletedAt)
	if d := ledgerDeletedAt.Sub(f.oldDeletedAt); d > time.Millisecond || d < -time.Millisecond {
		t.Errorf("ledger deleted_at = %v, want the original deletion %v", ledgerDeletedAt, f.oldDeletedAt)
	}
	if n := countWhere(t, `SELECT count(*) FROM deleted_subjects WHERE company_id = $1`, f.companyID); n != 1 {
		t.Errorf("ledger entries = %d after erasure and two replays, want 1", n)
	}

	// A late event the live holder generates now is still theirs.
	erased, err := database.IsErasedSubject(f.companyID, reusedExternalID, time.Now())
	if err != nil || erased {
		t.Errorf("a live-era event for the reused number is treated as the deleted member's (%v, %v)", erased, err)
	}
}

// Deleting the LIVE holder while the old holder is still unerased reaches only
// the live holder's own era; the old holder's rows wait for their own erasure.
func TestDeletingTheNewHolderLeavesTheOldHoldersEraAlone(t *testing.T) {
	f := newReusedNumber(t)
	if err := database.DeleteMember(f.companyID, reusedExternalID); err != nil {
		t.Fatalf("deleting the live holder: %v", err)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events WHERE target_label = $1
	                        AND changes->>'full_name' = 'Old Holder'`, reusedExternalID); n != 1 {
		t.Errorf("the old holder's audit row was relabelled by the new holder's deletion (%d left, want 1)", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM events WHERE subject_external_id = $1
	                        AND occurred_at < CURRENT_TIMESTAMP - interval '100 days'`, reusedExternalID); n != 2 {
		t.Errorf("the old holder's events were anonymised by the new holder's deletion (%d left, want 2)", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events WHERE changes->>'full_name' = 'Live Holder'`); n != 0 {
		t.Error("the deleted live holder's own audit row still names them")
	}
}

// ---------------------------------------------------------------------------
// Terminals that were not being synced when somebody was deleted
// ---------------------------------------------------------------------------

func TestAPausedTerminalIsToldWhenItResumes(t *testing.T) {
	f := newDeletionFixture(t)
	// The terminal was sent the person before (the delivered CREATE every
	// placement implies).
	mustExec(t, `INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
	                                    payload, protocol_version, status, acknowledged_at)
	             SELECT d.site_id, d.id, 'CREATE', 'PERSON', $1, $2,
	                    jsonb_build_object('member_id', $3::text, 'full_name', 'Deleted Person'), 1,
	                    'COMPLETED', CURRENT_TIMESTAMP
	               FROM devices d WHERE d.id = $4`, f.personID, f.externalID, f.externalID, f.deviceID)
	if err := database.PauseDeviceSync(f.deviceID, "test", "bench evidence"); err != nil {
		t.Fatal(err)
	}

	f.deletePerson(t)

	if n := countWhere(t, `SELECT count(*) FROM sync_jobs WHERE device_id = $1 AND job_type = 'DELETE'`,
		f.deviceID); n != 0 {
		t.Fatalf("a DELETE was queued to a paused terminal (%d); quarantine must hold", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM sync_jobs WHERE entity_id = $1 AND job_type = 'CREATE'
	                        AND payload::text NOT LIKE '%Deleted Person%'`, f.personID); n != 1 {
		t.Fatalf("the paused terminal's job history was not kept stripped (%d rows)", n)
	}
	if n := finalize(t); n != 0 {
		t.Fatalf("finalised %d while a paused terminal still holds the person", n)
	}

	if err := database.ResumeDeviceSync(f.deviceID); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := database.ReconcileDeviceRoster(f.deviceID); err != nil || removed != 1 {
		t.Fatalf("reconcile after resume removed %d (err %v), want the 1 deleted person", removed, err)
	}
	f.completeDeleteJobs(t)
	f.reportRemoved(t)
	if n := finalize(t); n != 1 {
		t.Fatalf("finalised %d once the resumed terminal acknowledged, want 1", n)
	}

	// The reconciler does not re-queue a DELETE the terminal acknowledged.
	if _, removed, err := database.ReconcileDeviceRoster(f.deviceID); err != nil || removed != 0 {
		t.Errorf("a second reconcile queued %d DELETE(s) (err %v), want 0", removed, err)
	}
}

func TestAFailedDeleteBlocksFinalisation(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	f.reportRemoved(t)
	mustExec(t, `UPDATE sync_jobs SET status = 'FAILED', error_message = 'gave up'
	              WHERE entity_id = $1 AND job_type = 'DELETE'`, f.personID)
	if n := finalize(t); n != 0 {
		t.Errorf("finalised %d although the terminal's DELETE FAILED", n)
	}
}

// A DELETE waiting out a backoff for the old holder of a number is never
// overtaken by a job for the new holder.
func TestNothingOvertakesAPendingDeleteForTheSameNumber(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	mustExec(t, `UPDATE sync_jobs SET next_attempt_at = CURRENT_TIMESTAMP + interval '1 hour'
	              WHERE entity_id = $1 AND job_type = 'DELETE'`, f.personID)

	newcomer := seedPerson(t, f.companyID, f.externalID, "Newcomer")
	mustExec(t, `INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
	                                    payload, protocol_version, status)
	             SELECT d.site_id, d.id, 'CREATE', 'PERSON', $1, $2,
	                    jsonb_build_object('member_id', $3::text, 'full_name', 'Newcomer'), 1, 'PENDING'
	               FROM devices d WHERE d.id = $4`, newcomer, f.externalID, f.externalID, f.deviceID)

	for _, job := range pollJobs(t, f.env, f.deviceKey) {
		if job["entity_external_id"] == f.externalID {
			t.Fatalf("a %v job for the reused number overtook the old holder's pending DELETE", job["job_type"])
		}
	}

	mustExec(t, `UPDATE sync_jobs SET next_attempt_at = CURRENT_TIMESTAMP WHERE entity_id = $1 AND job_type = 'DELETE'`,
		f.personID)
	jobs := pollJobs(t, f.env, f.deviceKey)
	if len(jobs) == 0 || jobs[0]["job_type"] != "DELETE" {
		t.Fatalf("first job for the number = %v, want the DELETE", jobs)
	}
	for _, job := range jobs {
		if job["job_type"] == "CREATE" {
			t.Fatal("the CREATE was handed out in the same poll as the DELETE it must follow")
		}
	}
}

// A late REMOVED report for a number the ledger knows, resolving to a live
// holder this terminal was not asked to remove, leaves that holder alone.
func TestARemovedReportForAReusedNumberLeavesTheLiveHolderAlone(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	finalize(t)

	newcomer := seedPerson(t, f.companyID, f.externalID, "Newcomer")
	credential := seedFingerprintCredential(t, f.companyID, newcomer, f.deviceID, "ACTIVE")
	seedPlacement(t, credential, f.deviceID, models.PlacementPlaced, 9)

	if res := f.reportRemoved(t); res.Code != http.StatusOK {
		t.Fatalf("late REMOVED = %d: %s", res.Code, res.Raw)
	}
	var state string
	mustScan(t, `SELECT pl.state FROM credential_placements pl JOIN credentials c ON c.id = pl.credential_id
	              WHERE c.person_id = `+itoa(newcomer), &state)
	if state != models.PlacementPlaced {
		t.Errorf("the live holder's placement became %s after the old holder's late REMOVED", state)
	}
}

// ---------------------------------------------------------------------------
// The trigger exception admits only what erasure does
// ---------------------------------------------------------------------------

func TestTheAuditExceptionAdmitsOnlyPseudonymsAndKeyRemoval(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	var id int64
	mustScan(t, `INSERT INTO audit_events (company_id, actor_email, action, target_type, target_label, changes)
	             VALUES (`+itoa(company)+`, 'ops@example.com', 'PERSON_UPDATED', 'PERSON', 'P-1',
	                     '{"full_name": "A", "active": true}'::jsonb) RETURNING id`, &id)

	try := func(query string) error {
		tx, err := database.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`SELECT set_config('accesslink.anonymizing', 'on', true)`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(query, id)
		return err
	}

	refused := map[string]string{
		"forging the actor":       `UPDATE audit_events SET actor_email = 'someone.else@example.com' WHERE id = $1`,
		"relabelling arbitrarily": `UPDATE audit_events SET target_label = 'P-2' WHERE id = $1`,
		"adding to the diff":      `UPDATE audit_events SET changes = changes || '{"x": 1}'::jsonb WHERE id = $1`,
		"altering the diff":       `UPDATE audit_events SET changes = '{"full_name": "B", "active": true}'::jsonb WHERE id = $1`,
	}
	for name, query := range refused {
		if err := try(query); err == nil {
			t.Errorf("%s was admitted by the anonymisation exception", name)
		}
	}
	admitted := map[string]string{
		"pseudonymising the actor": `UPDATE audit_events SET actor_email = 'deleted-operator:x' WHERE id = $1`,
		"pseudonymising the label": `UPDATE audit_events SET target_label = 'deleted-person:x' WHERE id = $1`,
		"removing a key":           `UPDATE audit_events SET changes = changes - 'full_name' WHERE id = $1`,
	}
	for name, query := range admitted {
		if err := try(query); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// A terminal claimed or collected records only the operator's ADDRESS, with no
// user id; those rows, and diffs that name the address, are pseudonymised too.
func TestAddressOnlyAuditRowsArePseudonymised(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	leaver := mustCreateOperator(t, company, "claimer@example.com", models.RoleManager)
	other := mustCreateOperator(t, company, "other@example.com", models.RoleOwner)
	mustExec(t, `INSERT INTO audit_events (company_id, actor_email, action, changes)
	             VALUES ($1, 'claimer@example.com', 'DEVICE_CLAIMED', '{"issued_by": "claimer@example.com"}'::jsonb)`,
		company)
	mustExec(t, `INSERT INTO audit_events (company_id, actor_user_id, actor_email, action, changes)
	             VALUES ($1, $2, 'other@example.com', 'TERMINAL_COLLECTED',
	                     '{"approved_by": "claimer@example.com", "serial": "AT-1"}'::jsonb)`, company, other.ID)

	if _, err := database.DeleteUser(company, leaver.ID); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events
	                        WHERE row_to_json(audit_events)::text ILIKE '%claimer@example.com%'`); n != 0 {
		t.Errorf("%d audit row(s) still carry the deleted operator's address", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events WHERE actor_email = 'other@example.com'
	                        AND changes->>'serial' = 'AT-1'`); n != 1 {
		t.Error("another operator's row lost more than the deleted operator's address")
	}
}

// An address given to a new account after the old one was deleted keeps the
// new account's attribution when the old account is erased.
func TestAReinvitedAddressKeepsTheNewAccountsAttribution(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	old := mustCreateOperator(t, company, "reused@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET created_at = CURRENT_TIMESTAMP - interval '200 days',
	                              deleted_at = CURRENT_TIMESTAMP - interval '100 days', active = FALSE
	              WHERE id = $1`, old.ID)
	current := mustCreateOperator(t, company, "reused@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET created_at = CURRENT_TIMESTAMP - interval '50 days' WHERE id = $1`, current.ID)

	for _, row := range []struct {
		ago    string
		prefix string
	}{{"150 days", "atp_live_0000a001"}, {"10 days", "atp_live_0000a002"}} {
		mustExec(t, `INSERT INTO api_credentials (company_id, name, key_hash, key_prefix, scopes, created_by_email, created_at)
		             VALUES ($1, $2::text, repeat($4::text, 64), $2::text, ARRAY['members:read'], 'reused@example.com',
		                     CURRENT_TIMESTAMP - $3::interval)`, company, row.prefix, row.ago, row.prefix[len(row.prefix)-1:])
		mustExec(t, `INSERT INTO audit_events (company_id, actor_email, action, occurred_at)
		             VALUES ($1, 'reused@example.com', 'DEVICE_CLAIMED', CURRENT_TIMESTAMP - $2::interval)`,
			company, row.ago)
	}

	if n, err := database.EraseLegacyDeletedOperators(context.Background()); err != nil || n != 1 {
		t.Fatalf("legacy operator erasure = %d (err %v), want 1", n, err)
	}

	if n := countWhere(t, `SELECT count(*) FROM api_credentials WHERE key_prefix = 'atp_live_0000a002'
	                        AND created_by_email = 'reused@example.com'`); n != 1 {
		t.Error("the current account's API key lost its attribution")
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events WHERE actor_email = 'reused@example.com'
	                        AND occurred_at > CURRENT_TIMESTAMP - interval '30 days'`); n != 1 {
		t.Error("the current account's audit row was pseudonymised")
	}
	if n := countWhere(t, `SELECT count(*) FROM api_credentials WHERE key_prefix = 'atp_live_0000a001'
	                        AND created_by_email LIKE 'deleted-operator:%'`); n != 1 {
		t.Error("the old account's API key was not pseudonymised")
	}
	if n := countWhere(t, `SELECT count(*) FROM users WHERE id = $1`, current.ID); n != 1 {
		t.Error("the current account was deleted")
	}
}

// ---------------------------------------------------------------------------
// Assistant matching is by whole token
// ---------------------------------------------------------------------------

func TestAssistantScrubMatchesWholeTokensOnly(t *testing.T) {
	f := newDeletionFixture(t)
	mustExec(t, `UPDATE people SET external_id = '1001', full_name = 'Anna Bell' WHERE id = $1`, f.personID)
	f.externalID = "1001"
	var userID int64
	mustScan(t, `SELECT id FROM users WHERE email = 'placement-delete@example.com'`, &userID)
	conversation := func(text string) int64 {
		var id int64
		mustScan(t, `INSERT INTO assistant_conversations (company_id, user_id, model, prompt_hash)
		              VALUES (`+itoa(f.companyID)+`, `+itoa(userID)+`, 'm', repeat('a', 64)) RETURNING id`, &id)
		mustExec(t, `INSERT INTO assistant_messages (conversation_id, seq, role, content)
		             VALUES ($1, 1, 'user', jsonb_build_object('text', $2::text))`, id, text)
		return id
	}
	byNumber := conversation("member 1001 was denied")
	byName := conversation("what happened to anna bell?")
	longerNumber := conversation("member 10012 was denied")
	longerName := conversation("Hannah Bellamy signed in")

	f.deletePerson(t)

	for name, id := range map[string]int64{"by number": byNumber, "by name": byName} {
		if countWhere(t, `SELECT count(*) FROM assistant_conversations WHERE id = $1`, id) != 0 {
			t.Errorf("the conversation naming the member %s survived", name)
		}
	}
	for name, id := range map[string]int64{"10012": longerNumber, "Hannah Bellamy": longerName} {
		if countWhere(t, `SELECT count(*) FROM assistant_conversations WHERE id = $1`, id) != 1 {
			t.Errorf("an unrelated conversation mentioning %q was deleted", name)
		}
	}
}
