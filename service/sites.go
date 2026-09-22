package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Sites, as the public API exposes them.
//
// The site restriction on a credential is applied HERE, not in the query: a
// site in the caller's own company that the credential is scoped away from is
// site_not_permitted (403), which is a different answer from a site in
// another company (404). Both are decided from the TenantContext and the row's
// company_id; nothing the caller sends can move a site between the two.
//
// ---------------------------------------------------------------------------
// THE WRITES (037) FOLLOW EXACTLY THE SAME RULES AS THE READS
// ---------------------------------------------------------------------------
//
// A create is bounded by the tenant on the context and by nothing else -- there
// is no field naming a company, and the insert reads company_id from the scoped
// transaction. An update resolves the site inside the tenant first, so a public
// id from another company is not found rather than forbidden, and then applies
// the credential's own site restriction, so a site in this company that the
// grant does not reach is site_not_permitted.
//
// WHAT A WRITE HERE CAN NEVER DO. It cannot read, return or rotate the site's
// provisioning key; it cannot retire a site (retirement cascades to every
// terminal at the location and is a console decision with a person behind it);
// and it cannot touch the offline policy, which is a safety control rather than
// metadata.

// Site is the public projection of a site (API_SPEC.md section 18). The
// provisioning key, the settings blob and the offline policy are absent by
// construction; `address` is always present, "" when the site has none.
type Site struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	// Country is ISO 3166-1 alpha-2, or "" for a site created before the field
	// existed or through the console, which does not ask for one. Always
	// present in the response so a client does not have to distinguish an
	// absent key from an unknown country.
	Country       string    `json:"country"`
	Timezone      string    `json:"timezone"`
	Active        bool      `json:"active"`
	TerminalCount int       `json:"terminal_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// SiteInput is the body of a create or an update.
//
// POINTERS WHERE ABSENCE MATTERS. On a create, an absent field is a missing
// field; on an update, an absent field keeps its value and an explicitly empty
// one is a refusal. A plain string cannot carry that difference, and an update
// that silently blanked a timezone it was never told about would be the kind of
// bug nobody finds until a schedule fires at the wrong hour.
//
// NO FIELD NAMES A COMPANY, and the structural test in service_test.go keeps it
// that way.
type SiteInput struct {
	Name     *string
	Address  *string
	Country  *string
	Timezone *string
	Active   *bool
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

// Create adds a site. Requires sites:write.
//
// NAME, COUNTRY AND TIMEZONE ARE ALL REQUIRED, which is stricter than the
// console. See models.ErrSiteTimezoneRequired: the console has a person looking
// at a form who can fill a blank in later; an integration creating sites
// unattended has nobody, and a fleet of locations silently recorded as UTC is a
// schedule that fires at the wrong hour at every door.
//
// A SITE-RESTRICTED CREDENTIAL MAY NOT CREATE, and this is the decision rather
// than a consequence of there being no site to check.
//
// A restriction can only ever name sites that ALREADY EXIST -- api_credential_sites
// has a foreign key, and a grant's restriction is resolved from the operator's
// existing site grants. So "a narrowed credential needs to be able to add the
// location it was narrowed for" is not a real case: that location is already
// there.
//
// What the alternative would actually produce is incoherent. An administrator
// who deliberately narrowed a key to Site A would find it able to create Site
// B -- and then unable to read or change B, because B is outside the set. A
// credential that can bring a resource into existence and then cannot manage it
// is a capability nobody chose.
//
// So creating is treated as what it is: a COMPANY-WIDE act, refused to a
// credential that has been scoped away from company-wide reach. site_not_permitted
// (403) rather than insufficient_scope, because the scope IS held; it is the
// site restriction that forbids it.
//
// THIS COSTS THE OAUTH PATH NOTHING. sites:write requires ADMIN, and
// database.grantedSiteIDs leaves ADMIN and OWNER unrestricted, so a grant that
// carries sites:write is never site-restricted in the first place.
func (s *SiteService) Create(ctx context.Context, tc *TenantContext, in SiteInput) (*Site, error) {
	if err := tc.RequireScope(models.ScopeSitesWrite); err != nil {
		return nil, err
	}
	if tc.RestrictedToSites() {
		return nil, ErrSiteCreateNotPermitted()
	}
	if in.Active != nil {
		// A site is created active. Accepting `active: false` here would mint a
		// location whose terminals could not authenticate the moment they were
		// installed, which is never what a create means.
		return nil, ErrInvalidField("active",
			"A new site is always active. Use PATCH to deactivate one.")
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, ErrMissingField("name")
	}
	if in.Country == nil || strings.TrimSpace(*in.Country) == "" {
		return nil, ErrMissingField("country")
	}
	if in.Timezone == nil || strings.TrimSpace(*in.Timezone) == "" {
		return nil, ErrMissingField("timezone")
	}
	if err := validateTimezone(*in.Timezone); err != nil {
		return nil, err
	}
	if !models.KnownCountry(*in.Country) {
		return nil, errInvalidCountry()
	}

	address := ""
	if in.Address != nil {
		address = *in.Address
	}

	var out *Site
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		row, err := database.CreateSiteInTenant(tx, database.TenantSiteInput{
			Name:     *in.Name,
			Address:  address,
			Timezone: *in.Timezone,
			Country:  *in.Country,
		})
		if err != nil {
			return siteWriteFailure(err)
		}
		out = publicSite(row)
		return nil
	})
	return out, err
}

// Update changes a site's details. Requires sites:write, and the credential
// must reach the site.
//
// PARTIAL. An absent field keeps its value; an explicitly empty name, country
// or timezone is a refusal rather than a blanking, because there is no such
// thing as a site with no name and no zone.
func (s *SiteService) Update(ctx context.Context, tc *TenantContext, siteID string,
	in SiteInput) (*Site, error) {

	if err := tc.RequireScope(models.ScopeSitesWrite); err != nil {
		return nil, err
	}
	if in.Name == nil && in.Address == nil && in.Country == nil &&
		in.Timezone == nil && in.Active == nil {
		return nil, ErrInvalidField("body",
			"Supply at least one of name, address, country, timezone or active.")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return nil, ErrInvalidField("name", "name cannot be empty.")
	}
	if in.Timezone != nil {
		if strings.TrimSpace(*in.Timezone) == "" {
			return nil, ErrInvalidField("timezone", "timezone cannot be empty.")
		}
		if err := validateTimezone(*in.Timezone); err != nil {
			return nil, err
		}
	}
	if in.Country != nil {
		if strings.TrimSpace(*in.Country) == "" {
			return nil, ErrInvalidField("country", "country cannot be empty.")
		}
		if !models.KnownCountry(*in.Country) {
			return nil, errInvalidCountry()
		}
	}

	var out *Site
	err := database.WithTenant(ctx, tc.CompanyID(), s.timeout, func(tx *database.ScopedTx) error {
		// RESOLVED BEFORE IT IS WRITTEN, so the tenancy answer (404) and the
		// restriction answer (403) are decided in that order -- the same order
		// Get applies them in, and the only order that does not confirm a
		// foreign site's existence.
		current, err := database.SiteInTenant(tx, tx.CompanyID(), siteID)
		if err != nil {
			return notFoundOrInternal(err)
		}
		if err := tc.RequireSite(current.ID); err != nil {
			return err
		}
		row, err := database.UpdateSiteInTenant(tx, siteID, database.TenantSiteUpdate{
			Name:     in.Name,
			Address:  in.Address,
			Timezone: in.Timezone,
			Country:  in.Country,
			Active:   in.Active,
		})
		if err != nil {
			return siteWriteFailure(err)
		}
		out = publicSite(row)
		return nil
	})
	return out, err
}

// validateTimezone applies the project's existing timezone rule.
//
// time.LoadLocation against the platform's zone database, which is what
// assistant/tools_schedules_parse.go already does for a schedule's zone and
// what database/authorization.go does when it evaluates one. One rule, one
// place it can be wrong: a zone the API accepts is a zone the authorization
// engine can actually evaluate a schedule in.
//
// A NAME, NOT AN OFFSET. "+01:00" is not a timezone -- it is a timezone at one
// moment in the year -- and accepting one would put a value in a site's record
// that goes wrong at the next daylight-saving transition. LoadLocation refuses
// it, and so does this.
func validateTimezone(zone string) error {
	trimmed := strings.TrimSpace(zone)
	if trimmed == "" || trimmed == "Local" {
		// "Local" resolves to whatever the SERVER's zone happens to be, which
		// is not a property of the customer's site and would change if the
		// deployment moved.
		return ErrInvalidField("timezone",
			"timezone must be an IANA zone name such as Africa/Lagos or Europe/London.")
	}
	if _, err := time.LoadLocation(trimmed); err != nil {
		return ErrInvalidField("timezone",
			"timezone must be an IANA zone name such as Africa/Lagos or Europe/London.")
	}
	return nil
}

// errInvalidCountry is the one wording for an unusable country, so the create
// and the update cannot describe the same rule differently.
func errInvalidCountry() error {
	return ErrInvalidField("country",
		"country must be an ISO 3166-1 alpha-2 code such as NG, GB or US.")
}

// siteWriteFailure maps what the site write path can raise onto the registry.
func siteWriteFailure(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, models.ErrSiteNotFound):
		return ErrNotFound()
	case errors.Is(err, models.ErrSiteNameTaken):
		return ErrSiteNameExists()
	case errors.Is(err, models.ErrSiteNameRequired):
		return ErrMissingField("name")
	case errors.Is(err, models.ErrSiteNameTooLong):
		return ErrInvalidField("name", "name must be 100 characters or fewer.")
	case errors.Is(err, models.ErrSiteTimezoneRequired):
		return ErrMissingField("timezone")
	case errors.Is(err, models.ErrUnknownCountry):
		return errInvalidCountry()
	}
	return ErrInternal(err)
}

func publicSite(row *database.TenantSite) *Site {
	return &Site{
		ID:            row.Site.ID,
		Name:          row.Site.Name,
		Address:       row.Site.Address,
		Country:       row.Country,
		Timezone:      row.Site.Timezone,
		Active:        row.Site.Active,
		TerminalCount: row.Site.DeviceCount,
		CreatedAt:     row.Site.CreatedAt,
	}
}
