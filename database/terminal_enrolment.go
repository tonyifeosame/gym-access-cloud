package database

import (
	"database/sql"
	"fmt"

	"github.com/lib/pq"
)

// Retiring a terminal ends the fingerprint enrolments that lived only on it.
//
// A fingerprint is bound to the sensor that captured it, so a person whose only
// copy was on a retired terminal is not enrolled anywhere. Both stores behind
// personEnrolledPredicate have to say so, or the People page keeps calling them
// Enrolled for ever: `credentials` and the legacy people.fingerprint_template.
//
// WHAT COUNTS AS STILL ENROLLED ELSEWHERE, on a terminal that is not retired:
//
//   - a PLACED placement of the credential;
//   - a PENDING or FAILED placement of a credential whose sealed material the
//     platform holds, because that copy is on its way or being retried and needs
//     nothing from the retired unit;
//   - the terminal the credential says it was enrolled on, by enrolled_device_id
//     or a backfilled legacy identifier, with no placement saying the finger has
//     left it. The result and placement writes are not atomic
//     (handlers/enrollment.go), so a real enrolment can have no placement row.
//
// A DISABLED terminal counts. Disabling is reversible and the template is still
// on its sensor, so re-enabling it must not find its people unenrolled.
//
// NOTHING ELSE ABOUT A PERSON IS TOUCHED, and no sync job is queued: their record,
// permissions, access history and attendance are not enrolment, and the terminals
// still holding them are unaffected.
//
// IT RUNS IN THE RETIRING TRANSACTION, so a failure here also leaves the terminal
// unretired. It must run after the devices have been soft-deleted.
//
// RETIREMENTS IN ONE COMPANY ARE SERIALISED HERE. Two transactions retiring the two
// terminals that hold one credential cannot see each other's uncommitted delete,
// so without the lock each would keep the credential for the sake of the other,
// and nothing could ever correct it: neither terminal can be retired again. The
// statements below take a fresh snapshot after the lock, so the second sees the
// first's commit.

const retiredEnrolmentReason = "terminal retired"

// retireEnrolmentLockClass is the first key of the advisory lock. 30 is API
// credential issuance (api_credentials.go).
const retireEnrolmentLockClass = 31

// resetEnrolmentsForRetiredTerminalsTx returns how many people became Not enrolled.
func resetEnrolmentsForRetiredTerminalsTx(tx *sql.Tx, companyID int64,
	deviceIDs []int64) (int, error) {

	if len(deviceIDs) == 0 {
		return 0, nil
	}
	retired := pq.Array(deviceIDs)

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1, $2::int)`,
		retireEnrolmentLockClass, companyID); err != nil {
		return 0, fmt.Errorf("serialising retirement: %w", err)
	}

	// REVOKED rather than deleted, so the credentials endpoint and event history
	// can still explain what this person held. A bare PENDING row has nothing to
	// explain and cannot legally be REVOKED (credentials_substance_check), so it
	// is soft-deleted, which ends it just the same.
	rows, err := tx.Query(`
		WITH retired AS (
		    SELECT id, serial_number FROM devices WHERE id = ANY($2::bigint[])
		), affected AS (
		    SELECT c.id
		      FROM credentials c
		     WHERE c.company_id = $1
		       AND c.credential_type = 'FINGERPRINT'
		       AND c.deleted_at IS NULL
		       AND c.status IN ('PENDING', 'ACTIVE')
		       AND (
		            c.enrolled_device_id = ANY($2::bigint[])
		         OR EXISTS (SELECT 1 FROM credential_placements pl
		                     WHERE pl.credential_id = c.id
		                       AND pl.device_id = ANY($2::bigint[]))
		         OR EXISTS (SELECT 1 FROM retired r
		                     WHERE starts_with(c.identifier,
		                               'legacy:terminal:' || r.serial_number || ':'))
		       )
		       AND NOT EXISTS (
		            SELECT 1 FROM credential_placements pl
		              JOIN devices d ON d.id = pl.device_id
		              JOIN sites s ON s.id = d.site_id
		             WHERE pl.credential_id = c.id
		               AND d.deleted_at IS NULL AND s.deleted_at IS NULL
		               AND (pl.state = 'PLACED'
		                    OR (pl.state IN ('PENDING', 'FAILED')
		                        AND c.sealed_material IS NOT NULL))
		       )
		       AND NOT EXISTS (
		            SELECT 1 FROM devices d
		              JOIN sites s ON s.id = d.site_id
		             WHERE (d.id = c.enrolled_device_id
		                    OR (s.company_id = $1
		                        AND starts_with(c.identifier,
		                                'legacy:terminal:' || d.serial_number || ':')))
		               AND d.deleted_at IS NULL AND s.deleted_at IS NULL
		               AND NOT EXISTS (
		                    SELECT 1 FROM credential_placements pl
		                     WHERE pl.credential_id = c.id AND pl.device_id = d.id
		                       AND pl.state IN ('REMOVING', 'REMOVED', 'FAILED'))
		       )
		), substance AS (
		    SELECT c.id,
		           (c.sealed_material IS NOT NULL OR c.identifier IS NOT NULL
		            OR c.template_format = 'SENSOR_LOCAL') AS can_revoke
		      FROM credentials c JOIN affected a ON a.id = c.id
		)
		UPDATE credentials c
		   SET status = CASE WHEN s.can_revoke THEN 'REVOKED' ELSE c.status END,
		       revoked_at = CASE WHEN s.can_revoke THEN CURRENT_TIMESTAMP END,
		       revoked_reason = $3,
		       deleted_at = CASE WHEN s.can_revoke THEN NULL ELSE CURRENT_TIMESTAMP END
		  FROM substance s
		 WHERE c.id = s.id
		RETURNING c.id, c.person_id`,
		companyID, retired, retiredEnrolmentReason)
	if err != nil {
		return 0, fmt.Errorf("revoking credentials of retired terminals: %w", err)
	}

	var credentialIDs, personIDs []int64
	for rows.Next() {
		var credentialID, personID int64
		if err := rows.Scan(&credentialID, &personID); err != nil {
			rows.Close()
			return 0, err
		}
		credentialIDs = append(credentialIDs, credentialID)
		personIDs = append(personIDs, personID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	// A placement still owed for a credential that has just ended will never be
	// delivered. REMOVING is left alone: that terminal still has a real erase to
	// confirm.
	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'REMOVED',
		       removed_at = CURRENT_TIMESTAMP,
		       last_error = $2
		 WHERE credential_id = ANY($1::bigint[])
		   AND state = 'PENDING'`, pq.Array(credentialIDs), retiredEnrolmentReason); err != nil {
		return 0, fmt.Errorf("closing placements of revoked credentials: %w", err)
	}

	// The legacy column names one terminal. If it names a retired one, point it at
	// a terminal that still holds the finger, or clear it. Repointing matters to
	// the site-key member API, which reads this column alone.
	rows, err = tx.Query(`
		UPDATE people p
		   SET fingerprint_template = (
		        SELECT 'terminal:' || d.serial_number || ':slot:' || pl.slot
		          FROM credentials c
		          JOIN credential_placements pl ON pl.credential_id = c.id
		          JOIN devices d ON d.id = pl.device_id
		          JOIN sites s ON s.id = d.site_id
		         WHERE c.person_id = p.id
		           AND c.credential_type = 'FINGERPRINT'
		           AND c.deleted_at IS NULL
		           AND c.status IN ('PENDING', 'ACTIVE')
		           AND pl.state = 'PLACED'
		           AND pl.slot IS NOT NULL
		           AND d.deleted_at IS NULL AND s.deleted_at IS NULL
		         ORDER BY pl.placed_at DESC NULLS LAST, pl.id DESC
		         LIMIT 1)
		  FROM devices r
		 WHERE r.id = ANY($2::bigint[])
		   AND p.company_id = $1
		   AND starts_with(p.fingerprint_template,
		           'terminal:' || r.serial_number || ':')
		RETURNING p.id`, companyID, retired)
	if err != nil {
		return 0, fmt.Errorf("clearing fingerprint locators of retired terminals: %w", err)
	}
	for rows.Next() {
		var personID int64
		if err := rows.Scan(&personID); err != nil {
			rows.Close()
			return 0, err
		}
		personIDs = append(personIDs, personID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	if len(personIDs) == 0 {
		return 0, nil
	}

	var reset int
	err = tx.QueryRow(`
		SELECT count(*) FROM people p
		 WHERE p.id = ANY($1::bigint[])
		   AND p.deleted_at IS NULL
		   AND NOT `+personEnrolledPredicate, pq.Array(personIDs)).Scan(&reset)
	if err != nil {
		return 0, fmt.Errorf("counting enrolments reset: %w", err)
	}
	return reset, nil
}
