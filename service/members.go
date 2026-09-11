package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Members, as the public API will expose them.
//
// ---------------------------------------------------------------------------
// WHAT A SERVICE METHOD DOES, AND WHAT IT LEAVES TO THE HANDLER
// ---------------------------------------------------------------------------
//
// Every method here: checks the scope on the TenantContext, opens a
// tenant-scoped transaction (statement timeout, tenant GUC), runs queries that
// filter on tc.CompanyID(), and maps every failure to a service.Error carrying
// a registry code. It never sees an HTTP request, never reads a header, and
// takes the tenant from nothing but the context it was handed.
//
// The handler above it (handlers/public_api.go and public_api_writes.go) parses the request into a
// MemberInput or PageRequest, authenticate into a TenantContext, call one
// method, and map the result or the *Error to a response. That is all.
//
// THE SAME WRITE PATH AS THE CONSOLE AND THE LEGACY API. Create, Update and
// Delete run database.CreateMemberTx / UpdateMemberTx / DeleteMemberTx, which
// are the bodies the existing wrappers call -- the default access grant, the
// placement REMOVING mark and the sync fan-out all happen exactly as they do
// for an operator, so a terminal cannot tell which door a person came in by.

// Member is the public projection of a person.
//
// NO FINGERPRINT FIELD, and there must never be one -- the same construction
// models.MemberResponse uses to keep credential material out of the legacy
// site-key API. The struct cannot carry what it must not disclose.
type Member struct {
	ID             string    `json:"id"`
	MemberID       string    `json:"member_id"`
	FullName       string    `json:"full_name"`
	MembershipType string    `json:"membership_type"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// MemberInput is what Create and Update accept.
//
// THERE IS NO TENANT FIELD ON THIS STRUCT, and a test enumerates the package to
// keep it that way. Active is a pointer so that Update can tell "not
// mentioned" from "set to false"; Create treats absent as active.
type MemberInput struct {
	MemberID       string
	FullName       string
	MembershipType string
	Active         *bool
}

// DefaultMembershipType is what a member created without one gets. The public
// contract makes the field optional (section 18); the legacy route requires it.
const DefaultMembershipType = "STANDARD"

// MaxMemberNameLength bounds full_name. The column is unbounded TEXT; this is
// the service's own limit so that a terminal's display is not the first thing
// to discover a 10 KB name.
const MaxMemberNameLength = 200

// MemberService is the member operations. Construct with NewMemberService.
type MemberService struct {
	signer  *models.CursorSigner
	timeout time.Duration
}

// NewMemberService wires the cursor signer the list operation needs.
//
// signer may be nil for a service that will never list; List then answers
// internal_error rather than serving an unsigned cursor.
func NewMemberService(signer *models.CursorSigner) *MemberService {
	return &MemberService{signer: signer, timeout: database.DefaultPublicStatementTimeout}
}

// membersListFilter is the fingerprint a members cursor is bound to. The list
// has no filters yet, so it is a constant -- but it is still checked, so a
// cursor from a future filtered list cannot be replayed against this one.
const membersListFilter = "members:v1"

// Get reads one member by member id. Requires members:read.
func (s *MemberService) Get(ctx context.Context, tc *TenantContext, memberID string) (*Member, error) {
	if err := tc.RequireScope(models.ScopeMembersRead); err != nil {
		return nil, err
	}
	var out *Member
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		m, err := database.MemberInTenant(tx, tx.CompanyID(), memberID)
		if err != nil {
			return notFoundOrInternal(err)
		}
		out = publicMember(m)
		return nil
	})
	return out, err
}

// List reads one page of members, newest first. Requires members:read.
func (s *MemberService) List(ctx context.Context, tc *TenantContext, page PageRequest) (*Page[Member], error) {
	if err := tc.RequireScope(models.ScopeMembersRead); err != nil {
		return nil, err
	}
	size, err := pageSize(page.Limit)
	if err != nil {
		return nil, err
	}
	after, err := decodeCursor(s.signer, tc, page.Cursor, membersListFilter, 0)
	if err != nil {
		return nil, err
	}

	var rows []models.Member
	err = database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		// One more than the page, to learn whether a next page exists without
		// a second query.
		var err error
		rows, err = database.MembersAfter(tx, tx.CompanyID(), after, size+1)
		if err != nil {
			return ErrInternal(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := &Page[Member]{Items: make([]Member, 0, len(rows))}
	if len(rows) > size {
		rows = rows[:size]
		out.HasMore = true
	}
	for i := range rows {
		out.Items = append(out.Items, *publicMember(&rows[i]))
	}
	if out.HasMore {
		last := rows[len(rows)-1]
		out.NextCursor, err = encodeCursor(s.signer, tc, membersListFilter,
			database.KeysetPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Create adds a member. Requires members:write.
func (s *MemberService) Create(ctx context.Context, tc *TenantContext, in MemberInput) (*Member, error) {
	if err := tc.RequireScope(models.ScopeMembersWrite); err != nil {
		return nil, err
	}
	if err := validateMemberInput(in, true); err != nil {
		return nil, err
	}
	// FW-09 at the boundary, so the failure names the field rather than
	// arriving from the store as a generic error. The id is validated AS
	// SUPPLIED and stored as supplied: it is an identifier, and whitespace in
	// one is a refusal, not something to tidy.
	if err := models.ValidateExternalID(in.MemberID); err != nil {
		return nil, ErrMemberIDUnusable()
	}

	// DEFAULTS ARE THE PUBLIC CONTRACT (API_SPEC.md section 18), and they differ
	// from the legacy site-key route on purpose: membership_type is optional
	// and defaults to STANDARD, and a member is active unless told otherwise.
	membershipType := strings.TrimSpace(in.MembershipType)
	if membershipType == "" {
		membershipType = DefaultMembershipType
	}
	member := models.Member{
		MemberID:       in.MemberID,
		FullName:       strings.TrimSpace(in.FullName),
		MembershipType: membershipType,
		Active:         in.Active == nil || *in.Active,
	}
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		if err := database.CreateMemberTx(tx.Tx, tx.CompanyID(), &member); err != nil {
			return writeFailure(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return publicMember(&member), nil
}

// Update changes a member's name, type or active flag. Requires members:write.
// The member id itself is the lookup key and cannot be changed.
func (s *MemberService) Update(ctx context.Context, tc *TenantContext, memberID string, in MemberInput) (*Member, error) {
	if err := tc.RequireScope(models.ScopeMembersWrite); err != nil {
		return nil, err
	}
	if err := validateMemberInput(in, false); err != nil {
		return nil, err
	}

	var out *Member
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		current, err := database.MemberInTenant(tx, tx.CompanyID(), memberID)
		if err != nil {
			return notFoundOrInternal(err)
		}
		// Absent fields keep their value. The legacy PUT replaces the whole
		// record; an integrator sending {"active": false} should not have to
		// resend the name to keep it.
		if in.FullName != "" {
			current.FullName = strings.TrimSpace(in.FullName)
		}
		if in.MembershipType != "" {
			current.MembershipType = strings.TrimSpace(in.MembershipType)
		}
		if in.Active != nil {
			current.Active = *in.Active
		}
		if err := database.UpdateMemberTx(tx.Tx, tx.CompanyID(), current); err != nil {
			return writeFailure(err)
		}
		out = publicMember(current)
		return nil
	})
	return out, err
}

// Delete soft-deletes a member and fans the removal out to terminals.
// Requires members:write.
//
// IDEMPOTENT, AND SILENT ABOUT WHY. The contract (API_SPEC.md section 18) is
// 204 for a member that was removed, a member that was already removed, and a
// member that never existed -- including one that exists in another company.
// A client retrying a delete must get the same answer twice, and a caller must
// not be able to probe another tenant's ids by the difference between "gone"
// and "never here". The returned bool says whether THIS call removed a row, so
// the handler can audit the removal without auditing a no-op.
func (s *MemberService) Delete(ctx context.Context, tc *TenantContext, memberID string) (bool, error) {
	if err := tc.RequireScope(models.ScopeMembersWrite); err != nil {
		return false, err
	}
	removed := false
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		deleted, err := database.DeleteMemberTx(tx.Tx, tx.CompanyID(), memberID)
		if err != nil {
			return writeFailure(err)
		}
		removed = deleted
		return nil
	})
	return removed, err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func publicMember(m *models.Member) *Member {
	return &Member{
		ID:             m.PublicID,
		MemberID:       m.MemberID,
		FullName:       m.FullName,
		MembershipType: m.MembershipType,
		Active:         m.Active,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

// validateMemberInput checks shape. `creating` makes the identifying and
// descriptive fields required; an update may name only what it changes.
func validateMemberInput(in MemberInput, creating bool) error {
	if creating {
		// Untrimmed on purpose: an id of spaces is not "missing", it is
		// unusable, and ValidateExternalID says so.
		if in.MemberID == "" {
			return ErrMissingField("member_id")
		}
		if strings.TrimSpace(in.FullName) == "" {
			return ErrMissingField("full_name")
		}
	}
	if len(in.FullName) > MaxMemberNameLength {
		return ErrInvalidField("full_name", "full_name must be at most 200 characters.")
	}
	if len(in.MembershipType) > 50 {
		return ErrInvalidField("membership_type", "membership_type must be at most 50 characters.")
	}
	return nil
}

// notFoundOrInternal is the lookup rule in one place: a missing row -- in
// this tenant or any other -- is not-found; anything else is a server fault.
func notFoundOrInternal(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound()
	}
	return ErrInternal(err)
}

// writeFailure maps what the member write path can raise.
func writeFailure(err error) error {
	var overflow *database.RosterCapacityError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound()
	case errors.Is(err, models.ErrExternalIDUnusable), errors.Is(err, models.ErrExternalIDRequired):
		return ErrMemberIDUnusable()
	case errors.As(err, &overflow):
		return ErrRosterOverCapacity(overflow.Serial, overflow.RosterSize, overflow.Capacity)
	case database.IsUniqueViolation(err):
		return ErrMemberIDExists()
	default:
		return ErrInternal(err)
	}
}
