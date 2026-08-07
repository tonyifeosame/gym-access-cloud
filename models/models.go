package models

import "time"

// Site represents a physical location running an access terminal
type Site struct {
	ID        int64     `json:"id"`
	PublicID  string    `json:"public_id"`
	CompanyID int64     `json:"company_id"`
	SiteName  string    `json:"site_name" binding:"required"`
	APIKey    string    `json:"api_key" binding:"required"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Member represents an enrolled member. It maps to the `people` table, where
// Member.MemberID is stored as people.external_id.
type Member struct {
	ID                    int64      `json:"id"`
	PublicID              string     `json:"public_id"`
	MemberID              string     `json:"member_id" binding:"required"`
	FullName              string     `json:"full_name" binding:"required"`
	MembershipType        string     `json:"membership_type" binding:"required"`
	Active                bool       `json:"active"`
	FingerprintTemplate   string     `json:"fingerprint_template,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// EnrollmentRequest represents a fingerprint enrollment request
type EnrollmentRequest struct {
	ID          int64     `json:"id"`
	PublicID    string    `json:"public_id"`
	MemberID    string    `json:"member_id" binding:"required"`
	Status      string    `json:"status" binding:"required"` // PENDING, IN_PROGRESS, COMPLETED, FAILED
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// AccessLog represents an access attempt log
type AccessLog struct {
	ID        int64     `json:"id"`
	PublicID  string    `json:"public_id"`
	MemberID  *string   `json:"member_id,omitempty"`
	Granted   bool      `json:"granted"`
	Source    string    `json:"source" binding:"required"`
	SiteName  string    `json:"site_name" binding:"required"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AccessCheckRequest is the request body for access check
type AccessCheckRequest struct {
	MemberID string `json:"member_id" binding:"required"`
}

// AccessCheckResponse is the response for access check
type AccessCheckResponse struct {
	Granted bool   `json:"granted"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// EnrollmentStartRequest is the request to start enrollment
type EnrollmentStartRequest struct {
	MemberID string `json:"member_id" binding:"required"`
}

// EnrollmentResultRequest is the request to submit enrollment result
type EnrollmentResultRequest struct {
	MemberID            string `json:"member_id" binding:"required"`
	FingerprintTemplate string `json:"fingerprint_template" binding:"required"`
}

// AccessLogRequest is the request to log an access attempt
type AccessLogRequest struct {
	MemberID string `json:"member_id" binding:"required"`
	Granted  bool   `json:"granted" binding:"required"`
	Source   string `json:"source" binding:"required"`
	SiteName string `json:"site_name" binding:"required"`
	Message  string `json:"message,omitempty"`
}

// MemberChangesRequest is the request for member changes
type MemberChangesRequest struct {
	Since string `json:"since" binding:"required"` // ISO 8601 timestamp
}
