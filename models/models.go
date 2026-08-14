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

// Site represents a physical location running an access terminal.
//
// THERE IS NO API KEY FIELD, AND THERE MUST NOT BE ONE. Since
// 011_site_credentials.sql the database stores only a SHA-256 hash, so there is
// no plaintext to put here -- but the omission is load-bearing beyond that.
// This struct is the authenticated site as the middleware resolves it, and it
// is one careless `c.JSON(200, site)` away from a response. The provisioning
// secret registers terminals and rotates their device credentials; a type that
// cannot carry it cannot leak it.
//
// The key is returned exactly twice in the lifetime of a site: from the console
// call that creates it and from the one that rotates it. Both use a dedicated
// response type that says so in its name.
type Site struct {
	ID        int64     `json:"id"`
	PublicID  string    `json:"public_id"`
	CompanyID int64     `json:"company_id"`
	SiteName  string    `json:"site_name" binding:"required"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Member represents an enrolled member. It maps to the `people` table, where
// Member.MemberID is stored as people.external_id.
type Member struct {
	ID                  int64     `json:"id"`
	PublicID            string    `json:"public_id"`
	MemberID            string    `json:"member_id" binding:"required"`
	FullName            string    `json:"full_name" binding:"required"`
	MembershipType      string    `json:"membership_type" binding:"required"`
	Active              bool      `json:"active"`
	FingerprintTemplate string    `json:"fingerprint_template,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// EnrollmentRequest represents a fingerprint enrollment request
type EnrollmentRequest struct {
	ID          int64      `json:"id"`
	PublicID    string     `json:"public_id"`
	MemberID    string     `json:"member_id" binding:"required"`
	Status      string     `json:"status" binding:"required"` // PENDING, IN_PROGRESS, COMPLETED, FAILED
	CreatedAt   time.Time  `json:"created_at"`
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

// AccessLogRequest is the request to log an access attempt.
//
// Note what is NOT required here, and why:
//
//   - Granted must not be `binding:"required"`. In go-playground/validator
//     "required" means non-zero, so on a bool it rejects `false` -- which would
//     make it impossible to log a denied entry. An audit trail that can only
//     record successes is worse than none.
//   - MemberID is optional so an unrecognised credential can still be logged.
//     The schema stores person_external_id as nullable precisely for this.
//   - SiteName is optional because the server derives it from the authenticated
//     site and ignores whatever the client sends.
type AccessLogRequest struct {
	MemberID string `json:"member_id,omitempty"`
	Granted  bool   `json:"granted"`
	Source   string `json:"source" binding:"required"`
	SiteName string `json:"site_name,omitempty"`
	Message  string `json:"message,omitempty"`
}

// DeviceAccessLogRequest is the body of POST /api/v1/devices/access/log.
//
// A terminal reports door events with its OWN credential. The operator endpoint
// (POST /access/log) takes the site key, which is the provisioning secret --
// putting that on every terminal so it can write an audit line would hand every
// door the ability to register devices and rotate their credentials.
//
// WHAT THE DEVICE MAY NOT SAY. There is no site, company or device field here,
// deliberately. All three are taken from the authenticated credential, so a
// terminal cannot write a log against another site however it is asked to.
// Anything of the sort in the body is ignored because there is nowhere for it
// to land.
//
// EventID is the idempotency key and the reason retries are safe. The terminal
// generates it once per door event and reuses it for every retry of that event;
// the server maps it onto access_logs.public_id, which is UNIQUE, and a repeat
// insert is discarded. Without it a terminal that uploaded successfully but
// lost the response would duplicate the event on the next attempt -- and an
// audit trail that double-counts entries is not one.
type DeviceAccessLogRequest struct {
	// A UUID the DEVICE generates. Required: an absent one would make every
	// retry a new event.
	EventID string `json:"event_id" binding:"required,uuid"`

	// Empty for an unrecognised finger, which is stored as NULL and matches no
	// person. A denial is the more interesting half of an audit trail.
	MemberID string `json:"member_id,omitempty"`

	Granted bool   `json:"granted"`
	Source  string `json:"source" binding:"required"`
	Message string `json:"message,omitempty"`

	// When the event happened at the door, RFC3339. Optional, because a terminal
	// whose clock has not synced cannot supply an honest one -- the server then
	// records its own arrival time and the log says so by omission rather than
	// by inventing a timestamp.
	//
	// A queued event uploaded hours later is the normal case, so this is the
	// difference between an audit trail that reflects the door and one that
	// reflects the network.
	OccurredAt string `json:"occurred_at,omitempty"`
}

// MemberUpdateRequest is the body of PUT /members/:id.
//
// Separate from Member because the member id comes from the URL; requiring it in
// the body as well made a correct-looking update fail validation.
type MemberUpdateRequest struct {
	FullName            string `json:"full_name" binding:"required"`
	MembershipType      string `json:"membership_type" binding:"required"`
	Active              bool   `json:"active"`
	FingerprintTemplate string `json:"fingerprint_template,omitempty"`
}

// MemberChangesRequest is the request for member changes
type MemberChangesRequest struct {
	Since string `json:"since" binding:"required"` // ISO 8601 timestamp
}

// ErrDeviceSiteMismatch is returned when a serial number is already registered
// to a different site. Reassignment is a provisioning decision, not something a
// registration call should do silently.
var ErrDeviceSiteMismatch = errors.New("device serial is registered to another site")

// ErrDeviceDisabled is returned when registration targets a device an operator
// has taken out of service, by status or by clearing `active`.
//
// Disabling is currently the only way to revoke a terminal -- there is no
// revocation endpoint -- so registration must not be a way around it. Otherwise
// anyone holding the site key could re-enable a device that was deliberately
// stopped, and be issued a working credential for it in the same call.
var ErrDeviceDisabled = errors.New("device is administratively disabled")

// ErrDeviceNotFound is returned when a serial number does not resolve within the
// caller's company. Reported rather than 403 for the usual reason: the API must
// not confirm that a serial exists in someone else's account.
var ErrDeviceNotFound = errors.New("device not found")

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
//
// device_type and release_channel are validated here rather than being left to
// the CHECK constraints on `devices`. Those constraints do reject a bad value,
// but they reject it as a database error, which the handler cannot distinguish
// from the database being down -- so a terminal sending `"device_type":"ESP32"`
// received a 500 and retried forever instead of a 400 telling it what was
// wrong. `oneof` on an `omitempty` field only fires when the field is present,
// so the defaults applied in the database layer still stand.
type DeviceRegistrationRequest struct {
	SerialNumber     string `json:"serial_number" binding:"required"`
	DeviceName       string `json:"device_name,omitempty"`
	DeviceType       string `json:"device_type,omitempty" binding:"omitempty,oneof=TERMINAL READER CONTROLLER"`
	FirmwareVersion  string `json:"firmware_version,omitempty"`
	HardwareRevision string `json:"hardware_revision,omitempty"`
	BuildNumber      string `json:"build_number,omitempty"`
	ReleaseChannel   string `json:"release_channel,omitempty" binding:"omitempty,oneof=STABLE BETA CANARY"`
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
//
// TWO SITE IDENTIFIERS, AND THE SECOND IS THE USEFUL ONE. SiteID is the internal
// row id and predates the console; SitePublicID is the same site's public_id,
// which is what /console/sites returns as its `id`. Without it a browser holding
// a terminal and a site had nothing to match them on but site_name -- names are
// editable and not unique, so scoping a terminal list to the selected site, or
// linking a terminal to the site it stands at, was not something the console
// could do correctly. Added rather than swapped: the internal id is part of a
// contract terminals and existing tooling already speak.
type DeviceInventory struct {
	ID                     int64      `json:"id"`
	PublicID               string     `json:"public_id"`
	SiteID                 int64      `json:"site_id"`
	SitePublicID           string     `json:"site_public_id"`
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

// SyncJobResult is a device's acknowledgement of a job it attempted.
//
// Status is not required: an empty body and `{}` both mean COMPLETED, so a
// minimal acknowledgement from constrained firmware is always valid.
type SyncJobResult struct {
	Status string `json:"status,omitempty"` // COMPLETED (default) or FAILED
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
