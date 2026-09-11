package database

import (
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// The event trail, as the public API reads it (API_SPEC.md section 18,
// "Events").
//
// A SEPARATE READER FROM ListEvents, not a mode of it. The console reader is
// offset-paged, counts its total, joins names for display and takes a dozen
// free-text filters; the public reader is keyset-paged, never counts, carries
// identifiers rather than names and takes the five filters the contract
// names. Folding one into the other would make each carry the other's rules,
// and the public one has a rule the console's does not: the caller's SITE
// RESTRICTION is a hard bound on which rows exist at all, not a filter it
// chose.
//
// KEYSET ON (occurred_at, id), NEWEST FIRST -- the same shape MembersAfter
// uses on (created_at, id), for the same reason: an event uploaded from a
// terminal's queue while a client is paging must not shift the page it holds.
// The cursor carries the position; the filters that produced it are bound into
// the cursor's associated data by the service, so a cursor cannot be replayed
// against a different query.

// PublicEventFilter is what GET /events accepts. Every field is optional; an
// empty string or nil means "not filtered".
type PublicEventFilter struct {
	// MemberID is the external id (the terminal-facing member id).
	MemberID string
	// SiteID is the site's public UUID, as text.
	SiteID string
	// Decision is one of the four closed outcomes, already validated upstream.
	Decision string
	From     *time.Time
	To       *time.Time
}

// PublicEventRow is one event with identifiers only. No names (they change and
// are the console's concern), no credential (its type says which modality a
// person enrolled with, which is biometric-adjacent and not this API's to
// disclose), no payload (application-defined, may carry anything).
type PublicEventRow struct {
	ID                int64
	PublicID          string
	EventType         string
	Application       string
	Decision          string
	ReasonCode        string
	Direction         string
	SitePublicID      string
	DeviceSerial      string
	PersonPublicID    string
	MemberID          string
	SubjectExternalID string
	OccurredAt        time.Time
	OccurredAtTrusted bool
	RecordedAt        time.Time
}

// EventsAfter reads one page of a company's events, newest first, strictly
// after `after` in listing order (that is, older).
//
// restrictedTo, when non-empty, is the credential's site restriction: only
// events at those sites are visible, and an event with no site (a company-level
// event) is NOT visible to a restricted credential -- it belongs to no site the
// credential was granted. nil means the credential reaches every site.
//
// The caller asks for one row more than it will serve, to learn whether a next
// page exists.
func EventsAfter(q Querier, companyID int64, restrictedTo []int64, filter PublicEventFilter,
	after *KeysetPosition, limit int) ([]PublicEventRow, error) {

	var (
		afterTS time.Time
		afterID int64
		paging  = after != nil
	)
	if paging {
		afterTS, afterID = after.CreatedAt, after.ID
	}
	var from, to sql.NullTime
	if filter.From != nil {
		from = sql.NullTime{Time: *filter.From, Valid: true}
	}
	if filter.To != nil {
		to = sql.NullTime{Time: *filter.To, Valid: true}
	}

	// Filters that name another resource resolve it INSIDE the tenant: a
	// site_id or member_id belonging to another company matches nothing, so
	// the answer is an empty page -- indistinguishable from a filter that
	// simply has no events, which is the section 18 rule for foreign ids.
	rows, err := q.Query(`
		SELECT e.id, e.public_id::text, e.event_type, COALESCE(e.application, ''),
		       e.decision, COALESCE(e.reason_code, ''), COALESCE(e.direction, ''),
		       COALESCE(s.public_id::text, ''), COALESCE(d.serial_number, ''),
		       COALESCE(p.public_id::text, ''), COALESCE(p.external_id, ''),
		       COALESCE(e.subject_external_id, ''),
		       e.occurred_at, e.occurred_at_trusted, e.recorded_at
		  FROM events e
		  LEFT JOIN sites   s ON s.id = e.site_id
		  LEFT JOIN devices d ON d.id = e.device_id
		  LEFT JOIN people  p ON p.id = e.person_id
		 WHERE e.company_id = $1
		   AND ($2::bigint[] IS NULL OR e.site_id = ANY($2::bigint[]))
		   AND ($3 = '' OR e.person_id = (SELECT id FROM people
		                                    WHERE company_id = $1 AND external_id = $3))
		   AND ($4 = '' OR e.site_id = (SELECT id FROM sites
		                                  WHERE company_id = $1 AND public_id::text = $4))
		   AND ($5 = '' OR e.decision = $5)
		   AND ($6::timestamptz IS NULL OR e.occurred_at >= $6)
		   AND ($7::timestamptz IS NULL OR e.occurred_at <  $7)
		   AND (NOT $8 OR (e.occurred_at, e.id) < ($9::timestamptz, $10::bigint))
		 ORDER BY e.occurred_at DESC, e.id DESC
		 LIMIT $11`,
		companyID, nullableIDs(restrictedTo), filter.MemberID, filter.SiteID, filter.Decision,
		from, to, paging, afterTS, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PublicEventRow, 0, limit)
	for rows.Next() {
		var r PublicEventRow
		if err := rows.Scan(&r.ID, &r.PublicID, &r.EventType, &r.Application,
			&r.Decision, &r.ReasonCode, &r.Direction,
			&r.SitePublicID, &r.DeviceSerial, &r.PersonPublicID, &r.MemberID,
			&r.SubjectExternalID, &r.OccurredAt, &r.OccurredAtTrusted, &r.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nullableIDs renders a restriction as a SQL array, or NULL for "none".
//
// An EMPTY restriction must not become an empty array: `= ANY('{}')` matches
// nothing, which would turn "unrestricted" into "sees nothing". NULL is the
// value the query tests for.
func nullableIDs(ids []int64) any {
	if len(ids) == 0 {
		return nil
	}
	return pq.Array(ids)
}
