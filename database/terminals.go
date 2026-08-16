package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"access-terminal-cloud-api/models"
)

// Terminal lifecycle (migrations/016_terminal_lifecycle.sql).
//
// Everything an operator can do to one terminal, and the reason each exists.
// Before this file the answer was "nothing": DISABLED was written in exactly one
// place in the codebase -- RetireSite, cascading when a whole location closed --
// so the only lever for a stolen unit was retiring its entire site, which stops
// every other door there.
//
// THE THREE OPERATIONS ARE NOT INTERCHANGEABLE, and conflating them is the
// mistake this file exists to prevent:
//
//	Disable  reversible. The credential still resolves; device auth refuses it
//	         on status. "This terminal is faulty, take it out of service."
//
//	Revoke   irreversible for that credential. The hash is CLEARED, so the key
//	         stops resolving at the index probe rather than at a check above it.
//	         "This terminal is stolen" -- where the question is whether the
//	         SECRET is trusted, and the answer must hold even if somebody
//	         re-enables the row afterwards.
//
//	Retire   soft delete, revoking on the way out. "This unit is gone." A
//	         retired row that still authenticates is precisely the orphaned
//	         hardware RetireSite's comment warns about.
//
// EVERY FUNCTION HERE IS COMPANY-SCOPED BY JOIN, never by trusting a caller's
// argument. Devices carry no company_id of their own -- they reach one through
// sites -- so the join is the tenancy filter, and a serial belonging to another
// tenant is reported as not found rather than as forbidden.

// TerminalLifecycleResult reports what an operation actually did, so a caller
// can tell an operator rather than leaving them to infer it.
type TerminalLifecycleResult struct {
	SerialNumber string
	Status       string
	Active       bool

	// CredentialCleared is true when the device's key was invalidated. The
	// terminal cannot authenticate again until it re-registers.
	CredentialCleared bool

	// PendingJobsCancelled is how much queued work was discarded. Retiring a
	// terminal with a backlog silently dropping it would leave an operator
	// believing those changes had been delivered.
	PendingJobsCancelled int64

	// RosterResynced reports that a full-sync snapshot was queued in the same
	// transaction, which is what makes a relocation take effect at the door
	// rather than at the next reconciler pass.
	//
	// Reported so the console can say "moved, and its roster is being rebuilt"
	// instead of "moved" -- the difference is whether an operator should expect
	// the old site's people to still open it, and for how long.
	RosterResynced bool
}

// ErrTerminalAlreadyRetired reports an operation on a terminal that is gone.
// Distinguished from not-found so a repeated retire is idempotent rather than
// looking like a tenancy failure.
var ErrTerminalAlreadyRetired = errors.New("terminal is already retired")

// resolveTerminal finds a live terminal inside one company and returns its
// internal id.
//
// The single place the tenancy join is written. Every operation below goes
// through it, because a second copy of a tenancy filter is a second place for it
// to be relaxed.
func resolveTerminal(q rowQuerier, companyID int64, serial string) (int64, error) {
	if serial == "" {
		return 0, models.ErrDeviceNotFound
	}

	var id int64
	err := q.QueryRow(`
		SELECT d.id
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $1
		   AND s.company_id = $2
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, serial, companyID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, models.ErrDeviceNotFound
	}
	return id, err
}

// rowQuerier is *sql.DB or *sql.Tx, so resolveTerminal works inside a caller's
// transaction as well as on its own.
type rowQuerier interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

// SetTerminalDisabled takes a terminal out of service, or puts it back.
//
// REVERSIBLE, and deliberately does not touch the credential. A faulty terminal
// that is repaired should come back without being re-provisioned, and an
// operator disabling one to investigate should not be creating a site visit for
// themselves.
//
// The device auth middleware already refuses a DISABLED device, and
// RegisterDevice already refuses to bring one back -- so this is the control for
// enforcement that has existed since Sprint 6 and was unreachable.
func SetTerminalDisabled(companyID int64, serial string, disabled bool,
	reason string, actorUserID int64) (*TerminalLifecycleResult, error) {

	deviceID, err := resolveTerminal(DB, companyID, serial)
	if err != nil {
		return nil, err
	}

	// PROVISIONING is preserved rather than becoming OFFLINE: a device row that
	// has never registered has no credential, and calling it offline would put
	// it in the fleet's "unreachable" count alongside terminals that are
	// genuinely missing.
	var result TerminalLifecycleResult
	err = DB.QueryRow(`
		UPDATE devices
		   SET status = CASE
		           WHEN $2 THEN 'DISABLED'
		           WHEN status <> 'DISABLED' THEN status
		           WHEN registered_at IS NULL THEN 'PROVISIONING'
		           ELSE 'OFFLINE'
		       END,
		       active = NOT $2,
		       disabled_reason = CASE WHEN $2 THEN NULLIF($3, '') ELSE NULL END,
		       disabled_by = CASE WHEN $2 THEN NULLIF($4, 0)::bigint ELSE NULL END,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING serial_number, status, active`,
		deviceID, disabled, reason, actorUserID).
		Scan(&result.SerialNumber, &result.Status, &result.Active)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// RevokeTerminalCredential invalidates a terminal's device key.
//
// THE HASH IS CLEARED, NOT FLAGGED. AuthenticateDevice resolves a device by
// looking up api_key_hash; a status the lookup does not consult authorizes
// nothing, and a revocation that depends on a check somewhere above the lookup
// is a revocation that a future refactor can lose. After this the credential
// does not resolve at all.
//
// The terminal is also DISABLED, because a stolen unit should not quietly
// re-provision itself the moment somebody with the site key runs the
// registration call: registration refuses a DISABLED device, so the two
// together mean recovering the unit is a deliberate act.
//
// Queued work is cancelled. A revoked terminal will never apply it, and leaving
// it PENDING would keep the device's backlog climbing against a credential that
// no longer exists -- which then reads on the dashboard as a terminal that is
// merely behind.
func RevokeTerminalCredential(companyID int64, serial, reason string,
	actorUserID int64) (*TerminalLifecycleResult, error) {

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deviceID, err := resolveTerminal(tx, companyID, serial)
	if err != nil {
		return nil, err
	}

	var result TerminalLifecycleResult
	err = tx.QueryRow(`
		UPDATE devices
		   SET api_key_hash = NULL,
		       api_key_prefix = NULL,
		       credential_revoked_at = CURRENT_TIMESTAMP,
		       credential_revoked_reason = NULLIF($2, ''),
		       status = 'DISABLED',
		       active = FALSE,
		       disabled_reason = COALESCE(NULLIF($2, ''), 'credential revoked'),
		       disabled_by = NULLIF($3, 0)::bigint,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING serial_number, status, active`,
		deviceID, reason, actorUserID).
		Scan(&result.SerialNumber, &result.Status, &result.Active)
	if err != nil {
		return nil, err
	}

	cancelled, err := cancelQueuedWork(tx, deviceID, "terminal credential revoked")
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result.CredentialCleared = true
	result.PendingJobsCancelled = cancelled
	return &result, nil
}

// RetireTerminal soft-deletes a terminal.
//
// Revokes on the way out rather than leaving the credential live, because the
// row becomes invisible to every console query -- all of them join sites and
// filter deleted_at -- while the key would keep authenticating. That is exactly
// the orphaned hardware RetireSite's comment describes: a terminal no operator
// can see and nobody can revoke.
//
// One-way through the API, like site retirement. Disable is the reversible
// alternative and is what "we have taken it off the wall for a week" wants.
func RetireTerminal(companyID int64, serial, reason string,
	actorUserID int64) (*TerminalLifecycleResult, error) {

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deviceID, err := resolveTerminal(tx, companyID, serial)
	if err != nil {
		return nil, err
	}

	var result TerminalLifecycleResult
	err = tx.QueryRow(`
		UPDATE devices
		   SET deleted_at = CURRENT_TIMESTAMP,
		       retired_at = CURRENT_TIMESTAMP,
		       retired_reason = NULLIF($2, ''),
		       api_key_hash = NULL,
		       api_key_prefix = NULL,
		       credential_revoked_at = COALESCE(credential_revoked_at, CURRENT_TIMESTAMP),
		       credential_revoked_reason = COALESCE(credential_revoked_reason, 'terminal retired'),
		       status = 'DISABLED',
		       active = FALSE,
		       disabled_by = NULLIF($3, 0)::bigint,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING serial_number, status, active`,
		deviceID, reason, actorUserID).
		Scan(&result.SerialNumber, &result.Status, &result.Active)
	if err != nil {
		return nil, err
	}

	cancelled, err := cancelQueuedWork(tx, deviceID, "terminal retired")
	if err != nil {
		return nil, err
	}

	// Placements go with it. A credential recorded as living on a terminal that
	// no longer exists would make the distribution engine believe a person is
	// enrolled somewhere they cannot be -- and that belief is what decides
	// whether the credential gets sent anywhere else.
	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'REMOVED',
		       removed_at = CURRENT_TIMESTAMP,
		       last_error = 'terminal retired'
		 WHERE device_id = $1
		   AND state IN ('PENDING', 'PLACED', 'REMOVING')`, deviceID); err != nil {
		return nil, fmt.Errorf("clearing placements for retired terminal: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result.CredentialCleared = true
	result.PendingJobsCancelled = cancelled
	return &result, nil
}

// MoveTerminal reassigns a terminal to another site in the SAME company.
//
// Cross-company moves are not a thing that exists: a device reaches its company
// only through its site, so moving one across that boundary would silently
// transfer whatever roster it holds to a different tenant. The target site is
// resolved inside the caller's company, so naming another tenant's site is a
// not-found rather than a move.
//
// THE ROSTER IS REBUILT, NOT CARRIED. A terminal at the new site must hold the
// people permitted at the NEW site, and must forget the ones it held for the
// old. Queued work for the old site is cancelled and the device is re-seeded, so
// the move converges rather than leaving a terminal that opens for the previous
// location's staff.
func MoveTerminal(companyID int64, serial, targetSitePublicID string) (*TerminalLifecycleResult, error) {
	if !looksLikeUUID(targetSitePublicID) {
		return nil, models.ErrSiteNotFound
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	deviceID, err := resolveTerminal(tx, companyID, serial)
	if err != nil {
		return nil, err
	}

	var targetSiteID int64
	err = tx.QueryRow(`
		SELECT id FROM sites
		 WHERE public_id = $1 AND company_id = $2 AND deleted_at IS NULL`,
		targetSitePublicID, companyID).Scan(&targetSiteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}

	var result TerminalLifecycleResult
	err = tx.QueryRow(`
		UPDATE devices
		   SET site_id = $2,
		       site_assigned_at = CURRENT_TIMESTAMP,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING serial_number, status, active`,
		deviceID, targetSiteID).
		Scan(&result.SerialNumber, &result.Status, &result.Active)
	if err != nil {
		return nil, err
	}

	// Placements are marked for removal rather than deleted: the templates are
	// physically still in that sensor, and the terminal has to be told to erase
	// the ones it may no longer hold. Deleting the rows here would lose the
	// instruction.
	if _, err := tx.Exec(`
		UPDATE credential_placements
		   SET state = 'REMOVING', last_error = NULL
		 WHERE device_id = $1 AND state = 'PLACED'`, deviceID); err != nil {
		return nil, fmt.Errorf("marking placements for removal after move: %w", err)
	}

	// THE RELOCATION ITSELF, and the reason this is not merely a cancel.
	//
	// This used to call cancelQueuedWork and stop. That left the terminal
	// pointing at its new site with an EMPTY QUEUE and nothing durable telling
	// it anything had changed -- so it went on admitting the OLD site's people
	// from its cached roster until the 15-minute roster reconciler happened to
	// run. A terminal carried from a warehouse to a boardroom kept the
	// warehouse's staff list for a quarter of an hour, and the console showed
	// the move as done.
	//
	// A FULL_SYNC snapshot is exactly the right instrument and already exists.
	// It is a SET DIFFERENCE -- "your local set should be exactly this, remove
	// anything else" -- so one job simultaneously withdraws the old site's
	// people and establishes the new site's. The firmware already implements it
	// that way (sync_engine.cpp, reconcile): a member absent from the roster is
	// removed AND its template is erased from the sensor, then the removal is
	// reported back as a REMOVED placement, which converges the rows marked
	// REMOVING above. No firmware change, and no second protocol.
	//
	// ORDER MATTERS. devices.site_id was updated above, and every query in the
	// snapshot joins sites through it -- so the roster, and the SETTINGS job
	// carrying the offline policy, are the NEW site's. Compacting before the
	// move would have snapshotted the site the terminal is leaving.
	//
	// IN THIS TRANSACTION, so the move and the work that makes it true commit
	// together. That is what makes it safe across a crash: either the terminal
	// has moved and the snapshot is queued, or neither happened.
	//
	// IDEMPOTENT AND REDELIVERY-SAFE by construction. Moving twice cancels the
	// first snapshot and queues a second; a FULL_SYNC applied twice is a no-op
	// once converged, because it describes a set rather than a change. That is
	// the property the compaction comment calls out and the reason a "wipe then
	// re-add" design was rejected there.
	//
	// THE MOVE IS REFUSED IF THE DESTINATION DOES NOT FIT (FW-01). A terminal
	// that cannot hold the new site's roster would arrive there unable to be
	// told who is allowed, and the honest moment to say so is while the operator
	// is still looking at the screen -- not at a door in another building. The
	// whole transaction rolls back, so the terminal has not moved either.
	superseded, err := compactDeviceBacklogTx(tx, deviceID,
		"superseded by relocation to another site")
	if err != nil {
		var overflow *RosterCapacityError
		if errors.As(err, &overflow) {
			tx.Rollback()
			if recordErr := RecordRosterOverflow(deviceID, overflow.RosterSize,
				overflow.Capacity); recordErr != nil {
				log.Printf("recording roster overflow for device %d: %v", deviceID, recordErr)
			}
			return nil, err
		}
		return nil, fmt.Errorf("queueing relocation snapshot: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result.PendingJobsCancelled = int64(superseded)
	result.RosterResynced = true
	return &result, nil
}

// cancelQueuedWork retires a device's outstanding jobs.
//
// CANCELLED rather than COMPLETED, matching CompactDeviceBacklog: the work was
// superseded, not applied, so acknowledged_at stays null and the schema's
// "only acknowledged jobs are complete" invariant holds.
func cancelQueuedWork(tx *sql.Tx, deviceID int64, reason string) (int64, error) {
	result, err := tx.Exec(`
		UPDATE sync_jobs
		   SET status = 'CANCELLED', error_message = $2
		 WHERE device_id = $1
		   AND acknowledged_at IS NULL
		   AND status IN ('PENDING', 'FAILED')`, deviceID, reason)
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(`
		UPDATE devices SET pending_job_count = 0, failed_job_count = 0 WHERE id = $1`,
		deviceID); err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// RefreshTerminalJobCounters recomputes a device's denormalised backlog counts.
//
// The counters exist so the terminal list does not run a correlated subquery
// over sync_jobs per device -- the query that gets expensive on a fleet of a few
// thousand exactly when an operator most needs the page. They are maintained by
// the sync path; this is the reconciliation for anything that writes jobs
// outside it.
func RefreshTerminalJobCounters(deviceID int64) error {
	_, err := DB.Exec(`
		UPDATE devices d
		   SET pending_job_count = COALESCE(c.pending, 0),
		       failed_job_count  = COALESCE(c.failed, 0)
		  FROM (
			SELECT count(*) FILTER (WHERE status = 'PENDING') AS pending,
			       count(*) FILTER (WHERE status = 'FAILED')  AS failed
			  FROM sync_jobs WHERE device_id = $1
		  ) c
		 WHERE d.id = $1`, deviceID)
	return err
}

// RecordTerminalApplyFailure stores the reason a terminal could not apply a job.
//
// SYN-01: a device reporting "table full" produced a FAILED job and nothing
// else. The failure was invisible to every operator surface, so the symptom a
// customer saw was that some people simply did not work at some doors, with no
// indication anywhere that anything had gone wrong.
func RecordTerminalApplyFailure(deviceID int64, reason string) error {
	_, err := DB.Exec(`
		UPDATE devices
		   SET last_apply_error = NULLIF(left($2, 200), ''),
		       last_apply_error_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, deviceID, reason)
	return err
}

// ClearTerminalApplyFailure removes the recorded failure once a device applies
// something successfully, so the console does not keep reporting a problem that
// has resolved itself.
func ClearTerminalApplyFailure(deviceID int64) error {
	_, err := DB.Exec(`
		UPDATE devices
		   SET last_apply_error = NULL, last_apply_error_at = NULL
		 WHERE id = $1 AND last_apply_error IS NOT NULL`, deviceID)
	return err
}

// TerminalHealth is the operational state the console needs beyond inventory.
type TerminalHealth struct {
	PendingJobs    int
	FailedJobs     int
	LastApplyError string

	// LastApplyErrorAt dates the message above. An apply failure from four
	// months ago and one from four minutes ago read identically without it, and
	// they are not the same problem.
	LastApplyErrorAt *time.Time

	HasApplyError    bool
	CredentialActive bool
	OfflinePolicy    string
	OfflineGraceMins int

	// Capacity (FW-01). MemberCapacity is nil when the terminal has never
	// reported one, which is not the same as unlimited and must not be rendered
	// as a number -- see database/capacity.go.
	//
	// RosterSize is how many people this terminal's permissions cover right now.
	// It is the number an operator needs beside the capacity, because "over
	// capacity" without "by how many" does not tell anybody what to buy.
	MemberCapacity *int
	RosterSize     int
	OverCapacity   bool
}

// GetTerminalHealth reports what a terminal owes and whether it can still
// authenticate.
//
// CredentialActive is derived from the hash being present, not from a status
// column, because that is the same thing authentication checks: reporting a
// terminal as credentialed when the lookup would not resolve it would be a
// console that disagrees with the door.
func GetTerminalHealth(companyID int64, serial string) (*TerminalHealth, error) {
	var h TerminalHealth
	var applyError sql.NullString
	var applyErrorAt sql.NullTime
	var deviceID int64

	err := DB.QueryRow(`
		SELECT d.id, d.pending_job_count, d.failed_job_count, d.last_apply_error,
		       d.last_apply_error_at, d.api_key_hash IS NOT NULL,
		       s.offline_policy, s.offline_grace_minutes
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.serial_number = $1
		   AND s.company_id = $2
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`, serial, companyID).
		Scan(&deviceID, &h.PendingJobs, &h.FailedJobs, &applyError, &applyErrorAt,
			&h.CredentialActive, &h.OfflinePolicy, &h.OfflineGraceMins)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	h.LastApplyError = applyError.String
	h.HasApplyError = applyError.Valid
	if applyErrorAt.Valid {
		when := applyErrorAt.Time
		h.LastApplyErrorAt = &when
	}

	// Capacity is a second query rather than a join, because counting the
	// roster means evaluating the permission predicate and that does not belong
	// inline in a health lookup. This is a single-terminal read; the fleet list
	// deliberately does not carry it.
	capacity, err := InspectTerminalCapacity(DB, deviceID)
	if err != nil {
		return nil, err
	}
	h.RosterSize = capacity.RosterSize
	h.OverCapacity = capacity.Exceeded()
	if capacity.Known {
		known := capacity.Capacity
		h.MemberCapacity = &known
	}

	return &h, nil
}

// ResyncTerminal forces a terminal's queue to be replaced with a snapshot,
// resolved inside the caller's company.
//
// The site-key route that did this could not be reached from a browser -- it
// authenticates with the provisioning secret -- so an operator who believed a
// terminal had drifted had no way to act on it. Same underlying operation,
// reachable by the identity that actually needs it.
func ResyncTerminal(companyID int64, serial string) (superseded, pending int, err error) {
	deviceID, err := resolveTerminal(DB, companyID, serial)
	if err != nil {
		return 0, 0, err
	}

	superseded, err = CompactDeviceBacklog(deviceID)
	if err != nil {
		return 0, 0, err
	}

	pending, err = GetDeviceSyncBacklog(deviceID)
	if err != nil {
		return 0, 0, err
	}

	if err := RefreshTerminalJobCounters(deviceID); err != nil {
		return 0, 0, err
	}
	return superseded, pending, nil
}
