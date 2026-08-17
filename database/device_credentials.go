package database

import (
	"database/sql"
	"errors"
	"strings"

	"access-terminal-cloud-api/models"

	"github.com/lib/pq"
)

// The device-facing half of credentials and placements.
//
// firmware-protocol-requirements.md §4. 012_identity_and_credentials.sql built
// `credentials` and `credential_placements` and the firmware side agrees they
// are the right shape. What was missing is any way for a DEVICE to reach them:
// a terminal still reported an enrolment as a `fingerprint_template` string that
// landed in the single `people` column the placement tables were built to
// replace.
//
// That column is why "person A works at terminals A, B and C" could not be
// expressed. Not a sensor limitation -- a schema one. There is one column, so
// enrolling the same person at a second door OVERWRITES the record of the first.
//
// ---------------------------------------------------------------------------
// NO BIOMETRIC MATERIAL CROSSES THIS BOUNDARY. NONE. IN EITHER DIRECTION.
// ---------------------------------------------------------------------------
//
// The pending list carries a person, a credential id and a TYPE. It does not
// carry `sealed_material`, `material_digest`, `sealed_key_id` or an identifier,
// and the queries below do not select those columns -- so there is no field a
// future edit could accidentally populate, and a reviewer checking this
// property only has to read the SELECT lists.
//
// This is not a limitation being worked around; it is what the fitted hardware
// permits. The sensor matches on-module and its driver implements template
// EXPORT but not import, so a template captured at one door cannot be installed
// at another by this firmware at all. What the platform can usefully say is
// "this person should be enrolled here and is not", which turns a member who
// silently does not work at one door into a visible task list.
//
// ---------------------------------------------------------------------------
// WHAT "AUTHORIZED TO RECEIVE" MEANS
// ---------------------------------------------------------------------------
//
// Exactly the roster rule in database/roster.go, reused rather than restated. A
// terminal is told about a credential only for a person its own permissions
// would admit -- so a terminal at a public reception desk is not handed a work
// list naming staff who have no business there, and the enrolment surface is as
// narrow as the access surface.

// PendingPlacement is one credential a terminal should hold and does not.
type PendingPlacement struct {
	// CredentialID is the credential's public id. It is a HANDLE, not material:
	// it identifies which credential a later placement report is about.
	CredentialID string

	// PlacementID is the placement row's public id where one already exists
	// (PENDING or FAILED). Empty when the platform has not recorded an
	// intention yet and the terminal is simply expected to enrol.
	PlacementID string

	// ExternalID is the person as the wire names them, matching `member_id`
	// everywhere else in the device protocol.
	ExternalID string
	FullName   string

	CredentialType string
	TemplateFormat string
	Vendor         string

	// State is the placement's current state, or PENDING when there is no row.
	State string

	// Attempts and LastError let a terminal skip something it has already
	// failed at repeatedly rather than retrying it at every poll.
	Attempts  int
	LastError string

	// Generation is the sensor era this placement belongs to (020). A terminal
	// echoes it back so a placement written before a sensor wipe can be told
	// from one written after.
	Generation int
}

// Bounds on the pending list. A terminal has a small parse buffer and an 8 KB
// task stack; handing it a thousand-entry work list would be a denial of service
// against the device rather than a feature.
const (
	defaultPendingPlacementLimit = 25
	maxPendingPlacementLimit     = 100
)

// PendingPlacementsFor returns what this terminal is expected to hold but does
// not, newest person first.
//
// The device is identified by the authenticated credential, never by a
// parameter: a terminal must not be able to ask what some OTHER terminal is
// missing, which would turn this into an enumeration endpoint for the whole
// fleet's roster.
func PendingPlacementsFor(deviceID int64, limit int) ([]PendingPlacement, error) {
	if limit <= 0 {
		limit = defaultPendingPlacementLimit
	}
	if limit > maxPendingPlacementLimit {
		limit = maxPendingPlacementLimit
	}

	// The SELECT list is the security boundary and is worth reading as one: a
	// person's external id and name, a credential's public id, its type, format
	// and vendor, and the placement's own bookkeeping. No sealed material, no
	// digest, no identifier, no key id.
	rows, err := DB.Query(`
		SELECT c.public_id,
		       COALESCE(pl.public_id::text, ''),
		       p.external_id,
		       COALESCE(p.full_name, ''),
		       c.credential_type,
		       COALESCE(c.template_format, ''),
		       COALESCE(c.vendor, ''),
		       COALESCE(pl.state, 'PENDING'),
		       COALESCE(pl.attempts, 0),
		       COALESCE(pl.last_error, ''),
		       d.placement_generation
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN people p ON p.company_id = s.company_id
		  JOIN credentials c
		       ON c.person_id = p.id
		      AND c.company_id = p.company_id
		      AND c.deleted_at IS NULL
		  LEFT JOIN credential_placements pl
		       ON pl.credential_id = c.id AND pl.device_id = d.id
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL
		   AND p.deleted_at IS NULL
		   AND p.active

		   -- Only credentials that are supposed to be usable. A REVOKED or
		   -- SUSPENDED credential must never appear as work to do.
		   AND c.status IN ('PENDING', 'ACTIVE')

		   -- Only what this terminal can actually capture. A CARD or a PIN is a
		   -- credential this hardware has no reader for, and listing it would
		   -- produce a work list an installer cannot action.
		   AND c.credential_type = ANY($3)

		   -- The roster rule, so the enrolment surface is exactly as narrow as
		   -- the access surface.
		   AND `+rosterMembershipPredicate+`

		   -- Not already held. PLACED means the sensor has it; REMOVING and
		   -- REMOVED are the platform withdrawing it, which is the opposite of
		   -- work to do.
		   AND (pl.id IS NULL OR pl.state IN ('PENDING', 'FAILED'))
		 ORDER BY p.external_id
		 LIMIT $2`, deviceID, limit, deviceCapturableTypes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	placements := make([]PendingPlacement, 0, limit)
	for rows.Next() {
		var item PendingPlacement
		if err := rows.Scan(&item.CredentialID, &item.PlacementID, &item.ExternalID,
			&item.FullName, &item.CredentialType, &item.TemplateFormat, &item.Vendor,
			&item.State, &item.Attempts, &item.LastError, &item.Generation); err != nil {
			return nil, err
		}
		placements = append(placements, item)
	}
	return placements, rows.Err()
}

// deviceCapturableTypes is what a terminal in this fleet can enrol.
//
// FINGERPRINT only, because that is the reader that is fitted. Written as a
// list rather than an equality so adding a face module is a one-line change
// here rather than a rewrite of the query -- the firmware's credential model is
// already wider than fingerprints and the schema's is too.
func deviceCapturableTypes() any {
	return pq.Array([]string{models.CredentialFingerprint})
}

// ---------------------------------------------------------------------------
// Reporting an outcome
// ---------------------------------------------------------------------------

// PlacementReport is a terminal telling the platform what happened.
type PlacementReport struct {
	// CredentialID is the credential's public id where the terminal was working
	// from a pending item. Empty for an enrolment the terminal originated, in
	// which case the credential is resolved or created from the person.
	CredentialID string

	ExternalID     string
	CredentialType string
	TemplateFormat string
	Vendor         string

	// Slot is the 1-based sensor slot. Required for PLACED; meaningless
	// otherwise.
	Slot int

	// State is PLACED, FAILED or REMOVED.
	State string

	// Error is the device's own words -- "sensor full" -- so an operator sees
	// why rather than just that.
	Error string
}

// Validate checks a report before it reaches the database.
func (r PlacementReport) Validate() error {
	if strings.TrimSpace(r.ExternalID) == "" {
		return models.ErrPlacementSubjectRequired
	}
	if !models.IsPlacementState(r.State) {
		return models.ErrPlacementStateInvalid
	}
	if r.State == models.PlacementPlaced && r.Slot <= 0 {
		// Slot 0 is the firmware's "unbound" sentinel and the schema refuses it.
		// A PLACED report without a real slot names nothing.
		return models.ErrPlacementSlotRequired
	}
	if r.CredentialType != "" && !models.IsCredentialType(r.CredentialType) {
		return models.ErrCredentialTypeInvalid
	}
	return nil
}

// RecordPlacement writes what a terminal reported about one credential.
//
// IDEMPOTENT on (credential, device), matching the unique index 012 created. A
// terminal retrying a report whose response it never heard must not create a
// second placement -- the same contract every other device-facing write here
// keeps.
//
// The credential is resolved INSIDE the device's own company and the placement
// is written against the AUTHENTICATED device id. Neither is taken from the
// body, so there is no parameter through which a terminal could report a
// placement onto another terminal or against another tenant's person.
func RecordPlacement(deviceID int64, report PlacementReport) (*PendingPlacement, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The device's company and current sensor generation, from the device row
	// rather than from anything the caller sent.
	var companyID, generation int64
	err = tx.QueryRow(`
		SELECT s.company_id, d.placement_generation
		  FROM devices d JOIN sites s ON s.id = d.site_id
		 WHERE d.id = $1 AND d.deleted_at IS NULL AND s.deleted_at IS NULL`,
		deviceID).Scan(&companyID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	var personID int64
	err = tx.QueryRow(`
		SELECT id FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, strings.TrimSpace(report.ExternalID)).Scan(&personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	credentialID, err := resolveOrCreateCredentialTx(tx, companyID, personID, deviceID, report)
	if err != nil {
		return nil, err
	}

	// The placement itself. slot is stored only for PLACED -- a FAILED report
	// names no slot, and storing the one it tried would look like a binding.
	var slot any
	var placedAt any
	if report.State == models.PlacementPlaced {
		slot = report.Slot
		placedAt = "now"
	}

	var placementPublicID string
	err = tx.QueryRow(`
		INSERT INTO credential_placements
		    (credential_id, device_id, slot, state, last_error, attempts,
		     placed_at, removed_at, generation)
		-- $4 is cast at every use. Without it PostgreSQL sees the same
		-- parameter as a column value in one position and as a comparison
		-- operand in another, and refuses to deduce one type for it (42P08).
		VALUES ($1, $2, $3, $4::text, NULLIF($5, ''), 1,
		        CASE WHEN $6::text IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END,
		        CASE WHEN $4::text = 'REMOVED' THEN CURRENT_TIMESTAMP ELSE NULL END,
		        $7)
		ON CONFLICT (credential_id, device_id) DO UPDATE
		   SET slot       = EXCLUDED.slot,
		       state      = EXCLUDED.state,
		       last_error = EXCLUDED.last_error,
		       attempts   = credential_placements.attempts + 1,
		       placed_at  = COALESCE(EXCLUDED.placed_at, credential_placements.placed_at),
		       removed_at = EXCLUDED.removed_at,
		       generation = EXCLUDED.generation,
		       updated_at = CURRENT_TIMESTAMP
		RETURNING public_id`,
		credentialID, deviceID, slot, report.State, report.Error,
		placedAt, generation).Scan(&placementPublicID)
	if err != nil {
		return nil, err
	}

	// A credential that has actually been placed somewhere is ACTIVE. Until
	// then it is PENDING -- which is precisely the state 012 added the status
	// column to express, and which the old nullable string could not.
	if report.State == models.PlacementPlaced {
		if _, err := tx.Exec(`
			UPDATE credentials
			   SET status = 'ACTIVE',
			       enrolled_device_id = COALESCE(enrolled_device_id, $2),
			       enrolled_at = COALESCE(enrolled_at, CURRENT_TIMESTAMP),
			       updated_at = CURRENT_TIMESTAMP
			 WHERE id = $1 AND status = 'PENDING'`, credentialID, deviceID); err != nil {
			return nil, err
		}
	}

	var result PendingPlacement
	err = tx.QueryRow(`
		SELECT c.public_id, pl.public_id::text, p.external_id, COALESCE(p.full_name, ''),
		       c.credential_type, COALESCE(c.template_format, ''), COALESCE(c.vendor, ''),
		       pl.state, pl.attempts, COALESCE(pl.last_error, ''), pl.generation
		  FROM credential_placements pl
		  JOIN credentials c ON c.id = pl.credential_id
		  JOIN people p ON p.id = c.person_id
		 WHERE pl.public_id = $1::uuid`, placementPublicID).
		Scan(&result.CredentialID, &result.PlacementID, &result.ExternalID, &result.FullName,
			&result.CredentialType, &result.TemplateFormat, &result.Vendor,
			&result.State, &result.Attempts, &result.LastError, &result.Generation)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &result, nil
}

// resolveOrCreateCredentialTx finds the credential a report is about.
//
// A report naming a credential id uses it, scoped to the company. One that does
// not -- an enrolment the terminal originated -- reuses this person's existing
// credential of that type, or creates one. Creating rather than refusing is what
// lets the terminal stay the place enrolment physically happens: an operator
// stands at a door and presents a finger, and the platform learns about it
// afterwards.
func resolveOrCreateCredentialTx(tx *sql.Tx, companyID, personID, deviceID int64,
	report PlacementReport) (int64, error) {

	credentialType := report.CredentialType
	if credentialType == "" {
		credentialType = models.CredentialFingerprint
	}

	// A biometric credential a TERMINAL reports is SENSOR_LOCAL unless it says
	// otherwise, because that is the only thing the fitted hardware can produce:
	// the template is written to the sensor's own flash and its driver cannot
	// read one back in. Defaulting matters rather than being cosmetic -- the
	// format is what substantiates the credential once it leaves PENDING (see
	// migration 020), so a report that omitted it would create a credential that
	// could never become ACTIVE.
	templateFormat := report.TemplateFormat
	if templateFormat == "" && models.IsBiometricCredential(credentialType) {
		templateFormat = models.TemplateFormatSensorLocal
	}

	if report.CredentialID != "" {
		if !looksLikeUUID(report.CredentialID) {
			return 0, models.ErrCredentialNotFound
		}
		var id int64
		err := tx.QueryRow(`
			SELECT id FROM credentials
			 WHERE public_id = $1::uuid AND company_id = $2 AND person_id = $3
			   AND deleted_at IS NULL`,
			report.CredentialID, companyID, personID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			// Present but not this person's, or not this tenant's. Reported as
			// not found: a terminal learns nothing about whose it is.
			return 0, models.ErrCredentialNotFound
		}
		if err != nil {
			return 0, err
		}
		return id, nil
	}

	// An existing live credential of this type for this person.
	var id int64
	err := tx.QueryRow(`
		SELECT id FROM credentials
		 WHERE company_id = $1 AND person_id = $2 AND credential_type = $3
		   AND status IN ('PENDING', 'ACTIVE') AND deleted_at IS NULL
		 ORDER BY created_at
		 LIMIT 1`, companyID, personID, credentialType).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// None: create one. PENDING, with no material of any kind -- the substance
	// of a SENSOR_LOCAL fingerprint lives on the sensor and the platform holds
	// only the fact that it exists. 012's substance CHECK exempts PENDING for
	// exactly this case.
	err = tx.QueryRow(`
		INSERT INTO credentials
		    (company_id, person_id, credential_type, template_format, vendor,
		     status, enrolled_device_id)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), 'PENDING', $6)
		RETURNING id`,
		companyID, personID, credentialType,
		templateFormat, report.Vendor, deviceID).Scan(&id)
	return id, err
}
