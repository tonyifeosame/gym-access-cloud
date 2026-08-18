-- ---------------------------------------------------------------------------
-- 025: what a terminal says it can do
-- ---------------------------------------------------------------------------
--
-- THE QUESTION THIS COLUMN ANSWERS, AND WHY NOTHING ELSE COULD. Before the
-- platform queues an operator command it has to know whether the terminal will
-- ACT on it or acknowledge it and throw it away. Firmware that predates a job
-- type parses it as kUnknown and acknowledges it as applied -- deliberately, so
-- that a newer server's job types are not redelivered for ever -- which is
-- exactly what makes an additive job type safe to serve to an old fleet AND
-- exactly what makes an old unit's acknowledgement indistinguishable from a new
-- one's.
--
-- WHY NOT THE VERSION COLUMN WE ALREADY HAVE. Because it does not work, and it
-- cannot be made to work retroactively. include/device_info.h defaults
-- DEVICE_FIRMWARE_VERSION to "1.0.0" and the build flag that would override it
-- was commented out, so every image ever produced reports the same string. That
-- is also why OTA could not be the remedy: an image that installs an update and
-- then keeps reporting the version it replaced is offered the same image again
-- on the next heartbeat, for ever. Both halves are fixed from this release
-- forward -- the version is stamped in platformio.ini now -- but the fleet
-- ALREADY IN THE FIELD will keep reporting 1.0.0 until it is reflashed, and no
-- column can fix that after the fact.
--
-- So the platform stops inferring and starts asking. A capability is the fact
-- itself rather than a proxy for it, and it needs no version ordering on either
-- side -- which matters, because this codebase deliberately has exactly one
-- implementation of version ordering (the device's) and refuses to grow a
-- second that could disagree with it.
--
-- NULLABLE, AND THE THREE VALUES ARE ALL DIFFERENT:
--
--   NULL   this terminal has never reported. Nothing may be inferred from it --
--          not "no capabilities", and not "old firmware" either, since a
--          brand-new unit is also NULL until its first heartbeat.
--   '[]'   it reports, and has none of them. A real answer.
--   [...]  it reports, and has these.
--
-- The distinction is load-bearing: the heartbeat MERGES with COALESCE, so an
-- absent field means "unchanged" and cannot switch a feature off for a door
-- because one heartbeat came from a build that does not report. A terminal that
-- genuinely loses a capability says so by sending a shorter array.

BEGIN;

-- JSONB rather than TEXT[], and rather than a table of rows.
--
-- TEXT[] would be the more natural Postgres shape, but every array on this path
-- would then need lib/pq's array wrapper at each call site, and the one place
-- that reads it is a Go slice either way. JSONB is what `payload` on sync_jobs
-- already uses, so there is one array encoding in this schema rather than two.
--
-- A TABLE would be the fully normalised answer and buys nothing here: the list
-- is small, it is always read whole, it is never joined against, and it is
-- replaced atomically on every heartbeat rather than edited.
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS capabilities JSONB;

-- When the list last changed shape, so "this terminal was reflashed" is
-- answerable without diffing heartbeats. Set only when the value actually
-- differs, not on every beat.
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS capabilities_reported_at TIMESTAMPTZ;

-- An ARRAY or nothing. A scalar or an object here would be a client bug, and it
-- would surface at the point of USE -- inside the gate that decides whether a
-- door is sent a command -- rather than at the point of write.
--
-- NULL passes, which is what makes this safe to add to a table full of
-- pre-existing rows.
ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_capabilities_check;
ALTER TABLE devices
    ADD CONSTRAINT devices_capabilities_check
    CHECK (capabilities IS NULL OR jsonb_typeof(capabilities) = 'array');

COMMENT ON COLUMN devices.capabilities IS
    'What this terminal reported it can do, as a JSON array of tokens '
    '(025_device_capabilities.sql). NULL means it has never reported, which is '
    'NOT the same as reporting none -- an empty array is the second. Merged '
    'rather than overwritten on heartbeat, so an image that does not report '
    'cannot switch a gated feature off for that door.';

COMMENT ON COLUMN devices.capabilities_reported_at IS
    'When the capability list last CHANGED, not when it was last received.';

COMMIT;
