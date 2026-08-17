-- ---------------------------------------------------------------------------
-- 019: device claim codes
-- ---------------------------------------------------------------------------
--
-- Requested by docs/firmware-protocol-requirements.md section 6 in the FIRMWARE
-- repository, which is the contract of record between the two sides.
--
-- THE PROBLEM. Enrolling one terminal requires the SITE API KEY on an
-- installer's laptop. That key is the provisioning secret for the whole site:
-- it registers devices there and rotates any of their credentials. To bring up
-- one door it ends up in a shell history and a terminal's scrollback, and the
-- device key it produces is then read out and pasted, so that exists in two
-- more places nobody will clean.
--
-- THE EXCHANGE. An operator creates the terminal in the console -- authenticated,
-- scoped, audited -- and gets a short single-use code. The installer types it
-- into the unit. The unit exchanges it for its own credential, which is never
-- displayed anywhere.
--
-- WHY THIS IS A TABLE AND NOT A COLUMN ON devices. A claim code is issued
-- against a serial that may not have a device row yet -- that is the normal
-- case, because the operator is pre-authorising hardware that has not arrived.
-- It also has to survive being superseded: issuing a second code for the same
-- serial must invalidate the first, and a column would lose the record that the
-- first ever existed. The audit question "who issued a code for this serial, and
-- was it used" needs the history.
--
-- WHAT MAKES AN INTERCEPTED CODE CLOSE TO WORTHLESS: it is bound to ONE serial,
-- which the device derives from its factory MAC and sends back. A code lifted
-- from an installer's screen cannot be redeemed by other hardware.

BEGIN;

CREATE TABLE IF NOT EXISTS device_claim_codes (
    id         BIGSERIAL PRIMARY KEY,
    public_id  UUID NOT NULL DEFAULT gen_random_uuid(),

    -- The site the terminal will belong to. RESTRICT rather than CASCADE: a
    -- claim code is a security record and must not vanish because somebody
    -- removed a site, which is exactly when you would want to read it.
    site_id    BIGINT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,

    -- Denormalised so the redemption path -- which is UNAUTHENTICATED and must
    -- be as narrow as possible -- can scope to a tenant without a join.
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,

    -- SHA-256 of the plaintext, like every other secret in this schema. The
    -- code itself exists exactly once, in the response that mints it.
    code_hash  CHAR(64) NOT NULL,

    -- The non-secret leading characters, so the console can say WHICH code was
    -- issued without being able to reproduce it.
    code_prefix VARCHAR(8) NOT NULL,

    -- BOUND TO ONE SERIAL. The whole security argument for an unauthenticated
    -- endpoint rests on this column.
    serial_number VARCHAR(64) NOT NULL,

    -- Who issued it. Nullable because the operator row may be removed later and
    -- the security record must outlive them; issued_by_email keeps it readable.
    issued_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    issued_by_email VARCHAR(255),

    expires_at  TIMESTAMPTZ NOT NULL,

    -- Redemption. All three are set together or none is.
    redeemed_at        TIMESTAMPTZ,
    redeemed_ip        VARCHAR(45),
    redeemed_device_id BIGINT REFERENCES devices(id) ON DELETE SET NULL,

    -- Superseded by a newer code for the same serial, or revoked by an
    -- operator. Distinct from redeemed: nobody used this one.
    superseded_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT device_claim_codes_hash_check
        CHECK (code_hash ~ '^[0-9a-f]{64}$'),

    CONSTRAINT device_claim_codes_serial_check
        CHECK (length(btrim(serial_number)) > 0),

    -- A redeemed code must say when and by what. "Used, but we cannot say by
    -- which unit" is not an answer an audit can accept.
    CONSTRAINT device_claim_codes_redemption_check
        CHECK ((redeemed_at IS NULL) = (redeemed_device_id IS NULL)),

    -- Redeemed and superseded are mutually exclusive states. A code that was
    -- used cannot later be described as never used.
    CONSTRAINT device_claim_codes_exclusive_state_check
        CHECK (redeemed_at IS NULL OR superseded_at IS NULL)
);

CREATE UNIQUE INDEX IF NOT EXISTS device_claim_codes_public_id_key
    ON device_claim_codes(public_id);

-- The redemption lookup: by hash, globally unique so a code resolves without
-- the caller naming a tenant. That is the point -- the device knows nothing
-- except its serial and the code it was given.
CREATE UNIQUE INDEX IF NOT EXISTS device_claim_codes_hash_key
    ON device_claim_codes(code_hash);

-- AT MOST ONE LIVE CODE PER SERIAL, per site. Issuing a second supersedes the
-- first rather than leaving two valid codes for one door, which is what
-- "single use" has to mean in practice: an installer holding an older printout
-- must not be able to claim the unit a colleague just re-issued.
CREATE UNIQUE INDEX IF NOT EXISTS device_claim_codes_live_serial_key
    ON device_claim_codes(site_id, serial_number)
    WHERE redeemed_at IS NULL AND superseded_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_device_claim_codes_company
    ON device_claim_codes(company_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_device_claim_codes_expiry
    ON device_claim_codes(expires_at)
    WHERE redeemed_at IS NULL AND superseded_at IS NULL;

COMMENT ON TABLE device_claim_codes IS
    'Single-use, serial-bound, short-lived codes that let a terminal fetch its '
    'own device credential without the site provisioning key ever reaching an '
    'installer (019_device_claim_codes.sql). Stored hashed; '
    'the plaintext exists once, in the response that mints it.';

COMMIT;
