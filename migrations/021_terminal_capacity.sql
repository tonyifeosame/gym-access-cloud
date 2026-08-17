-- ---------------------------------------------------------------------------
-- 021: terminal roster capacity
-- ---------------------------------------------------------------------------
--
-- FW-01 / SYN-01. A terminal holds a FIXED number of people. The server has
-- never known that number, so it has never been able to tell a customer that
-- the site they are building is larger than the hardware standing at its door.
--
-- WHAT ACTUALLY HAPPENS TODAY, and it is worth writing down because it is not
-- the failure people expect. A FULL_SYNC job carries the authoritative roster.
-- The firmware refuses one longer than its table WHOLESALE rather than
-- truncating it (sync_handoff.cpp: `if (count > kMaxMembers) return false`),
-- and it is right to -- a short roster reads as a list of deletions, which is
-- the most destructive misreading available in this protocol. So the job fails,
-- is retried, exhausts its attempts and parks FAILED, and the terminal keeps
-- serving the roster it already had. Nobody is told. The door works, for the
-- wrong set of people, indefinitely.
--
-- ---------------------------------------------------------------------------
-- WHY A REPORTED NUMBER AND NOT A CONSTANT
-- ---------------------------------------------------------------------------
--
-- The ceiling is a compile-time constant on the device (MAX_MEMBERS), and it
-- has ALREADY CHANGED ONCE -- 64 when the audit was written, 256 now that the
-- member table moved behind a record store. A constant duplicated on this side
-- would have been wrong within one firmware release, and wrong in the dangerous
-- direction: a server that believes 64 refuses rosters a terminal could hold.
--
-- So the device states it, and this column records what it stated. NULL means
-- "has not said", which is the honest answer for every terminal in the field
-- today and is treated as such -- see database/capacity.go, where an unknown
-- capacity warns and a KNOWN one is enforced.
--
-- The reporting half is the firmware's (AI #2); the exact contract it has to
-- meet is in docs/sync-protocol.md. This migration and the code around it are
-- the server half, and they are useful before that lands: the column is also
-- writable from the console, so an operator who knows what they installed can
-- record it.

BEGIN;

ALTER TABLE devices
    -- The number of people this terminal can hold at once, as REPORTED BY IT.
    -- NULL is not zero and not a default: it means the terminal has never told
    -- us, which is what every unit running firmware older than the heartbeat
    -- field will keep meaning.
    ADD COLUMN IF NOT EXISTS member_capacity INTEGER,

    -- When it last said so. A capacity from four firmware versions ago is worth
    -- knowing about as a capacity, and worth knowing the age of.
    ADD COLUMN IF NOT EXISTS member_capacity_reported_at TIMESTAMPTZ,

    -- The last time this terminal's permitted roster did not fit.
    --
    -- Recorded rather than derived, because the interesting moment is when the
    -- server DECLINED to queue a snapshot -- a state that leaves no other trace
    -- and would otherwise be visible only in a log line.
    ADD COLUMN IF NOT EXISTS roster_overflow_at TIMESTAMPTZ,

    -- How many people were permitted at that moment. Kept beside the timestamp
    -- so the console can say "312 people, capacity 256" rather than "over
    -- capacity", which tells an operator nothing about how much hardware they
    -- need to buy.
    ADD COLUMN IF NOT EXISTS roster_overflow_count INTEGER;

-- A capacity of zero would mean a terminal that can hold nobody, which is not a
-- thing the firmware can report and would silently disable a door if it were
-- accepted. Negative is nonsense. The upper bound is not the firmware's current
-- constant -- that is allowed to grow -- but a sanity limit that catches a
-- garbage value read out of a corrupt field.
ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_member_capacity_sane;
ALTER TABLE devices
    ADD CONSTRAINT devices_member_capacity_sane
    CHECK (member_capacity IS NULL OR (member_capacity > 0 AND member_capacity <= 100000));

COMMENT ON COLUMN devices.member_capacity IS
    'People this terminal can hold, as reported by the terminal itself on the '
    'heartbeat (FW-01). NULL means it has never reported one, which is not the '
    'same as unlimited -- see database/capacity.go.';

COMMENT ON COLUMN devices.roster_overflow_at IS
    'When this terminal was last found to have more permitted people than it '
    'can store. Set by the server when it declines to queue a snapshot the '
    'terminal would refuse wholesale.';

COMMIT;
