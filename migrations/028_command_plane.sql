-- ---------------------------------------------------------------------------
-- 028: the remote command plane
-- ---------------------------------------------------------------------------
--
-- WHAT THIS IS, AND WHY IT IS NOT A NEW MECHANISM. There is exactly one channel
-- from the platform to a terminal -- sync_jobs, delivered by GET /devices/jobs,
-- retired by the acknowledgement posted to /devices/jobs/:id/complete, leased
-- and retried by machinery that has been in production since Sprint 4. This
-- migration does not add a second one. It gives the rows in that table a
-- CLASSIFICATION they have always implicitly had, and attaches to the class the
-- five properties a command needs and a state snapshot does not.
--
-- ---------------------------------------------------------------------------
-- THE DISTINCTION: STATE VERSUS COMMAND
-- ---------------------------------------------------------------------------
--
-- A STATE job is declarative. It says what the terminal should HOLD, and
-- re-applying it is a no-op by construction: CREATE and UPDATE are upserts,
-- DELETE is a delete-if-present, SETTINGS is gated on a monotonic version, and
-- FULL_SYNC is a set-difference. Delivering one late is merely late.
--
-- A COMMAND job is an event. It says what the terminal should DO, and applying
-- it twice does it twice. Delivering one late is not late, it is wrong: 024
-- worked this out the hard way for WIFI_RECOVERY, where a command that sat in
-- the queue while the customer recovered the terminal by hand would arrive after
-- they had re-provisioned it and wipe the Wi-Fi they had just typed in.
--
-- Both halves of that reasoning are already written down -- in 024, in
-- include/sync_job.h's note on kWifiRecovery, and again in 027 for
-- ENROLL_FINGERPRINT. What has never existed is a place to WRITE THE ANSWER
-- DOWN, so each new command type has had to rediscover it. WIFI_RECOVERY got a
-- validity window, a one-outstanding index and a capability gate.
-- ENROLL_FINGERPRINT, added three migrations later by the same reasoning in the
-- same file, got none of the three. That is not a lapse anybody stops making by
-- being more careful; it is what happens when a class of thing has no name.
--
-- So COMMAND becomes a column, and the properties become constraints on it.
--
-- ---------------------------------------------------------------------------
-- WHAT IS DELIBERATELY NOT CHANGED
-- ---------------------------------------------------------------------------
--
-- EVERY EXISTING BEHAVIOUR IS PRESERVED EXACTLY, including two this migration
-- can see are inconsistent and does not fix:
--
--   * WIFI_RECOVERY keeps its own one-outstanding index. The generalised index
--     below deliberately EXCLUDES it rather than replacing it, so its behaviour
--     is bit-for-bit what it was and its tests pass unmodified.
--
--   * ENROLL_FINGERPRINT is classified as a COMMAND -- which it is -- but is
--     given NO validity window and is NOT added to the one-outstanding index.
--     It has neither today. Giving it either would change how a shipped feature
--     behaves, which is a separate decision with its own tests, and smuggling it
--     in under a schema change is exactly how a working feature breaks.
--
-- The classification is therefore descriptive for the two existing commands and
-- prescriptive for every new one. Phase 0 of the plan closes the gap.
--
-- ---------------------------------------------------------------------------
-- THE CONSTRAINT-RESTATEMENT TRAP
-- ---------------------------------------------------------------------------
--
-- sync_jobs_type_check and sync_jobs_change_device_check are NOT ADDITIVE.
-- Every migration that touches them DROPs and re-CREATEs the whole list, so a
-- migration that names only its own types silently revokes everyone else's.
-- This nearly shipped: the enrolment branch numbered itself 022, and as written
-- would have revoked WIFI_RECOVERY, which 024 had added on the deployed line.
--
-- Both constraints below therefore restate EVERY type, and command_plane_test.go
-- asserts the live constraint's contents against a canonical list in Go -- so a
-- future migration that forgets one fails a test rather than a door.

BEGIN;

-- ---------------------------------------------------------------------------
-- The job-type vocabulary, restated whole
-- ---------------------------------------------------------------------------
--
-- TWO TYPES ARE ADDED and five inherited ones are carried forward untouched.
--
-- INCREMENTAL_SYNC, PERMISSION_PUSH, TEMPLATE_PUSH, FIRMWARE_UPDATE and
-- LOG_PULL are Sprint-2 vocabulary that no Go code has ever enqueued and no
-- firmware has ever recognised -- syncJobTypeFromName has no case for any of
-- them, so one would parse as kUnknown, be acknowledged, and be discarded.
-- They are KEPT rather than dropped, for one reason: dropping a value from a
-- CHECK constraint is irreversible against rows that already hold it, and
-- although none should exist, "should" is not a thing to bet a deployed
-- database on. They are marked below so nobody mistakes them for live protocol,
-- and models.SyncJobReserved names them on the Go side.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_type_check CHECK (job_type IN (
    -- entity change operations (Sprint 4) -- STATE
    'CREATE', 'UPDATE', 'DELETE', 'SETTINGS',
    -- operational jobs (Sprint 2) -- STATE
    'FULL_SYNC',
    -- RESERVED. Never enqueued, never parsed by any firmware. Do not use
    -- without giving them semantics on both sides first.
    'INCREMENTAL_SYNC', 'PERMISSION_PUSH', 'TEMPLATE_PUSH', 'FIRMWARE_UPDATE',
    'LOG_PULL',
    -- operator commands (024) -- COMMAND
    'WIFI_RECOVERY',
    -- operator-driven enrolment (027) -- COMMAND
    'ENROLL_FINGERPRINT',
    -- the command plane (this migration) -- COMMAND
    'DIAGNOSTIC_SNAPSHOT', 'DEVICE_TEST'
));

-- ---------------------------------------------------------------------------
-- Addressed to exactly one device
-- ---------------------------------------------------------------------------
--
-- Restated whole, with the two new types joined to it. A command fanned out to
-- a site because device_id happened to be NULL is the worst outcome any of
-- these types has available -- 024 says so about setup mode, and it is more
-- true of a command that pulses a strike. The COMMAND-completeness constraint
-- below repeats the device_id requirement from the other direction, so a new
-- command type that is forgotten here is still caught there.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_change_device_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_change_device_check CHECK (
    job_type NOT IN ('CREATE', 'UPDATE', 'DELETE', 'SETTINGS', 'WIFI_RECOVERY',
                     'ENROLL_FINGERPRINT', 'DIAGNOSTIC_SNAPSHOT', 'DEVICE_TEST')
    OR device_id IS NOT NULL
);

-- ---------------------------------------------------------------------------
-- The command columns
-- ---------------------------------------------------------------------------
--
-- Every one is nullable or defaulted, so every existing row stays valid under
-- every constraint added below without being rewritten.

-- 'STATE' or 'COMMAND'. NULL is neither, and means a row written before this
-- migration by a code path that has not been taught the classification --
-- which, after the backfill at the bottom, is nothing.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS command_class VARCHAR(8);

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_command_class_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_command_class_check
    CHECK (command_class IS NULL OR command_class IN ('STATE', 'COMMAND'));

-- The version of the COMMAND ENVELOPE, which is not the sync protocol version.
--
-- TWO SEPARATE NUMBERS ON PURPOSE. protocol_version describes the transport --
-- the shape of the jobs response and the acknowledgement -- and does not move
-- for an additive job type. This describes the shape of the `command` object
-- inside one job, and lets a command's parameters change without renumbering
-- the protocol every terminal in the fleet negotiates on.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS command_version SMALLINT NOT NULL DEFAULT 1;

-- When this command stops being deliverable.
--
-- NULLABLE, and null means "does not lapse". That is not a loophole, it is the
-- two inherited commands: WIFI_RECOVERY computes its window from created_at in
-- Go and ENROLL_FINGERPRINT has never had one. The delivery filter treats NULL
-- as "no expiry" so both keep behaving exactly as they do today, and
-- models.CommandSpec requires a validity for every NEW type so the null case
-- cannot spread.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- The capability token the terminal must have reported before this row may be
-- created (025).
--
-- ON THE ROW, NOT ONLY IN THE ISSUING CODE, because that is what makes the gate
-- impossible to forget rather than merely customary: the completeness
-- constraint below refuses a COMMAND without one, so a new command type cannot
-- reach the table until somebody has decided which capability it needs.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS requires_capability VARCHAR(64);

-- Who asked. Denormalised beside the reference for the reason models.AuditRecord
-- denormalises its actor: an operator account can be deleted, and a foreign key
-- alone would leave a null and lose the answer to the question somebody is
-- asking six months later.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS requested_by BIGINT;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS requested_by_email VARCHAR(255);

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_requested_by_fkey;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_requested_by_fkey
    FOREIGN KEY (requested_by) REFERENCES users(id) ON DELETE SET NULL;

-- Why the operator asked, in their own words. Free text, shown back to whoever
-- reads the trail. NOT a machine value: result_code is that.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS reason TEXT;

-- The caller's retry token.
--
-- A BROWSER THAT NEVER SAW ITS RESPONSE WILL SEND THE REQUEST AGAIN, and for a
-- command that is a second physical act. The one-outstanding index below covers
-- the common case; this covers the case the index cannot -- a retry arriving
-- after the first command has already been collected and completed, where
-- nothing is outstanding and a second row would be perfectly legal.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS idempotency_key UUID;

-- When the terminal FIRST collected it.
--
-- Distinct from last_attempt_at, which the delivery lease overwrites on every
-- fetch. The console's question is "has the terminal got it yet", which is a
-- first-time fact, and answering it from a column that moves would make a
-- command look freshly delivered every time it was redelivered.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS delivered_at TIMESTAMPTZ;

-- What the terminal reported back.
--
-- result_code is the stable machine value a client branches on; result is the
-- structured detail behind it. Both are written by the device, so both are
-- bounded: handlers.CompleteDeviceJob caps the encoded result and Go rejects an
-- over-long code, because an unbounded device-writable column is a growth
-- vector with a credential behind it.
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS result JSONB;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS result_code VARCHAR(48);

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_result_shape_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_result_shape_check
    CHECK (result IS NULL OR jsonb_typeof(result) = 'object');

-- ---------------------------------------------------------------------------
-- A COMMAND MUST BE COMPLETE
-- ---------------------------------------------------------------------------
--
-- THE LOAD-BEARING CONSTRAINT OF THIS MIGRATION. A command row that names no
-- device is a fan-out; one that names no capability is a command the platform
-- will happily send to firmware that will acknowledge it and throw it away,
-- which is precisely the false-ACCEPTED that 025 exists to have stopped.
--
-- IN THE SCHEMA RATHER THAN IN GO, on 024's reasoning: Go can be bypassed by
-- the next handler somebody writes, and the whole value of a command plane is
-- that the sixth command type gets these properties without anybody
-- remembering to give them to it.
--
-- expires_at is NOT required here -- see the column note. It is required per
-- type by models.CommandSpec, which is where the two inherited exceptions are
-- named and where a new type cannot avoid declaring one.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_command_complete_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_command_complete_check CHECK (
    command_class IS DISTINCT FROM 'COMMAND'
    OR (device_id IS NOT NULL AND requires_capability IS NOT NULL)
);

-- ---------------------------------------------------------------------------
-- One outstanding command PER TYPE per terminal
-- ---------------------------------------------------------------------------
--
-- The generalisation of sync_jobs_one_pending_wifi_recovery, and it is a
-- SEPARATE index rather than a replacement. The 024 index stays exactly as it
-- is and this one excludes the type it covers, so Change Wi-Fi behaves
-- bit-for-bit as it does today.
--
-- ENROLL_FINGERPRINT IS ALSO EXCLUDED, and that is a preservation rather than a
-- design: it has no such rule today, the console relies on being able to
-- re-issue, and adding one here would change a shipped feature inside a
-- migration that is supposed to be additive. Named in the exclusion list rather
-- than left to fall out of the class, so the omission is visibly deliberate.
--
-- PER TYPE, not per terminal. A diagnostic snapshot and a device test are not
-- in tension with each other and serialising them would make the console
-- refuse work it could do; two device tests ARE, because the second would sound
-- a buzzer somebody has already stopped listening for.
--
-- PENDING only. A completed command is history and must not block the next one.
DROP INDEX IF EXISTS sync_jobs_one_pending_command;
CREATE UNIQUE INDEX sync_jobs_one_pending_command
    ON sync_jobs(device_id, job_type)
    WHERE command_class = 'COMMAND'
      AND status = 'PENDING'
      AND job_type NOT IN ('WIFI_RECOVERY', 'ENROLL_FINGERPRINT');

-- ---------------------------------------------------------------------------
-- The retry token is unique per terminal
-- ---------------------------------------------------------------------------
--
-- Scoped to the device rather than global: a client generating one key per
-- user-visible action and issuing it to three terminals is doing something
-- reasonable, and a global unique index would refuse the second and third.
CREATE UNIQUE INDEX IF NOT EXISTS sync_jobs_idempotency_key
    ON sync_jobs(device_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Delivery index, narrowed to commands
-- ---------------------------------------------------------------------------
--
-- idx_sync_jobs_device_due (003) already serves the hot path. This one serves
-- the console's question -- "what is outstanding for this terminal, by type" --
-- which is a different access pattern and would otherwise scan a device's whole
-- history on every poll of a command-status dialog.
CREATE INDEX IF NOT EXISTS idx_sync_jobs_command_status
    ON sync_jobs(device_id, job_type, id DESC)
    WHERE command_class = 'COMMAND';

-- ---------------------------------------------------------------------------
-- CLASSIFICATION IS AUTOMATIC
-- ---------------------------------------------------------------------------
--
-- A TRIGGER RATHER THAN EDITS TO SEVEN INSERT SITES, and the choice is about
-- risk rather than taste.
--
-- sync_jobs is written from enqueuePersonChangeTx, EnqueueSettingsJob,
-- enqueueCurrentSettingsTx, enqueueSettingsFanoutTx, enqueueBootstrapJobs,
-- compactDeviceBacklogTx, RequestWifiRecovery and the enrolment path. Every one
-- of those is production code with tests behind it, and the completeness
-- constraint above would reject a COMMAND row that any of them wrote without a
-- capability -- so a migration that added the constraint and left them alone
-- would break enrolment the moment it deployed.
--
-- Two ways out. Edit all eight call sites, which is eight chances to typo a
-- token into a column the platform then gates a door on. Or derive the two
-- values from the one column that already decides them. The second is both
-- smaller and stronger: it means a NINTH insert site, written next year by
-- somebody who has never read this file, is classified correctly without their
-- participation. That is the entire thesis of this migration applied to itself.
--
-- IT ONLY EVER FILLS IN A BLANK. An explicit command_class or
-- requires_capability from the caller is left exactly as given, so
-- database/commands.go stays the authority for the rows it writes and this is
-- the floor rather than the rule.
CREATE OR REPLACE FUNCTION sync_jobs_classify() RETURNS trigger AS $$
BEGIN
    IF NEW.command_class IS NULL THEN
        NEW.command_class := CASE NEW.job_type
            WHEN 'WIFI_RECOVERY'       THEN 'COMMAND'
            WHEN 'ENROLL_FINGERPRINT'  THEN 'COMMAND'
            WHEN 'DIAGNOSTIC_SNAPSHOT' THEN 'COMMAND'
            WHEN 'DEVICE_TEST'         THEN 'COMMAND'
            ELSE 'STATE'
        END;
    END IF;

    -- The capability token, derived by the same rule the firmware and
    -- models.CommandCapability use: the job type, lowercased. The two inherited
    -- types predate the `cmd_` prefix and keep the tokens their firmware
    -- actually advertises -- `wifi_recovery`, not `cmd_wifi_recovery` -- because
    -- a gate that names a token no image reports is a gate that refuses
    -- everything.
    IF NEW.command_class = 'COMMAND' AND NEW.requires_capability IS NULL THEN
        NEW.requires_capability := CASE NEW.job_type
            WHEN 'WIFI_RECOVERY'      THEN 'wifi_recovery'
            WHEN 'ENROLL_FINGERPRINT' THEN 'enroll_fingerprint'
            ELSE 'cmd_' || lower(NEW.job_type)
        END;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- DROP-then-CREATE rather than CREATE IF NOT EXISTS, which triggers do not
-- have. 027's note applies: a migration that is not idempotent passes once and
-- fails every rerun, which is indistinguishable from a broken schema when the
-- suite rebuilds the database on every run.
DROP TRIGGER IF EXISTS sync_jobs_classify_trigger ON sync_jobs;
CREATE TRIGGER sync_jobs_classify_trigger
    BEFORE INSERT ON sync_jobs
    FOR EACH ROW EXECUTE FUNCTION sync_jobs_classify();

-- ---------------------------------------------------------------------------
-- Backfill
-- ---------------------------------------------------------------------------
--
-- Every pre-existing row is classified, so command_class IS NULL stops meaning
-- "old row" and starts meaning "a bug in whatever wrote this".

UPDATE sync_jobs
   SET command_class = 'STATE'
 WHERE command_class IS NULL
   AND job_type IN ('CREATE', 'UPDATE', 'DELETE', 'SETTINGS', 'FULL_SYNC',
                    'INCREMENTAL_SYNC', 'PERMISSION_PUSH', 'TEMPLATE_PUSH',
                    'FIRMWARE_UPDATE', 'LOG_PULL');

-- The two inherited commands. requires_capability is filled from what each
-- feature ALREADY requires in Go, so the column records the rule rather than
-- introducing one:
--
--   WIFI_RECOVERY      database/wifi_recovery.go refuses a terminal without
--                      models.CapabilityWifiRecovery. The column now says so.
--   ENROLL_FINGERPRINT the firmware advertises `enroll_fingerprint` and NOTHING
--                      ON THE PLATFORM GATES ON IT YET -- include/device_info.h
--                      says exactly that. Recording the token here does not
--                      create the gate: the enrolment start path does not go
--                      through the command store, so its behaviour is unchanged.
--                      What it does is put the answer somewhere, so the Phase 0
--                      change is a one-line gate rather than a rediscovery.
UPDATE sync_jobs
   SET command_class = 'COMMAND',
       requires_capability = COALESCE(requires_capability, 'wifi_recovery')
 WHERE job_type = 'WIFI_RECOVERY' AND command_class IS NULL;

UPDATE sync_jobs
   SET command_class = 'COMMAND',
       requires_capability = COALESCE(requires_capability, 'enroll_fingerprint')
 WHERE job_type = 'ENROLL_FINGERPRINT' AND command_class IS NULL;

-- Delivery evidence for commands already collected. last_attempt_at is the only
-- record the platform has that a terminal ever held one, and 024's status path
-- already reads it that way; copying it forward means the console's DELIVERED
-- state survives this migration for commands in flight during the deploy.
UPDATE sync_jobs
   SET delivered_at = last_attempt_at
 WHERE command_class = 'COMMAND'
   AND delivered_at IS NULL
   AND last_attempt_at IS NOT NULL;

COMMENT ON COLUMN sync_jobs.command_class IS
    'STATE (declarative, idempotent, safe to redeliver late) or COMMAND (an '
    'event, which applying twice performs twice). Set for every row by '
    '028_command_plane.sql; NULL now means the writer did not classify it.';

COMMENT ON COLUMN sync_jobs.expires_at IS
    'When a COMMAND stops being deliverable. NULL means it does not lapse, '
    'which is true only of the two commands inherited from 024 and 027 -- '
    'models.CommandSpec requires a validity for every type added since.';

COMMENT ON COLUMN sync_jobs.requires_capability IS
    'The 025 capability token a terminal must have reported before this row '
    'may exist. Refusing to store a COMMAND without one is what stops the '
    'platform sending work to firmware that would acknowledge and discard it.';

COMMENT ON COLUMN sync_jobs.result IS
    'What the terminal reported back, bounded and written by the device. '
    'result_code is the stable machine value; this is the detail behind it.';

COMMIT;
