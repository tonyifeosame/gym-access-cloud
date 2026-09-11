package service

import (
	"context"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Events, as the public API exposes them (API_SPEC.md section 18, "Events").
//
// WHAT AN INTEGRATOR GETS is the platform's record of what happened at a door:
// who (by member id), where (by site and terminal), when (two times, and the
// difference matters), the outcome and the machine-readable reason. WHAT THEY
// DO NOT GET, by construction of the struct: the credential that was presented
// (its type names a biometric modality), the application payload (opaque and
// unbounded), and any display name (the console's concern, and a rename would
// otherwise rewrite history).
//
// THE SITE RESTRICTION IS A BOUND, NOT A FILTER. A credential restricted to two
// sites sees those sites' events and nothing else; a site_id filter outside
// the restriction yields an empty page rather than a refusal, so a restricted
// caller cannot use the difference to learn which other sites exist.

// Event is the public projection of one row of the event trail.
type Event struct {
	ID          string `json:"id"`
	EventType   string `json:"event_type"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
	Direction   string `json:"direction,omitempty"`
	Application string `json:"application,omitempty"`

	// MemberID is the terminal-facing id of the person the platform matched;
	// Member is that person's public UUID. Both absent when the presentation
	// matched nobody. Subject is what the terminal actually read, kept so an
	// unmatched attempt is still traceable.
	MemberID string `json:"member_id,omitempty"`
	Member   string `json:"member,omitempty"`
	Subject  string `json:"subject,omitempty"`

	SiteID         string `json:"site_id,omitempty"`
	TerminalSerial string `json:"terminal_serial,omitempty"`

	OccurredAt        time.Time `json:"occurred_at"`
	OccurredAtTrusted bool      `json:"occurred_at_trusted"`
	RecordedAt        time.Time `json:"recorded_at"`
}

// EventFilter is what List accepts beyond the page. Validated by the caller;
// the service binds it into the cursor so a page cannot be replayed against
// different filters.
type EventFilter struct {
	MemberID string
	SiteID   string
	Decision string
	From     *time.Time
	To       *time.Time
}

// EventService is the event operations. Construct with NewEventService.
type EventService struct {
	signer  *models.CursorSigner
	timeout time.Duration
}

// NewEventService wires the cursor signer the list needs.
func NewEventService(signer *models.CursorSigner) *EventService {
	return &EventService{signer: signer, timeout: database.DefaultPublicStatementTimeout}
}

// List reads one page of events, newest first. Requires events:read.
func (s *EventService) List(ctx context.Context, tc *TenantContext, page PageRequest,
	filter EventFilter) (*Page[Event], error) {

	if err := tc.RequireScope(models.ScopeEventsRead); err != nil {
		return nil, err
	}
	size, err := pageSize(page.Limit)
	if err != nil {
		return nil, err
	}
	fingerprint := eventsFilterFingerprint(filter)
	after, err := decodeCursor(s.signer, tc, page.Cursor, fingerprint, 0)
	if err != nil {
		return nil, err
	}

	var rows []database.PublicEventRow
	err = database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		var err error
		rows, err = database.EventsAfter(tx, tx.CompanyID(), tc.SiteIDs(), database.PublicEventFilter{
			MemberID: filter.MemberID,
			SiteID:   filter.SiteID,
			Decision: filter.Decision,
			From:     filter.From,
			To:       filter.To,
		}, after, size+1)
		if err != nil {
			return ErrInternal(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := &Page[Event]{Items: make([]Event, 0, len(rows))}
	if len(rows) > size {
		rows = rows[:size]
		out.HasMore = true
	}
	for i := range rows {
		out.Items = append(out.Items, publicEvent(&rows[i]))
	}
	if out.HasMore {
		last := rows[len(rows)-1]
		out.NextCursor, err = encodeCursor(s.signer, tc, fingerprint,
			database.KeysetPosition{CreatedAt: last.OccurredAt, ID: last.ID})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// eventsFilterFingerprint binds the filters into the cursor. The version tag
// keeps an events cursor from ever authenticating against a members list.
func eventsFilterFingerprint(f EventFilter) string {
	filters := map[string]string{
		"member_id": f.MemberID,
		"site_id":   f.SiteID,
		"decision":  f.Decision,
	}
	if f.From != nil {
		filters["from"] = f.From.UTC().Format(time.RFC3339Nano)
	}
	if f.To != nil {
		filters["to"] = f.To.UTC().Format(time.RFC3339Nano)
	}
	return "events:v1:" + models.FilterFingerprint(filters)
}

func publicEvent(r *database.PublicEventRow) Event {
	return Event{
		ID:                r.PublicID,
		EventType:         r.EventType,
		Decision:          r.Decision,
		Reason:            r.ReasonCode,
		Direction:         r.Direction,
		Application:       r.Application,
		MemberID:          r.MemberID,
		Member:            r.PersonPublicID,
		Subject:           r.SubjectExternalID,
		SiteID:            r.SitePublicID,
		TerminalSerial:    r.DeviceSerial,
		OccurredAt:        r.OccurredAt,
		OccurredAtTrusted: r.OccurredAtTrusted,
		RecordedAt:        r.RecordedAt,
	}
}
