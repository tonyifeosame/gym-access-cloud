package models

import (
	"encoding/json"
	"errors"
	"time"
)

// SyncProtocolVersion is the version of the device synchronization protocol
// this server speaks. It is stamped on every job at enqueue time and echoed in
// every device-facing envelope, so a newer server can keep serving older
// firmware while a fleet is upgraded.
//
// Bump this only for a breaking change to the job or envelope format.
const SyncProtocolVersion = 1

// Sync job types
const (
	SyncJobCreate   = "CREATE"
	SyncJobUpdate   = "UPDATE"
	SyncJobDelete   = "DELETE"
	SyncJobSettings = "SETTINGS"
)

// Sync job entity types
const (
	SyncEntityPerson   = "PERSON"
	SyncEntitySettings = "SETTINGS"
)

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

// ErrDeviceSiteMismatch is returned when a serial number is already registered
// to a different site. Reassignment is a provisioning decision, not something a
// registration call should do silently.
var ErrDeviceSiteMismatch = errors.New("device serial is registered to another site")

// Device represents a terminal installed at a site
type Device struct {
	ID           int64  `json:"id"`
	PublicID     string `json:"public_id"`
	SiteID       int64  `json:"site_id"`
	SerialNumber string `json:"serial_number"`
	DeviceName   string `json:"device_name"`
	DeviceType   string `json:"device_type"`
	Status       string `json:"status"`
	Active       bool   `json:"active"`
	APIKeyPrefix string `json:"api_key_prefix,omitempty"`
}

// DeviceIdentity is the authenticated caller behind a device request
type DeviceIdentity struct {
	ID           int64
	PublicID     string
	SerialNumber string
	SiteID       int64
	CompanyID    int64
	Status       string
	Active       bool
}

// DeviceRegistrationRequest is the body of POST /devices/register
type DeviceRegistrationRequest struct {
	SerialNumber    string `json:"serial_number" binding:"required"`
	DeviceName      string `json:"device_name,omitempty"`
	DeviceType      string `json:"device_type,omitempty"`
	FirmwareVersion string `json:"firmware_version,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
}

// DeviceRegistrationResponse carries the issued credential. The plaintext key is
// returned exactly once and is not recoverable afterwards.
type DeviceRegistrationResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	DeviceID        string `json:"device_id"`
	SerialNumber    string `json:"serial_number"`
	APIKey          string `json:"api_key"`
	BootstrapJobs   int    `json:"bootstrap_jobs"`
	Warning         string `json:"warning"`
}

// DeviceHeartbeatRequest is the body of POST /devices/heartbeat
type DeviceHeartbeatRequest struct {
	FirmwareVersion string `json:"firmware_version,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
}

// DeviceHeartbeatResponse tells a device whether it has work waiting
type DeviceHeartbeatResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	DeviceID        string    `json:"device_id"`
	ServerTime      time.Time `json:"server_time"`
	PendingJobs     int       `json:"pending_jobs"`
}

// SyncJob is one unit of change a device must apply. CREATE and UPDATE are both
// upserts on the device, which is what makes redelivery safe.
type SyncJob struct {
	ID               int64           `json:"id"`
	PublicID         string          `json:"public_id"`
	ProtocolVersion  int             `json:"protocol_version"`
	JobType          string          `json:"job_type"`
	EntityType       string          `json:"entity_type,omitempty"`
	EntityExternalID string          `json:"entity_external_id,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	Attempts         int             `json:"attempts"`
	CreatedAt        time.Time       `json:"created_at"`
}

// SyncJobBatch is the envelope returned to a device. The protocol version is at
// the envelope level so firmware can check compatibility before parsing jobs.
type SyncJobBatch struct {
	ProtocolVersion int       `json:"protocol_version"`
	DeviceID        string    `json:"device_id"`
	ServerTime      time.Time `json:"server_time"`
	Count           int       `json:"count"`
	Jobs            []SyncJob `json:"jobs"`
}

// SyncJobResult is a device's acknowledgement of a job it attempted
type SyncJobResult struct {
	Status string `json:"status" binding:"required"` // COMPLETED or FAILED
	Error  string `json:"error,omitempty"`
}

// SiteSettings is a site's device configuration and its monotonic version
type SiteSettings struct {
	Settings json.RawMessage `json:"settings"`
	Version  int             `json:"settings_version"`
}

// SettingsSyncPayload is the snapshot a device applies for a SETTINGS job. The
// version lets a device discard a stale push it receives after a newer one.
type SettingsSyncPayload struct {
	SettingsVersion int             `json:"settings_version"`
	Settings        json.RawMessage `json:"settings"`
}

// PersonSyncPayload is the snapshot a device applies for a PERSON job. For a
// DELETE the identifying fields are still present and Deleted is true, so the
// terminal can remove the record without ever having seen it created.
type PersonSyncPayload struct {
	MemberID            string    `json:"member_id"`
	FullName            string    `json:"full_name,omitempty"`
	MembershipType      string    `json:"membership_type,omitempty"`
	Active              bool      `json:"active"`
	FingerprintTemplate string    `json:"fingerprint_template,omitempty"`
	Deleted             bool      `json:"deleted"`
	UpdatedAt           time.Time `json:"updated_at"`
}
