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

// MemberResponse is a person as the LEGACY site-key API returns them.
//
// THE DIFFERENCE FROM Member IS THE POINT: there is no FingerprintTemplate
// field, and there must never be one.
//
// GET /members and GET /members/{id} used to return the template column for
// every person in the COMPANY to anyone holding ANY site key. A site key is a
// provisioning secret installed on hardware bolted to a wall at one location; it
// is handled by whoever installs terminals, and it should not be able to read a
// tenant's credential material at all, let alone across every site.
//
// The field carries a credential locator today rather than a template, but the
// column was designed to hold a template and the enrolment endpoint still writes
// to it, so the exposure is about what the endpoint is permitted to disclose
// rather than about what happens to be in the column this month.
//
// A type that cannot carry the value cannot leak it, which is the same
// construction models.Site uses to keep the site key out of responses.
//
// BiometricEnrolled replaces it with the only fact a caller legitimately needs:
// whether a credential exists. That is what the console shows and what an
// integration checking enrolment status is actually asking.
type MemberResponse struct {
	ID             int64  `json:"id"`
	PublicID       string `json:"public_id"`
	MemberID       string `json:"member_id"`
	FullName       string `json:"full_name"`
	MembershipType string `json:"membership_type"`
	Active         bool   `json:"active"`

	// Whether this person has a credential, never which one or what it holds.
	BiometricEnrolled bool `json:"biometric_enrolled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewMemberResponse projects a person for the legacy API, dropping the
// credential column.
func NewMemberResponse(m Member) MemberResponse {
	return MemberResponse{
		ID:                m.ID,
		PublicID:          m.PublicID,
		MemberID:          m.MemberID,
		FullName:          m.FullName,
		MembershipType:    m.MembershipType,
		Active:            m.Active,
		BiometricEnrolled: m.FingerprintTemplate != "",
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
	}
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

// AccessCheckResponse is gone with the function that built it (S4).
//
// It carried `granted`, `message` and `status` for an answer computed from
// `people.active` alone. GET /access/{member_id} still emits those three keys so
// an existing client keeps parsing, but they now come from AccessDecision --
// which is the type that actually knows what it is talking about, and the one a
// new field belongs on.

// EnrollmentStartRequest is the request to start enrollment
type EnrollmentStartRequest struct {
	MemberID string `json:"member_id" binding:"required"`
}

// EnrolledCredential is the structured placement a terminal now sends alongside
// the legacy locator string.
//
// THE FIRMWARE HAS BEEN SENDING THIS ALREADY and the server has been discarding
// it: ShouldBindJSON ignores unknown keys, so the object arrived and went
// nowhere. Binding it lets an enrolment write a real `credentials` row and a
// real `credential_placements` row instead of a string in a column, WITH NO
// FIRMWARE CHANGE AT ALL -- which is exactly what
// docs/firmware-protocol-requirements.md §4 proposed.
//
// The values are the firmware's own enums (credential_ref.h) and match the
// platform's CHECK constraints exactly: FINGERPRINT/CARD/PIN/MOBILE/FACE/QR for
// the type, SENSOR_LOCAL/VENDOR_TEMPLATE/IDENTIFIER for the format.
//
// NONE OF THIS IS MATERIAL. A slot number and a vendor name describe WHERE a
// template lives, not what it is.
type EnrolledCredential struct {
	CredentialType string `json:"credential_type,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`
	Vendor         string `json:"vendor,omitempty"`

	// Terminal is the serial the firmware believes it is. Recorded but NOT
	// trusted: the placement is written against the authenticated device, so a
	// terminal claiming another's serial changes nothing.
	Terminal string `json:"terminal,omitempty"`

	Slot int `json:"slot,omitempty"`
}

// EnrollmentResultRequest is the request to submit enrollment result.
//
// FingerprintTemplate stays required and stays first. It is not a template --
// it is a locator, "terminal:<serial>:slot:<n>" -- and the name is frozen
// because deployed firmware sends it and the 012 back-fill parses it. Renaming a
// JSON field that shipped hardware writes is not a cosmetic change.
type EnrollmentResultRequest struct {
	MemberID            string `json:"member_id" binding:"required"`
	FingerprintTemplate string `json:"fingerprint_template" binding:"required"`

	// Credential is optional and additive: older firmware omits it entirely and
	// the enrolment still completes exactly as it did.
	Credential *EnrolledCredential `json:"credential,omitempty"`
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

// MaxDeviceSerialLength is what the FIRMWARE can hold.
//
// device_identity.h sizes a serial at char[16], so fifteen characters plus a
// terminator. A claim code issued for anything longer could never be redeemed by
// the hardware it was issued for, because that hardware cannot represent its own
// serial to send back. Refused when the code is minted rather than discovered by
// an installer at a door.
const MaxDeviceSerialLength = 15

// ErrDeviceSerialTooLong reports a serial no terminal could present.
var ErrDeviceSerialTooLong = errors.New(
	"serial_number must be 15 characters or fewer, which is what the terminal can store")

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

	// MemberCapacity is how many people this terminal can hold at once (FW-01).
	//
	// OPTIONAL, AND ITS ABSENCE IS MEANINGFUL. Every terminal in the field today
	// omits it, and the server treats "not reported" as "unknown" rather than
	// substituting a constant -- the on-device ceiling has already changed once
	// (64 to 256) and a server that guessed would refuse rosters a terminal can
	// hold. See database/capacity.go.
	//
	// A pointer so a terminal reporting 0 is distinguishable from one reporting
	// nothing. Zero is refused rather than stored: a terminal that can hold
	// nobody is not a state the firmware can be in, and accepting it would
	// silently stop that door being given a roster at all.
	//
	// THE FIRMWARE HALF IS NOT BUILT (AI #2). The exact contract is written out
	// in docs/sync-protocol.md; this side is ready for it and is useful before
	// it lands, because the column is also settable by an operator who knows
	// what they installed.
	MemberCapacity *int `json:"member_capacity,omitempty"`
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

	// Capacity (FW-01).
	//
	// MemberCapacity is OMITTED when the terminal has never reported one, and a
	// client must render that as "unknown" rather than as unlimited or as zero.
	// Every terminal in the field today omits it.
	//
	// RosterOverflowAt is when this terminal was last found to have more
	// permitted people than it can hold, and RosterOverflowCount how many that
	// was. Both are stored state rather than a live count, so a list read costs
	// nothing extra -- the live number is on the single-terminal read.
	MemberCapacity      *int       `json:"member_capacity,omitempty"`
	RosterOverflowAt    *time.Time `json:"roster_overflow_at,omitempty"`
	RosterOverflowCount *int       `json:"roster_overflow_count,omitempty"`
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

// FirmwareUpdateOffer is an image the platform is offering a terminal.
//
// THE FIELD NAMES ARE A WIRE CONTRACT with the firmware's FirmwareOffer
// (firmware_update.h) and are specified in
// docs/firmware-protocol-requirements.md §5. They match the catalogue columns
// they come from, which is what makes the mapping checkable by reading it.
//
// Nothing here is optional. The device refuses an offer missing a digest or a
// size, and the server withholds one it could not populate -- see
// database/firmware_offer.go, where each refusal is logged with the reason.
type FirmwareUpdateOffer struct {
	Version        string `json:"version"`
	DownloadURL    string `json:"download_url"`
	ChecksumSHA256 string `json:"checksum_sha256"`
	SizeBytes      int64  `json:"size_bytes"`

	// IsMandatory changes WHEN, not WHETHER. The device treats it as a
	// scheduling signal and applies every other check unchanged -- if it could
	// relax one, "mandatory" would be the word an attacker used.
	IsMandatory bool `json:"is_mandatory"`
}

// DeviceHeartbeatResponse tells a device whether it has work waiting.
//
// ServerTime is marshalled by encoding/json as RFC3339 with a Z, because the
// value is always constructed in UTC. The firmware parses it as an authoritative
// clock when it cannot reach NTP, and REFUSES a numeric offset rather than
// silently mis-parsing it -- so a change that made this a local time would take
// the fleet's clock with it.
type DeviceHeartbeatResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	DeviceID        string    `json:"device_id"`
	ServerTime      time.Time `json:"server_time"`
	PendingJobs     int       `json:"pending_jobs"`

	// FirmwareUpdate is present only when there is one to offer, so a fleet
	// that is up to date sees exactly the response it saw before this field
	// existed. Older firmware ignores an unknown key; newer firmware treats an
	// absent one as "nothing to do".
	FirmwareUpdate *FirmwareUpdateOffer `json:"firmware_update,omitempty"`
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

	// THE EFFECTIVE POLICY, ALWAYS PRESENT (F2/F3/F4).
	//
	// `settings` is the free-form object an operator writes. The offline policy
	// is NOT in it -- it lives in validated columns, and the terminal is sent
	// those columns layered over the blob (database/device_settings.go).
	//
	// Without these two fields a caller reading this endpoint saw the blob and
	// had no way to learn what the terminals at that site are actually running.
	// If somebody had written `offline_grace_minutes` into the free-form object,
	// the read showed THAT number -- convincing, and inert, because the merge
	// overwrites it on the way to the device. Writing the reserved keys is now
	// refused outright, and these fields say what is really in force, so the two
	// halves of that trap are both closed.
	OfflinePolicy       string `json:"offline_policy"`
	OfflineGraceMinutes int    `json:"offline_grace_minutes"`
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
