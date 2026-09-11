package service

import (
	"context"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Sites, read-only, as the public API will expose them.
//
// The site restriction on a credential is applied HERE, not in the query: a
// site in the caller's own company that the credential is scoped away from is
// site_not_permitted (403), which is a different answer from a site in
// another company (404). Both are decided from the TenantContext and the row's
// company_id; nothing the caller sends can move a site between the two.

// Site is the public projection of a site (API_SPEC.md section 18). The
// provisioning key, the settings blob and the offline policy are absent by
// construction; `address` is always present, "" when the site has none.
type Site struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Address       string    `json:"address"`
	Timezone      string    `json:"timezone"`
	Active        bool      `json:"active"`
	TerminalCount int       `json:"terminal_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// SiteService is the site operations. Construct with NewSiteService.
type SiteService struct {
	timeout time.Duration
}

// NewSiteService returns a site service.
func NewSiteService() *SiteService {
	return &SiteService{timeout: database.DefaultPublicStatementTimeout}
}

// Get reads one site by public id. Requires sites:read, and the credential
// must reach the site.
func (s *SiteService) Get(ctx context.Context, tc *TenantContext, siteID string) (*Site, error) {
	if err := tc.RequireScope(models.ScopeSitesRead); err != nil {
		return nil, err
	}
	var out *Site
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		row, err := database.SiteInTenant(tx, tx.CompanyID(), siteID)
		if err != nil {
			return notFoundOrInternal(err)
		}
		// Tenancy first (above), then the credential's own restriction.
		if err := tc.RequireSite(row.ID); err != nil {
			return err
		}
		out = publicSite(row)
		return nil
	})
	return out, err
}

// List reads every site the credential reaches, by name. Requires sites:read.
//
// Sites are few per company, so this is not paged: an integrator listing
// locations gets all of them in one answer.
func (s *SiteService) List(ctx context.Context, tc *TenantContext) ([]Site, error) {
	if err := tc.RequireScope(models.ScopeSitesRead); err != nil {
		return nil, err
	}
	var restriction []int64
	if tc.RestrictedToSites() {
		restriction = tc.SiteIDs()
	}
	var out []Site
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		rows, err := database.SitesInTenant(tx, tx.CompanyID(), restriction)
		if err != nil {
			return ErrInternal(err)
		}
		out = make([]Site, 0, len(rows))
		for i := range rows {
			out = append(out, *publicSite(&rows[i]))
		}
		return nil
	})
	return out, err
}

func publicSite(row *database.TenantSite) *Site {
	return &Site{
		ID:            row.Site.ID,
		Name:          row.Site.Name,
		Address:       row.Site.Address,
		Timezone:      row.Site.Timezone,
		Active:        row.Site.Active,
		TerminalCount: row.Site.DeviceCount,
		CreatedAt:     row.Site.CreatedAt,
	}
}
