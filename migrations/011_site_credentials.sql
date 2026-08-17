-- ---------------------------------------------------------------------------
-- 011: site API keys stop being recoverable
-- ---------------------------------------------------------------------------
--
-- sites.api_key held the PROVISIONING SECRET IN PLAINTEXT. Whoever holds a site
-- key can register a terminal at that site, rotate any device credential there,
-- read the whole roster and pull the access log. Storing it recoverably meant
-- that anything which could read one row of this table -- a backup, a replica,
-- a support engineer with SELECT, an injection on any query touching `sites` --
-- yielded a working credential for every site in every company on the
-- installation.
--
-- 005_device_identity.sql moved DEVICE credentials to a SHA-256 hash and left
-- this one alone, saying so explicitly: "Hashing site keys belongs in its own
-- migration with a rotation window." This is that migration.
--
-- WHY THIS BREAKS NOTHING
--
-- The wire contract does not change by a single byte. A caller still sends
--
--     X-API-Key: ats_<64 hex>
--
-- and still gets the same answer. What changes is only how the server FINDS the
-- site: it hashes what was presented and looks that up, instead of comparing
-- plaintext. Both are one index probe.
--
-- This is possible without a rotation window precisely because -- unlike a
-- password -- the plaintext is sitting in the column right now, so every
-- existing key's hash can be computed here. Every already-provisioned site, and
-- every terminal still on the deprecated site-key-plus-serial path, keeps
-- authenticating with the key it already holds. No firmware is aware of this.
--
-- WHAT IS ACTUALLY LOST, and it is the point: the key can no longer be READ BACK
-- out of the database. An operator who has lost theirs must now rotate it
-- (POST /api/v1/console/sites/{id}/api-key) rather than run a SELECT. That is
-- the property being bought.
--
-- SHA-256 rather than bcrypt, matching devices: the secret is 256 bits from
-- crypto/rand, not a human-chosen password, so it is not open to dictionary
-- attack and does not need a slow KDF. It does need to be verifiable on every
-- request without burning CPU.

BEGIN;

-- ---------------------------------------------------------------------------
-- The stored form
-- ---------------------------------------------------------------------------
--
-- api_key_prefix is the first 12 characters -- "ats_" plus 8 hex. NOT SECRET
-- and deliberately so: it lets a key be identified in a log line, in a support
-- conversation, or beside a rotation warning in the console, without anything
-- being reconstructible from it. Exactly the shape devices.api_key_prefix has.
ALTER TABLE sites ADD COLUMN IF NOT EXISTS api_key_hash CHAR(64);
ALTER TABLE sites ADD COLUMN IF NOT EXISTS api_key_prefix VARCHAR(16);
ALTER TABLE sites ADD COLUMN IF NOT EXISTS api_key_rotated_at TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- Carry the existing keys across
-- ---------------------------------------------------------------------------
--
-- pgcrypto's digest() would be the obvious tool and is deliberately not used:
-- it is an extension, it may not be installed, and CREATE EXTENSION needs
-- privileges a managed database often withholds. sha256() is built in from
-- PostgreSQL 11 and needs nothing.
--
-- The prefix of a legacy key is whatever its first 12 characters happen to be.
-- Keys created before this migration were not required to carry the ats_ prefix
-- -- the seed data and deploy/README.md both minted bare hex -- so the prefix
-- is descriptive of what is there rather than an assertion about its shape.
UPDATE sites
   SET api_key_hash   = encode(sha256(api_key::bytea), 'hex'),
       api_key_prefix = left(api_key, 12)
 WHERE api_key IS NOT NULL
   AND api_key_hash IS NULL;

-- ---------------------------------------------------------------------------
-- Constraints, matching devices.api_key_hash
-- ---------------------------------------------------------------------------

-- One live credential per site, and no two sites sharing one.
CREATE UNIQUE INDEX IF NOT EXISTS sites_api_key_hash_key
    ON sites(api_key_hash) WHERE api_key_hash IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_api_key_hash_check;
ALTER TABLE sites ADD CONSTRAINT sites_api_key_hash_check
    CHECK (api_key_hash IS NULL OR api_key_hash ~ '^[0-9a-f]{64}$');

-- ---------------------------------------------------------------------------
-- Refuse to proceed if anything would be locked out
-- ---------------------------------------------------------------------------
--
-- A live site whose hash did not get set would silently stop authenticating the
-- moment the plaintext column disappears, and the symptom would be every
-- terminal at that site failing at once with "Invalid API key". Far better to
-- fail the migration.
DO $$
DECLARE
    orphaned int;
BEGIN
    SELECT count(*) INTO orphaned
      FROM sites
     WHERE deleted_at IS NULL
       AND api_key_hash IS NULL;

    IF orphaned > 0 THEN
        RAISE EXCEPTION
            '% live site(s) have no api_key_hash; migrating would lock out their '
            'terminals. Every live site must carry an api_key before this runs.',
            orphaned;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- Drop the plaintext
-- ---------------------------------------------------------------------------
--
-- THIS IS THE IRREVERSIBLE PART, and it is the entire purpose. Keeping the
-- column "just in case" would preserve exactly the exposure this migration
-- exists to remove, and a column nobody reads is one somebody eventually does.
--
-- Its unique index goes with it.
DROP INDEX IF EXISTS sites_api_key_key;
ALTER TABLE sites DROP COLUMN IF EXISTS api_key;

COMMENT ON COLUMN sites.api_key_hash IS
    'SHA-256 of the site provisioning key. The key itself is returned once, at '
    'creation or rotation, and is not recoverable from this database.';
COMMENT ON COLUMN sites.api_key_prefix IS
    'First 12 characters of the key. NOT secret; identifies a key in logs and UI.';

COMMIT;
