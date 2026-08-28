package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"access-terminal-cloud-api/models"
)

// Operator-driven fingerprint enrolment (migrations/022_fingerprint_enrollment.sql).
//
// An enrolment is TWO ROWS WRITTEN TOGETHER: a `sync_jobs` row that carries the
// instruction to one terminal, and an `enrollment_requests` row that is the
// lifecycle an operator watches. They are created in one transaction and they
// are retired together, because either one alone is a lie -- a job with no
// enrolment is an armed reader nobody is tracking, and an enrolment with no job
// is a screen that says "waiting for terminal" for ever.
//
// ---------------------------------------------------------------------------
// WHY THE ADDRESSING IS NOT NEW CODE
// ---------------------------------------------------------------------------
//
// "Only the selected terminal may execute this job" is enforced in four places,
// none of which was written for this feature:
//
//	GetPendingJobsForDevice   WHERE device_id = $1   -- only that terminal is offered it
//	AckJobCompleted           AND device_id = $2     -- only that terminal may complete it
//	AckJobFailed              AND device_id = $2     -- only that terminal may fail it
//	sync_jobs_change_device_check                    -- the row cannot exist unaddressed
//
// The fifth is on the terminal: the firmware refuses a job whose
// `serial_number` is not its own before it will enter enrolment mode at all.
// A new queue would have meant re-deriving every one of those, differently.

// ErrEnrollmentNotFound is returned when a person has no enrolment to act on.
var ErrEnrollmentNotFound = errors.New("no enrolment for this person")

// ErrTerminalNotEnrollable is returned when the chosen terminal cannot be asked
// to capture a finger.
//
// A DISTINCT ERROR FROM "not found", because the two send an operator to
// different places: one means they picked hardware that is not theirs, the
// other means they picked hardware that is disabled, retired, or has never been
// issued a credential. The reason travels with it.
var ErrTerminalNotEnrollable = errors.New("terminal cannot perform an enrolment")

// EnrollmentTarget is a terminal considered as somewhere a person could stand.
type EnrollmentTarget struct {
	DeviceID     int64
	SiteID       int64
	SerialNumber string
	DeviceName   string
	SiteName     string
	SitePublicID string
	Status       string
}

// StartEnrollmentInput is one operator asking one terminal to capture a finger.
type StartEnrollmentInput struct {
	CompanyID int64

	// DeviceID comes from RequireTerminalGrant, which has already resolved the
	// serial inside the operator's company and applied their site grant. It is
	// NOT re-resolved here: one resolution, one authorization, no chance of the
	// two disagreeing about which terminal was authorised.
	DeviceID int64

	ExternalID       string
	ExpiresInSeconds int

	ActorUserID int64
	ActorEmail  string
}

// StartFingerprintEnrollment queues an ENROLL_FINGERPRINT job for one terminal
// and opens the enrolment an operator watches.
//
// SUPERSEDES rather than refuses when the person already has a live enrolment.
// That is what makes "retry", and "retry at a different terminal", one operator
// action instead of two -- and it is what keeps the one-live-per-person index
// satisfiable. The superseded job is CANCELLED, so the terminal that was holding
// it can no longer complete it: AckJobCompleted refuses a cancelled job, which
// is behaviour that already existed and is relied on here.
func StartFingerprintEnrollment(input StartEnrollmentInput) (*models.ConsoleEnrollment, error) {
	window := input.ExpiresInSeconds
	if window <= 0 {
		window = models.DefaultEnrollmentWindowSeconds
	}
	if window < models.MinEnrollmentWindowSeconds {
		window = models.MinEnrollmentWindowSeconds
	}
	if window > models.MaxEnrollmentWindowSeconds {
		window = models.MaxEnrollmentWindowSeconds
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The terminal, re-read for its serial and its state.
	//
	// THE AUTHORIZATION IS NOT REPEATED HERE -- it happened in the middleware --
	// but the ELIGIBILITY is, and the two are different questions. A terminal an
	// operator is allowed to command can still be one that cannot capture
	// anything, and asking it to would leave a customer standing at a dead door.
	target, err := enrollmentTargetTx(tx, input.CompanyID, input.DeviceID)
	if err != nil {
		return nil, err
	}

	// The person, inside the caller's company. Locked for the length of the
	// transaction so two operators clicking at once cannot both supersede the
	// other and leave two live enrolments.
	var personID int64
	var fullName string
	var enrolled bool
	err = tx.QueryRow(`
		SELECT id, full_name, COALESCE(fingerprint_template, '') <> ''
		  FROM people
		 WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL
		 FOR UPDATE`,
		input.ExternalID, input.CompanyID).Scan(&personID, &fullName, &enrolled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	// Supersede whatever was outstanding, job and enrolment together.
	if err := supersedeLiveEnrollmentTx(tx, personID,
		"superseded by a newer enrolment request"); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(models.EnrollmentJobPayload{
		MemberID:         input.ExternalID,
		FullName:         fullName,
		SerialNumber:     target.SerialNumber,
		ExpiresInSeconds: window,
	})
	if err != nil {
		return nil, err
	}

	// max_attempts = 1, and this is the one job type that wants it.
	//
	// Every other job is idempotent and worth retrying: a redelivered CREATE is
	// an upsert. An enrolment is an APPOINTMENT. A terminal that reports FAILED
	// has already asked somebody to present a finger and been unable to capture
	// it, and re-offering the same job on a backoff would re-arm that reader
	// minutes later with nobody there -- for a person the operator may by then
	// have enrolled somewhere else. Retrying is an operator decision, because
	// only they know whether the customer is still in the building.
	var jobID int64
	err = tx.QueryRow(`
		INSERT INTO sync_jobs
		    (site_id, device_id, job_type, entity_type, entity_id, entity_external_id,
		     payload, protocol_version, status, max_attempts, next_attempt_at)
		VALUES ($1, $2, $3, 'PERSON', $4, $5, $6::jsonb, $7, 'PENDING', 1, CURRENT_TIMESTAMP)
		RETURNING id`,
		target.SiteID, target.DeviceID, models.SyncJobEnrollFingerprint,
		personID, input.ExternalID, payload, models.SyncProtocolVersion).Scan(&jobID)
	if err != nil {
		return nil, err
	}

	var enrollmentID string
	var createdAt time.Time
	var expiresAt time.Time
	err = tx.QueryRow(`
		INSERT INTO enrollment_requests
		    (person_id, device_id, sync_job_id, status, expires_at,
		     requested_by, requested_by_email)
		VALUES ($1, $2, $3, 'PENDING',
		        -- EVERY PARAMETER IS CAST EXPLICITLY. PostgreSQL infers a
		        -- placeholder's type from its context, and concatenating one
		        -- onto a string to build an interval gives it a text operator to
		        -- infer from against an integer argument -- which fails at
		        -- prepare time rather than in review. Multiplying a typed
		        -- interval has no such ambiguity.
		        CURRENT_TIMESTAMP + ($4::int * INTERVAL '1 second'),
		        NULLIF($5::bigint, 0), NULLIF($6::text, ''))
		RETURNING public_id::text, created_at, expires_at`,
		personID, target.DeviceID, jobID, window,
		input.ActorUserID, input.ActorEmail).Scan(&enrollmentID, &createdAt, &expiresAt)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &models.ConsoleEnrollment{
		ID:                enrollmentID,
		Status:            models.EnrollmentPending,
		ExternalID:        input.ExternalID,
		FullName:          fullName,
		TerminalSerial:    target.SerialNumber,
		TerminalName:      target.DeviceName,
		SiteName:          target.SiteName,
		SitePublicID:      target.SitePublicID,
		TerminalStatus:    target.Status,
		RequestedByEmail:  input.ActorEmail,
		CreatedAt:         createdAt,
		ExpiresAt:         &expiresAt,
		BiometricEnrolled: enrolled,
	}, nil
}

// enrollmentTargetTx reads the chosen terminal and decides whether it can be
// asked to capture a finger.
//
// THE RULE, and each clause is a way an enrolment would otherwise be sent to a
// door that cannot answer:
//
//   - not soft-deleted: a retired terminal is gone.
//   - active and not DISABLED: an operator took it out of service, and it
//     refuses every authenticated call including the jobs poll.
//   - holds a device credential: a terminal that has never registered, or whose
//     credential was revoked, cannot authenticate to fetch the job at all.
//
// OFFLINE IS NOT ON THAT LIST, deliberately. A terminal that is merely
// unreachable right now is a terminal that will pick the job up when it comes
// back, and refusing to queue one would make this feature unusable at exactly
// the sites that most need it. The console says the door is offline and lets the
// operator decide.
func enrollmentTargetTx(tx *sql.Tx, companyID, deviceID int64) (*EnrollmentTarget, error) {
	var target EnrollmentTarget
	var active bool
	var hasCredential bool

	err := tx.QueryRow(`
		SELECT d.id, d.site_id, d.serial_number, COALESCE(d.device_name, ''),
		       COALESCE(s.site_name, ''), s.public_id::text, d.status, d.active,
		       d.api_key_hash IS NOT NULL AND d.api_key_hash <> ''
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		 WHERE d.id = $1
		   AND s.company_id = $2
		   AND d.deleted_at IS NULL
		   AND s.deleted_at IS NULL`,
		deviceID, companyID).Scan(
		&target.DeviceID, &target.SiteID, &target.SerialNumber, &target.DeviceName,
		&target.SiteName, &target.SitePublicID, &target.Status, &active, &hasCredential)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}

	if !active || target.Status == models.DeviceDisabled {
		return nil, ErrTerminalNotEnrollable
	}
	if !hasCredential {
		return nil, ErrTerminalNotEnrollable
	}
	return &target, nil
}

// supersedeLiveEnrollmentTx retires a person's outstanding enrolment, and the
// job carrying it, without touching the person.
//
// THE JOB IS CANCELLED, NOT DELETED. A cancelled job is one AckJobCompleted
// refuses to complete -- that guard has been in place since the sync engine was
// written -- so a terminal that already holds this job cannot report success
// against it afterwards. That is what makes cancellation meaningful against
// hardware that has already been handed the work.
func supersedeLiveEnrollmentTx(tx *sql.Tx, personID int64, reason string) error {
	rows, err := tx.Query(`
		UPDATE enrollment_requests
		   SET status = 'CANCELLED',
		       completed_at = CURRENT_TIMESTAMP,
		       error_message = $2
		 WHERE person_id = $1
		   AND status IN ('PENDING', 'IN_PROGRESS')
		RETURNING sync_job_id`, personID, reason)
	if err != nil {
		return err
	}

	var jobIDs []int64
	for rows.Next() {
		var jobID sql.NullInt64
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		if jobID.Valid {
			jobIDs = append(jobIDs, jobID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, jobID := range jobIDs {
		// Only a job still in flight is cancelled. One the terminal already
		// completed stays COMPLETED -- rewriting it would contradict an
		// acknowledgement the device made and break sync_jobs_ack_check.
		if _, err := tx.Exec(`
			UPDATE sync_jobs
			   SET status = 'CANCELLED',
			       error_message = $2,
			       completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
			 WHERE id = $1 AND status IN ('PENDING', 'FAILED')`, jobID, reason); err != nil {
			return err
		}
	}
	return nil
}

// enrollmentColumns is the shared projection. One spelling, so the list, the
// read and the write-through response cannot disagree about what an enrolment
// is.
const enrollmentColumns = `
	er.public_id::text,
	er.status,
	p.external_id,
	p.full_name,
	COALESCE(d.serial_number, ''),
	COALESCE(d.device_name, ''),
	COALESCE(s.site_name, ''),
	COALESCE(s.public_id::text, ''),
	COALESCE(d.status, ''),
	COALESCE(er.error_message, ''),
	COALESCE(er.requested_by_email, ''),
	er.created_at,
	er.expires_at,
	er.started_at,
	er.completed_at,
	COALESCE(p.fingerprint_template, '') <> ''`

func scanEnrollment(scan func(dest ...any) error) (*models.ConsoleEnrollment, error) {
	var e models.ConsoleEnrollment
	var expiresAt, startedAt, completedAt sql.NullTime

	if err := scan(&e.ID, &e.Status, &e.ExternalID, &e.FullName,
		&e.TerminalSerial, &e.TerminalName, &e.SiteName, &e.SitePublicID,
		&e.TerminalStatus, &e.ErrorMessage, &e.RequestedByEmail,
		&e.CreatedAt, &expiresAt, &startedAt, &completedAt,
		&e.BiometricEnrolled); err != nil {
		return nil, err
	}

	if expiresAt.Valid {
		e.ExpiresAt = &expiresAt.Time
	}
	if startedAt.Valid {
		e.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		e.CompletedAt = &completedAt.Time
	}
	return &e, nil
}

// LatestEnrollmentForPerson returns the enrolment a console should show.
//
// THE MOST RECENT ONE, not the most recent LIVE one. A screen that showed
// nothing once an enrolment failed would give an operator no way to find out
// what went wrong -- which is the single most useful thing this endpoint
// returns.
//
// Expiry is applied on read. See expireDueEnrollmentsTx: a window that has run
// out is EXPIRED the moment anybody looks, rather than staying PENDING until a
// terminal that may never come back says otherwise.
func LatestEnrollmentForPerson(companyID int64, externalID string) (*models.ConsoleEnrollment, error) {
	if err := ExpireDueEnrollments(); err != nil {
		return nil, err
	}

	enrollment, err := scanEnrollment(DB.QueryRow(`
		SELECT `+enrollmentColumns+`
		  FROM enrollment_requests er
		  JOIN people p ON p.id = er.person_id
		  LEFT JOIN devices d ON d.id = er.device_id
		  LEFT JOIN sites s ON s.id = d.site_id
		 WHERE p.external_id = $1
		   AND p.company_id = $2
		   AND p.deleted_at IS NULL
		 ORDER BY er.id DESC
		 LIMIT 1`, externalID, companyID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEnrollmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return enrollment, nil
}

// CancelFingerprintEnrollment stops a person's live enrolment.
//
// Returns ErrEnrollmentNotFound when there is nothing live to stop, which
// includes an enrolment that has already completed. Cancelling a finished
// enrolment is not an error the operator can act on and it must not un-enrol
// anybody -- the credential is on the sensor either way.
func CancelFingerprintEnrollment(companyID int64, externalID, reason string) (*models.ConsoleEnrollment, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var personID int64
	err = tx.QueryRow(`
		SELECT id FROM people
		 WHERE external_id = $1 AND company_id = $2 AND deleted_at IS NULL
		 FOR UPDATE`, externalID, companyID).Scan(&personID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrPersonNotFound
	}
	if err != nil {
		return nil, err
	}

	var live bool
	if err := tx.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM enrollment_requests
		                WHERE person_id = $1 AND status IN ('PENDING', 'IN_PROGRESS'))`,
		personID).Scan(&live); err != nil {
		return nil, err
	}
	if !live {
		return nil, ErrEnrollmentNotFound
	}

	if err := supersedeLiveEnrollmentTx(tx, personID, reason); err != nil {
		return nil, err
	}

	enrollment, err := scanEnrollment(tx.QueryRow(`
		SELECT `+enrollmentColumns+`
		  FROM enrollment_requests er
		  JOIN people p ON p.id = er.person_id
		  LEFT JOIN devices d ON d.id = er.device_id
		  LEFT JOIN sites s ON s.id = d.site_id
		 WHERE er.person_id = $1
		 ORDER BY er.id DESC
		 LIMIT 1`, personID).Scan)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return enrollment, nil
}

// ExpireDueEnrollments closes windows that have run out.
//
// CALLED ON READ rather than only from a sweep, and the difference matters for
// what an operator sees. A terminal that took the job and then lost power never
// reports anything, so an enrolment whose window has passed would otherwise sit
// at "waiting" for ever -- and the person could not be enrolled anywhere else,
// because the one-live-per-person rule would still be holding their slot.
//
// The job is cancelled with it, so the terminal cannot complete it afterwards.
func ExpireDueEnrollments() error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := expireDueEnrollmentsTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func expireDueEnrollmentsTx(tx *sql.Tx) error {
	rows, err := tx.Query(`
		UPDATE enrollment_requests
		   SET status = 'EXPIRED',
		       completed_at = CURRENT_TIMESTAMP,
		       error_message = COALESCE(NULLIF(error_message, ''),
		                                'the enrolment window closed with nobody at the terminal')
		 WHERE status IN ('PENDING', 'IN_PROGRESS')
		   AND expires_at IS NOT NULL
		   AND expires_at <= CURRENT_TIMESTAMP
		RETURNING sync_job_id`)
	if err != nil {
		return err
	}

	var jobIDs []int64
	for rows.Next() {
		var jobID sql.NullInt64
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		if jobID.Valid {
			jobIDs = append(jobIDs, jobID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, jobID := range jobIDs {
		if _, err := tx.Exec(`
			UPDATE sync_jobs
			   SET status = 'CANCELLED',
			       error_message = 'the enrolment window expired',
			       completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
			 WHERE id = $1 AND status IN ('PENDING', 'FAILED')`, jobID); err != nil {
			return err
		}
	}
	return nil
}

// MarkEnrollmentDelivered records that the selected terminal has the job.
//
// "Waiting for terminal" and "the terminal is showing the prompt" are the two
// states an operator watching this screen most needs to tell apart -- one means
// the network has not turned over yet, the other means the person should be at
// that door NOW. Nothing else in the system could distinguish them: a job stays
// PENDING while it is being applied, deliberately, so its status says nothing
// about whether a device has seen it.
//
// Called from the jobs fetch, for the enrolment jobs in a batch. Best effort:
// failing a device's job poll because a progress field could not be written
// would break sync to fix a label.
func MarkEnrollmentDelivered(deviceID int64, jobIDs []int64) error {
	if len(jobIDs) == 0 {
		return nil
	}
	for _, jobID := range jobIDs {
		if _, err := DB.Exec(`
			UPDATE enrollment_requests
			   SET status = 'IN_PROGRESS',
			       started_at = COALESCE(started_at, CURRENT_TIMESTAMP)
			 WHERE sync_job_id = $1
			   AND device_id = $2
			   AND status = 'PENDING'`, jobID, deviceID); err != nil {
			return err
		}
	}
	return nil
}

// FailEnrollmentForJob records a terminal's FAILED acknowledgement against the
// enrolment an operator is watching.
//
// The terminal's own words are kept. "Sensor error" and "the window closed with
// nobody at the door" send somebody to two different places, and a console that
// flattened both to "failed" would send them to neither.
func FailEnrollmentForJob(deviceID, jobID int64, reason string) error {
	if reason == "" {
		reason = "the terminal could not complete the enrolment"
	}
	_, err := DB.Exec(`
		UPDATE enrollment_requests
		   SET status = 'FAILED',
		       completed_at = CURRENT_TIMESTAMP,
		       error_message = $3
		 WHERE sync_job_id = $1
		   AND device_id = $2
		   AND status IN ('PENDING', 'IN_PROGRESS')`, jobID, deviceID, reason)
	return err
}

// EnrollmentDisposition is what a device's enrolment report should be allowed
// to do.
type EnrollmentDisposition struct {
	// Bind is false when the platform must NOT write a credential for this
	// report.
	Bind bool

	// Reason is why, when Bind is false, in words fit for an event.
	Reason string

	// EnrollmentID is the enrolment this report belongs to, if any.
	EnrollmentID string
}

// DispositionForDeviceReport decides whether an enrolment result from a device
// may bind a credential.
//
// ---------------------------------------------------------------------------
// THE ONE RULE, AND WHY IT IS ONLY ONE
// ---------------------------------------------------------------------------
//
// A report is REFUSED when the most recent enrolment for that person AT THAT
// TERMINAL was CANCELLED. Everything else binds.
//
//   - No enrolment at all -> BIND. This is the bench path: a technician enrolling
//     at the console of a terminal, with no job and no platform involvement.
//     That path predates this feature, is the documented fallback when the
//     network is down, and breaking it would take away the only way to enrol
//     during an outage.
//
//   - A live enrolment -> BIND. The ordinary case.
//
//   - EXPIRED or FAILED -> BIND. The terminal captured a finger. It is on that
//     sensor whatever the platform's clock decided a moment earlier, and
//     refusing the report would leave the platform believing the person is not
//     enrolled at a door that will admit them. Divergence between the record and
//     the hardware is the failure mode this whole subsystem exists to prevent.
//
//   - CANCELLED -> REFUSE. This is the only case where a human said "stop", and
//     it is the case requirement 7 is about: a cancelled enrolment must not
//     later result in a fingerprint being bound. The template may exist on the
//     sensor, and the console says so rather than pretending otherwise -- but
//     the platform does not adopt it as a credential, and the person stays Not
//     enrolled until somebody enrols them deliberately.
func DispositionForDeviceReport(companyID, deviceID int64, externalID string) (EnrollmentDisposition, error) {
	var status, enrollmentID string
	err := DB.QueryRow(`
		SELECT er.status, er.public_id::text
		  FROM enrollment_requests er
		  JOIN people p ON p.id = er.person_id
		 WHERE p.external_id = $1
		   AND p.company_id = $2
		   AND er.device_id = $3
		 ORDER BY er.id DESC
		 LIMIT 1`, externalID, companyID, deviceID).Scan(&status, &enrollmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollmentDisposition{Bind: true}, nil
	}
	if err != nil {
		return EnrollmentDisposition{}, err
	}

	if status == models.EnrollmentCancelled {
		return EnrollmentDisposition{
			Bind:         false,
			Reason:       "the enrolment was cancelled before the terminal reported it",
			EnrollmentID: enrollmentID,
		}, nil
	}
	return EnrollmentDisposition{Bind: true, EnrollmentID: enrollmentID}, nil
}

// CompleteEnrollmentForDevice retires the enrolment a terminal just fulfilled.
//
// Scoped to the reporting device as well as the person, so a terminal reporting
// an enrolment cannot close one addressed to a different door.
func CompleteEnrollmentForDevice(companyID, deviceID int64, externalID string) error {
	_, err := DB.Exec(`
		UPDATE enrollment_requests er
		   SET status = 'COMPLETED',
		       completed_at = CURRENT_TIMESTAMP,
		       error_message = NULL
		  FROM people p
		 WHERE p.id = er.person_id
		   AND p.external_id = $1
		   AND p.company_id = $2
		   AND er.device_id = $3
		   AND er.status IN ('PENDING', 'IN_PROGRESS', 'EXPIRED', 'FAILED')`,
		externalID, companyID, deviceID)
	return err
}
