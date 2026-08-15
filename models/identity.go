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
// The server cannot read Ciphertext and holds no key that could. It routes the
// bytes to the terminals that need them and can prove which plaintext they
// correspond to, via Digest, without ever recovering a biometric.
//
// WHAT THIS DOES NOT PROMISE, stated here rather than only in the migration
// because this is the type a future reader will find first: it protects against
// a database compromise, a backup, a replica or a support engineer with SELECT.
// It does NOT protect against an attacker with physical possession of a
// terminal, who can read the sealing key out of NVS on a part without flash
// encryption. That is a separate, tracked piece of work and this scheme's
// strength depends on it.
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
