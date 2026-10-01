package database

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"access-terminal-cloud-api/models"
)

// Erasure: what deleting a member or an operator actually removes (039).
//
// ---------------------------------------------------------------------------
// THE SHAPE, IN TWO PHASES
// ---------------------------------------------------------------------------
//
// A member is deleted IMMEDIATELY and IRREVERSIBLY as far as their personal
// data goes: in the delete's own transaction their name, email, phone and
// category go, their door history and audit trail lose their identity, their
// sealed template is destroyed, their old sync jobs (which carry their name)
// are removed and a DELETE goes to every terminal. What is left is one row
// holding an internal id and the member number -- the minimum the terminals
// need to report that they have let go.
//
// The row itself is deleted later, by FinalizeErasedPeople, once every terminal
// that held the person has confirmed the removal. Deleting it sooner would
// leave a terminal's REMOVED report with nothing to resolve against, and the
// console with no way to say "a deleted person is still on that door" -- which
// is a security fact somebody needs to see.
//
// ---------------------------------------------------------------------------
// THE LEDGER
// ---------------------------------------------------------------------------
//
// Two things bring a deleted member's number back after the deletion:
//
//   * An OFFLINE TERMINAL uploads door events it queued before it heard about
//     the deletion. Without the ledger they would be stored against the number
//     and recreate exactly the history the deletion removed.
//   * A RESTORED BACKUP brings the whole row back.
//
// deleted_subjects holds an HMAC of (company, member number) -- never the
// number -- so both can be recognised.
//
// THE KEY IS REQUIRED. It comes from DELETION_LEDGER_KEY, and the API refuses
// to start without a usable one (CheckLedgerKeyConfig). A key generated and
// kept in the database beside the hashes would protect little -- a member
// number is short enough that anybody holding both can test candidates -- and
// a key that appeared silently could as silently be replaced, leaving every
// earlier entry unable to match. Generating one is therefore an explicit
// development-only opt-in, DELETION_LEDGER_ALLOW_STORED_KEY=true.

// ErasedPersonLabel is what a deleted member becomes in the audit trail: stable
// per person, so a reviewer can still follow one person's history, and
// meaningless without the row that is about to be deleted.
func ErasedPersonLabel(publicID string) string { return "deleted-person:" + publicID }

// ErasedOperatorLabel is the same for an operator.
func ErasedOperatorLabel(publicID string) string { return "deleted-operator:" + publicID }

// ---------------------------------------------------------------------------
// The ledger key
// ---------------------------------------------------------------------------

type ledgerKey struct {
	id     string
	secret []byte
}

var (
	ledgerKeyMu    sync.Mutex
	ledgerKeyCache []ledgerKey // [0] is the one new entries are written under
)

// resetLedgerKeyCache forgets the loaded keys. Tests rebuild the database, and
// a key cached from the previous one would hash against a table that is gone.
func resetLedgerKeyCache() {
	ledgerKeyMu.Lock()
	ledgerKeyCache = nil
	ledgerKeyMu.Unlock()
}

// ResetLedgerKeyCacheForTests is resetLedgerKeyCache, exported for the
// integration suite.
func ResetLedgerKeyCacheForTests() { resetLedgerKeyCache() }

func keyIDFor(prefix string, secret []byte) string {
	sum := sha256.Sum256(secret)
	return prefix + hex.EncodeToString(sum[:])[:8]
}

// decodeLedgerKey parses one base64 key of at least 32 bytes.
func decodeLedgerKey(raw string) (ledgerKey, bool) {
	secret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(secret) < 32 {
		return ledgerKey{}, false
	}
	return ledgerKey{id: keyIDFor("env-", secret), secret: secret}, true
}

// ErrLedgerKeyRequired is a deletion attempted with no usable ledger key. It
// fails the deletion -- the whole transaction rolls back -- rather than erase a
// person the platform could then not recognise again.
var ErrLedgerKeyRequired = errors.New("deletion ledger key not configured: set DELETION_LEDGER_KEY " +
	"to base64 of at least 32 random bytes (docs/erasure.md)")

// allowStoredLedgerKey is the development-only opt-in to a generated key.
func allowStoredLedgerKey() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DELETION_LEDGER_ALLOW_STORED_KEY")))
	return v == "true" || v == "1"
}

// envLedgerKey reads DELETION_LEDGER_KEY: base64, at least 32 bytes. Set but
// unusable is an error, never a fallback: somebody configured it and got it
// wrong, and writing under anything else would be the silent substitution the
// requirement exists to prevent.
func envLedgerKey() (ledgerKey, bool, error) {
	raw := strings.TrimSpace(os.Getenv("DELETION_LEDGER_KEY"))
	if raw == "" {
		return ledgerKey{}, false, nil
	}
	key, ok := decodeLedgerKey(raw)
	if !ok {
		return ledgerKey{}, false, errors.New("DELETION_LEDGER_KEY is set but is not base64 " +
			"of at least 32 bytes")
	}
	return key, true, nil
}

// CheckLedgerKeyConfig is the startup check: nil when deletions can be
// recorded and recognised, an error saying what to fix otherwise. main()
// refuses to start on an error. Never includes a key value.
func CheckLedgerKeyConfig() error {
	_, haveEnv, err := envLedgerKey()
	if err != nil {
		return err
	}
	if !haveEnv && !allowStoredLedgerKey() {
		return ErrLedgerKeyRequired
	}
	_, err = previousLedgerKeys()
	return err
}

// previousLedgerKeys reads DELETION_LEDGER_PREVIOUS_KEYS: comma-separated
// keys that entries may have been written under and must still be MATCHED
// against, but are never written with again.
//
// ROTATION WITHOUT LOSS. An entry stores only the hash and the id of the key
// that made it; nobody can re-hash it under a new key, because nobody holds
// the member number. So a rotated-out key has to stay readable for as long as
// any entry made under it is alive, or those entries silently stop matching
// and the people they record can come back. A malformed value is reported and
// skipped, never fatal.
func previousLedgerKeys() ([]ledgerKey, error) {
	var keys []ledgerKey
	for i, raw := range strings.Split(os.Getenv("DELETION_LEDGER_PREVIOUS_KEYS"), ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		key, ok := decodeLedgerKey(raw)
		if !ok {
			return nil, fmt.Errorf("DELETION_LEDGER_PREVIOUS_KEYS entry %d is not base64 of at "+
				"least 32 bytes", i+1)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// ledgerKeys returns every key the ledger may have been written under, the
// one to write new entries under first. Loaded once per process.
func ledgerKeys(q queryer) ([]ledgerKey, error) {
	ledgerKeyMu.Lock()
	defer ledgerKeyMu.Unlock()
	if ledgerKeyCache != nil {
		return ledgerKeyCache, nil
	}

	var keys []ledgerKey
	env, haveEnv, err := envLedgerKey()
	if err != nil {
		return nil, err
	}
	if haveEnv {
		keys = append(keys, env)
	} else if !allowStoredLedgerKey() {
		return nil, ErrLedgerKeyRequired
	}
	previous, err := previousLedgerKeys()
	if err != nil {
		return nil, err
	}

	// Keys stored by a development install are still MATCHED -- an entry
	// written under one must keep working -- but are written with only under
	// the explicit opt-in above.
	stored, err := loadStoredLedgerKeys(q)
	if err != nil {
		return nil, err
	}
	if !haveEnv && len(stored) == 0 {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generating the deletion ledger key: %w", err)
		}
		id := keyIDFor("db-", secret)
		if _, err := q.Exec(`INSERT INTO deletion_ledger_keys (key_id, secret) VALUES ($1, $2)
		                     ON CONFLICT (key_id) DO NOTHING`, id, secret); err != nil {
			return nil, fmt.Errorf("storing the deletion ledger key: %w", err)
		}
		if stored, err = loadStoredLedgerKeys(q); err != nil {
			return nil, err
		}
		log.Printf("WARNING: deletion ledger: DELETION_LEDGER_ALLOW_STORED_KEY is set and no " +
			"DELETION_LEDGER_KEY is; generated a key held in the database. Development only.")
	}
	keys = append(keys, stored...)
	keys = append(keys, previous...)

	ledgerKeyCache = keys
	return keys, nil
}

// UnmatchableLedgerEntries counts ledger entries written under a key this
// process does not hold.
//
// THE SIGNAL THAT A KEY WAS LOST. Such an entry can no longer recognise its
// person: a late upload is stored with the number, and a restore is not
// replayed. Nothing can repair it -- the number is not kept -- so the only
// defence is to say so, loudly and every pass, until the missing key is put
// back (DELETION_LEDGER_KEY or DELETION_LEDGER_PREVIOUS_KEYS). The erasure task
// fails while this is non-zero, which is what makes it visible on the health
// endpoint rather than in one log line.
func UnmatchableLedgerEntries(ctx context.Context) (int64, error) {
	keys, err := ledgerKeys(DB)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.id)
	}
	var n int64
	err = DB.QueryRowContext(ctx, `
		SELECT count(*) FROM deleted_subjects WHERE NOT (key_id = ANY($1::text[]))`,
		pqStringArray(ids)).Scan(&n)
	return n, err
}

func loadStoredLedgerKeys(q queryer) ([]ledgerKey, error) {
	rows, err := q.Query(`SELECT key_id, secret FROM deletion_ledger_keys ORDER BY created_at, key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []ledgerKey
	for rows.Next() {
		var k ledgerKey
		if err := rows.Scan(&k.id, &k.secret); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// subjectHash is HMAC-SHA256(key, company || 0x00 || member number).
func subjectHash(key ledgerKey, companyID int64, externalID string) string {
	mac := hmac.New(sha256.New, key.secret)
	mac.Write([]byte(strconv.FormatInt(companyID, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.TrimSpace(externalID)))
	return hex.EncodeToString(mac.Sum(nil))
}

// queryer is what both *sql.DB and *sql.Tx offer.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ---------------------------------------------------------------------------
// Phase 0: erasing a member, inside the delete's transaction
// ---------------------------------------------------------------------------

// erasedMember is what the delete needs to know about the row it is erasing.
type erasedMember struct {
	ID         int64
	PublicID   string
	ExternalID string
	FullName   string
}

// erasePersonTx removes everything personal about one member except the
// member number on their own row, and records them in the ledger.
//
// Scoped by company throughout. Idempotent: running it twice over the same
// person finds nothing left to remove and writes one more ledger entry, which
// matches the same hash and changes nothing.
func erasePersonTx(tx *sql.Tx, companyID int64, m erasedMember) error {
	// The sealed template first, on every route into erasure -- a delete made
	// now and one made before 039 alike.
	if err := destroySealedMaterialTx(tx, companyID, m.ID); err != nil {
		return fmt.Errorf("destroying sealed material: %w", err)
	}

	steps := []struct {
		what  string
		query string
		args  []any
	}{
		// Rules naming the person, and enrolments waiting for them. Both
		// would cascade at finalisation; going now means a deleted person
		// holds no permission in the meantime.
		{"permissions", `DELETE FROM permissions WHERE company_id = $1 AND person_id = $2`,
			[]any{companyID, m.ID}},
		{"enrolment requests", `DELETE FROM enrollment_requests WHERE person_id = $1`,
			[]any{m.ID}},

		// Delivered and failed person jobs carry the person's name in their
		// payload; pending upserts are superseded by the DELETE. A PENDING
		// DELETE is NOT removed: for somebody deleted before 039 it is the
		// only instruction an offline terminal will ever get to forget them,
		// and nothing re-queues it. It keeps its row, and -- like a job a
		// terminal has already taken, whose acknowledgement has to find it --
		// loses everything in the payload but the number.
		{"finished sync jobs", `
			DELETE FROM sync_jobs
			 WHERE entity_type = 'PERSON' AND entity_id = $1
			   AND (status IN ('COMPLETED', 'FAILED', 'CANCELLED')
			        OR (status = 'PENDING' AND job_type <> 'DELETE'))`,
			[]any{m.ID}},
		{"in-flight and undelivered DELETE jobs", `
			UPDATE sync_jobs
			   SET payload = jsonb_build_object('member_id', entity_external_id, 'deleted', TRUE)
			 WHERE entity_type = 'PERSON' AND entity_id = $1
			   AND (status = 'IN_PROGRESS' OR (status = 'PENDING' AND job_type = 'DELETE'))`,
			[]any{m.ID}},

		// The person row keeps the member number and nothing else of theirs.
		{"person row", `
			UPDATE people
			   SET full_name = '', membership_type = '', email = NULL, phone = NULL,
			       valid_from = NULL, valid_until = NULL, category_id = NULL,
			       fingerprint_template = NULL, active = FALSE,
			       deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP),
			       updated_at = CURRENT_TIMESTAMP
			 WHERE id = $1 AND company_id = $2`,
			[]any{m.ID, companyID}},
	}
	for _, s := range steps {
		if _, err := tx.Exec(s.query, s.args...); err != nil {
			return fmt.Errorf("erasing %s: %w", s.what, err)
		}
	}

	if _, err := tx.Exec(`SELECT anonymize_person_history($1, $2, $3)`,
		companyID, m.ID, m.ExternalID); err != nil {
		return fmt.Errorf("anonymising door history: %w", err)
	}
	if _, err := tx.Exec(`SELECT redact_person_audit($1, $2, $3)`,
		companyID, m.ExternalID, ErasedPersonLabel(m.PublicID)); err != nil {
		return fmt.Errorf("redacting the audit trail: %w", err)
	}
	if err := scrubAssistantMentionsTx(tx, companyID, m); err != nil {
		return fmt.Errorf("scrubbing assistant records: %w", err)
	}
	return recordDeletedSubjectTx(tx, companyID, m)
}

// scrubAssistantMentionsTx removes the assistant's working copies of a deleted
// person: whole conversations whose transcript names them, and the arguments
// of tool calls and confirmations that do.
//
// BEST EFFORT, AND SAID SO. A transcript is free text: this finds the member
// number and the full name as they were stored, and cannot find a paraphrase.
// The guarantee is the assistant's short retention; this shortens the tail.
//
// A member number shorter than four characters is matched only as a whole JSON
// string ("12"), never as a substring, or deleting member 12 would delete every
// conversation that mentions a twelve. A name shorter than four characters is
// not matched at all, for the same reason.
func scrubAssistantMentionsTx(tx *sql.Tx, companyID int64, m erasedMember) error {
	external := strings.TrimSpace(m.ExternalID)
	if len(external) < 4 {
		external = `"` + external + `"`
	}
	name := strings.ToLower(strings.TrimSpace(m.FullName))
	if len(name) < 4 {
		name = ""
	}

	const mentions = `(strpos(%[1]s, $2) > 0 OR ($3 <> '' AND strpos(lower(%[1]s), $3) > 0))`

	if _, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM assistant_conversations c
		 WHERE c.company_id = $1
		   AND EXISTS (SELECT 1 FROM assistant_messages msg
		                WHERE msg.conversation_id = c.id
		                  AND `+mentions+`)`, "msg.content::text"),
		companyID, external, name); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		UPDATE assistant_tool_calls
		   SET arguments = '{"redacted": true}'::jsonb, route = ''
		 WHERE company_id = $1
		   AND (`+mentions+` OR strpos(route, $2) > 0)`, "arguments::text"),
		companyID, external, name); err != nil {
		return err
	}
	_, err := tx.Exec(fmt.Sprintf(`
		UPDATE assistant_confirmations
		   SET arguments = '{"redacted": true}'::jsonb
		 WHERE company_id = $1
		   AND `+mentions, "arguments::text"),
		companyID, external, name)
	return err
}

func recordDeletedSubjectTx(tx *sql.Tx, companyID int64, m erasedMember) error {
	keys, err := ledgerKeys(tx)
	if err != nil {
		return err
	}
	key := keys[0]
	_, err = tx.Exec(`
		INSERT INTO deleted_subjects (company_id, subject_hash, key_id, person_id)
		VALUES ($1, $2, $3, $4)`,
		companyID, subjectHash(key, companyID, m.ExternalID), key.id, m.ID)
	return err
}

// ---------------------------------------------------------------------------
// Late data: an offline terminal's queue
// ---------------------------------------------------------------------------

// IsErasedSubject reports whether a member number reported by a terminal
// belongs to a deleted member, so the event can be stored without it.
//
// The ledger alone is not the answer, because a company may give the number to
// somebody new. An event is the deleted member's when the ledger knows the
// number AND either nobody live holds it now or the event happened before the
// current holder existed. An event after that belongs to the new holder.
//
// Fails OPEN to "not erased" only on an empty number; any database error is
// returned, and the caller decides.
func IsErasedSubject(companyID int64, externalID string, occurredAt time.Time) (bool, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return false, nil
	}
	keys, err := ledgerKeys(DB)
	if err != nil {
		return false, err
	}
	hashes := make([]string, 0, len(keys))
	for _, k := range keys {
		hashes = append(hashes, subjectHash(k, companyID, externalID))
	}

	var known bool
	if err := DB.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM deleted_subjects
		                WHERE company_id = $1 AND subject_hash::text = ANY($2::text[]))`,
		companyID, pqStringArray(hashes)).Scan(&known); err != nil {
		return false, err
	}
	if !known {
		return false, nil
	}

	var createdAt time.Time
	err = DB.QueryRow(`
		SELECT created_at FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, externalID).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return occurredAt.Before(createdAt), nil
}

// pqStringArray renders a Go slice as a PostgreSQL text[] literal. The values
// are hex digests, so no quoting is ever needed beyond the braces.
func pqStringArray(values []string) string {
	return "{" + strings.Join(values, ",") + "}"
}

// ---------------------------------------------------------------------------
// Phase 2: deleting the row once every terminal has let go
// ---------------------------------------------------------------------------

// FinalizeErasedPeople deletes erased members whose terminals have all
// confirmed the removal, and returns how many it deleted.
//
// A person is held while any LIVE terminal still owes the platform something
// about them: a placement that is not yet REMOVED or FAILED, or a person job it
// has not acknowledged. A deleted or released terminal owes nothing -- release
// wipes the unit -- so it holds nobody.
//
// Every deletion is its own transaction, so one person whose row cannot go does
// not hold back the rest.
func FinalizeErasedPeople(ctx context.Context, graceDays int) (int, error) {
	rows, err := DB.QueryContext(ctx, `
		SELECT ds.id, ds.company_id, ds.person_id
		  FROM deleted_subjects ds
		 WHERE ds.finalized_at IS NULL AND ds.person_id IS NOT NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM credential_placements pl
		         JOIN credentials c ON c.id = pl.credential_id
		         JOIN devices d ON d.id = pl.device_id
		        WHERE c.person_id = ds.person_id
		          AND d.deleted_at IS NULL
		          AND pl.state IN ('PENDING', 'PLACED', 'REMOVING'))
		   AND NOT EXISTS (
		       SELECT 1 FROM sync_jobs j
		         JOIN devices d ON d.id = j.device_id
		        WHERE j.entity_type = 'PERSON' AND j.entity_id = ds.person_id
		          AND d.deleted_at IS NULL
		          AND j.status IN ('PENDING', 'IN_PROGRESS'))
		 ORDER BY ds.id`)
	if err != nil {
		return 0, err
	}
	type pending struct{ ledgerID, companyID, personID int64 }
	var due []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.ledgerID, &p.companyID, &p.personID); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	finalized := 0
	for _, p := range due {
		if err := finalizeOneTx(ctx, p.ledgerID, p.companyID, p.personID, graceDays); err != nil {
			return finalized, fmt.Errorf("finalising ledger entry %d: %w", p.ledgerID, err)
		}
		finalized++
	}
	return finalized, nil
}

// finalizeOneTx deletes one erased person, in the order the foreign keys and
// the immutability triggers require.
//
//  1. Door history is anonymised again. Phase 0 did it, but a REMOVED report
//     or a late event may have arrived since; the cascades below would
//     otherwise have to UPDATE an immutable event, and would be refused.
//  2. credentials, which cascades to credential_placements. events.
//     credential_id is already NULL, so the SET NULL fires on nothing.
//  3. Anything else still naming the person.
//  4. The person.
//  5. The ledger entry is finalised and given its expiry.
func finalizeOneTx(ctx context.Context, ledgerID, companyID, personID int64, graceDays int) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var externalID string
	err = tx.QueryRow(`SELECT external_id FROM people
	                    WHERE id = $1 AND company_id = $2 AND deleted_at IS NOT NULL
	                    FOR UPDATE`, personID, companyID).Scan(&externalID)
	if errors.Is(err, sql.ErrNoRows) {
		// Already gone -- deleted by an earlier pass that failed to record it.
		externalID = ""
	} else if err != nil {
		return err
	}

	if externalID != "" {
		steps := []struct {
			query string
			args  []any
		}{
			{`SELECT anonymize_person_history($1, $2, $3)`, []any{companyID, personID, externalID}},
			{`DELETE FROM credentials WHERE company_id = $1 AND person_id = $2`, []any{companyID, personID}},
			{`DELETE FROM permissions WHERE company_id = $1 AND person_id = $2`, []any{companyID, personID}},
			{`DELETE FROM enrollment_requests WHERE person_id = $1`, []any{personID}},
			{`DELETE FROM sync_jobs WHERE entity_type = 'PERSON' AND entity_id = $1`, []any{personID}},
			{`DELETE FROM people WHERE id = $1 AND company_id = $2`, []any{personID, companyID}},
		}
		for _, s := range steps {
			if _, err := tx.Exec(s.query, s.args...); err != nil {
				return err
			}
		}
	}

	// graceDays <= 0 means the entry never expires: the safe setting while any
	// backup of unknown age, or any terminal that does not scrub its queue,
	// could still bring the number back (docs/erasure.md).
	var grace sql.NullInt32
	if graceDays > 36500 {
		graceDays = 36500
	}
	if graceDays > 0 {
		grace = sql.NullInt32{Int32: int32(graceDays), Valid: true}
	}
	if _, err := tx.Exec(`
		UPDATE deleted_subjects
		   SET person_id = NULL, finalized_at = CURRENT_TIMESTAMP,
		       expires_at = CASE WHEN $2::int IS NULL THEN NULL
		                         ELSE CURRENT_TIMESTAMP + make_interval(days => $2::int) END
		 WHERE id = $1`, ledgerID, grace); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeExpiredDeletedSubjects drops ledger entries past their expiry. Only a
// finalised entry ever has one.
func PurgeExpiredDeletedSubjects(ctx context.Context) (int64, error) {
	res, err := DB.ExecContext(ctx,
		`DELETE FROM deleted_subjects WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Rows deleted before erasure existed
// ---------------------------------------------------------------------------

// EraseLegacyDeletedPeople applies phase 0 to people soft-deleted before 039,
// who still hold their name, email and phone. Returns how many it erased.
//
// Their DELETE jobs were queued when they were deleted, so none is queued
// again; the finaliser then deletes them like anybody else.
func EraseLegacyDeletedPeople(ctx context.Context) (int, error) {
	rows, err := DB.QueryContext(ctx, `
		SELECT p.id, p.company_id, p.public_id, p.external_id, COALESCE(p.full_name, '')
		  FROM people p
		 WHERE p.deleted_at IS NOT NULL
		   AND NOT EXISTS (SELECT 1 FROM deleted_subjects ds WHERE ds.person_id = p.id)
		 ORDER BY p.id`)
	if err != nil {
		return 0, err
	}
	type legacy struct {
		companyID int64
		member    erasedMember
	}
	var found []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.member.ID, &l.companyID, &l.member.PublicID,
			&l.member.ExternalID, &l.member.FullName); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	erased := 0
	for _, l := range found {
		tx, err := DB.BeginTx(ctx, nil)
		if err != nil {
			return erased, err
		}
		if err := erasePersonTx(tx, l.companyID, l.member); err != nil {
			tx.Rollback()
			return erased, fmt.Errorf("erasing legacy person %d: %w", l.member.ID, err)
		}
		if err := tx.Commit(); err != nil {
			return erased, err
		}
		erased++
	}
	return erased, nil
}

// ---------------------------------------------------------------------------
// Restored backups
// ---------------------------------------------------------------------------

// ReplayDeletedSubjects erases live people the ledger says were deleted, and
// returns how many. It is what makes a restored backup safe: the restore brings
// back rows deleted after the backup was taken, and this deletes them again.
//
// A live person is the deleted one -- rather than somebody new given the same
// number -- only if they were created BEFORE the deletion. A restored row keeps
// its original created_at, so this tells the two apart without the number.
//
// THE LEDGER MUST SURVIVE THE RESTORE for this to work, and a restore of the
// whole database replaces it with the backup's copy. The procedure is in
// docs/erasure.md: export deleted_subjects and deletion_ledger_keys before
// restoring, import them after, then let this run.
func ReplayDeletedSubjects(ctx context.Context) (int, error) {
	keys, err := ledgerKeys(DB)
	if err != nil {
		return 0, err
	}
	keyByID := map[string]ledgerKey{}
	for _, k := range keys {
		keyByID[k.id] = k
	}

	type entry struct {
		hash      string
		keyID     string
		deletedAt time.Time
	}
	ledger := map[int64][]entry{}
	rows, err := DB.QueryContext(ctx, `SELECT company_id, subject_hash, key_id, deleted_at FROM deleted_subjects`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var companyID int64
		var e entry
		if err := rows.Scan(&companyID, &e.hash, &e.keyID, &e.deletedAt); err != nil {
			rows.Close()
			return 0, err
		}
		ledger[companyID] = append(ledger[companyID], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	replayed := 0
	for companyID, entries := range ledger {
		people, err := DB.QueryContext(ctx, `
			SELECT external_id, created_at FROM people
			 WHERE company_id = $1 AND deleted_at IS NULL`, companyID)
		if err != nil {
			return replayed, err
		}
		var doomed []string
		for people.Next() {
			var externalID string
			var createdAt time.Time
			if err := people.Scan(&externalID, &createdAt); err != nil {
				people.Close()
				return replayed, err
			}
			for _, e := range entries {
				k, ok := keyByID[e.keyID]
				if !ok || createdAt.After(e.deletedAt) {
					continue
				}
				if hmac.Equal([]byte(subjectHash(k, companyID, externalID)), []byte(e.hash)) {
					doomed = append(doomed, externalID)
					break
				}
			}
		}
		people.Close()
		if err := people.Err(); err != nil {
			return replayed, err
		}

		for _, externalID := range doomed {
			if err := DeleteMember(companyID, externalID); err != nil {
				return replayed, fmt.Errorf("replaying a deletion in company %d: %w", companyID, err)
			}
			replayed++
		}
	}
	return replayed, nil
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// DeleteUser removes an operator account for good and returns the pseudonym
// the audit trail now knows them by.
//
// Their audit rows are kept -- what was done, when, to what -- but the actor
// becomes a pseudonym and loses their address and browser; rows ABOUT them lose
// their email. Every other table that copied their email at the time gets the
// pseudonym instead. Then the account row goes, and with it, by cascade, their
// sessions, reset and invitation tokens, site grants and assistant records.
func DeleteUser(companyID, userID int64) (string, error) {
	tx, err := DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	pseudonym, err := deleteUserTx(tx, companyID, userID, true)
	if err != nil {
		return "", err
	}
	return pseudonym, tx.Commit()
}

func deleteUserTx(tx *sql.Tx, companyID, userID int64, liveOnly bool) (string, error) {
	var publicID, email string
	query := `SELECT public_id, email FROM users
	           WHERE id = $1 AND company_id = $2`
	if liveOnly {
		query += ` AND deleted_at IS NULL`
	}
	err := tx.QueryRow(query+` FOR UPDATE`, userID, companyID).Scan(&publicID, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", models.ErrUserNotFound
	}
	if err != nil {
		return "", err
	}
	pseudonym := ErasedOperatorLabel(publicID)

	if _, err := tx.Exec(`SELECT anonymize_audit_actor($1, $2, $3::uuid, $4)`,
		companyID, userID, publicID, pseudonym); err != nil {
		return "", fmt.Errorf("pseudonymising the audit trail: %w", err)
	}

	// The tables that copied the operator's address when they acted. Matched
	// case-insensitively and scoped to this company, so an operator of another
	// company with the same address is untouched.
	copies := []string{
		`UPDATE api_credentials SET created_by_email = $3
		  WHERE company_id = $1 AND lower(created_by_email) = lower($2)`,
		`UPDATE device_claim_codes SET issued_by_email = $3
		  WHERE company_id = $1 AND lower(issued_by_email) = lower($2)`,
		`UPDATE devices SET release_ordered_by_email = $3
		  WHERE site_id IN (SELECT id FROM sites WHERE company_id = $1)
		    AND lower(release_ordered_by_email) = lower($2)`,
		`UPDATE enrollment_requests SET requested_by_email = $3
		  WHERE person_id IN (SELECT id FROM people WHERE company_id = $1)
		    AND lower(requested_by_email) = lower($2)`,
		`UPDATE sync_jobs SET requested_by_email = $3
		  WHERE site_id IN (SELECT id FROM sites WHERE company_id = $1)
		    AND lower(requested_by_email) = lower($2)`,
		`UPDATE terminal_announcements SET adopted_by_email = $3
		  WHERE company_id = $1 AND lower(adopted_by_email) = lower($2)`,
		`UPDATE terminal_announcements SET approved_by_email = $3
		  WHERE company_id = $1 AND lower(approved_by_email) = lower($2)`,
		`UPDATE terminal_announcements SET rejected_by_email = $3
		  WHERE company_id = $1 AND lower(rejected_by_email) = lower($2)`,
		`UPDATE user_credential_tokens SET issued_by_email = $3
		  WHERE user_id IN (SELECT id FROM users WHERE company_id = $1)
		    AND lower(issued_by_email) = lower($2)`,
	}
	for _, q := range copies {
		if _, err := tx.Exec(q, companyID, email, pseudonym); err != nil {
			return "", fmt.Errorf("replacing a copied operator address: %w", err)
		}
	}

	if _, err := tx.Exec(`DELETE FROM users WHERE id = $1 AND company_id = $2`,
		userID, companyID); err != nil {
		return "", fmt.Errorf("deleting the operator: %w", err)
	}
	return pseudonym, nil
}

// EraseLegacyDeletedOperators deletes operators soft-deleted before 039, the
// same way DeleteUser deletes a live one. Returns how many.
func EraseLegacyDeletedOperators(ctx context.Context) (int, error) {
	rows, err := DB.QueryContext(ctx,
		`SELECT id, company_id FROM users WHERE deleted_at IS NOT NULL ORDER BY id`)
	if err != nil {
		return 0, err
	}
	type legacy struct{ id, companyID int64 }
	var found []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.companyID); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	erased := 0
	for _, l := range found {
		tx, err := DB.BeginTx(ctx, nil)
		if err != nil {
			return erased, err
		}
		if _, err := deleteUserTx(tx, l.companyID, l.id, false); err != nil {
			tx.Rollback()
			return erased, fmt.Errorf("erasing legacy operator %d: %w", l.id, err)
		}
		if err := tx.Commit(); err != nil {
			return erased, err
		}
		erased++
	}
	return erased, nil
}
