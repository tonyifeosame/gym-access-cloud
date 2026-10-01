package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/maintenance"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Erasure (039): deleting a member or an operator removes them, rather than
// hiding them.
//
// Most tests start from deletionFixture -- one terminal, one MANAGER session,
// and person P-DELETE whose credential that terminal holds at slot 7 -- and
// add whatever personal data the case needs before deleting through the
// console, the way an operator does.

const erasedName = "Adaeze Okonkwo-Bright"

// newUUID is a v4 uuid for a terminal's event id.
func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// uploadDoorEvent is the terminal reporting one door presentation.
func (f *deletionFixture) uploadDoorEvent(t *testing.T, memberID string, at time.Time) {
	t.Helper()
	res := f.env.do("POST", "/api/v1/devices/access/log", map[string]any{
		"event_id":    newUUID(t),
		"member_id":   memberID,
		"granted":     true,
		"source":      "FINGERPRINT",
		"occurred_at": at.UTC().Format(time.RFC3339),
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("uploading a door event = %d: %s", res.Code, res.Raw)
	}
}

// giveContactDetails makes P-DELETE a person worth erasing.
func (f *deletionFixture) giveContactDetails(t *testing.T) {
	t.Helper()
	mustExec(t, `UPDATE people SET full_name = $2, email = 'ada@example.com', phone = '+2348000000000'
	              WHERE id = $1`, f.personID, erasedName)
}

func (f *deletionFixture) reportRemoved(t *testing.T) response {
	t.Helper()
	return f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id":       f.externalID,
		"state":           models.PlacementRemoved,
		"credential_type": models.CredentialFingerprint,
	}, deviceAuth(f.deviceKey))
}

// completeDeleteJobs is the terminal collecting its work and acknowledging it,
// exactly as the firmware does -- including the DELETE.
func (f *deletionFixture) completeDeleteJobs(t *testing.T) {
	t.Helper()
	for _, job := range pollJobs(t, f.env, f.deviceKey) {
		id, _ := job["id"].(float64)
		if res := ackJob(t, f.env, f.deviceKey, id, map[string]any{"status": "COMPLETED"}); res.Code != http.StatusOK {
			t.Fatalf("acknowledging job %v = %d: %s", id, res.Code, res.Raw)
		}
	}
	if n := countWhere(t, `SELECT count(*) FROM sync_jobs
	                        WHERE entity_type = 'PERSON' AND entity_id = $1
	                          AND status IN ('PENDING', 'IN_PROGRESS')`, f.personID); n != 0 {
		t.Fatalf("setup: %d person job(s) still undelivered after the terminal polled", n)
	}
}

func countWhere(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := database.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func finalize(t *testing.T) int {
	t.Helper()
	n, err := database.FinalizeErasedPeople(context.Background(), 30)
	if err != nil {
		t.Fatalf("finalising: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Phase 0: what goes immediately
// ---------------------------------------------------------------------------

func TestErasureStripsTheMemberImmediately(t *testing.T) {
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	f.deletePerson(t)

	var name, membership string
	var email, phone *string
	var active bool
	mustScan(t, `SELECT full_name, membership_type, email, phone, active FROM people
	              WHERE id = `+itoa(f.personID), &name, &membership, &email, &phone, &active)
	if name != "" || membership != "" || email != nil || phone != nil || active {
		t.Errorf("person row still holds personal data: name=%q category=%q email=%v phone=%v active=%v",
			name, membership, email, phone, active)
	}
	if countWhere(t, `SELECT count(*) FROM permissions WHERE person_id = $1`, f.personID) != 0 {
		t.Error("a deleted person still holds permissions")
	}
}

func TestErasureAnonymisesDoorHistory(t *testing.T) {
	f := newDeletionFixture(t)
	f.uploadDoorEvent(t, f.externalID, time.Now().Add(-2*time.Minute))
	f.uploadDoorEvent(t, f.externalID, time.Now().Add(-1*time.Minute))
	f.uploadDoorEvent(t, "", time.Now().Add(-1*time.Minute)) // an unknown finger

	eventsBefore := countWhere(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID)
	logsBefore := countWhere(t, `SELECT count(*) FROM access_logs WHERE company_id = $1`, f.companyID)

	f.deletePerson(t)

	if n := countWhere(t, `SELECT count(*) FROM events
	                        WHERE company_id = $1 AND (person_id = $2 OR subject_external_id = $3)`,
		f.companyID, f.personID, f.externalID); n != 0 {
		t.Errorf("%d event(s) still identify the deleted member", n)
	}
	if n := countWhere(t, `SELECT count(*) FROM access_logs
	                        WHERE company_id = $1 AND (person_id = $2 OR person_external_id = $3)`,
		f.companyID, f.personID, f.externalID); n != 0 {
		t.Errorf("%d access log(s) still identify the deleted member", n)
	}

	// The door history itself is kept: the events happened.
	if got := countWhere(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID); got != eventsBefore {
		t.Errorf("events = %d after erasure, want %d -- history must be anonymised, not deleted", got, eventsBefore)
	}
	if got := countWhere(t, `SELECT count(*) FROM access_logs WHERE company_id = $1`, f.companyID); got != logsBefore {
		t.Errorf("access logs = %d after erasure, want %d", got, logsBefore)
	}
}

// TestErasureRedactsTheAuditTrail creates and edits a person through the
// console so the audit rows are the real ones, then deletes them.
func TestErasureRedactsTheAuditTrail(t *testing.T) {
	f := newDeletionFixture(t)
	code, body := consoleCall(t, f.env.router, "POST", "/api/v1/console/people",
		`{"external_id":"P-AUDIT","full_name":"`+erasedName+`"}`, f.token, f.csrf)
	if code != http.StatusCreated {
		t.Fatalf("creating a person = %d: %v", code, body)
	}
	code, body = consoleCall(t, f.env.router, "PUT", "/api/v1/console/people/P-AUDIT",
		`{"full_name":"Renamed Person"}`, f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("renaming = %d: %v", code, body)
	}
	publicID, err := database.PersonPublicID(f.companyID, "P-AUDIT")
	if err != nil || publicID == "" {
		t.Fatalf("resolving the person: %q, %v", publicID, err)
	}
	if code, body := consoleCall(t, f.env.router, "DELETE", "/api/v1/console/people/P-AUDIT",
		"", f.token, f.csrf); code != http.StatusNoContent {
		t.Fatalf("deleting = %d: %v", code, body)
	}

	label := database.ErasedPersonLabel(publicID)
	if n := countWhere(t, `SELECT count(*) FROM audit_events
	                        WHERE company_id = $1 AND (target_label = 'P-AUDIT'
	                           OR changes::text LIKE '%Okonkwo%' OR changes::text LIKE '%Renamed%')`,
		f.companyID); n != 0 {
		t.Errorf("%d audit row(s) still name the deleted member", n)
	}
	for _, action := range []string{"PERSON_CREATED", "PERSON_UPDATED", "PERSON_DELETED"} {
		if n := countWhere(t, `SELECT count(*) FROM audit_events
		                        WHERE company_id = $1 AND action = $2 AND target_label = $3`,
			f.companyID, action, label); n != 1 {
			t.Errorf("%s rows labelled with the pseudonym = %d, want 1 -- the action must survive", action, n)
		}
	}
	// Who did it is not personal to the member, and stays.
	if n := countWhere(t, `SELECT count(*) FROM audit_events
	                        WHERE company_id = $1 AND target_label = $2
	                          AND actor_email = 'placement-delete@example.com'`,
		f.companyID, label); n != 3 {
		t.Errorf("operator attribution on the member's audit rows = %d, want 3", n)
	}
}

func TestErasureLeavesOnlyAMinimalDeleteJob(t *testing.T) {
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	// A delivered job carrying the name, as every earlier upsert left behind.
	mustExec(t, `INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type, entity_id,
	                                    entity_external_id, payload, protocol_version, status)
	             SELECT d.site_id, d.id, 'UPDATE', 'PERSON', $1, $2,
	                    jsonb_build_object('member_id', $5::text, 'full_name', $3::text), 1, 'FAILED'
	               FROM devices d WHERE d.id = $4`, f.personID, f.externalID, erasedName, f.deviceID, f.externalID)

	f.deletePerson(t)

	if n := countWhere(t, `SELECT count(*) FROM sync_jobs WHERE payload::text LIKE '%Okonkwo%'`); n != 0 {
		t.Errorf("%d sync job(s) still carry the deleted member's name", n)
	}
	var payload string
	mustScan(t, `SELECT payload::text FROM sync_jobs
	              WHERE entity_type = 'PERSON' AND entity_id = `+itoa(f.personID)+`
	                AND job_type = 'DELETE' AND status = 'PENDING'`, &payload)
	if strings.Contains(payload, "full_name") || !strings.Contains(payload, f.externalID) {
		t.Errorf("DELETE payload = %s, want the member number and nothing personal", payload)
	}
}

func TestErasureScrubsAssistantMentions(t *testing.T) {
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	var userID int64
	mustScan(t, `SELECT id FROM users WHERE email = 'placement-delete@example.com'`, &userID)

	newConversation := func(text string) int64 {
		var id int64
		mustScan(t, `INSERT INTO assistant_conversations (company_id, user_id, model, prompt_hash)
		              VALUES (`+itoa(f.companyID)+`, `+itoa(userID)+`, 'm', repeat('a', 64)) RETURNING id`, &id)
		mustExec(t, `INSERT INTO assistant_messages (conversation_id, seq, role, content)
		             VALUES ($1, 1, 'user', jsonb_build_object('text', $2::text))`, id, text)
		return id
	}
	naming := newConversation("why was " + erasedName + " denied?")
	unrelated := newConversation("how many doors are offline?")
	mustExec(t, `INSERT INTO assistant_tool_calls (turn_id, company_id, user_id, session_id, tool_name,
	                                               arguments, route, status)
	             VALUES (gen_random_uuid(), $1, $2, 1, 'get_person',
	                     jsonb_build_object('external_id', $3::text), '/console/people/' || $3, 'EXECUTED')`,
		f.companyID, userID, f.externalID)

	f.deletePerson(t)

	if countWhere(t, `SELECT count(*) FROM assistant_conversations WHERE id = $1`, naming) != 0 {
		t.Error("a conversation naming the deleted member survived")
	}
	if countWhere(t, `SELECT count(*) FROM assistant_conversations WHERE id = $1`, unrelated) != 1 {
		t.Error("a conversation that named nobody was deleted")
	}
	if n := countWhere(t, `SELECT count(*) FROM assistant_tool_calls
	                        WHERE arguments::text LIKE '%P-DELETE%' OR route LIKE '%P-DELETE%'`); n != 0 {
		t.Errorf("%d tool call(s) still name the deleted member", n)
	}
}

func TestErasureLedgerHoldsNoPlaintext(t *testing.T) {
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	f.deletePerson(t)

	var hash, keyID string
	mustScan(t, `SELECT subject_hash, key_id FROM deleted_subjects WHERE company_id = `+itoa(f.companyID),
		&hash, &keyID)
	if len(hash) != 64 || strings.Contains(hash, "P-DELETE") {
		t.Errorf("ledger hash = %q, want a 64-character digest", hash)
	}
	if n := countWhere(t, `SELECT count(*) FROM deleted_subjects
	                        WHERE row_to_json(deleted_subjects)::text LIKE '%P-DELETE%'
	                           OR row_to_json(deleted_subjects)::text LIKE '%Okonkwo%'`); n != 0 {
		t.Error("the ledger holds the member number or name in plaintext")
	}
}

// ---------------------------------------------------------------------------
// Phase 2: deletion ordering and terminal convergence
// ---------------------------------------------------------------------------

// TestHardDeleteNeedsHistoryAnonymisedFirst proves the ordering problem the
// finaliser solves: a person still referenced by an event cannot simply be
// deleted, because the FK's SET NULL is an UPDATE the immutability trigger
// refuses.
func TestHardDeleteNeedsHistoryAnonymisedFirst(t *testing.T) {
	f := newDeletionFixture(t)
	f.uploadDoorEvent(t, f.externalID, time.Now().Add(-time.Minute))
	if countWhere(t, `SELECT count(*) FROM events WHERE person_id = $1`, f.personID) == 0 {
		t.Fatal("setup: the uploaded event is not linked to the person")
	}

	if _, err := database.DB.Exec(`DELETE FROM people WHERE id = $1`, f.personID); err == nil {
		t.Fatal("a person referenced by an immutable event was deleted directly; " +
			"the finaliser's ordering would be untested")
	}
}

func TestFinalisationWaitsForEveryTerminal(t *testing.T) {
	f := newDeletionFixture(t)
	f.uploadDoorEvent(t, f.externalID, time.Now().Add(-time.Minute))
	f.deletePerson(t)

	// The terminal still holds the finger: placement REMOVING.
	if n := finalize(t); n != 0 {
		t.Fatalf("finalised %d person(s) while a terminal still held the finger", n)
	}

	// It reports the removal but has not acknowledged the DELETE job.
	if res := f.reportRemoved(t); res.Code != http.StatusOK {
		t.Fatalf("REMOVED report = %d: %s", res.Code, res.Raw)
	}
	if n := finalize(t); n != 0 {
		t.Fatalf("finalised %d person(s) with a DELETE job undelivered", n)
	}

	f.completeDeleteJobs(t)
	if n := finalize(t); n != 1 {
		t.Fatalf("finalised %d person(s), want 1 once the terminal let go", n)
	}

	for table, query := range map[string]string{
		"people":                `SELECT count(*) FROM people WHERE id = $1`,
		"credentials":           `SELECT count(*) FROM credentials WHERE person_id = $1`,
		"credential_placements": `SELECT count(*) FROM credential_placements pl JOIN credentials c ON c.id = pl.credential_id WHERE c.person_id = $1`,
		"sync_jobs":             `SELECT count(*) FROM sync_jobs WHERE entity_type = 'PERSON' AND entity_id = $1`,
	} {
		if n := countWhere(t, query, f.personID); n != 0 {
			t.Errorf("%s still holds %d row(s) for the finalised person", table, n)
		}
	}
	if countWhere(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID) == 0 {
		t.Error("finalisation deleted door history; it must only anonymise it")
	}

	var expires time.Time
	mustScan(t, `SELECT expires_at FROM deleted_subjects WHERE company_id = `+itoa(f.companyID), &expires)
	if d := time.Until(expires); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("ledger expiry is %v away, want about 30 days", d)
	}

	// Finalising again finds nothing: idempotent.
	if n := finalize(t); n != 0 {
		t.Errorf("a second pass finalised %d", n)
	}
}

// A terminal that is deleted owes nothing; it does not hold the person hostage.
func TestFinalisationIgnoresADeletedTerminal(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	mustExec(t, `UPDATE devices SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1`, f.deviceID)
	if n := finalize(t); n != 1 {
		t.Errorf("finalised %d, want 1: a deleted terminal holds nobody", n)
	}
}

func TestRemovedReportAfterFinalisationIsAccepted(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	finalize(t)

	res := f.reportRemoved(t)
	if res.Code != http.StatusOK {
		t.Fatalf("a REMOVED report after finalisation = %d, want 200: %s", res.Code, res.Raw)
	}
	if countWhere(t, `SELECT count(*) FROM credentials WHERE company_id = $1`, f.companyID) != 0 {
		t.Error("the late REMOVED report re-created a credential for an erased person")
	}
}

// ---------------------------------------------------------------------------
// Late data from an offline terminal, and member-number reuse
// ---------------------------------------------------------------------------

func TestLateOfflineEventsAreStoredAnonymously(t *testing.T) {
	for _, stage := range []string{"erased", "finalised"} {
		t.Run(stage, func(t *testing.T) {
			f := newDeletionFixture(t)
			f.deletePerson(t)
			if stage == "finalised" {
				f.reportRemoved(t)
				f.completeDeleteJobs(t)
				if finalize(t) != 1 {
					t.Fatal("setup: not finalised")
				}
			}

			// Queued before the deletion, uploaded after it.
			f.uploadDoorEvent(t, f.externalID, time.Now().Add(-time.Minute))

			if n := countWhere(t, `SELECT count(*) FROM events WHERE subject_external_id = $1`, f.externalID); n != 0 {
				t.Errorf("a late event recreated %d identified event(s)", n)
			}
			if n := countWhere(t, `SELECT count(*) FROM access_logs WHERE person_external_id = $1`, f.externalID); n != 0 {
				t.Errorf("a late event recreated %d identified access log(s)", n)
			}
			if countWhere(t, `SELECT count(*) FROM events WHERE company_id = $1`, f.companyID) != 1 {
				t.Error("the late event was not stored at all; it must be kept, anonymously")
			}
		})
	}
}

// TestAReusedMemberNumberKeepsItsOwnHistory: a company gives the number to
// somebody new. Their events are theirs; the old member's late events are not.
func TestAReusedMemberNumberKeepsItsOwnHistory(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	beforeNewcomer := time.Now().Add(-30 * time.Second)

	time.Sleep(1100 * time.Millisecond) // created_at strictly after the deletion
	newcomer := seedPerson(t, f.companyID, f.externalID, "Newcomer")

	f.uploadDoorEvent(t, f.externalID, beforeNewcomer)
	// Event times travel at one-second precision; a second's gap keeps this
	// one unambiguously after the newcomer's creation.
	time.Sleep(1100 * time.Millisecond)
	f.uploadDoorEvent(t, f.externalID, time.Now())

	if n := countWhere(t, `SELECT count(*) FROM events WHERE subject_external_id = $1`, f.externalID); n != 1 {
		t.Errorf("identified events for the reused number = %d, want only the newcomer's 1", n)
	}

	// The ledger must not erase the newcomer, who was created after it.
	if n, err := database.ReplayDeletedSubjects(context.Background()); err != nil || n != 0 {
		t.Fatalf("replay erased %d (err %v); the newcomer is not the deleted member", n, err)
	}
	var name string
	mustScan(t, `SELECT full_name FROM people WHERE id = `+itoa(newcomer), &name)
	if name != "Newcomer" {
		t.Errorf("the newcomer was erased: name = %q", name)
	}
}

// ---------------------------------------------------------------------------
// Backup restore
// ---------------------------------------------------------------------------

// TestReplayErasesARowARestoreBroughtBack simulates restoring a backup taken
// before the deletion: the row comes back with its original created_at, and
// the ledger (carried across, per docs/erasure.md) deletes it again.
func TestReplayErasesARowARestoreBroughtBack(t *testing.T) {
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	var createdAt time.Time
	mustScan(t, `SELECT created_at FROM people WHERE id = `+itoa(f.personID), &createdAt)

	f.deletePerson(t)
	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	finalize(t)

	// The restore.
	mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, email, active, created_at)
	             VALUES ($1, $2, $3, 'MEMBER', 'ada@example.com', TRUE, $4)`,
		f.companyID, f.externalID, erasedName, createdAt)

	n, err := database.ReplayDeletedSubjects(context.Background())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 1 {
		t.Fatalf("replay erased %d, want the 1 restored row", n)
	}
	if c := countWhere(t, `SELECT count(*) FROM people
	                        WHERE company_id = $1 AND (full_name = $2 OR email IS NOT NULL OR deleted_at IS NULL)`,
		f.companyID, erasedName); c != 0 {
		t.Errorf("%d restored row(s) still hold the deleted member", c)
	}

	// Replay is idempotent.
	if n, _ := database.ReplayDeletedSubjects(context.Background()); n != 0 {
		t.Errorf("a second replay erased %d", n)
	}
}

// ---------------------------------------------------------------------------
// Company scoping
// ---------------------------------------------------------------------------

func TestErasureIsCompanyScoped(t *testing.T) {
	f := newDeletionFixture(t)
	other := operatorCompanyID(t, "two")
	twin := seedPerson(t, other, f.externalID, erasedName) // same number, other tenant
	mustExec(t, `INSERT INTO events (company_id, person_id, subject_external_id, event_type, decision, occurred_at)
	             VALUES ($1, $2, $3, 'ACCESS_GRANTED', 'GRANTED', CURRENT_TIMESTAMP)`, other, twin, f.externalID)
	mustExec(t, `INSERT INTO access_logs (company_id, person_id, person_external_id, granted, source, site_name, occurred_at)
	             VALUES ($1, $2, $3, TRUE, 'FINGERPRINT', 'Site C', CURRENT_TIMESTAMP)`, other, twin, f.externalID)
	mustExec(t, `INSERT INTO audit_events (company_id, action, target_type, target_label, changes)
	             VALUES ($1, 'PERSON_CREATED', 'PERSON', $2, jsonb_build_object('full_name', $3::text))`,
		other, f.externalID, erasedName)

	f.deletePerson(t)
	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	finalize(t)

	var name string
	mustScan(t, `SELECT full_name FROM people WHERE id = `+itoa(twin), &name)
	if name != erasedName {
		t.Errorf("the other company's person was touched: name = %q", name)
	}
	if countWhere(t, `SELECT count(*) FROM events WHERE company_id = $1 AND person_id = $2 AND subject_external_id = $3`,
		other, twin, f.externalID) != 1 {
		t.Error("the other company's door event lost its identity")
	}
	if countWhere(t, `SELECT count(*) FROM access_logs WHERE company_id = $1 AND person_external_id = $2`,
		other, f.externalID) != 1 {
		t.Error("the other company's access log lost its identity")
	}
	if countWhere(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND target_label = $2
	                   AND changes ? 'full_name'`, other, f.externalID) != 1 {
		t.Error("the other company's audit row was redacted")
	}
	erased, err := database.IsErasedSubject(other, f.externalID, time.Now().Add(-time.Hour))
	if err != nil || erased {
		t.Errorf("IsErasedSubject in the other company = %v (err %v), want false", erased, err)
	}
}

// ---------------------------------------------------------------------------
// The immutability trigger is still a trigger
// ---------------------------------------------------------------------------

func TestHistoryTablesStillRefuseEveryOtherEdit(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	var eventID, auditID int64
	mustScan(t, `INSERT INTO events (company_id, subject_external_id, event_type, decision, occurred_at)
	             VALUES (`+itoa(company)+`, 'X-1', 'ACCESS_DENIED', 'DENIED', CURRENT_TIMESTAMP) RETURNING id`, &eventID)
	mustScan(t, `INSERT INTO audit_events (company_id, action, actor_email)
	             VALUES (`+itoa(company)+`, 'SITE_CREATED', 'ops@example.com') RETURNING id`, &auditID)

	attempt := func(name, flag, query string, id int64) {
		tx, err := database.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if flag != "" {
			if _, err := tx.Exec(`SELECT set_config('accesslink.anonymizing', $1, true)`, flag); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(query, id); err == nil {
			t.Errorf("%s was allowed", name)
		}
	}

	attempt("editing a decision without the flag", "", `UPDATE events SET decision = 'GRANTED' WHERE id = $1`, eventID)
	attempt("editing a decision WITH the flag", "on", `UPDATE events SET decision = 'GRANTED' WHERE id = $1`, eventID)
	attempt("setting an identity WITH the flag", "on", `UPDATE events SET subject_external_id = 'Y-2' WHERE id = $1`, eventID)
	attempt("editing an audit action WITH the flag", "on", `UPDATE audit_events SET action = 'NOTHING' WHERE id = $1`, auditID)
	attempt("anonymising without the flag", "", `UPDATE events SET subject_external_id = NULL WHERE id = $1`, eventID)
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func TestDeletingAnOperatorPseudonymisesThem(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	_, ownerToken, ownerCSRF := consoleOperatorSession(t, env.router, one, "owner@example.com", models.RoleOwner)
	leaver, _, _ := consoleOperatorSession(t, env.router, one, "leaver@example.com", models.RoleManager)
	// Addresses are unique across companies, so "another company's operator"
	// is a different account; what must hold is that its rows are untouched.
	twin := mustCreateOperator(t, two, "stayer@example.com", models.RoleManager)

	mustExec(t, `INSERT INTO audit_events (company_id, actor_user_id, actor_email, ip_address, user_agent, action)
	             VALUES ($1, $2, 'leaver@example.com', '203.0.113.9', 'Browser', 'SITE_CREATED')`, one, leaver.ID)
	mustExec(t, `INSERT INTO audit_events (company_id, actor_user_id, actor_email, ip_address, action)
	             VALUES ($1, $2, 'stayer@example.com', '203.0.113.9', 'SITE_CREATED')`, two, twin.ID)
	mustExec(t, `INSERT INTO api_credentials (company_id, name, key_hash, key_prefix, scopes, created_by, created_by_email)
	             VALUES ($1, 'k', repeat('a', 64), 'atp_live_0000aaaa', ARRAY['members:read'], $2, 'leaver@example.com')`,
		one, leaver.ID)

	code, body := consoleCall(t, env.router, "DELETE", "/api/v1/console/operators/"+leaver.PublicID,
		"", ownerToken, ownerCSRF)
	if code != http.StatusNoContent {
		t.Fatalf("deleting the operator = %d: %v", code, body)
	}

	pseudonym := database.ErasedOperatorLabel(leaver.PublicID)
	if countWhere(t, `SELECT count(*) FROM users WHERE id = $1`, leaver.ID) != 0 {
		t.Error("the operator row survived")
	}
	if countWhere(t, `SELECT count(*) FROM user_sessions WHERE user_id = $1`, leaver.ID) != 0 {
		t.Error("the operator's sessions survived")
	}
	if n := countWhere(t, `SELECT count(*) FROM audit_events
	                        WHERE company_id = $1 AND (actor_email = 'leaver@example.com'
	                           OR target_label = 'leaver@example.com')`, one); n != 0 {
		t.Errorf("%d audit row(s) in the company still name the operator", n)
	}
	if countWhere(t, `SELECT count(*) FROM audit_events
	                   WHERE company_id = $1 AND actor_email = $2 AND ip_address IS NULL AND user_agent IS NULL`,
		one, pseudonym) != 1 {
		t.Error("the operator's own audit row was not pseudonymised (actor, address, browser)")
	}
	if countWhere(t, `SELECT count(*) FROM audit_events
	                   WHERE company_id = $1 AND action = 'OPERATOR_DELETED' AND target_label = $2`, one, pseudonym) != 1 {
		t.Error("the OPERATOR_DELETED record does not use the pseudonym")
	}
	if countWhere(t, `SELECT count(*) FROM api_credentials WHERE created_by_email = $1`, pseudonym) != 1 {
		t.Error("the copied creator address on an API key was not replaced")
	}

	// Another company's operator is untouched.
	if countWhere(t, `SELECT count(*) FROM users WHERE id = $1`, twin.ID) != 1 ||
		countWhere(t, `SELECT count(*) FROM audit_events WHERE company_id = $1 AND actor_email = 'stayer@example.com'
		                 AND actor_user_id = $2 AND ip_address IS NOT NULL`, two, twin.ID) != 1 {
		t.Error("deleting an operator touched another company's operator")
	}
}

// ---------------------------------------------------------------------------
// Rows deleted before 039
// ---------------------------------------------------------------------------

func TestLegacySoftDeletedRowsAreErased(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	person := seedPerson(t, one, "P-OLD", erasedName)
	mustExec(t, `UPDATE people SET email = 'old@example.com', deleted_at = CURRENT_TIMESTAMP WHERE id = $1`, person)
	operator := mustCreateOperator(t, one, "old-op@example.com", models.RoleViewer)
	mustExec(t, `UPDATE users SET deleted_at = CURRENT_TIMESTAMP, active = FALSE WHERE id = $1`, operator.ID)
	mustExec(t, `INSERT INTO audit_events (company_id, actor_user_id, actor_email, action)
	             VALUES ($1, $2, 'old-op@example.com', 'SITE_CREATED')`, one, operator.ID)

	people, err := database.EraseLegacyDeletedPeople(context.Background())
	if err != nil || people != 1 {
		t.Fatalf("legacy people erased = %d (err %v), want 1", people, err)
	}
	operators, err := database.EraseLegacyDeletedOperators(context.Background())
	if err != nil || operators != 1 {
		t.Fatalf("legacy operators erased = %d (err %v), want 1", operators, err)
	}

	var name string
	var email *string
	mustScan(t, `SELECT full_name, email FROM people WHERE id = `+itoa(person), &name, &email)
	if name != "" || email != nil {
		t.Errorf("legacy person still holds name %q / email %v", name, email)
	}
	if countWhere(t, `SELECT count(*) FROM audit_events WHERE actor_email = 'old-op@example.com'`) != 0 {
		t.Error("the legacy operator is still named in the audit trail")
	}

	// A second sweep finds nothing.
	if n, _ := database.EraseLegacyDeletedPeople(context.Background()); n != 0 {
		t.Errorf("a second legacy sweep erased %d people", n)
	}
	if n, _ := database.EraseLegacyDeletedOperators(context.Background()); n != 0 {
		t.Errorf("a second legacy sweep erased %d operators", n)
	}
}

// ---------------------------------------------------------------------------
// The ledger's own lifetime, and the purges 039 wired up
// ---------------------------------------------------------------------------

func TestLedgerEntriesExpireOnlyAfterFinalisation(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	mustExec(t, `UPDATE deleted_subjects SET deleted_at = CURRENT_TIMESTAMP - interval '5 years'`)

	if n, err := database.PurgeExpiredDeletedSubjects(context.Background()); err != nil || n != 0 {
		t.Fatalf("purged %d unfinalised ledger entr(ies) (err %v); an offline terminal may still upload", n, err)
	}

	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	finalize(t)
	mustExec(t, `UPDATE deleted_subjects SET expires_at = CURRENT_TIMESTAMP - interval '1 second'`)
	if n, err := database.PurgeExpiredDeletedSubjects(context.Background()); err != nil || n != 1 {
		t.Fatalf("purged %d expired entr(ies) (err %v), want 1", n, err)
	}
}

func TestRetiredAPICredentialsArePurged(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	insert := func(prefix, state string) {
		mustExec(t, `INSERT INTO api_credentials (company_id, name, key_hash, key_prefix, scopes,
		                                          revoked_at, revoked_reason, expires_at, created_at)
		             VALUES ($1, $3, repeat($2, 64), $3, ARRAY['members:read'],
		                     CASE WHEN $4 = 'revoked-old' THEN CURRENT_TIMESTAMP - interval '200 days'
		                          WHEN $4 = 'revoked-new' THEN CURRENT_TIMESTAMP - interval '10 days' END,
		                     CASE WHEN $4 LIKE 'revoked-%' THEN 'test' END,
		                     CASE WHEN $4 = 'expired-old' THEN CURRENT_TIMESTAMP - interval '200 days'
		                          ELSE CURRENT_TIMESTAMP + interval '200 days' END,
		                     CURRENT_TIMESTAMP - interval '400 days')`,
			company, prefix[len(prefix)-1:], prefix, state)
	}
	insert("atp_live_0000000a", "revoked-old")
	insert("atp_live_0000000b", "revoked-new")
	insert("atp_live_0000000c", "expired-old")
	insert("atp_live_0000000d", "live")

	n, err := database.PurgeRetiredAPICredentials(context.Background(), 90)
	if err != nil || n != 2 {
		t.Fatalf("purged %d (err %v), want the 2 retired more than 90 days ago", n, err)
	}
	if countWhere(t, `SELECT count(*) FROM api_credentials WHERE key_prefix IN ('atp_live_0000000b','atp_live_0000000d')`) != 2 {
		t.Error("a recently revoked or a live key was purged")
	}
}

func TestAssistantRecordsArePurgedWithTheirWindow(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	user := mustCreateOperator(t, company, "ai@example.com", models.RoleOwner)
	for _, age := range []string{"60 days", "1 day"} {
		mustExec(t, `INSERT INTO assistant_tool_calls (turn_id, company_id, user_id, session_id, tool_name,
		                                               arguments, status, created_at)
		             VALUES (gen_random_uuid(), $1, $2, 1, 't', '{}'::jsonb, 'EXECUTED',
		                     CURRENT_TIMESTAMP - $3::interval)`, company, user.ID, age)
	}
	n, err := database.PurgeAssistantRecordsContext(context.Background(), 30)
	if err != nil || n != 1 {
		t.Fatalf("purged %d (err %v), want the one 60-day-old tool call", n, err)
	}
}

// ---------------------------------------------------------------------------
// Request logs
// ---------------------------------------------------------------------------

func TestRequestLogsRecordTheRouteNotTheURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)

	r := gin.New()
	r.Use(middleware.LoggingMiddleware())
	r.GET("/api/v1/console/people/:external_id", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/console/people/MEMBER-4471", nil))

	line := buf.String()
	if strings.Contains(line, "MEMBER-4471") {
		t.Errorf("the request log names the member: %s", line)
	}
	if !strings.Contains(line, "/api/v1/console/people/:external_id") {
		t.Errorf("the request log does not carry the route: %s", line)
	}
}

// ---------------------------------------------------------------------------
// The ledger key: unset, rotated, lost
// ---------------------------------------------------------------------------

// freshLedgerKeys makes the ledger reload its keys under the environment the
// test sets, and again afterwards so no later test inherits them.
func freshLedgerKeys(t *testing.T) {
	t.Helper()
	database.ResetLedgerKeyCacheForTests()
	t.Cleanup(database.ResetLedgerKeyCacheForTests)
}

func randomLedgerKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func ledgerKeyIDs(t *testing.T, companyID int64) []string {
	t.Helper()
	rows, err := database.DB.Query(`SELECT key_id FROM deleted_subjects WHERE company_id = $1 ORDER BY id`, companyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

// With the development opt-in and no key, a key is generated once and stored
// in the database -- and is then stable: a second deletion uses the same one.
func TestLedgerKeyUnsetUsesOneStoredKey(t *testing.T) {
	t.Setenv("DELETION_LEDGER_KEY", "")
	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", "")
	t.Setenv("DELETION_LEDGER_ALLOW_STORED_KEY", "true")
	freshLedgerKeys(t)
	f := newDeletionFixture(t)
	seedPerson(t, f.companyID, "P-SECOND", "Second")

	f.deletePerson(t)
	if err := database.DeleteMember(f.companyID, "P-SECOND"); err != nil {
		t.Fatal(err)
	}
	ids := ledgerKeyIDs(t, f.companyID)
	if len(ids) != 2 || ids[0] != ids[1] || !strings.HasPrefix(ids[0], "db-") {
		t.Errorf("ledger key ids = %v, want two entries under one database-held key", ids)
	}
	if n := countWhere(t, `SELECT count(*) FROM deletion_ledger_keys`); n < 1 {
		t.Error("no key was stored")
	}
}

// Setting a key AFTER entries exist under the stored one: new entries use the
// new key, and the old entries still match.
func TestSettingALedgerKeyKeepsOlderEntriesMatchable(t *testing.T) {
	t.Setenv("DELETION_LEDGER_KEY", "")
	t.Setenv("DELETION_LEDGER_ALLOW_STORED_KEY", "true")
	freshLedgerKeys(t)
	f := newDeletionFixture(t)
	f.deletePerson(t)

	// A key is configured and the opt-in withdrawn: the stored key is no
	// longer written with, but is still matched.
	t.Setenv("DELETION_LEDGER_KEY", randomLedgerKey(t))
	t.Setenv("DELETION_LEDGER_ALLOW_STORED_KEY", "")
	freshLedgerKeys(t)

	erased, err := database.IsErasedSubject(f.companyID, f.externalID, time.Now().Add(-time.Hour))
	if err != nil || !erased {
		t.Errorf("an entry under the stored key stopped matching once a key was set: %v (err %v)", erased, err)
	}
	if n, err := database.UnmatchableLedgerEntries(context.Background()); err != nil || n != 0 {
		t.Errorf("unmatchable entries = %d (err %v), want 0", n, err)
	}

	seedPerson(t, f.companyID, "P-LATER", "Later")
	if err := database.DeleteMember(f.companyID, "P-LATER"); err != nil {
		t.Fatal(err)
	}
	ids := ledgerKeyIDs(t, f.companyID)
	if len(ids) != 2 || !strings.HasPrefix(ids[1], "env-") {
		t.Errorf("ledger key ids = %v, want the newer entry under the configured key", ids)
	}
}

// Replacing the configured key WITHOUT keeping the old one is detected, not
// silent; putting the old key in DELETION_LEDGER_PREVIOUS_KEYS restores it.
func TestALostLedgerKeyIsDetectedAndRecoverable(t *testing.T) {
	original := randomLedgerKey(t)
	t.Setenv("DELETION_LEDGER_KEY", original)
	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", "")
	freshLedgerKeys(t)
	f := newDeletionFixture(t)
	f.deletePerson(t)

	t.Setenv("DELETION_LEDGER_KEY", randomLedgerKey(t))
	freshLedgerKeys(t)
	if n, err := database.UnmatchableLedgerEntries(context.Background()); err != nil || n != 1 {
		t.Fatalf("unmatchable entries after losing the key = %d (err %v), want 1", n, err)
	}

	// The erasure task fails while an entry is unmatchable.
	task := erasureTask(t)
	if _, err := task(context.Background()); err == nil {
		t.Error("the erasure pass succeeded with a ledger entry no key can match")
	}

	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", original)
	freshLedgerKeys(t)
	if n, _ := database.UnmatchableLedgerEntries(context.Background()); n != 0 {
		t.Errorf("unmatchable entries with the old key restored = %d, want 0", n)
	}
	erased, err := database.IsErasedSubject(f.companyID, f.externalID, time.Now().Add(-time.Hour))
	if err != nil || !erased {
		t.Errorf("the entry does not match with the old key restored: %v (err %v)", erased, err)
	}
	if _, err := task(context.Background()); err != nil {
		t.Errorf("the erasure pass still fails with the old key restored: %v", err)
	}
}

// erasureTask is the maintenance task as configured, so the test drives the
// real pass rather than a copy of it.
func erasureTask(t *testing.T) func(context.Context) (string, error) {
	t.Helper()
	for _, task := range maintenance.LoadConfig().Tasks() {
		if task.Name == "erasure" {
			return task.Run
		}
	}
	t.Fatal("no erasure task configured")
	return nil
}

func TestANeverExpiringGraceKeepsTheEntry(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)
	f.reportRemoved(t)
	f.completeDeleteJobs(t)
	if n, err := database.FinalizeErasedPeople(context.Background(), 0); err != nil || n != 1 {
		t.Fatalf("finalised %d (err %v), want 1", n, err)
	}
	var expires *time.Time
	mustScan(t, `SELECT expires_at FROM deleted_subjects WHERE company_id = `+itoa(f.companyID), &expires)
	if expires != nil {
		t.Errorf("grace 0 set an expiry (%v); it must mean never", *expires)
	}
	if n, _ := database.PurgeExpiredDeletedSubjects(context.Background()); n != 0 {
		t.Errorf("a never-expiring entry was purged")
	}
}

// Without a key and without the development opt-in, the API must not start
// and a deletion must not happen: nothing is generated, nothing is erased into
// a ledger that could not recognise the person again.
func TestTheLedgerKeyIsRequired(t *testing.T) {
	t.Setenv("DELETION_LEDGER_KEY", "")
	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", "")
	t.Setenv("DELETION_LEDGER_ALLOW_STORED_KEY", "")
	freshLedgerKeys(t)
	f := newDeletionFixture(t)
	f.giveContactDetails(t)
	keysBefore := countWhere(t, `SELECT count(*) FROM deletion_ledger_keys`)

	if err := database.CheckLedgerKeyConfig(); !errors.Is(err, database.ErrLedgerKeyRequired) {
		t.Errorf("startup check without a key = %v, want ErrLedgerKeyRequired", err)
	}

	if err := database.DeleteMember(f.companyID, f.externalID); !errors.Is(err, database.ErrLedgerKeyRequired) {
		t.Fatalf("deleting without a key = %v, want ErrLedgerKeyRequired", err)
	}
	// The whole deletion rolled back: the person is untouched and live.
	var name string
	var deleted *time.Time
	mustScan(t, `SELECT full_name, deleted_at FROM people WHERE id = `+itoa(f.personID), &name, &deleted)
	if name != erasedName || deleted != nil {
		t.Errorf("a deletion without a key changed the person: name %q, deleted_at %v", name, deleted)
	}
	if got := countWhere(t, `SELECT count(*) FROM deletion_ledger_keys`); got != keysBefore {
		t.Errorf("a key was generated without the opt-in (%d -> %d)", keysBefore, got)
	}

	// Through the console it is a server error, not a silent success.
	code, _ := consoleCall(t, f.env.router, "DELETE", "/api/v1/console/people/"+f.externalID,
		"", f.token, f.csrf)
	if code != http.StatusInternalServerError {
		t.Errorf("console delete without a key = %d, want 500", code)
	}
}

// A key that is set but unusable refuses startup, and the error never carries
// the value.
func TestAnUnusableLedgerKeyRefusesStartup(t *testing.T) {
	const bad = "not-a-valid-key"
	t.Setenv("DELETION_LEDGER_KEY", bad)
	t.Setenv("DELETION_LEDGER_ALLOW_STORED_KEY", "true") // the opt-in does not rescue a bad key
	freshLedgerKeys(t)
	err := database.CheckLedgerKeyConfig()
	if err == nil {
		t.Fatal("an unusable DELETION_LEDGER_KEY passed the startup check")
	}
	if strings.Contains(err.Error(), bad) {
		t.Error("the startup error contains the key value")
	}

	t.Setenv("DELETION_LEDGER_KEY", randomLedgerKey(t))
	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", randomLedgerKey(t)+","+bad)
	freshLedgerKeys(t)
	err = database.CheckLedgerKeyConfig()
	if err == nil {
		t.Fatal("a malformed DELETION_LEDGER_PREVIOUS_KEYS entry passed the startup check")
	}
	if strings.Contains(err.Error(), bad) {
		t.Error("the startup error contains a key value")
	}

	t.Setenv("DELETION_LEDGER_PREVIOUS_KEYS", "")
	freshLedgerKeys(t)
	if err := database.CheckLedgerKeyConfig(); err != nil {
		t.Errorf("a valid key failed the startup check: %v", err)
	}
}

func TestDeletedSubjectGraceDefaultsToNever(t *testing.T) {
	t.Setenv("DELETED_SUBJECT_GRACE_DAYS", "")
	if got := maintenance.LoadConfig().DeletedSubjectGraceDays; got != 0 {
		t.Errorf("DELETED_SUBJECT_GRACE_DAYS default = %d, want 0 (never expire)", got)
	}
}

// ---------------------------------------------------------------------------
// Rows deleted before 039: the two things the legacy path must not lose
// ---------------------------------------------------------------------------

// softDeleteTheOldWay deletes P-DELETE as the platform did before 039: the
// row is only marked, the DELETE job is queued with the full payload, and the
// terminal has not collected it yet.
func (f *deletionFixture) softDeleteTheOldWay(t *testing.T) {
	t.Helper()
	f.giveContactDetails(t)
	mustExec(t, `UPDATE people SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1`, f.personID)
	mustExec(t, `UPDATE credential_placements SET state = 'REMOVING'
	              WHERE credential_id = (SELECT id FROM credentials WHERE public_id = $1::uuid)`, f.credential)
	mustExec(t, `INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type, entity_id,
	                                    entity_external_id, payload, protocol_version, status)
	             SELECT d.site_id, d.id, 'DELETE', 'PERSON', $1, $2,
	                    jsonb_build_object('member_id', $5::text, 'full_name', $3::text, 'deleted', TRUE),
	                    1, 'PENDING'
	               FROM devices d WHERE d.id = $4`, f.personID, f.externalID, erasedName, f.deviceID, f.externalID)
}

// TestLegacyErasureKeepsAnUndeliveredDeleteJob: an offline terminal's only
// instruction to forget a person deleted before 039 survives their erasure,
// stripped to the member number -- and finalisation waits for it.
func TestLegacyErasureKeepsAnUndeliveredDeleteJob(t *testing.T) {
	f := newDeletionFixture(t)
	f.softDeleteTheOldWay(t)

	if n, err := database.EraseLegacyDeletedPeople(context.Background()); err != nil || n != 1 {
		t.Fatalf("legacy erasure = %d (err %v), want 1", n, err)
	}

	var payload string
	mustScan(t, `SELECT payload::text FROM sync_jobs
	              WHERE entity_type = 'PERSON' AND entity_id = `+itoa(f.personID)+`
	                AND job_type = 'DELETE' AND status = 'PENDING'`, &payload)
	if strings.Contains(payload, "full_name") || strings.Contains(payload, "Okonkwo") {
		t.Errorf("the kept DELETE job still carries the name: %s", payload)
	}
	if !strings.Contains(payload, f.externalID) {
		t.Errorf("the kept DELETE job lost the member number: %s", payload)
	}

	// The terminal reports the removal, but has not collected the DELETE:
	// the person is not finalised while that instruction is outstanding.
	f.reportRemoved(t)
	if n := finalize(t); n != 0 {
		t.Fatalf("finalised %d with the DELETE job undelivered", n)
	}

	// The terminal collects it -- and gets the member number, nothing more.
	jobs := pollJobs(t, f.env, f.deviceKey)
	delivered := false
	for _, job := range jobs {
		if job["job_type"] == "DELETE" && job["entity_external_id"] == f.externalID {
			delivered = true
		}
		id, _ := job["id"].(float64)
		ackJob(t, f.env, f.deviceKey, id, map[string]any{"status": "COMPLETED"})
	}
	if !delivered {
		t.Fatalf("the terminal was not given the DELETE: %v", jobs)
	}
	if n := finalize(t); n != 1 {
		t.Errorf("finalised %d once the DELETE was acknowledged, want 1", n)
	}
}

// TestLegacyErasureDestroysSealedMaterial: a person deleted before 039 loses
// their sealed template on the legacy path exactly as a delete made now does.
func TestLegacyErasureDestroysSealedMaterial(t *testing.T) {
	f := newDeletionFixture(t)
	giveSealedMaterial(t, f.credential, digestOne)
	f.softDeleteTheOldWay(t)

	if n, err := database.EraseLegacyDeletedPeople(context.Background()); err != nil || n != 1 {
		t.Fatalf("legacy erasure = %d (err %v), want 1", n, err)
	}
	if s := sealedStateOf(t, f.credential); s.holdsAnyMaterial() {
		t.Errorf("a legacy-deleted person's credential still holds sealed material: %+v", s)
	}
}
