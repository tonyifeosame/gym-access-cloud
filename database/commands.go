package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"access-terminal-cloud-api/models"
)

// The remote command plane's store (migrations/028_command_plane.sql).
//
// THERE IS NO SECOND COMMAND SYSTEM HERE, and that sentence is lifted verbatim
// from database/wifi_recovery.go because it is the same claim and it stays
// true. A command is one row in sync_jobs, delivered by GET /devices/jobs,
// retired by the acknowledgement the terminal posts to
// /devices/jobs/:id/complete, and leased and retried by the same machinery
// every other job uses. What this file adds is the operator's entry point and
// the rules a command needs that a state snapshot does not.
//
// ---------------------------------------------------------------------------
// WHY THIS FILE EXISTS RATHER THAN A SECOND wifi_recovery.go
// ---------------------------------------------------------------------------
//
// Because that is exactly what happened last time. WIFI_RECOVERY (024) worked
// out that a command needs a capability gate, a validity window, a
// one-outstanding rule and a state machine that never claims more than the
// platform can prove. ENROLL_FINGERPRINT (027) was added three migrations
// later, by the same author, applying the same reasoning in the same file --
// and got none of the four, because there was nowhere to put the answer.
//
// This file is that place. Every rule below is 024's, generalised, and the
// registry in models/commands.go is what makes the next command type inherit
// them instead of rediscovering them.
//
// ---------------------------------------------------------------------------
// WHAT IT DOES NOT TOUCH
// ---------------------------------------------------------------------------
//
// The two inherited commands keep their own entry points and their own
// behaviour. RequestWifiRecovery still owns Change Wi-Fi; the enrolment handler
// still owns enrolment. Both are registered in models.CommandSpecs as NOT
// Issuable, so this path describes their rows and refuses to create them --
// two ways to queue the same command is two places for its rules to diverge.

// commandTarget is the terminal, as far as issuing a command is concerned.
//
// Deliberately the same shape as wifiRecoveryTarget, minus the assumption that
// the capability in question is Wi-Fi recovery. The two are not merged: doing
// that would mean editing the Change Wi-Fi path, which is in production and
// whose tests are the proof that this generalisation preserved its behaviour.
type commandTarget struct {
	deviceID        int64
	siteID          int64
	serial          string
	status          string
	active          bool
	hasCredential   bool
	lastHeartbeatAt *time.Time
	firmwareVersion string

	// What this terminal last reported it can do (025), and whether it has ever
	// reported at all.
	//
	// TWO FIELDS RATHER THAN ONE NIL SLICE. "Reports, and cannot" sends an
	// operator to the firmware catalogue; "has never told us" sends them to
	// check whether the unit has heartbeat since it was updated. Neither queues
	// a command, and a nil slice alone could not tell them apart -- nor could it
	// be told from what a scan error produced.
	capabilities      []string
	capabilitiesKnown bool
}

// deliverable reports whether this terminal could collect a command of this
// type, and says why not when it could not.
//
// THE ORDER IS THE ORDER AN OPERATOR SHOULD READ THEM IN, and it is 024's
// order for 024's reason. A disabled terminal is also offline, and a
// never-provisioned one is also both -- reporting "offline" for a terminal an
// administrator switched off would send somebody to the door with a laptop to
// fix a problem that is one click away in this console.
//
// The capability check sits BEFORE the offline check and after the two
// administrative ones, because that is the order of what is actionable:
//
//	disabled / unprovisioned  a fact the operator can change from this console.
//	CANNOT DO THIS            a fact about the firmware, which no amount of
//	                          bringing the terminal online will alter.
//	offline                   transient, and the remedy is at the door.
func (t *commandTarget) deliverable(spec models.CommandSpec) *TerminalUnreachableError {
	switch {
	case !t.active || t.status == "DISABLED":
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.CommandRefusedDisabled,
		}

	case !t.hasCredential:
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.CommandRefusedNoCredential,
		}

	case !DeviceHasCapability(t.capabilities, spec.Capability):
		// THE GATE THAT STOPS A LIE. Firmware that does not recognise a job
		// type acknowledges it as applied -- deliberately, so a newer server's
		// types are not redelivered for ever -- which means an old unit's
		// acknowledgement is indistinguishable from a new one's. Without this,
		// the console would show ACCEPTED for a command that was parsed as
		// kUnknown and thrown away.
		//
		// SILENCE IS NOT CONSENT. A terminal that has never reported is refused
		// on the same terms as one that reported and lacks it, because treating
		// silence as capability is exactly what produced the false ACCEPTED that
		// 025 was written to stop. The whole fleet in the field is in the first
		// category, which is why the two are distinguished for the human even
		// though neither queues anything.
		detail := "This terminal has never reported what it can do, so the " +
			"platform cannot tell whether it would carry this out."
		if t.capabilitiesKnown {
			detail = fmt.Sprintf("This terminal reported what it can do, and "+
				"%s is not among it.", spec.Type)
		}
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code:   models.CommandRefusedIncapable,
			Detail: detail,
		}

	case t.status != "ONLINE" && t.status != "UPDATING" && t.status != "ERROR":
		// OFFLINE, or PROVISIONING and never heard from. Both mean the poll
		// that would collect this command is not happening.
		//
		// UPDATING and ERROR are deliberately NOT refused: both are states a
		// terminal reports while it is still heartbeating, and refusing one
		// that is talking to us would be the console inventing a restriction
		// the transport does not have.
		return &TerminalUnreachableError{
			Serial: t.serial, Status: t.status,
			Code: models.CommandRefusedOffline,
		}
	}
	return nil
}

// loadCommandTarget resolves a serial inside one company and reads the facts a
// command needs.
//
// Tenancy goes through resolveTerminal, which is the single place that join is
// written. A serial belonging to another company is models.ErrDeviceNotFound,
// never a refusal, so the answer cannot confirm that it is registered
// elsewhere.
func loadCommandTarget(q rowQuerier, companyID int64, serial string) (*commandTarget, error) {
	deviceID, err := resolveTerminal(q, companyID, serial)
	if err != nil {
		return nil, err
	}

	target := &commandTarget{deviceID: deviceID}
	var capabilities []byte
	var firmware sql.NullString
	err = q.QueryRow(`
		SELECT site_id, serial_number, status, active, api_key_hash IS NOT NULL,
		       last_heartbeat_at, capabilities, firmware_version
		  FROM devices WHERE id = $1`, deviceID).Scan(
		&target.siteID, &target.serial, &target.status, &target.active,
		&target.hasCredential, &target.lastHeartbeatAt, &capabilities, &firmware)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	target.firmwareVersion = firmware.String

	// A list that will not decode is treated as NEVER REPORTED rather than as
	// an error. The column is filled by devices, and a terminal that wrote
	// nonsense into it must not be able to fail an operator's request with a
	// 500 -- the honest answer is that the platform does not know what this
	// unit can do, which is the same answer as never having heard from it.
	if parsed, parseErr := scanCapabilities(capabilities); parseErr == nil {
		target.capabilities = parsed
		target.capabilitiesKnown = parsed != nil
	}

	return target, nil
}

// CommandIssueInput is what the handler has resolved before the store is asked
// to do anything.
//
// The operator's identity is passed in rather than read here, because tenancy
// and role have already been settled by middleware and re-deriving them in the
// store would be the authorization rule written a second time.
type CommandIssueInput struct {
	CompanyID int64
	Serial    string
	Type      string

	// Params has already been through the spec's validator, so it is the
	// canonical form rather than whatever the caller sent.
	Params json.RawMessage

	Reason         string
	OperatorID     int64
	OperatorEmail  string
	IdempotencyKey string
}

// IssueCommand queues one command for one terminal, or returns the one already
// waiting for it.
//
// Returns *TerminalUnreachableError when the terminal could not collect it.
// NOTHING IS QUEUED IN THAT CASE: a command left waiting for a terminal that is
// not listening is worse than no command at all, because it will be collected
// eventually -- possibly after the situation that prompted it has been resolved
// by hand.
func IssueCommand(in CommandIssueInput) (*models.ConsoleCommand, error) {
	spec, known := models.CommandSpecFor(in.Type)
	if !known {
		return nil, &CommandRefusedError{
			Code:   models.CommandRefusedUnknownType,
			Detail: fmt.Sprintf("%q is not a command this platform issues", in.Type),
		}
	}
	if !spec.Issuable {
		// A registered type with its own entry point. Refused here rather than
		// quietly forwarded, so there is exactly one path that owns its rules.
		return nil, &CommandRefusedError{
			Code: models.CommandRefusedNotIssuable,
			Detail: fmt.Sprintf("%s has its own endpoint and is not issued "+
				"through the command plane", in.Type),
		}
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	target, err := loadCommandTarget(tx, in.CompanyID, in.Serial)
	if err != nil {
		return nil, err
	}

	// The device row is locked for the rest of this transaction, so two
	// requests arriving together cannot both find nothing outstanding and both
	// queue one. The partial unique index would catch that anyway; serialising
	// here means the loser gets the SAME answer as a sequential second press
	// rather than a duplicate-key error it would have to interpret.
	if _, err := tx.Exec(`SELECT id FROM devices WHERE id = $1 FOR UPDATE`,
		target.deviceID); err != nil {
		return nil, err
	}

	// A RETRY OF A REQUEST WHOSE RESPONSE THE CALLER NEVER SAW.
	//
	// Checked BEFORE the reachability refusal, deliberately. The first request
	// may have succeeded and the terminal may have gone offline since; refusing
	// the retry as "offline" would tell the operator their command was never
	// queued when it was. The stored row is the truth about what happened.
	if in.IdempotencyKey != "" {
		existing, err := readCommandByIdempotencyKey(tx, target.deviceID, in.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			out := describeCommand(target, existing)
			out.AlreadyPending = true
			return out, tx.Commit()
		}
	}

	if refusal := target.deliverable(spec); refusal != nil {
		return nil, refusal
	}

	// A command that was never collected inside its window is retired before
	// anything else looks at the queue. It occupies the one-outstanding index
	// and it is no longer safe to deliver, so leaving it there would block
	// every future request of this type for this terminal for ever.
	if _, err := tx.Exec(`
		UPDATE sync_jobs
		   SET status = 'CANCELLED',
		       error_message = 'command lapsed before the terminal collected it'
		 WHERE device_id = $1
		   AND job_type = $2
		   AND status = 'PENDING'
		   AND expires_at IS NOT NULL
		   AND expires_at <= CURRENT_TIMESTAMP`,
		target.deviceID, spec.Type,
	); err != nil {
		return nil, fmt.Errorf("retiring a lapsed %s command: %w", spec.Type, err)
	}

	if !spec.Repeatable {
		outstanding, err := readOutstandingCommand(tx, target.deviceID, spec.Type)
		if err != nil {
			return nil, err
		}
		if outstanding != nil {
			// A REPEAT AND A DIFFERENT REQUEST ARE NOT THE SAME EVENT, and
			// answering both with 202 was a defect: an operator who asked for a
			// self-test while a display test was outstanding was told
			// "Accepted", handed the display test's id, and the self-test never
			// ran. A success code for work that will never happen is the worst
			// of the available answers.
			//
			// So the parameters decide. Identical parameters are the repeated
			// button press this branch was written for and keep the idempotent
			// 202. Different parameters are a different command, and the caller
			// is told the slot is taken -- with the outstanding command named,
			// so the console can offer to withdraw it.
			if !sameCommandParams(outstanding.payload, in.Params) {
				return nil, &CommandRefusedError{
					Code: models.CommandRefusedAlreadyPending,
					Detail: fmt.Sprintf(
						"a %s command is already waiting for this terminal "+
							"with different parameters; withdraw it before "+
							"issuing another", spec.Type),
				}
			}

			// IDEMPOTENT. The same command, reported as already waiting, and no
			// second row. Not an error: an operator pressing a button twice has
			// done nothing wrong, and the honest answer is what is already
			// queued for them.
			out := describeCommand(target, outstanding)
			out.AlreadyPending = true
			return out, tx.Commit()
		}
	}

	var expiresAt interface{}
	if spec.Validity > 0 {
		expiresAt = time.Now().UTC().Add(spec.Validity)
	}

	var operatorID interface{}
	if in.OperatorID != 0 {
		operatorID = in.OperatorID
	}
	var key interface{}
	if in.IdempotencyKey != "" {
		key = in.IdempotencyKey
	}
	var payload interface{}
	if len(in.Params) > 0 {
		payload = []byte(in.Params)
	}

	var publicID string
	err = tx.QueryRow(`
		INSERT INTO sync_jobs (
		    site_id, device_id, job_type, protocol_version, status, payload,
		    command_class, command_version, expires_at, requires_capability,
		    requested_by, requested_by_email, reason, idempotency_key)
		VALUES ($1, $2, $3, $4, 'PENDING', $5,
		        $6, $7, $8, $9,
		        $10, $11, $12, $13)
		RETURNING public_id`,
		target.siteID, target.deviceID, spec.Type, models.SyncProtocolVersion, payload,
		models.CommandClassCommand, models.CommandEnvelopeVersion, expiresAt, spec.Capability,
		operatorID, in.OperatorEmail, nullIfEmpty(in.Reason), key,
	).Scan(&publicID)
	if err != nil {
		return nil, fmt.Errorf("enqueueing the %s command: %w", spec.Type, err)
	}

	queued, err := readCommandByPublicID(tx, target.deviceID, publicID)
	if err != nil {
		return nil, err
	}
	if queued == nil {
		// The row disappeared between the insert and the read, which should be
		// impossible inside one transaction. Reported as not found rather than
		// as a success that queued nothing.
		return nil, models.ErrDeviceNotFound
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return describeCommand(target, queued), nil
}

// RecordCommandResult stores what a terminal reported about a command.
//
// SEPARATE FROM THE ACKNOWLEDGEMENT, AND CALLED BEFORE IT. Three reasons, in
// order of how much they matter:
//
//   - AckJobCompleted and AckJobFailed are the oldest and most heavily tested
//     path in the sync engine, and they are on every job of every type. Widening
//     their signatures to carry two fields that only apply to commands would put
//     a change into the path that retires a person's roster update, to serve a
//     diagnostic snapshot.
//
//   - Writing the result FIRST means a console reading the row between the two
//     writes sees a PENDING command that already has its result, which is merely
//     early. The other order shows an ACCEPTED command with nothing behind it,
//     which reads as a terminal that answered and said nothing.
//
//   - It is scoped to COMMAND rows, so a STATE job that somehow carried a result
//     code updates nothing rather than growing a column it has no meaning for.
//
// Best effort at the call site: the acknowledgement itself is the thing that
// must not fail, and a terminal has nothing useful to do with "your result was
// not stored" except retry an acknowledgement the platform already accepted.
func RecordCommandResult(deviceID, jobID int64, resultCode string, result json.RawMessage) error {
	if resultCode == "" && len(result) == 0 {
		return nil
	}

	// Bounded here as well as at the handler. The handler is where a
	// well-formed refusal can be returned to the device; this is the backstop
	// for any other caller, and for the case where the two disagree.
	if len(result) > models.MaxCommandResultBytes {
		return fmt.Errorf("command result is %d bytes, over the %d-byte ceiling",
			len(result), models.MaxCommandResultBytes)
	}

	var payload interface{}
	if len(result) > 0 {
		payload = []byte(result)
	}

	_, err := DB.Exec(`
		UPDATE sync_jobs
		   SET result_code = COALESCE(NULLIF($3, ''), result_code),
		       result = COALESCE($4::jsonb, result)
		 WHERE id = $1 AND device_id = $2 AND command_class = 'COMMAND'`,
		jobID, deviceID, resultCode, payload)
	return err
}

// CommandRefusedError is a request the platform will not turn into a row, for a
// reason that is about the REQUEST rather than about the terminal.
//
// Distinct from TerminalUnreachableError, which is about the door. The two
// carry different HTTP statuses and send an operator to different places: an
// unknown command type is a client bug, an unreachable terminal is a site
// visit.
type CommandRefusedError struct {
	Code   string
	Detail string
}

func (e *CommandRefusedError) Error() string {
	return fmt.Sprintf("command refused: %s (%s)", e.Code, e.Detail)
}

// ErrCommandRefused matches any CommandRefusedError under errors.Is.
var ErrCommandRefused = errors.New("command refused")

func (e *CommandRefusedError) Is(target error) bool {
	return target == ErrCommandRefused
}

// commandRow is one row of sync_jobs, as this feature reads it.
type commandRow struct {
	publicID      string
	jobType       string
	status        string
	payload       []byte
	reason        sql.NullString
	requestedBy   sql.NullString
	createdAt     time.Time
	deliveredAt   *time.Time
	acknowledged  *time.Time
	expiresAt     *time.Time
	resultCode    sql.NullString
	result        []byte
	errorMessage  sql.NullString
	attempts      int
	lastAttemptAt *time.Time
}

const commandColumns = `public_id, job_type, status, payload, reason,
	requested_by_email, created_at, delivered_at, acknowledged_at, expires_at,
	result_code, result, error_message, attempts, last_attempt_at`

func scanCommandRow(scan func(dest ...interface{}) error) (*commandRow, error) {
	var row commandRow
	err := scan(
		&row.publicID, &row.jobType, &row.status, &row.payload, &row.reason,
		&row.requestedBy, &row.createdAt, &row.deliveredAt, &row.acknowledged,
		&row.expiresAt, &row.resultCode, &row.result, &row.errorMessage,
		&row.attempts, &row.lastAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// readOutstandingCommand returns a still-PENDING command of one type, if any.
// sameCommandParams reports whether two validated parameter payloads describe
// the same request.
//
// COMPARED AS JSON VALUES, NOT AS BYTES. Both sides have normally been through
// the registry's validator and so are already canonical, but a row written by
// an older build -- or by a validator whose field order later changes -- would
// compare unequal as bytes while meaning exactly the same thing, and that would
// turn a repeated button press into a refusal. Absent and empty are the same
// request, because the validator spells an absent parameter object out into its
// defaults on the way in.
func sameCommandParams(a, b []byte) bool {
	var left, right interface{}
	if len(a) > 0 {
		if err := json.Unmarshal(a, &left); err != nil {
			return false
		}
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &right); err != nil {
			return false
		}
	}
	return reflect.DeepEqual(left, right)
}

func readOutstandingCommand(q rowQuerier, deviceID int64, jobType string) (*commandRow, error) {
	return scanCommandRow(q.QueryRow(`
		SELECT `+commandColumns+`
		  FROM sync_jobs
		 WHERE device_id = $1 AND job_type = $2
		   AND command_class = 'COMMAND' AND status = 'PENDING'
		 ORDER BY id DESC LIMIT 1`, deviceID, jobType).Scan)
}

func readCommandByPublicID(q rowQuerier, deviceID int64, publicID string) (*commandRow, error) {
	return scanCommandRow(q.QueryRow(`
		SELECT `+commandColumns+`
		  FROM sync_jobs
		 WHERE device_id = $1 AND public_id = $2 AND command_class = 'COMMAND'`,
		deviceID, publicID).Scan)
}

func readCommandByIdempotencyKey(q rowQuerier, deviceID int64, key string) (*commandRow, error) {
	return scanCommandRow(q.QueryRow(`
		SELECT `+commandColumns+`
		  FROM sync_jobs
		 WHERE device_id = $1 AND idempotency_key = $2::uuid
		   AND command_class = 'COMMAND'`, deviceID, key).Scan)
}

// CommandStatus reports one command by its public id.
//
// A PURE READ. It never retires a lapsed command, even though it can see one --
// housekeeping on a GET would mean the answer depended on who had looked at it
// last. A lapsed command reads as EXPIRED here and is retired by the next issue
// of the same type, which is the only path that needs the index slot back.
func CommandStatus(companyID int64, serial, publicID string) (*models.ConsoleCommand, error) {
	target, err := loadCommandTarget(DB, companyID, serial)
	if err != nil {
		return nil, err
	}

	row, err := readCommandByPublicID(DB, target.deviceID, publicID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, models.ErrCommandNotFound
	}
	return describeCommand(target, row), nil
}

// ListCommands reports a terminal's command history, newest first.
//
// openOnly narrows it to commands that can still change, which is what a
// console polling a dialog asks for. The unfiltered form is the audit-shaped
// question -- "what has been asked of this door" -- and is what an operator
// investigating a complaint reads.
func ListCommands(companyID int64, serial string, openOnly bool, limit int) (*models.ConsoleCommandList, error) {
	target, err := loadCommandTarget(DB, companyID, serial)
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > 200 {
		limit = 50
	}

	filter := ""
	if openOnly {
		filter = ` AND status = 'PENDING'`
	}

	rows, err := DB.Query(`
		SELECT `+commandColumns+`
		  FROM sync_jobs
		 WHERE device_id = $1 AND command_class = 'COMMAND'`+filter+`
		 ORDER BY id DESC
		 LIMIT $2`, target.deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &models.ConsoleCommandList{
		SerialNumber: target.serial,
		Commands:     []models.ConsoleCommand{},
	}
	for rows.Next() {
		row, err := scanCommandRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		out.Commands = append(out.Commands, *describeCommand(target, row))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Count = len(out.Commands)
	return out, nil
}

// WithdrawCommand cancels a command that has NOT yet been collected.
//
// A DELIVERED COMMAND CANNOT BE RECALLED, and this refuses rather than
// pretending. The terminal already has it and will act on it or not; saying
// otherwise would be exactly the lie the ACCEPTED state was designed to avoid.
// The honest answer to "can I stop it" after delivery is no.
func WithdrawCommand(companyID int64, serial, publicID string) (*models.ConsoleCommand, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	target, err := loadCommandTarget(tx, companyID, serial)
	if err != nil {
		return nil, err
	}

	row, err := readCommandByPublicID(tx, target.deviceID, publicID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, models.ErrCommandNotFound
	}

	// The status guard is in the UPDATE as well as here, so a command collected
	// between this read and that write is still not withdrawn from under a
	// terminal that already has it.
	result, err := tx.Exec(`
		UPDATE sync_jobs
		   SET status = 'CANCELLED',
		       error_message = 'withdrawn by an operator before delivery'
		 WHERE device_id = $1 AND public_id = $2
		   AND command_class = 'COMMAND'
		   AND status = 'PENDING'
		   AND delivered_at IS NULL`, target.deviceID, publicID)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, models.ErrCommandNotWithdrawable
	}

	updated, err := readCommandByPublicID(tx, target.deviceID, publicID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return describeCommand(target, updated), nil
}

// TerminalCapabilities reports what a terminal says it can do and which
// commands the platform will therefore accept for it.
func TerminalCapabilities(companyID int64, serial string) (*models.ConsoleTerminalCapabilities, error) {
	target, err := loadCommandTarget(DB, companyID, serial)
	if err != nil {
		return nil, err
	}

	var reportedAt *time.Time
	if err := DB.QueryRow(
		`SELECT capabilities_reported_at FROM devices WHERE id = $1`,
		target.deviceID).Scan(&reportedAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	out := &models.ConsoleTerminalCapabilities{
		SerialNumber:    target.serial,
		Reported:        target.capabilities,
		ReportedAt:      reportedAt,
		FirmwareVersion: target.firmwareVersion,
		TerminalStatus:  target.status,
		// The device state machine's own answer, not a timestamp comparison
		// invented here. A second definition of "online" would put this view
		// and the fleet page into disagreement about the same terminal.
		Online:          target.status == "ONLINE",
		LastHeartbeatAt: target.lastHeartbeatAt,
		Commands:        []models.ConsoleCommandOffer{},
	}

	for _, name := range models.IssuableCommandTypes() {
		spec := models.CommandSpecs[name]
		out.Commands = append(out.Commands, models.ConsoleCommandOffer{
			Type:            spec.Type,
			Capability:      spec.Capability,
			Supported:       DeviceHasCapability(target.capabilities, spec.Capability),
			MinRole:         spec.MinRole,
			ReadOnly:        spec.ReadOnly,
			Repeatable:      spec.Repeatable,
			ValiditySeconds: int(spec.Validity.Seconds()),
		})
	}
	return out, nil
}

// describeCommand turns a terminal and one row into what the console reads.
//
// THE STATE IS DERIVED HERE AND NOWHERE ELSE, so the issue response and the
// poll that follows it cannot describe the same row differently. This is 024's
// rule and its ordering; what changed is that expiry now comes from the row's
// own expires_at rather than from a constant, so a type with no window and a
// type with a fifteen-minute one are read by the same code.
func describeCommand(target *commandTarget, row *commandRow) *models.ConsoleCommand {
	out := &models.ConsoleCommand{
		SerialNumber:    target.serial,
		State:           models.CommandStateNone,
		TerminalStatus:  target.status,
		Online:          target.status == "ONLINE",
		LastHeartbeatAt: target.lastHeartbeatAt,
	}
	if row == nil {
		return out
	}

	out.ID = row.publicID
	out.Type = row.jobType
	out.Attempts = row.attempts
	out.Reason = row.reason.String
	out.RequestedByEmail = row.requestedBy.String
	out.ResultCode = row.resultCode.String
	out.Error = row.errorMessage.String

	if len(row.payload) > 0 {
		out.Params = json.RawMessage(row.payload)
	}
	if len(row.result) > 0 {
		out.Result = json.RawMessage(row.result)
	}

	queued := row.createdAt
	out.QueuedAt = &queued
	out.DeliveredAt = row.deliveredAt
	out.AcknowledgedAt = row.acknowledged
	out.ExpiresAt = row.expiresAt

	switch row.status {
	case "COMPLETED":
		// THE ONLY STATE THAT MEANS THE TERMINAL SPOKE. It is evidence the
		// command was received and acknowledged; the result code is the only
		// evidence of what was done with it.
		out.State = models.CommandStateAccepted
	case "FAILED":
		out.State = models.CommandStateFailed
	case "CANCELLED":
		out.State = models.CommandStateCancelled
	default:
		switch {
		case row.expiresAt != nil && !time.Now().Before(*row.expiresAt):
			out.State = models.CommandStateExpired
		case row.deliveredAt != nil:
			// Collected. Fetching stamps delivered_at without changing the
			// status, so this is the only evidence the platform has that the
			// terminal HAS the command -- and it is short of evidence that it
			// applied it.
			out.State = models.CommandStateDelivered
		default:
			out.State = models.CommandStateQueued
		}
	}
	return out
}
