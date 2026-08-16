package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"access-terminal-cloud-api/models"
)

// Synchronization engine (migrations/003_sync_engine.sql).
//
// sync_jobs is a per-device outbox. Enqueueing happens inside the same
// transaction as the write it describes, so a change and the record of its
// delivery commit together -- there is no window where a person is created but
// no device is told about it.
//
// The lifecycle of a job is:
//
//	enqueue  -> PENDING (next_attempt_at = now)
//	fetch    -> PENDING (lease taken: next_attempt_at pushed forward)
//	ack ok   -> COMPLETED (acknowledged_at set)
//	ack fail -> PENDING with backoff, or FAILED once max_attempts is spent
//
// Fetching deliberately does not change status. A job is only ever retired by
// an acknowledgement, so a device that dies mid-apply gets it again when the
// lease expires.

// deliveryLease is how long a fetched job stays hidden from subsequent fetches
// before it is assumed lost and redelivered.
const deliveryLease = 60 * time.Second

// backoffFor returns the delay before a failed job is offered again.
// Exponential, capped, so a permanently broken job does not spin.
func backoffFor(attempts int) time.Duration {
	backoff := time.Duration(1<<uint(attempts)) * time.Second
	if backoff > 15*time.Minute {
		backoff = 15 * time.Minute
	}
	return backoff
}

// personSyncPayload builds the snapshot a terminal applies for a person change
func personSyncPayload(member *models.Member, deleted bool) ([]byte, error) {
	payload := models.PersonSyncPayload{
		MemberID:            member.MemberID,
		FullName:            member.FullName,
		MembershipType:      member.MembershipType,
		Active:              member.Active,
		FingerprintTemplate: member.FingerprintTemplate,
		Deleted:             deleted,
		UpdatedAt:           member.UpdatedAt,
	}
	return json.Marshal(payload)
}

// enqueuePersonChangeTx fans a person change out to every device in the company
// that is expected to hold member data. Runs on the caller's transaction.
//
// A company with no devices simply produces no jobs; a device registered later
// is brought up to date by EnqueueBootstrapJobs instead of replaying history.
func enqueuePersonChangeTx(tx *sql.Tx, companyID int64, jobType string, member *models.Member, deleted bool) error {
	payload, err := personSyncPayload(member, deleted)
	if err != nil {
		return fmt.Errorf("building sync payload: %w", err)
	}

	// SCOPED BY PERMISSION, not by company (SEC-04). `p` and `d` are the
	// aliases rosterMembershipPredicate expects; see database/roster.go for what
	// the rule is and why DENY is only subtracted when it is unconditional.
	//
	// A DELETE is exempt from the predicate. A person being removed, deactivated
	// or losing their permission must be withdrawn from terminals that were
	// holding them -- and by the time this runs, the rule that put them there
	// may already be gone, so testing membership would skip exactly the
	// terminals that need telling.
	membership := rosterMembershipPredicate
	if jobType == "DELETE" || deleted {
		membership = "TRUE"
	}

	query := `INSERT INTO sync_jobs
	          (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
	           payload, protocol_version, status, next_attempt_at)
	          SELECT d.site_id, d.id, $1, 'PERSON', $2, $3, $4::jsonb, $5, 'PENDING', CURRENT_TIMESTAMP
	            FROM devices d
	            JOIN sites s ON s.id = d.site_id
	            JOIN people p ON p.id = $2
	           WHERE s.company_id = $6
	             AND s.deleted_at IS NULL
	             AND ` + deviceIsSyncable + `
	             AND (` + membership + `)`

	_, err = tx.Exec(query, jobType, member.ID, member.MemberID, payload,
		models.SyncProtocolVersion, companyID)
	if err != nil {
		return fmt.Errorf("enqueueing %s job: %w", jobType, err)
	}
	return nil
}

// EnqueueSettingsJob queues a settings push for a single device.
func EnqueueSettingsJob(deviceID int64, settings any) error {
	payload, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("building settings payload: %w", err)
	}

	query := `INSERT INTO sync_jobs
	          (site_id, device_id, job_type, entity_type, payload, protocol_version, status)
	          SELECT d.site_id, d.id, 'SETTINGS', 'SETTINGS', $1::jsonb, $2, 'PENDING'
	            FROM devices d WHERE d.id = $3 AND d.deleted_at IS NULL`

	_, err = DB.Exec(query, payload, models.SyncProtocolVersion, deviceID)
	return err
}

// enqueueSettingsFanoutTx pushes a site's settings to every device at that site.
// Runs on the caller's transaction so the settings write and its delivery
// records commit together, exactly as person changes do.
func enqueueSettingsFanoutTx(tx *sql.Tx, siteID int64, payload []byte) error {
	query := `INSERT INTO sync_jobs
	          (site_id, device_id, job_type, entity_type, payload, protocol_version, status, next_attempt_at)
	          SELECT d.site_id, d.id, 'SETTINGS', 'SETTINGS', $1::jsonb, $2, 'PENDING', CURRENT_TIMESTAMP
	            FROM devices d
	           WHERE d.site_id = $3
	             AND d.active = TRUE
	             AND d.deleted_at IS NULL
	             AND d.status <> 'DISABLED'`

	if _, err := tx.Exec(query, payload, models.SyncProtocolVersion, siteID); err != nil {
		return fmt.Errorf("enqueueing SETTINGS job: %w", err)
	}
	return nil
}

// GetSiteSettings returns a site's current settings, their version, and the
// offline policy actually in force.
//
// The policy columns are read here rather than left to the caller so that every
// surface showing a site's settings shows the same thing the terminals at that
// site are running. Reading the blob alone is what made an inert
// `offline_grace_minutes` in the free-form object look real.
func GetSiteSettings(siteID int64) (*models.SiteSettings, error) {
	var s models.SiteSettings
	var payload []byte
	err := DB.QueryRow(
		`SELECT settings, settings_version, offline_policy, offline_grace_minutes
		   FROM sites WHERE id = $1 AND deleted_at IS NULL`,
		siteID).Scan(&payload, &s.Version, &s.OfflinePolicy, &s.OfflineGraceMinutes)
	if err != nil {
		return nil, err
	}
	s.Settings = payload
	return &s, nil
}

// UpdateSiteSettings replaces a site's settings, bumps the version, and fans a
// SETTINGS job out to every device at the site -- all in one transaction.
func UpdateSiteSettings(siteID int64, settings json.RawMessage) (*models.SiteSettings, error) {
	// F3. The two policy keys are REFUSED in the free-form object rather than
	// silently dropped, and the reason is that dropping them is what produced
	// the trap: the write succeeded, the read showed the value back, and the
	// merge overwrote it before it ever reached a terminal. An operator who set
	// a grace window this way was told it had taken effect and it had not.
	//
	// The check is here, at the store, so both mountings of the handler and any
	// future caller get it -- the console route and the site-key route are the
	// same code, but that is a fact about today's routing table.
	if err := models.RejectReservedSettingsKeys(settings); err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var result models.SiteSettings
	var stored []byte
	err = tx.QueryRow(
		`UPDATE sites
		    SET settings = $1::jsonb,
		        settings_version = settings_version + 1,
		        settings_updated_at = CURRENT_TIMESTAMP
		  WHERE id = $2 AND deleted_at IS NULL
		  RETURNING settings, settings_version, offline_policy, offline_grace_minutes`,
		[]byte(settings), siteID).
		Scan(&stored, &result.Version, &result.OfflinePolicy, &result.OfflineGraceMinutes)
	if err != nil {
		return nil, err
	}
	result.Settings = stored

	// The job payload carries the version so a device can discard a stale
	// settings push it receives after a newer one.
	//
	// Built by deviceSettingsPayloadTx rather than from `stored` directly, so
	// the validated offline-policy columns are layered over the free-form blob
	// exactly as GET /devices/settings does it. Marshalling `stored` here --
	// which is what this did -- meant an operator editing the settings object
	// pushed a payload with NO offline policy in it, and a terminal that had
	// been told DENY_ALL would keep the policy only because absent keys are
	// treated as "leave it alone". The two paths must not be able to describe
	// different configurations.
	payload, _, err := deviceSettingsPayloadTx(tx, siteID)
	if err != nil {
		return nil, err
	}

	if err := enqueueSettingsFanoutTx(tx, siteID, payload); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &result, nil
}

// EnqueueBootstrapJobs seeds a device with the company's current live members
// as CREATE jobs. This is how a newly registered -- or long-absent -- device
// converges without replaying every historical change.
//
// Soft-deleted people are intentionally excluded: a device that never knew
// about them has nothing to delete.
func EnqueueBootstrapJobs(deviceID int64) (int, error) {
	return enqueueBootstrapJobs(DB, deviceID)
}

// enqueueBootstrapJobs is the same work against an arbitrary executor, so
// registration can seed the device inside the transaction that issued its
// credential. See the note in RegisterDevice about why that has to be atomic.
func enqueueBootstrapJobs(db execer, deviceID int64) (int, error) {
	query := `INSERT INTO sync_jobs
	          (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
	           payload, protocol_version, status)
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
	                 $1, 'PENDING'
	            FROM devices d
	            JOIN sites s ON s.id = d.site_id
	            JOIN people p ON p.company_id = s.company_id
	           WHERE d.id = $2
	             AND d.deleted_at IS NULL
	             AND p.deleted_at IS NULL
	             -- SEC-04: a newly registered terminal is seeded with the people
	             -- its permissions cover, not with the company's whole roster.
	             AND ` + rosterMembershipPredicate

	result, err := db.Exec(query, models.SyncProtocolVersion, deviceID)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

// Device lookup

// GetDeviceBySerial retrieves an active device belonging to a given site
func GetDeviceBySerial(siteID int64, serial string) (*models.Device, error) {
	var d models.Device
	query := `SELECT id, public_id, site_id, serial_number, device_name, device_type, status, active
	          FROM devices
	          WHERE serial_number = $1 AND site_id = $2 AND deleted_at IS NULL`

	err := DB.QueryRow(query, serial, siteID).Scan(
		&d.ID, &d.PublicID, &d.SiteID, &d.SerialNumber, &d.DeviceName,
		&d.DeviceType, &d.Status, &d.Active,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Job delivery

// GetPendingJobsForDevice returns a device's due work, oldest first, and takes a
// delivery lease on each row returned.
//
// The status is left PENDING on purpose: only an acknowledgement retires a job.
// Rows are locked FOR UPDATE SKIP LOCKED so two concurrent polls from the same
// device cannot hand out the same job twice.
func GetPendingJobsForDevice(deviceID int64, limit int) ([]models.SyncJob, error) {
	query := `WITH due AS (
	              SELECT id FROM sync_jobs
	               WHERE device_id = $1
	                 AND status = 'PENDING'
	                 AND next_attempt_at <= CURRENT_TIMESTAMP
	               ORDER BY id ASC
	               LIMIT $2
	               FOR UPDATE SKIP LOCKED
	          )
	          UPDATE sync_jobs sj
	             SET next_attempt_at = CURRENT_TIMESTAMP + $3::interval,
	                 last_attempt_at = CURRENT_TIMESTAMP,
	                 started_at = COALESCE(sj.started_at, CURRENT_TIMESTAMP)
	            FROM due
	           WHERE sj.id = due.id
	       RETURNING sj.id, sj.public_id, sj.protocol_version, sj.job_type,
	                 COALESCE(sj.entity_type, ''), COALESCE(sj.entity_external_id, ''),
	                 sj.payload, sj.attempts, sj.created_at`

	lease := fmt.Sprintf("%d seconds", int(deliveryLease.Seconds()))
	rows, err := DB.Query(query, deviceID, limit, lease)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []models.SyncJob
	for rows.Next() {
		var job models.SyncJob
		var payload []byte
		err := rows.Scan(
			&job.ID, &job.PublicID, &job.ProtocolVersion, &job.JobType,
			&job.EntityType, &job.EntityExternalID, &payload, &job.Attempts, &job.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		job.Payload = payload
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// AckJobCompleted marks a job applied. Acknowledging an already-completed job is
// a no-op that still reports success, so a device may safely retry an ack whose
// response it never received.
//
// Returns false only when the job does not exist or belongs to another device.
func AckJobCompleted(deviceID, jobID int64) (bool, error) {
	query := `UPDATE sync_jobs
	             SET status = 'COMPLETED',
	                 acknowledged_at = COALESCE(acknowledged_at, CURRENT_TIMESTAMP),
	                 completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP),
	                 error_message = NULL
	           WHERE id = $1 AND device_id = $2
	             AND status <> 'CANCELLED'`

	result, err := DB.Exec(query, jobID, deviceID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		// A completed job means sync made forward progress, which is what
		// last_sync_at reports on the dashboard.
		if _, err := DB.Exec(
			`UPDATE devices SET last_sync_at = CURRENT_TIMESTAMP WHERE id = $1`,
			deviceID); err != nil {
			return false, err
		}
		return true, nil
	}

	// Nothing updated: either it is already cancelled, or it is not this
	// device's job. Distinguish so the caller can answer 404 vs 200.
	var exists bool
	err = DB.QueryRow(`SELECT EXISTS (SELECT 1 FROM sync_jobs WHERE id = $1 AND device_id = $2)`,
		jobID, deviceID).Scan(&exists)
	return exists, err
}

// AckJobFailed records a failed apply and schedules a retry. The job stays
// PENDING so it is redelivered; once max_attempts is spent it is parked in
// FAILED rather than retried forever.
//
// Only a job that is still in flight is retried. A job that has already been
// retired is left exactly as it is, because a late failure report for one is a
// stale retransmission rather than new information:
//
//   - COMPLETED means the device already told us it applied the job. Reopening
//     it would undo an acknowledgement, and because the schema enforces
//     `(status = 'COMPLETED') = (acknowledged_at IS NOT NULL)`, the write would
//     fail the constraint and surface to the device as a 500 it retries forever.
//   - CANCELLED means a snapshot superseded the job. Reopening it puts work the
//     snapshot deliberately replaced back into the queue, so a device that was
//     compacted mid-batch would re-apply pre-snapshot state -- including
//     recreating people who were deleted before the snapshot was taken.
//
// Both cases report success: the device has nothing useful to do with an error,
// and its report has been received and correctly ignored.
func AckJobFailed(deviceID, jobID int64, reason string) (bool, error) {
	var status string
	var attempts, maxAttempts int
	err := DB.QueryRow(
		`SELECT status, attempts, max_attempts FROM sync_jobs WHERE id = $1 AND device_id = $2`,
		jobID, deviceID).Scan(&status, &attempts, &maxAttempts)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if status != "PENDING" && status != "FAILED" {
		return true, nil
	}

	attempts++
	next := "PENDING"
	if attempts >= maxAttempts {
		next = "FAILED"
	}

	// The status guard is repeated in the UPDATE so a job retired between the
	// read above and this write is still not reopened.
	backoff := fmt.Sprintf("%d seconds", int(backoffFor(attempts).Seconds()))
	_, err = DB.Exec(`UPDATE sync_jobs
	                     SET attempts = $1,
	                         status = $2,
	                         error_message = $3,
	                         last_attempt_at = CURRENT_TIMESTAMP,
	                         next_attempt_at = CURRENT_TIMESTAMP + $4::interval
	                   WHERE id = $5 AND device_id = $6
	                     AND status IN ('PENDING', 'FAILED')`,
		attempts, next, reason, backoff, jobID, deviceID)
	if err != nil {
		return false, err
	}
	return true, nil
}

// Backlog compaction (Sprint 6)
//
// A device offline for a long time accumulates one job per change. Replaying
// ten thousand edits to reach a state describable in two hundred rows is waste,
// and on a constrained terminal it is a very long recovery.
//
// Compaction cancels the outstanding queue and replaces it with a snapshot.
//
// The snapshot is deliberately a *reconciliation*, not a wipe. A FULL_SYNC job
// carries the authoritative roster of member IDs and means "your local set
// should be exactly this; remove anything else". It does not carry templates,
// so it stays small -- a thousand members is roughly ten kilobytes.
//
// This matters for correctness, not just size. The obvious design -- a "wipe"
// marker followed by CREATE jobs -- loses data: if the wipe's acknowledgement
// is lost after the CREATEs were already applied and acked, redelivery wipes the
// device and nothing repopulates it, because those CREATEs are gone. Framing the
// job as a set-difference makes redelivery a no-op once converged.

// defaultCompactionThreshold is the unacknowledged backlog at which a device's
// queue is collapsed into a snapshot.
const defaultCompactionThreshold = 500

// compactionThreshold reads the threshold from the environment, falling back to
// the default. Configurable so it can be tuned per deployment.
func compactionThreshold() int {
	if raw := os.Getenv("SYNC_COMPACTION_THRESHOLD"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultCompactionThreshold
}

// CompactDeviceBacklog replaces a device's outstanding queue with a snapshot of
// current state. Returns how many queued jobs were superseded.
func CompactDeviceBacklog(deviceID int64) (int, error) {
	tx, err := DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	superseded, err := compactDeviceBacklogTx(tx, deviceID, "superseded by full sync")
	if err != nil {
		// The refusal is recorded after the rollback, not inside it. See
		// RecordRosterOverflow: a trace written on the transaction that was
		// abandoned would be abandoned with it, and the trace is the point.
		var overflow *RosterCapacityError
		if errors.As(err, &overflow) {
			tx.Rollback()
			if recordErr := RecordRosterOverflow(deviceID, overflow.RosterSize,
				overflow.Capacity); recordErr != nil {
				log.Printf("recording roster overflow for device %d: %v", deviceID, recordErr)
			}
		}
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// It fit. A terminal that was over capacity and no longer is must stop being
	// reported as over capacity.
	if err := ClearRosterOverflow(deviceID); err != nil {
		log.Printf("clearing roster overflow for device %d: %v", deviceID, err)
	}
	return superseded, nil
}

// compactDeviceBacklogTx is the whole of compaction, on the caller's
// transaction.
//
// Factored out for RELOCATION (MoveTerminal), which has to move a terminal
// between sites and rebuild its state ATOMICALLY. A committed move whose
// snapshot failed to enqueue would leave a terminal pointing at its new site
// with an empty queue -- still holding the OLD site's people, and with nothing
// durable scheduled to say otherwise.
//
// `reason` is recorded on the cancelled jobs. Compaction and relocation both
// supersede work, but an operator reading a cancelled queue should be able to
// tell "your backlog was collapsed" from "this terminal was moved".
func compactDeviceBacklogTx(tx *sql.Tx, deviceID int64, reason string) (int, error) {
	// FW-01, and it is checked BEFORE anything is cancelled.
	//
	// Order is the whole point. Cancelling the queue and then discovering the
	// replacement snapshot cannot be applied would leave the terminal with no
	// queued work AND a roster it can never be brought in line with -- strictly
	// worse than the backlog it started with. Refusing first leaves the existing
	// queue exactly as it was.
	if err := guardRosterCapacityTx(tx, deviceID); err != nil {
		return 0, err
	}

	// Retire whatever was queued. These are superseded, not applied, so they
	// are CANCELLED rather than COMPLETED -- acknowledged_at stays null and the
	// "only acked jobs are complete" invariant holds.
	result, err := tx.Exec(`
		UPDATE sync_jobs
		   SET status = 'CANCELLED',
		       error_message = $2
		 WHERE device_id = $1
		   AND acknowledged_at IS NULL
		   AND status IN ('PENDING', 'FAILED')`, deviceID, reason)
	if err != nil {
		return 0, err
	}
	superseded, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	// The authoritative roster: IDs only, so this stays small.
	_, err = tx.Exec(`
		INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type,
		                       payload, protocol_version, status)
		SELECT d.site_id, d.id, 'FULL_SYNC', 'ROSTER',
		       jsonb_build_object(
		           'snapshot', TRUE,
		           'count', COALESCE(roster.n, 0),
		           'member_ids', COALESCE(roster.ids, '[]'::jsonb)
		       ),
		       $2, 'PENDING'
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  LEFT JOIN LATERAL (
		       -- SCOPED BY PERMISSION (SEC-04). The snapshot is the AUTHORITATIVE
		       -- roster, so listing the whole company here would make the terminal
		       -- immediately re-add everybody the change fan-out correctly withheld
		       -- -- the recovery path would silently undo the scoping.
		       SELECT count(*) AS n,
		              jsonb_agg(p.external_id ORDER BY p.external_id) AS ids
		         FROM people p
		        WHERE p.company_id = s.company_id
		          AND p.deleted_at IS NULL
		          AND `+rosterMembershipPredicate+`
		  ) roster ON TRUE
		 WHERE d.id = $1 AND d.deleted_at IS NULL`,
		deviceID, models.SyncProtocolVersion)
	if err != nil {
		return 0, fmt.Errorf("enqueueing roster snapshot: %w", err)
	}

	// Then the full records, so the device can fill in anything it is missing.
	_, err = tx.Exec(`
		INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type, entity_id,
		                       entity_external_id, payload, protocol_version, status)
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
		       $2, 'PENDING'
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN people p ON p.company_id = s.company_id
		 WHERE d.id = $1 AND d.deleted_at IS NULL AND p.deleted_at IS NULL
		   AND `+rosterMembershipPredicate,
		deviceID, models.SyncProtocolVersion)
	if err != nil {
		return 0, fmt.Errorf("enqueueing snapshot records: %w", err)
	}

	// Current settings, since the queued SETTINGS job was just cancelled
	_, err = tx.Exec(`
		INSERT INTO sync_jobs (site_id, device_id, job_type, entity_type,
		                       payload, protocol_version, status)
		SELECT d.site_id, d.id, 'SETTINGS', 'SETTINGS',
		       jsonb_build_object(
		           'settings_version', s.settings_version,
		           -- The SAME merge mergeDeviceSettings performs in Go: the
		           -- validated policy columns layered OVER the free-form blob,
		           -- with a non-object blob treated as empty rather than
		           -- failing the whole snapshot. Concatenation takes the
		           -- right-hand side on a key collision, which is the column
		           -- precedence the policy needs -- an operator must not be able
		           -- to override a safety control by writing raw JSON.
		           'settings',
		           (CASE WHEN jsonb_typeof(s.settings) = 'object'
		                 THEN s.settings ELSE '{}'::jsonb END)
		           || jsonb_build_object(
		                  'offline_policy', s.offline_policy,
		                  'offline_grace_minutes', s.offline_grace_minutes)
		       ),
		       $2, 'PENDING'
		  FROM devices d JOIN sites s ON s.id = d.site_id
		 WHERE d.id = $1 AND d.deleted_at IS NULL`,
		deviceID, models.SyncProtocolVersion)
	if err != nil {
		return 0, fmt.Errorf("enqueueing snapshot settings: %w", err)
	}

	if _, err = tx.Exec(
		`UPDATE devices SET last_compacted_at = CURRENT_TIMESTAMP WHERE id = $1`,
		deviceID); err != nil {
		return 0, err
	}

	return int(superseded), nil
}

// FetchDeviceWork returns a device's due jobs, compacting its backlog first if
// it has fallen too far behind. Reports whether a snapshot was taken so the
// device can be told its queue was replaced.
func FetchDeviceWork(deviceID int64, limit int) ([]models.SyncJob, bool, error) {
	backlog, err := GetDeviceSyncBacklog(deviceID)
	if err != nil {
		return nil, false, err
	}

	compacted := false
	if backlog > compactionThreshold() {
		switch _, err := CompactDeviceBacklog(deviceID); {
		case err == nil:
			compacted = true

		case errors.Is(err, ErrRosterExceedsCapacity):
			// THE POLL STILL SUCCEEDS. This terminal cannot be given an
			// authoritative roster, which CompactDeviceBacklog has already
			// recorded where an operator will see it -- but it can still be
			// given the individual jobs already queued for it, and failing its
			// poll would take away the work it CAN do on top of the work it
			// cannot. A door that is behind is better than a door that is also
			// no longer talking to the platform.
			log.Printf("device %d: backlog compaction withheld: %v", deviceID, err)

		default:
			return nil, false, err
		}
	}

	jobs, err := GetPendingJobsForDevice(deviceID, limit)
	return jobs, compacted, err
}

// GetDeviceSyncBacklog reports how much work a device still owes.
//
// Only PENDING and FAILED count. CANCELLED jobs are unacknowledged but were
// superseded by a snapshot, so counting them would both overstate the backlog to
// the device and -- because compaction itself creates cancelled rows -- let a
// compacted device immediately re-cross the compaction threshold, compacting
// forever.
func GetDeviceSyncBacklog(deviceID int64) (int, error) {
	var pending int
	err := DB.QueryRow(
		`SELECT count(*) FROM sync_jobs
		  WHERE device_id = $1 AND status IN ('PENDING', 'FAILED')`,
		deviceID).Scan(&pending)
	return pending, err
}
