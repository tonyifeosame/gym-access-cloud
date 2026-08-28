package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"access-terminal-cloud-api/models"
)

// The console's Change Wi-Fi command (migrations/024_wifi_recovery_command.sql).
//
// THERE IS NO SECOND COMMAND SYSTEM HERE. A Change Wi-Fi request is one row in
// sync_jobs, delivered by GET /devices/jobs, retired by the acknowledgement the
// terminal posts to /devices/jobs/:id/complete, and leased and retried by the
// same machinery every other job uses. What this file adds is the operator's
// entry point to that queue and the two rules a command needs that a state
// snapshot does not:
//
//	ONE OUTSTANDING AT A TIME. Enforced by a partial unique index, because Go
//	cannot enforce it against a browser that retried a request whose response it
//	never saw. Requesting again while one is waiting RETURNS THE SAME COMMAND
//	rather than queueing a second -- which is what makes the endpoint safely
//	retryable, and is the whole of requirement "idempotent".
//
//	IT LAPSES. Every other job describes state, so delivering it late is merely
//	late; this one describes an ACT, and performing it late is destructive. See
//	models.WifiRecoveryValiditySeconds.
//
// NOTHING SECRET IS READ, WRITTEN OR LOGGED. The insert names no payload column
// at all: the job carries an id and a type, which is exactly what the firmware
// parses. No SSID and no pre-shared key exists anywhere on this path, so there
// is nothing here that redaction could have missed.

// wifiRecoveryValidity is models.WifiRecoveryValiditySeconds as a duration.
var wifiRecoveryValidity = time.Duration(models.WifiRecoveryValiditySeconds) * time.Second

// TerminalUnreachableError reports a terminal that cannot be sent a command.
//
// A DISTINCT ERROR RATHER THAN A BOOLEAN because the reason decides what the
// console tells the customer to do, and the three reasons have three different
// answers: recover the terminal by hand at the door, re-enable it first, or
// provision it at all. Code is one of the models.WifiRecoveryTerminal*
// constants and is what a client branches on.
type TerminalUnreachableError struct {
	Serial string
	Code   string
	Status string

	// Detail narrows a refusal that has more than one cause, for the human half
	// of the answer only. The CODE is what a client branches on and is stable;
	// this is not.
	//
	// It exists for exactly one distinction so far: a terminal that reported its
	// capabilities and lacks this one, versus one that has never reported at
	// all. Both refuse, and both are fixed by newer firmware -- but "it cannot"
	// and "we have not heard" send an operator to different places, and the
	// second is the whole fleet in the field today.
	Detail string
}

func (e *TerminalUnreachableError) Error() string {
	return fmt.Sprintf("terminal %s cannot receive a command: %s (status %s)",
		e.Serial, e.Code, e.Status)
}

// ErrTerminalUnreachable matches any TerminalUnreachableError under errors.Is,
// for callers that only care that the command was refused.
var ErrTerminalUnreachable = errors.New("terminal cannot receive a command")

func (e *TerminalUnreachableError) Is(target error) bool {
	return target == ErrTerminalUnreachable
}

// wifiRecoveryTarget is the terminal, as far as this operation is concerned.
type wifiRecoveryTarget struct {
	deviceID        int64
	serial          string
	status          string
	active          bool
	hasCredential   bool
	lastHeartbeatAt *time.Time

	// What this terminal last reported it can do (025), and whether it has ever
	// reported at all.
	//
	// TWO FIELDS RATHER THAN ONE NIL SLICE, because the console has to tell the
	// two apart and a nil slice from the database layer would also be what a
	// scan error produced. "Reports, and cannot" sends an operator to the
	// firmware catalogue; "has never told us" sends them to check whether the
	// unit has heartbeat since it was updated. Neither queues a command.
	capabilities      []string
	capabilitiesKnown bool
}

// deliverable reports whether this terminal could actually collect a command,
// and says why not when it could not.
//
// THE ORDER IS THE ORDER AN OPERATOR SHOULD READ THEM IN. A disabled terminal is
// also offline, and a never-provisioned one is also both -- reporting "offline"
// for a terminal an administrator switched off would send somebody to the door
// with a laptop to fix a problem that is one click away in this console.
func (t *wifiRecoveryTarget) deliverable() *TerminalUnreachableError {
	switch {
	case !t.active || t.status == "DISABLED":
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.WifiRecoveryTerminalDisabled,
		}
	case !t.hasCredential:
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.WifiRecoveryTerminalNoCredential,
		}

	case !DeviceHasCapability(t.capabilities, models.CapabilityWifiRecovery):
		// BEFORE THE OFFLINE CHECK, and after the two administrative ones. The
		// ordering is what an operator should read them in, and it turns on
		// which answer is actionable:
		//
		//   disabled / unprovisioned  a fact the operator can change from this
		//                             console, in one click.
		//   CANNOT CHANGE WI-FI       a fact about the firmware, which no amount
		//                             of bringing the terminal online will alter.
		//   offline                   transient, and the remedy is the local
		//                             recovery at the door.
		//
		// Reporting "offline" to somebody whose terminal ALSO cannot carry the
		// command out would send them to the door, get the unit back online, and
		// leave them exactly where they started.
		detail := "This terminal has never reported what it can do, so the " +
			"platform cannot tell whether it would carry this out."
		if t.capabilitiesKnown {
			detail = "This terminal reported what it can do, and changing " +
				"Wi-Fi remotely is not among it."
		}
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code:   models.WifiRecoveryTerminalIncapable,
			Detail: detail,
		}
	case t.status != "ONLINE" && t.status != "UPDATING" && t.status != "ERROR":
		// OFFLINE, or PROVISIONING and never heard from. Both mean the poll that
		// would collect this command is not happening.
		//
		// UPDATING and ERROR are deliberately NOT refused: both are states a
		// terminal reports while it is still heartbeating, and refusing a
		// terminal that is talking to us would be the console inventing a
		// restriction the transport does not have.
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.WifiRecoveryTerminalOffline,
		}
	}
	return nil
}

// loadWifiRecoveryTarget resolves a serial inside one company and reads the
// facts the command needs.
//
// Tenancy goes through resolveTerminal, which is the single place that join is
// written -- a second copy of a tenancy filter is a second place for it to be
// relaxed. A serial belonging to another company is models.ErrDeviceNotFound,
// never a refusal, so the answer cannot confirm that it is registered elsewhere.
func loadWifiRecoveryTarget(q rowQuerier, companyID int64, serial string) (*wifiRecoveryTarget, error) {
	deviceID, err := resolveTerminal(q, companyID, serial)
	if err != nil {
		return nil, err
	}

	target := &wifiRecoveryTarget{deviceID: deviceID}
	var capabilities []byte
	err = q.QueryRow(`
		SELECT serial_number, status, active, api_key_hash IS NOT NULL,
		       last_heartbeat_at, capabilities
		  FROM devices WHERE id = $1`, deviceID).Scan(
		&target.serial, &target.status, &target.active,
		&target.hasCredential, &target.lastHeartbeatAt, &capabilities)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	// A list that will not decode is treated as NEVER REPORTED rather than as an
	// error. The column is filled by devices, and a terminal that wrote nonsense
	// into it must not be able to fail an operator's request with a 500 -- the
	// honest answer is that the platform does not know what this unit can do,
	// which is the same answer as never having heard from it.
	if parsed, parseErr := scanCapabilities(capabilities); parseErr == nil {
		target.capabilities = parsed
		target.capabilitiesKnown = parsed != nil
	}

	return target, nil
}

// RequestWifiRecovery queues the Change Wi-Fi command for one terminal, or
// returns the command already waiting for it.
//
// Returns *TerminalUnreachableError when the terminal could not collect it.
// Nothing is queued in that case -- see the lapsing note at the top of this
// file for why a command left waiting for a terminal that is not listening is
// worse than no command at all.
func RequestWifiRecovery(companyID int64, serial string) (*models.ConsoleWifiRecovery, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	target, err := loadWifiRecoveryTarget(tx, companyID, serial)
	if err != nil {
		return nil, err
	}

	// The device row is locked for the rest of this transaction, so two requests
	// arriving together cannot both find no command waiting and both queue one.
	// The partial unique index would catch that anyway; serialising here means
	// the loser gets the SAME answer as a sequential second press rather than a
	// duplicate-key error it would have to interpret.
	if _, err := tx.Exec(`SELECT id FROM devices WHERE id = $1 FOR UPDATE`,
		target.deviceID); err != nil {
		return nil, err
	}

	if refusal := target.deliverable(); refusal != nil {
		return nil, refusal
	}

	// A command that was never collected inside its window is retired before
	// anything else looks at the queue. It occupies the one-outstanding index
	// and it is no longer safe to deliver, so leaving it there would block every
	// future request for this terminal for ever.
	if _, err := tx.Exec(`
		UPDATE sync_jobs
		   SET status = 'CANCELLED',
		       error_message = 'wifi recovery command lapsed before the terminal collected it'
		 WHERE device_id = $1
		   AND job_type = $2
		   AND status = 'PENDING'
		   AND created_at <= CURRENT_TIMESTAMP - ($3 || ' seconds')::interval`,
		target.deviceID, models.WifiRecoveryJobType, models.WifiRecoveryValiditySeconds,
	); err != nil {
		return nil, fmt.Errorf("retiring a lapsed wifi recovery command: %w", err)
	}

	existing, err := readWifiRecoveryJob(tx, target.deviceID, true)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// IDEMPOTENT. The same command, reported as already waiting, and no
		// second row. A terminal that received two would enter setup mode, be
		// re-provisioned by the customer, and then be sent back into setup mode
		// by the one still in the queue.
		out := describeWifiRecovery(target, existing)
		out.AlreadyQueued = true
		return out, tx.Commit()
	}

	// NO PAYLOAD COLUMN IS NAMED. The job id and the type are the whole message,
	// which is exactly what the firmware parses -- and it means there is no
	// field on this path that a Wi-Fi credential could ever be put into.
	if _, err := tx.Exec(`
		INSERT INTO sync_jobs (site_id, device_id, job_type, protocol_version, status)
		SELECT d.site_id, d.id, $2, $3, 'PENDING'
		  FROM devices d WHERE d.id = $1 AND d.deleted_at IS NULL`,
		target.deviceID, models.WifiRecoveryJobType, models.SyncProtocolVersion,
	); err != nil {
		return nil, fmt.Errorf("enqueueing the wifi recovery command: %w", err)
	}

	queued, err := readWifiRecoveryJob(tx, target.deviceID, true)
	if err != nil {
		return nil, err
	}
	if queued == nil {
		// The device disappeared between the lock and the insert. Reported as
		// not found rather than as a success that queued nothing.
		return nil, models.ErrDeviceNotFound
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return describeWifiRecovery(target, queued), nil
}

// WifiRecoveryStatus reports the terminal's most recent Change Wi-Fi command.
//
// A PURE READ. It never cancels a lapsed command, even though it can see one --
// housekeeping on a GET would mean the answer depended on who had looked at it
// last. A lapsed command reads as EXPIRED here and is retired by the next
// request, which is the only path that needs the index slot back.
func WifiRecoveryStatus(companyID int64, serial string) (*models.ConsoleWifiRecovery, error) {
	target, err := loadWifiRecoveryTarget(DB, companyID, serial)
	if err != nil {
		return nil, err
	}

	job, err := readWifiRecoveryJob(DB, target.deviceID, false)
	if err != nil {
		return nil, err
	}
	return describeWifiRecovery(target, job), nil
}

// wifiRecoveryJob is one row of sync_jobs, as this feature reads it.
type wifiRecoveryJob struct {
	publicID      string
	status        string
	createdAt     time.Time
	lastAttemptAt *time.Time
	acknowledged  *time.Time
}

// readWifiRecoveryJob returns a terminal's Change Wi-Fi command.
//
// pendingOnly narrows it to one that is still outstanding, which is the
// question the request path asks ("is there already one waiting"). The status
// path asks the other question -- "what happened to the last one" -- and takes
// the most recent row whatever became of it.
func readWifiRecoveryJob(q rowQuerier, deviceID int64, pendingOnly bool) (*wifiRecoveryJob, error) {
	filter := ""
	if pendingOnly {
		filter = ` AND status = 'PENDING'`
	}

	var job wifiRecoveryJob
	err := q.QueryRow(`
		SELECT public_id, status, created_at, last_attempt_at, acknowledged_at
		  FROM sync_jobs
		 WHERE device_id = $1 AND job_type = $2`+filter+`
		 ORDER BY id DESC
		 LIMIT 1`, deviceID, models.WifiRecoveryJobType).Scan(
		&job.publicID, &job.status,
		&job.createdAt, &job.lastAttemptAt, &job.acknowledged)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// describeWifiRecovery turns a terminal and its command into what the console
// reads.
//
// THE STATE IS DERIVED HERE AND NOWHERE ELSE, so the request's answer and the
// poll that follows it cannot describe the same row differently.
func describeWifiRecovery(target *wifiRecoveryTarget, job *wifiRecoveryJob) *models.ConsoleWifiRecovery {
	out := &models.ConsoleWifiRecovery{
		SerialNumber:   target.serial,
		State:          models.WifiRecoveryNone,
		TerminalStatus: target.status,
		// The device state machine's own answer, not a timestamp comparison
		// invented here. The offline sweep owns this column, and a second
		// definition of "online" would put the console and the fleet page into
		// disagreement about the same terminal.
		Online:          target.status == "ONLINE",
		LastHeartbeatAt: target.lastHeartbeatAt,
	}
	if job == nil {
		return out
	}

	out.RequestID = job.publicID
	queued := job.createdAt
	out.QueuedAt = &queued
	out.AcknowledgedAt = job.acknowledged

	switch job.status {
	case "COMPLETED":
		// THE ONLY STATE THAT MEANS ANYTHING HAPPENED AT THE TERMINAL. The
		// firmware produces this acknowledgement before it drops the link,
		// deliberately, so that the command is retired rather than redelivered
		// after the customer re-provisions.
		out.State = models.WifiRecoveryAccepted
	case "FAILED":
		out.State = models.WifiRecoveryFailed
	case "CANCELLED":
		out.State = models.WifiRecoveryCancelled
	default:
		expiresAt := job.createdAt.Add(wifiRecoveryValidity)
		out.ExpiresAt = &expiresAt
		switch {
		case !time.Now().Before(expiresAt):
			out.State = models.WifiRecoveryExpired
		case job.lastAttemptAt != nil:
			// Collected. Fetching takes a delivery lease and stamps
			// last_attempt_at without changing the status, so this is the only
			// evidence the platform has that the terminal HAS the command --
			// and it is short of evidence that it applied it.
			out.State = models.WifiRecoveryDelivered
			out.DeliveredAt = job.lastAttemptAt
		default:
			out.State = models.WifiRecoveryQueued
		}
	}
	return out
}
