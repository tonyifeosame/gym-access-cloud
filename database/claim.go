package database

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// Device claim codes (migrations/019_device_claim_codes.sql).
//
// firmware-protocol-requirements.md §6. Enrolling one terminal required the SITE
// API KEY on an installer's laptop. That key is the provisioning secret for the
// whole site -- it registers devices there and rotates any of their credentials
// -- so bringing up one door put it in a shell history and a terminal's
// scrollback, and the device key it produced was then read out and pasted, which
// is two more places nobody will clean.
//
// The exchange replaces that: an operator creates the code in the console
// (authenticated, scoped, audited), the installer types it into the unit, and
// the unit fetches its own credential. THE DEVICE KEY IS NEVER DISPLAYED
// ANYWHERE.
//
// ---------------------------------------------------------------------------
// WHY AN UNAUTHENTICATED ENDPOINT IS ACCEPTABLE HERE
// ---------------------------------------------------------------------------
//
// It is the same argument the operator credential handover makes, and it rests
// on four properties rather than on the endpoint being obscure:
//
//   1. BOUND TO ONE SERIAL. The device sends the serial it derives from its
//      factory MAC. A code redeemed by different hardware is refused. This is
//      what makes an intercepted code close to worthless -- an attacker needs
//      the code AND the specific unit it was issued for.
//   2. SINGLE USE. Redemption marks it, and issuing a new code for a serial
//      supersedes any outstanding one, so an old printout cannot claim a unit a
//      colleague just re-issued.
//   3. SHORT LIVED. Minutes to hours; an installer claims a terminal while
//      standing at it.
//   4. HASHED AT REST. Like every other secret in this schema.
//
// Rate limiting is the fifth and lives at the route, because it is a property of
// the HTTP surface rather than of the record.
//
// ---------------------------------------------------------------------------
// WHAT A FAILED REDEMPTION IS TOLD
// ---------------------------------------------------------------------------
//
// One error, for every failure. Wrong code, expired code, superseded code, right
// code and wrong serial -- all ErrClaimRefused. Distinguishing them would let an
// unauthenticated caller learn that a code is real but the serial is wrong,
// which is precisely the fact that turns an intercepted code back into
// something worth having.

// Claim code errors.
var (
	// ErrClaimRefused covers every redemption failure. See the note above:
	// telling them apart is the leak.
	ErrClaimRefused = errors.New("the claim code was refused")

	ErrClaimSerialRequired = errors.New("serial_number is required")
	ErrClaimCodeRequired   = errors.New("claim_code is required")
)

// Claim code shape.
//
// EIGHT CHARACTERS FROM A 30-SYMBOL ALPHABET is log2(30^8) ~= 39 bits, and the
// grouping into two blocks of four is what makes it typable over a serial
// console by somebody standing at a door.
//
// Thirty-nine bits is not a lot on its own. It does not have to be: a code is
// valid for ONE serial, for minutes, ONCE, behind a rate limiter. The realistic
// attack is not offline search of the keyspace -- there is nothing to search
// offline, because only the hash is stored -- it is online guessing, which the
// limiter bounds to a rate at which 2^39 is unreachable many times over before
// the code expires.
const (
	claimCodeGroups    = 2
	claimCodeGroupSize = 4
	claimCodeSeparator = "-"

	// DefaultClaimCodeTTL is what the console issues when it does not say.
	DefaultClaimCodeTTL = 2 * time.Hour
	// MaxClaimCodeTTL bounds what an operator may ask for. A code that lives
	// for a week is a site key with extra steps.
	MaxClaimCodeTTL = 24 * time.Hour
)

// claimAlphabet excludes I, L, O, U, 0 and 1.
//
// Crockford's set, minus the letters that are misread when typed off a screen
// into a serial console at a door. U is dropped as well, which is conventional
// and avoids accidental words. The firmware refuses a code containing a quote, a
// backslash, a control byte or a space -- every character here is safely outside
// all four.
const claimAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// generateClaimCode returns a plaintext code and its storage form.
func generateClaimCode() (code, hash, prefix string, err error) {
	total := claimCodeGroups * claimCodeGroupSize

	// rand.Read, then rejection-free indexing via modulo over a buffer drawn
	// large enough that the bias is not the weak part of a 40-bit code used
	// once, for minutes, behind a limiter. Stated rather than hidden: an
	// alphabet of 30 into 256 is a bias of about 1.6%, which changes the
	// effective entropy by well under a bit.
	buf := make([]byte, total)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generating claim code: %w", err)
	}

	var b strings.Builder
	for i := 0; i < total; i++ {
		if i > 0 && i%claimCodeGroupSize == 0 {
			b.WriteString(claimCodeSeparator)
		}
		b.WriteByte(claimAlphabet[int(buf[i])%len(claimAlphabet)])
	}

	code = b.String()
	hash = hashClaimCode(code)
	// The non-secret leading group, so the console can say WHICH code was
	// issued without being able to reproduce it.
	prefix = code[:claimCodeGroupSize]
	return code, hash, prefix, nil
}

// hashClaimCode returns the stored form.
//
// SHA-256 rather than bcrypt, on the same reasoning as device keys: the secret
// is machine-generated rather than human-chosen, so it is not subject to
// dictionary attack, and the redemption path has to look it up by hash on an
// unauthenticated request. Normalised to upper case first, so an installer who
// types it in lower case is not refused for a reason nobody would guess.
func hashClaimCode(code string) string {
	sum := sha256.Sum256([]byte(normalizeClaimCode(code)))
	return hex.EncodeToString(sum[:])
}

// normalizeClaimCode puts a typed code into its canonical form.
func normalizeClaimCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// IssuedClaimCode is a minted code, returned ONCE.
type IssuedClaimCode struct {
	// Code is the plaintext and exists here and nowhere else.
	Code         string
	Prefix       string
	SerialNumber string
	SiteID       int64
	SiteName     string
	ExpiresAt    time.Time

	// SupersededCount is how many outstanding codes this one invalidated, so
	// the console can say "this replaced an earlier code" rather than leaving an
	// installer holding a printout that has silently stopped working.
	SupersededCount int
}

// IssueClaimCode mints a code for one serial at one site.
//
// The site is resolved INSIDE the caller's company, so a code cannot be issued
// against another tenant's site. Superseding is done in the same transaction as
// the insert: two live codes for one serial would make "single use" meaningless.
func IssueClaimCode(companyID, siteID int64, serial string, ttl time.Duration,
	actorID int64, actorEmail string) (*IssuedClaimCode, error) {

	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil, ErrClaimSerialRequired
	}
	// The firmware stores a serial in char[16], so anything longer could never
	// be presented by the hardware it was issued for. Refused here rather than
	// discovered at a door.
	if len(serial) > models.MaxDeviceSerialLength {
		return nil, models.ErrDeviceSerialTooLong
	}

	if ttl <= 0 {
		ttl = DefaultClaimCodeTTL
	}
	if ttl > MaxClaimCodeTTL {
		ttl = MaxClaimCodeTTL
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var siteName string
	err = tx.QueryRow(`
		SELECT site_name FROM sites
		 WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL AND active`,
		siteID, companyID).Scan(&siteName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}

	// Supersede first, so the partial unique index on (site, serial) has room.
	superseded, err := tx.Exec(`
		UPDATE device_claim_codes
		   SET superseded_at = CURRENT_TIMESTAMP
		 WHERE site_id = $1 AND serial_number = $2
		   AND redeemed_at IS NULL AND superseded_at IS NULL`, siteID, serial)
	if err != nil {
		return nil, err
	}
	supersededCount, err := superseded.RowsAffected()
	if err != nil {
		return nil, err
	}

	code, hash, prefix, err := generateClaimCode()
	if err != nil {
		return nil, err
	}

	expiresAt := time.Now().UTC().Add(ttl)

	_, err = tx.Exec(`
		INSERT INTO device_claim_codes
		    (site_id, company_id, code_hash, code_prefix, serial_number,
		     issued_by, issued_by_email, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0)::bigint, NULLIF($7, ''), $8)`,
		siteID, companyID, hash, prefix, serial, actorID, actorEmail, expiresAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &IssuedClaimCode{
		Code:            code,
		Prefix:          prefix,
		SerialNumber:    serial,
		SiteID:          siteID,
		SiteName:        siteName,
		ExpiresAt:       expiresAt,
		SupersededCount: int(supersededCount),
	}, nil
}

// ClaimedDevice is what a successful redemption produced.
type ClaimedDevice struct {
	APIKey       string
	SerialNumber string

	DeviceID  int64
	CompanyID int64
	SiteID    int64
	SiteName  string

	// ClaimPublicID identifies the code that was redeemed, for the audit record.
	ClaimPublicID string
	IssuedByEmail string

	BootstrapJobs int
}

// RedeemClaimCode exchanges a code for a device credential.
//
// UNAUTHENTICATED. Everything that decides anything comes from the stored record
// rather than from the request: the site, the company and the serial the code
// was issued for. The caller supplies a code and a serial and gets either a
// credential for exactly that pairing or one indistinguishable refusal.
//
// One transaction from the lookup to the credential. A code marked redeemed
// without a device credential having been issued would strand the installer with
// a dead code and no key; a credential issued without the code being marked
// would leave a reusable code behind.
func RedeemClaimCode(code, serial, ip string) (*ClaimedDevice, error) {
	code = normalizeClaimCode(code)
	serial = strings.TrimSpace(serial)

	if code == "" {
		return nil, ErrClaimCodeRequired
	}
	if serial == "" {
		return nil, ErrClaimSerialRequired
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// FOR UPDATE, so two devices racing the same code cannot both be issued a
	// credential. The row is the lock.
	var (
		claimID       int64
		claimPublicID string
		siteID        int64
		companyID     int64
		storedSerial  string
		issuedByEmail sql.NullString
	)
	err = tx.QueryRow(`
		SELECT id, public_id, site_id, company_id, serial_number,
		       issued_by_email
		  FROM device_claim_codes
		 WHERE code_hash = $1
		   AND redeemed_at IS NULL
		   AND superseded_at IS NULL
		   AND expires_at > CURRENT_TIMESTAMP
		 FOR UPDATE`, hashClaimCode(code)).
		Scan(&claimID, &claimPublicID, &siteID, &companyID, &storedSerial, &issuedByEmail)
	if errors.Is(err, sql.ErrNoRows) {
		// Unknown, expired, superseded or already redeemed -- one answer.
		return nil, ErrClaimRefused
	}
	if err != nil {
		return nil, err
	}

	// BOUND TO ONE SERIAL. The comparison is the security property that makes an
	// intercepted code close to worthless, and it is deliberately made AFTER the
	// row is found so that a wrong serial and a wrong code take the same path
	// and return the same error.
	if !strings.EqualFold(storedSerial, serial) {
		return nil, ErrClaimRefused
	}

	// The site must still be usable. A code outliving its site's deactivation
	// would be a way to enrol hardware into a site an operator had switched off.
	var siteActive bool
	var siteName string
	err = tx.QueryRow(`
		SELECT active, site_name FROM sites
		 WHERE id = $1 AND deleted_at IS NULL`, siteID).Scan(&siteActive, &siteName)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !siteActive) {
		return nil, ErrClaimRefused
	}
	if err != nil {
		return nil, err
	}

	// The credential itself, through the SAME registration path an operator's
	// site key would use -- so a claimed terminal is not a second class of
	// device with a different lifecycle. Registration is idempotent by serial
	// and rotates the credential of an existing row, which is exactly right for
	// a unit being re-provisioned after a factory reset.
	device, key, jobs, err := registerDeviceTx(tx, siteID, models.DeviceRegistrationRequest{
		SerialNumber: storedSerial,
	}, ProvisionedViaClaimCode)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec(`
		UPDATE device_claim_codes
		   SET redeemed_at = CURRENT_TIMESTAMP,
		       redeemed_ip = NULLIF($2, ''),
		       redeemed_device_id = $3
		 WHERE id = $1`, claimID, ip, device.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &ClaimedDevice{
		APIKey:        key,
		SerialNumber:  device.SerialNumber,
		DeviceID:      device.ID,
		CompanyID:     companyID,
		SiteID:        siteID,
		SiteName:      siteName,
		ClaimPublicID: claimPublicID,
		IssuedByEmail: issuedByEmail.String,
		BootstrapJobs: jobs,
	}, nil
}

// PurgeExpiredClaimCodes removes codes that can no longer be redeemed.
//
// Kept for a window after expiry rather than deleted at once: "was a code ever
// issued for this serial, and was it used" is an audit question, and the answer
// disappearing the moment the code expires would make it unanswerable.
func PurgeExpiredClaimCodes(retentionDays int) (int64, error) {
	if retentionDays < 0 {
		return 0, nil
	}
	result, err := DB.Exec(`
		DELETE FROM device_claim_codes
		 WHERE expires_at < CURRENT_TIMESTAMP - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
