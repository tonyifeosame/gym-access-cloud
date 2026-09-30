package service

import (
	"context"
	"errors"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Terminal is a terminal as an integration sees it. No credential of any kind.
type Terminal struct {
	Serial     string     `json:"serial"`
	Name       string     `json:"name"`
	SiteID     string     `json:"site_id"`
	SiteName   string     `json:"site_name"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	Enrollable bool       `json:"enrollable"`
}

// Enrollment is one fingerprint enrolment request, identifiers only: no names,
// no operator identity, and nothing biometric -- the template never leaves the
// terminal, and this object says only where and whether it was captured.
type Enrollment struct {
	ID             string     `json:"id"`
	MemberID       string     `json:"member_id"`
	Member         string     `json:"member"`
	Status         string     `json:"status"`
	TerminalSerial string     `json:"terminal_serial"`
	SiteID         string     `json:"site_id,omitempty"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

// EnrollmentInput is the body of POST /terminals/{serial}/enrollments.
type EnrollmentInput struct {
	MemberID         string
	ExpiresInSeconds *int
}

// EnrollmentService is the public face of terminal enrolment. It reuses the
// store the console drives -- database.StartFingerprintEnrollment and friends --
// so there is one enrolment state machine, one job shape and one set of rules;
// what differs is only who is asking and how far their credential reaches.
type EnrollmentService struct {
	timeout time.Duration
}

func NewEnrollmentService() *EnrollmentService {
	return &EnrollmentService{timeout: database.DefaultPublicStatementTimeout}
}

func restriction(tc *TenantContext) []int64 {
	if tc.RestrictedToSites() {
		return tc.SiteIDs()
	}
	return nil
}

// Terminals lists the terminals the credential can reach. Scope terminals:read.
func (s *EnrollmentService) Terminals(ctx context.Context, tc *TenantContext) ([]Terminal, error) {
	if err := tc.RequireScope(models.ScopeTerminalsRead); err != nil {
		return nil, err
	}
	var out []Terminal
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		rows, err := database.TerminalsInTenant(tx, tx.CompanyID(), restriction(tc))
		if err != nil {
			return ErrInternal(err)
		}
		out = make([]Terminal, 0, len(rows))
		for i := range rows {
			out = append(out, publicTerminal(&rows[i]))
		}
		return nil
	})
	return out, err
}

// Start puts one terminal into enrolment mode for one member. Scope
// enrollments:write. A live enrolment for the member is superseded, exactly as
// in the console, so a retry at another door simply moves it -- unless it is at
// a terminal outside a site-restricted credential's sites, which is refused and
// left running: a credential may not cancel what it cannot see.
func (s *EnrollmentService) Start(ctx context.Context, tc *TenantContext, serial string, in EnrollmentInput) (*Enrollment, error) {
	if err := tc.RequireScope(models.ScopeEnrollmentsWrite); err != nil {
		return nil, err
	}
	if in.MemberID == "" {
		return nil, ErrMissingField("member_id")
	}
	window := 0 // the store's default
	if in.ExpiresInSeconds != nil {
		if *in.ExpiresInSeconds < models.MinEnrollmentWindowSeconds || *in.ExpiresInSeconds > models.MaxEnrollmentWindowSeconds {
			return nil, ErrInvalidField("expires_in_seconds", "expires_in_seconds must be between 30 and 3600.")
		}
		window = *in.ExpiresInSeconds
	}

	var member *models.Member
	var deviceID int64
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		terminal, err := database.TerminalInTenant(tx, tx.CompanyID(), restriction(tc), serial)
		if errors.Is(err, models.ErrDeviceNotFound) {
			return ErrNotFound()
		}
		if err != nil {
			return ErrInternal(err)
		}
		if !terminal.Enrollable {
			return ErrTerminalNotEnrollable()
		}
		deviceID = terminal.DeviceID
		if member, err = database.MemberInTenant(tx, tx.CompanyID(), in.MemberID); err != nil {
			return notFoundOrInternal(err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	started, err := database.StartFingerprintEnrollment(database.StartEnrollmentInput{
		CompanyID:        tc.CompanyID(),
		DeviceID:         deviceID,
		ExternalID:       in.MemberID,
		ExpiresInSeconds: window,
		ActorEmail:       "integration:" + tc.KeyPrefix(),
		ReachableSiteIDs: restriction(tc),
	})
	if err != nil {
		return nil, enrollmentFailure(err)
	}
	return publicEnrollment(started, member.PublicID), nil
}

// Get reads a member's most recent enrolment. Scope members:read -- it is a
// question about the member. An enrolment at a site the credential cannot see
// is 404, as if there were none.
func (s *EnrollmentService) Get(ctx context.Context, tc *TenantContext, memberID string) (*Enrollment, error) {
	if err := tc.RequireScope(models.ScopeMembersRead); err != nil {
		return nil, err
	}
	member, err := s.visibleMember(ctx, tc, memberID)
	if err != nil {
		return nil, err
	}
	latest, err := database.LatestEnrollmentForPerson(tc.CompanyID(), memberID)
	if err != nil {
		return nil, enrollmentFailure(err)
	}
	if err := s.requireReachable(ctx, tc, latest.TerminalSerial); err != nil {
		return nil, err
	}
	return publicEnrollment(latest, member.PublicID), nil
}

// Cancel ends a member's live enrolment and cancels the terminal's job. Scope
// enrollments:write. Nothing live is 404; the terminal returns to checking
// fingers at its door.
func (s *EnrollmentService) Cancel(ctx context.Context, tc *TenantContext, memberID string) (*Enrollment, error) {
	if err := tc.RequireScope(models.ScopeEnrollmentsWrite); err != nil {
		return nil, err
	}
	member, err := s.visibleMember(ctx, tc, memberID)
	if err != nil {
		return nil, err
	}
	latest, err := database.LatestEnrollmentForPerson(tc.CompanyID(), memberID)
	if err != nil {
		return nil, enrollmentFailure(err)
	}
	if err := s.requireReachable(ctx, tc, latest.TerminalSerial); err != nil {
		return nil, err
	}
	cancelled, err := database.CancelFingerprintEnrollment(tc.CompanyID(), memberID, "cancelled through the public API")
	if err != nil {
		return nil, enrollmentFailure(err)
	}
	return publicEnrollment(cancelled, member.PublicID), nil
}

func (s *EnrollmentService) visibleMember(ctx context.Context, tc *TenantContext, memberID string) (*models.Member, error) {
	var member *models.Member
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		var err error
		if member, err = database.MemberInTenant(tx, tx.CompanyID(), memberID); err != nil {
			return notFoundOrInternal(err)
		}
		return nil
	})
	return member, err
}

// requireReachable hides an enrolment whose terminal is outside the credential's
// site restriction. An unrestricted credential reaches everything.
func (s *EnrollmentService) requireReachable(ctx context.Context, tc *TenantContext, serial string) error {
	if !tc.RestrictedToSites() {
		return nil
	}
	return database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		if _, err := database.TerminalInTenant(tx, tx.CompanyID(), tc.SiteIDs(), serial); err != nil {
			if errors.Is(err, models.ErrDeviceNotFound) {
				return ErrNotFound()
			}
			return ErrInternal(err)
		}
		return nil
	})
}

func enrollmentFailure(err error) error {
	switch {
	case errors.Is(err, models.ErrPersonNotFound), errors.Is(err, models.ErrDeviceNotFound),
		errors.Is(err, database.ErrEnrollmentNotFound):
		return ErrNotFound()
	case errors.Is(err, database.ErrTerminalNotEnrollable):
		return ErrTerminalNotEnrollable()
	case errors.Is(err, database.ErrLiveEnrollmentOutOfReach):
		return ErrEnrollmentOutOfReach()
	}
	return ErrInternal(err)
}

func publicTerminal(t *database.TenantTerminal) Terminal {
	return Terminal{Serial: t.Serial, Name: t.Name, SiteID: t.SitePublicID, SiteName: t.SiteName,
		Status: t.Status, LastSeenAt: t.LastSeenAt, Enrollable: t.Enrollable}
}

func publicEnrollment(e *models.ConsoleEnrollment, memberPublicID string) *Enrollment {
	return &Enrollment{
		ID: e.ID, MemberID: e.ExternalID, Member: memberPublicID, Status: e.Status,
		TerminalSerial: e.TerminalSerial, SiteID: e.SitePublicID, Error: e.ErrorMessage,
		CreatedAt: e.CreatedAt, ExpiresAt: e.ExpiresAt, StartedAt: e.StartedAt, CompletedAt: e.CompletedAt,
	}
}
