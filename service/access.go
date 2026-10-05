package service

import (
	"context"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// A member's access standing, as the public API exposes it (API_SPEC.md
// section 18, "Access").
//
// RULES AND THEIR STANDING, NOT A VERDICT. Whether a person gets in is decided
// at a door, at a moment, by the authorization engine with the terminal, its
// site's timezone and its schedule in hand; a yes/no with none of those is a
// number that would be wrong somewhere. What an integrator can usefully read
// is the set of rules that decision draws on, each with whether it is in force
// right now -- the same reading the console gives an operator.
//
// NO CREDENTIALS. Which fingerprints or cards a person has enrolled is
// biometric-adjacent and belongs to the platform, not to this contract.

// AccessRule is one permission, projected.
type AccessRule struct {
	ID string `json:"id"`
	// Scope is COMPANY, SITE or TERMINAL. SiteID is set for SITE, TerminalSerial
	// for TERMINAL; both absent for COMPANY.
	Scope          string `json:"scope"`
	SiteID         string `json:"site_id,omitempty"`
	TerminalSerial string `json:"terminal_serial,omitempty"`
	// Effect is ALLOW or DENY.
	Effect string `json:"effect"`
	// Application narrows the rule to one capability; absent means every one.
	Application string `json:"application,omitempty"`
	// ScheduleID narrows the rule to a schedule's windows; absent means always.
	ScheduleID string     `json:"schedule_id,omitempty"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	EndsAt     *time.Time `json:"ends_at,omitempty"`
	// Standing is IN_FORCE, NOT_YET, EXPIRED or INACTIVE, as of the request.
	Standing string `json:"standing"`
}

// MemberAccess is the answer to GET /members/{member_id}/access.
type MemberAccess struct {
	// PersonPublicID is for the audit line only; never serialized.
	PersonPublicID string `json:"-"`

	MemberID string       `json:"member_id"`
	Active   bool         `json:"active"`
	Rules    []AccessRule `json:"rules"`
}

// Rule standings. The same four words the console uses.
const (
	StandingInForce  = "IN_FORCE"
	StandingNotYet   = "NOT_YET"
	StandingExpired  = "EXPIRED"
	StandingInactive = "INACTIVE"
)

// AccessService is the access-standing operations. Construct with NewAccessService.
type AccessService struct {
	timeout time.Duration
}

// NewAccessService builds the service.
func NewAccessService() *AccessService {
	return &AccessService{timeout: database.DefaultPublicStatementTimeout}
}

// Rules reads a member's access rules with their standing. Requires
// access:read. Not-found when the member is unknown, deleted, or in another
// company -- one answer for all three.
//
// Rules are company-wide: a SITE-scoped rule for a site outside a credential's
// restriction is still listed, because it describes the person, not that
// site's data. Section 18 says so.
func (s *AccessService) Rules(ctx context.Context, tc *TenantContext, memberID string) (*MemberAccess, error) {
	if err := tc.RequireScope(models.ScopeAccessRead); err != nil {
		return nil, err
	}
	var out *MemberAccess
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		member, err := database.MemberInTenant(tx, tx.CompanyID(), memberID)
		if err != nil {
			return notFoundOrInternal(err)
		}
		rules, err := database.ListPersonPermissionsTx(tx, tx.CompanyID(), memberID)
		if err != nil {
			return ErrInternal(err)
		}
		now := time.Now()
		out = &MemberAccess{MemberID: member.MemberID, Active: member.Active, Rules: make([]AccessRule, 0, len(rules))}
		for i := range rules {
			out.Rules = append(out.Rules, publicRule(&rules[i], now))
		}
		return nil
	})
	return out, err
}

func publicRule(p *models.Permission, now time.Time) AccessRule {
	return AccessRule{
		ID:             p.ID,
		Scope:          p.ScopeType,
		SiteID:         p.SiteID,
		TerminalSerial: p.DeviceSerial,
		Effect:         p.Effect,
		Application:    p.Application,
		ScheduleID:     p.ScheduleID,
		StartsAt:       p.StartsAt,
		EndsAt:         p.EndsAt,
		Standing:       standingOf(p, now),
	}
}

// standingOf mirrors the console's reading (web/src/pages/access/accessVocabulary.ts):
// inactive first, then not-yet, then expired, else in force.
func standingOf(p *models.Permission, now time.Time) string {
	switch {
	case !p.Active:
		return StandingInactive
	case p.StartsAt != nil && p.StartsAt.After(now):
		return StandingNotYet
	case p.EndsAt != nil && !p.EndsAt.After(now):
		return StandingExpired
	default:
		return StandingInForce
	}
}

// integrationGrant is what this credential may give: its lineage and its sites.
func integrationGrant(tc *TenantContext) *database.IntegrationGrant {
	return &database.IntegrationGrant{LineageID: tc.LineageID(), SiteIDs: restriction(tc)}
}

// Grant gives a member this integration created the standard access its
// credential covers (access:write). Idempotent: already in place is a no-op.
// A member the integration cannot prove it created is not-found. Terminal
// rosters are brought up to date in the same transaction. Returns the
// integration's own rules and how many were added.
func (s *AccessService) Grant(ctx context.Context, tc *TenantContext, memberID string) (*MemberAccess, int, error) {
	return s.changeManagedAccess(ctx, tc, memberID, database.GrantIntegrationAccessTx)
}

// Revoke removes only the rules this integration wrote for the member, within
// its credential's reach; operator rules are never touched.
func (s *AccessService) Revoke(ctx context.Context, tc *TenantContext, memberID string) (*MemberAccess, int, error) {
	return s.changeManagedAccess(ctx, tc, memberID, database.RevokeIntegrationAccessTx)
}

func (s *AccessService) changeManagedAccess(ctx context.Context, tc *TenantContext, memberID string,
	change func(database.Querier, int64, int64, database.IntegrationGrant) (int, error)) (*MemberAccess, int, error) {
	if err := tc.RequireScope(models.ScopeAccessWrite); err != nil {
		return nil, 0, err
	}
	var out *MemberAccess
	changed := 0
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		member, err := database.ManagedPersonTx(tx, tx.CompanyID(), memberID, tc.LineageID())
		if err != nil {
			return notFoundOrInternal(err)
		}
		if changed, err = change(tx, tx.CompanyID(), member.ID, *integrationGrant(tc)); err != nil {
			return ErrInternal(err)
		}
		if changed > 0 {
			if _, _, _, err := database.ReconcilePersonRosterTx(tx, tx.CompanyID(), member.ID); err != nil {
				return ErrInternal(err)
			}
		}
		rules, err := database.ListPersonPermissionsTx(tx, tx.CompanyID(), memberID)
		if err != nil {
			return ErrInternal(err)
		}
		own, err := database.IntegrationRuleIDsTx(tx, tx.CompanyID(), member.ID, tc.LineageID())
		if err != nil {
			return ErrInternal(err)
		}
		now := time.Now()
		out = &MemberAccess{PersonPublicID: member.PublicID, MemberID: member.MemberID, Active: member.Active, Rules: []AccessRule{}}
		for i := range rules {
			if own[rules[i].ID] {
				out.Rules = append(out.Rules, publicRule(&rules[i], now))
			}
		}
		return nil
	})
	return out, changed, err
}
