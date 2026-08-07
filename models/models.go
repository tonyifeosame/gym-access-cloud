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

// Device states. PROVISIONING is not device-reportable: it is the state of a
// row created before the device ever registered.
const (
	DeviceProvisioning = "PROVISIONING"
	DeviceOnline       = "ONLINE"
	DeviceOffline      = "OFFLINE"
	DeviceUpdating     = "UPDATING"
	DeviceError        = "ERROR"
	DeviceDisabled     = "DISABLED"
)

// DeviceReportableStates are the states a device may claim for itself. OFFLINE
// is inferred by the server from missed heartbeats, and DISABLED/PROVISIONING
// are administrative, so a device cannot assert any of them.
var DeviceReportableStates = map[string]bool{
	DeviceOnline:   true,
	DeviceUpdating: true,
	DeviceError:    true,
}

// DeviceRegistrationRequest is the body of POST /devices/register
type DeviceRegistrationRequest struct {
	SerialNumber     string `json:"serial_number" binding:"required"`
	DeviceName       string `json:"device_name,omitempty"`
	DeviceType       string `json:"device_type,omitempty"`
	FirmwareVersion  string `json:"firmware_version,omitempty"`
	HardwareRevision string `json:"hardware_revision,omitempty"`
	BuildNumber      string `json:"build_number,omitempty"`
	ReleaseChannel   string `json:"release_channel,omitempty"`
	IPAddress        string `json:"ip_address,omitempty"`
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
	FirmwareVersion  string `json:"firmware_version,omitempty"`
	HardwareRevision string `json:"hardware_revision,omitempty"`
	BuildNumber      string `json:"build_number,omitempty"`
	BootCount        *int   `json:"boot_count,omitempty"`
	Status           string `json:"status,omitempty"` // ONLINE, UPDATING or ERROR
	Error            string `json:"error,omitempty"`
	IPAddress        string `json:"ip_address,omitempty"`
}

// DeviceInventory is a device as the dashboard sees it, including whether it is
// behind the current build for its release channel.
type DeviceInventory struct {
	ID                     int64      `json:"id"`
	PublicID               string     `json:"public_id"`
	SiteID                 int64      `json:"site_id"`
	SiteName               string     `json:"site_name"`
	SerialNumber           string     `json:"serial_number"`
	DeviceName             string     `json:"device_name"`
	DeviceType             string     `json:"device_type"`
	Status                 string     `json:"status"`
	Active                 bool       `json:"active"`
	ReleaseChannel         string     `json:"release_channel"`
	FirmwareVersion        string     `json:"firmware_version"`
	HardwareRevision       string     `json:"hardware_revision"`
	BuildNumber            string     `json:"build_number"`
	BootCount              *int       `json:"boot_count,omitempty"`
	LastSeenAt             *time.Time `json:"last_seen_at,omitempty"`
	LastSyncAt             *time.Time `json:"last_sync_at,omitempty"`
	LastHeartbeatAt        *time.Time `json:"last_heartbeat_at,omitempty"`
	CurrentFirmwareVersion string     `json:"current_firmware_version"`
	FirmwareOutdated       bool       `json:"firmware_outdated"`
}

// FleetSummary is the device-count rollup a dashboard header shows
type FleetSummary struct {
	Total            int `json:"total"`
	Online           int `json:"online"`
	Offline          int `json:"offline"`
	Updating         int `json:"updating"`
	Error            int `json:"error"`
	Disabled         int `json:"disabled"`
	Provisioning     int `json:"provisioning"`
	FirmwareOutdated int `json:"firmware_outdated"`
}

// FirmwareVersion is a build in the catalog
type FirmwareVersion struct {
	ID             int64      `json:"id"`
	PublicID       string     `json:"public_id"`
	Version        string     `json:"version"`
	DeviceType     string     `json:"device_type"`
	ReleaseChannel string     `json:"release_channel"`
	DownloadURL    string     `json:"download_url,omitempty"`
	ChecksumSHA256 string     `json:"checksum_sha256,omitempty"`
	SizeBytes      *int64     `json:"size_bytes,omitempty"`
	ReleaseNotes   string     `json:"release_notes,omitempty"`
	IsMandatory    bool       `json:"is_mandatory"`
	IsCurrent      bool       `json:"is_current"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateFirmwareRequest is the body of POST /firmware
type CreateFirmwareRequest struct {
	Version        string `json:"version" binding:"required"`
	DeviceType     string `json:"device_type,omitempty"`
	ReleaseChannel string `json:"release_channel,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	ChecksumSHA256 string `json:"checksum_sha256,omitempty"`
	SizeBytes      *int64 `json:"size_bytes,omitempty"`
	ReleaseNotes   string `json:"release_notes,omitempty"`
	IsMandatory    bool   `json:"is_mandatory,omitempty"`
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
	SnapshotTaken   bool      `json:"snapshot_taken"`
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
