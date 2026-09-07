package database

import (
	"database/sql"
	"errors"
	"strings"

	"access-terminal-cloud-api/models"
)

// Quarantine: excluding one terminal from roster-driven change without taking
// it out of service.
//
// WHAT A QUARANTINED TERMINAL STILL DOES: admits people, heartbeats, uploads
// its access log, answers diagnostics, accepts firmware updates. Everything a
// door is for.
//
// WHAT STOPS: roster reconciliation, and the CREATE / UPDATE / DELETE /
// FULL_SYNC jobs it generates. Those are the jobs that reshape a terminal's
// member table -- and a DELETE is applied by removing the member row AND
// erasing that person's template from the sensor, which makes reconciliation a
// destructive operation as far as stored credentials are concerned.
//
// THE INCIDENT. A bench terminal was holding two templates that were the only
// evidence of a live defect. Bringing the API up so the terminal could drain
// its queued access events also woke the 15-minute reconciler, which queued
// DELETE jobs for both people within fifteen minutes. Delivering them would
// have erased the evidence. The reconciler was doing exactly its job; there was
// simply no way to say "leave this one alone", and the only lever available --
// disabling the device -- would have shut the door.
//
// Pausing is always reversible and never loses anything: the next reconcile
// after a resume converges the terminal. See migration 029.

// PauseDeviceSync quarantines one terminal. Idempotent: pausing an
// already-paused terminal refreshes who asked and why, and does not move the
// original timestamp -- the answer to "how long has this been paused" must not
// reset every time somebody re-runs the command.
func PauseDeviceSync(deviceID int64, pausedBy, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		// REQUIRED, deliberately. A terminal found paused weeks later is a
		// puzzle unless the pause says what it was for, and the person who can
		// answer that is the one running this now.
		return errors.New("a quarantine reason is required")
	}

	result, err := DB.Exec(`
		UPDATE devices
		   SET sync_paused_at    = COALESCE(sync_paused_at, NOW()),
		       sync_paused_by    = $2,
		       sync_pause_reason = $3,
		       updated_at        = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`,
		deviceID, strings.TrimSpace(pausedBy), reason)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return models.ErrDeviceNotFound
	}
	return nil
}

// ResumeDeviceSync lifts the quarantine. The terminal rejoins reconciliation on
// the next pass, which converges whatever changed while it was paused.
//
// NOTHING IS REPLAYED AND NOTHING IS OWED. Reconciliation is a convergence, not
// a journal: it queues what is missing and removes what should not be there,
// against the roster as it stands now. That is why pausing is safe -- and it is
// also why resuming a terminal whose people were deleted meanwhile WILL queue
// those deletions. Reconcile the records before resuming, not after.
func ResumeDeviceSync(deviceID int64) error {
	result, err := DB.Exec(`
		UPDATE devices
		   SET sync_paused_at    = NULL,
		       sync_paused_by    = NULL,
		       sync_pause_reason = NULL,
		       updated_at        = NOW()
		 WHERE id = $1 AND deleted_at IS NULL`, deviceID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return models.ErrDeviceNotFound
	}
	return nil
}

// DeviceSyncQuarantine is what an operator sees: whether this terminal is
// paused, and if so since when, by whom and why.
type DeviceSyncQuarantine struct {
	Paused bool
	Since  sql.NullTime
	By     sql.NullString
	Reason sql.NullString
}

// DeviceSyncPaused reports one terminal's quarantine state.
func DeviceSyncPaused(deviceID int64) (DeviceSyncQuarantine, error) {
	var out DeviceSyncQuarantine
	err := DB.QueryRow(`
		SELECT sync_paused_at, sync_paused_by, sync_pause_reason
		  FROM devices
		 WHERE id = $1 AND deleted_at IS NULL`, deviceID).
		Scan(&out.Since, &out.By, &out.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return out, models.ErrDeviceNotFound
	}
	if err != nil {
		return out, err
	}
	out.Paused = out.Since.Valid
	return out, nil
}
