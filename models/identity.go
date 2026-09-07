package models

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Identity: people, what a company calls them, and what they present.
//
// The platform's position, stated once here because every type below depends on
// it: A PERSON IS NOT A MEMBER, A CATEGORY IS NOT A TAXONOMY, AND A CREDENTIAL
// IS NOT A COLUMN.
//
// What a company calls its people is that company's vocabulary, not a value the
// platform enumerates. What a person presents is an entity with a lifecycle, not
// a nullable string on their record. Both were the opposite before
// 012_identity_and_credentials.sql, and both were industry assumptions wearing
// database constraints.

// ---------------------------------------------------------------------------
// Person categories
// ---------------------------------------------------------------------------

// PersonCategory is one entry in a company's own vocabulary for its people.
//
// Code is the machine value and is what travels to firmware in the person sync
// payload's `membership_type` field -- a legacy wire name that cannot change
// while deployed terminals parse it. Label is what an operator reads and may be
// edited freely without touching a terminal.
type PersonCategory struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	SortOrder   int    `json:"sort_order"`
	Active      bool   `json:"active"`

	// PeopleCount is populated by the list read so an operator can see what a
	// category costs before removing it.
	PeopleCount int `json:"people_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PersonCategoryRequest is the body of a category create or update.
type PersonCategoryRequest struct {
	Code        string  `json:"code"`
	Label       string  `json:"label"`
	Description *string `json:"description,omitempty"`
	SortOrder   *int    `json:"sort_order,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

// categoryCodePattern matches what the schema accepts. Enforced in Go as well so
// a bad value is a 400 with a usable message rather than a constraint violation
// the handler cannot distinguish from the database being down.
var categoryCodePattern = regexp.MustCompile(`^[A-Z0-9_]{1,30}$`)

// Category errors.
var (
	ErrCategoryCodeInvalid = errors.New("category code must be 1-30 characters of A-Z, 0-9 or underscore")
	ErrCategoryLabelEmpty  = errors.New("category label is required")
	ErrCategoryNotFound    = errors.New("category not found")
	// ErrPersonNotFound is returned when an external id does not resolve INSIDE
	// the caller's company. A person in another tenant is not found rather than
	// forbidden: telling a caller that an id exists somewhere they cannot reach
	// is itself a cross-tenant disclosure.
	ErrPersonNotFound = errors.New("person not found")
	ErrCategoryInUse  = errors.New("category is still assigned to people")

	// ErrPersonDeleted is returned when the id DOES resolve inside the caller's
	// company but the person has been soft-deleted.
	//
	// SEPARATE FROM ErrPersonNotFound BECAUSE THE CALLER MUST BEHAVE
	// DIFFERENTLY. "Not found" invites a retry -- the id might be a typo, the
	// record might arrive later. "Deleted" never will: the answer is settled
	// and will not change however many times it is asked.
	//
	// The incident: a terminal reported an enrolment for a person the platform
	// had soft-deleted. The lookup filtered deleted_at IS NULL, found nothing,
	// and answered 404 -- the same 404 a typo produces. The terminal retried
	// that report until its retry budget ran out and then DISCARDED it, and the
	// platform went on believing a credential was placed on a sensor that had
	// erased it. Both halves of that are avoidable, and both are avoided by
	// telling the caller which of the two things happened.
	ErrPersonDeleted = errors.New("person has been deleted")
)

// NormalizeCategoryCode puts a code into the form the schema stores.
//
// Uppercased and with runs of anything else collapsed to an underscore, matching
// what 012's back-fill did to existing membership_type values. A caller typing
// "Night shift" gets NIGHT_SHIFT rather than a validation error, because
// refusing a reasonable input to enforce a formatting rule the platform invented
// is not useful.
func NormalizeCategoryCode(raw string) string {
	upper := strings.ToUpper(strings.TrimSpace(raw))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range upper {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// ValidateCategoryCode reports whether a normalized code is storable.
func ValidateCategoryCode(code string) error {
	if !categoryCodePattern.MatchString(code) {
		return ErrCategoryCodeInvalid
	}
	return nil
}

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

// Credential types the platform understands.
//
// A CLOSED SET, unlike person categories, and the difference is the point:
// adding a category is configuration a customer does, while adding a credential
// type means writing code that can verify one. Pretending otherwise would let a
// customer configure a credential nothing could ever match.
const (
	CredentialFingerprint = "FINGERPRINT"
	CredentialCard        = "CARD"
	CredentialPIN         = "PIN"
	CredentialMobile      = "MOBILE"
	CredentialFace        = "FACE"
	CredentialQR          = "QR"
)

// CredentialTypes is the closed set, validated in Go as well as in the schema.
var CredentialTypes = map[string]bool{
	CredentialFingerprint: true,
	CredentialCard:        true,
	CredentialPIN:         true,
	CredentialMobile:      true,
	CredentialFace:        true,
	CredentialQR:          true,
}

// BiometricCredentialTypes are the types whose material is biometric.
//
// Separated because they are handled differently at every layer: they are
// sealed rather than stored, they are never returned to a browser in any form,
// they are placed onto sensors rather than looked up, and they carry data
// protection obligations the others do not. A caller asking "is this biometric"
// must not have to keep its own list.
var BiometricCredentialTypes = map[string]bool{
	CredentialFingerprint: true,
	CredentialFace:        true,
}

// Template formats: how a credential's substance is held.
//
// The firmware's CredentialFormat enum (credential_ref.h), spelled identically
// because these values travel on the wire in both directions.
const (
	// TemplateFormatSensorLocal means the material NEVER LEAVES the device that
	// captured it. A placement on one sensor says nothing usable to any other.
	// This is what the fitted hardware produces, and it is why such a credential
	// is substantiated by its placements rather than by stored material -- see
	// the substance check in migration 020.
	TemplateFormatSensorLocal = "SENSOR_LOCAL"

	// TemplateFormatVendorTemplate is a template read off a sensor that could be
	// written to another of the same family. This is what replication moves.
	//
	// NOT YET PRODUCED BY ANY FIRMWARE. The platform side (026) accepts, stores
	// and routes it; the terminal side does not exist yet. The fitted Adafruit
	// driver implements NEITHER half properly -- getModel() sends UpChar and
	// never reads the returned data packets, and DownChar (0x09) is not defined
	// at all -- so both directions have to be written against the driver's public
	// packet primitives before anything emits this format.
	//
	// "Of the same family" is enforced, not assumed: material moves only between
	// byte-equal devices.sensor_profile values (026). Whether two different
	// modules actually interoperate is a HARDWARE FACT THAT HAS NOT BEEN TESTED
	// and is claimed nowhere.
	TemplateFormatVendorTemplate = "VENDOR_TEMPLATE"

	// TemplateFormatIdentifier is a non-secret value -- a card number, a QR
	// payload. The only format the platform holds in the clear.
	TemplateFormatIdentifier = "IDENTIFIER"
)

// IsCredentialType reports whether code is a credential the platform handles.
func IsCredentialType(code string) bool { return CredentialTypes[code] }

// IsBiometricCredential reports whether a credential type carries biometric
// material and must therefore be sealed rather than stored.
func IsBiometricCredential(code string) bool { return BiometricCredentialTypes[code] }

// Credential lifecycle states.
const (
	// CredentialPending is requested but not yet captured. The state the
	// enrolment workflow lives in, and the one the old nullable column could
	// not express at all.
	CredentialPending = "PENDING"
	// CredentialActive is usable.
	CredentialActive = "ACTIVE"
	// CredentialSuspended is temporarily withdrawn and reversible.
	CredentialSuspended = "SUSPENDED"
	// CredentialRevoked is permanently withdrawn. Terminal state.
	CredentialRevoked = "REVOKED"
)

// Credential errors.
var (
	ErrCredentialNotFound       = errors.New("credential not found")
	ErrCredentialTypeInvalid    = errors.New("unknown credential type")
	ErrCredentialRevoked        = errors.New("credential is revoked")
	ErrCredentialNotPending     = errors.New("credential is not awaiting capture")
	ErrCredentialIdentifierUsed = errors.New("that credential identifier is already in use")
	ErrSealedMaterialRequired   = errors.New("a biometric credential must carry sealed material")
	ErrSealedMaterialRefused    = errors.New("a non-biometric credential must not carry sealed material")
)

// Credential is something a person presents to a terminal.
//
// WHAT IS ABSENT FROM THIS STRUCT, AND MUST STAY ABSENT: the sealed material
// itself. This type is what the console sees, and the console has no business
// holding biometric ciphertext -- it cannot decrypt it, it cannot use it, and a
// type that cannot carry it cannot leak it. Sealed material moves between the
// database and a device credential only, over the sync path, and is modelled by
// SealedCredentialMaterial below.
type Credential struct {
	ID       string `json:"id"`
	PersonID string `json:"person_id"`
	Type     string `json:"credential_type"`
	Status   string `json:"status"`

	// Vendor and format, so a placement is only attempted on a device that can
	// actually store it. Null for a credential captured before this was
	// recorded, and for types where it is meaningless.
	Vendor         string `json:"vendor,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`

	// Identifier is the NON-SECRET handle: a card number, a mobile device id.
	// Empty for a biometric, where there is no such thing -- the material is the
	// credential.
	Identifier string `json:"identifier,omitempty"`

	// HasMaterial reports whether sealed material exists, without disclosing
	// anything about it. This is what the console renders as "enrolled".
	HasMaterial bool `json:"has_material"`

	EnrolledDeviceSerial string     `json:"enrolled_device_serial,omitempty"`
	EnrolledAt           *time.Time `json:"enrolled_at,omitempty"`

	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`

	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokedReason string     `json:"revoked_reason,omitempty"`

	// Placements is where this credential physically lives right now. The answer
	// to "will this finger work at the east gate", which nothing could answer
	// before.
	Placements []CredentialPlacement `json:"placements"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Usable reports whether a credential could authorize anything at the given
// instant, ignoring permissions.
//
// Status and validity only. Kept on the model rather than in SQL so the same
// rule is applied by the authorization engine, by the console's rendering, and
// by any future caller -- three copies of "is this credential live" is three
// places for it to drift.
func (c Credential) Usable(at time.Time) bool {
	if c.Status != CredentialActive {
		return false
	}
	if c.ValidFrom != nil && at.Before(*c.ValidFrom) {
		return false
	}
	if c.ValidUntil != nil && !at.Before(*c.ValidUntil) {
		return false
	}
	return true
}

// Credential placement states.
const (
	PlacementPending  = "PENDING"
	PlacementPlaced   = "PLACED"
	PlacementFailed   = "FAILED"
	PlacementRemoving = "REMOVING"
	PlacementRemoved  = "REMOVED"
)

// DeviceReportablePlacementStates are the placement states a TERMINAL may
// report. (models.go has DeviceReportableStates for the device's own STATUS;
// these are different sets about different things, so they are named apart.)
//
// A device says what it did: it placed the credential, it could not, or it has
// erased it. PENDING and REMOVING are the PLATFORM's intentions -- "you should
// hold this" and "stop holding this" -- and a terminal claiming either would be
// a device deciding what the platform wants, which is backwards.
var DeviceReportablePlacementStates = map[string]bool{
	PlacementPlaced:  true,
	PlacementFailed:  true,
	PlacementRemoved: true,
}

// IsPlacementState reports whether a terminal may report this state.
func IsPlacementState(state string) bool { return DeviceReportablePlacementStates[state] }

// Placement errors.
var (
	ErrPlacementStateInvalid = errors.New(
		"state must be PLACED, FAILED or REMOVED")
	ErrPlacementSlotRequired = errors.New(
		"a PLACED report must name the sensor slot it used")
	ErrPlacementSubjectRequired = errors.New(
		"member_id is required")
)

// CredentialPlacement is the server's record that one credential should be --
// and whether it actually is -- on one terminal.
//
// THIS IS THE ENTITY THAT MAKES MULTI-TERMINAL IDENTITY WORK. Before it, a
// biometric was a locator string naming the single terminal that enrolled it,
// which is why a person enrolled at the front desk was unknown at the loading
// bay. Distribution is now a convergent process with a visible state per
// terminal, rather than a side effect of where somebody happened to stand.
type CredentialPlacement struct {
	ID           string `json:"id"`
	DeviceSerial string `json:"device_serial"`
	DeviceName   string `json:"device_name,omitempty"`
	SiteName     string `json:"site_name,omitempty"`

	// Slot is the sensor slot the DEVICE chose and reported back. Server-assigned
	// slots would be wrong the moment a sensor was replaced or a write failed.
	Slot  *int   `json:"slot,omitempty"`
	State string `json:"state"`

	// LastError is the device's own words, so an operator sees "sensor full"
	// rather than "failed".
	LastError string `json:"last_error,omitempty"`
	Attempts  int    `json:"attempts"`

	PlacedAt  *time.Time `json:"placed_at,omitempty"`
	RemovedAt *time.Time `json:"removed_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// SealedCredentialMaterial is biometric material as it moves between a device
// and the database. It never reaches a browser.
//
// The server routes these bytes to the terminals that need them, and no code
// path that handles material ever decrypts one -- upload, fetch and fan-out are
// byte pipes with authorisation checks and no key.
//
// WHAT THIS DOES NOT PROMISE, stated here rather than only in the migration
// because this is the type a future reader will find first:
//
//   - It protects against a database compromise, a backup, a replica, a support
//     engineer with SELECT, or an injection on any query touching credentials.
//     Those yield ciphertext and nothing that decrypts it.
//
//   - It does NOT protect against an attacker holding the RUNNING SERVER. This
//     comment used to say the server "cannot read Ciphertext and holds no key
//     that could"; that was never built and is not true. The server generates
//     the per-company sealing key and stores it wrapped under a master key from
//     its own environment, because it has to hand that key to each terminal
//     when the terminal collects its credential. Environment plus database
//     decrypts everything.
//
//   - It does NOT protect against an attacker with physical possession of a
//     terminal, who can read the sealing key out of NVS on a part without flash
//     encryption. That is a separate, tracked piece of work and this scheme's
//     strength depends on it.
//
// Full lifecycle and threat model: docs/sealing-key-lifecycle.md.
type SealedCredentialMaterial struct {
	// Ciphertext is opaque. Base64 on the wire, bytes in the column.
	Ciphertext []byte `json:"ciphertext"`

	// KeyID names the company sealing key that was used, so a key rotation can
	// tell which material still needs re-sealing.
	KeyID string `json:"key_id"`

	// Algorithm is recorded rather than assumed, so the scheme can change
	// without every stored credential becoming unreadable.
	Algorithm string `json:"algorithm"`

	// Digest is SHA-256 of the PLAINTEXT, computed on the device. Lets a
	// receiving terminal confirm it applied the right template and lets the
	// server deduplicate, without learning the template.
	//
	// Not a cross-vendor biometric identifier and must never become one: the
	// digest of the same finger differs between template formats, which is a
	// property worth keeping.
	Digest string `json:"digest"`
}

// SealingAlgorithms are the sealing schemes this build understands.
//
// A device presenting anything else is refused rather than stored: material the
// platform cannot describe is material it cannot later route to a terminal that
// could read it.
var SealingAlgorithms = map[string]bool{
	// AES-256-GCM with a per-company key, nonce prepended to the ciphertext.
	"AES-256-GCM": true,
}

// IsSealingAlgorithm reports whether the platform understands a sealing scheme.
func IsSealingAlgorithm(name string) bool { return SealingAlgorithms[name] }

// EnrolmentRequest is an operator asking for a person to be enrolled at a
// terminal.
//
// Replaces the old enrollment_requests flow, which keyed on a member id and had
// no way to say which terminal, which credential type, or what to do if it
// failed.
type EnrolmentRequest struct {
	CredentialID string    `json:"credential_id"`
	PersonID     string    `json:"person_id"`
	PersonName   string    `json:"person_name"`
	ExternalID   string    `json:"external_id"`
	Type         string    `json:"credential_type"`
	DeviceSerial string    `json:"device_serial,omitempty"`
	Status       string    `json:"status"`
	RequestedAt  time.Time `json:"requested_at"`
}

// CredentialRequest is the body of a console credential create.
type CredentialRequest struct {
	Type string `json:"credential_type"`

	// Identifier is required for a non-biometric credential and refused for a
	// biometric one. The asymmetry is real: a card number is chosen by whoever
	// prints the card, while a fingerprint has no identifier a human can type.
	Identifier string `json:"identifier,omitempty"`

	// DeviceSerial names where a biometric should be captured. Optional: an
	// enrolment with no terminal named is captured at whichever terminal the
	// person presents at next.
	DeviceSerial string `json:"device_serial,omitempty"`

	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

// CredentialRevokeRequest is the body of a console credential revoke.
type CredentialRevokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// Biometric replication (026)
// ---------------------------------------------------------------------------
//
// The device-facing material contract. docs/biometric-replication.md §6, and
// docs/sealing-key-lifecycle.md for what the key does and does not protect.
//
// NOTE ON WHERE THE CRYPTO IS: nowhere on this path. The platform validates the
// SHAPE of what a terminal sends -- a known algorithm, this company's active
// key, a well-formed digest, a bounded ciphertext -- stores the bytes, and hands
// them back to terminals that are authorised to hold them. It never seals,
// never unseals, and holds no key while doing any of it.

// Bounds on sealed material.
const (
	// MaxSealedMaterialBytes caps the ciphertext a terminal may upload.
	//
	// A ZFM template is ~512 bytes; adding a 12-byte nonce and a 16-byte tag
	// leaves this four times larger than anything legitimate. Generous on
	// purpose -- a bound that a real template could brush against would fail in
	// the field -- while still refusing to let the column be used as storage.
	MaxSealedMaterialBytes = 2048

	// SealingKeyIDPattern is the shape of a key label, matching the CHECK in
	// 026. Not a secret; it names which key sealed something.
	SealingKeyIDPrefix = "ck_"
)

// SealedMaterialUpload is an enrolling terminal handing over what it captured.
//
// The person is named by MemberID, the same name the roster, the sync payload
// and the placement report already use. The company is NOT a field: it is taken
// from the authenticated device, so there is no parameter through which a
// terminal could file material against another tenant.
type SealedMaterialUpload struct {
	MemberID string `json:"member_id" binding:"required"`

	// CredentialID is optional, and present when the terminal was working from
	// a pending item. Absent for an enrolment the terminal originated.
	CredentialID string `json:"credential_id,omitempty"`

	CredentialType string `json:"credential_type,omitempty"`
	Vendor         string `json:"vendor,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`

	// SensorProfile is what this terminal's module answered, not what its build
	// assumes. It becomes the ONLY compatibility rule: material moves between
	// byte-equal profiles and nowhere else.
	SensorProfile string `json:"sensor_profile" binding:"required"`

	Sealed SealedCredentialMaterial `json:"sealed" binding:"required"`
}

// SealedMaterialResponse is what a receiving terminal is given.
//
// MemberID is echoed because the terminal needs it to reconstruct the AAD the
// enrolling terminal sealed under (`company|member|key_id`) -- without it the
// unseal fails, which is the point of the binding. It is not new information:
// the terminal already holds this person from the roster.
type SealedMaterialResponse struct {
	CredentialID string `json:"credential_id"`
	MemberID     string `json:"member_id"`

	CredentialType string `json:"credential_type"`
	Vendor         string `json:"vendor,omitempty"`
	TemplateFormat string `json:"template_format,omitempty"`
	SensorProfile  string `json:"sensor_profile"`

	Sealed SealedCredentialMaterial `json:"sealed"`
}

// Material errors. Separate values rather than one, because the handler answers
// them differently and a terminal retries them differently.
var (
	ErrSealingAlgorithmUnknown = errors.New(
		"unknown sealing algorithm")
	ErrSealingKeyUnknown = errors.New(
		"that sealing key is not this company's active key")
	ErrSealedMaterialTooLarge = errors.New(
		"sealed material is larger than a template can be")
	ErrSealedMaterialEmpty = errors.New(
		"sealed material carries no ciphertext")
	ErrMaterialDigestInvalid = errors.New(
		"material digest must be 64 lowercase hex characters")
	ErrSensorProfileRequired = errors.New(
		"sensor_profile is required: material cannot be routed without it")
	ErrMaterialNotAvailable = errors.New(
		"no sealed material for that credential")

	// ErrDeviceCannotExport and ErrDeviceCannotImport are refusals based on what
	// the terminal itself reported (025). A terminal that has never reported its
	// capabilities gets these too -- NULL means "has never spoken", and the
	// replication path fails closed on it.
	ErrDeviceCannotExport = errors.New(
		"this terminal has not reported that it can export templates")
	ErrDeviceCannotImport = errors.New(
		"this terminal has not reported that it can import templates")
)

// IsMaterialDigest reports whether s is the digest shape the schema stores:
// 64 lowercase hex, the same shape every other digest in this system uses.
func IsMaterialDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
