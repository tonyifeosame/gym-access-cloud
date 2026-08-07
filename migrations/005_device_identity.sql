-- Sprint 5: device identity and credentials
--
-- Until now a terminal proved itself with the site API key plus an
-- X-Device-Serial header. That authenticates the *site*: every terminal at a
-- site shares one secret, so a single stolen unit compromises all of them, one
-- device can poll another's job queue, and a lost device cannot be revoked
-- without re-keying the whole site.
--
-- Each device now gets its own credential, issued at registration.
--
-- The credential is stored as a SHA-256 hash, never in plaintext. Lookup is an
-- exact match on the hash, so this stays a single index probe. A short
-- non-secret prefix is kept alongside it so a key can be identified in logs and
-- in an admin UI without being able to reconstruct it.
--
-- Note the asymmetry with sites.api_key, which is still plaintext. That predates
-- this migration and is left alone deliberately: changing it would break every
-- currently-provisioned site's authentication. Hashing site keys belongs in its
-- own migration with a rotation window.

BEGIN;

ALTER TABLE devices ADD COLUMN IF NOT EXISTS api_key_hash CHAR(64);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS api_key_prefix VARCHAR(16);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS registered_at TIMESTAMP;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS last_heartbeat_at TIMESTAMP;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS reported_firmware VARCHAR(50);

-- One credential per live device
CREATE UNIQUE INDEX IF NOT EXISTS devices_api_key_hash_key
    ON devices(api_key_hash) WHERE api_key_hash IS NOT NULL AND deleted_at IS NULL;

-- A hash is 64 lowercase hex characters, or absent for a device that has not
-- yet been issued a credential.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_api_key_hash_check;
ALTER TABLE devices ADD CONSTRAINT devices_api_key_hash_check
    CHECK (api_key_hash IS NULL OR api_key_hash ~ '^[0-9a-f]{64}$');

-- A registered device must hold a credential
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_registration_check;
ALTER TABLE devices ADD CONSTRAINT devices_registration_check
    CHECK (registered_at IS NULL OR api_key_hash IS NOT NULL);

-- Fleet health: "which terminals have gone quiet"
CREATE INDEX IF NOT EXISTS idx_devices_last_heartbeat_at
    ON devices(last_heartbeat_at) WHERE deleted_at IS NULL;

COMMIT;
