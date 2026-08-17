-- ---------------------------------------------------------------------------
-- 016: terminal lifecycle -- revocation, disable, retire, reassign
-- ---------------------------------------------------------------------------
--
-- The audit's second blocker, and the one that matters most for a product sold
-- as access control: THERE WAS NO WAY TO REVOKE A TERMINAL.
--
-- `DISABLED` existed in the device state machine from 006 and was written in
-- exactly one place in the entire codebase -- RetireSite, which cascades when a
-- whole location is closed. There was no operator-facing route that disabled
-- one terminal, deleted one, moved one, or invalidated one credential. The only
-- lever for a stolen unit was retiring its entire site, which stops every other
-- door there.
--
-- Rotating the site key does not help and was never going to: since
-- 005_device_identity.sql a terminal authenticates with its OWN credential, and
-- rotation deliberately does not touch it. That separation is correct -- it is
-- what stops one compromised terminal from being fixed by breaking every other
-- one -- and it means revocation has to exist per device.
--
-- ---------------------------------------------------------------------------
-- WHAT REVOCATION HAS TO MEAN
-- ---------------------------------------------------------------------------
--
-- Not a status flag. AuthenticateDevice resolves a device by looking up
-- api_key_hash; a status the lookup does not consult is a status that authorizes
-- nothing. Revocation therefore CLEARS THE HASH, so the credential stops
-- resolving at the index probe rather than at a check somewhere above it.
--
-- That is also why revoke and disable are different operations:
--
--   DISABLE   reversible. The credential still exists and still resolves; the
--             device auth middleware refuses it because the status says so.
--             For "this terminal is faulty, take it out of service".
--
--   REVOKE    irreversible for that credential. The hash is gone and cannot be
--             recovered, so the terminal must re-register to work again. For
--             "this terminal is stolen" -- where the question is not whether we
--             trust the device but whether we trust the SECRET, and the answer
--             has to hold even if somebody re-enables the row.
--
--   RETIRE    soft delete. The unit is gone: decommissioned, destroyed,
--             returned. Revokes the credential on the way out, because a
--             retired row that still authenticates is exactly the orphaned
--             hardware RetireSite's comment warns about.
--
-- A revoked device that re-registers gets a fresh credential, which is the
-- documented recovery path for a factory reset and is deliberately still open:
-- possession of the site provisioning key is the authority to enrol hardware at
-- that site, and always was.
--
-- ---------------------------------------------------------------------------
-- WHY DISABLED SURVIVES RE-REGISTRATION AND REVOKED DOES NOT
-- ---------------------------------------------------------------------------
--
-- RegisterDevice already refuses to bring a DISABLED device back, and its
-- comment explains why: registration must not undo the only revocation the
-- system had. That rule stands for DISABLED because disabling is a statement
-- about the DEVICE.
--
-- Revocation is a statement about a KEY. Once the old key is gone, re-registering
-- issues a new one and there is nothing left to protect -- refusing would only
-- mean a terminal recovered from theft could never be redeployed.

BEGIN;

-- ---------------------------------------------------------------------------
-- Lifecycle bookkeeping
-- ---------------------------------------------------------------------------
--
-- WHO and WHY, not just when. An operator looking at a dead terminal needs to
-- know whether it was taken out of service deliberately and by whom, and
-- "status = DISABLED" answers neither.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_reason VARCHAR(160);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_by BIGINT
    REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS credential_revoked_at TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS credential_revoked_reason VARCHAR(160);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS credential_rotated_at TIMESTAMPTZ;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS retired_reason VARCHAR(160);

-- Reassignment history, so a terminal that reports against the wrong site can be
-- explained rather than only corrected.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS site_assigned_at TIMESTAMPTZ;
UPDATE devices SET site_assigned_at = COALESCE(site_assigned_at, created_at);

-- The status and the timestamp must agree, for the same reason companies.active
-- and deactivated_at must: two answers to "is this switched off" means the
-- authentication path picks one of them.
--
-- Maintained by trigger rather than enforced by a bare CHECK, so that
-- `UPDATE devices SET status = 'DISABLED'` -- which RetireSite and every
-- existing caller does -- keeps working and cannot leave the pair inconsistent.
CREATE OR REPLACE FUNCTION sync_device_disabled_state()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'DISABLED' THEN
        IF NEW.disabled_at IS NULL THEN
            NEW.disabled_at := CURRENT_TIMESTAMP;
        END IF;
    ELSE
        NEW.disabled_at := NULL;
        NEW.disabled_reason := NULL;
        NEW.disabled_by := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS devices_disabled_sync ON devices;
CREATE TRIGGER devices_disabled_sync
    BEFORE INSERT OR UPDATE OF status, disabled_at, disabled_reason, disabled_by ON devices
    FOR EACH ROW EXECUTE FUNCTION sync_device_disabled_state();

UPDATE devices SET disabled_at = COALESCE(disabled_at, updated_at)
 WHERE status = 'DISABLED' AND disabled_at IS NULL;

-- A revoked credential is one that is GONE, not one that is flagged. The
-- constraint states the invariant the whole design rests on: if a revocation is
-- recorded, there is no hash left to authenticate with.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_revocation_check;
ALTER TABLE devices ADD CONSTRAINT devices_revocation_check
    CHECK (credential_revoked_at IS NULL OR api_key_hash IS NULL);

-- ---------------------------------------------------------------------------
-- The 005 invariant has to admit a third state
-- ---------------------------------------------------------------------------
--
-- 005_device_identity.sql wrote:
--
--     CHECK (registered_at IS NULL OR api_key_hash IS NOT NULL)
--        -- "A registered device must hold a credential"
--
-- That was correct when the only two states were "never registered" and
-- "registered, holding a key". Revocation introduces a third -- registered, and
-- deliberately holding nothing -- and the original constraint forbids it, which
-- is why clearing the hash failed rather than revoking.
--
-- The guarantee 005 was buying is still worth having and is preserved: a device
-- cannot be marked registered without a credential having been issued. What is
-- added is the one way a registered device may legitimately have none.
--
-- Written as an explicit third disjunct rather than by dropping the constraint,
-- so the state space stays enumerated: a device with no hash, no revocation and
-- a registration timestamp is still refused, because that is a device somebody
-- lost the credential of by accident.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_registration_check;
ALTER TABLE devices ADD CONSTRAINT devices_registration_check
    CHECK (
        registered_at IS NULL
        OR api_key_hash IS NOT NULL
        OR credential_revoked_at IS NOT NULL
    );

-- ---------------------------------------------------------------------------
-- Operational visibility for the sync backlog (SYN-04)
-- ---------------------------------------------------------------------------
--
-- The console could show last_sync_at and nothing else: no backlog depth, no
-- failed jobs, no indication that a terminal was behind. An operator could not
-- tell that people were missing from a door.
--
-- These are DENORMALISED COUNTERS maintained by the sync path rather than
-- computed on every list read. The alternative is a correlated subquery over
-- sync_jobs per device on the terminal list, which is the query a fleet of a
-- few thousand terminals makes expensive exactly when it matters most.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS pending_job_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS failed_job_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_apply_error VARCHAR(200);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_apply_error_at TIMESTAMPTZ;

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_job_counts_check;
ALTER TABLE devices ADD CONSTRAINT devices_job_counts_check
    CHECK (pending_job_count >= 0 AND failed_job_count >= 0);

-- Seed from what is actually queued, so the counters are right from the moment
-- this migration runs rather than from the next sync.
UPDATE devices d
   SET pending_job_count = COALESCE(counts.pending, 0),
       failed_job_count  = COALESCE(counts.failed, 0)
  FROM (
        SELECT device_id,
               count(*) FILTER (WHERE status = 'PENDING') AS pending,
               count(*) FILTER (WHERE status = 'FAILED')  AS failed
          FROM sync_jobs
         WHERE device_id IS NOT NULL
         GROUP BY device_id
  ) counts
 WHERE counts.device_id = d.id;

-- "Which terminals need attention", for the fleet view and the alerting.
CREATE INDEX IF NOT EXISTS idx_devices_failed_jobs
    ON devices(failed_job_count) WHERE deleted_at IS NULL AND failed_job_count > 0;

-- MAINTAINED BY THE DATABASE, not by the sync path.
--
-- A denormalised counter that the application is responsible for updating is a
-- counter that drifts: jobs are written from at least five places today
-- (person change fan-out, settings fan-out, bootstrap seeding, compaction, and
-- lifecycle cancellation) and any new one that forgets leaves an operator
-- looking at a fleet view that under-reports a backlog.
--
-- A trigger cannot be forgotten. It costs one row update per job transition,
-- against a table whose writes are already one-per-device-per-change.
CREATE OR REPLACE FUNCTION sync_device_job_counters()
RETURNS TRIGGER AS $$
DECLARE
    target BIGINT;
BEGIN
    target := COALESCE(NEW.device_id, OLD.device_id);
    IF target IS NULL THEN
        RETURN NULL;
    END IF;

    UPDATE devices d
       SET pending_job_count = COALESCE(c.pending, 0),
           failed_job_count  = COALESCE(c.failed, 0)
      FROM (
            SELECT count(*) FILTER (WHERE status = 'PENDING') AS pending,
                   count(*) FILTER (WHERE status = 'FAILED')  AS failed
              FROM sync_jobs WHERE device_id = target
      ) c
     WHERE d.id = target;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- AFTER, and statement-level would be wrong here: the fan-out inserts many rows
-- for many devices in one statement, and the counter is per device. FOR EACH ROW
-- is what lets the trigger know which device to recount.
DROP TRIGGER IF EXISTS sync_jobs_device_counters ON sync_jobs;
CREATE TRIGGER sync_jobs_device_counters
    AFTER INSERT OR UPDATE OF status OR DELETE ON sync_jobs
    FOR EACH ROW EXECUTE FUNCTION sync_device_job_counters();

-- ---------------------------------------------------------------------------
-- The offline policy a site runs under (part of APP-02's decision chain)
-- ---------------------------------------------------------------------------
--
-- What a terminal does when it cannot reach the platform is a SAFETY decision
-- with no default the platform can pick. A warehouse that must keep working
-- through an outage and a secure store that must not are both correct, and
-- neither is a sensible default for the other.
--
-- CACHED_GRACE is the value new and existing sites get: it degrades rather than
-- failing instantly, and the window is visible and bounded. CACHED_INDEFINITE --
-- which is what every terminal does TODAY, keeping its cached roster for ever --
-- stays available but must now be chosen knowingly, because a terminal cut off
-- from the platform admitting people revoked since is a decision, not a default.
ALTER TABLE sites ADD COLUMN IF NOT EXISTS offline_policy VARCHAR(24) NOT NULL DEFAULT 'CACHED_GRACE';
ALTER TABLE sites ADD COLUMN IF NOT EXISTS offline_grace_minutes INTEGER NOT NULL DEFAULT 720;

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_offline_policy_check;
ALTER TABLE sites ADD CONSTRAINT sites_offline_policy_check
    CHECK (offline_policy IN ('DENY_ALL', 'CACHED_GRACE', 'CACHED_INDEFINITE'));

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_offline_grace_check;
ALTER TABLE sites ADD CONSTRAINT sites_offline_grace_check
    CHECK (offline_grace_minutes BETWEEN 0 AND 43200);

COMMENT ON COLUMN sites.offline_policy IS
    'What terminals at this site do when they cannot reach the platform. '
    'DENY_ALL refuses everything. CACHED_GRACE honours the cached roster for '
    'offline_grace_minutes then falls back to DENY_ALL. CACHED_INDEFINITE '
    'honours it for ever, which is what terminals did before this column and '
    'must now be chosen deliberately.';

-- Existing sites keep TODAY'S behaviour rather than being silently tightened.
-- Changing what a deployed door does during a migration is not something a
-- schema change is entitled to do; the console surfaces the choice instead.
UPDATE sites SET offline_policy = 'CACHED_INDEFINITE'
 WHERE deleted_at IS NULL
   AND created_at < CURRENT_TIMESTAMP;

COMMIT;
