package database

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"access-terminal-cloud-api/models"
)

// Tenant-scoped site writes for the public API.
//
// ---------------------------------------------------------------------------
// WHY THESE ARE NOT database.CreateSite AND database.UpdateSite
// ---------------------------------------------------------------------------
//
// Those two are the CONSOLE's, and they run against DB directly: no scoped
// transaction, no statement timeout, no app.company_id, and CreateSite RETURNS
// THE PROVISIONING KEY. The public tree runs everything inside
// database.WithTenant so the timeout and the tenant setting apply, and it must
// never be in a position to serialise a site key at all.
//
// So these are separate functions taking a *ScopedTx, exactly as
// tenant_reads.go is separate from console.go, and the console's own path is
// untouched. What they SHARE is the part that must not diverge: the name policy
// and the key generation both come from sites.go, so a site created through the
// public API is the same row as one created in the console, minted the same way.
//
// ---------------------------------------------------------------------------
// THE PROVISIONING KEY IS MINTED AND DISCARDED
// ---------------------------------------------------------------------------
//
// A site with no provisioning key cannot have a terminal registered at it, so
// creating one without a key would produce a site that silently cannot be used
// for what sites are for. The key is therefore generated and stored hashed
// exactly as the console path stores it -- and the plaintext is dropped on the
// floor here rather than returned, because a third-party integration must never
// hold the secret that registers door hardware. An operator who needs it rotates
// it in the console, which is the only place it is ever shown.

// TenantSiteInput creates a site through the public API.
//
// Country and Timezone are REQUIRED here and optional in the console. That is
// not an inconsistency to be smoothed over: the console is used by somebody who
// can fill them in later and can see that they are blank, and an integration
// creating a site unattended cannot. Requiring them at the one boundary where
// nobody is looking is what keeps the data usable.
type TenantSiteInput struct {
	Name     string
	Address  string
	Timezone string
	Country  string
}

// TenantSiteUpdate changes a site through the public API. Every field is
// optional; only what is supplied is applied, so correcting a name cannot blank
// a country the caller never mentioned.
type TenantSiteUpdate struct {
	Name     *string
	Address  *string
	Timezone *string
	Country  *string
	Active   *bool
}

// CreateSiteInTenant inserts a site inside a tenant-scoped transaction.
//
// company_id comes from the ScopedTx and from nowhere else, so there is no
// parameter through which a caller could create a site in another company. A
// duplicate live name is models.ErrSiteNameTaken, from the same partial unique
// index the console path meets.
func CreateSiteInTenant(tx *ScopedTx, in TenantSiteInput) (*TenantSite, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, models.ErrSiteNameRequired
	}
	if len(name) > 100 {
		return nil, models.ErrSiteNameTooLong
	}

	timezone := strings.TrimSpace(in.Timezone)
	if timezone == "" {
		return nil, models.ErrSiteTimezoneRequired
	}
	country, err := models.NormaliseCountry(in.Country)
	if err != nil {
		return nil, err
	}

	// Minted here and never returned. See the note at the top of this file.
	_, hash, prefix, err := generateSiteKey()
	if err != nil {
		return nil, err
	}

	var ts TenantSite
	err = tx.QueryRow(`
		INSERT INTO sites (company_id, site_name, address, timezone, country,
		                   api_key_hash, api_key_prefix, active)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, TRUE)
		RETURNING id, public_id, site_name, COALESCE(address, ''), timezone,
		          active, created_at, offline_policy, offline_grace_minutes,
		          COALESCE(country, '')`,
		tx.CompanyID(), name, strings.TrimSpace(in.Address), timezone, country,
		hash, prefix).
		Scan(&ts.ID, &ts.Site.ID, &ts.Site.Name, &ts.Site.Address, &ts.Site.Timezone,
			&ts.Site.Active, &ts.Site.CreatedAt, &ts.Site.OfflinePolicy,
			&ts.Site.OfflineGraceMinutes, &ts.Country)
	if IsUniqueViolation(err) {
		return nil, models.ErrSiteNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("creating site: %w", err)
	}

	// A brand new site has no terminals. Stated rather than left to the zero
	// value so the create response has the same shape as a read.
	ts.Site.DeviceCount = 0
	return &ts, nil
}

// UpdateSiteInTenant changes a site's details inside a tenant-scoped
// transaction.
//
// COALESCE against the parameter rather than building the statement from
// whichever fields were supplied: one query with a fixed shape, which cannot
// lose its company filter to a concatenation mistake. The same form
// UpdateSite already uses.
//
// THE SITE IS ADDRESSED BY ITS PUBLIC ID and resolved in the same statement, so
// there is no window between "this site is in my company" and "update it".
func UpdateSiteInTenant(tx *ScopedTx, sitePublicID string, in TenantSiteUpdate) (*TenantSite, error) {
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, models.ErrSiteNameRequired
		}
		if len(name) > 100 {
			return nil, models.ErrSiteNameTooLong
		}
		in.Name = &name
	}
	if in.Timezone != nil {
		timezone := strings.TrimSpace(*in.Timezone)
		if timezone == "" {
			return nil, models.ErrSiteTimezoneRequired
		}
		in.Timezone = &timezone
	}
	if in.Country != nil {
		country, err := models.NormaliseCountry(*in.Country)
		if err != nil {
			return nil, err
		}
		in.Country = &country
	}

	var ts TenantSite
	err := tx.QueryRow(`
		UPDATE sites
		   SET site_name  = COALESCE($3, site_name),
		       address    = COALESCE($4, address),
		       timezone   = COALESCE($5, timezone),
		       country    = COALESCE($6, country),
		       active     = COALESCE($7, active),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE company_id = $1
		   AND public_id::text = $2
		   AND deleted_at IS NULL
		 RETURNING id, public_id, site_name, COALESCE(address, ''), timezone,
		           active, created_at,
		           (SELECT count(*) FROM devices d
		             WHERE d.site_id = sites.id AND d.deleted_at IS NULL),
		           offline_policy, offline_grace_minutes, COALESCE(country, '')`,
		tx.CompanyID(), sitePublicID, in.Name, in.Address, in.Timezone, in.Country,
		in.Active).
		Scan(&ts.ID, &ts.Site.ID, &ts.Site.Name, &ts.Site.Address, &ts.Site.Timezone,
			&ts.Site.Active, &ts.Site.CreatedAt, &ts.Site.DeviceCount,
			&ts.Site.OfflinePolicy, &ts.Site.OfflineGraceMinutes, &ts.Country)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if IsUniqueViolation(err) {
		return nil, models.ErrSiteNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("updating site: %w", err)
	}
	return &ts, nil
}
