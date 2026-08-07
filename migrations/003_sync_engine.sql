-- Sprint 4: Synchronization engine
--
-- Turns sync_jobs into a per-device outbox. Every write to a synced entity
-- transactionally enqueues one job per device that must learn about it, so a
-- change and its delivery record commit together or not at all.
--
-- Why an outbox rather than a changes feed:
--
--   A feed (`GET /members/changes?since=...`) can only describe rows that still
--   exist. A soft-deleted person disappears from it, so an offline terminal
--   keeps a cached credential forever and keeps opening the door. Deletions
--   must be pushed as their own durable, acknowledged fact. That is what this
--   table now is.
--
-- Design notes:
--
--   * Jobs are self-contained. `payload` is a snapshot taken at enqueue time and
--     there is deliberately NO foreign key from entity_id to people(id): the job
--     must stay applicable even after the row it describes is purged. This is
--     the standard outbox trade-off -- integrity is carried by the snapshot.
--
--   * Delivery is a lease, not a state change. Fetching a job leaves it PENDING
--     and pushes `next_attempt_at` forward. Only an acknowledgement moves it to
--     COMPLETED, enforced by sync_jobs_ack_check below. A device that dies
--     mid-apply simply has the job redelivered when its lease expires.
--
--   * Ordering is by `id`. There is no `after_sequence` cursor, because a cursor
--     over a sequence is unsafe: transactions commit out of order, so a reader
--     can skip past an id that had not yet committed when it read. Progress is
--     tracked by acknowledgement instead, which has no such gap.
--
--   * CREATE and UPDATE are both upserts on the device. Redelivery is therefore
--     harmless, which is what makes the protocol idempotent.

BEGIN;

-- ---------------------------------------------------------------------------
-- Job types: the change operations join the existing operational job types
-- ---------------------------------------------------------------------------
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_type_check CHECK (job_type IN (
    -- entity change operations (Sprint 4)
    'CREATE', 'UPDATE', 'DELETE', 'SETTINGS',
    -- operational jobs (Sprint 2)
    'FULL_SYNC', 'INCREMENTAL_SYNC', 'PERMISSION_PUSH',
    'TEMPLATE_PUSH', 'FIRMWARE_UPDATE', 'LOG_PULL'
));

-- ---------------------------------------------------------------------------
-- Protocol versioning, carried from the first job ever written.
-- Lets a newer server keep serving older firmware through an upgrade.
-- ---------------------------------------------------------------------------
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS protocol_version SMALLINT NOT NULL DEFAULT 1;

-- What the job is about
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS entity_type VARCHAR(30);
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS entity_id BIGINT;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS entity_external_id VARCHAR(50);

-- Delivery / acknowledgement bookkeeping
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMP;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS max_attempts SMALLINT NOT NULL DEFAULT 10;
ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMP;

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_entity_type_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_entity_type_check
    CHECK (entity_type IS NULL OR entity_type IN ('PERSON', 'SETTINGS'));

-- A change job is always addressed to exactly one device
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_change_device_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_change_device_check CHECK (
    job_type NOT IN ('CREATE', 'UPDATE', 'DELETE', 'SETTINGS')
    OR device_id IS NOT NULL
);

-- A person change must carry the external id, since that is the only handle the
-- terminal has -- and for DELETE the row it names may already be soft-deleted.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_person_entity_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_person_entity_check CHECK (
    entity_type IS DISTINCT FROM 'PERSON' OR entity_external_id IS NOT NULL
);

-- The core guarantee: a job is COMPLETED only if it was acknowledged.
ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_ack_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_ack_check CHECK (
    (status = 'COMPLETED') = (acknowledged_at IS NOT NULL)
);

ALTER TABLE sync_jobs DROP CONSTRAINT IF EXISTS sync_jobs_attempts_check;
ALTER TABLE sync_jobs ADD CONSTRAINT sync_jobs_attempts_check
    CHECK (attempts >= 0 AND max_attempts > 0);

-- ---------------------------------------------------------------------------
-- Indexes
-- ---------------------------------------------------------------------------

-- The hot path: "give me this device's due work, oldest first"
CREATE INDEX IF NOT EXISTS idx_sync_jobs_device_due
    ON sync_jobs(device_id, next_attempt_at, id)
    WHERE status = 'PENDING';

-- Operational visibility: which devices are behind, and by how much
CREATE INDEX IF NOT EXISTS idx_sync_jobs_unacknowledged
    ON sync_jobs(device_id, created_at)
    WHERE acknowledged_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_sync_jobs_entity
    ON sync_jobs(entity_type, entity_external_id);

-- ---------------------------------------------------------------------------
-- Backfill: any pre-existing rows predate acknowledgement bookkeeping
-- ---------------------------------------------------------------------------
UPDATE sync_jobs
   SET acknowledged_at = COALESCE(completed_at, CURRENT_TIMESTAMP)
 WHERE status = 'COMPLETED' AND acknowledged_at IS NULL;

COMMIT;
