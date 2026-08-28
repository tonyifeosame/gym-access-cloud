-- ---------------------------------------------------------------------------
-- 027: operator-driven fingerprint enrolment
-- ---------------------------------------------------------------------------
--
-- WHAT WAS MISSING, said plainly.
--
-- Creating a person left them with no credential -- correctly, because
-- biometric material is captured at a sensor and never travels. Binding a
-- finger to them then required somebody with a USB cable typing
-- `enroll <member> <name>` into the serial console of the right terminal. At a
-- site with one door that is awkward. At a site with four it is a guess, and
-- the failure is silent: the person exists, is active, and opens nothing, with
-- no surface anywhere saying why.
--
-- `enrollment_requests` has existed since 001 and was never an instruction. It
-- recorded that somebody wanted an enrolment; nothing delivered that intent to
-- a terminal, so `POST /enrollment/start` created a row a device never saw.
--
-- The firmware side of the fix already ships. It reads an ENROLL_FINGERPRINT
-- job from the jobs endpoint it already polls, refuses any job whose
-- `serial_number` is not its own, enters enrolment mode by itself, and
-- acknowledges only once a finger has actually been captured. This migration is
-- the platform's half: the job type, and the enrolment row becoming a real
-- lifecycle addressed to one terminal.
--
-- ---------------------------------------------------------------------------
-- WHY A sync_jobs ROW RATHER THAN A NEW QUEUE
-- ---------------------------------------------------------------------------
--
-- sync_jobs is already a per-device outbox with delivery leases, acknowledgement
-- constraints, attempt caps and backlog compaction, and `GetPendingJobsForDevice`
-- already filters on `device_id`. "Only the selected terminal may execute this"
-- is therefore not a new rule to write -- it is the rule that table has always
-- enforced. `AckJobCompleted` and `AckJobFailed` both carry
-- `AND device_id = $2`, so another terminal cannot acknowledge this job either.
--
-- A second queue would have meant re-deriving all of that, differently.

BEGIN;

-- ---------------------------------------------------------------------------
-- The job type
-- ---------------------------------------------------------------------------
--
-- Additive, and it does NOT bump SyncProtocolVersion: the sync protocol names a
-- new job type as the extension path, and firmware older than this reports it
-- as unknown and acknowledges it rather than stalling.
--
-- THE LIST BELOW IS THE UNION OF EVERY JOB TYPE, not this feature's half
-- of it.
--
-- PostgreSQL has no "add one value to a CHECK": every migration that touches
-- this constraint restates the whole list, and whichever ran last decides what
-- the table accepts. 024 added WIFI_RECOVERY exactly this way. Restating only
-- the types this feature knows about would silently revoke Wi-Fi recovery --
-- and, on a database where a WIFI_RECOVERY row already exists, would fail this
-- migration outright when ADD CONSTRAINT revalidates the table.
--
-- So this is 024's list plus ENROLL_FINGERPRINT. A later migration adding a
-- job type owes the same debt to this one.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_type_check CHECK (job_type IN (
    -- entity change operations (Sprint 4)
    'CREATE', 'UPDATE', 'DELETE', 'SETTINGS',
    -- operational jobs (Sprint 2)
    'FULL_SYNC', 'INCREMENTAL_SYNC', 'PERMISSION_PUSH',
    'TEMPLATE_PUSH', 'FIRMWARE_UPDATE', 'LOG_PULL',
    -- operator commands (024)
    'WIFI_RECOVERY',
    -- operator-driven enrolment (this migration)
    'ENROLL_FINGERPRINT'
));

-- AN ENROLMENT IS ALWAYS ADDRESSED TO EXACTLY ONE DEVICE, and this constraint is
-- where that becomes impossible to get wrong rather than merely intended.
--
-- A NULL device_id on this job type would be a site-wide enrolment: every
-- terminal at the site entering enrolment mode for one person, racing to capture
-- whichever finger reached a platen first. The operator picked a door; the
-- schema refuses to store a job that forgot which.
--
-- WIFI_RECOVERY IS CARRIED FORWARD FROM 024, for the same reason and by the
-- same rule as the type list above: 024 joined it to this constraint because a
-- recovery command with a null device_id would put every door at a site into
-- setup mode at once. Dropping it here would restore precisely that.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_change_device_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_change_device_check CHECK (
    job_type NOT IN ('CREATE', 'UPDATE', 'DELETE', 'SETTINGS', 'WIFI_RECOVERY',
                     'ENROLL_FINGERPRINT')
    OR device_id IS NOT NULL
);

-- ---------------------------------------------------------------------------
-- enrollment_requests becomes a lifecycle
-- ---------------------------------------------------------------------------

-- The job that carries this enrolment to the terminal.
--
-- ON DELETE SET NULL rather than CASCADE: jobs are pruned by age and an
-- enrolment that completed six months ago is still a true record of what
-- happened. Losing the history because the delivery row aged out would be the
-- outbox's retention policy quietly editing the audit trail.
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS sync_job_id BIGINT;

ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_sync_job_id_fkey;
ALTER TABLE enrollment_requests ADD CONSTRAINT enrollment_requests_sync_job_id_fkey
    FOREIGN KEY (sync_job_id) REFERENCES sync_jobs(id) ON DELETE SET NULL;

-- When the window closes. The terminal holds its own copy of this and fails the
-- job when it runs out; the platform holds it so an enrolment nobody actioned
-- stops being reported as live even if the terminal never came back to say so.
--
-- NULLABLE, because rows written before this migration never had one and
-- inventing a deadline for them would expire history.
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- Who asked for it, denormalised alongside the reference for the reason
-- audit_events denormalises its actor: an operator account can be deleted, and
-- the record of what they set in motion must stay readable when it is.
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS requested_by BIGINT;
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS requested_by_email VARCHAR(255);

ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_requested_by_fkey;
ALTER TABLE enrollment_requests ADD CONSTRAINT enrollment_requests_requested_by_fkey
    FOREIGN KEY (requested_by) REFERENCES users(id) ON DELETE SET NULL;

-- The terminal's own words about why it could not enrol -- "sensor error",
-- "the window closed with nobody at the door" -- so the console shows something
-- actionable rather than "failed".
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS error_message TEXT;

-- When the selected terminal actually started. Distinguishes "queued, the
-- terminal has not polled yet" from "the terminal is showing the prompt", which
-- are the two states an operator watching this screen most needs to tell apart.
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- The status set
-- ---------------------------------------------------------------------------
--
-- Normalised BEFORE the constraint is added. The application only ever wrote the
-- first four, but this migration must not fail on a row a future or a
-- hand-edited database happens to hold -- and a migration that aborts half way
-- through a deployment is worse than one that records what it could not read.
UPDATE enrollment_requests
   SET status = 'FAILED',
       error_message = COALESCE(error_message, 'status ' || status || ' predates 027')
 WHERE status NOT IN ('PENDING', 'IN_PROGRESS', 'COMPLETED', 'FAILED',
                      'EXPIRED', 'CANCELLED');

ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_status_check;
ALTER TABLE enrollment_requests ADD CONSTRAINT enrollment_requests_status_check CHECK (
    status IN ('PENDING', 'IN_PROGRESS', 'COMPLETED', 'FAILED', 'EXPIRED', 'CANCELLED')
);

-- ---------------------------------------------------------------------------
-- One live enrolment per person
-- ---------------------------------------------------------------------------
--
-- THE RULE THAT KEEPS "WHICH TERMINAL" MEANINGFUL. Two live enrolments for one
-- person would be two terminals waiting to bind the same finger, and whichever
-- captured first would leave the other armed for somebody who is no longer
-- coming -- a reader that binds the next finger it sees to a name.
--
-- Retrying is not blocked by this: starting a new enrolment supersedes the
-- outstanding one in the same transaction, which is what makes "retry at a
-- different terminal" a single operator action rather than two.
--
-- Older rows are superseded first, oldest-cancelled, so the index can be built
-- on a database that already holds duplicates.
UPDATE enrollment_requests er
   SET status = 'CANCELLED',
       completed_at = COALESCE(completed_at, CURRENT_TIMESTAMP),
       error_message = COALESCE(error_message, 'superseded when 027 introduced one live enrolment per person')
 WHERE er.status IN ('PENDING', 'IN_PROGRESS')
   AND EXISTS (
       SELECT 1 FROM enrollment_requests newer
        WHERE newer.person_id = er.person_id
          AND newer.status IN ('PENDING', 'IN_PROGRESS')
          AND newer.id > er.id
   );

CREATE UNIQUE INDEX IF NOT EXISTS enrollment_requests_one_live_per_person
    ON enrollment_requests(person_id)
    WHERE status IN ('PENDING', 'IN_PROGRESS');

-- The sweep that expires windows nobody actioned reads exactly this.
CREATE INDEX IF NOT EXISTS idx_enrollment_requests_expiring
    ON enrollment_requests(expires_at)
    WHERE status IN ('PENDING', 'IN_PROGRESS');

-- "What is this terminal being asked to enrol", for the terminal detail view.
CREATE INDEX IF NOT EXISTS idx_enrollment_requests_device
    ON enrollment_requests(device_id, status);

COMMIT;
