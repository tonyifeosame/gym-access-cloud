package database

import (
	"database/sql"
	"errors"
	"strings"

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
	            WHERE d.site_id = s.id AND d.deleted_at IS NULL) AS terminal_count,
	          s.offline_policy, s.offline_grace_minutes`

func scanConsoleSites(rows *sql.Rows) ([]models.ConsoleSite, error) {
	defer rows.Close()

	var sites []models.ConsoleSite
	for rows.Next() {
		var site models.ConsoleSite
		if err := rows.Scan(&site.ID, &site.Name, &site.Address, &site.Timezone,
			&site.Active, &site.CreatedAt, &site.DeviceCount,
			&site.OfflinePolicy, &site.OfflineGraceMinutes); err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

// ListConsoleSites returns a company's sites, optionally narrowed to the ones an
// operator has been granted and optionally matched against a search term.
//
// A nil siteIDs means "every site in the company", which is what an unscoped
// operator and every ADMIN or OWNER gets. An EMPTY-but-non-nil slice would mean
// "no sites at all" -- a distinction the caller has to make deliberately, so
// nil and empty are not conflated here.
//
// SEARCH IS IN SQL, for the same reason it is for people and terminals: a
// console that narrowed a fetched list in the browser would search what it had
// already been given rather than the estate, which is silently wrong the moment
// a company has more locations than one response carries -- and silently right
// in every test with two fixtures.
//
// ONE STATEMENT SERVES EVERY COMBINATION, in the shape ListConsolePeople below
// already uses: a "is this narrowing on" flag beside each value. The two
// hand-built variants this replaces were the same query differing by one
// predicate, which is how a tenancy filter ends up present on one path and
// absent on the other.
func ListConsoleSites(companyID int64, siteIDs []int64, search string) ([]models.ConsoleSite, error) {
	scoped := siteIDs != nil
	searching := search != ""
	pattern := "%" + escapeLikePattern(search) + "%"

	rows, err := DB.Query(`
		SELECT `+consoleSiteColumns+`
		  FROM sites s
		 WHERE s.company_id = $1
		   AND s.deleted_at IS NULL
		   AND (NOT $2 OR s.id = ANY($3::bigint[]))
		   AND (NOT $4
		        OR s.site_name ILIKE $5 ESCAPE '\'
		        OR COALESCE(s.address, '') ILIKE $5 ESCAPE '\')
		 ORDER BY s.site_name`,
		companyID, scoped, pq.Array(siteIDs), searching, pattern)
	if err != nil {
		return nil, err
	}
	return scanConsoleSites(rows)
}

// People, paginated and searchable.
//
// The console needs this and the site-key API deliberately does not get it:
// GET /api/v1/members is a contract terminals and existing tooling already
// speak, and quietly bounding it would silently truncate a roster somebody is
// relying on being complete. This is a separate query for a separate caller.

// PeopleQuery is one page of a people search.
//
// THE FILTERS ARE POINTERS, and that is load-bearing rather than stylistic: an
// ABSENT filter is not the same question as `false`. `enrolled=false` means
// "show me the people with no credential", which is what an operator asks
// before a rollout; no `enrolled` at all means "show me everybody". A plain bool
// would collapse the second into the first and silently hide every enrolled
// person from an unfiltered list.
type PeopleQuery struct {
	// Search matches the external id or the full name, case-insensitively and
	// anywhere in the value. Empty matches everything.
	Search string

	// Enrolled narrows by whether the person is enrolled AT ALL --
	// personEnrolledPredicate, the union of the structured record and the legacy
	// column. It is the same rule the person projection reports, so the filter
	// and the badge beside each row cannot disagree.
	Enrolled *bool

	// Active narrows by people.active.
	Active *bool

	Limit  int
	Offset int
}

// ConsolePersonRow is one person plus what the two enrolment stores say.
//
// The enrolment is read as COLUMNS ON THE PAGE QUERY rather than by asking per
// row: a page of fifty would otherwise be fifty extra round trips to answer a
// question the same statement can answer for free.
type ConsolePersonRow struct {
	Member    models.Member
	Enrolment PersonEnrolment
}

// PeoplePage is a page of results plus what a caller needs to ask for the next
// one. Total is the size of the whole match, not of this page.
type PeoplePage struct {
	People []ConsolePersonRow
	Total  int
}

// ListConsolePeople returns one page of a company's people, newest first.
//
// Ordered by created_at DESC with an id tiebreak. The tiebreak is not cosmetic:
// people created in the same transaction share a created_at to the microsecond,
// and without a second sort key the same row can appear on two consecutive
// pages while another is skipped entirely.
func ListConsolePeople(companyID int64, query PeopleQuery) (*PeoplePage, error) {
	pattern := "%" + escapeLikePattern(query.Search) + "%"
	searching := query.Search != ""

	// The optional filters, in the shape this file already uses for search: a
	// "is this filter on" flag beside the value, so one statement serves every
	// combination and there is no string-built WHERE clause to get wrong.
	//
	// THE COUNT AND THE PAGE APPLY THE SAME PREDICATE, from the same constant.
	// If they ever diverged, `total` would describe a different set from the
	// rows -- and the symptom is a "next page" button that leads nowhere, or a
	// page that stops before the end of the match.
	filterEnrolled := query.Enrolled != nil
	wantEnrolled := filterEnrolled && *query.Enrolled
	filterActive := query.Active != nil
	wantActive := filterActive && *query.Active

	const where = `
		 WHERE p.company_id = $1
		   AND p.deleted_at IS NULL
		   AND (NOT $2 OR p.external_id ILIKE $3 ESCAPE '\' OR p.full_name ILIKE $3 ESCAPE '\')
		   AND (NOT $4 OR ` + personEnrolledPredicate + ` = $5)
		   AND (NOT $6 OR p.active = $7)`

	args := []any{companyID, searching, pattern,
		filterEnrolled, wantEnrolled, filterActive, wantActive}

	var page PeoplePage
	if err := DB.QueryRow(`SELECT count(*) FROM people p`+where, args...).
		Scan(&page.Total); err != nil {
		return nil, err
	}

	// The member columns, aliased to `p`, plus the two enrolment facts. The
	// projection is otherwise exactly memberColumns -- see the note there.
	rows, err := DB.Query(`
		SELECT p.id, p.public_id, p.external_id, p.full_name, p.membership_type,
		       p.active, COALESCE(p.fingerprint_template, ''),
		       p.created_at, p.updated_at,
		       `+personHasCredentialExpr+`,
		       COALESCE(p.fingerprint_template, '') <> ''
		  FROM people p`+where+`
		 ORDER BY p.created_at DESC, p.id DESC
		 LIMIT $8 OFFSET $9`,
		append(args, query.Limit, query.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	people := make([]ConsolePersonRow, 0, 16)
	for rows.Next() {
		var row ConsolePersonRow
		if err := rows.Scan(
			&row.Member.ID, &row.Member.PublicID, &row.Member.MemberID,
			&row.Member.FullName, &row.Member.MembershipType, &row.Member.Active,
			&row.Member.FingerprintTemplate, &row.Member.CreatedAt, &row.Member.UpdatedAt,
			&row.Enrolment.HasCredential, &row.Enrolment.HasLegacy,
		); err != nil {
			return nil, err
		}
		people = append(people, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	page.People = people

	return &page, nil
}

// escapeLikePattern neutralises the wildcards in a user's search term.
//
// Without this, searching for "100%" matches every person whose id starts with
// 100, and a lone "_" matches everyone. The escape character is itself escaped
// first, or escaping would corrupt a term containing a backslash.
func escapeLikePattern(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(term)
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
