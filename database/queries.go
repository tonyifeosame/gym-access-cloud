package database

import (
	"database/sql"
	"time"

	"gym-access-api/models"
)

// Site Queries

// GetSiteByAPIKey retrieves a site by its API key
func GetSiteByAPIKey(apiKey string) (*models.Site, error) {
	var site models.Site
	query := `SELECT id, site_name, api_key, active, created_at, updated_at 
	          FROM sites WHERE api_key = $1 AND active = true`
	
	err := DB.QueryRow(query, apiKey).Scan(
		&site.ID, &site.SiteName, &site.APIKey, &site.Active, 
		&site.CreatedAt, &site.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &site, nil
}

// Member Queries

// GetAllMembers retrieves all members
func GetAllMembers() ([]models.Member, error) {
	query := `SELECT id, member_id, full_name, membership_type, active, 
	          fingerprint_template, created_at, updated_at 
	          FROM members ORDER BY created_at DESC`
	
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []models.Member
	for rows.Next() {
		var m models.Member
		err := rows.Scan(
			&m.ID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
			&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

// GetMemberByID retrieves a member by member_id
func GetMemberByID(memberID string) (*models.Member, error) {
	var m models.Member
	query := `SELECT id, member_id, full_name, membership_type, active, 
	          fingerprint_template, created_at, updated_at 
	          FROM members WHERE member_id = $1`
	
	err := DB.QueryRow(query, memberID).Scan(
		&m.ID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
		&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMembersChangedSince retrieves members changed since a given timestamp
func GetMembersChangedSince(since string) ([]models.Member, error) {
	query := `SELECT id, member_id, full_name, membership_type, active, 
	          fingerprint_template, created_at, updated_at 
	          FROM members WHERE updated_at > $1 ORDER BY updated_at ASC`
	
	rows, err := DB.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []models.Member
	for rows.Next() {
		var m models.Member
		err := rows.Scan(
			&m.ID, &m.MemberID, &m.FullName, &m.MembershipType, &m.Active,
			&m.FingerprintTemplate, &m.CreatedAt, &m.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

// CreateMember creates a new member
func CreateMember(member *models.Member) error {
	query := `INSERT INTO members (member_id, full_name, membership_type, active, fingerprint_template) 
	          VALUES ($1, $2, $3, $4, $5) 
	          RETURNING id, created_at, updated_at`
	
	err := DB.QueryRow(query, member.MemberID, member.FullName, member.MembershipType, 
		member.Active, member.FingerprintTemplate).Scan(&member.ID, &member.CreatedAt, &member.UpdatedAt)
	if err != nil {
		return err
	}
	return nil
}

// UpdateMember updates an existing member
func UpdateMember(member *models.Member) error {
	query := `UPDATE members SET full_name = $1, membership_type = $2, active = $3, 
	          fingerprint_template = $4 WHERE member_id = $5 
	          RETURNING updated_at`
	
	err := DB.QueryRow(query, member.FullName, member.MembershipType, member.Active,
		member.FingerprintTemplate, member.MemberID).Scan(&member.UpdatedAt)
	if err != nil {
		return err
	}
	return nil
}

// DeleteMember deletes a member by member_id
func DeleteMember(memberID string) error {
	query := `DELETE FROM members WHERE member_id = $1`
	_, err := DB.Exec(query, memberID)
	return err
}

// Enrollment Queries

// CreateEnrollmentRequest creates a new enrollment request
func CreateEnrollmentRequest(memberID string) (*models.EnrollmentRequest, error) {
	query := `INSERT INTO enrollment_requests (member_id, status) 
	          VALUES ($1, 'PENDING') 
	          RETURNING id, member_id, status, created_at, completed_at`
	
	var req models.EnrollmentRequest
	err := DB.QueryRow(query, memberID).Scan(
		&req.ID, &req.MemberID, &req.Status, &req.CreatedAt, &req.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// GetPendingEnrollmentRequests retrieves all pending enrollment requests
func GetPendingEnrollmentRequests() ([]models.EnrollmentRequest, error) {
	query := `SELECT id, member_id, status, created_at, completed_at 
	          FROM enrollment_requests WHERE status = 'PENDING' ORDER BY created_at ASC`
	
	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []models.EnrollmentRequest
	for rows.Next() {
		var req models.EnrollmentRequest
		err := rows.Scan(
			&req.ID, &req.MemberID, &req.Status, &req.CreatedAt, &req.CompletedAt,
		)
		if err != nil {
			return nil, err
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// UpdateEnrollmentRequestStatus updates the status of an enrollment request
func UpdateEnrollmentRequestStatus(id int, status string) error {
	var completedAt sql.NullTime
	if status == "COMPLETED" || status == "FAILED" {
		now := time.Now()
		completedAt = sql.NullTime{Time: now, Valid: true}
	}
	
	query := `UPDATE enrollment_requests SET status = $1, completed_at = $2 WHERE id = $3`
	_, err := DB.Exec(query, status, completedAt, id)
	return err
}

// CompleteEnrollment completes enrollment by updating member fingerprint and request status
func CompleteEnrollment(memberID, fingerprintTemplate string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Update member fingerprint template and set active to true
	_, err = tx.Exec(`UPDATE members SET fingerprint_template = $1, active = true WHERE member_id = $2`, 
		fingerprintTemplate, memberID)
	if err != nil {
		return err
	}

	// Update enrollment request status
	now := time.Now()
	_, err = tx.Exec(`UPDATE enrollment_requests SET status = 'COMPLETED', completed_at = $1 
	                  WHERE member_id = $2 AND status = 'PENDING'`, now, memberID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// Access Log Queries

// CreateAccessLog creates a new access log entry
func CreateAccessLog(log *models.AccessLog) error {
	query := `INSERT INTO access_logs (member_id, granted, source, site_name, message) 
	          VALUES ($1, $2, $3, $4, $5) 
	          RETURNING id, created_at`
	
	err := DB.QueryRow(query, log.MemberID, log.Granted, log.Source, log.SiteName, log.Message).
		Scan(&log.ID, &log.CreatedAt)
	if err != nil {
		return err
	}
	return nil
}

// GetAccessLogs retrieves access logs with optional filtering
func GetAccessLogs(limit int, offset int) ([]models.AccessLog, error) {
	query := `SELECT id, member_id, granted, source, site_name, message, created_at 
	          FROM access_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`
	
	rows, err := DB.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.AccessLog
	for rows.Next() {
		var log models.AccessLog
		err := rows.Scan(
			&log.ID, &log.MemberID, &log.Granted, &log.Source, 
			&log.SiteName, &log.Message, &log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, nil
}

// GetAccessLogsByMember retrieves access logs for a specific member
func GetAccessLogsByMember(memberID string, limit int) ([]models.AccessLog, error) {
	query := `SELECT id, member_id, granted, source, site_name, message, created_at 
	          FROM access_logs WHERE member_id = $1 ORDER BY created_at DESC LIMIT $2`
	
	rows, err := DB.Query(query, memberID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.AccessLog
	for rows.Next() {
		var log models.AccessLog
		err := rows.Scan(
			&log.ID, &log.MemberID, &log.Granted, &log.Source, 
			&log.SiteName, &log.Message, &log.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, nil
}

// CheckMemberAccess checks if a member has access
func CheckMemberAccess(memberID string) (*models.AccessCheckResponse, error) {
	var active bool
	var membershipType string
	
	query := `SELECT active, membership_type FROM members WHERE member_id = $1`
	err := DB.QueryRow(query, memberID).Scan(&active, &membershipType)
	
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
