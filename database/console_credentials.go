package database

import (
	"database/sql"
	"errors"
	"strings"

	"access-terminal-cloud-api/models"
)

// Credential visibility for the console (D1).
//
// ---------------------------------------------------------------------------
// NO BIOMETRIC MATERIAL CROSSES THIS BOUNDARY, IN EITHER DIRECTION
// ---------------------------------------------------------------------------
//
// The SELECT list below is the security boundary and is meant to be checked by
// reading it: a credential's public id, its type, its status, when it was taken,
// the terminal that took it, and how many terminals hold it. There is no
// `sealed_material`, no `sealed_key_id`, no `sealed_algorithm`, no
// `material_digest`, no `identifier`, no `sensor_profile`, no `vendor`, no
// `template_format`, and no `credential_placements.slot`.
//
// The models these scan into cannot carry any of those either, which is the
// same construction models.Site uses to keep the site key out of responses --
// and unlike a rule written in a comment, it survives somebody adding a field in
// a hurry. A test scans the raw HTTP body for every one of those words.
//
// ---------------------------------------------------------------------------
// WHY THIS FILE EXISTS RATHER THAN A COLUMN ON THE PERSON READ
// ---------------------------------------------------------------------------
//
// `people.fingerprint_template` answers "is this person enrolled" and nothing
// else. It cannot say where, when, at how many doors, or whether the credential
// has been revoked -- so an operator whose member is unrecognised at the east
// gate has no way to discover that the enrolment was taken at the front desk and
// binds to that sensor. `credentials` and `credential_placements` have held all
// of that since 012 and nothing operator-facing has ever read them.
//
// ---------------------------------------------------------------------------
// SCOPE
// ---------------------------------------------------------------------------
//
// Company-scoped, not narrowed by site grant, matching ListPersonPermissions --
// the precedent for every person-scoped read. People are company-wide in this
// schema, permissions already name sites and terminals to any VIEWER, and the
// rules are not secret from the people administering the deployment. Inventing a
// second, stricter scoping rule here would be a rule that exists in one place.

// personHasCredentialExpr is TRUE when a person holds a live credential row.
//
// Expects `p` to be the people alias, so it composes into any query that has
// one. PENDING and ACTIVE only: a REVOKED credential is precisely somebody who
// is NOT enrolled any more, and counting it would keep a dismissed employee
// showing as enrolled for ever. SUSPENDED is excluded on the same reasoning --
// it is a credential deliberately withdrawn.
const personHasCredentialExpr = `
	EXISTS (
	    SELECT 1 FROM credentials pc
	     WHERE pc.person_id = p.id
	       AND pc.deleted_at IS NULL
	       AND pc.status IN ('PENDING', 'ACTIVE')
	)`

// personEnrolledPredicate is TRUE when a person is enrolled AT ALL.
//
// THE UNION OF BOTH STORES, DELIBERATELY, and this is the whole reason
// ConsolePerson.EnrolmentSource exists. The structured record and the legacy
// column are written by different paths that do not keep each other in step:
// site-key tooling writes only the column, the placement endpoint writes only
// the row. A predicate that consulted one of them would report people as
// unenrolled who are enrolled, which is the failure this endpoint exists to end.
//
// WRITTEN ONCE AND SHARED by the `enrolled` filter, the person projection and
// the credentials endpoint. Three copies of "is this person enrolled" would
// become three answers, and the first place they disagreed would be a filter
// returning somebody the badge beside them calls enrolled.
const personEnrolledPredicate = `(` + personHasCredentialExpr + `
	OR COALESCE(p.fingerprint_template, '') <> '')`

// PersonEnrolment is what the two stores say about one person.
type PersonEnrolment struct {
	// HasCredential is a live row in `credentials`.
	HasCredential bool
	// HasLegacy is a non-empty people.fingerprint_template.
	HasLegacy bool
}

// Source maps the pair onto the value the console renders.
func (e PersonEnrolment) Source() string {
	switch {
	case e.HasCredential:
		return models.EnrolmentSourceCredential
	case e.HasLegacy:
		return models.EnrolmentSourceLegacy
	default:
		return models.EnrolmentSourceNone
	}
}

// Enrolled reports whether this person is enrolled at all.
func (e PersonEnrolment) Enrolled() bool { return e.HasCredential || e.HasLegacy }

// PersonEnrolmentFor reads both stores for one person.
//
// Used by the single-person reads, which have a member row in hand and need the
// other half. The list read gets the same answer as a column on its own query
// rather than calling this per row, because a page of fifty would otherwise be
// fifty extra round trips.
func PersonEnrolmentFor(companyID int64, externalID string) (PersonEnrolment, error) {
	var out PersonEnrolment
	err := DB.QueryRow(`
		SELECT `+personHasCredentialExpr+`,
		       COALESCE(p.fingerprint_template, '') <> ''
		  FROM people p
		 WHERE p.company_id = $1 AND p.external_id = $2 AND p.deleted_at IS NULL`,
		companyID, strings.TrimSpace(externalID)).Scan(&out.HasCredential, &out.HasLegacy)
	if errors.Is(err, sql.ErrNoRows) {
		return out, models.ErrPersonNotFound
	}
	return out, err
}

// ListPersonCredentials returns every credential a person holds.
//
// INCLUDES REVOKED AND SUSPENDED CREDENTIALS, and reports the state rather than
// filtering them out. "This person had a credential and it was revoked on
// Tuesday" is the answer to why they stopped working at every door, and a list
// that silently dropped the row would leave an operator looking at an empty
// panel for somebody who was enrolled an hour ago.
//
// The ENROLMENT SOURCE is computed from the live-credential rule, not from the
// length of this list -- so a person with only a REVOKED credential reads as
// NONE while their revoked row is still shown, which is the truth in both
// directions.
func ListPersonCredentials(companyID int64, externalID string) (*models.PersonCredentialsResponse, error) {
	// Resolve the person first, so a missing one is a 404 rather than an empty
	// list. An empty list is a real and different answer -- somebody who exists
	// and has never been enrolled -- and conflating the two would have the
	// console show "no credentials" for a person who is not there.
	var personID int64
	err := DB.QueryRow(`
		SELECT id FROM people
		 WHERE company_id = $1 AND external_id = $2 AND deleted_at IS NULL`,
		companyID, strings.TrimSpace(externalID)).Scan(&personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	enrolment, err := PersonEnrolmentFor(companyID, externalID)
	if err != nil {
		return nil, err
	}

	// THE SELECT LIST IS THE BOUNDARY. Read it as one: a public id, a type, a
	// status, a timestamp, the enrolling terminal's name and site, whether that
	// terminal is retired, and a count of placements. Nothing here is material
	// and nothing here locates material on a sensor.
	rows, err := DB.Query(`
		SELECT c.public_id,
		       c.credential_type,
		       c.status,
		       c.enrolled_at,
		       d.serial_number,
		       COALESCE(d.device_name, ''),
		       COALESCE(s.site_name, ''),
		       COALESCE(d.deleted_at IS NOT NULL, FALSE),

		       -- Terminals that actually HOLD this credential right now.
		       --
		       -- PLACED only. PENDING and FAILED are doors that SHOULD hold it
		       -- and do not, which is the opposite of what this number means --
		       -- counting them would tell an operator a person works at a door
		       -- where they will be refused.
		       --
		       -- Deleted devices and sites are excluded: a retired terminal
		       -- holds nothing, whatever its placement row still says.
		       (SELECT count(*)
		          FROM credential_placements pl
		          JOIN devices pd ON pd.id = pl.device_id
		          JOIN sites   ps ON ps.id = pd.site_id
		         WHERE pl.credential_id = c.id
		           AND pl.state = 'PLACED'
		           AND pd.deleted_at IS NULL
		           AND ps.deleted_at IS NULL)
		  FROM credentials c
		  -- LEFT JOIN, and deleted terminals are NOT filtered out here: the
		  -- column is ON DELETE SET NULL, so a hard-deleted device leaves NULL,
		  -- while a RETIRED one still resolves and is reported with retired=true.
		  -- Losing where an enrolment came from is worse than naming a retired
		  -- unit -- it is usually the explanation for why somebody stopped
		  -- being recognised.
		  LEFT JOIN devices d ON d.id = c.enrolled_device_id
		  LEFT JOIN sites   s ON s.id = d.site_id
		 WHERE c.person_id = $1
		   AND c.company_id = $2
		   AND c.deleted_at IS NULL
		 ORDER BY c.created_at, c.id`, personID, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	credentials := make([]models.PersonCredential, 0, 4)
	for rows.Next() {
		var item models.PersonCredential
		var serial, deviceName, siteName sql.NullString
		var retired bool

		if err := rows.Scan(&item.ID, &item.Type, &item.State, &item.EnrolledAt,
			&serial, &deviceName, &siteName, &retired,
			&item.UsableAtTerminalCount); err != nil {
			return nil, err
		}

		if serial.Valid {
			item.EnrolledAtTerminal = &models.CredentialTerminalRef{
				SerialNumber: serial.String,
				DeviceName:   deviceName.String,
				SiteName:     siteName.String,
				Retired:      retired,
			}
		}
		credentials = append(credentials, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &models.PersonCredentialsResponse{
		Count:           len(credentials),
		EnrolmentSource: enrolment.Source(),
		Credentials:     credentials,
	}, nil
}
