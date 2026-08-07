-- Sprint 6: device state machine, firmware/hardware inventory, sync compaction
--
-- Three related changes:
--
-- 1. Device state. ONLINE/OFFLINE could not express a device that is mid-update,
--    has faulted, or has been deliberately taken out of service. A terminal
--    reporting ERROR is operationally very different from one that is merely
--    unreachable, and the dashboard needs to tell them apart.
--
--    RETIRED is folded into DISABLED. It overlapped with the soft-delete column
--    already present on devices: a decommissioned unit is deleted, whereas a
--    unit that is switched off but still installed is DISABLED. PROVISIONING is
--    kept (it is not in the requested set) because a device row can exist before
--    it has ever registered and therefore before it has any credential.
--
-- 2. Firmware and hardware inventory, so the dashboard can answer "which
--    terminals are behind?". The catalog gains an explicit `is_current` target
--    per device type and release channel; a device is outdated when what it
--    reports differs from that target. Comparing against the newest published
--    row would be wrong -- the newest build is not necessarily the one a fleet
--    is supposed to be on.
--
--    This is inventory only. No OTA: nothing here downloads, schedules, or
--    applies firmware.
--
-- 3. Compaction bookkeeping for collapsing a long-offline device's backlog,
--    see database/sync.go.

BEGIN;

-- ---------------------------------------------------------------------------
-- Device state machine
-- ---------------------------------------------------------------------------
UPDATE devices SET status = 'DISABLED' WHERE status = 'RETIRED';

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_status_check;
ALTER TABLE devices ADD CONSTRAINT devices_status_check CHECK (status IN (
    'PROVISIONING',  -- created, never registered
    'ONLINE',        -- heartbeating
    'OFFLINE',       -- missed its heartbeat window
    'UPDATING',      -- applying firmware
    'ERROR',         -- reported a fault
    'DISABLED'       -- administratively out of service
));

-- Why a device is in ERROR, so the dashboard can show something actionable
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_error_at TIMESTAMP;

-- ---------------------------------------------------------------------------
-- Firmware and hardware inventory
-- ---------------------------------------------------------------------------

-- `reported_firmware` (005) becomes `firmware_version`: it sits alongside
-- hardware_revision and build_number as the device's self-reported identity.
-- Distinct from firmware_version_id, which is the resolved catalog row.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'devices' AND column_name = 'reported_firmware')
       AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'devices' AND column_name = 'firmware_version') THEN
        ALTER TABLE devices RENAME COLUMN reported_firmware TO firmware_version;
    END IF;
END $$;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS firmware_version VARCHAR(50);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS hardware_revision VARCHAR(50);

-- build_number is VARCHAR, not INTEGER, on purpose: a device must never be able
-- to fail its heartbeat because its build identifier is not a number.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS build_number VARCHAR(50);

ALTER TABLE devices ADD COLUMN IF NOT EXISTS boot_count INTEGER;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS release_channel VARCHAR(20) NOT NULL DEFAULT 'STABLE';

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_release_channel_check;
ALTER TABLE devices ADD CONSTRAINT devices_release_channel_check
    CHECK (release_channel IN ('STABLE', 'BETA', 'CANARY'));

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_boot_count_check;
ALTER TABLE devices ADD CONSTRAINT devices_boot_count_check
    CHECK (boot_count IS NULL OR boot_count >= 0);

-- The catalog's deployment target. Exactly one current build per
-- (device_type, release_channel).
ALTER TABLE firmware_versions ADD COLUMN IF NOT EXISTS is_current BOOLEAN NOT NULL DEFAULT FALSE;

CREATE UNIQUE INDEX IF NOT EXISTS firmware_versions_current_key
    ON firmware_versions(device_type, release_channel)
    WHERE is_current AND deleted_at IS NULL;

-- Seed the 1.0.0 dev build as the current STABLE target if nothing is set
UPDATE firmware_versions
   SET is_current = TRUE
 WHERE version = '1.0.0'
   AND device_type = 'TERMINAL'
   AND release_channel = 'STABLE'
   AND deleted_at IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM firmware_versions
         WHERE device_type = 'TERMINAL' AND release_channel = 'STABLE'
           AND is_current AND deleted_at IS NULL
   );

-- ---------------------------------------------------------------------------
-- Compaction bookkeeping
--
-- A compacted backlog is replaced by a FULL_SYNC job carrying the authoritative
-- roster, which needs its own entity type: it describes the whole member set
-- rather than one person, so it cannot satisfy the PERSON rule that requires an
-- entity_external_id.
-- ---------------------------------------------------------------------------
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_compacted_at TIMESTAMP;

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_entity_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_entity_type_check
    CHECK (entity_type IS NULL OR entity_type IN ('PERSON', 'SETTINGS', 'ROSTER'));

-- ---------------------------------------------------------------------------
-- Dashboard indexes
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_devices_firmware_version
    ON devices(firmware_version) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_devices_release_channel
    ON devices(device_type, release_channel) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_devices_last_sync_at
    ON devices(last_sync_at) WHERE deleted_at IS NULL;

COMMIT;
