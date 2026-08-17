package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"access-terminal-cloud-api/models"
)

// The operator audit trail (migrations/013_events_and_audit.sql).
//
// SEC-07: nothing recorded who created a site, rotated a key, changed a role,
// removed a person or enabled a capability. Sessions were logged; the changes
// made inside them were not, so after a disputed change there was no way to
// establish who made it.
//
// TWO PROPERTIES THIS FILE EXISTS TO GUARANTEE.
//
// 1. AN AUDIT WRITE NEVER FAILS A REQUEST. WriteAuditEvent logs and returns
//    rather than propagating. That is a deliberate trade and the reasoning is
//    not obvious, so it is written down: the alternative -- failing the mutation
//    when its audit row cannot be written -- turns a full audit table or a
//    momentary write error into an outage of every console write. An audit trail
//    that takes the product down is one somebody disables.
//
//    Where the write must be atomic with the change it describes, the caller
//    passes its transaction to WriteAuditEventTx instead. That is the right tool
//    for a mutation where a missing audit row would be worse than a failed
//    request -- credential revocation, role changes -- and the choice is made at
//    the call site rather than globally.
//
// 2. NOTHING SECRET REACHES THIS TABLE. `changes` is redacted by the caller and
//    the sensitive columns are not readable from the paths that write here:
//    password hashes never leave database/users.go, site and device keys exist
//    only in the local variable that generated them, and sealed biometric
//    material is never loaded by a console handler at all.

// AuditEntry is one operator action, as the caller describes it.
type AuditEntry struct {
	CompanyID int64

	// The actor, denormalised. An operator account can be deleted, and when it
	// is, the record of what they did must survive with enough identity to be
	// readable -- a foreign key alone would leave a null and lose the answer.
	ActorUserID int64
	ActorEmail  string
	ActorRole   string

	IPAddress string
	UserAgent string
	RequestID string

	Action string

	TargetType     string
	TargetPublicID string
	TargetLabel    string

	// Changes is redacted BY THE CALLER. This layer cannot know which fields of
	// an arbitrary map are sensitive, and a redactor that guesses is one that
	// eventually guesses wrong about a new field.
	Changes map[string]any
}

// WriteAuditEvent records an action, best effort.
//
// Errors are swallowed after logging. See the note at the top of this file for
// why: an audit failure must not become a product outage.
func WriteAuditEvent(entry AuditEntry) {
	if err := writeAuditEvent(DB, entry); err != nil {
		// Logged loudly, and with the record that was lost, so a failing audit
		// path is visible in the operational log rather than silently producing
		// a trail with holes in it.
		log.Printf("AUDIT WRITE FAILED action=%s company=%d actor=%s target=%s/%s: %v",
			entry.Action, entry.CompanyID, entry.ActorEmail,
			entry.TargetType, entry.TargetLabel, err)
	}
}

// WriteAuditEventTx records an action inside the caller's transaction, so the
// change and its record commit together or not at all.
//
// For mutations where a missing audit row is worse than a failed request. The
// error is returned rather than swallowed, because the caller has a transaction
// to roll back and a decision to make.
func WriteAuditEventTx(tx *sql.Tx, entry AuditEntry) error {
	return writeAuditEvent(tx, entry)
}

type auditExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func writeAuditEvent(db auditExecer, entry AuditEntry) error {
	var changes []byte
	if len(entry.Changes) > 0 {
		encoded, err := json.Marshal(entry.Changes)
		if err != nil {
			// A change set that will not marshal must not lose the whole record.
			// The action, the actor and the target are the load-bearing parts.
			encoded = []byte(`{"_error":"change set could not be encoded"}`)
		}
		changes = encoded
	}

	_, err := db.Exec(`
		INSERT INTO audit_events
		    (company_id, actor_user_id, actor_email, actor_role,
		     ip_address, user_agent, request_id, action,
		     target_type, target_public_id, target_label, changes)
		VALUES ($1, NULLIF($2, 0)::bigint, NULLIF($3, ''), NULLIF($4, ''),
		        NULLIF($5, ''), NULLIF(left($6, 255), ''), NULLIF($7, ''), $8,
		        NULLIF($9, ''), NULLIF($10, '')::uuid, NULLIF(left($11, 160), ''),
		        $12::jsonb)`,
		entry.CompanyID, entry.ActorUserID, entry.ActorEmail, entry.ActorRole,
		entry.IPAddress, entry.UserAgent, entry.RequestID, entry.Action,
		entry.TargetType, entry.TargetPublicID, entry.TargetLabel, changes)
	return err
}

// AuditQuery narrows an audit listing.
type AuditQuery struct {
	Action     string
	TargetType string
	ActorEmail string
	Since      *time.Time
	Until      *time.Time
	Limit      int
	Offset     int
}

// AuditPage is one page of audit records plus the size of the whole match.
type AuditPage struct {
	Entries []models.AuditRecord
	Total   int
}

// ListAuditEvents returns one company's audit trail, newest first.
//
// Company-scoped like every other console read. There is deliberately no
// cross-company audit read anywhere in the platform: a support identity that can
// see every tenant's administrative history is a credential worth attacking, and
// the platform-admin routes operate on companies rather than inside them.
func ListAuditEvents(companyID int64, query AuditQuery) (*AuditPage, error) {
	if query.Limit <= 0 || query.Limit > 200 {
		query.Limit = 50
	}
	if query.Offset < 0 {
		query.Offset = 0
	}

	// One predicate, applied to both the count and the page, so the total can
	// never describe a different set from the rows.
	const where = `
		 WHERE a.company_id = $1
		   AND ($2 = '' OR a.action = $2)
		   AND ($3 = '' OR a.target_type = $3)
		   AND ($4 = '' OR a.actor_email = $4)
		   AND ($5::timestamptz IS NULL OR a.occurred_at >= $5)
		   AND ($6::timestamptz IS NULL OR a.occurred_at <= $6)`

	var page AuditPage
	err := DB.QueryRow(`SELECT count(*) FROM audit_events a`+where,
		companyID, query.Action, query.TargetType, query.ActorEmail,
		query.Since, query.Until).Scan(&page.Total)
	if err != nil {
		return nil, err
	}

	rows, err := DB.Query(`
		SELECT a.public_id, a.action, COALESCE(a.actor_email, ''),
		       COALESCE(a.actor_role, ''), COALESCE(a.target_type, ''),
		       a.target_public_id, COALESCE(a.target_label, ''),
		       COALESCE(a.ip_address, ''), a.changes, a.occurred_at
		  FROM audit_events a`+where+`
		 ORDER BY a.occurred_at DESC, a.id DESC
		 LIMIT $7 OFFSET $8`,
		companyID, query.Action, query.TargetType, query.ActorEmail,
		query.Since, query.Until, query.Limit, query.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []models.AuditRecord{}
	for rows.Next() {
		var e models.AuditRecord
		var targetID sql.NullString
		var changes []byte

		if err := rows.Scan(&e.ID, &e.Action, &e.ActorEmail, &e.ActorRole,
			&e.TargetType, &targetID, &e.TargetLabel, &e.IPAddress,
			&changes, &e.OccurredAt); err != nil {
			return nil, err
		}
		e.TargetID = targetID.String
		if len(changes) > 0 {
			e.Changes = json.RawMessage(changes)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page.Entries = entries
	return &page, nil
}

// PurgeAuditEvents applies each company's configured retention window.
//
// Goes through the SQL function rather than issuing a DELETE, because
// audit_events refuses DELETE by trigger. The function sets the session flag the
// trigger honours, which keeps "remove rows past their window" expressible and
// "remove the row that incriminates me" not.
func PurgeAuditEvents(ctx context.Context) (int64, error) {
	var removed int64
	err := DB.QueryRowContext(ctx, `SELECT purge_audit_events(NULL)`).Scan(&removed)
	return removed, err
}

// PurgeEvents applies each company's configured retention window to field
// events. Same mechanism, same reasoning.
func PurgeEvents(ctx context.Context) (int64, error) {
	var removed int64
	err := DB.QueryRowContext(ctx, `SELECT purge_events(NULL)`).Scan(&removed)
	return removed, err
}
