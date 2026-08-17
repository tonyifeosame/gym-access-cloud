package database

import (
	"context"
	"database/sql"
	"log"

	"access-terminal-cloud-api/models"
)

// Roster scoping (SEC-04).
//
// THE FINDING. `enqueuePersonChangeTx` and `enqueueBootstrapJobs` joined
// `people` to `devices` through `sites.company_id`, so every person a company
// had was pushed to every terminal it owned. A company with four sites and one
// roster sent the whole roster to all of them -- which is a data-minimisation
// problem (a terminal at a public reception desk holding the entire staff list,
// including the credential column), an operational one (person 65 exhausts a
// terminal's table and parks FAILED, which is half of FW-01), and a correctness
// one (a site-scoped permission meant nothing at the door because every
// terminal held everybody anyway).
//
// ---------------------------------------------------------------------------
// WHAT A TERMINAL'S ROSTER IS NOW
// ---------------------------------------------------------------------------
//
// The people a live ALLOW permission could admit at THAT terminal, minus the
// people an unconditional DENY refuses there. Not the company's roster, and not
// the site's roster either -- the permission set's, which is what APP-02 made
// expressible and what the audit's remediation names once the engine exists.
//
// ---------------------------------------------------------------------------
// WHY DENY IS SUBTRACTED ONLY WHEN IT IS UNCONDITIONAL
// ---------------------------------------------------------------------------
//
// A terminal caches a flat roster; it does not evaluate the permission model.
// So the subtraction can only be safe for a DENY that refuses under EVERY
// circumstance at that terminal -- no application scope, no schedule, and
// currently inside its validity window. That person can never be admitted
// there, so removing them from the roster is exactly right and is what makes an
// offline terminal honour an exclusion.
//
// A DENY that is narrower than that -- "not for ATTENDANCE", "not on Sundays" --
// is CONDITIONAL, and a terminal that dropped the person entirely would refuse
// them in the hours they are allowed. Those people stay on the roster and the
// server decides at the door. THIS IS A KNOWN LIMITATION AND IT IS WRITTEN DOWN
// RATHER THAN HIDDEN: while a terminal is offline, a conditional DENY is not
// enforced. The site's offline policy is the lever that bounds it, and a
// customer who cannot accept that sets DENY_ALL.
//
// ---------------------------------------------------------------------------
// TIME
// ---------------------------------------------------------------------------
//
// Validity windows make roster membership change with the clock rather than
// with an edit, and nothing edits a row when a permission expires. That is what
// ReconcileDeviceRoster exists for -- see maintenance/tasks.go, which runs it.
// Without it, a permission that expired at midnight would leave the person on
// every terminal until somebody happened to touch their record.

// rosterMembershipPredicate is the SQL that decides whether person `p` belongs
// on device `d`'s roster.
//
// Written once and shared by the change fan-out, the bootstrap seed, the resync
// snapshot and the reconciler, because four copies of this rule would become
// four different rules. It expects `p` to be the people alias and `d` the
// devices alias, and it references no parameters, so it composes into any query
// that has both.
const rosterMembershipPredicate = `
	EXISTS (
	    SELECT 1 FROM permissions pm
	     WHERE pm.person_id = p.id
	       AND pm.deleted_at IS NULL
	       AND pm.active
	       AND pm.effect = 'ALLOW'
	       AND (pm.starts_at IS NULL OR pm.starts_at <= CURRENT_TIMESTAMP)
	       AND (pm.ends_at   IS NULL OR pm.ends_at   >  CURRENT_TIMESTAMP)
	       AND (   pm.scope_type = 'COMPANY'
	            OR (pm.scope_type = 'SITE'     AND pm.site_id   = d.site_id)
	            OR (pm.scope_type = 'TERMINAL' AND pm.device_id = d.id))
	)
	AND NOT EXISTS (
	    SELECT 1 FROM permissions pm
	     WHERE pm.person_id = p.id
	       AND pm.deleted_at IS NULL
	       AND pm.active
	       AND pm.effect = 'DENY'
	       -- Unconditional only. A DENY narrowed to one capability or one
	       -- schedule cannot be enforced by removing somebody from a flat
	       -- roster without also refusing them when they ARE allowed.
	       AND pm.application IS NULL
	       AND pm.schedule_id IS NULL
	       AND (pm.starts_at IS NULL OR pm.starts_at <= CURRENT_TIMESTAMP)
	       AND (pm.ends_at   IS NULL OR pm.ends_at   >  CURRENT_TIMESTAMP)
	       AND (   pm.scope_type = 'COMPANY'
	            OR (pm.scope_type = 'SITE'     AND pm.site_id   = d.site_id)
	            OR (pm.scope_type = 'TERMINAL' AND pm.device_id = d.id))
	)`

// deviceIsSyncable is the other half of every roster query: which terminals are
// eligible to receive people at all.
//
// A DISABLED or revoked terminal is deliberately excluded. Queueing work for a
// terminal that cannot authenticate would build a backlog that looks like a sync
// failure, and a revoked unit must not be handed a roster if it ever comes back.
const deviceIsSyncable = `
	d.active = TRUE
	AND d.deleted_at IS NULL
	AND d.status <> 'DISABLED'
	AND d.api_key_hash IS NOT NULL`

// ReconcileDeviceRoster brings one terminal's queued roster back in line with
// what its permissions currently say, and returns what it changed.
//
// WHY THIS IS NOT OPTIONAL. Roster membership depends on time -- a permission
// with a validity window joins and leaves without anything editing a row -- and
// on rules written about OTHER people's permissions, which no person-change hook
// observes. Without a reconciler, "access expires on Friday" is a promise the
// platform makes and does not keep at an offline terminal.
//
// It is also the recovery path SYN-01 asks for. A terminal whose apply failures
// left it with a partial roster converges here rather than needing somebody to
// notice and force a full resync.
//
// IDEMPOTENT AND CHEAP WHEN THERE IS NOTHING TO DO: the two statements insert
// only what is missing and delete only what should not be there, so a converged
// terminal produces zero jobs.
func ReconcileDeviceRoster(deviceID int64) (added, removed int, err error) {
	// Added: people who belong on the roster and have no live job saying so.
	//
	// The NOT EXISTS against sync_jobs is what makes this idempotent -- running
	// it twice before the terminal has polled must not queue the person twice.
	addResult, err := DB.Exec(`
		INSERT INTO sync_jobs
		    (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
		     payload, protocol_version, status, next_attempt_at)
		SELECT d.site_id, d.id, 'CREATE', 'PERSON', p.id, p.external_id,
		       jsonb_build_object(
		           'member_id', p.external_id,
		           'full_name', p.full_name,
		           'membership_type', p.membership_type,
		           'active', p.active,
		           'fingerprint_template', COALESCE(p.fingerprint_template, ''),
		           'deleted', FALSE,
		           'updated_at', to_char(p.updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		       ),
		       $1, 'PENDING', CURRENT_TIMESTAMP
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN people p ON p.company_id = s.company_id
		 WHERE d.id = $2
		   AND p.deleted_at IS NULL
		   AND `+deviceIsSyncable+`
		   AND `+rosterMembershipPredicate+`
		   AND NOT EXISTS (
		        SELECT 1 FROM sync_jobs j
		         WHERE j.device_id = d.id
		           AND j.entity_type = 'PERSON'
		           AND j.entity_id = p.id
		           AND j.job_type <> 'DELETE'
		           AND j.status IN ('PENDING', 'DELIVERED')
		   )`,
		models.SyncProtocolVersion, deviceID)
	if err != nil {
		return 0, 0, err
	}
	addedRows, err := addResult.RowsAffected()
	if err != nil {
		return 0, 0, err
	}

	// Removed: people the terminal was told about who no longer belong.
	//
	// Bounded to people the terminal has actually been sent, so a reconcile does
	// not queue a DELETE for everybody in the company who was never on it.
	removeResult, err := DB.Exec(`
		INSERT INTO sync_jobs
		    (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
		     payload, protocol_version, status, next_attempt_at)
		SELECT d.site_id, d.id, 'DELETE', 'PERSON', p.id, p.external_id,
		       jsonb_build_object(
		           'member_id', p.external_id,
		           'full_name', p.full_name,
		           'membership_type', p.membership_type,
		           'active', p.active,
		           'fingerprint_template', '',
		           'deleted', TRUE,
		           'updated_at', to_char(p.updated_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		       ),
		       $1, 'PENDING', CURRENT_TIMESTAMP
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN people p ON p.company_id = s.company_id
		 WHERE d.id = $2
		   AND `+deviceIsSyncable+`
		   AND NOT (`+rosterMembershipPredicate+`)
		   AND EXISTS (
		        SELECT 1 FROM sync_jobs j
		         WHERE j.device_id = d.id
		           AND j.entity_type = 'PERSON'
		           AND j.entity_id = p.id
		           AND j.job_type <> 'DELETE'
		   )
		   AND NOT EXISTS (
		        SELECT 1 FROM sync_jobs j
		         WHERE j.device_id = d.id
		           AND j.entity_type = 'PERSON'
		           AND j.entity_id = p.id
		           AND j.job_type = 'DELETE'
		           AND j.status IN ('PENDING', 'DELIVERED')
		   )`,
		models.SyncProtocolVersion, deviceID)
	if err != nil {
		return int(addedRows), 0, err
	}
	removedRows, err := removeResult.RowsAffected()
	if err != nil {
		return int(addedRows), 0, err
	}

	return int(addedRows), int(removedRows), nil
}

// ReconcileCompanyRosters reconciles every syncable terminal in one company.
//
// Returns the number of terminals that actually changed, which is what an
// operator or a scheduled task wants to see -- "reconciled 40 terminals, 2
// changed" is actionable; "reconciled 40 terminals" is not.
func ReconcileCompanyRosters(companyID int64) (terminals, added, removed int, err error) {
	rows, err := DB.Query(`
		SELECT d.id
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE s.company_id = $1
		   AND s.deleted_at IS NULL
		   AND `+deviceIsSyncable, companyID)
	if err != nil {
		return 0, 0, 0, err
	}

	var deviceIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		deviceIDs = append(deviceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	// The cursor is drained before any reconciliation runs, because each
	// reconcile writes to sync_jobs and holding a read cursor open across those
	// writes is how a long transaction turns into a lock nobody expected.
	for _, deviceID := range deviceIDs {
		a, r, err := ReconcileDeviceRoster(deviceID)
		if err != nil {
			return terminals, added, removed, err
		}
		if a > 0 || r > 0 {
			terminals++
		}
		added += a
		removed += r
	}
	return terminals, added, removed, nil
}

// ---------------------------------------------------------------------------
// What a new person is allowed (018_default_person_access.sql)
// ---------------------------------------------------------------------------

// grantDefaultPersonAccessTx writes the permission a company's policy says a
// newly created person should start with.
//
// WHY THIS EXISTS. 014 established deny-by-default and back-filled existing
// people so no deployed door stopped working. Without this, the same lockout
// would simply arrive later: a deployed installation adding somebody through the
// legacy /api/v1/members surface would find them reaching no terminal, and would
// discover it at a door rather than at an upgrade.
//
// The policy is per company because two customers want opposite things and both
// are right -- see the migration, where the reasoning is written out. Existing
// tenants were migrated to COMPANY_ALLOW; new tenants are created with NONE.
//
// WHAT THIS IS NOT: a special case in the evaluator. It writes a REAL permission
// row that an operator can see and remove. The engine's default is still denial;
// this only decides what rule is written when somebody is added.
func grantDefaultPersonAccessTx(tx *sql.Tx, companyID, personID int64) error {
	// One statement rather than a read followed by a conditional write: the
	// SELECT supplies the row only when the policy says to, so a company set to
	// NONE inserts nothing and there is no branch in Go that could disagree with
	// the column.
	_, err := tx.Exec(`
		INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
		SELECT c.id, $2, 'COMPANY', 'ALLOW', TRUE
		  FROM companies c
		 WHERE c.id = $1
		   AND c.default_person_access = 'COMPANY_ALLOW'
		ON CONFLICT (person_id, effect, COALESCE(application, ''))
		     WHERE scope_type = 'COMPANY' AND deleted_at IS NULL
		DO NOTHING`, companyID, personID)
	return err
}

// ReconcileStaleRosters reconciles every syncable terminal on the installation.
//
// The scheduled form of ReconcileDeviceRoster, run by maintenance/tasks.go. It
// is what makes a validity window mean something: roster membership changes with
// the clock, and nothing edits a row when a permission expires, so without a
// periodic pass "access expires on Friday" is a promise the platform makes and
// does not keep at an offline terminal.
//
// Bounded by the caller's context so a shutdown does not wait for a fleet-wide
// pass to finish, and so a slow pass cannot overrun the next one indefinitely.
func ReconcileStaleRosters(ctx context.Context) (terminals, added, removed int, err error) {
	rows, err := DB.QueryContext(ctx, `
		SELECT d.id
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN companies c ON c.id = s.company_id
		 WHERE s.deleted_at IS NULL
		   AND c.active
		   AND `+deviceIsSyncable+`
		 ORDER BY d.id`)
	if err != nil {
		return 0, 0, 0, err
	}

	var deviceIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		deviceIDs = append(deviceIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	for _, deviceID := range deviceIDs {
		// Checked between terminals rather than mid-reconcile: each reconcile is
		// two statements and stopping between them would leave a terminal with
		// additions queued and removals not, which is worse than finishing it.
		if err := ctx.Err(); err != nil {
			return terminals, added, removed, nil
		}

		a, r, err := ReconcileDeviceRoster(deviceID)
		if err != nil {
			// One unhealthy terminal must not stop the pass. The error is
			// returned once the rest have been attempted, so the scheduler
			// still reports it.
			log.Printf("roster reconcile failed for device %d: %v", deviceID, err)
			continue
		}
		if a > 0 || r > 0 {
			terminals++
		}
		added += a
		removed += r
	}
	return terminals, added, removed, nil
}
