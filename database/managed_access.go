package database

import (
	"database/sql"
	"errors"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// Integration-managed access (migrations/039_integration_managed_access.sql).
//
// An integration may give the members IT CREATED the standard access its
// credential covers: one company-wide ALLOW, or one SITE ALLOW per site of a
// site-restricted credential. Every rule it writes carries its lineage in
// granted_by_lineage_id, and removal touches only those. Anything an operator
// wrote -- including an ALLOW identical to the integration's, which the unique
// indexes make the integration's insert a no-op against -- is never changed.
// An operator DENY is untouched and, as everywhere, outweighs any ALLOW.

// IntegrationGrant is what a credential may give: its lineage, and the sites it
// is restricted to (nil = the whole company).
type IntegrationGrant struct {
	LineageID int64
	SiteIDs   []int64
}

// ErrNotManaged: the person exists but this integration cannot prove it created
// them. Callers answer as if the person did not exist.
var ErrNotManaged = errors.New("person is not managed by this integration")

// ManagedPersonTx returns the person behind externalID when -- and only when --
// the given lineage created them. Locked, so a concurrent grant and removal for
// the same person serialize.
func ManagedPersonTx(q Querier, companyID int64, externalID string, lineageID int64) (*models.Member, error) {
	member, err := MemberInTenant(q, companyID, externalID)
	if err != nil {
		return nil, err
	}
	var managedBy sql.NullInt64
	if err := q.QueryRow(`SELECT managed_by_lineage_id FROM people WHERE id = $1 AND company_id = $2 FOR UPDATE`,
		member.ID, companyID).Scan(&managedBy); err != nil {
		return nil, err
	}
	if !managedBy.Valid || managedBy.Int64 != lineageID {
		return nil, ErrNotManaged
	}
	return member, nil
}

// GrantIntegrationAccessTx writes the integration's rule(s) for a person it
// manages and returns how many were added (0 when already in place).
func GrantIntegrationAccessTx(q Querier, companyID, personID int64, grant IntegrationGrant) (int, error) {
	var res sql.Result
	var err error
	if grant.SiteIDs == nil {
		res, err = q.Exec(`
			INSERT INTO permissions (company_id, person_id, scope_type, effect, active, granted_by_lineage_id)
			VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE, $3)
			ON CONFLICT (person_id, effect, COALESCE(application, ''))
			     WHERE scope_type = 'COMPANY' AND deleted_at IS NULL
			DO NOTHING`, companyID, personID, grant.LineageID)
	} else {
		res, err = q.Exec(`
			INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active, granted_by_lineage_id)
			SELECT $1, $2, 'SITE', s.id, 'ALLOW', TRUE, $3
			  FROM sites s
			 WHERE s.company_id = $1 AND s.deleted_at IS NULL AND s.id = ANY($4::bigint[])
			ON CONFLICT (person_id, site_id, effect, COALESCE(application, ''))
			     WHERE scope_type = 'SITE' AND deleted_at IS NULL
			DO NOTHING`, companyID, personID, grant.LineageID, pq.Array(grant.SiteIDs))
	}
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RevokeIntegrationAccessTx removes the rules this lineage wrote for a person,
// within the credential's reach: a site-restricted credential removes only its
// SITE rules at its own sites. Operator rules are never selected.
func RevokeIntegrationAccessTx(q Querier, companyID, personID int64, grant IntegrationGrant) (int, error) {
	res, err := q.Exec(`
		UPDATE permissions
		   SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE company_id = $1 AND person_id = $2 AND granted_by_lineage_id = $3
		   AND deleted_at IS NULL
		   AND ($4::bigint[] IS NULL OR (scope_type = 'SITE' AND site_id = ANY($4::bigint[])))`,
		companyID, personID, grant.LineageID, pq.Array(grant.SiteIDs))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// IntegrationRuleIDsTx lists the public ids of the live rules this lineage
// wrote for a person: what the access endpoints report, and nothing else.
func IntegrationRuleIDsTx(q Querier, companyID, personID, lineageID int64) (map[string]bool, error) {
	rows, err := q.Query(`
		SELECT public_id::text FROM permissions
		 WHERE company_id = $1 AND person_id = $2 AND granted_by_lineage_id = $3 AND deleted_at IS NULL`,
		companyID, personID, lineageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// PersonIDByPublicID resolves a person's internal id inside a company.
func PersonIDByPublicID(companyID int64, publicID string) (int64, error) {
	var id int64
	err := DB.QueryRow(`SELECT id FROM people WHERE company_id = $1 AND public_id::text = $2`, companyID, publicID).Scan(&id)
	return id, err
}
