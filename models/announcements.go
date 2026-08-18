package models

import "time"

// Terminal announcement request and response shapes.
//
// Two audiences with almost nothing in common share this file because they share
// one record: a terminal that has no credential and is asking to be set up, and
// an operator deciding whether to let it in. Keeping the two shapes beside each
// other is what makes it obvious that NO FIELD CROSSES BETWEEN THEM -- the
// device is never told who adopted it before it is approved, and the console is
// never shown the announce token.

// ---------------------------------------------------------------------------
// Device-facing
// ---------------------------------------------------------------------------

// AnnounceRequest is the body a terminal posts to /devices/announce.
//
// The firmware sends its MAC-derived serial and, if it already holds one, the
// announce token from a previous announcement. The token travels in the
// X-Announce-Token header rather than the body, so this shape stays the same
// whether or not the unit has been here before.
type AnnounceRequest struct {
	SerialNumber string `json:"serial_number" binding:"required"`

	// Reported so the console can show what it is about to approve. Optional:
	// a terminal that cannot report them is still a terminal.
	FirmwareVersion  string `json:"firmware_version,omitempty"`
	HardwareRevision string `json:"hardware_revision,omitempty"`

	// Capabilities is what this image says it can do (025), in the same shape
	// the heartbeat sends and stored under the same rules.
	//
	// NIL AND EMPTY ARE DIFFERENT and the slice preserves the difference: an
	// absent key decodes to nil, which merges as "unchanged", while `[]`
	// decodes to a non-nil empty slice, which is the real answer "I report my
	// capabilities and have none".
	//
	// SHOWN, NEVER TRUSTED. It reaches an operator's screen before they approve
	// and gates nothing: the gate reads devices.capabilities, which only an
	// authenticated heartbeat writes.
	Capabilities []string `json:"capabilities,omitempty"`
}

// AnnounceResponse is what the terminal gets back.
//
// PairingCode and AnnounceToken are present ONLY when this call created a new
// announcement. A unit re-announcing with a token it already holds is told the
// state and nothing else, so that the code a customer is part-way through typing
// cannot rotate underneath them.
type AnnounceResponse struct {
	AnnouncementID string `json:"announcement_id"`
	State          string `json:"state"`
	SerialNumber   string `json:"serial_number"`

	PairingCode   string `json:"pairing_code,omitempty"`
	AnnounceToken string `json:"announce_token,omitempty"`

	ExpiresAt        time.Time `json:"expires_at"`
	PollAfterSeconds int       `json:"poll_after_seconds"`
}

// AnnounceStatusResponse is the answer to the terminal's poll.
//
// FOUR STATES, and no more, because a terminal has four things it can do:
// keep showing its code (ANNOUNCED), tell the customer somebody is dealing with
// it (ADOPTED), store a credential (APPROVED), or start over (REFUSED).
// Rejected, expired, superseded and already-collected are all REFUSED -- the
// distinctions matter to an operator and mean nothing at a door.
type AnnounceStatusResponse struct {
	State string `json:"state"`

	// APIKey is delivered exactly once, in the response that moves the
	// announcement to COLLECTED.
	APIKey string `json:"api_key,omitempty"`

	SerialNumber string `json:"serial_number,omitempty"`

	// What the panel shows once the terminal is set up, so a customer can see
	// their own company and site name on the hardware and know it joined the
	// right account.
	CompanyName string `json:"company_name,omitempty"`
	SiteName    string `json:"site_name,omitempty"`
	DeviceName  string `json:"device_name,omitempty"`

	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	PollAfterSeconds int        `json:"poll_after_seconds"`
}

// ---------------------------------------------------------------------------
// Console-facing
// ---------------------------------------------------------------------------

// AdoptAnnouncementRequest is the one field the "Add a terminal" screen has.
type AdoptAnnouncementRequest struct {
	PairingCode string `json:"pairing_code" binding:"required"`
}

// ApproveAnnouncementRequest places an adopted terminal at a site.
//
// SiteID is the site's PUBLIC id, matching every other console route that names
// one. DeviceName is optional: the store defaults it to the serial, which is
// what registration has always done.
type ApproveAnnouncementRequest struct {
	SiteID     string `json:"site_id" binding:"required"`
	DeviceName string `json:"device_name,omitempty"`
}

// RejectAnnouncementRequest refuses one.
type RejectAnnouncementRequest struct {
	Reason string `json:"reason,omitempty"`
}

// PendingTerminal is the console's view of a terminal waiting to be set up.
//
// NO SECRET APPEARS HERE. Not the pairing code -- which is not readable back and
// exists only on the terminal's panel -- and not the announce token, which
// belongs to the device.
type PendingTerminal struct {
	ID           string `json:"id"`
	SerialNumber string `json:"serial_number"`

	// State is the stored lifecycle state, with one adjustment: a row past its
	// window reads as EXPIRED from the moment it is, rather than when a
	// background sweep notices. A console offering an Approve button the API
	// would refuse is a console that lies about what it can do.
	State string `json:"state"`

	// Verdict is what the console must SAY before an operator approves:
	// NEW, RE_PROVISION, REFUSED_OTHER_COMPANY or REFUSED_DISABLED. Recomputed
	// on every read, because what it describes can change between the screen
	// opening and the button being pressed.
	Verdict string `json:"verdict"`

	// ExistingTerminal is present for RE_PROVISION and REFUSED_DISABLED, so the
	// warning can name the terminal it is about to affect rather than describing
	// it in the abstract.
	ExistingTerminal *ExistingTerminalSummary `json:"existing_terminal,omitempty"`

	FirmwareVersion  string `json:"firmware_version,omitempty"`
	HardwareRevision string `json:"hardware_revision,omitempty"`

	// Capabilities is what the unit said it can do when it announced.
	//
	// OMITTED WHEN IT NEVER SAID, which a console must render as "unknown"
	// rather than as "none". The distinction is the useful one on this screen:
	// an administrator about to mount a terminal on a door wants to know
	// whether it can be recovered over the network, and "we have not been told"
	// and "it cannot" send them to different places.
	//
	// An EMPTY ARRAY is a real answer and is sent as one.
	//
	// SHOWN, NEVER TRUSTED. Nothing is gated on it -- the Change Wi-Fi gate
	// reads devices.capabilities, written only by an authenticated heartbeat --
	// because this arrives on an unauthenticated endpoint from hardware nobody
	// has confirmed yet.
	Capabilities []string `json:"capabilities,omitempty"`

	// The corroboration an operator uses to believe this is the unit in front of
	// them: where it called from, and how recently.
	FirstSeenIP string     `json:"first_seen_ip,omitempty"`
	LastSeenIP  string     `json:"last_seen_ip,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	AnnouncedAt time.Time  `json:"announced_at"`

	AdoptedBy string     `json:"adopted_by,omitempty"`
	AdoptedAt *time.Time `json:"adopted_at,omitempty"`

	SiteID     string `json:"site_id,omitempty"`
	SiteName   string `json:"site_name,omitempty"`
	DeviceName string `json:"device_name,omitempty"`

	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`

	ExpiresAt time.Time `json:"expires_at"`
}

// ExistingTerminalSummary names the terminal a re-provision would affect.
type ExistingTerminalSummary struct {
	SerialNumber string `json:"serial_number"`
	DeviceName   string `json:"device_name,omitempty"`
	SiteName     string `json:"site_name,omitempty"`
	Status       string `json:"status,omitempty"`
}

// PendingTerminalsResponse is the list envelope, matching every other console
// collection: a count and a named array.
type PendingTerminalsResponse struct {
	Count   int               `json:"count"`
	Pending []PendingTerminal `json:"pending"`
}

// ReleaseTerminalRequest is the platform administrator's reason.
type ReleaseTerminalRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ReleaseTerminalResponse reports what a release did.
//
// The counts are the facts the administrator has to relay to the customer losing
// the hardware: queued work that will never be delivered, and any setup that was
// in flight and has just stopped.
type ReleaseTerminalResponse struct {
	SerialNumber         string `json:"serial_number"`
	Released             bool   `json:"released"`
	PreviousCompany      string `json:"previous_company"`
	PreviousSite         string `json:"previous_site,omitempty"`
	PendingJobsCancelled int64  `json:"pending_jobs_cancelled"`
	AnnouncementsVoided  int64  `json:"announcements_voided"`
}
