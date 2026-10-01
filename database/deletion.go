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
	"regexp"
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

// lifetimeSlack widens a person's lifetime by the time it takes the audit row
// for their creation or deletion to be written after the row itself, which is
// after the transaction commits.
const lifetimeSlack = time.Minute

// personLifetimeTx returns the window [from, to] in which a row that merely
// NAMES this person's member number -- with no person id or public id to say
// whose it is -- can be attributed to them.
//
// THE REUSED-NUMBER RULE. A member number can be given to somebody new once its
// holder is deleted. Matching by number alone would then reach the other
// holder's rows -- a live member's audit trail, door history and assistant
// records rewritten because somebody else with their number was erased. So the
// window is this person's own lifetime, widened by lifetimeSlack and then
// CLAMPED so it can never reach into the lifetime of any other row holding the
// same number: not past the creation of a later holder, not before the
// deletion of an earlier one.
func personLifetimeTx(tx *sql.Tx, companyID, personID int64, externalID string,
	createdAt, deletedAt time.Time) (time.Time, time.Time, error) {
	var from, to time.Time
	err := tx.QueryRow(`
		SELECT GREATEST($4::timestamptz - make_interval(secs => $6),
		                COALESCE((SELECT max(o.deleted_at) FROM people o
		                           WHERE o.company_id = $1 AND o.external_id = $3 AND o.id <> $2
		                             AND o.deleted_at IS NOT NULL AND o.deleted_at <= $4),
		                         '-infinity'::timestamptz)),
		       LEAST($5::timestamptz + make_interval(secs => $6),
		             COALESCE((SELECT min(o.created_at) FROM people o
		                        WHERE o.company_id = $1 AND o.external_id = $3 AND o.id <> $2
		                          AND o.created_at > $4),
		                      'infinity'::timestamptz))`,
		companyID, personID, externalID, createdAt, deletedAt, lifetimeSlack.Seconds()).Scan(&from, &to)
	return from, to, err
}

// erasePersonTx removes everything personal about one member except the
// member number on their own row, and records them in the ledger.
//
// Scoped by company throughout, and by IDENTITY: rows linked to the person are
// theirs; rows that only name their number are theirs only inside their own
// lifetime (personLifetimeTx). A live member holding the same number is never
// touched. Idempotent.
func erasePersonTx(tx *sql.Tx, companyID int64, m erasedMember) error {
	// The sealed template first, on every route into erasure -- a delete made
	// now and one made before 039 alike.
	if err := destroySealedMaterialTx(tx, companyID, m.ID); err != nil {
		return fmt.Errorf("destroying sealed material: %w", err)
	}

	// The person row keeps the member number and nothing else of theirs. A row
	// deleted before 039 keeps its ORIGINAL deleted_at: that date is what the
	// lifetime and the ledger are measured from.
	var createdAt, deletedAt time.Time
	if err := tx.QueryRow(`
		UPDATE people
		   SET full_name = '', membership_type = '', email = NULL, phone = NULL,
		       valid_from = NULL, valid_until = NULL, category_id = NULL,
		       fingerprint_template = NULL, active = FALSE,
		       deleted_at = COALESCE(deleted_at, CURRENT_TIMESTAMP),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND company_id = $2
		RETURNING created_at, deleted_at`, m.ID, companyID).Scan(&createdAt, &deletedAt); err != nil {
		return fmt.Errorf("erasing person row: %w", err)
	}

	from, to, err := personLifetimeTx(tx, companyID, m.ID, m.ExternalID, createdAt, deletedAt)
	if err != nil {
		return fmt.Errorf("bounding the person's lifetime: %w", err)
	}

	steps := []struct {
		what  string
		query string
		args  []any
	}{
		// Rules naming the person, and enrolments waiting for them.
		{"permissions", `DELETE FROM permissions WHERE company_id = $1 AND person_id = $2`,
			[]any{companyID, m.ID}},
		{"enrolment requests", `DELETE FROM enrollment_requests WHERE person_id = $1`,
			[]any{m.ID}},

		// EVERY person job keeps its row and loses everything but the number.
		//
		// The rows are the only record of which terminals were ever sent this
		// person. The roster reconciler queues a DELETE for a terminal from
		// exactly that record -- so a terminal that was paused, disabled or
		// otherwise not being synced when the person was deleted still gets
		// told once it rejoins -- and finalisation will not delete the person
		// while any terminal sent them has not acknowledged a DELETE.
		{"person sync job payloads", `
			UPDATE sync_jobs
			   SET payload = jsonb_build_object('member_id', entity_external_id, 'deleted', TRUE)
			 WHERE entity_type = 'PERSON' AND entity_id = $1`,
			[]any{m.ID}},

		// Undelivered upserts are cancelled: delivering them would re-add the
		// person. An undelivered DELETE is kept -- for somebody deleted before
		// 039 it is the only instruction an offline terminal will get.
		{"undelivered upserts", `
			UPDATE sync_jobs
			   SET status = 'CANCELLED'
			 WHERE entity_type = 'PERSON' AND entity_id = $1
			   AND status = 'PENDING' AND job_type <> 'DELETE'`,
			[]any{m.ID}},
	}
	for _, s := range steps {
		if _, err := tx.Exec(s.query, s.args...); err != nil {
			return fmt.Errorf("erasing %s: %w", s.what, err)
		}
	}

	if _, err := tx.Exec(`SELECT anonymize_person_history($1, $2, $3, $4, $5)`,
		companyID, m.ID, m.ExternalID, from, to); err != nil {
		return fmt.Errorf("anonymising door history: %w", err)
	}
	if _, err := tx.Exec(`SELECT redact_person_audit($1, $2::uuid, $3, $4, $5, $6)`,
		companyID, m.PublicID, m.ExternalID, ErasedPersonLabel(m.PublicID), from, to); err != nil {
		return fmt.Errorf("redacting the audit trail: %w", err)
	}
	if err := scrubAssistantMentionsTx(tx, companyID, m, from, to); err != nil {
		return fmt.Errorf("scrubbing assistant records: %w", err)
	}
	return recordDeletedSubjectTx(tx, companyID, m, createdAt, deletedAt)
}

// mentionPatterns builds the PostgreSQL regular expressions a deleted person's
// assistant records are matched with: a member number, a request route naming
// it, and a name. Empty means "do not match on this".
//
// WHOLE TOKENS, NEVER SUBSTRINGS. Member 1001 must not match 10012 or a phone
// number, and Anna must not match Hannah. A number shorter than four characters
// is matched only as a whole JSON string value ("12") or a whole route segment
// (/people/12), never as a word in prose, or deleting member 12 would delete
// every conversation that mentions twelve of anything. A name shorter than four
// characters is not matched at all.
func mentionPatterns(externalID, fullName string) (number, route, name string) {
	externalID = strings.TrimSpace(externalID)
	quoted := regexp.QuoteMeta(externalID)
	const boundaryL, boundaryR = `(^|[^[:alnum:]])`, `([^[:alnum:]]|$)`
	if externalID != "" {
		if len(externalID) >= 4 {
			number = boundaryL + quoted + boundaryR
		} else {
			number = `"` + quoted + `"`
		}
		route = `/` + quoted + `(/|\?|$)`
	}
	fullName = strings.TrimSpace(fullName)
	if len(fullName) >= 4 {
		name = boundaryL + regexp.QuoteMeta(fullName) + boundaryR
	}
	return number, route, name
}

// scrubAssistantMentionsTx removes the assistant's working copies of a deleted
// person: whole conversations whose transcript names them, and the arguments
// of tool calls and confirmations that do -- only those written inside the
// person's lifetime [from, to], so a later holder of the same number keeps
// theirs.
//
// BEST EFFORT, AND SAID SO. A transcript is free text: this finds the member
// number and the full name as stored, never a paraphrase. The guarantee is the
// assistant's short retention; this shortens the tail.
func scrubAssistantMentionsTx(tx *sql.Tx, companyID int64, m erasedMember, from, to time.Time) error {
	number, route, name := mentionPatterns(m.ExternalID, m.FullName)
	if number == "" && name == "" {
		return nil
	}

	const mentions = `(($2 <> '' AND %[1]s ~ $2) OR ($3 <> '' AND %[1]s ~* $3))`

	if _, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM assistant_conversations c
		 WHERE c.company_id = $1
		   AND EXISTS (SELECT 1 FROM assistant_messages msg
		                WHERE msg.conversation_id = c.id
		                  AND msg.created_at BETWEEN $4 AND $5
		                  AND `+mentions+`)`, "msg.content::text"),
		companyID, number, name, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		UPDATE assistant_tool_calls
		   SET arguments = '{"redacted": true}'::jsonb, route = ''
		 WHERE company_id = $1
		   AND created_at BETWEEN $4 AND $5
		   AND (`+mentions+` OR ($6 <> '' AND route ~ $6))`, "arguments::text"),
		companyID, number, name, from, to, route); err != nil {
		return err
	}
	_, err := tx.Exec(fmt.Sprintf(`
		UPDATE assistant_confirmations
		   SET arguments = '{"redacted": true}'::jsonb
		 WHERE company_id = $1
		   AND issued_at BETWEEN $4 AND $5
		   AND `+mentions, "arguments::text"),
		companyID, number, name, from, to)
	return err
}

// recordDeletedSubjectTx records the deletion in the ledger, dated by the
// person's ACTUAL deletion -- for somebody deleted before 039, that is the
// original date, not the day erasure first ran. The date is what replay and
// the late-event rule measure against, so getting it wrong turns a later,
// live holder of the same number into somebody "created before the deletion".
//
// ONE ENTRY PER INCARNATION. If the ledger already records this person -- an
// entry for the same number dated at or after their creation, which only their
// own deletion can be -- that entry is re-pointed at the row instead of a new
// one being written. A restored backup replayed repeatedly therefore never
// grows the ledger.
func recordDeletedSubjectTx(tx *sql.Tx, companyID int64, m erasedMember, createdAt, deletedAt time.Time) error {
	keys, err := ledgerKeys(tx)
	if err != nil {
		return err
	}
	hashes := make([]string, 0, len(keys))
	for _, k := range keys {
		hashes = append(hashes, subjectHash(k, companyID, m.ExternalID))
	}

	var existing int64
	err = tx.QueryRow(`
		SELECT id FROM deleted_subjects
		 WHERE company_id = $1 AND subject_hash = ANY($2::bpchar[]) AND deleted_at >= $3
		 ORDER BY id LIMIT 1`, companyID, pqStringArray(hashes), createdAt).Scan(&existing)
	if err == nil {
		_, err = tx.Exec(`
			UPDATE deleted_subjects
			   SET person_id = $2, finalized_at = NULL, expires_at = NULL
			 WHERE id = $1`, existing, m.ID)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	key := keys[0]
	_, err = tx.Exec(`
		INSERT INTO deleted_subjects (company_id, subject_hash, key_id, person_id, deleted_at)
		VALUES ($1, $2, $3, $4, $5)`,
		companyID, subjectHash(key, companyID, m.ExternalID), key.id, m.ID, deletedAt)
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

	// bpchar[] rather than text[]: subject_hash is CHAR(64), and comparing it
	// as text would keep the (company_id, subject_hash) index from serving the
	// lookup -- a scan of the company's whole ledger on every door event.
	var known bool
	if err := DB.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM deleted_subjects
		                WHERE company_id = $1 AND subject_hash = ANY($2::bpchar[]))`,
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

// LedgerKnowsSubject reports whether the deletion ledger records anybody, ever,
// under this member number in this company -- with no judgement about whether
// a live holder is the same person. Used where the question is "could this
// report be about somebody deleted", not "is this event theirs".
func LedgerKnowsSubject(companyID int64, externalID string) (bool, error) {
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
	err = DB.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM deleted_subjects
		                WHERE company_id = $1 AND subject_hash = ANY($2::bpchar[]))`,
		companyID, pqStringArray(hashes)).Scan(&known)
	return known, err
}

// pqStringArray renders a Go slice as a PostgreSQL array literal. The values
// are hex digests and key ids, so no quoting is ever needed beyond the braces.
func pqStringArray(values []string) string {
	return "{" + strings.Join(values, ",") + "}"
}

// ---------------------------------------------------------------------------
// Phase 2: deleting the row once every terminal has let go
// ---------------------------------------------------------------------------

// terminalStillHolds is the predicate, over a live device `d` and the person
// in `ds.person_id`, for "this terminal may still hold the person".
//
// A terminal holds somebody until it has ACKNOWLEDGED a DELETE for them that is
// newer than the last thing it was sent about them. Short of that, any of these
// keeps the person:
//
//   - a placement not yet REMOVED or FAILED -- the platform knows a finger is
//     there;
//   - a person job not yet acknowledged, whatever its type;
//   - a DELETE that FAILED -- the terminal never confirmed the removal, and a
//     job that ran out of attempts is not one that succeeded;
//   - any job it was ever sent about the person (the stripped history erasure
//     keeps) with no acknowledged DELETE after it -- which is what holds a
//     paused or disabled terminal until it rejoins, is sent the DELETE by the
//     roster reconciler, and acknowledges it.
const terminalStillHolds = `(
	EXISTS (SELECT 1 FROM credential_placements pl
	          JOIN credentials c ON c.id = pl.credential_id
	         WHERE c.person_id = ds.person_id AND pl.device_id = d.id
	           AND pl.state IN ('PENDING', 'PLACED', 'REMOVING'))
	OR EXISTS (SELECT 1 FROM sync_jobs j
	            WHERE j.device_id = d.id AND j.entity_type = 'PERSON'
	              AND j.entity_id = ds.person_id
	              AND (j.status IN ('PENDING', 'IN_PROGRESS')
	                   OR (j.status = 'FAILED' AND j.job_type = 'DELETE')))
	OR EXISTS (SELECT 1 FROM sync_jobs sent
	            WHERE sent.device_id = d.id AND sent.entity_type = 'PERSON'
	              AND sent.entity_id = ds.person_id
	              AND NOT EXISTS (SELECT 1 FROM sync_jobs ack
	                               WHERE ack.device_id = d.id AND ack.entity_type = 'PERSON'
	                                 AND ack.entity_id = ds.person_id
	                                 AND ack.job_type = 'DELETE' AND ack.status = 'COMPLETED'
	                                 AND ack.id > sent.id)
	              AND sent.job_type <> 'DELETE')
)`

// FinalizeErasedPeople deletes erased members no live terminal still holds,
// and returns how many it deleted.
//
// A deleted terminal holds nobody -- it is gone, or released, which wipes it.
// Every deletion is its own transaction, so one person whose row cannot go does
// not hold back the rest.
func FinalizeErasedPeople(ctx context.Context, graceDays int) (int, error) {
	rows, err := DB.QueryContext(ctx, `
		SELECT ds.id, ds.company_id, ds.person_id
		  FROM deleted_subjects ds
		 WHERE ds.finalized_at IS NULL AND ds.person_id IS NOT NULL
		   AND NOT EXISTS (SELECT 1 FROM devices d
		                    WHERE d.deleted_at IS NULL
		                      AND `+terminalStillHolds+`)
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
//  1. Door history is anonymised again, within the same lifetime bound phase 0
//     used. A REMOVED report or a late event may have arrived since; the
//     cascades below would otherwise have to UPDATE an immutable event.
//  2. credentials, which cascades to credential_placements. events.
//     credential_id is already NULL, so the SET NULL fires on nothing.
//  3. Anything else still naming the person -- the stripped job history is no
//     longer needed once every terminal has acknowledged.
//  4. The person.
//  5. The ledger entry is finalised and given its expiry.
func finalizeOneTx(ctx context.Context, ledgerID, companyID, personID int64, graceDays int) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var externalID string
	var createdAt, deletedAt time.Time
	err = tx.QueryRow(`SELECT external_id, created_at, deleted_at FROM people
	                    WHERE id = $1 AND company_id = $2 AND deleted_at IS NOT NULL
	                    FOR UPDATE`, personID, companyID).Scan(&externalID, &createdAt, &deletedAt)
	gone := errors.Is(err, sql.ErrNoRows)
	if err != nil && !gone {
		return err
	}

	if !gone {
		from, to, err := personLifetimeTx(tx, companyID, personID, externalID, createdAt, deletedAt)
		if err != nil {
			return err
		}
		steps := []struct {
			query string
			args  []any
		}{
			{`SELECT anonymize_person_history($1, $2, $3, $4, $5)`,
				[]any{companyID, personID, externalID, from, to}},
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
// again; an undelivered one is kept by erasePersonTx. Their original deletion
// date is kept, which is what keeps a live member who was later given the same
// number out of reach (personLifetimeTx, recordDeletedSubjectTx).
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
// number -- only if they were created no later than the deletion the entry
// records. A restored row keeps its original created_at; a new holder's is
// after the deletion. The entry's date is the ACTUAL deletion date, so this
// holds for people deleted before 039 too.
//
// Cost: one hash per live person per key, looked up in a map -- not people x
// entries.
//
// THE LEDGER MUST SURVIVE THE RESTORE for this to work; see docs/erasure.md.
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
		keyID     string
		deletedAt time.Time
	}
	ledger := map[int64]map[string][]entry{} // company -> hash -> entries
	rows, err := DB.QueryContext(ctx, `SELECT company_id, subject_hash, key_id, deleted_at FROM deleted_subjects`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var companyID int64
		var hash string
		var e entry
		if err := rows.Scan(&companyID, &hash, &e.keyID, &e.deletedAt); err != nil {
			rows.Close()
			return 0, err
		}
		if ledger[companyID] == nil {
			ledger[companyID] = map[string][]entry{}
		}
		ledger[companyID][hash] = append(ledger[companyID][hash], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	replayed := 0
	for companyID, byHash := range ledger {
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
		match:
			for _, k := range keys {
				for _, e := range byHash[subjectHash(k, companyID, externalID)] {
					if e.keyID == k.id && !createdAt.After(e.deletedAt) {
						doomed = append(doomed, externalID)
						break match
					}
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
// becomes a pseudonym and loses their address and browser; rows ABOUT them and
// rows that recorded only their address lose it too. Every other table that
// copied their address gets the pseudonym instead. Then the account row goes,
// and with it, by cascade, their sessions, reset and invitation tokens, site
// grants and assistant records.
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

// operatorLifetimeTx is personLifetimeTx for an operator's ADDRESS: an
// address can be given to a new account once the old one is deleted, so a row
// that carries only the address is the old account's only inside its own
// lifetime, clamped against every other account that held the address.
func operatorLifetimeTx(tx *sql.Tx, userID int64, email string,
	createdAt, deletedAt time.Time) (time.Time, time.Time, error) {
	var from, to time.Time
	err := tx.QueryRow(`
		SELECT GREATEST($3::timestamptz - make_interval(secs => $5),
		                COALESCE((SELECT max(o.deleted_at) FROM users o
		                           WHERE lower(o.email) = lower($2) AND o.id <> $1
		                             AND o.deleted_at IS NOT NULL AND o.deleted_at <= $3),
		                         '-infinity'::timestamptz)),
		       LEAST($4::timestamptz + make_interval(secs => $5),
		             COALESCE((SELECT min(o.created_at) FROM users o
		                        WHERE lower(o.email) = lower($2) AND o.id <> $1
		                          AND o.created_at > $3),
		                      'infinity'::timestamptz))`,
		userID, email, createdAt, deletedAt, lifetimeSlack.Seconds()).Scan(&from, &to)
	return from, to, err
}

func deleteUserTx(tx *sql.Tx, companyID, userID int64, liveOnly bool) (string, error) {
	var publicID, email string
	var createdAt, deletedAt time.Time
	query := `SELECT public_id, email, created_at, COALESCE(deleted_at, CURRENT_TIMESTAMP)
	            FROM users WHERE id = $1 AND company_id = $2`
	if liveOnly {
		query += ` AND deleted_at IS NULL`
	}
	err := tx.QueryRow(query+` FOR UPDATE`, userID, companyID).Scan(&publicID, &email, &createdAt, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", models.ErrUserNotFound
	}
	if err != nil {
		return "", err
	}
	pseudonym := ErasedOperatorLabel(publicID)

	from, to, err := operatorLifetimeTx(tx, userID, email, createdAt, deletedAt)
	if err != nil {
		return "", fmt.Errorf("bounding the operator's lifetime: %w", err)
	}

	if _, err := tx.Exec(`SELECT anonymize_audit_actor($1, $2, $3::uuid, $4, $5, $6, $7)`,
		companyID, userID, publicID, pseudonym, email, from, to); err != nil {
		return "", fmt.Errorf("pseudonymising the audit trail: %w", err)
	}

	// The tables that copied the operator's address when they acted. Matched
	// by the operator's own id wherever the row has one; by address only where
	// it does not, and then only inside this account's lifetime -- so a new
	// account later given the same address keeps its own attribution.
	copies := []string{
		`UPDATE api_credentials SET created_by_email = $3
		  WHERE company_id = $1
		    AND (created_by = $4 OR (created_by IS NULL AND lower(created_by_email) = lower($2)
		                             AND created_at BETWEEN $5 AND $6))`,
		`UPDATE device_claim_codes SET issued_by_email = $3
		  WHERE company_id = $1
		    AND (issued_by = $4 OR (issued_by IS NULL AND lower(issued_by_email) = lower($2)
		                            AND created_at BETWEEN $5 AND $6))`,
		`UPDATE devices SET release_ordered_by_email = $3
		  WHERE site_id IN (SELECT id FROM sites WHERE company_id = $1)
		    AND (release_ordered_by = $4
		         OR (release_ordered_by IS NULL AND lower(release_ordered_by_email) = lower($2)
		             AND release_ordered_at BETWEEN $5 AND $6))`,
		`UPDATE enrollment_requests SET requested_by_email = $3
		  WHERE person_id IN (SELECT id FROM people WHERE company_id = $1)
		    AND (requested_by = $4 OR (requested_by IS NULL AND lower(requested_by_email) = lower($2)
		                               AND created_at BETWEEN $5 AND $6))`,
		`UPDATE sync_jobs SET requested_by_email = $3
		  WHERE site_id IN (SELECT id FROM sites WHERE company_id = $1)
		    AND (requested_by = $4 OR (requested_by IS NULL AND lower(requested_by_email) = lower($2)
		                               AND created_at BETWEEN $5 AND $6))`,
		`UPDATE terminal_announcements SET adopted_by_email = $3
		  WHERE company_id = $1
		    AND (adopted_by = $4 OR (adopted_by IS NULL AND lower(adopted_by_email) = lower($2)
		                             AND created_at BETWEEN $5 AND $6))`,
		`UPDATE terminal_announcements SET approved_by_email = $3
		  WHERE company_id = $1
		    AND (approved_by = $4 OR (approved_by IS NULL AND lower(approved_by_email) = lower($2)
		                              AND created_at BETWEEN $5 AND $6))`,
		// No operator id is recorded for a rejection, so the address and the
		// lifetime are all there is to go on ($4 is unused but typed).
		`UPDATE terminal_announcements SET rejected_by_email = $3
		  WHERE company_id = $1 AND lower(rejected_by_email) = lower($2)
		    AND rejected_at BETWEEN $5 AND $6 AND $4::bigint IS NOT NULL`,
		`UPDATE user_credential_tokens SET issued_by_email = $3
		  WHERE user_id IN (SELECT id FROM users WHERE company_id = $1)
		    AND (issued_by_user_id = $4
		         OR (issued_by_user_id IS NULL AND lower(issued_by_email) = lower($2)
		             AND created_at BETWEEN $5 AND $6))`,
	}
	for _, q := range copies {
		if _, err := tx.Exec(q, companyID, email, pseudonym, userID, from, to); err != nil {
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
// same way DeleteUser deletes a live one -- dated by their ORIGINAL deletion,
// so a new account later given the same address is untouched. Returns how
// many.
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
