package database

import (
	"database/sql"
	"errors"
	"time"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// TenantTerminal is a terminal as the public API describes it: identifiers,
// where it is, whether it is reachable, and whether it can run an enrolment.
// Nothing here is a credential.
type TenantTerminal struct {
	DeviceID     int64 // internal; what the enrolment store takes
	Serial       string
	Name         string
	SitePublicID string
	SiteName     string
	Status       string
	LastSeenAt   *time.Time
	Enrollable   bool
}

// The enrollable predicate is the one enrollmentTargetTx enforces at start
// time -- active, not DISABLED, and holding a credential of its own -- so the
// list never offers a terminal that the start would then refuse.
const tenantTerminalSelect = `
	SELECT d.id, d.serial_number, COALESCE(d.device_name, ''), s.public_id::text,
	       COALESCE(s.site_name, ''), d.status, COALESCE(d.last_heartbeat_at, d.last_seen_at),
	       d.active AND d.status <> 'DISABLED' AND COALESCE(d.api_key_hash, '') <> ''
	  FROM devices d
	  JOIN sites s ON s.id = d.site_id
	 WHERE s.company_id = $1
	   AND d.deleted_at IS NULL
	   AND s.deleted_at IS NULL
	   AND (NOT $2 OR s.id = ANY($3::bigint[]))`

// TerminalsInTenant lists a company's terminals, bounded by siteIDs when the
// credential is site-restricted (nil means every site).
func TerminalsInTenant(q Querier, companyID int64, siteIDs []int64) ([]TenantTerminal, error) {
	rows, err := q.Query(tenantTerminalSelect+` ORDER BY s.site_name, d.serial_number`,
		companyID, siteIDs != nil, pq.Array(siteIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TenantTerminal{}
	for rows.Next() {
		t, err := scanTenantTerminal(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// TerminalInTenant resolves one serial inside the company and the credential's
// site restriction. A serial in another company, or at a site the credential
// cannot see, is models.ErrDeviceNotFound -- indistinguishable from no terminal.
func TerminalInTenant(q Querier, companyID int64, siteIDs []int64, serial string) (*TenantTerminal, error) {
	t, err := scanTenantTerminal(q.QueryRow(tenantTerminalSelect+` AND d.serial_number = $4`,
		companyID, siteIDs != nil, pq.Array(siteIDs), serial).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	return t, err
}

func scanTenantTerminal(scan func(dest ...any) error) (*TenantTerminal, error) {
	var t TenantTerminal
	var seen sql.NullTime
	if err := scan(&t.DeviceID, &t.Serial, &t.Name, &t.SitePublicID, &t.SiteName, &t.Status, &seen, &t.Enrollable); err != nil {
		return nil, err
	}
	if seen.Valid {
		at := seen.Time.UTC()
		t.LastSeenAt = &at
	}
	return &t, nil
}
