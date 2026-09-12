package database

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// Terminal release (migrations/032_terminal_release.sql).
//
// How a terminal leaves the company that holds it so that another company can
// adopt it, WITHOUT there being any state in which the new owner has the
// hardware while the old owner's members and templates are still usable on it.
//
// ---------------------------------------------------------------------------
// TWO STEPS, AND WHY THE ROW STAYS LIVE BETWEEN THEM
// ---------------------------------------------------------------------------
//
//	live ──order──► ORDERED ──finalize──► RELEASED (soft-deleted, serial free)
//	                   │
//	                   └──cancel──► live again
//
// ORDER mints a Release Order: serial, a fresh release_id, the time, and an
// HMAC keyed with the terminal's own credential hash. The row stays live and
// the credential keeps authenticating, because the terminal has to be able to
// fetch the order on its heartbeat, flush its door events to THIS company,
// and confirm -- all with the key it still holds. What changes is that no
// roster work is queued for it any more (deviceIsSyncable) and any queued work
// is cancelled.
//
// FINALIZE is what Retire and the platform release always did -- soft-delete,
// clear the hash, cancel work, void announcements -- plus the release record.
// It is reached by four callers, all through finalizeReleaseTx and all
// idempotent on release_id:
//
//	TERMINAL   the terminal confirmed with a receipt, either on the
//	           authenticated confirm route or carried on its next announce.
//	OPERATOR   an administrator of the holding company forced it, with a typed
//	           attestation, because the unit is offline or on old firmware.
//	PLATFORM   the platform administrator's route, which orders (if nothing
//	           is ordered yet) and forces in one call.
//
// ---------------------------------------------------------------------------
// THE MAC KEY IS THE CREDENTIAL HASH, AND THAT IS THE WHOLE TRICK
// ---------------------------------------------------------------------------
//
// The platform never holds a device key, only sha256(key). The terminal holds
// the key and can compute the same hash. So an HMAC keyed with the raw hash
// bytes is something both sides can produce and nothing else can: forging an
// order needs the platform's row, forging a receipt needs the terminal's key.
// A terminal later re-keyed by its next owner computes a different MAC key and
// ignores a stale order, so a leaked or replayed order cannot reach the new
// owner's door. Neither value reveals the hash.
//
// The MAC is kept on the row after the hash is cleared at finalization,
// because a terminal that was offline when an operator forced the release
// fetches the order by serial on its first 401 and must still verify it.

const (
	ReleaseStateOrdered  = "ORDERED"
	ReleaseStateReleased = "RELEASED"

	ReleaseConfirmedByTerminal = "TERMINAL"
	ReleaseConfirmedByOperator = "OPERATOR"
	ReleaseConfirmedByPlatform = "PLATFORM"

	// The domain-separation prefixes. Spelled identically in the firmware
	// (release_order.h); a fixture on each side asserts the same vectors.
	releaseOrderContext   = "accesslink-release-v1"
	releaseReceiptContext = "accesslink-receipt-v1"

	// CapabilityTerminalRelease is the 025 token a terminal reports when its
	// firmware acts on release orders and presents receipts. The console keys
	// the automated workflow on it and offers only the physical path without.
	CapabilityTerminalRelease = "terminal_release"

	// ReleaseReportMaxBytes bounds what a terminal may attach to its
	// confirmation. Counts of what it erased are a couple of hundred bytes.
	ReleaseReportMaxBytes = 2048
)

var (
	// ErrReleaseNotOrdered: cancel or force on a terminal with nothing ordered.
	ErrReleaseNotOrdered = errors.New("no release is in progress for that terminal")

	// ErrReleaseInProgress: adoption of a serial whose own company has a
	// release outstanding. Distinct from the anti-hijack refusal because the
	// remedy is the caller's own: cancel the release, or wait for it.
	ErrReleaseInProgress = errors.New(
		"a release of that terminal is in progress; cancel it, or wait for it to complete")

	// ErrReleaseReceiptMismatch: a confirm whose receipt does not verify
	// against the order this row carries.
	ErrReleaseReceiptMismatch = errors.New("that release receipt does not verify")
)

// ReleaseOrder is the object a terminal verifies and acts on.
type ReleaseOrder struct {
	SerialNumber string
	ReleaseID    string
	OrderedAt    time.Time
	MAC          []byte
}

// MACHex renders the MAC the way it crosses the wire.
func (o ReleaseOrder) MACHex() string { return hex.EncodeToString(o.MAC) }

// releaseOrderMessage is the byte string the order MAC is computed over.
//
// ordered_at travels as epoch SECONDS, and the stored timestamp is truncated
// to the second at order time so the value in the row and the value in the
// message can never disagree by a microsecond.
func releaseOrderMessage(serial, releaseID string, orderedAt time.Time) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s|%d", releaseOrderContext, serial,
		releaseID, orderedAt.Unix()))
}

func releaseReceiptMessage(serial, releaseID string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s", releaseReceiptContext, serial, releaseID))
}

// releaseMACKey turns the stored hex credential hash into the raw MAC key.
func releaseMACKey(apiKeyHashHex string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(apiKeyHashHex))
	if err != nil || len(key) != sha256.Size {
		return nil, errors.New("credential hash is not a 32-byte hex digest")
	}
	return key, nil
}

// ComputeReleaseOrderMAC is exported so the test fixture can assert the exact
// bytes the firmware's copy produces.
func ComputeReleaseOrderMAC(apiKeyHashHex, serial, releaseID string, orderedAt time.Time) ([]byte, error) {
	key, err := releaseMACKey(apiKeyHashHex)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(releaseOrderMessage(serial, releaseID, orderedAt))
	return mac.Sum(nil), nil
}

// ComputeReleaseReceipt is what a terminal produces after it has wiped.
func ComputeReleaseReceipt(apiKeyHashHex, serial, releaseID string) ([]byte, error) {
	key, err := releaseMACKey(apiKeyHashHex)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(releaseReceiptMessage(serial, releaseID))
	return mac.Sum(nil), nil
}

// newReleaseID mints a version-4 UUID.
//
// crypto/rand rather than the database's gen_random_uuid(), because the MAC is
// computed in Go BEFORE the row is written and the id is part of the message.
func newReleaseID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating release id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ---------------------------------------------------------------------------
// What the console reads
// ---------------------------------------------------------------------------

// TerminalRelease is the release facts for one live terminal.
type TerminalRelease struct {
	SerialNumber   string
	State          string // "" | ORDERED
	ReleaseID      string
	OrderedAt      *time.Time
	OrderedByEmail string
	Reason         string

	// OrderVerifiable is false when the row had no credential hash to key the
	// order with -- a previously revoked terminal. Such a unit can only be
	// wiped physically, and the console has to say so.
	OrderVerifiable bool

	// TerminalCapable is whether the terminal has reported terminal_release.
	TerminalCapable bool

	LastSeenAt *time.Time
}

// GetTerminalRelease reads the release state of a live terminal in the company.
func GetTerminalRelease(companyID int64, serial string) (*TerminalRelease, error) {
	return loadTerminalRelease(DB, companyID, serial)
}

func loadTerminalRelease(q rowQuerier, companyID int64, serial string) (*TerminalRelease, error) {
	var (
		out       TerminalRelease
		state     sql.NullString
		releaseID sql.NullString
		orderedAt sql.NullTime
		email     sql.NullString
		reason    sql.NullString
		macBytes  []byte
		caps      []byte
		lastSeen  sql.NullTime
	)
	err := q.QueryRow(`
		SELECT d.serial_number, d.release_state, d.release_id::text,
		       d.release_ordered_at, d.release_ordered_by_email, d.release_reason,
		       d.release_order_mac, d.capabilities, d.last_seen_at
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $2
		   AND s.company_id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, companyID, serial).
		Scan(&out.SerialNumber, &state, &releaseID, &orderedAt, &email, &reason,
			&macBytes, &caps, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	out.State = state.String
	out.ReleaseID = releaseID.String
	if orderedAt.Valid {
		t := orderedAt.Time
		out.OrderedAt = &t
	}
	out.OrderedByEmail = email.String
	out.Reason = reason.String
	out.OrderVerifiable = len(macBytes) == sha256.Size
	out.TerminalCapable = capabilitiesInclude(caps, CapabilityTerminalRelease)
	if lastSeen.Valid {
		t := lastSeen.Time
		out.LastSeenAt = &t
	}
	return &out, nil
}

// capabilitiesInclude answers whether a stored capability list names a token.
// A NULL or malformed list has never said so.
func capabilitiesInclude(stored []byte, token string) bool {
	parsed, err := scanCapabilities(stored)
	if err != nil {
		return false
	}
	for _, c := range parsed {
		if c == token {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// OrderTerminalRelease puts a live terminal into ORDERED.
//
// IDEMPOTENT. A second order while one is outstanding returns the existing
// one and changes nothing -- a browser that retried a request whose response
// it never saw must not mint a second order and invalidate the first.
//
// Returns the release facts and whether this call created the order.
func OrderTerminalRelease(companyID int64, serial, reason string,
	actorID int64, actorEmail string) (*TerminalRelease, bool, error) {

	serial = strings.TrimSpace(serial)
	if len(reason) > 200 {
		reason = reason[:200]
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var (
		deviceID int64
		state    sql.NullString
		hash     sql.NullString
	)
	err = tx.QueryRow(`
		SELECT d.id, d.release_state, d.api_key_hash
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $2
		   AND s.company_id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		 FOR UPDATE OF d`, companyID, serial).Scan(&deviceID, &state, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, false, err
	}

	if state.String == ReleaseStateOrdered {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		existing, err := loadTerminalRelease(DB, companyID, serial)
		return existing, false, err
	}

	if _, err := orderReleaseTx(tx, deviceID, serial, hash.String, reason,
		actorID, actorEmail); err != nil {
		return nil, false, err
	}

	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	out, err := loadTerminalRelease(DB, companyID, serial)
	return out, true, err
}

// orderReleaseTx writes the order onto a row the caller has locked.
func orderReleaseTx(tx *sql.Tx, deviceID int64, serial, apiKeyHash, reason string,
	actorID int64, actorEmail string) (ReleaseOrder, error) {

	releaseID, err := newReleaseID()
	if err != nil {
		return ReleaseOrder{}, err
	}
	orderedAt := time.Now().UTC().Truncate(time.Second)

	// No hash means no MAC -- a revoked credential cannot key one. The order is
	// still recorded so the state machine is the same shape; the terminal can
	// only be released physically or by force, and the console reads
	// OrderVerifiable to say so.
	var mac []byte
	if apiKeyHash != "" {
		mac, err = ComputeReleaseOrderMAC(apiKeyHash, serial, releaseID, orderedAt)
		if err != nil {
			return ReleaseOrder{}, err
		}
	}

	if _, err := tx.Exec(`
		UPDATE devices
		   SET release_state = 'ORDERED',
		       release_id = $2::uuid,
		       release_ordered_at = $3,
		       release_ordered_by = NULLIF($4, 0)::bigint,
		       release_ordered_by_email = NULLIF($5, ''),
		       release_reason = NULLIF($6, ''),
		       release_order_mac = $7,
		       release_confirmed_at = NULL,
		       release_confirmed_by = NULL,
		       release_report = NULL,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		deviceID, releaseID, orderedAt, actorID, actorEmail, reason, mac); err != nil {
		return ReleaseOrder{}, err
	}

	// Queued roster work is void: the terminal is about to erase everything it
	// would have applied it to. Placements are marked for removal rather than
	// removed, on the same reasoning MoveTerminal gives -- the templates are
	// physically still in the sensor until the terminal says otherwise.
	if _, err := cancelQueuedWork(tx, deviceID, "terminal release ordered"); err != nil {
		return ReleaseOrder{}, err
	}
	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'REMOVING', last_error = NULL
		 WHERE device_id = $1 AND state = 'PLACED'`, deviceID); err != nil {
		return ReleaseOrder{}, fmt.Errorf("marking placements for removal at release: %w", err)
	}

	return ReleaseOrder{SerialNumber: serial, ReleaseID: releaseID,
		OrderedAt: orderedAt, MAC: mac}, nil
}

// CancelTerminalRelease withdraws an outstanding order.
//
// ORDERED only. The row goes back to ordinary service and a snapshot is queued
// so a terminal that had its backlog cancelled at order time converges again.
// A terminal that had ALREADY executed the order cannot be un-wiped: it will
// find its receipt refused, announce afresh, and need setting up again -- which
// the console says before the operator confirms.
func CancelTerminalRelease(companyID int64, serial string) (*TerminalRelease, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var deviceID int64
	err = tx.QueryRow(`
		SELECT d.id
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $2
		   AND s.company_id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND d.release_state = 'ORDERED'
		 FOR UPDATE OF d`, companyID, serial).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		// Not found, or found and not ordered: told apart for the operator.
		if _, lookupErr := loadTerminalRelease(tx, companyID, serial); lookupErr != nil {
			return nil, lookupErr
		}
		return nil, ErrReleaseNotOrdered
	}
	if err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`
		UPDATE devices
		   SET release_state = NULL,
		       release_id = NULL,
		       release_ordered_at = NULL,
		       release_ordered_by = NULL,
		       release_ordered_by_email = NULL,
		       release_reason = NULL,
		       release_order_mac = NULL,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, deviceID); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'PLACED', last_error = NULL
		 WHERE device_id = $1 AND state = 'REMOVING'`, deviceID); err != nil {
		return nil, fmt.Errorf("restoring placements after cancelled release: %w", err)
	}

	// Converge. The snapshot is exactly the instrument: a set difference the
	// terminal applies whether or not it started wiping. A capacity refusal is
	// recorded rather than failing the cancel -- the release is withdrawn
	// either way, and an over-capacity terminal is a fact the console already
	// knows how to show.
	if _, err := compactDeviceBacklogTx(tx, deviceID, "release cancelled"); err != nil {
		var overflow *RosterCapacityError
		if !errors.As(err, &overflow) {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return loadTerminalRelease(DB, companyID, serial)
}

// ---------------------------------------------------------------------------
// Finalizing
// ---------------------------------------------------------------------------

// ReleaseFinalization reports what finalizing did, for the audit record and
// the response.
type ReleaseFinalization struct {
	SerialNumber         string
	ReleaseID            string
	CompanyID            int64
	CompanyName          string
	SiteName             string
	DeviceName           string
	ConfirmedBy          string
	PendingJobsCancelled int64
	AnnouncementsVoided  int64

	// AlreadyReleased is true when this call found the release already
	// finalized and changed nothing -- a retried confirm, or an operator and
	// the terminal racing. Reported so the caller can answer 200 without
	// writing a second audit line.
	AlreadyReleased bool
}

// finalizeReleaseTx moves a locked ORDERED row to RELEASED.
//
// The one place the soft-delete happens for a release, so the four callers
// cannot drift from one another. `deviceID` must already be locked FOR UPDATE
// by the caller and be live and ORDERED.
func finalizeReleaseTx(tx *sql.Tx, deviceID int64, by, reason string,
	report json.RawMessage) (*ReleaseFinalization, error) {

	var (
		out       ReleaseFinalization
		releaseID sql.NullString
	)
	err := tx.QueryRow(`
		SELECT d.serial_number, d.release_id::text, COALESCE(d.device_name, ''),
		       s.company_id, c.name, s.site_name
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN companies c ON c.id = s.company_id
		 WHERE d.id = $1`, deviceID).
		Scan(&out.SerialNumber, &releaseID, &out.DeviceName, &out.CompanyID,
			&out.CompanyName, &out.SiteName)
	if err != nil {
		return nil, err
	}
	out.ReleaseID = releaseID.String
	out.ConfirmedBy = by

	if len(report) == 0 || string(report) == "null" {
		report = nil
	}

	// The credential is cleared as well as the row being deleted, on the
	// reasoning RetireTerminal follows: a soft-deleted row is invisible to
	// every console query while its key would go on authenticating.
	//
	// release_order_mac is deliberately NOT cleared. See the file comment.
	if _, err := tx.Exec(`
		UPDATE devices
		   SET deleted_at = CURRENT_TIMESTAMP,
		       api_key_hash = NULL,
		       api_key_prefix = NULL,
		       credential_revoked_at = CURRENT_TIMESTAMP,
		       credential_revoked_reason = COALESCE(NULLIF($2, ''),
		           'released from this account'),
		       status = 'DISABLED',
		       active = FALSE,
		       release_state = 'RELEASED',
		       release_confirmed_at = CURRENT_TIMESTAMP,
		       release_confirmed_by = $3,
		       release_report = $4::jsonb,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, deviceID, reason, by, nullableJSON(report)); err != nil {
		return nil, err
	}

	cancelled, err := cancelQueuedWork(tx, deviceID, "terminal released from this account")
	if err != nil {
		return nil, err
	}
	out.PendingJobsCancelled = cancelled

	// Placements go with it, as on retirement: a credential recorded as living
	// on a terminal that no longer exists would make the distribution engine
	// believe a person is enrolled somewhere they cannot be.
	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'REMOVED',
		       removed_at = CURRENT_TIMESTAMP,
		       last_error = 'terminal released'
		 WHERE device_id = $1
		   AND state IN ('PENDING', 'PLACED', 'REMOVING')`, deviceID); err != nil {
		return nil, fmt.Errorf("clearing placements for released terminal: %w", err)
	}

	// Anything in flight for this serial is void. A PENDING row has no company
	// and expires; an adopted or approved one belongs to the company losing the
	// hardware and is rejected with the reason, so their operator sees why it
	// stopped rather than watching it time out.
	//
	// The announce-with-receipt path calls this BEFORE inserting the terminal's
	// fresh announcement, so the row it is about to create is not the one being
	// voided here.
	voided, err := tx.Exec(`
		UPDATE terminal_announcements
		   SET state = CASE WHEN company_id IS NULL THEN 'EXPIRED' ELSE 'REJECTED' END,
		       rejected_at = CASE WHEN company_id IS NULL
		                          THEN NULL ELSE CURRENT_TIMESTAMP END,
		       rejected_reason = CASE WHEN company_id IS NULL THEN NULL
		           ELSE 'the terminal was released from this account'
		       END
		 WHERE serial_number = $1
		   AND state IN ('PENDING', 'ADOPTED', 'APPROVED')`, out.SerialNumber)
	if err != nil {
		return nil, err
	}
	if n, err := voided.RowsAffected(); err == nil {
		out.AnnouncementsVoided = n
	}

	return &out, nil
}

// nullableJSON renders a raw message as a text parameter, or NULL.
func nullableJSON(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

// ForceTerminalRelease finalizes an outstanding order without the terminal.
//
// OPERATOR path. Requires the row to be ORDERED -- a force is an escalation of
// an order the operator already placed and confirmed the consequences of,
// never a first step. The handler owns the typed attestation.
func ForceTerminalRelease(companyID int64, serial, reason string) (*ReleaseFinalization, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var deviceID int64
	err = tx.QueryRow(`
		SELECT d.id
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $2
		   AND s.company_id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND d.release_state = 'ORDERED'
		 FOR UPDATE OF d`, companyID, serial).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, lookupErr := loadTerminalRelease(tx, companyID, serial); lookupErr != nil {
			return nil, lookupErr
		}
		return nil, ErrReleaseNotOrdered
	}
	if err != nil {
		return nil, err
	}

	out, err := finalizeReleaseTx(tx, deviceID, ReleaseConfirmedByOperator, reason, nil)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ConfirmTerminalRelease is the terminal's authenticated confirmation.
//
// The device is the one the credential resolved to, so the serial is its own.
// The receipt is verified against the row's hash and the order it carries;
// a mismatch changes nothing. A confirmation for a release that has ALREADY
// been finalized cannot arrive here -- the hash is gone and the credential no
// longer authenticates -- which is why the terminal treats a 401 on this call
// as the definitive answer it is.
func ConfirmTerminalRelease(deviceID int64, releaseID string, receipt []byte,
	report json.RawMessage) (*ReleaseFinalization, error) {

	if !looksLikeUUID(releaseID) {
		return nil, ErrReleaseNotOrdered
	}
	if len(report) > ReleaseReportMaxBytes {
		return nil, fmt.Errorf("release report exceeds %d bytes", ReleaseReportMaxBytes)
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		serial  string
		state   sql.NullString
		rowID   sql.NullString
		hash    sql.NullString
		deleted sql.NullTime
	)
	err = tx.QueryRow(`
		SELECT serial_number, release_state, release_id::text, api_key_hash, deleted_at
		  FROM devices
		 WHERE id = $1
		 FOR UPDATE`, deviceID).Scan(&serial, &state, &rowID, &hash, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	if state.String != ReleaseStateOrdered || deleted.Valid ||
		!strings.EqualFold(rowID.String, releaseID) {
		return nil, ErrReleaseNotOrdered
	}

	if !receiptVerifies(hash.String, serial, releaseID, receipt) {
		return nil, ErrReleaseReceiptMismatch
	}

	out, err := finalizeReleaseTx(tx, deviceID, ReleaseConfirmedByTerminal,
		"released from this account; the terminal confirmed the wipe", report)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// receiptVerifies checks a receipt in constant time.
func receiptVerifies(apiKeyHash, serial, releaseID string, receipt []byte) bool {
	if apiKeyHash == "" || len(receipt) != sha256.Size {
		return false
	}
	expected, err := ComputeReleaseReceipt(apiKeyHash, serial, releaseID)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, receipt)
}

// ReceiptStatus is what an announcing terminal is told about the receipt it
// presented, so it knows when to stop carrying it.
const (
	ReceiptStatusConsumed = "CONSUMED" // this call finalized the release
	ReceiptStatusUnknown  = "UNKNOWN"  // no order matches; stop presenting it
	ReceiptStatusAbsent   = "ABSENT"   // nothing was presented
)

// consumeReleaseReceiptTx finalizes a release from a receipt carried on an
// UNAUTHENTICATED announce.
//
// The receipt IS the authentication: it can only have been produced by the
// holder of the credential the row's hash describes, and it names the order.
// A receipt that does not verify is reported UNKNOWN and nothing changes -- an
// attacker who knows a serial learns nothing they could not learn by
// announcing without one.
//
// Idempotent: a serial whose matching release is already RELEASED answers
// CONSUMED with no change, so a terminal whose earlier announce succeeded but
// whose response was lost is told to stop rather than being told it is
// unknown.
func consumeReleaseReceiptTx(tx *sql.Tx, serial, releaseID string,
	receipt []byte) (string, *ReleaseFinalization, error) {

	if releaseID == "" || len(receipt) == 0 {
		return ReceiptStatusAbsent, nil, nil
	}
	if !looksLikeUUID(releaseID) {
		return ReceiptStatusUnknown, nil, nil
	}

	var (
		deviceID int64
		state    sql.NullString
		hash     sql.NullString
		deleted  sql.NullTime
	)
	err := tx.QueryRow(`
		SELECT id, release_state, api_key_hash, deleted_at
		  FROM devices
		 WHERE serial_number = $1
		   AND release_id = $2::uuid
		 ORDER BY release_ordered_at DESC
		 LIMIT 1
		 FOR UPDATE`, serial, releaseID).Scan(&deviceID, &state, &hash, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return ReceiptStatusUnknown, nil, nil
	}
	if err != nil {
		return "", nil, err
	}

	if state.String == ReleaseStateReleased {
		return ReceiptStatusConsumed, &ReleaseFinalization{
			SerialNumber: serial, ReleaseID: releaseID, AlreadyReleased: true,
		}, nil
	}
	if state.String != ReleaseStateOrdered || deleted.Valid {
		return ReceiptStatusUnknown, nil, nil
	}
	if !receiptVerifies(hash.String, serial, releaseID, receipt) {
		return ReceiptStatusUnknown, nil, nil
	}

	out, err := finalizeReleaseTx(tx, deviceID, ReleaseConfirmedByTerminal,
		"released from this account; the terminal confirmed the wipe", nil)
	if err != nil {
		return "", nil, err
	}
	return ReceiptStatusConsumed, out, nil
}

// ---------------------------------------------------------------------------
// What the terminal reads
// ---------------------------------------------------------------------------

// ReleaseOrderForDevice is the order carried on the heartbeat while the
// authenticated device's row is ORDERED and the order can be verified.
func ReleaseOrderForDevice(deviceID int64) (*ReleaseOrder, error) {
	var (
		out ReleaseOrder
		id  sql.NullString
		at  sql.NullTime
		mac []byte
	)
	err := DB.QueryRow(`
		SELECT serial_number, release_id::text, release_ordered_at, release_order_mac
		  FROM devices
		 WHERE id = $1
		   AND deleted_at IS NULL
		   AND release_state = 'ORDERED'`, deviceID).
		Scan(&out.SerialNumber, &id, &at, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(mac) != sha256.Size {
		return nil, nil
	}
	out.ReleaseID = id.String
	out.OrderedAt = at.Time
	out.MAC = mac
	return &out, nil
}

// ReleaseOrderForSerial is the by-serial lookup a terminal makes when its
// credential is refused: the newest order for the serial, live or released.
//
// UNAUTHENTICATED, and what it discloses is that an order exists for a serial
// and when. The MAC is unforgeable and unusable without the terminal's key.
func ReleaseOrderForSerial(serial string) (*ReleaseOrder, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil, nil
	}
	var (
		out ReleaseOrder
		id  sql.NullString
		at  sql.NullTime
		mac []byte
	)
	err := DB.QueryRow(`
		SELECT serial_number, release_id::text, release_ordered_at, release_order_mac
		  FROM devices
		 WHERE serial_number = $1
		   AND release_id IS NOT NULL
		   AND release_order_mac IS NOT NULL
		 ORDER BY release_ordered_at DESC
		 LIMIT 1`, serial).Scan(&out.SerialNumber, &id, &at, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(mac) != sha256.Size {
		return nil, nil
	}
	out.ReleaseID = id.String
	out.OrderedAt = at.Time
	out.MAC = mac
	return &out, nil
}

// ---------------------------------------------------------------------------
// Event attribution guard
// ---------------------------------------------------------------------------

// UploadGuard is what LogDeviceAccess needs to refuse a door event that a
// previous owner's firmware queued before this row existed.
type UploadGuard struct {
	RegisteredAt time.Time

	// Guarded is true when this serial has a RELEASED predecessor row -- the
	// only situation in which a queued event could belong to somebody else.
	Guarded bool
}

// UploadGuardTolerance is how far before this row's registration an event may
// claim to have occurred and still be accepted, covering an unsynced clock.
const UploadGuardTolerance = 10 * time.Minute

func DeviceUploadGuard(deviceID int64) (UploadGuard, error) {
	var (
		out        UploadGuard
		registered sql.NullTime
	)
	err := DB.QueryRow(`
		SELECT d.registered_at,
		       EXISTS (SELECT 1 FROM devices p
		                WHERE p.serial_number = d.serial_number
		                  AND p.id <> d.id
		                  AND p.release_state = 'RELEASED')
		  FROM devices d
		 WHERE d.id = $1`, deviceID).Scan(&registered, &out.Guarded)
	if err != nil {
		return out, err
	}
	if registered.Valid {
		out.RegisteredAt = registered.Time
	} else {
		out.Guarded = false
	}
	return out, nil
}

// EventPredatesRegistration applies the guard to one event.
//
// An event with no timestamp cannot be judged and is accepted -- a terminal
// whose clock never synced must not lose its audit trail over this. That is a
// documented residual limited to firmware that does not wipe its queue.
func (g UploadGuard) EventPredatesRegistration(occurredAt time.Time) bool {
	if !g.Guarded || occurredAt.IsZero() {
		return false
	}
	return occurredAt.Before(g.RegisteredAt.Add(-UploadGuardTolerance))
}

// ReadinessPassed answers whether an acknowledged job is the one gating this
// device's readiness. Used by the job-completion handler to write TERMINAL_READY.
func ReadinessPassed(deviceID, jobID int64) (bool, error) {
	var passed bool
	err := DB.QueryRow(`
		SELECT EXISTS (
		    SELECT 1 FROM devices d
		     WHERE d.id = $1 AND d.readiness_job_id = $2)`, deviceID, jobID).Scan(&passed)
	return passed, err
}
