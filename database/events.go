package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// The typed event trail (migrations/013_events_and_audit.sql).
//
// APP-04 and SEC-08, which are one piece of work: there was no generic event
// model, and the only thing resembling one -- `access_logs` -- was unreachable
// from an operator session. An attendance record, a check-in, a tamper alarm and
// a verification all had to be forced through a table whose columns are
// `granted` and `door_id`, or not recorded at all.
//
// ---------------------------------------------------------------------------
// WHY NOT JUST WIDEN access_logs
// ---------------------------------------------------------------------------
//
// Because its shape encodes an assumption this platform does not get to make.
// `granted BOOLEAN` has no answer for "the person clocked in" -- there is
// nothing to grant, the event IS the outcome -- and `door_id` names a noun a
// warehouse, a clinic and a depot counter do not have. Adding nullable columns
// until it fits would leave every application reading a table where most columns
// are meaningless for it and the meaningful ones are named after somebody else's
// product.
//
// `events` carries decision (four values, one of which is RECORDED for the
// no-outcome case), an open event_type, an application, a direction and an
// opaque payload. That is enough for the five unbuilt capabilities without a
// further migration, which is the test APP-04 set.
//
// access_logs is NOT dropped. Deployed firmware uploads to it, the frozen
// contract in the compatibility policy includes it, and the device path writes
// BOTH -- see RecordAccessEvent's caller. The event trail is the model going
// forward; the log is the wire format.
//
// ---------------------------------------------------------------------------
// IDEMPOTENCY
// ---------------------------------------------------------------------------
//
// public_id is the device-generated key and the insert is ON CONFLICT DO
// NOTHING, matching what access_logs already established: a terminal that
// retried an upload whose response it never heard must not produce a second
// event. A duplicate is a success, not an error -- the caller asked for the
// event to exist and it does.

// AccessEvent is one thing to record.
//
// Constructed by the caller from an authorization decision plus the terminal
// context, rather than derived here, because the same struct records events that
// never went through Authorize at all -- a tamper alarm, a terminal coming
// online, an enrolment.
type AccessEvent struct {
	// PublicID is the device's idempotency key. Empty means the server generates
	// one, which is right for events the server itself originates.
	PublicID string

	CompanyID int64
	SiteID    int64
	DeviceID  int64

	// PersonID is 0 for a presentation that matched nobody. SubjectExternalID
	// keeps what the terminal read either way.
	PersonID          int64
	SubjectExternalID string
	CredentialID      int64

	EventType   string
	Application string
	Decision    string
	ReasonCode  string
	Direction   string

	Payload map[string]any

	// OccurredAt is when it happened AT THE TERMINAL. Zero means the server
	// stamps its own arrival time and marks the result untrusted, which is the
	// honest answer for a terminal that has never reached NTP.
	OccurredAt        time.Time
	OccurredAtTrusted bool
}

// RecordAccessEvent writes one event and returns its public id.
//
// FAILS LOUDLY, UNLIKE THE AUDIT TRAIL. database/audit.go swallows write errors
// deliberately, because an operator mutation must not fail because its audit row
// could not be written. This is the opposite case and the reasoning inverts: an
// access event is the record that somebody was admitted or refused at a door,
// and a caller that gets an error can still decide what to do about it. The
// device path logs and continues -- the person is already through -- but it
// knows the record was lost, which "best effort, silently" would not tell it.
func RecordAccessEvent(event AccessEvent) (string, error) {
	if event.EventType == "" {
		return "", errors.New("an event must carry a type")
	}
	if event.Decision == "" {
		event.Decision = models.DecisionRecorded
	}
	if !models.IsEventDecision(event.Decision) {
		return "", errors.New("unknown event decision " + event.Decision)
	}

	occurredAt := event.OccurredAt
	trusted := event.OccurredAtTrusted
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
		trusted = false
	}

	var payload []byte
	if len(event.Payload) > 0 {
		encoded, err := json.Marshal(event.Payload)
		if err != nil {
			// The payload is application detail; the event itself is the
			// load-bearing part and must not be lost with it.
			encoded = []byte(`{"_error":"payload could not be encoded"}`)
		}
		payload = encoded
	}

	// COALESCE on the public id rather than two query variants: an empty string
	// becomes a generated uuid, a supplied one is honoured, and the ON CONFLICT
	// makes the retry of a supplied one a no-op.
	var publicID string
	err := DB.QueryRow(`
		INSERT INTO events
		    (public_id, company_id, site_id, device_id, person_id,
		     subject_external_id, credential_id, event_type, application,
		     decision, reason_code, direction, payload,
		     occurred_at, occurred_at_trusted)
		VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()),
		        $2, NULLIF($3, 0)::bigint, NULLIF($4, 0)::bigint, NULLIF($5, 0)::bigint,
		        NULLIF($6, ''), NULLIF($7, 0)::bigint, $8, NULLIF($9, ''),
		        $10, NULLIF($11, ''), NULLIF($12, ''), $13::jsonb,
		        $14, $15)
		ON CONFLICT (public_id) DO NOTHING
		RETURNING public_id`,
		event.PublicID, event.CompanyID, event.SiteID, event.DeviceID, event.PersonID,
		event.SubjectExternalID, event.CredentialID, event.EventType, event.Application,
		event.Decision, event.ReasonCode, event.Direction, payload,
		occurredAt, trusted).Scan(&publicID)

	if errors.Is(err, sql.ErrNoRows) {
		// The conflict fired: this event already exists. The caller's intent is
		// satisfied, so this is a success and the existing id is the answer.
		return event.PublicID, nil
	}
	if err != nil {
		return "", err
	}
	return publicID, nil
}

// RecordAccessDecision is the paired call to Authorize: it turns a decision into
// the event that proves it was made.
//
// Kept beside Authorize rather than inside it because Authorize also answers
// "would this be allowed" for the console, and a preview that wrote a door event
// would put fiction in the trail.
func RecordAccessDecision(ctx AccessEventContext, decision *models.AccessDecision) (string, error) {
	eventType := models.EventAccessDenied
	eventDecision := models.DecisionDenied
	if decision.Granted {
		eventType = models.EventAccessGranted
		eventDecision = models.DecisionGranted
	}

	// The person and credential public ids on the decision are resolved back to
	// row ids here. A decision that matched nobody carries neither, and the
	// event records the external id the terminal read instead.
	var personID, credentialID int64
	if decision.PersonID != "" {
		personID = ctx.PersonID
	}
	if decision.CredentialID != "" {
		credentialID = ctx.CredentialID
	}

	subject := decision.ExternalID
	if subject == "" {
		subject = ctx.SubjectExternalID
	}

	return RecordAccessEvent(AccessEvent{
		PublicID:          ctx.PublicID,
		CompanyID:         ctx.CompanyID,
		SiteID:            ctx.SiteID,
		DeviceID:          ctx.DeviceID,
		PersonID:          personID,
		SubjectExternalID: subject,
		CredentialID:      credentialID,
		EventType:         eventType,
		Application:       decision.Application,
		Decision:          eventDecision,
		ReasonCode:        decision.Reason,
		Direction:         ctx.Direction,
		Payload:           ctx.Payload,
		OccurredAt:        ctx.OccurredAt,
		OccurredAtTrusted: ctx.OccurredAtTrusted,
	})
}

// AccessEventContext is the terminal-side detail a decision does not carry.
type AccessEventContext struct {
	PublicID  string
	CompanyID int64
	SiteID    int64
	DeviceID  int64

	// PersonID and CredentialID are the row ids behind the decision's public
	// ids, which the caller already has from resolving them.
	PersonID     int64
	CredentialID int64

	SubjectExternalID string
	Direction         string
	Payload           map[string]any

	OccurredAt        time.Time
	OccurredAtTrusted bool
}

// LogEventFailure records that an event could not be written, in the one format
// an operator grepping the log can find.
func LogEventFailure(companyID, deviceID int64, eventType string, err error) {
	log.Printf("EVENT WRITE FAILED type=%s company=%d device=%d: %v",
		eventType, companyID, deviceID, err)
}

// ---------------------------------------------------------------------------
// Reading the trail
// ---------------------------------------------------------------------------

// EventQuery filters the console's event list.
//
// EVERY FILTER IS OPTIONAL AND EVERY ONE IS APPLIED INSIDE THE COMPANY. SiteIDs
// is not a filter the caller chooses -- it is the operator's site grant, applied
// by the handler, and an empty slice with Scoped set means "granted no sites"
// rather than "no filter". Conflating those two would turn a scoped operator
// into an unscoped one.
type EventQuery struct {
	Limit  int
	Offset int

	// Scoped reports whether SiteIDs is a grant restriction at all. An
	// unscoped operator (ADMIN/OWNER) sets Scoped false and sees the company.
	Scoped  bool
	SiteIDs []int64

	SiteID       string
	DeviceSerial string
	ExternalID   string
	PersonID     string
	EventType    string
	Application  string
	Decision     string
	Direction    string

	From *time.Time
	To   *time.Time

	// Query is free text over the person's name and the external id the
	// terminal read.
	Query string
}

// Event list bounds. An event trail grows with every presentation at every
// terminal, so an unbounded limit would let one request pull a year of door
// traffic into memory.
const (
	defaultEventLimit = 50
	maxEventLimit     = 500
)

// ListEvents returns one page of the event trail, newest first.
func ListEvents(companyID int64, query EventQuery) (*models.ConsoleEventPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = defaultEventLimit
	}
	if limit > maxEventLimit {
		limit = maxEventLimit
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	// The filter list is built up rather than written out, because thirteen
	// optional filters as one query string with thirteen `($n IS NULL OR ...)`
	// clauses is a query no index can serve and nobody can read.
	var (
		where = []string{"e.company_id = $1"}
		args  = []any{companyID}
	)
	// `?` in a clause is replaced by the positional parameter the value lands
	// on. $1 is always companyID, so a subquery that has to re-scope itself to
	// the tenant writes $1 directly.
	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)), 1))
	}

	// The site grant. Applied first because it is the boundary, not a
	// convenience filter.
	if query.Scoped {
		if len(query.SiteIDs) == 0 {
			// Granted no sites: the honest answer is an empty page, not the
			// company's whole trail.
			return &models.ConsoleEventPage{
				Limit: limit, Offset: offset, Events: []models.Event{},
			}, nil
		}
		add("e.site_id = ANY(?)", pq.Array(query.SiteIDs))
	}

	if query.SiteID != "" {
		if !looksLikeUUID(query.SiteID) {
			return emptyEventPage(limit, offset), nil
		}
		add("e.site_id = (SELECT id FROM sites "+
			"WHERE public_id = ?::uuid AND company_id = $1)", query.SiteID)
	}
	if query.DeviceSerial != "" {
		add("e.device_id = (SELECT d.id FROM devices d JOIN sites s ON s.id = d.site_id "+
			"WHERE d.serial_number = ? AND s.company_id = $1)", query.DeviceSerial)
	}
	if query.PersonID != "" {
		if !looksLikeUUID(query.PersonID) {
			return emptyEventPage(limit, offset), nil
		}
		add("e.person_id = (SELECT id FROM people "+
			"WHERE public_id = ?::uuid AND company_id = $1)", query.PersonID)
	}
	if query.ExternalID != "" {
		add("e.subject_external_id = ?", query.ExternalID)
	}
	if query.EventType != "" {
		add("e.event_type = ?", strings.ToUpper(query.EventType))
	}
	if query.Application != "" {
		add("e.application = ?", strings.ToUpper(query.Application))
	}
	if query.Decision != "" {
		add("e.decision = ?", strings.ToUpper(query.Decision))
	}
	if query.Direction != "" {
		add("e.direction = ?", strings.ToUpper(query.Direction))
	}

	// Time bounds are on occurred_at, not recorded_at: an operator asking what
	// happened on Tuesday means at the door, not in the database. An event
	// queued through an outage and uploaded on Wednesday belongs to Tuesday.
	if query.From != nil {
		add("e.occurred_at >= ?", *query.From)
	}
	if query.To != nil {
		add("e.occurred_at < ?", *query.To)
	}

	if term := strings.TrimSpace(query.Query); term != "" {
		pattern := "%" + escapeLikePattern(term) + "%"
		args = append(args, pattern)
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(e.subject_external_id ILIKE "+n+" ESCAPE '\\' "+
			"OR p.external_id ILIKE "+n+" ESCAPE '\\' "+
			"OR p.full_name ILIKE "+n+" ESCAPE '\\')")
	}

	clause := strings.Join(where, " AND ")

	from := `
		  FROM events e
		  LEFT JOIN people p ON p.id = e.person_id
		  LEFT JOIN sites s ON s.id = e.site_id
		  LEFT JOIN devices d ON d.id = e.device_id
		  LEFT JOIN credentials cr ON cr.id = e.credential_id
		 WHERE ` + clause

	var total int
	if err := DB.QueryRow(`SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, err
	}

	// public_id breaks ties on occurred_at. Two events stamped in the same
	// millisecond by the same terminal would otherwise page unstably -- the
	// same row appearing on page one and page two, and another appearing on
	// neither.
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := DB.Query(`
		SELECT e.public_id, e.event_type, e.application, e.decision, e.reason_code,
		       e.direction, s.site_name, d.serial_number, d.device_name,
		       p.public_id, p.full_name,
		       e.subject_external_id, cr.public_id, cr.credential_type,
		       e.payload, e.occurred_at, e.recorded_at, e.occurred_at_trusted`+
		from+`
		 ORDER BY e.occurred_at DESC, e.public_id DESC
		 LIMIT $`+strconv.Itoa(len(args)+1)+` OFFSET $`+strconv.Itoa(len(args)+2), pageArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]models.Event, 0, limit)
	for rows.Next() {
		var (
			event          models.Event
			application    sql.NullString
			reason         sql.NullString
			direction      sql.NullString
			siteName       sql.NullString
			serial         sql.NullString
			deviceName     sql.NullString
			personID       sql.NullString
			personName     sql.NullString
			externalID     sql.NullString
			credentialID   sql.NullString
			credentialType sql.NullString
			payload        []byte
		)
		if err := rows.Scan(&event.ID, &event.Type, &application, &event.Decision, &reason,
			&direction, &siteName, &serial, &deviceName,
			&personID, &personName, &externalID, &credentialID, &credentialType,
			&payload, &event.OccurredAt, &event.RecordedAt, &event.OccurredAtTrusted); err != nil {
			return nil, err
		}

		event.Application = application.String
		event.Reason = reason.String
		event.Direction = direction.String
		event.SiteName = siteName.String
		event.DeviceSerial = serial.String
		event.DeviceName = deviceName.String
		event.PersonID = personID.String
		event.PersonName = strings.TrimSpace(personName.String)
		event.SubjectExternalID = externalID.String
		event.CredentialID = credentialID.String
		event.CredentialType = credentialType.String
		if len(payload) > 0 {
			event.Payload = json.RawMessage(payload)
		}

		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &models.ConsoleEventPage{
		Count:   len(events),
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(events) < total,
		Events:  events,
	}, nil
}

func emptyEventPage(limit, offset int) *models.ConsoleEventPage {
	return &models.ConsoleEventPage{Limit: limit, Offset: offset, Events: []models.Event{}}
}
