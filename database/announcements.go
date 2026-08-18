package database

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// Terminal announcements (migrations/022_terminal_announcements.sql).
//
// ANNOUNCE AND APPROVE. The terminal introduces itself to the cloud and displays
// an eight-character pairing code on its own panel. An authenticated operator
// types that code into the console, confirms the unit, picks a site, and
// approves. The terminal then collects its credential.
//
// ---------------------------------------------------------------------------
// WHY THIS EXISTS ALONGSIDE CLAIM CODES RATHER THAN INSTEAD OF THEM
// ---------------------------------------------------------------------------
//
// A claim code is minted FOR A SERIAL, and that binding is what makes an
// intercepted code close to worthless. It is also what makes the flow unusable
// by a customer: the serial is derived from the factory MAC and is printed in
// exactly one place, the terminal's USB console at boot. Issuing a code needs a
// cable to read the serial; redeeming one needs a cable to type the code.
//
// The fix is NOT to drop the serial binding and mint codes that any hardware can
// redeem -- that would take the one property the unauthenticated redemption path
// rests on and throw it away. It is to reverse the direction of the secret.
//
// ---------------------------------------------------------------------------
// WHAT MAKES AN UNAUTHENTICATED ANNOUNCE ENDPOINT ACCEPTABLE
// ---------------------------------------------------------------------------
//
//  1. ANNOUNCING GRANTS NOTHING. A row in this table is not a device, not a
//     credential and not a member of any company. It is a request to be looked
//     at, and until an operator acts on it it is visible to nobody at all --
//     there is no route that lists un-adopted announcements.
//  2. THE PAIRING CODE TRAVELS OUTWARD, ON GLASS. It is displayed on the unit
//     and typed by a human standing in front of it. An attacker who wants one
//     has to be looking at the customer's hardware.
//  3. THE CREDENTIAL IS MINTED BY AN AUTHENTICATED ADMIN'S DECISION. Not by the
//     announcement, not by the code, not by any device-side event.
//  4. THE ANNOUNCE TOKEN MAKES THE POLL AUTHENTICATED. Only the caller that
//     created an announcement can collect against it, so the credential cannot
//     be intercepted by something that merely knows the serial.
//  5. SHORT LIVED, HASHED AT REST, ONE LIVE ROW PER SERIAL, and every refusal is
//     the same refusal.
//
// Rate limiting is the sixth and lives at the route, because it is a property of
// the HTTP surface rather than of the record.
//
// ---------------------------------------------------------------------------
// THE ONE RULE THAT IS NEVER RELAXED
// ---------------------------------------------------------------------------
//
// A SERIAL THAT BELONGS TO ANOTHER COMPANY IS REFUSED. Checked when the code is
// adopted, checked again when the announcement is approved, and enforced a third
// time at collection by registerDeviceTx's own site-mismatch guard. Three
// checks, because the window between them is minutes long and the consequence of
// missing it is one customer's door silently becoming another customer's door.
// Only a platform administrator can break that binding, through
// ReleaseTerminalSerial, and it is audited into the company that loses it.

// Announcement lifecycle states. The transitions are:
//
//	PENDING --adopt--> ADOPTED --approve--> APPROVED --collect--> COLLECTED
//
// with EXPIRED, REJECTED and SUPERSEDED as terminal states reachable from the
// live ones. Every one of them is also a CHECK constraint in the migration,
// because a state machine that only exists in Go is one a future code path can
// step outside of.
const (
	AnnouncementPending    = "PENDING"
	AnnouncementAdopted    = "ADOPTED"
	AnnouncementApproved   = "APPROVED"
	AnnouncementCollected  = "COLLECTED"
	AnnouncementRejected   = "REJECTED"
	AnnouncementExpired    = "EXPIRED"
	AnnouncementSuperseded = "SUPERSEDED"
)

// The provisioning sources recorded on devices.provisioned_via (023).
const (
	ProvisionedViaSiteKey      = "SITE_KEY"
	ProvisionedViaClaimCode    = "CLAIM_CODE"
	ProvisionedViaAnnouncement = "ANNOUNCEMENT"
)

// Adoption verdicts. What the console must SAY before an operator approves.
const (
	// VerdictNew is a serial with no device row anywhere.
	VerdictNew = "NEW"

	// VerdictReprovision is a serial that already has a live terminal IN THIS
	// COMPANY -- a factory reset, a replaced mainboard, a unit being brought
	// back after revocation. Allowed, because it is the legitimate recovery
	// path, but the console must warn: collection ROTATES the credential, and
	// the key that terminal is using right now stops working.
	VerdictReprovision = "RE_PROVISION"
)

// Windows.
const (
	// AnnouncementTTL bounds a pairing code somebody is reading off a panel. It
	// is short on purpose: the customer is standing at the unit with a phone.
	//
	// Adoption RESETS it rather than inheriting the remainder, so an operator who
	// types a code at minute fourteen still gets a full window to choose a site.
	// Extending is safe at that point and not before: the row now names an
	// authenticated operator who has taken responsibility for it.
	AnnouncementTTL = 15 * time.Minute

	// ApprovalTTL is how long an approved terminal has to come and collect. A
	// different clock from AnnouncementTTL and much longer, because what it has
	// to survive is different: a unit being screwed to a wall, a network that
	// comes back in the afternoon, an installer who approved before the terminal
	// was powered.
	ApprovalTTL = 24 * time.Hour

	// AnnouncePollSeconds is what the device is told to wait between polls. Sent
	// in the response so the cadence is the server's to change without a
	// firmware release.
	AnnouncePollSeconds = 5
)

// Announcement errors.
//
// THE REFUSALS ARE UNIFORM ON PURPOSE. ErrAnnouncementRefused covers an unknown
// pairing code, an expired one, one already adopted and one belonging to a
// rejected row -- because telling them apart would let somebody sitting in a
// console distinguish "no such code" from "that code is real but spent", which
// is the fact that makes guessing worth doing.
//
// The two OWNERSHIP errors are deliberately NOT uniform, and that is not an
// inconsistency. They are only ever returned to an authenticated operator who
// has just proved possession of a code displayed on a physical unit, and each
// one has a different remedy that only they can carry out.
var (
	ErrAnnouncementRefused = errors.New("that code was not recognised")

	// ErrAnnouncementNotFound is for a console route naming an announcement by
	// public id -- one that does not exist, or belongs to another company.
	ErrAnnouncementNotFound = errors.New("announcement not found")

	// ErrAnnouncementUnknownToken is the device-side uniform refusal.
	ErrAnnouncementUnknownToken = errors.New("that announce token was not recognised")

	// ErrTerminalOwnedElsewhere is THE anti-hijack refusal. The serial has a live
	// terminal in a different company, so adopting it here would silently move
	// somebody else's door.
	ErrTerminalOwnedElsewhere = errors.New(
		"that terminal is already registered to another AccessLink account")

	// ErrTerminalDisabledLocally is the same serial inside the caller's own
	// company, taken out of service by one of their administrators. Named
	// distinctly because the remedy is theirs and is one click: re-enable it.
	ErrTerminalDisabledLocally = errors.New(
		"that terminal is disabled; re-enable it before setting it up again")

	ErrAnnouncementSerialRequired = errors.New("serial_number is required")
)

// serialPattern is the firmware's own rule (device_identity.cpp,
// deviceSerialIsWellFormed) -- letters, digits, hyphen and underscore.
//
// Enforced HERE and not only at the CHECK constraint because this value arrives
// on an unauthenticated endpoint and is eventually written into `devices` as an
// identity. A constraint violation would surface as a 500 that a caller cannot
// distinguish from the database being down; a validated refusal is a 400 that
// says what was wrong.
var serialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// pairingCodeGroups formats the code the way somebody has to read it off a
// 16x2 panel and type it into a browser: two groups of four.
const (
	pairingCodeGroups    = 2
	pairingCodeGroupSize = 4
	pairingCodeSeparator = "-"

	// announceTokenBytes is 32 from crypto/rand -- a machine secret, never typed
	// by anybody, so there is no reason for it to be short.
	announceTokenBytes = 32
)

// generatePairingCode returns the plaintext code and its storage form.
//
// THE ALPHABET IS claimAlphabet, shared with claim codes rather than copied:
// Crockford's set minus I, L, O, U, 0 and 1, which are the characters misread
// when somebody transcribes them off a screen. The same reasoning applies to
// both, so the same constant should serve both -- two copies would be two things
// to keep in step, and the one that drifts is the one a customer cannot type.
func generatePairingCode() (code, hash, prefix string, err error) {
	total := pairingCodeGroups * pairingCodeGroupSize

	buf := make([]byte, total)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generating pairing code: %w", err)
	}

	var b strings.Builder
	for i := 0; i < total; i++ {
		if i > 0 && i%pairingCodeGroupSize == 0 {
			b.WriteString(pairingCodeSeparator)
		}
		b.WriteByte(claimAlphabet[int(buf[i])%len(claimAlphabet)])
	}

	code = b.String()
	hash = hashPairingCode(code)
	prefix = code[:pairingCodeGroupSize]
	return code, hash, prefix, nil
}

// hashPairingCode normalises and hashes.
//
// SHA-256 rather than bcrypt, on the same reasoning claim codes use: the secret
// is machine-generated so it is not subject to dictionary attack, and the lookup
// has to find a row BY the value. Upper-cased first, so an operator who types it
// in lower case is not refused for a reason nobody could guess.
func hashPairingCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizePairingCode(code)))
	return hex.EncodeToString(sum[:])
}

// NormalizePairingCode puts a typed code into canonical form.
//
// Exported because the console formats as somebody types, and the two have to
// agree on what "the same code" means -- including that a pasted code with a
// stray space is the same code.
func NormalizePairingCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func generateAnnounceToken() (token, hash, prefix string, err error) {
	buf := make([]byte, announceTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generating announce token: %w", err)
	}
	token = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	// A non-secret handle for the audit trail. Twelve characters of a 64-hex
	// token identifies which token without being one.
	prefix = token[:12]
	return token, hash, prefix, nil
}

func hashAnnounceToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// normalizeSerial validates what a device says its serial is.
func normalizeSerial(serial string) (string, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return "", ErrAnnouncementSerialRequired
	}
	if len(serial) > models.MaxDeviceSerialLength {
		return "", models.ErrDeviceSerialTooLong
	}
	if !serialPattern.MatchString(serial) {
		return "", models.ErrDeviceSerialTooLong
	}
	return serial, nil
}

// ---------------------------------------------------------------------------
// Announcing
// ---------------------------------------------------------------------------

// AnnounceRequest is what a terminal sends.
type AnnounceRequest struct {
	SerialNumber     string
	FirmwareVersion  string
	HardwareRevision string
	IPAddress        string

	// Capabilities is what the unit says it can do. Stored on the announcement
	// so the console can show it before an approval; the same nil-versus-empty
	// rule the heartbeat follows applies here, through the same
	// capabilityJSON.
	Capabilities []string

	// PresentedToken is the announce token from a PREVIOUS announcement, if this
	// unit still holds one. Optional, and the difference it makes is in
	// AnnouncedTerminal's comment.
	PresentedToken string
}

// AnnouncedTerminal is the result of announcing.
type AnnouncedTerminal struct {
	PublicID string
	State    string

	// PairingCode and AnnounceToken are populated ONLY when this call created a
	// new announcement. They exist here and nowhere else.
	//
	// A UNIT RE-ANNOUNCING WITH A VALID TOKEN GETS NEITHER, deliberately: its
	// code must not rotate underneath a customer who is part-way through typing
	// it. The device is expected to persist the code it was given -- it is
	// displaying it, so it has to anyway -- and a unit that has genuinely lost
	// its code re-announces WITHOUT its token, which supersedes the old row and
	// mints a fresh pair.
	PairingCode   string
	AnnounceToken string

	SerialNumber string
	ExpiresAt    time.Time

	// Existing reports that this call resolved to an announcement that was
	// already live rather than creating one.
	Existing bool
}

// Announce creates or refreshes a terminal's request to be set up.
//
// UNAUTHENTICATED. Everything it produces is inert until a human acts on it.
//
// The behaviour when a live announcement already exists for this serial is the
// whole of the interesting logic, and each branch is answering a real field
// situation:
//
//	PENDING + the caller's own token   -> the same row, touched. A terminal that
//	                                      rebooted, or that re-announces on a
//	                                      cycle, must not rotate the code a
//	                                      customer is reading off its panel.
//	PENDING + no or wrong token        -> superseded, fresh code. Possession of
//	                                      the hardware is the authority, and this
//	                                      is how a unit that lost its stored code
//	                                      gets a new one.
//	ADOPTED or APPROVED                -> LEFT ALONE. An operator has already
//	                                      typed this code, or already approved
//	                                      it; a reboot at the wrong moment must
//	                                      not destroy work in flight. The device
//	                                      is told the state and nothing else.
func Announce(req AnnounceRequest) (*AnnouncedTerminal, error) {
	serial, err := normalizeSerial(req.SerialNumber)
	if err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Time out anything for this serial that has run out before touching the
	// unique index it occupies. The background sweep does this fleet-wide on an
	// interval; a provisioning path must never WAIT for a background task, so it
	// does its own row first.
	if err := expireStaleForSerialTx(tx, serial); err != nil {
		return nil, err
	}

	var (
		liveID    int64
		livePub   string
		liveState string
		liveHash  string
		liveExp   time.Time
	)
	err = tx.QueryRow(`
		SELECT id, public_id, state, announce_token_hash, expires_at
		  FROM terminal_announcements
		 WHERE serial_number = $1
		   AND state IN ('PENDING', 'ADOPTED', 'APPROVED')
		 FOR UPDATE`, serial).
		Scan(&liveID, &livePub, &liveState, &liveHash, &liveExp)

	switch {
	case err == nil:
		sameCaller := req.PresentedToken != "" &&
			hashAnnounceToken(req.PresentedToken) == liveHash

		if sameCaller || liveState != AnnouncementPending {
			// Touched rather than replaced. announce_count and last_seen_* are
			// what let the console say "seen four seconds ago", which is how the
			// operator knows the unit in front of them is the one they are about
			// to approve.
			if _, err := tx.Exec(`
				UPDATE terminal_announcements
				   SET last_seen_at = CURRENT_TIMESTAMP,
				       last_seen_ip = COALESCE(NULLIF($2, ''), last_seen_ip),
				       announce_count = announce_count + 1,
				       firmware_version = COALESCE(NULLIF($3, ''), firmware_version),
				       hardware_revision = COALESCE(NULLIF($4, ''), hardware_revision),

				       -- MERGED, exactly as the heartbeat merges it: an announce
				       -- that does not name capabilities leaves what is stored
				       -- alone. A terminal re-announcing after a reboot must not
				       -- blank a list an operator is looking at.
				       capabilities = COALESCE($5::jsonb, capabilities)
				 WHERE id = $1`,
				liveID, req.IPAddress, req.FirmwareVersion, req.HardwareRevision,
				capabilityJSON(req.Capabilities)); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &AnnouncedTerminal{
				PublicID:     livePub,
				State:        liveState,
				SerialNumber: serial,
				ExpiresAt:    liveExp,
				Existing:     true,
			}, nil
		}

		// A PENDING row whose owner cannot prove it is theirs. Superseded, so the
		// partial unique index has room for the replacement.
		if _, err := tx.Exec(`
			UPDATE terminal_announcements
			   SET state = 'SUPERSEDED'
			 WHERE id = $1`, liveID); err != nil {
			return nil, err
		}

	case errors.Is(err, sql.ErrNoRows):
		// Nothing live for this serial. The normal first-boot case.

	default:
		return nil, err
	}

	code, codeHash, codePrefix, err := generatePairingCode()
	if err != nil {
		return nil, err
	}
	token, tokenHash, tokenPrefix, err := generateAnnounceToken()
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().UTC().Add(AnnouncementTTL)

	var publicID string
	err = tx.QueryRow(`
		INSERT INTO terminal_announcements
		    (serial_number, pairing_code_hash, pairing_code_prefix,
		     announce_token_hash, announce_token_prefix, state,
		     first_seen_ip, last_seen_ip, last_seen_at,
		     firmware_version, hardware_revision, capabilities, expires_at)
		VALUES ($1, $2, $3, $4, $5, 'PENDING',
		        NULLIF($6, ''), NULLIF($6, ''), CURRENT_TIMESTAMP,
		        NULLIF($7, ''), NULLIF($8, ''), $10::jsonb, $9)
		RETURNING public_id`,
		serial, codeHash, codePrefix, tokenHash, tokenPrefix,
		req.IPAddress, req.FirmwareVersion, req.HardwareRevision, expiresAt,
		capabilityJSON(req.Capabilities)).
		Scan(&publicID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &AnnouncedTerminal{
		PublicID:      publicID,
		State:         AnnouncementPending,
		PairingCode:   code,
		AnnounceToken: token,
		SerialNumber:  serial,
		ExpiresAt:     expiresAt,
	}, nil
}

// expireStaleForSerialTx times out this serial's own overdue rows.
//
// Both clocks, because PENDING/ADOPTED and APPROVED expire on different ones.
func expireStaleForSerialTx(tx *sql.Tx, serial string) error {
	_, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = 'EXPIRED'
		 WHERE serial_number = $1
		   AND (
		        (state IN ('PENDING', 'ADOPTED') AND expires_at <= CURRENT_TIMESTAMP)
		     OR (state = 'APPROVED' AND approval_expires_at <= CURRENT_TIMESTAMP)
		   )`, serial)
	return err
}

// ---------------------------------------------------------------------------
// The device's poll, and collection
// ---------------------------------------------------------------------------

// AnnouncementStatusResult is what a polling terminal is told.
//
// The device-facing state vocabulary is deliberately SMALLER than the stored
// one: a terminal has four things it can usefully do -- keep showing the code,
// say somebody is dealing with it, store a credential, or start over -- so
// REJECTED, EXPIRED, SUPERSEDED and COLLECTED all arrive as one REFUSED.
type AnnouncementStatusResult struct {
	// State is one of ANNOUNCED, ADOPTED, APPROVED, REFUSED.
	State string

	// APIKey is present exactly once, in the response that transitions APPROVED
	// to COLLECTED. It is generated inside that transaction and stored nowhere.
	APIKey string

	SerialNumber string
	CompanyName  string
	SiteName     string
	DeviceName   string

	ExpiresAt time.Time

	// CompanyID, DeviceID and ApprovedByEmail are for the audit record the
	// handler writes. Not serialised to the device.
	CompanyID       int64
	DeviceID        int64
	ApprovedByEmail string

	// BootstrapJobs is how much work was queued for the new terminal, reported
	// for the audit trail the same way claim redemption reports it.
	BootstrapJobs int
}

// Device-facing states.
const (
	AnnounceStateAnnounced = "ANNOUNCED"
	AnnounceStateAdopted   = "ADOPTED"
	AnnounceStateApproved  = "APPROVED"
	AnnounceStateRefused   = "REFUSED"
)

// AnnouncementStatus answers a polling terminal and, when the announcement has
// been approved, issues its credential.
//
// AUTHENTICATED BY THE ANNOUNCE TOKEN, which is the whole reason the token
// exists: without it this would be a second unauthenticated endpoint, and one
// that hands out credentials.
//
// COLLECTION IS ONE TRANSACTION AND HAPPENS ONCE. The credential is generated,
// the device row is created or rotated, and the announcement is marked COLLECTED
// together. A key issued without the row being marked would be re-issuable; a
// row marked without a key issued would strand a terminal that will never be
// offered another.
//
// A terminal that collects and then fails to persist what it was given does NOT
// get a second copy. It re-announces instead -- which works, because COLLECTED
// does not hold the live-serial index -- and an operator adopts it again. That is
// a deliberate trade: one-shot delivery is worth more than saving somebody a
// second approval.
func AnnouncementStatus(token, ip string) (*AnnouncementStatusResult, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrAnnouncementUnknownToken
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id           int64
		state        string
		serial       string
		expiresAt    time.Time
		approvalExp  sql.NullTime
		companyID    sql.NullInt64
		siteID       sql.NullInt64
		deviceName   sql.NullString
		approvedBy   sql.NullString
		nowExpired   bool
		approvalDead bool
	)
	err = tx.QueryRow(`
		SELECT id, state, serial_number, expires_at, approval_expires_at,
		       company_id, site_id, device_name, approved_by_email,
		       expires_at <= CURRENT_TIMESTAMP,
		       approval_expires_at IS NOT NULL
		           AND approval_expires_at <= CURRENT_TIMESTAMP
		  FROM terminal_announcements
		 WHERE announce_token_hash = $1
		 FOR UPDATE`, hashAnnounceToken(token)).
		Scan(&id, &state, &serial, &expiresAt, &approvalExp,
			&companyID, &siteID, &deviceName, &approvedBy, &nowExpired, &approvalDead)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAnnouncementUnknownToken
	}
	if err != nil {
		return nil, err
	}

	// The poll doubles as a liveness signal, which is what makes "last seen four
	// seconds ago" true on the console while an operator is looking at it.
	if _, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET last_seen_at = CURRENT_TIMESTAMP,
		       last_seen_ip = COALESCE(NULLIF($2, ''), last_seen_ip)
		 WHERE id = $1`, id, ip); err != nil {
		return nil, err
	}

	refused := func() (*AnnouncementStatusResult, error) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &AnnouncementStatusResult{State: AnnounceStateRefused,
			SerialNumber: serial}, nil
	}

	switch {
	case state == AnnouncementPending && !nowExpired:
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &AnnouncementStatusResult{
			State:        AnnounceStateAnnounced,
			SerialNumber: serial,
			ExpiresAt:    expiresAt,
		}, nil

	case state == AnnouncementAdopted && !nowExpired:
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &AnnouncementStatusResult{
			State:        AnnounceStateAdopted,
			SerialNumber: serial,
			ExpiresAt:    expiresAt,
		}, nil

	case state == AnnouncementApproved && !approvalDead:
		// Fall through to collection below.

	default:
		// COLLECTED, REJECTED, EXPIRED, SUPERSEDED, or a live state whose window
		// has closed. All the same instruction to a terminal: start again.
		return refused()
	}

	// ---------------------------------------------------------------------
	// Collection
	// ---------------------------------------------------------------------

	var (
		siteActive  bool
		siteName    string
		siteCompany int64
		companyName string
	)
	err = tx.QueryRow(`
		SELECT s.active, s.site_name, s.company_id, c.name
		  FROM sites s
		  JOIN companies c ON c.id = s.company_id
		 WHERE s.id = $1 AND s.deleted_at IS NULL`, siteID.Int64).
		Scan(&siteActive, &siteName, &siteCompany, &companyName)

	// The site went away, or was switched off, between approval and collection.
	// The approval is void: recorded as rejected with the reason, which both
	// tells the operator what happened and frees the serial for a fresh
	// announcement instead of leaving the terminal deadlocked against its own
	// stale APPROVED row.
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !siteActive) {
		return rejectAndRefuse(tx, id, serial, "the site was deactivated before the terminal collected its credential")
	}
	if err != nil {
		return nil, err
	}

	// Belt and braces on the tenancy boundary. The site was resolved inside the
	// adopting company at approval time, so these agree unless something has
	// rewritten one of them since.
	if !companyID.Valid || companyID.Int64 != siteCompany {
		return rejectAndRefuse(tx, id, serial, "the approved site no longer belongs to the adopting company")
	}

	device, key, jobs, err := registerDeviceTx(tx, siteID.Int64,
		models.DeviceRegistrationRequest{
			SerialNumber: serial,
			DeviceName:   deviceName.String,
			IPAddress:    ip,
		}, ProvisionedViaAnnouncement)

	switch {
	case errors.Is(err, models.ErrDeviceSiteMismatch):
		// THE LAST OF THE THREE OWNERSHIP CHECKS. Between approval and
		// collection the serial acquired a device row at a different site --
		// another company claimed it, or an operator registered it with a site
		// key. Refused rather than reassigned, and the transaction that had
		// already written a fresh key hash is rolled back with it.
		return rollbackThenReject(tx, id, serial,
			"the serial is registered to a different site")

	case errors.Is(err, models.ErrDeviceDisabled):
		return rollbackThenReject(tx, id, serial,
			"the terminal was disabled before it collected its credential")

	case err != nil:
		return nil, err
	}

	if _, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = 'COLLECTED',
		       device_id = $2,
		       collected_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, id, device.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &AnnouncementStatusResult{
		State:           AnnounceStateApproved,
		APIKey:          key,
		SerialNumber:    device.SerialNumber,
		CompanyName:     companyName,
		SiteName:        siteName,
		DeviceName:      device.DeviceName,
		CompanyID:       siteCompany,
		DeviceID:        device.ID,
		ApprovedByEmail: approvedBy.String,
		BootstrapJobs:   jobs,
	}, nil
}

// rejectAndRefuse records why an approval could not be honoured and answers the
// terminal with the one instruction it can act on.
func rejectAndRefuse(tx *sql.Tx, id int64, serial, reason string) (*AnnouncementStatusResult, error) {
	if _, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = 'REJECTED',
		       rejected_at = CURRENT_TIMESTAMP,
		       rejected_reason = $2
		 WHERE id = $1`, id, reason); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &AnnouncementStatusResult{State: AnnounceStateRefused, SerialNumber: serial}, nil
}

// rollbackThenReject handles the two failures registerDeviceTx reports AFTER
// having already written a credential into the device row.
//
// The rollback is not optional and is the whole reason this is separate: that
// function deliberately writes first and checks afterwards, so its caller must
// discard the transaction rather than commit a key into a terminal somebody
// disabled. The rejection is then recorded on its own connection, because the
// transaction that would have carried it no longer exists.
func rollbackThenReject(tx *sql.Tx, id int64, serial, reason string) (*AnnouncementStatusResult, error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return nil, err
	}
	if _, err := DB.Exec(`
		UPDATE terminal_announcements
		   SET state = 'REJECTED',
		       rejected_at = CURRENT_TIMESTAMP,
		       rejected_reason = $2
		 WHERE id = $1`, id, reason); err != nil {
		return nil, err
	}
	return &AnnouncementStatusResult{State: AnnounceStateRefused, SerialNumber: serial}, nil
}

// ---------------------------------------------------------------------------
// The console: adopt, list, approve, reject
// ---------------------------------------------------------------------------

// Announcement is the console's projection of a pending terminal.
//
// NO SECRET IS IN THIS STRUCT and none may ever be added. The pairing code is
// not readable back, and the announce token belongs to the device.
type Announcement struct {
	PublicID     string
	SerialNumber string
	State        string

	// Verdict is recomputed on every read rather than stored, because what it
	// describes can change underneath a row: a terminal can be disabled, or
	// released, between an operator opening the screen and pressing approve.
	Verdict string

	// ExistingTerminal is populated for RE_PROVISION, so the console can name
	// what is about to have its credential rotated.
	ExistingTerminal *ExistingTerminal

	FirmwareVersion  string
	HardwareRevision string

	// Capabilities is what the unit reported when it announced.
	//
	// NIL MEANS IT NEVER SAID, which a console must render as unknown rather
	// than as none -- a build that predates capability reporting and a unit
	// whose announce simply omitted the field are both nil here, and neither is
	// a claim that the terminal cannot do anything.
	Capabilities []string

	FirstSeenIP string
	LastSeenIP  string
	LastSeenAt  *time.Time
	AnnouncedAt time.Time

	AdoptedByEmail string
	AdoptedAt      *time.Time

	SitePublicID string
	SiteName     string
	DeviceName   string

	ApprovedByEmail string
	ApprovedAt      *time.Time
	ExpiresAt       time.Time
}

// ExistingTerminal describes the live terminal a RE_PROVISION would rotate.
type ExistingTerminal struct {
	SerialNumber string
	DeviceName   string
	SiteName     string
	Status       string
}

// terminalOwnership is the answer to "does anybody already have this serial".
type terminalOwnership struct {
	found     bool
	companyID int64
	name      string
	siteName  string
	status    string
	active    bool
}

// lookupTerminalOwnership finds a live device row for a serial ACROSS ALL
// COMPANIES.
//
// The one query in this file that is deliberately not tenant-scoped, because the
// question it answers is a cross-tenant one: "is this serial already somebody
// else's". What it returns is never disclosed -- the caller turns a foreign
// owner into a fixed refusal that names no company, no site and no operator.
func lookupTerminalOwnership(q rowQuerier, serial string) (terminalOwnership, error) {
	var own terminalOwnership
	err := q.QueryRow(`
		SELECT s.company_id, COALESCE(d.device_name, ''), s.site_name,
		       d.status, d.active
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, serial).
		Scan(&own.companyID, &own.name, &own.siteName, &own.status, &own.active)
	if errors.Is(err, sql.ErrNoRows) {
		return own, nil
	}
	if err != nil {
		return own, err
	}
	own.found = true
	return own, nil
}

// verdictFor applies the ownership rule for one company.
func verdictFor(own terminalOwnership, companyID int64) (string, error) {
	switch {
	case !own.found:
		return VerdictNew, nil
	case own.companyID != companyID:
		return "", ErrTerminalOwnedElsewhere
	case own.status == models.DeviceDisabled || !own.active:
		return "", ErrTerminalDisabledLocally
	default:
		return VerdictReprovision, nil
	}
}

// AdoptAnnouncement binds a pending announcement to the caller's company.
//
// THE ONLY WAY AN ANNOUNCEMENT BECOMES VISIBLE TO ANYBODY. Until this succeeds
// the row has no company and no route can read it, which is what keeps one
// customer's unclaimed hardware invisible to every other -- including to
// somebody enumerating.
//
// Adoption does NOT create a device, issue a credential or choose a site. It
// records that a company has taken responsibility for a unit whose code one of
// their administrators has just read off its panel.
func AdoptAnnouncement(companyID, actorID int64, actorEmail, pairingCode string) (*Announcement, error) {
	code := NormalizePairingCode(pairingCode)
	if code == "" {
		return nil, ErrAnnouncementRefused
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id     int64
		pub    string
		serial string
	)
	err = tx.QueryRow(`
		SELECT id, public_id, serial_number
		  FROM terminal_announcements
		 WHERE pairing_code_hash = $1
		   AND state = 'PENDING'
		   AND expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`, hashPairingCode(code)).
		Scan(&id, &pub, &serial)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown, expired, already adopted, rejected -- one answer. See the note
		// on ErrAnnouncementRefused.
		return nil, ErrAnnouncementRefused
	}
	if err != nil {
		return nil, err
	}

	own, err := lookupTerminalOwnership(tx, serial)
	if err != nil {
		return nil, err
	}
	// The verdict itself is recomputed by loadAnnouncement below; what is needed
	// HERE is only whether it refuses. NOTHING IS WRITTEN on a refusal: a hijack
	// attempt must not leave the announcement adopted, and a disabled terminal
	// must not be half-claimed while an operator goes to re-enable it.
	if _, err := verdictFor(own, companyID); err != nil {
		return nil, err
	}

	// Adoption resets the window. See AnnouncementTTL.
	expiresAt := time.Now().UTC().Add(AnnouncementTTL)

	if _, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = 'ADOPTED',
		       company_id = $2,
		       adopted_by = NULLIF($3, 0)::bigint,
		       adopted_by_email = NULLIF($4, ''),
		       adopted_at = CURRENT_TIMESTAMP,
		       expires_at = $5
		 WHERE id = $1`, id, companyID, actorID, actorEmail, expiresAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return loadAnnouncement(companyID, pub)
}

// ListAnnouncements returns this company's terminals waiting to be set up.
//
// ADOPTED and APPROVED only. A COLLECTED announcement has become a terminal and
// belongs on the fleet list instead, and a rejected one is history.
//
// An adopted row past its window is returned WITH its expired state rather than
// filtered out: an operator who walked away mid-setup is owed the explanation,
// and a row that silently vanishes reads as the platform having lost it.
func ListAnnouncements(companyID int64) ([]Announcement, error) {
	rows, err := DB.Query(`
		SELECT a.public_id, a.serial_number, a.state,
		       COALESCE(a.firmware_version, ''), COALESCE(a.hardware_revision, ''),
		       a.capabilities,
		       COALESCE(a.first_seen_ip, ''), COALESCE(a.last_seen_ip, ''),
		       a.last_seen_at, a.created_at,
		       COALESCE(a.adopted_by_email, ''), a.adopted_at,
		       COALESCE(s.public_id::text, ''), COALESCE(s.site_name, ''),
		       COALESCE(a.device_name, ''),
		       COALESCE(a.approved_by_email, ''), a.approved_at,
		       a.expires_at,
		       CASE
		           WHEN a.state = 'APPROVED'
		                AND a.approval_expires_at <= CURRENT_TIMESTAMP THEN TRUE
		           WHEN a.state = 'ADOPTED'
		                AND a.expires_at <= CURRENT_TIMESTAMP THEN TRUE
		           ELSE FALSE
		       END AS timed_out
		  FROM terminal_announcements a
		  LEFT JOIN sites s ON s.id = a.site_id
		 WHERE a.company_id = $1
		   AND a.state IN ('ADOPTED', 'APPROVED')
		 ORDER BY a.created_at DESC
		 LIMIT 200`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Announcement{}
	for rows.Next() {
		item, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The verdict is per-row and needs its own query, so it is resolved after the
	// cursor is closed rather than inside the loop -- one connection, one
	// statement at a time.
	for i := range out {
		if err := attachVerdict(&out[i], companyID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetAnnouncement reads one, scoped to the company that adopted it.
func GetAnnouncement(companyID int64, publicID string) (*Announcement, error) {
	if !looksLikeUUID(publicID) {
		return nil, ErrAnnouncementNotFound
	}
	return loadAnnouncement(companyID, publicID)
}

func loadAnnouncement(companyID int64, publicID string) (*Announcement, error) {
	row := DB.QueryRow(`
		SELECT a.public_id, a.serial_number, a.state,
		       COALESCE(a.firmware_version, ''), COALESCE(a.hardware_revision, ''),
		       a.capabilities,
		       COALESCE(a.first_seen_ip, ''), COALESCE(a.last_seen_ip, ''),
		       a.last_seen_at, a.created_at,
		       COALESCE(a.adopted_by_email, ''), a.adopted_at,
		       COALESCE(s.public_id::text, ''), COALESCE(s.site_name, ''),
		       COALESCE(a.device_name, ''),
		       COALESCE(a.approved_by_email, ''), a.approved_at,
		       a.expires_at,
		       CASE
		           WHEN a.state = 'APPROVED'
		                AND a.approval_expires_at <= CURRENT_TIMESTAMP THEN TRUE
		           WHEN a.state IN ('PENDING', 'ADOPTED')
		                AND a.expires_at <= CURRENT_TIMESTAMP THEN TRUE
		           ELSE FALSE
		       END AS timed_out
		  FROM terminal_announcements a
		  LEFT JOIN sites s ON s.id = a.site_id
		 WHERE a.public_id = $1 AND a.company_id = $2`, publicID, companyID)

	item, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAnnouncementNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := attachVerdict(&item, companyID); err != nil {
		return nil, err
	}
	return &item, nil
}

// scannable is *sql.Row or *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

func scanAnnouncement(row scannable) (Announcement, error) {
	var (
		item       Announcement
		lastSeen   sql.NullTime
		adoptedAt  sql.NullTime
		approvedAt sql.NullTime
		timedOut   bool
	)
	var capabilities []byte
	err := row.Scan(&item.PublicID, &item.SerialNumber, &item.State,
		&item.FirmwareVersion, &item.HardwareRevision, &capabilities,
		&item.FirstSeenIP, &item.LastSeenIP, &lastSeen, &item.AnnouncedAt,
		&item.AdoptedByEmail, &adoptedAt,
		&item.SitePublicID, &item.SiteName, &item.DeviceName,
		&item.ApprovedByEmail, &approvedAt, &item.ExpiresAt, &timedOut)
	if err != nil {
		return item, err
	}

	// A list that will not decode reads as NEVER REPORTED rather than failing
	// the read. This column is filled by an unauthenticated device; one unit
	// that wrote nonsense into it must not be able to take the console's
	// waiting-to-be-set-up list down.
	if parsed, parseErr := scanCapabilities(capabilities); parseErr == nil {
		item.Capabilities = parsed
	}

	if lastSeen.Valid {
		item.LastSeenAt = &lastSeen.Time
	}
	if adoptedAt.Valid {
		item.AdoptedAt = &adoptedAt.Time
	}
	if approvedAt.Valid {
		item.ApprovedAt = &approvedAt.Time
	}

	// PRESENTED AS EXPIRED THE MOMENT IT IS, rather than when a background sweep
	// happens to notice. A console that offered an Approve button on a row the
	// API would refuse is a console that lies about what it can do.
	if timedOut {
		item.State = AnnouncementExpired
	}
	return item, nil
}

// attachVerdict recomputes the ownership answer for a projection.
func attachVerdict(item *Announcement, companyID int64) error {
	own, err := lookupTerminalOwnership(DB, item.SerialNumber)
	if err != nil {
		return err
	}

	verdict, verdictErr := verdictFor(own, companyID)
	switch {
	case errors.Is(verdictErr, ErrTerminalOwnedElsewhere):
		item.Verdict = "REFUSED_OTHER_COMPANY"
	case errors.Is(verdictErr, ErrTerminalDisabledLocally):
		item.Verdict = "REFUSED_DISABLED"
		item.ExistingTerminal = &ExistingTerminal{
			SerialNumber: item.SerialNumber,
			DeviceName:   own.name,
			SiteName:     own.siteName,
			Status:       own.status,
		}
	case verdictErr != nil:
		return verdictErr
	default:
		item.Verdict = verdict
		if verdict == VerdictReprovision {
			item.ExistingTerminal = &ExistingTerminal{
				SerialNumber: item.SerialNumber,
				DeviceName:   own.name,
				SiteName:     own.siteName,
				Status:       own.status,
			}
		}
	}
	return nil
}

// ApproveAnnouncement authorises a terminal into one site.
//
// AUTHORISES, AND MINTS NOTHING. The credential is produced when the terminal
// comes to collect it -- see AnnouncementStatus. What this writes is the
// decision: which company, which site, what the unit is called.
//
// The ownership rule is applied AGAIN here rather than trusted from adoption.
// Minutes pass between the two, and in those minutes the serial can acquire a
// device row somewhere else, or the operator's own colleague can disable it.
func ApproveAnnouncement(companyID, actorID int64, actorEmail, publicID,
	sitePublicID, deviceName string) (*Announcement, error) {

	if !looksLikeUUID(publicID) {
		return nil, ErrAnnouncementNotFound
	}
	if !looksLikeUUID(sitePublicID) {
		return nil, models.ErrSiteNotFound
	}

	deviceName = strings.TrimSpace(deviceName)
	if len(deviceName) > 100 {
		deviceName = deviceName[:100]
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id     int64
		serial string
	)
	err = tx.QueryRow(`
		SELECT id, serial_number
		  FROM terminal_announcements
		 WHERE public_id = $1
		   AND company_id = $2
		   AND state = 'ADOPTED'
		   AND expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`, publicID, companyID).Scan(&id, &serial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAnnouncementNotFound
	}
	if err != nil {
		return nil, err
	}

	var siteID int64
	err = tx.QueryRow(`
		SELECT id FROM sites
		 WHERE public_id = $1 AND company_id = $2
		   AND deleted_at IS NULL AND active`, sitePublicID, companyID).Scan(&siteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}

	own, err := lookupTerminalOwnership(tx, serial)
	if err != nil {
		return nil, err
	}
	if _, err := verdictFor(own, companyID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = 'APPROVED',
		       site_id = $2,
		       device_name = NULLIF($3, ''),
		       approved_by = NULLIF($4, 0)::bigint,
		       approved_by_email = NULLIF($5, ''),
		       approved_at = CURRENT_TIMESTAMP,
		       approval_expires_at = $6
		 WHERE id = $1`,
		id, siteID, deviceName, actorID, actorEmail,
		time.Now().UTC().Add(ApprovalTTL)); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return loadAnnouncement(companyID, publicID)
}

// RejectAnnouncement refuses a terminal.
//
// Reachable from ADOPTED and from APPROVED, and the second is the one that
// matters operationally: it is how an operator undoes an approval, and how a
// serial is freed after a unit was approved and then lost its announce token to
// a factory reset. Rejecting releases the live-serial slot, so the terminal can
// announce again and be set up properly.
func RejectAnnouncement(companyID int64, actorEmail, publicID, reason string) (*Announcement, error) {
	if !looksLikeUUID(publicID) {
		return nil, ErrAnnouncementNotFound
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}

	result, err := DB.Exec(`
		UPDATE terminal_announcements
		   SET state = 'REJECTED',
		       rejected_at = CURRENT_TIMESTAMP,
		       rejected_by_email = NULLIF($3, ''),
		       rejected_reason = NULLIF($4, '')
		 WHERE public_id = $1
		   AND company_id = $2
		   AND state IN ('ADOPTED', 'APPROVED')`,
		publicID, companyID, actorEmail, reason)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrAnnouncementNotFound
	}

	return loadAnnouncement(companyID, publicID)
}

// ---------------------------------------------------------------------------
// Platform: releasing a serial from a company
// ---------------------------------------------------------------------------

// ReleasedTerminal reports what a release did, for the audit record.
type ReleasedTerminal struct {
	SerialNumber         string
	CompanyID            int64
	CompanyName          string
	SiteName             string
	DeviceName           string
	PendingJobsCancelled int64
	AnnouncementsVoided  int64
}

// ReleaseTerminalSerial detaches a serial from the company that holds it.
//
// THE ONLY OPERATION IN THE PLATFORM THAT MOVES HARDWARE BETWEEN TENANTS, and it
// is deliberately not a move: it releases, and the new owner then adopts through
// the ordinary flow. There is no endpoint that takes a serial from company A and
// gives it to company B in one step, because the two halves need two different
// people to agree -- and a single call would be a single credential able to
// reassign any door on the platform.
//
// PLATFORM ADMINISTRATOR ONLY. A tenant operator cannot reach it in either
// direction: the losing company cannot be made to give a unit up by the company
// that wants it, and the gaining company cannot help itself to one.
//
// What it does: revokes the credential, soft-deletes the device row, cancels its
// queued work, and voids any announcement in flight for that serial. After it
// the serial has no live device row, so an announcement for it verdicts as NEW
// and any company may adopt it.
func ReleaseTerminalSerial(serial, reason string) (*ReleasedTerminal, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil, models.ErrDeviceNotFound
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		deviceID int64
		out      ReleasedTerminal
	)
	err = tx.QueryRow(`
		SELECT d.id, d.serial_number, COALESCE(d.device_name, ''),
		       s.company_id, c.name, s.site_name
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN companies c ON c.id = s.company_id
		 WHERE d.serial_number = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		 FOR UPDATE OF d`, serial).
		Scan(&deviceID, &out.SerialNumber, &out.DeviceName,
			&out.CompanyID, &out.CompanyName, &out.SiteName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	// The credential is cleared as well as the row being deleted, on the same
	// reasoning RetireTerminal follows: a soft-deleted row is invisible to every
	// console query while its key would go on authenticating.
	if _, err := tx.Exec(`
		UPDATE devices
		   SET deleted_at = CURRENT_TIMESTAMP,
		       api_key_hash = NULL,
		       api_key_prefix = NULL,
		       credential_revoked_at = CURRENT_TIMESTAMP,
		       credential_revoked_reason = COALESCE(NULLIF($2, ''),
		           'released from this account by a platform administrator'),
		       status = 'DISABLED',
		       active = FALSE,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, deviceID, reason); err != nil {
		return nil, err
	}

	cancelled, err := cancelQueuedWork(tx, deviceID, "terminal released from this account")
	if err != nil {
		return nil, err
	}
	out.PendingJobsCancelled = cancelled

	// Anything in flight for this serial is void. A PENDING row has no company
	// and expires; an adopted or approved one belongs to the company losing the
	// hardware and is rejected with the reason, so their operator sees why it
	// stopped rather than watching it time out.
	voided, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = CASE WHEN company_id IS NULL THEN 'EXPIRED' ELSE 'REJECTED' END,
		       rejected_at = CASE WHEN company_id IS NULL
		                          THEN NULL ELSE CURRENT_TIMESTAMP END,
		       rejected_reason = CASE WHEN company_id IS NULL THEN NULL
		           ELSE 'the terminal was released from this account by a platform administrator'
		       END
		 WHERE serial_number = $1
		   AND state IN ('PENDING', 'ADOPTED', 'APPROVED')`, serial)
	if err != nil {
		return nil, err
	}
	if n, err := voided.RowsAffected(); err == nil {
		out.AnnouncementsVoided = n
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Maintenance
// ---------------------------------------------------------------------------

// ExpireAnnouncements times out everything past its window.
//
// The live paths do not depend on this having run -- every lookup carries its
// own expiry predicate, and Announce expires its own serial's rows before
// touching the unique index. What the sweep is for is the SLOT: an expired row
// still occupies the one-live-announcement-per-serial index until something
// moves it, and the console's pending list should not fill with rows that can no
// longer do anything.
func ExpireAnnouncements() (int64, error) {
	result, err := DB.Exec(`
		UPDATE terminal_announcements
		   SET state = 'EXPIRED'
		 WHERE (state IN ('PENDING', 'ADOPTED') AND expires_at <= CURRENT_TIMESTAMP)
		    OR (state = 'APPROVED' AND approval_expires_at <= CURRENT_TIMESTAMP)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PurgeAnnouncements deletes finished rows past a retention window.
//
// Kept for a window rather than deleted when they finish, for the reason
// PurgeExpiredClaimCodes states about codes: "did a unit ever try to join this
// account, and what happened to it" is a support and audit question, and the
// answer disappearing the moment the row stops being useful makes it
// unanswerable.
func PurgeAnnouncements(retentionDays int) (int64, error) {
	if retentionDays < 0 {
		return 0, nil
	}
	result, err := DB.Exec(`
		DELETE FROM terminal_announcements
		 WHERE state IN ('COLLECTED', 'REJECTED', 'EXPIRED', 'SUPERSEDED')
		   AND created_at < CURRENT_TIMESTAMP - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
