package database

import (
	"database/sql"
	"errors"
	"strings"

	"access-terminal-cloud-api/models"
)

// Sealed biometric material: accepting it, routing it, and serving it (026).
//
// ---------------------------------------------------------------------------
// NO KEY IS TOUCHED ANYWHERE IN THIS FILE
// ---------------------------------------------------------------------------
//
// Everything here validates a shape, checks an authorisation, and moves opaque
// bytes. Nothing seals, nothing unseals, and nothing calls into
// database/sealing_keys.go except ActiveSealingKeyID, which returns a LABEL and
// deliberately does not unwrap anything.
//
// That is a property worth preserving deliberately rather than by accident: a
// bug in this file can route ciphertext to a terminal that should not have it --
// which is what the authorisation rules below exist to prevent -- but it cannot
// disclose a key, because there is no key in reach of it.
//
// ---------------------------------------------------------------------------
// WHAT DECIDES WHERE MATERIAL GOES
// ---------------------------------------------------------------------------
//
// Three gates, all of which must pass, and all of which fail closed:
//
//   1. THE ROSTER RULE, reused from database/roster.go rather than restated. A
//      terminal is offered material only for a person its own permissions would
//      admit, so the replication surface is exactly as narrow as the access
//      surface.
//
//   2. THE CAPABILITY, self-reported (025). NULL means "has never reported" and
//      is NOT "cannot" -- but for this path both are treated the same, because
//      sending a template to a terminal that has never said it can install one
//      is a guess, and guessing here means biometric material sitting on a
//      device that cannot use it.
//
//   3. SENSOR PROFILE EQUALITY. Material moves only between BYTE-EQUAL profiles.
//      Whether two DIFFERENT fingerprint modules can actually interoperate is a
//      hardware fact nobody has established, so no code here asserts it and no
//      inference is drawn from a shared vendor string. A module reporting a
//      different profile receives nothing and falls back to the re-enrolment
//      work list, which is a working product rather than a failure.

// MaterialStored is the outcome of an upload.
type MaterialStored struct {
	CredentialID string
	MemberID     string

	// PlacementsCreated is how many OTHER terminals were just told to expect
	// this credential. Zero is an ordinary answer -- a single-terminal company,
	// or a fleet where nothing else reports a matching sensor.
	PlacementsCreated int

	// Duplicate is true when this exact material was already stored. A terminal
	// that never heard the first response retries, and a retry must not look
	// like a failure.
	Duplicate bool
}

// Material errors that are this layer's own.
var (
	// ErrMaterialAlreadyPresent is a SECOND, DIFFERENT template for a credential
	// that already has one.
	//
	// Refused rather than overwritten, deliberately. This is SINGLE-enrolment
	// replication: overwriting would leave terminals that already placed the old
	// template holding one finger while every terminal placed afterwards holds
	// another, and the person would have to remember which door knows which
	// finger. That divergence is silent, and it presents as the exact complaint
	// this feature exists to end.
	//
	// Re-enrolling somebody is revoking the credential and creating a new one,
	// which the console already does.
	ErrMaterialAlreadyPresent = errors.New(
		"this credential already holds different sealed material")

	// ErrCredentialNotBiometric is material offered for a card or a PIN. There
	// is nothing to seal on those: their substance is a non-secret identifier.
	ErrCredentialNotBiometric = errors.New(
		"only a biometric credential carries sealed material")
)

// validateUpload checks everything about an upload that does not need the
// database.
//
// Separated so the rules are readable as a list, and so a malformed body is
// refused before it can open a transaction.
func validateUpload(up models.SealedMaterialUpload) error {
	if strings.TrimSpace(up.MemberID) == "" {
		return models.ErrPlacementSubjectRequired
	}
	if strings.TrimSpace(up.SensorProfile) == "" {
		return models.ErrSensorProfileRequired
	}
	if up.CredentialType != "" && !models.IsCredentialType(up.CredentialType) {
		return models.ErrCredentialTypeInvalid
	}
	if up.CredentialType != "" && !models.IsBiometricCredential(up.CredentialType) {
		return ErrCredentialNotBiometric
	}
	if !models.IsSealingAlgorithm(up.Sealed.Algorithm) {
		return models.ErrSealingAlgorithmUnknown
	}
	if len(up.Sealed.Ciphertext) == 0 {
		return models.ErrSealedMaterialEmpty
	}
	if len(up.Sealed.Ciphertext) > models.MaxSealedMaterialBytes {
		return models.ErrSealedMaterialTooLarge
	}
	if !models.IsMaterialDigest(up.Sealed.Digest) {
		return models.ErrMaterialDigestInvalid
	}
	if strings.TrimSpace(up.Sealed.KeyID) == "" {
		return models.ErrSealingKeyUnknown
	}
	return nil
}

// StoreCredentialMaterial records what an enrolling terminal captured, and then
// tells every other eligible terminal to expect it.
//
// The company is taken from the AUTHENTICATED DEVICE and never from the body,
// so there is no parameter through which a terminal could file material against
// another tenant's person.
func StoreCredentialMaterial(deviceID int64, up models.SealedMaterialUpload) (*MaterialStored, error) {
	if err := validateUpload(up); err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The device's company, and whether it has told us it can export at all.
	// Both from the device row; neither from the caller.
	var companyID int64
	var canExport bool
	err = tx.QueryRow(`
		SELECT s.company_id,
		       COALESCE(d.capabilities @> jsonb_build_array($2::text), FALSE)
		  FROM devices d JOIN sites s ON s.id = d.site_id
		 WHERE d.id = $1 AND d.deleted_at IS NULL AND s.deleted_at IS NULL`,
		deviceID, models.CapabilityBiometricExport).Scan(&companyID, &canExport)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	if !canExport {
		return nil, models.ErrDeviceCannotExport
	}

	// The key must be THIS company's active key. Checked by LABEL -- the key
	// itself is never unwrapped on this path.
	activeKeyID, err := ActiveSealingKeyID(companyID)
	if err != nil {
		return nil, err
	}
	if activeKeyID == "" || activeKeyID != up.Sealed.KeyID {
		return nil, models.ErrSealingKeyUnknown
	}

	var personID int64
	err = tx.QueryRow(`
		SELECT id FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, strings.TrimSpace(up.MemberID)).Scan(&personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	// Reuse the enrolment path's own resolver, so material arriving for a
	// credential the platform already requested attaches to THAT credential
	// rather than creating a second one beside it.
	credentialID, err := resolveOrCreateCredentialTx(tx, companyID, personID, deviceID,
		PlacementReport{
			CredentialID:   up.CredentialID,
			ExternalID:     up.MemberID,
			CredentialType: up.CredentialType,
			TemplateFormat: models.TemplateFormatVendorTemplate,
			Vendor:         up.Vendor,
		})
	if err != nil {
		return nil, err
	}

	// What is already there decides whether this is a store, a retry, or a
	// refusal.
	var existingDigest sql.NullString
	var credentialType string
	err = tx.QueryRow(`
		SELECT material_digest, credential_type FROM credentials WHERE id = $1`,
		credentialID).Scan(&existingDigest, &credentialType)
	if err != nil {
		return nil, err
	}
	if !models.IsBiometricCredential(credentialType) {
		return nil, ErrCredentialNotBiometric
	}

	duplicate := false
	switch {
	case existingDigest.Valid && existingDigest.String == up.Sealed.Digest:
		// A retry of an upload whose response never arrived. Idempotent: the
		// row is left alone and fan-out re-runs, which is itself idempotent.
		duplicate = true
	case existingDigest.Valid:
		return nil, ErrMaterialAlreadyPresent
	default:
		_, err = tx.Exec(`
			UPDATE credentials
			   SET sealed_material  = $2,
			       sealed_key_id    = $3,
			       sealed_algorithm = $4,
			       material_digest  = $5,
			       sensor_profile   = $6,
			       template_format  = $7,
			       vendor           = COALESCE(NULLIF($8, ''), vendor),
			       enrolled_device_id = COALESCE(enrolled_device_id, $9),
			       enrolled_at      = COALESCE(enrolled_at, CURRENT_TIMESTAMP),
			       updated_at       = CURRENT_TIMESTAMP
			 WHERE id = $1`,
			credentialID, up.Sealed.Ciphertext, up.Sealed.KeyID, up.Sealed.Algorithm,
			up.Sealed.Digest, strings.TrimSpace(up.SensorProfile),
			models.TemplateFormatVendorTemplate, up.Vendor, deviceID)
		if err != nil {
			return nil, err
		}
	}

	created, err := fanOutCredentialTx(tx, credentialID, deviceID)
	if err != nil {
		return nil, err
	}

	var credentialPublicID string
	if err := tx.QueryRow(`SELECT public_id FROM credentials WHERE id = $1`,
		credentialID).Scan(&credentialPublicID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MaterialStored{
		CredentialID:      credentialPublicID,
		MemberID:          strings.TrimSpace(up.MemberID),
		PlacementsCreated: created,
		Duplicate:         duplicate,
	}, nil
}

// fanOutCredentialTx creates a PENDING placement on every OTHER terminal that
// should hold this credential and does not already have a placement for it.
//
// One statement, because the decision is a single set expression and splitting
// it into a read and a loop of writes would let the roster change underneath.
//
// ON CONFLICT DO NOTHING is the whole re-run story. A placement that already
// exists is left exactly as it is -- PLACED stays placed and is not re-sent,
// FAILED keeps its error and its attempt count, and REMOVING/REMOVED are the
// platform withdrawing the credential and must not be resurrected by a fan-out.
// So this is safe to call again on any trigger that changes the roster.
func fanOutCredentialTx(tx *sql.Tx, credentialID, sourceDeviceID int64) (int, error) {
	result, err := tx.Exec(`
		INSERT INTO credential_placements
		    (credential_id, device_id, state, generation, source_device_id)
		SELECT c.id, d.id, 'PENDING', d.placement_generation, $2
		  FROM credentials c
		  JOIN people p  ON p.id = c.person_id
		  JOIN sites s   ON s.company_id = c.company_id
		  JOIN devices d ON d.site_id = s.id
		 WHERE c.id = $1
		   AND c.deleted_at IS NULL
		   AND c.status IN ('PENDING', 'ACTIVE')
		   AND c.sealed_material IS NOT NULL

		   -- Never the terminal that enrolled: it already holds the template in
		   -- its own sensor and reported that placement itself.
		   AND d.id <> $2
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND p.deleted_at IS NULL
		   AND p.active

		   -- Self-reported (025). NULL capabilities yield NULL here, which is
		   -- not true, so a terminal that has never reported is never a target.
		   AND d.capabilities @> jsonb_build_array($3::text)

		   -- The ONLY compatibility rule, and it is equality. See 026.
		   AND d.sensor_profile IS NOT NULL
		   AND d.sensor_profile = c.sensor_profile

		   -- Exactly as narrow as the access surface.
		   AND `+rosterMembershipPredicate+`
		ON CONFLICT (credential_id, device_id) DO NOTHING`,
		credentialID, sourceDeviceID, models.CapabilityBiometricImport)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// FanOutCredential re-runs fan-out for one credential.
//
// Exported so the roster-changing paths can converge a credential without this
// file having to know about them. Idempotent, by the same ON CONFLICT rule.
func FanOutCredential(credentialID, sourceDeviceID int64) (int, error) {
	tx, err := DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	created, err := fanOutCredentialTx(tx, credentialID, sourceDeviceID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}

// FetchCredentialMaterial serves sealed material to a terminal that is owed it.
//
// ---------------------------------------------------------------------------
// ONE ANSWER FOR EVERY REFUSAL
// ---------------------------------------------------------------------------
//
// No placement, wrong state, no capability, mismatched sensor, revoked
// credential, person deactivated, roster says no: all of them are
// ErrMaterialNotAvailable, and the handler turns that into one 404. A terminal
// that is not entitled to a credential does not learn which of those reasons
// applied, or that the credential exists at all.
//
// The device is the AUTHENTICATED one. There is no device parameter, so a
// terminal cannot fetch on another's behalf.
func FetchCredentialMaterial(deviceID int64, credentialPublicID string) (*models.SealedMaterialResponse, error) {
	if !looksLikeUUID(credentialPublicID) {
		return nil, models.ErrMaterialNotAvailable
	}

	var out models.SealedMaterialResponse
	err := DB.QueryRow(`
		SELECT c.public_id,
		       p.external_id,
		       c.credential_type,
		       COALESCE(c.vendor, ''),
		       COALESCE(c.template_format, ''),
		       c.sensor_profile,
		       c.sealed_material,
		       c.sealed_key_id,
		       c.sealed_algorithm,
		       c.material_digest
		  FROM credential_placements pl
		  JOIN credentials c ON c.id = pl.credential_id
		  JOIN people p      ON p.id = c.person_id
		  JOIN devices d     ON d.id = pl.device_id
		  JOIN sites s       ON s.id = d.site_id
		 WHERE pl.device_id = $1
		   AND c.public_id = $2::uuid

		   -- Only what this terminal has been TOLD to hold. PENDING is work to
		   -- do and FAILED is work to retry; PLACED means it already has it, and
		   -- REMOVING/REMOVED are the platform taking it away. Without this
		   -- clause the endpoint would be a way to read any credential on the
		   -- roster, which is precisely what it must not be.
		   AND pl.state IN ('PENDING', 'FAILED')

		   AND c.deleted_at IS NULL
		   AND c.status IN ('PENDING', 'ACTIVE')
		   AND c.sealed_material IS NOT NULL

		   -- Defence in depth: the placement already implies the tenant, and
		   -- this makes a mis-scoped placement fail closed rather than leak.
		   AND s.company_id = c.company_id

		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND p.deleted_at IS NULL
		   AND p.active

		   AND d.capabilities @> jsonb_build_array($3::text)

		   AND d.sensor_profile IS NOT NULL
		   AND d.sensor_profile = c.sensor_profile

		   AND `+rosterMembershipPredicate,
		deviceID, credentialPublicID, models.CapabilityBiometricImport).
		Scan(&out.CredentialID, &out.MemberID, &out.CredentialType, &out.Vendor,
			&out.TemplateFormat, &out.SensorProfile, &out.Sealed.Ciphertext,
			&out.Sealed.KeyID, &out.Sealed.Algorithm, &out.Sealed.Digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrMaterialNotAvailable
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
