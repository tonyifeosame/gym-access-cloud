package database

import (
	"database/sql"
	"errors"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// Console reads: the company and site information the operator dashboard needs.
//
// Every query is company-scoped, like the rest of the data layer. The site
// projection deliberately does NOT select api_key -- the column is not omitted
// from a struct somewhere downstream, it is never read out of the database at
// all, which is one fewer place a provisioning secret can escape from.

// GetCompany returns the tenant an operator belongs to.
func GetCompany(companyID int64) (*models.ConsoleCompany, error) {
	var company models.ConsoleCompany
	var contactEmail sql.NullString

	err := DB.QueryRow(`
		SELECT public_id, name, slug, contact_email, active, created_at
		  FROM companies
		 WHERE id = $1 AND deleted_at IS NULL`, companyID).
		Scan(&company.ID, &company.Name, &company.Slug, &contactEmail,
			&company.Active, &company.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrCompanyNotFound
	}
	if err != nil {
		return nil, err
	}

	company.ContactEmail = contactEmail.String
	return &company, nil
}

// consoleSiteColumns is the shared projection. api_key is absent on purpose.
const consoleSiteColumns = `s.public_id, s.site_name, COALESCE(s.address, ''),
	          s.timezone, s.active, s.created_at,
	          (SELECT count(*) FROM devices d
	            WHERE d.site_id = s.id AND d.deleted_at IS NULL) AS terminal_count`

func scanConsoleSites(rows *sql.Rows) ([]models.ConsoleSite, error) {
	defer rows.Close()

	var sites []models.ConsoleSite
	for rows.Next() {
		var site models.ConsoleSite
		if err := rows.Scan(&site.ID, &site.Name, &site.Address, &site.Timezone,
			&site.Active, &site.CreatedAt, &site.DeviceCount); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

// ListConsoleSites returns a company's sites, optionally narrowed to the ones an
// operator has been granted.
//
// A nil siteIDs means "every site in the company", which is what an unscoped
// operator and every ADMIN or OWNER gets. An EMPTY-but-non-nil slice would mean
// "no sites at all" -- a distinction the caller has to make deliberately, so
// nil and empty are not conflated here.
func ListConsoleSites(companyID int64, siteIDs []int64) ([]models.ConsoleSite, error) {
	if siteIDs == nil {
		rows, err := DB.Query(`
			SELECT `+consoleSiteColumns+`
			  FROM sites s
			 WHERE s.company_id = $1 AND s.deleted_at IS NULL
			 ORDER BY s.site_name`, companyID)
		if err != nil {
			return nil, err
		}
		return scanConsoleSites(rows)
	}

	rows, err := DB.Query(`
		SELECT `+consoleSiteColumns+`
		  FROM sites s
		 WHERE s.company_id = $1
		   AND s.deleted_at IS NULL
		   AND s.id = ANY($2)
		 ORDER BY s.site_name`, companyID, pq.Array(siteIDs))
	if err != nil {
		return nil, err
	}
	return scanConsoleSites(rows)
}

// GetConsoleSite returns one site, scoped to the company.
//
// Takes the internal id because the caller has already resolved and authorized
// the site -- RequireSiteGrant puts it in the request context. The company
// filter is kept anyway: an authorization check and a tenancy filter are
// different guarantees, and a query that relies on the caller having done the
// first one is a query that breaks the moment it is reused somewhere else.
func GetConsoleSite(companyID, siteID int64) (*models.ConsoleSite, error) {
	rows, err := DB.Query(`
		SELECT `+consoleSiteColumns+`
		  FROM sites s
		 WHERE s.id = $1 AND s.company_id = $2 AND s.deleted_at IS NULL`,
		siteID, companyID)
	if err != nil {
		return nil, err
	}

	sites, err := scanConsoleSites(rows)
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, models.ErrSiteNotFound
	}
	return &sites[0], nil
}
