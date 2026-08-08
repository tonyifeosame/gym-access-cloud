-- ---------------------------------------------------------------------------
-- 007: scope the firmware catalog to a company
-- ---------------------------------------------------------------------------
--
-- The firmware catalog was the one table in the schema with no tenant column.
-- Every other entity hangs off company_id and every query filters on it, but
-- firmware_versions was global, which meant any site API key could:
--
--   * read the whole catalog, including every other tenant's builds, their
--     download URLs and their checksums;
--   * insert a build into it; and
--   * flip `is_current` for a (device_type, release_channel) pair, which is the
--     value every other tenant's "is this terminal outdated" reporting is
--     measured against.
--
-- The last one is the sharp edge. `is_current` was unique per
-- (device_type, release_channel) across the whole installation, so two
-- companies could not even hold different target versions -- one tenant setting
-- its target silently moved everyone else's, and marked their fleets outdated.
-- Once OTA exists, that same row is what a terminal would be pointed at, so a
-- cross-tenant write here becomes a cross-tenant firmware push.
--
-- Assigning the catalog to a company fixes all three with the tenancy rule the
-- rest of the schema already uses, and needs no new concept of an operator or
-- an admin role.
--
-- Existing rows are adopted by the oldest company, which is the same tenant
-- migration 002 assigned every other pre-existing row to.

BEGIN;

ALTER TABLE firmware_versions ADD COLUMN IF NOT EXISTS company_id BIGINT;

UPDATE firmware_versions
   SET company_id = (SELECT id FROM companies ORDER BY id LIMIT 1)
 WHERE company_id IS NULL;

-- A catalog row with no owner would be readable by nobody and is therefore
-- worse than useless; fail the migration rather than leave one behind.
ALTER TABLE firmware_versions ALTER COLUMN company_id SET NOT NULL;

ALTER TABLE firmware_versions DROP CONSTRAINT IF EXISTS firmware_versions_company_id_fkey;
ALTER TABLE firmware_versions ADD CONSTRAINT firmware_versions_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE;

-- Uniqueness was installation-wide and has to become per-tenant, or one company
-- publishing version "1.4.0" would block every other company from doing so.
DROP INDEX IF EXISTS firmware_versions_device_type_version_key;
CREATE UNIQUE INDEX IF NOT EXISTS firmware_versions_company_device_type_version_key
    ON firmware_versions(company_id, device_type, version) WHERE deleted_at IS NULL;

-- The deployment target is per tenant, per device type, per channel.
DROP INDEX IF EXISTS firmware_versions_current_key;
CREATE UNIQUE INDEX IF NOT EXISTS firmware_versions_company_current_key
    ON firmware_versions(company_id, device_type, release_channel)
    WHERE is_current AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_firmware_versions_company
    ON firmware_versions(company_id) WHERE deleted_at IS NULL;

COMMIT;
