-- 032_terminal_release.sql
--
-- First-class release of a terminal from the company that holds it, so the
-- hardware can be adopted by another company through the ordinary announce and
-- approve flow.
--
-- ---------------------------------------------------------------------------
-- WHY RELEASE IS A STATE AND NOT A DELETE
-- ---------------------------------------------------------------------------
--
-- Retiring (016) and the platform release (022) both soft-delete the row and
-- clear its key hash. Neither reaches the terminal: the unit goes on holding
-- the losing company's members and fingerprint templates, and the only thing
-- that ever told it anything had changed was a 401 on its next heartbeat --
-- which it could not distinguish from a rotated key. The recovery documented
-- for that, `clear key` at the serial console, made things worse: a terminal
-- with no credential is STANDALONE in the firmware's terms and the site's
-- offline policy does not apply to it, so the door kept opening for the old
-- roster indefinitely.
--
-- So a release is now ORDERED first and RELEASED second, and the row stays
-- live and authenticating in between. The order is a small signed object the
-- terminal can verify with the one secret only it holds -- its own device key
-- -- and RELEASED is reached when the terminal proves it executed the order
-- (a receipt computed with that same key), or when an operator or the
-- platform explicitly forces it. Adoption by any company is refused while a
-- live row exists, ORDERED or not, so the new owner cannot have the hardware
-- while the old owner's data is still on it.
--
-- ---------------------------------------------------------------------------
-- THE MAC AND THE RECEIPT
-- ---------------------------------------------------------------------------
--
-- release_order_mac = HMAC-SHA256(key = the 32 raw bytes of api_key_hash,
--                                 msg = "accesslink-release-v1|serial|release_id|ordered_at")
--
-- The platform stores only the SHA-256 of a device key, and the terminal can
-- compute that hash from the key it holds. Both sides therefore share a MAC
-- key without a new secret existing anywhere and without the key ever
-- leaving the terminal. The MAC is kept on the row after the hash has been
-- cleared, because a terminal that was offline when the release was forced
-- fetches the order by serial on its first 401 and has to be able to verify
-- it then. The MAC reveals nothing about the hash.
--
-- The receipt is HMAC-SHA256 over "accesslink-receipt-v1|serial|release_id"
-- with the same key, and is verified against api_key_hash while the row is
-- still ORDERED -- which is why the hash is NOT cleared at order time.
--
-- ---------------------------------------------------------------------------
-- READINESS
-- ---------------------------------------------------------------------------
--
-- A terminal collected through an announcement is seeded with a FULL_SYNC
-- snapshot rather than bare CREATE jobs (database/announcements.go). The id of
-- that snapshot is recorded here so the console can say "setting up" until the
-- terminal has acknowledged it, and "ready" only after. On a transferred unit
-- that is the moment the previous owner's roster is provably gone; on a new
-- unit it is merely honest.
--
-- The gate is ARMED at collection (readiness_armed_at) separately from the
-- snapshot being recorded (readiness_job_id), because the two can come apart:
-- a collection whose snapshot was refused for capacity has armed a gate with
-- no job to pass it. Such a row must read SETTING_UP -- it has never been told
-- what to hold -- and not READY by the accident of a NULL job. A row from
-- before this migration has neither and reads READY, which is what it was.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS release_state            VARCHAR(10),
    ADD COLUMN IF NOT EXISTS release_id               UUID,
    ADD COLUMN IF NOT EXISTS release_ordered_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS release_ordered_by       BIGINT,
    ADD COLUMN IF NOT EXISTS release_ordered_by_email VARCHAR(255),
    ADD COLUMN IF NOT EXISTS release_reason           TEXT,
    ADD COLUMN IF NOT EXISTS release_order_mac        BYTEA,
    ADD COLUMN IF NOT EXISTS release_confirmed_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS release_confirmed_by     VARCHAR(10),
    ADD COLUMN IF NOT EXISTS release_report           JSONB,
    ADD COLUMN IF NOT EXISTS readiness_armed_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS readiness_job_id         BIGINT;

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_ordered_by_fkey;
ALTER TABLE devices ADD CONSTRAINT devices_release_ordered_by_fkey
    FOREIGN KEY (release_ordered_by) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_readiness_job_fkey;
ALTER TABLE devices ADD CONSTRAINT devices_readiness_job_fkey
    FOREIGN KEY (readiness_job_id) REFERENCES sync_jobs(id) ON DELETE SET NULL;

-- The state machine, as a CHECK, because a state machine that exists only in
-- Go is one a future code path can step outside of.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_state_check;
ALTER TABLE devices ADD CONSTRAINT devices_release_state_check
    CHECK (release_state IS NULL OR release_state IN ('ORDERED', 'RELEASED'));

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_confirmed_by_check;
ALTER TABLE devices ADD CONSTRAINT devices_release_confirmed_by_check
    CHECK (release_confirmed_by IS NULL
           OR release_confirmed_by IN ('TERMINAL', 'OPERATOR', 'PLATFORM'));

-- A state always names its order.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_shape_check;
ALTER TABLE devices ADD CONSTRAINT devices_release_shape_check
    CHECK (release_state IS NULL
           OR (release_id IS NOT NULL AND release_ordered_at IS NOT NULL));

-- RELEASED is a deleted row. The live-serial index (002) is what frees the
-- serial for the next owner, and this is what stops a row claiming to be
-- released while still occupying it.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_released_is_deleted_check;
ALTER TABLE devices ADD CONSTRAINT devices_released_is_deleted_check
    CHECK (release_state IS DISTINCT FROM 'RELEASED' OR deleted_at IS NOT NULL);

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_report_shape_check;
ALTER TABLE devices ADD CONSTRAINT devices_release_report_shape_check
    CHECK (release_report IS NULL OR jsonb_typeof(release_report) = 'object');

-- The by-serial order lookup a terminal makes on its first 401: the newest
-- order for the serial, live or released. Partial, because almost no row ever
-- carries an order.
CREATE INDEX IF NOT EXISTS idx_devices_release_lookup
    ON devices (serial_number, release_ordered_at DESC)
    WHERE release_id IS NOT NULL;

COMMENT ON COLUMN devices.release_state IS
    'NULL for a terminal not being released. ORDERED while the row is live and '
    'a signed release order is outstanding for the terminal to execute. '
    'RELEASED once the terminal confirmed the wipe, or an operator or the '
    'platform forced it; the row is soft-deleted at that moment and the serial '
    'is free for any company to adopt.';

COMMENT ON COLUMN devices.release_order_mac IS
    'HMAC-SHA256 over the order, keyed with the raw bytes of api_key_hash at '
    'the moment the order was made. Kept after the hash is cleared so a '
    'terminal that reconnects later can still verify the order. NULL when the '
    'row had no credential to key it with, in which case only the physical '
    'release path exists.';

COMMENT ON COLUMN devices.release_report IS
    'What the terminal reported having erased -- counts only, never biometric '
    'material -- when it confirmed the release.';

COMMENT ON COLUMN devices.readiness_armed_at IS
    'When this row was last collected through an announcement and its '
    'readiness gate armed. NULL on a row provisioned before the gate existed. '
    'An armed row with no readiness_job_id was refused its snapshot for '
    'capacity and is SETTING_UP until one is queued and acknowledged.';

COMMENT ON COLUMN devices.readiness_job_id IS
    'The FULL_SYNC snapshot this terminal was last seeded with. The terminal '
    'is READY once that job is COMPLETED and SETTING_UP until then.';
