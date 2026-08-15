package database

import (
	"database/sql"
	"time"

	"access-terminal-cloud-api/models"
)

// Queries target the Sprint 2 schema (migrations/002_core_schema.sql).
//
// Two things are true of every query below:
//
//   * Entity reads filter `deleted_at IS NULL`. Soft-deleted rows are retained
//     in the table but are invisible to the API.
//   * Reads and writes are scoped by company_id, which the auth middleware
//     derives from the API key's site. Nothing crosses a tenant boundary.
//
// The `members` table became `people` and `member_id` became `external_id`.
// The queries alias external_id back to member_id so the JSON contract the
// terminals already speak is unchanged.

// Site Queries

// GetSiteByAPIKey retrieves a site by its provisioning key.
//
// THE KEY PRESENTED IS HASHED AND THE HASH IS LOOKED UP. Since
// 011_site_credentials.sql the database holds no plaintext to compare against,
// so this is the only way to authenticate one -- and it is the reason the wire
// contract did not have to change when that migration ran: the caller still
// sends the same string it always did.
//
// Still one index probe, on the partial unique index over api_key_hash. An
// empty key is refused before touching the database: hashing "" produces a
// perfectly valid-looking hash, and a row that somehow carried it would then
// authenticate every caller who sent no key at all.
func GetSiteByAPIKey(apiKey string) (*models.Site, error) {
	if apiKey == "" {
		return nil, sql.ErrNoRows
	}

	var site models.Site
	query := `SELECT id, public_id, company_id, site_name, active, created_at, updated_at
	          FROM sites WHERE api_key_hash = $1 AND active = true AND deleted_at IS NULL`

	err := DB.QueryRow(query, HashSiteKey(apiKey)).Scan(
		&site.ID, &site.PublicID, &site.CompanyID, &site.SiteName,
		&site.Active, &site.CreatedAt, &site.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// Member Queries (backed by the `people` table)

// memberColumns is the shared projection for member reads. fingerprint_template
// is nullable, so it is coalesced rather than scanned into a string directly.
const memberColumns = `id, public_id, external_id, full_name, membership_type, active,
	          COALESCE(fingerprint_template, '') AS fingerprint_template, created_at, updated_at`

// scanMembers reads a member result set built from memberColumns
func scanMembers(rows *sql.Rows) ([]models.Member, error) {
	defer rows.Close()

	var members []models.Member
	for rows.Next() {
		var m models.Member
		err := rows.Scan(
			&m.ID, &m.PublicID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
			&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetAllMembers retrieves all members for a company
func GetAllMembers(companyID int64) ([]models.Member, error) {
	query := `SELECT ` + memberColumns + `
	          FROM people WHERE company_id = $1 AND deleted_at IS NULL
	          ORDER BY created_at DESC`

	rows, err := DB.Query(query, companyID)
	if err != nil {
		return nil, err
	}
	return scanMembers(rows)
}

// GetMemberByID retrieves a member by their external member ID
func GetMemberByID(companyID int64, memberID string) (*models.Member, error) {
	var m models.Member
	query := `SELECT ` + memberColumns + `
	          FROM people WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL`

	err := DB.QueryRow(query, memberID, companyID).Scan(
		&m.ID, &m.PublicID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
		&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMembersChangedSince retrieves members changed since a given timestamp.
//
// `since` arrives as the caller's raw string and is cast explicitly rather than
// left to be coerced by context. updated_at is TIMESTAMPTZ as of migration 010,
// so the cast decides how the caller's value is read:
//
//   - carrying an offset or a Z, it names an instant and is honoured exactly.
//     Before 010 the offset was silently DISCARDED, so a terminal politely
//     sending "…T09:00:00Z" was asking a question nobody answered correctly.
//   - carrying none, it is resolved in the session's zone, which the pool pins
//     to UTC. "No offset" therefore means UTC rather than meaning whatever the
//     database host is configured for.
//
// Both forms now round-trip: the value a caller passes back here is one this API
// gave it in a previous response, and those are RFC3339 in UTC.
func GetMembersChangedSince(companyID int64, since string) ([]models.Member, error) {
	query := `SELECT ` + memberColumns + `
	          FROM people WHERE company_id = $1 AND updated_at > $2::timestamptz AND deleted_at IS NULL
	          ORDER BY updated_at ASC`

	rows, err := DB.Query(query, companyID, since)
	if err != nil {
		return nil, err
	}
	return scanMembers(rows)
}

// CreateMember creates a new member within a company and queues a CREATE sync
// job for every device that must learn about them.
func CreateMember(companyID int64, member *models.Member) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO people (company_id, external_id, full_name, membership_type, active, fingerprint_template)
	          VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
	          RETURNING id, public_id, created_at, updated_at`

	err = tx.QueryRow(query, companyID, member.MemberID, member.FullName, member.MembershipType,
		member.Active, member.FingerprintTemplate).
		Scan(&member.ID, &member.PublicID, &member.CreatedAt, &member.UpdatedAt)
	if err != nil {
		return err
	}

	// The company's default access policy, written BEFORE the fan-out.
	//
	// Order matters and is not incidental: the fan-out is scoped by permission
	// (SEC-04), so a person with no rule yet reaches no terminal. Granting after
	// enqueueing would produce a person who is authorized but was never sent
	// anywhere, which is the most confusing possible state -- the console would
	// show access they have and the door would refuse them.
	if err := grantDefaultPersonAccessTx(tx, companyID, member.ID); err != nil {
		return err
	}

	if err := enqueuePersonChangeTx(tx, companyID, models.SyncJobCreate, member, false); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateMember updates an existing member and queues an UPDATE sync job
func UpdateMember(companyID int64, member *models.Member) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `UPDATE people SET full_name = $1, membership_type = $2, active = $3,
	          fingerprint_template = NULLIF($4, '')
	          WHERE external_id = $5 AND company_id = $6 AND deleted_at IS NULL
	          RETURNING id, public_id, created_at, updated_at`

	err = tx.QueryRow(query, member.FullName, member.MembershipType, member.Active,
		member.FingerprintTemplate, member.MemberID, companyID).
		Scan(&member.ID, &member.PublicID, &member.CreatedAt, &member.UpdatedAt)
	if err != nil {
		return err
	}

	if err := enqueuePersonChangeTx(tx, companyID, models.SyncJobUpdate, member, false); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteMember soft-deletes a member and queues a DELETE sync job.
//
// The DELETE job is the only way a terminal ever learns about a removal --
// GET /members/changes cannot express one, because a deleted row simply stops
// appearing in it. Deleting an already-deleted member is a no-op and queues
// nothing, which keeps repeated deletes idempotent.
func DeleteMember(companyID int64, memberID string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var member models.Member
	query := `UPDATE people SET deleted_at = CURRENT_TIMESTAMP
	          WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL
	          RETURNING id, public_id, external_id, full_name, membership_type, active, updated_at`

	err = tx.QueryRow(query, memberID, companyID).Scan(
		&member.ID, &member.PublicID, &member.MemberID, &member.FullName,
		&member.MembershipType, &member.Active, &member.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	if err := enqueuePersonChangeTx(tx, companyID, models.SyncJobDelete, &member, true); err != nil {
		return err
	}
	return tx.Commit()
}

// Enrollment Queries

// CreateEnrollmentRequest creates a new enrollment request for a member
func CreateEnrollmentRequest(companyID int64, memberID string) (*models.EnrollmentRequest, error) {
	query := `INSERT INTO enrollment_requests (person_id, status)
	          SELECT p.id, 'PENDING' FROM people p
	          WHERE p.external_id = $1 AND p.company_id = $2 AND p.deleted_at IS NULL
	          RETURNING id, public_id, status, created_at, completed_at`

	var req models.EnrollmentRequest
	err := DB.QueryRow(query, memberID, companyID).Scan(
		&req.ID, &req.PublicID, &req.Status, &req.CreatedAt, &req.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	req.MemberID = memberID
	return &req, nil
}

// GetPendingEnrollmentRequests retrieves all pending enrollment requests for a company
func GetPendingEnrollmentRequests(companyID int64) ([]models.EnrollmentRequest, error) {
	query := `SELECT er.id, er.public_id, p.external_id, er.status, er.created_at, er.completed_at
	          FROM enrollment_requests er
	          JOIN people p ON p.id = er.person_id
	          WHERE p.company_id = $1 AND er.status = 'PENDING' AND p.deleted_at IS NULL
	          ORDER BY er.created_at ASC`

	rows, err := DB.Query(query, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []models.EnrollmentRequest
	for rows.Next() {
		var req models.EnrollmentRequest
		err := rows.Scan(
			&req.ID, &req.PublicID, &req.MemberID, &req.Status, &req.CreatedAt, &req.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, rows.Err()
}

// UpdateEnrollmentRequestStatus updates the status of an enrollment request
func UpdateEnrollmentRequestStatus(id int64, status string) error {
	var completedAt sql.NullTime
	if status == "COMPLETED" || status == "FAILED" {
		now := time.Now()
		completedAt = sql.NullTime{Time: now, Valid: true}
	}

	query := `UPDATE enrollment_requests SET status = $1, completed_at = $2 WHERE id = $3`
	_, err := DB.Exec(query, status, completedAt, id)
	return err
}

// CompleteEnrollment completes enrollment by updating the person's fingerprint
// template and closing out their pending request
func CompleteEnrollment(companyID int64, memberID, fingerprintTemplate string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Records the enrolment. It does NOT touch `active`.
	//
	// This used to set `active = true`, which quietly made enrolment an
	// authorization decision: an operator suspends a member, someone presents
	// that member's finger at a terminal, and the enrolment report reactivates
	// them. Worse once terminals could call this with their own credential --
	// a device would have been able to restore access an operator had revoked,
	// which is not a privilege a door controller should hold.
	//
	// Enrolment and authorization are now strictly separate: this says "we have
	// a fingerprint for this person", and only an operator says "this person may
	// come in". A suspended member can be enrolled -- useful, since the finger
	// is captured ready for their return -- and stays suspended.
	//
	// `active` is still RETURNED, so the sync job below carries the member's
	// real state to every terminal rather than an assumed one.
	var member models.Member
	err = tx.QueryRow(`UPDATE people SET fingerprint_template = $1
	                   WHERE external_id = $2 AND company_id = $3 AND deleted_at IS NULL
	                   RETURNING id, public_id, external_id, full_name, membership_type,
	                             active, COALESCE(fingerprint_template, ''), updated_at`,
		fingerprintTemplate, memberID, companyID).Scan(
		&member.ID, &member.PublicID, &member.MemberID, &member.FullName,
		&member.MembershipType, &member.Active, &member.FingerprintTemplate, &member.UpdatedAt,
	)
	if err != nil {
		return err
	}

	// Update enrollment request status
	now := time.Now()
	_, err = tx.Exec(`UPDATE enrollment_requests SET status = 'COMPLETED', completed_at = $1
	                  WHERE status = 'PENDING' AND person_id = (
	                      SELECT id FROM people
	                      WHERE external_id = $2 AND company_id = $3 AND deleted_at IS NULL
	                  )`, now, memberID, companyID)
	if err != nil {
		return err
	}

	// A new fingerprint template is a change the terminals must receive
	if err := enqueuePersonChangeTx(tx, companyID, models.SyncJobUpdate, &member, false); err != nil {
		return err
	}

	return tx.Commit()
}

// Access Log Queries

// accessLogColumns is the shared projection for access log reads
const accessLogColumns = `id, public_id, person_external_id, granted, source, site_name,
	          COALESCE(message, '') AS message, created_at`

// scanAccessLogs reads a log result set built from accessLogColumns
func scanAccessLogs(rows *sql.Rows) ([]models.AccessLog, error) {
	defer rows.Close()

	var logs []models.AccessLog
	for rows.Next() {
		var log models.AccessLog
		err := rows.Scan(
			&log.ID, &log.PublicID, &log.MemberID, &log.Granted, &log.Source,
			&log.SiteName, &log.Message, &log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

// CreateAccessLog records an access attempt. The person is resolved from their
// external ID where possible; unmatched attempts are still logged with the raw
// identifier so denied unknown credentials leave a trail.
func CreateAccessLog(companyID, siteID int64, log *models.AccessLog) error {
	query := `INSERT INTO access_logs
	          (company_id, site_id, person_id, person_external_id, granted, source, site_name, message, occurred_at)
	          VALUES ($1, $2,
	                  (SELECT id FROM people
	                    WHERE external_id = $3 AND company_id = $1 AND deleted_at IS NULL),
	                  $3, $4, $5, $6, NULLIF($7, ''), CURRENT_TIMESTAMP)
	          RETURNING id, public_id, created_at`

	return DB.QueryRow(query, companyID, siteID, log.MemberID, log.Granted,
		log.Source, log.SiteName, log.Message).
		Scan(&log.ID, &log.PublicID, &log.CreatedAt)
}

// CreateDeviceAccessLog records a door event reported by a terminal.
//
// Separate from CreateAccessLog because the trust model is different, and the
// difference is the whole point:
//
//   - company, site and device come from the AUTHENTICATED DEVICE, never from
//     the body. A terminal cannot write a log against another site, because
//     there is no parameter through which it could name one.
//   - `eventID` becomes public_id, which is UNIQUE, and the insert is
//     ON CONFLICT DO NOTHING. A retry of an upload whose response was lost is
//     therefore a no-op rather than a duplicate audit line.
//   - occurredAt is the DEVICE's time, not the server's. A log queued offline
//     and uploaded hours later must say when the door opened, not when the
//     network came back. A zero time falls back to now, which is the honest
//     answer for a terminal whose clock has never synced.
//
// Returns whether a row was actually inserted, so the caller can distinguish a
// new event from a replayed one.
func CreateDeviceAccessLog(companyID, siteID, deviceID int64, eventID string,
	memberID *string, granted bool, source, siteName, message string,
	occurredAt time.Time) (bool, error) {

	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	// person_id is resolved by subquery and left NULL when the external id
	// matches nobody -- an unrecognised finger is still a real event and must
	// still be recorded.
	query := `INSERT INTO access_logs
	          (public_id, company_id, site_id, device_id, person_id,
	           person_external_id, granted, source, site_name, message, occurred_at)
	          VALUES ($1, $2, $3, $4,
	                  (SELECT id FROM people
	                    WHERE external_id = $5 AND company_id = $2 AND deleted_at IS NULL),
	                  $5, $6, $7, $8, NULLIF($9, ''), $10)
	          ON CONFLICT (public_id) DO NOTHING`

	result, err := DB.Exec(query, eventID, companyID, siteID, deviceID,
		memberID, granted, source, siteName, message, occurredAt)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// GetAccessLogs retrieves access logs for a company.
//
// Company-wide, and therefore reachable only from an operator session. The
// site-key surface uses GetSiteAccessLogs below.
func GetAccessLogs(companyID int64, limit int, offset int) ([]models.AccessLog, error) {
	query := `SELECT ` + accessLogColumns + `
	          FROM access_logs WHERE company_id = $1
	          ORDER BY occurred_at DESC LIMIT $2 OFFSET $3`

	rows, err := DB.Query(query, companyID, limit, offset)
	if err != nil {
		return nil, err
	}
	return scanAccessLogs(rows)
}

// GetSiteAccessLogs retrieves access logs for ONE site within a company.
//
// The site filter is what the site-key surface needs and did not have: that
// credential is installed on hardware at one location, and returning the whole
// company's audit trail to it meant a key at the smallest site could read who
// entered every other one.
//
// Both filters are applied. company_id is the tenancy boundary and site_id is
// the scope; keeping the tenancy filter even though site_id already implies it
// means this query is still correct if it is ever called with a site resolved
// some other way.
func GetSiteAccessLogs(companyID, siteID int64, limit, offset int) ([]models.AccessLog, error) {
	query := `SELECT ` + accessLogColumns + `
	          FROM access_logs WHERE company_id = $1 AND site_id = $2
	          ORDER BY occurred_at DESC LIMIT $3 OFFSET $4`

	rows, err := DB.Query(query, companyID, siteID, limit, offset)
	if err != nil {
		return nil, err
	}
	return scanAccessLogs(rows)
}

// GetAccessLogsByMember retrieves access logs for a specific member
func GetAccessLogsByMember(companyID int64, memberID string, limit int) ([]models.AccessLog, error) {
	query := `SELECT ` + accessLogColumns + `
	          FROM access_logs WHERE company_id = $1 AND person_external_id = $2
	          ORDER BY occurred_at DESC LIMIT $3`

	rows, err := DB.Query(query, companyID, memberID, limit)
	if err != nil {
		return nil, err
	}
	return scanAccessLogs(rows)
}

// GetSiteAccessLogsByMember is the same read, scoped to one site.
//
// The site-key surface uses this rather than the company-wide version above,
// for the same reason GetSiteAccessLogs exists: a credential installed at one
// location must not be able to reconstruct a person's movements across every
// other location the company operates.
func GetSiteAccessLogsByMember(companyID, siteID int64, memberID string, limit int) ([]models.AccessLog, error) {
	query := `SELECT ` + accessLogColumns + `
	          FROM access_logs
	         WHERE company_id = $1 AND site_id = $2 AND person_external_id = $3
	          ORDER BY occurred_at DESC LIMIT $4`

	rows, err := DB.Query(query, companyID, siteID, memberID, limit)
	if err != nil {
		return nil, err
	}
	return scanAccessLogs(rows)
}

// CheckMemberAccess checks if a member has access.
//
// This deliberately still checks only membership status. Evaluating the
// `permissions` table (door/site scope, schedules, validity windows) is
// business logic held back for a later sprint.
func CheckMemberAccess(companyID int64, memberID string) (*models.AccessCheckResponse, error) {
	var active bool
	var membershipType string

	query := `SELECT active, membership_type FROM people
	          WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL`
	err := DB.QueryRow(query, memberID, companyID).Scan(&active, &membershipType)

	if err == sql.ErrNoRows {
		return &models.AccessCheckResponse{
			Granted: false,
			Message: "Member not found",
			Status:  "NOT_FOUND",
		}, nil
	}
	if err != nil {
		return nil, err
	}

	if !active {
		return &models.AccessCheckResponse{
			Granted: false,
			Message: "Membership inactive",
			Status:  "INACTIVE",
		}, nil
	}

	return &models.AccessCheckResponse{
		Granted: true,
		Message: "Access Granted",
		Status:  "ACTIVE",
	}, nil
}
