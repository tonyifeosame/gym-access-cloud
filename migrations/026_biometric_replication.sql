-- ---------------------------------------------------------------------------
-- 026: single-enrolment biometric replication
-- ---------------------------------------------------------------------------
--
-- 012 built `credentials` with four sealing columns and `credential_placements`
-- with a five-state machine, and said in its own comment that the point was to
-- make "enrol once, recognised everywhere" expressible. Then nothing used the
-- sealing columns, because there was no key to seal with, no way for a template
-- to travel, and -- most concretely -- the fitted fingerprint driver implements
-- neither half of the sensor's template transfer.
--
-- This migration adds the three things that genuinely did not exist. It adds
-- nothing else: the placement state machine, the generation counter, the roster
-- rule and the capability column are all reused exactly as they stand.
--
--   1. A KEY TO SEAL WITH.            company_sealing_keys
--   2. A STATEMENT OF WHICH SENSOR.   credentials.sensor_profile,
--                                     devices.sensor_profile
--   3. TRANSFER BOOKKEEPING.          credential_placements.applied_digest,
--                                     credential_placements.source_device_id
--
-- The full design is docs/biometric-replication.md; the key's lifecycle and
-- threat model, reviewed and approved before this was written, is
-- docs/sealing-key-lifecycle.md. Read the second one before changing anything
-- about company_sealing_keys.

BEGIN;

-- ---------------------------------------------------------------------------
-- company_sealing_keys: the first secret in this schema that is RECOVERABLE
-- ---------------------------------------------------------------------------
--
-- Every other secret here is stored so that it CANNOT be read back. Site keys
-- became a SHA-256 in 011 and device credentials in 005, both deliberately, and
-- handlers/announcements.go refuses to mint a device key at approval time on
-- exactly that principle -- "a key minted here would have to be STORED in
-- plaintext until the device arrived, which is the one thing every other secret
-- in this schema is careful never to do."
--
-- A SEALING KEY CANNOT FOLLOW THAT RULE, and the difference is worth stating
-- rather than leaving for a reviewer to notice as an inconsistency. A hash
-- verifies a secret somebody presents. This key is never presented -- it is
-- HANDED OUT, once per terminal, and a hash cannot be handed out. It has to
-- survive in a form the server can turn back into 32 usable bytes.
--
-- So it is WRAPPED instead: AES-256-GCM under a master key that lives in the
-- deployment environment and never in this database. That buys the specific
-- property the whole feature rests on:
--
--     A backup, a read replica, a support engineer with SELECT, or an injection
--     on any query touching `credentials` yields ciphertext and wrapped keys,
--     and decrypts NOTHING.
--
-- WHAT IT DOES NOT BUY, stated here because this column is where a future reader
-- will look first: an attacker who holds the running server -- its environment
-- or its process memory -- and this database can decrypt every template in it.
-- The claim in 012 that material is sealed "under a key the server never holds"
-- is FALSE, and 012 STILL SAYS IT. That is deliberate: 012 is applied in
-- production and its checksum is recorded in schema_migrations, so editing it
-- would make deploy/migrate.sh refuse to run -- it hashes the whole file and
-- cannot tell a comment from a statement. An applied migration is a historical
-- record of what was run, not a document to keep current.
--
-- So the correction lives HERE, in the migration that makes the claim false,
-- and in models/identity.go and docs/sealing-key-lifecycle.md. A reader who
-- starts at 012 is one grep from this file; a reader who starts here has the
-- truth immediately. THE TRUE STATEMENT: the database alone yields nothing;
-- the database plus the master key in the server's environment yields
-- everything.
CREATE TABLE IF NOT EXISTS company_sealing_keys (
    id         BIGSERIAL PRIMARY KEY,
    public_id  UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,

    -- The NON-SECRET label. Travels to terminals, and lands in
    -- credentials.sealed_key_id so material can be matched to the key that
    -- sealed it. Naming a key is not disclosing one.
    key_id VARCHAR(64) NOT NULL,

    -- The key itself, AES-256-GCM under the deployment master key, nonce
    -- prepended. The AAD is `company_id|key_id`, so a wrapped key lifted into
    -- another company's row fails to unwrap rather than silently working.
    wrapped_key BYTEA NOT NULL,

    -- Which master key wrapped it. Recorded rather than assumed so the master
    -- key can be rotated without every sealed credential becoming unreadable.
    master_key_id VARCHAR(32) NOT NULL,

    algorithm VARCHAR(32) NOT NULL DEFAULT 'AES-256-GCM',

    -- RETIRED keys stay unwrappable ON PURPOSE. Material sealed under an old key
    -- is still material somebody's finger depends on; a retired key that could
    -- not be recovered would lock every person sealed under it out of every door
    -- they were not already placed on.
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retired_at TIMESTAMPTZ,

    CONSTRAINT company_sealing_keys_status_check
        CHECK (status IN ('ACTIVE', 'RETIRED')),

    -- A retired key must say when, the same shape as credentials_revocation_check.
    CONSTRAINT company_sealing_keys_retirement_check
        CHECK ((status = 'RETIRED') = (retired_at IS NOT NULL)),

    CONSTRAINT company_sealing_keys_key_id_check
        CHECK (key_id ~ '^ck_[0-9a-f]{12}$')
);

CREATE UNIQUE INDEX IF NOT EXISTS company_sealing_keys_public_id_key
    ON company_sealing_keys(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS company_sealing_keys_company_key_id_key
    ON company_sealing_keys(company_id, key_id);

-- EXACTLY ONE key may be sealed WITH at a time, enforced rather than left to the
-- application. Two ACTIVE keys would mean two terminals of the same company
-- sealing under different keys, and material that only some doors can read --
-- which presents as "this member works at three doors and not the fourth", the
-- exact complaint this whole feature exists to end.
CREATE UNIQUE INDEX IF NOT EXISTS company_sealing_keys_one_active
    ON company_sealing_keys(company_id) WHERE status = 'ACTIVE';

COMMENT ON TABLE company_sealing_keys IS
    'Per-company biometric sealing keys, wrapped under the deployment master '
    'key (SEALING_MASTER_KEY). The server unwraps one ONLY to hand it to a '
    'terminal collecting its credential -- never on the material path. See '
    'docs/sealing-key-lifecycle.md.';

-- ---------------------------------------------------------------------------
-- sensor_profile: the ONLY compatibility rule, and it is equality
-- ---------------------------------------------------------------------------
--
-- A ZFM template is a proprietary feature vector. Whether one exported from the
-- module fitted today imports AND MATCHES on a different module IS NOT KNOWN.
-- It is not established by the protocol being symmetric, and it is not
-- established by two parts both being R307-class.
--
-- So nothing here asserts compatibility. A terminal reports what its module
-- actually answers -- `vendor:system_id:capacity`, e.g. `ZFM:0x0009:1000` --
-- the credential records the profile of the module its template came off, and
-- MATERIAL MOVES ONLY BETWEEN BYTE-EQUAL PROFILES.
--
-- There is deliberately NO table of "these are probably compatible" and no
-- inference from the vendor string alone. Widening this later is a tested row
-- in an allow-list, added only once a specific pair has been shown on hardware
-- to actually match. Until then a module reporting a different profile receives
-- no material, and falls back to the pre-existing re-enrolment work list --
-- which is a working product, not a failure.
--
-- NULL means NEVER REPORTED, and is never a replication target. Fail closed,
-- the same three-valued discipline 025 established for capabilities.
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS sensor_profile VARCHAR(64);
ALTER TABLE devices     ADD COLUMN IF NOT EXISTS sensor_profile VARCHAR(64);
ALTER TABLE devices     ADD COLUMN IF NOT EXISTS sensor_profile_reported_at TIMESTAMPTZ;

COMMENT ON COLUMN credentials.sensor_profile IS
    'The module the template was captured on. Material is served to a terminal '
    'only when devices.sensor_profile is BYTE-EQUAL to this. Cross-module '
    'compatibility is UNTESTED and is not claimed anywhere.';
COMMENT ON COLUMN devices.sensor_profile IS
    'What this terminal''s fingerprint module reports: vendor:system_id:capacity. '
    'NULL means never reported, and is never a replication target.';

-- The fan-out read: "who else should hold this, on a matching module".
CREATE INDEX IF NOT EXISTS idx_devices_sensor_profile
    ON devices(sensor_profile) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Transfer bookkeeping on the placement
-- ---------------------------------------------------------------------------
--
-- applied_digest is what the RECEIVING terminal says it actually wrote, so a
-- template that arrived corrupted or was applied to the wrong person is
-- visible rather than silent.
--
-- THE SERVER CANNOT VERIFY IT. It has no key on this path and cannot decrypt
-- the material to check. It stores the value and COMPARES it to
-- credentials.material_digest; agreement is two devices agreeing, not the
-- platform attesting. Said here because a reader finding a digest column will
-- otherwise assume it was validated.
ALTER TABLE credential_placements
    ADD COLUMN IF NOT EXISTS applied_digest CHAR(64);

ALTER TABLE credential_placements
    DROP CONSTRAINT IF EXISTS credential_placements_applied_digest_check;
ALTER TABLE credential_placements
    ADD CONSTRAINT credential_placements_applied_digest_check
    CHECK (applied_digest IS NULL OR applied_digest ~ '^[0-9a-f]{64}$');

-- Which terminal's material fed this placement. Audit, and the evidence trail
-- the hardware-validation tests (docs/biometric-replication.md §9) are read
-- against: "this template reached that door from this module".
--
-- ON DELETE SET NULL, matching credentials.enrolled_device_id: a placement
-- outlives the terminal that sourced it, which is the entire point of holding
-- material centrally.
ALTER TABLE credential_placements
    ADD COLUMN IF NOT EXISTS source_device_id BIGINT
    REFERENCES devices(id) ON DELETE SET NULL;

COMMENT ON COLUMN credential_placements.applied_digest IS
    'SHA-256 of the plaintext the receiving device says it wrote. RECORDED AND '
    'COMPARED, never verified -- the server holds no key on this path.';

COMMIT;
