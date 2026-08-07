-- Sprint 2: Core domain schema for Access Terminal Cloud API
--
-- Introduces the multi-tenant domain model: companies own sites, sites have
-- doors, doors are controlled by devices, people hold permissions, and devices
-- are kept current through sync jobs and firmware versions.
--
-- This migration builds forward on 001_init_schema.sql. 001 is left untouched
-- as applied history; everything here is additive or a documented refactor of
-- existing objects. No existing rows are deleted.
--
-- Conventions established by this migration:
--
--   * id          BIGSERIAL, internal only. Never exposed over the API.
--   * public_id   UUID, the stable public identifier used in URLs/payloads.
--   * created_at / updated_at  on every table; updated_at is trigger-maintained.
--   * deleted_at  soft delete on entity tables. Rows are retained; every
--                 lookup filters `deleted_at IS NULL`.
--
-- Soft deletes are deliberately NOT applied to access_logs (an immutable audit
-- trail must not be erasable) or sync_jobs (transient work records with their
-- own terminal states). Both still carry created_at/updated_at.
--
-- Uniqueness on soft-deletable tables uses partial unique indexes scoped to
-- `WHERE deleted_at IS NULL`, so deleting a row frees its name/serial/API key
-- for reuse without a hard delete.
--
-- Day-of-week scheduling uses a bitmask: Mon=1, Tue=2, Wed=4, Thu=8, Fri=16,
-- Sat=32, Sun=64. 127 means "every day".
--
-- This migration contains no business logic: no permission evaluation, no
-- access decisions. Those land in a later sprint.

BEGIN;

-- gen_random_uuid() is built into PostgreSQL 13+; pgcrypto supplies it on 12.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------
-- companies: the tenant. Everything else ultimately hangs off a company.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS companies (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    name VARCHAR(150) NOT NULL,
    slug VARCHAR(64) NOT NULL,
    contact_email VARCHAR(255),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP
);

-- The tenant that adopts every row created before this migration
INSERT INTO companies (name, slug, active)
SELECT 'Default Company', 'default', TRUE
 WHERE NOT EXISTS (SELECT 1 FROM companies WHERE slug = 'default');

-- ---------------------------------------------------------------------------
-- firmware_versions: created before devices, which reference it
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS firmware_versions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    version VARCHAR(50) NOT NULL,
    device_type VARCHAR(30) NOT NULL DEFAULT 'TERMINAL',
    release_channel VARCHAR(20) NOT NULL DEFAULT 'STABLE',
    download_url TEXT,
    checksum_sha256 CHAR(64),
    size_bytes BIGINT,
    release_notes TEXT,
    is_mandatory BOOLEAN NOT NULL DEFAULT FALSE,
    published_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP,
    CONSTRAINT firmware_versions_channel_check
        CHECK (release_channel IN ('STABLE', 'BETA', 'CANARY')),
    CONSTRAINT firmware_versions_device_type_check
        CHECK (device_type IN ('TERMINAL', 'READER', 'CONTROLLER')),
    CONSTRAINT firmware_versions_size_check
        CHECK (size_bytes IS NULL OR size_bytes > 0)
);

-- ---------------------------------------------------------------------------
-- sites: now owned by a company (refactor of the 001 table)
-- ---------------------------------------------------------------------------
ALTER TABLE sites ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE sites ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS address TEXT;
ALTER TABLE sites ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) NOT NULL DEFAULT 'UTC';
ALTER TABLE sites ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP;

UPDATE sites
   SET company_id = (SELECT id FROM companies WHERE slug = 'default')
 WHERE company_id IS NULL;

ALTER TABLE sites ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE sites ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE sites ALTER COLUMN updated_at SET NOT NULL;

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_company_id_fkey;
ALTER TABLE sites ADD CONSTRAINT sites_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

-- Site names are unique per company, API keys globally; both ignore soft-deleted rows
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_site_name_key;
ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_api_key_key;
CREATE UNIQUE INDEX IF NOT EXISTS sites_company_id_site_name_key
    ON sites(company_id, site_name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS sites_api_key_key
    ON sites(api_key) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS sites_public_id_key ON sites(public_id);

-- ---------------------------------------------------------------------------
-- doors: a physical opening at a site
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS doors (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    site_id BIGINT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
    door_name VARCHAR(100) NOT NULL,
    description TEXT,
    direction VARCHAR(10) NOT NULL DEFAULT 'IN',
    unlock_duration_seconds SMALLINT NOT NULL DEFAULT 5,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP,
    CONSTRAINT doors_direction_check CHECK (direction IN ('IN', 'OUT', 'BOTH')),
    CONSTRAINT doors_unlock_duration_check
        CHECK (unlock_duration_seconds BETWEEN 1 AND 60)
);

-- ---------------------------------------------------------------------------
-- devices: the terminals/readers installed at a door
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS devices (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    site_id BIGINT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
    door_id BIGINT REFERENCES doors(id) ON DELETE SET NULL,
    serial_number VARCHAR(100) NOT NULL,
    device_name VARCHAR(100) NOT NULL,
    device_type VARCHAR(30) NOT NULL DEFAULT 'TERMINAL',
    firmware_version_id BIGINT REFERENCES firmware_versions(id) ON DELETE SET NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PROVISIONING',
    ip_address VARCHAR(45),
    last_seen_at TIMESTAMP,
    last_sync_at TIMESTAMP,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP,
    CONSTRAINT devices_type_check
        CHECK (device_type IN ('TERMINAL', 'READER', 'CONTROLLER')),
    CONSTRAINT devices_status_check
        CHECK (status IN ('PROVISIONING', 'ONLINE', 'OFFLINE', 'RETIRED'))
);

-- ---------------------------------------------------------------------------
-- people: refactor of the 001 `members` table.
-- `member_id` becomes `external_id` -- the badge/member number the terminal
-- sends -- and is now unique per company rather than globally.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF to_regclass('public.members') IS NOT NULL
       AND to_regclass('public.people') IS NULL THEN
        ALTER TABLE members RENAME TO people;
        ALTER TABLE people RENAME COLUMN member_id TO external_id;
        ALTER SEQUENCE members_id_seq RENAME TO people_id_seq;
        ALTER TRIGGER update_members_updated_at ON people
            RENAME TO update_people_updated_at;
    END IF;
END $$;

ALTER TABLE people ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE people ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE people ADD COLUMN IF NOT EXISTS person_type VARCHAR(20) NOT NULL DEFAULT 'MEMBER';
ALTER TABLE people ADD COLUMN IF NOT EXISTS email VARCHAR(255);
ALTER TABLE people ADD COLUMN IF NOT EXISTS phone VARCHAR(30);
ALTER TABLE people ADD COLUMN IF NOT EXISTS valid_from TIMESTAMP;
ALTER TABLE people ADD COLUMN IF NOT EXISTS valid_until TIMESTAMP;
ALTER TABLE people ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP;

UPDATE people
   SET company_id = (SELECT id FROM companies WHERE slug = 'default')
 WHERE company_id IS NULL;

ALTER TABLE people ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE people ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE people ALTER COLUMN updated_at SET NOT NULL;

ALTER TABLE people DROP CONSTRAINT IF EXISTS people_company_id_fkey;
ALTER TABLE people ADD CONSTRAINT people_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

ALTER TABLE people DROP CONSTRAINT IF EXISTS people_type_check;
ALTER TABLE people ADD CONSTRAINT people_type_check
    CHECK (person_type IN ('MEMBER', 'STAFF', 'CONTRACTOR', 'VISITOR'));

ALTER TABLE people DROP CONSTRAINT IF EXISTS people_validity_check;
ALTER TABLE people ADD CONSTRAINT people_validity_check
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from);

-- ---------------------------------------------------------------------------
-- enrollment_requests: repointed from the external string to people(id).
-- Done before the old unique key on external_id is replaced.
-- ---------------------------------------------------------------------------
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS person_id BIGINT;
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS device_id BIGINT;
ALTER TABLE enrollment_requests ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'enrollment_requests' AND column_name = 'member_id') THEN
        UPDATE enrollment_requests er
           SET person_id = p.id
          FROM people p
         WHERE er.person_id IS NULL
           AND p.external_id = er.member_id;
    END IF;
END $$;

ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_member_id_fkey;
ALTER TABLE enrollment_requests DROP COLUMN IF EXISTS member_id;
ALTER TABLE enrollment_requests ALTER COLUMN person_id SET NOT NULL;
ALTER TABLE enrollment_requests ALTER COLUMN created_at SET NOT NULL;

ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_person_id_fkey;
ALTER TABLE enrollment_requests ADD CONSTRAINT enrollment_requests_person_id_fkey
    FOREIGN KEY (person_id) REFERENCES people(id) ON DELETE CASCADE;

-- Which device picked the enrollment up, once one does
ALTER TABLE enrollment_requests DROP CONSTRAINT IF EXISTS enrollment_requests_device_id_fkey;
ALTER TABLE enrollment_requests ADD CONSTRAINT enrollment_requests_device_id_fkey
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS enrollment_requests_public_id_key
    ON enrollment_requests(public_id);

-- ---------------------------------------------------------------------------
-- access_logs: keep the historical text snapshots, add the new foreign keys.
-- No deleted_at: this is an immutable audit trail.
-- ---------------------------------------------------------------------------
ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_member_id_fkey;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_name = 'access_logs' AND column_name = 'member_id') THEN
        ALTER TABLE access_logs RENAME COLUMN member_id TO person_external_id;
    END IF;
END $$;

ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS company_id BIGINT;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS site_id BIGINT;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS door_id BIGINT;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS device_id BIGINT;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS person_id BIGINT;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS reason VARCHAR(50);
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS occurred_at TIMESTAMP;
ALTER TABLE access_logs ADD COLUMN IF NOT EXISTS updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;

-- Backfill the new keys from the denormalized columns 001 recorded
UPDATE access_logs al
   SET person_id = p.id
  FROM people p
 WHERE al.person_id IS NULL
   AND p.external_id = al.person_external_id;

UPDATE access_logs al
   SET site_id = s.id,
       company_id = s.company_id
  FROM sites s
 WHERE al.site_id IS NULL
   AND s.site_name = al.site_name;

UPDATE access_logs
   SET company_id = (SELECT id FROM companies WHERE slug = 'default')
 WHERE company_id IS NULL;

UPDATE access_logs SET occurred_at = created_at WHERE occurred_at IS NULL;

ALTER TABLE access_logs ALTER COLUMN occurred_at SET NOT NULL;
ALTER TABLE access_logs ALTER COLUMN occurred_at SET DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE access_logs ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE access_logs ALTER COLUMN created_at SET NOT NULL;

ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_company_id_fkey;
ALTER TABLE access_logs ADD CONSTRAINT access_logs_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

-- Logs outlive the entities that produced them, so these null out rather than cascade
ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_site_id_fkey;
ALTER TABLE access_logs ADD CONSTRAINT access_logs_site_id_fkey
    FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE SET NULL;

ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_door_id_fkey;
ALTER TABLE access_logs ADD CONSTRAINT access_logs_door_id_fkey
    FOREIGN KEY (door_id) REFERENCES doors(id) ON DELETE SET NULL;

ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_device_id_fkey;
ALTER TABLE access_logs ADD CONSTRAINT access_logs_device_id_fkey
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE SET NULL;

ALTER TABLE access_logs DROP CONSTRAINT IF EXISTS access_logs_person_id_fkey;
ALTER TABLE access_logs ADD CONSTRAINT access_logs_person_id_fkey
    FOREIGN KEY (person_id) REFERENCES people(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS access_logs_public_id_key ON access_logs(public_id);

-- ---------------------------------------------------------------------------
-- Nothing references people(external_id) any more, so scope it to the company
-- ---------------------------------------------------------------------------
ALTER TABLE people DROP CONSTRAINT IF EXISTS members_member_id_key;
ALTER TABLE people DROP CONSTRAINT IF EXISTS people_external_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS people_company_id_external_id_key
    ON people(company_id, external_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS people_public_id_key ON people(public_id);

-- ---------------------------------------------------------------------------
-- permissions: who may open what, and when.
-- Each row grants either a single door or a whole site, never both.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS permissions (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    person_id BIGINT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    door_id BIGINT REFERENCES doors(id) ON DELETE CASCADE,
    site_id BIGINT REFERENCES sites(id) ON DELETE CASCADE,
    access_level VARCHAR(20) NOT NULL DEFAULT 'STANDARD',
    days_of_week SMALLINT NOT NULL DEFAULT 127,
    start_time TIME,
    end_time TIME,
    starts_at TIMESTAMP,
    ends_at TIMESTAMP,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP,
    CONSTRAINT permissions_scope_check CHECK (
        (door_id IS NOT NULL AND site_id IS NULL) OR
        (door_id IS NULL AND site_id IS NOT NULL)
    ),
    CONSTRAINT permissions_level_check
        CHECK (access_level IN ('STANDARD', 'RESTRICTED', 'ADMIN')),
    CONSTRAINT permissions_days_check
        CHECK (days_of_week BETWEEN 0 AND 127),
    CONSTRAINT permissions_window_check
        CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at)
);

-- ---------------------------------------------------------------------------
-- sync_jobs: unit of work for pushing state out to a device.
-- No deleted_at: jobs have their own terminal states and are pruned by age.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sync_jobs (
    id BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),
    site_id BIGINT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    device_id BIGINT REFERENCES devices(id) ON DELETE CASCADE,
    job_type VARCHAR(30) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',
    since_timestamp TIMESTAMP,
    target_firmware_version_id BIGINT REFERENCES firmware_versions(id) ON DELETE SET NULL,
    records_total INTEGER NOT NULL DEFAULT 0,
    records_processed INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    payload JSONB,
    error_message TEXT,
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT sync_jobs_type_check CHECK (job_type IN (
        'FULL_SYNC', 'INCREMENTAL_SYNC', 'PERMISSION_PUSH',
        'TEMPLATE_PUSH', 'FIRMWARE_UPDATE', 'LOG_PULL'
    )),
    CONSTRAINT sync_jobs_status_check CHECK (status IN (
        'PENDING', 'IN_PROGRESS', 'COMPLETED', 'FAILED', 'CANCELLED'
    )),
    CONSTRAINT sync_jobs_progress_check
        CHECK (records_processed >= 0 AND records_total >= 0)
);

-- ---------------------------------------------------------------------------
-- Unique indexes (partial where the table is soft-deletable)
-- ---------------------------------------------------------------------------
CREATE UNIQUE INDEX IF NOT EXISTS companies_public_id_key ON companies(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS companies_slug_key
    ON companies(slug) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS doors_public_id_key ON doors(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS doors_site_id_door_name_key
    ON doors(site_id, door_name) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS devices_public_id_key ON devices(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS devices_serial_number_key
    ON devices(serial_number) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS firmware_versions_public_id_key ON firmware_versions(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS firmware_versions_device_type_version_key
    ON firmware_versions(device_type, version) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS permissions_public_id_key ON permissions(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS permissions_person_door_key
    ON permissions(person_id, door_id) WHERE door_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS permissions_person_site_key
    ON permissions(person_id, site_id) WHERE site_id IS NOT NULL AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS sync_jobs_public_id_key ON sync_jobs(public_id);

-- ---------------------------------------------------------------------------
-- Lookup indexes. Hot read paths are partial on `deleted_at IS NULL` to match
-- the predicate every query in database/queries.go applies.
-- ---------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_sites_company_id ON sites(company_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sites_active ON sites(active) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_doors_site_id ON doors(site_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_doors_active ON doors(active) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_devices_site_id ON devices(site_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_devices_door_id ON devices(door_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_devices_status ON devices(status) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_devices_firmware_version_id ON devices(firmware_version_id);
CREATE INDEX IF NOT EXISTS idx_devices_last_seen_at ON devices(last_seen_at);

CREATE INDEX IF NOT EXISTS idx_people_company_id ON people(company_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_people_external_id ON people(external_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_people_person_type ON people(person_type) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_people_updated_at ON people(updated_at);

CREATE INDEX IF NOT EXISTS idx_permissions_person_id ON permissions(person_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_permissions_door_id ON permissions(door_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_permissions_site_id ON permissions(site_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_permissions_active ON permissions(active) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_permissions_updated_at ON permissions(updated_at);

CREATE INDEX IF NOT EXISTS idx_access_logs_company_id ON access_logs(company_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_site_id ON access_logs(site_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_door_id ON access_logs(door_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_device_id ON access_logs(device_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_person_id ON access_logs(person_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_person_external_id ON access_logs(person_external_id);
CREATE INDEX IF NOT EXISTS idx_access_logs_occurred_at ON access_logs(occurred_at);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_site_id ON sync_jobs(site_id);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_device_id ON sync_jobs(device_id);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_status ON sync_jobs(status);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_created_at ON sync_jobs(created_at);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_pending
    ON sync_jobs(site_id, created_at) WHERE status = 'PENDING';

CREATE INDEX IF NOT EXISTS idx_firmware_versions_device_type ON firmware_versions(device_type) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_firmware_versions_channel ON firmware_versions(release_channel) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_enrollment_requests_person_id ON enrollment_requests(person_id);
CREATE INDEX IF NOT EXISTS idx_enrollment_requests_device_id ON enrollment_requests(device_id);

-- Superseded by the partial indexes above
DROP INDEX IF EXISTS idx_members_member_id;
DROP INDEX IF EXISTS idx_members_active;
DROP INDEX IF EXISTS idx_members_updated_at;
DROP INDEX IF EXISTS idx_enrollment_requests_member_id;

-- ---------------------------------------------------------------------------
-- updated_at triggers (update_updated_at_column() is defined in 001)
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_companies_updated_at ON companies;
CREATE TRIGGER update_companies_updated_at BEFORE UPDATE ON companies
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_doors_updated_at ON doors;
CREATE TRIGGER update_doors_updated_at BEFORE UPDATE ON doors
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_devices_updated_at ON devices;
CREATE TRIGGER update_devices_updated_at BEFORE UPDATE ON devices
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_permissions_updated_at ON permissions;
CREATE TRIGGER update_permissions_updated_at BEFORE UPDATE ON permissions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_sync_jobs_updated_at ON sync_jobs;
CREATE TRIGGER update_sync_jobs_updated_at BEFORE UPDATE ON sync_jobs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_firmware_versions_updated_at ON firmware_versions;
CREATE TRIGGER update_firmware_versions_updated_at BEFORE UPDATE ON firmware_versions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_enrollment_requests_updated_at ON enrollment_requests;
CREATE TRIGGER update_enrollment_requests_updated_at BEFORE UPDATE ON enrollment_requests
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- Data migration: product rename (Gym Access -> Access Terminal).
-- 001 seeded a 'Main Gym' site; renaming it here rather than editing 001 keeps
-- applied migration history intact. Any terminal still configured with the old
-- key must be repointed at 'main-site-api-key-123'.
-- ---------------------------------------------------------------------------
UPDATE sites
   SET site_name = 'Main Site',
       api_key = 'main-site-api-key-123'
 WHERE site_name = 'Main Gym'
   AND api_key = 'main-gym-api-key-123';

UPDATE access_logs SET site_name = 'Main Site' WHERE site_name = 'Main Gym';

-- ---------------------------------------------------------------------------
-- Development seed data (change or remove before production)
-- ---------------------------------------------------------------------------
INSERT INTO firmware_versions (version, device_type, release_channel, is_mandatory, published_at)
SELECT '1.0.0', 'TERMINAL', 'STABLE', FALSE, CURRENT_TIMESTAMP
 WHERE NOT EXISTS (
    SELECT 1 FROM firmware_versions
     WHERE device_type = 'TERMINAL' AND version = '1.0.0' AND deleted_at IS NULL
 );

-- One main entrance per existing site
INSERT INTO doors (site_id, door_name, description, direction)
SELECT s.id, 'Main Entrance', 'Primary entry door', 'IN'
  FROM sites s
 WHERE s.deleted_at IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM doors d
         WHERE d.site_id = s.id AND d.door_name = 'Main Entrance' AND d.deleted_at IS NULL
   );

COMMIT;
