-- ---------------------------------------------------------------------------
-- 024: the console's Change Wi-Fi command
-- ---------------------------------------------------------------------------
--
-- A terminal whose Wi-Fi password changed, or whose router was replaced, is
-- offline and unreachable. There is no keypad and no touchscreen, so the only
-- way back used to be a laptop and a serial console. Firmware 15caf88 added two
-- ways in, and both end at the SAME setup portal a new unit uses:
--
--   LOCALLY   hold the BOOT button for five seconds.
--   REMOTELY  a WIFI_RECOVERY sync job, which is what this migration admits.
--
-- WHY A JOB TYPE AND NOT A SETTINGS FIELD, which is the firmware's reasoning and
-- is repeated here because the schema is where it is enforced: settings are
-- DECLARATIVE and re-applying a snapshot must be a no-op, while clearing the
-- Wi-Fi credentials is an EVENT that applying twice performs twice. A terminal
-- re-sent its settings after a compaction must not re-enter setup mode.
--
-- NO CREDENTIALS TRAVEL. The job carries a job id and a type and nothing else --
-- there is no payload, and there is deliberately nowhere in this schema to put
-- an SSID or a pre-shared key. The platform never learns the customer's Wi-Fi
-- password, before or after. What the command does is hand the terminal back to
-- the setup portal, where somebody standing next to it types the new password
-- into the terminal itself.
--
-- NOT A FACTORY RESET, and nothing here can make it one. The device row, its
-- credential hash, its site, its people and their fingerprint bindings are all
-- in tables this job does not name and the firmware's recovery path holds no
-- reference to.

BEGIN;

-- ---------------------------------------------------------------------------
-- The job type
-- ---------------------------------------------------------------------------
--
-- Appended to the existing vocabulary rather than replacing it. Firmware that
-- predates 15caf88 parses an unrecognised type as kUnknown and ACKNOWLEDGES it,
-- so an older fleet ignores the command rather than failing on it for ever --
-- which is what makes it safe to serve this type to a mixed fleet.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_type_check CHECK (job_type IN (
    -- entity change operations (Sprint 4)
    'CREATE', 'UPDATE', 'DELETE', 'SETTINGS',
    -- operational jobs (Sprint 2)
    'FULL_SYNC', 'INCREMENTAL_SYNC', 'PERMISSION_PUSH',
    'TEMPLATE_PUSH', 'FIRMWARE_UPDATE', 'LOG_PULL',
    -- operator commands (024)
    'WIFI_RECOVERY'
));

-- ---------------------------------------------------------------------------
-- Addressed to exactly one device
-- ---------------------------------------------------------------------------
--
-- Joined to the existing change-job rule rather than given a rule of its own.
-- A command fanned out to a site because device_id happened to be null would
-- put every door at that location into setup mode at once, which is the single
-- worst outcome this feature has available.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_change_device_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_change_device_check CHECK (
    job_type NOT IN ('CREATE', 'UPDATE', 'DELETE', 'SETTINGS', 'WIFI_RECOVERY')
    OR device_id IS NOT NULL
);

-- ---------------------------------------------------------------------------
-- At most ONE outstanding command per terminal
-- ---------------------------------------------------------------------------
--
-- THIS IS THE IDEMPOTENCY, and it is in the schema rather than in Go because
-- that is the only place it holds under concurrency. An operator who presses
-- Change Wi-Fi twice -- or whose browser retries a request whose response it
-- never saw -- must not queue two commands: the second would be redelivered
-- after the customer had already re-provisioned the terminal, and would wipe the
-- Wi-Fi they had just given it.
--
-- PENDING only. A completed command is history and does not block a later one:
-- a customer who changes router twice in a month is doing nothing wrong.
CREATE UNIQUE INDEX IF NOT EXISTS sync_jobs_one_pending_wifi_recovery
    ON sync_jobs(device_id)
    WHERE job_type = 'WIFI_RECOVERY' AND status = 'PENDING';

COMMIT;
