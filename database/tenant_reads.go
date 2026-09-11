package database

import (
	"database/sql"
	"time"

	"github.com/lib/pq"

	"access-terminal-cloud-api/models"
)

// Tenant-scoped reads for the service layer.
//
// ---------------------------------------------------------------------------
// EVERY FUNCTION HERE TAKES THE TENANT AS ITS SECOND ARGUMENT AND FILTERS ON IT
// ---------------------------------------------------------------------------
//
// These run inside a ScopedTx opened by database.WithTenant, so the statement
// timeout applies and -- once migration 034 adds the policies -- so will RLS.
// Until then the WHERE clause is the whole of the isolation, which is why none
// of them has a form without company_id: a foreign company's row is not found,
// and the service layer maps sql.ErrNoRows to the public not-found error.
//
// Nothing here is wired to a route. The console and the legacy site-key API
// keep their own query functions, unchanged.

// Querier is what a tenant-scoped read runs against: a *ScopedTx in the public
// path; *sql.DB or *sql.Tx satisfy it too, which is what the tests use.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
}

// KeysetPosition is where a page continues from: the (created_at, id) pair of
// the last row served. Both are needed because created_at is not unique.
type KeysetPosition struct {
	CreatedAt time.Time
	ID        int64
}

// MemberInTenant reads one live person by external id. sql.ErrNoRows when the
// id is unknown, soft-deleted, or belongs to another company.
func MemberInTenant(q Querier, companyID int64, externalID string) (*models.Member, error) {
	var m models.Member
	err := q.QueryRow(`SELECT `+memberColumns+`
	          FROM people WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL`,
		externalID, companyID).Scan(
		&m.ID, &m.PublicID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
		&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// MembersAfter reads one page of live people, NEWEST FIRST: (created_at, id)
// descending, which is the public contract (API_SPEC.md section 18).
//
// KEYSET, NOT OFFSET. The caller passes the position of the last row it has
// and gets the rows strictly after it in listing order -- that is, older; a
// row inserted or deleted between pages cannot shift the window, and a row
// created while paging appears at the front of a fresh listing rather than on
// a page the client is holding. `after` nil starts from the newest. The caller
// asks for one row more than it will serve, to learn whether a next page exists.
func MembersAfter(q Querier, companyID int64, after *KeysetPosition, limit int) ([]models.Member, error) {
	var (
		afterTS time.Time
		afterID int64
		paging  = after != nil
	)
	if paging {
		afterTS, afterID = after.CreatedAt, after.ID
	}
	rows, err := q.Query(`SELECT `+memberColumns+`
	          FROM people
	         WHERE company_id = $1 AND deleted_at IS NULL
	           AND (NOT $2 OR (created_at, id) < ($3::timestamptz, $4::bigint))
	         ORDER BY created_at DESC, id DESC
	         LIMIT $5`,
		companyID, paging, afterTS, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanMembers(rows)
}

// TenantSite is a site with its internal id alongside the console projection.
//
// The internal id is what a credential's site restriction is stored against
// (api_credential_sites.site_id), so the service layer needs it to decide
// site_not_permitted -- and it is the one thing the console projection
// deliberately omits.
type TenantSite struct {
	ID   int64
	Site models.ConsoleSite
}

// SiteInTenant reads one live site by public id. sql.ErrNoRows when the id is
// unknown, deleted, or belongs to another company. The public id is compared
// as text so a malformed value is simply not found rather than a cast error.
func SiteInTenant(q Querier, companyID int64, publicID string) (*TenantSite, error) {
	rows, err := q.Query(`
		SELECT s.id, `+consoleSiteColumns+`
		  FROM sites s
		 WHERE s.company_id = $1
		   AND s.deleted_at IS NULL
		   AND s.public_id::text = $2`,
		companyID, publicID)
	if err != nil {
		return nil, err
	}
	sites, err := scanTenantSites(rows)
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, sql.ErrNoRows
	}
	return &sites[0], nil
}

// SitesInTenant lists a company's live sites by name, optionally narrowed to a
// set of internal ids -- the credential's restriction. nil means every site;
// an empty non-nil slice means none, which cannot arise from a credential but
// is answered honestly rather than widened.
//
// THE TIEBREAK IS THE PUBLIC ID, not the internal one: the public contract
// (API_SPEC.md section 18) orders by `name` then `id`, and `id` on that tree
// is the UUID. The internal sequence is not part of any public ordering.
func SitesInTenant(q Querier, companyID int64, siteIDs []int64) ([]TenantSite, error) {
	scoped := siteIDs != nil
	rows, err := q.Query(`
		SELECT s.id, `+consoleSiteColumns+`
		  FROM sites s
		 WHERE s.company_id = $1
		   AND s.deleted_at IS NULL
		   AND (NOT $2 OR s.id = ANY($3::bigint[]))
		 ORDER BY s.site_name, s.public_id`,
		companyID, scoped, pq.Array(siteIDs))
	if err != nil {
		return nil, err
	}
	return scanTenantSites(rows)
}

func scanTenantSites(rows *sql.Rows) ([]TenantSite, error) {
	defer rows.Close()
	var out []TenantSite
	for rows.Next() {
		var ts TenantSite
		if err := rows.Scan(&ts.ID, &ts.Site.ID, &ts.Site.Name, &ts.Site.Address,
			&ts.Site.Timezone, &ts.Site.Active, &ts.Site.CreatedAt, &ts.Site.DeviceCount,
			&ts.Site.OfflinePolicy, &ts.Site.OfflineGraceMinutes); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}
