-- ---------------------------------------------------------------------------
-- Development seed data -- NOT FOR PRODUCTION
-- ---------------------------------------------------------------------------
--
-- The API keys below are committed to the repository and are therefore public.
-- They exist so a local stack and the ESP32 firmware have something to talk to
-- without a provisioning step.
--
-- This file is deliberately NOT part of migrations/. A database built from the
-- migrations has no sites and no credentials; loading this is an explicit act.
--
--     psql -U postgres -d access_terminal -f seeds/dev_seed.sql
--
-- The guard below refuses to run when SEED_ALLOW_INSECURE is not set, so it
-- cannot be applied to a database by accident:
--
--     psql -U postgres -d access_terminal -v SEED_ALLOW_INSECURE=1 -f seeds/dev_seed.sql

\if :{?SEED_ALLOW_INSECURE}
\else
\echo '******************************************************************'
\echo 'refusing to seed: this inserts publicly known API keys.'
\echo 'Re-run with -v SEED_ALLOW_INSECURE=1 if this is a dev database.'
\echo '******************************************************************'
\quit
\endif

BEGIN;

-- Sites belong to the tenant migration 002 created.
--
-- The keys below are stored as SHA-256 hashes, exactly as a real one is since
-- 011_site_credentials.sql -- there is no plaintext column to write to any
-- more. The PLAINTEXT is in this file, in the VALUES list, which is the whole
-- reason this script refuses to run without SEED_ALLOW_INSECURE: these are
-- publicly known development credentials, not secrets.
--
-- Hashing them here rather than special-casing seed data keeps one
-- authentication path for every caller. A seed that wrote plaintext would need
-- the server to accept plaintext, which is precisely the hole 011 closed.
INSERT INTO sites (company_id, site_name, api_key_hash, api_key_prefix, active)
SELECT c.id, v.site_name,
       encode(sha256(v.api_key::bytea), 'hex'), left(v.api_key, 12), TRUE
  FROM companies c
  CROSS JOIN (VALUES
      ('Main Site',    'main-site-api-key-123'),
      ('Lekki Branch', 'lekki-branch-api-key-456'),
      ('Abuja Branch', 'abuja-branch-api-key-789')
  ) AS v(site_name, api_key)
 WHERE c.slug = 'default'
   AND NOT EXISTS (
        SELECT 1 FROM sites s
         WHERE s.company_id = c.id AND s.site_name = v.site_name AND s.deleted_at IS NULL
   );

-- One main entrance per site
INSERT INTO doors (site_id, door_name, description, direction)
SELECT s.id, 'Main Entrance', 'Primary entry door', 'IN'
  FROM sites s
 WHERE s.deleted_at IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM doors d
         WHERE d.site_id = s.id AND d.door_name = 'Main Entrance' AND d.deleted_at IS NULL
   );

-- A build for the fleet to be measured against. Not marked current: leaving it
-- current would report every terminal as outdated against a placeholder.
INSERT INTO firmware_versions (company_id, version, device_type, release_channel, is_mandatory, published_at)
SELECT c.id, '1.0.0', 'TERMINAL', 'STABLE', FALSE, CURRENT_TIMESTAMP
  FROM companies c
 WHERE c.slug = 'default'
   AND NOT EXISTS (
        SELECT 1 FROM firmware_versions f
         WHERE f.company_id = c.id AND f.device_type = 'TERMINAL'
           AND f.version = '1.0.0' AND f.deleted_at IS NULL
   );

COMMIT;

\echo 'Seeded development sites. These API keys are public -- dev use only.'
