-- 030_api_credentials.sql
--
-- Integration credentials: the fourth credential class.
--
-- ---------------------------------------------------------------------------
-- WHY A NEW CLASS RATHER THAN A NARROWED SITE KEY
-- ---------------------------------------------------------------------------
--
-- The site API key is the PROVISIONING SECRET. It authorises
-- POST /devices/register, which mints device credentials, and it reads the whole
-- company's people through GET /api/v1/members. Handing one to a customer's
-- booking system hands them the ability to enrol terminals at that site.
--
-- So an integration gets its own credential, with its own prefix (atp_, beside
-- ats_ for a site and atd_ for a device), its own scopes, its own expiry and its
-- own revocation. Nothing in this table can register a terminal or rotate a
-- device key, because no route will ever read it for that purpose.
--
-- NOTHING AUTHENTICATES WITH THESE ROWS YET. This migration and the console
-- routes that manage them are P1 of the public API; the public tree that would
-- accept one does not exist. That is deliberate: a credential a customer can be
-- issued, listed, rotated and revoked is worth having in place and exercised
-- before anything depends on it.
--
-- ---------------------------------------------------------------------------
-- ROTATION IS A NEW ROW, NOT A NEW HASH
-- ---------------------------------------------------------------------------
--
-- superseded_at + grace_expires_at on the OLD row, a fresh row for the new
-- secret. An integrator needs an overlap in which to deploy; an in-place hash
-- replacement forces a simultaneous cutover that will be done badly at 2 a.m.
-- A grace of zero is an immediate cutover and is the right choice for a
-- compromised key, so the window is caller-chosen rather than fixed.
--
-- The live-name index below excludes superseded rows, so the old row leaves it
-- as the new one enters and "rotate the booking key" stays unambiguous.

BEGIN;

CREATE TABLE IF NOT EXISTS api_credentials (
    id         BIGSERIAL PRIMARY KEY,
    public_id  UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,

    -- Operator-chosen, shown in the console and in the audit trail. Not a
    -- secret and not an identifier the API accepts.
    name VARCHAR(80) NOT NULL,

    -- live | test. Part of the credential string itself, so a staging key
    -- presented to production is refused by shape before any lookup happens.
    environment VARCHAR(8) NOT NULL DEFAULT 'live',

    -- SHA-256 of the full presented string, hex. Not bcrypt: 256 bits from
    -- crypto/rand is not open to dictionary attack and does not need a slow KDF,
    -- and this is verified on every request. Same decision as sites and devices.
    key_hash   CHAR(64) NOT NULL,
    -- "atp_live_7f3c2a91" -- class, environment and eight hex characters. For
    -- the console list and for support conversations. Not sufficient to
    -- authenticate.
    key_prefix VARCHAR(20) NOT NULL,

    -- The closed set from the specification. A CHECK rather than a join table:
    -- adding a scope is a code change anyway, so making it a migration is
    -- honest, and it keeps the authentication path to a single row read.
    --
    -- IMPLICATION IS EXPANDED AT ISSUE TIME. members:write stores members:read
    -- alongside it, so a permission check is a set membership test with no
    -- inference in it, and the console shows the customer exactly what the key
    -- can do.
    scopes TEXT[] NOT NULL,

    -- Provenance, denormalised the way audit_events already denormalises an
    -- actor: the operator who issued this may later be deleted, and the answer
    -- to "who authorised this integration" has to survive that.
    created_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_by_email VARCHAR(255),

    -- NULL means never. The application defaults it to a year out at issue, so
    -- a non-expiring credential is a deliberate choice rather than the path of
    -- least resistance.
    expires_at TIMESTAMPTZ,

    -- Written asynchronously and coalesced, so a read-only API does not double
    -- its write load to record that it was used. Never load-bearing: a failure
    -- to update these must not fail a request.
    last_used_at TIMESTAMPTZ,
    last_used_ip VARCHAR(45),

    -- Rotation. superseded_by points at the row that replaced this one, so the
    -- console can show a chain rather than two unrelated credentials.
    superseded_at    TIMESTAMPTZ,
    superseded_by    BIGINT REFERENCES api_credentials(id) ON DELETE SET NULL,
    grace_expires_at TIMESTAMPTZ,

    -- Revocation is irreversible and the row is kept, because the audit trail
    -- references it.
    revoked_at     TIMESTAMPTZ,
    revoked_by     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revoked_reason VARCHAR(160),

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT api_credentials_key_hash_check
        CHECK (key_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT api_credentials_key_prefix_check
        CHECK (key_prefix ~ '^atp_(live|test)_[0-9a-f]{8}$'),
    CONSTRAINT api_credentials_environment_check
        CHECK (environment IN ('live', 'test')),
    CONSTRAINT api_credentials_name_check
        CHECK (length(btrim(name)) > 0),

    -- The closed scope set. A scope this build does not know cannot be stored,
    -- so a permission check never has to decide what to do with an unknown one.
    CONSTRAINT api_credentials_scopes_check CHECK (
        scopes <@ ARRAY[
            'members:read',
            'members:write',
            'sites:read',
            'terminals:read',
            'events:read',
            'access:read',
            'webhooks:manage'
        ]::text[]
        AND array_length(scopes, 1) >= 1
    ),

    -- Both halves of a rotation, or neither. A superseded row with no window
    -- would authenticate forever; a window with nothing superseded is noise.
    CONSTRAINT api_credentials_rotation_check
        CHECK ((superseded_at IS NULL) = (grace_expires_at IS NULL)),

    -- Matching credentials_revocation_check: the reason is part of the record,
    -- not an optional extra somebody forgets under pressure.
    CONSTRAINT api_credentials_revocation_check
        CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL)),

    CONSTRAINT api_credentials_expiry_check
        CHECK (expires_at IS NULL OR expires_at > created_at)
);

-- Optional site restriction. NO ROWS MEANS EVERY SITE IN THE COMPANY, now and
-- in future -- the same rule user_site_grants already uses for operators, so
-- adding a site does not require revisiting every credential. Once a credential
-- has any row it is scoped to exactly those sites.
CREATE TABLE IF NOT EXISTS api_credential_sites (
    credential_id BIGINT NOT NULL REFERENCES api_credentials(id) ON DELETE CASCADE,
    site_id       BIGINT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (credential_id, site_id)
);

-- The authentication lookup: one indexed equality on the hash. No prefix scan
-- and no per-row comparison, so there is no timing channel to defend.
CREATE UNIQUE INDEX IF NOT EXISTS api_credentials_key_hash_key
    ON api_credentials(key_hash);

CREATE UNIQUE INDEX IF NOT EXISTS api_credentials_public_id_key
    ON api_credentials(public_id);

-- The console list, and the per-company cap the issue path counts against.
CREATE INDEX IF NOT EXISTS idx_api_credentials_company
    ON api_credentials(company_id)
    WHERE revoked_at IS NULL;

-- ONE LIVE CREDENTIAL PER NAME. A superseded row has left this index, so a
-- rotation can insert the replacement under the same name inside one
-- transaction. Case-insensitive, because "Booking sync" and "booking sync" are
-- the same credential to the person reading the list.
CREATE UNIQUE INDEX IF NOT EXISTS api_credentials_company_name_live
    ON api_credentials(company_id, lower(name))
    WHERE revoked_at IS NULL AND superseded_at IS NULL;

-- The expiry-warning sweep. Partial, because a credential with no expiry and a
-- revoked one are both uninteresting to it.
CREATE INDEX IF NOT EXISTS idx_api_credentials_expiring
    ON api_credentials(expires_at)
    WHERE revoked_at IS NULL AND expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_api_credential_sites_site
    ON api_credential_sites(site_id);

COMMENT ON TABLE api_credentials IS
    'Third-party integration credentials (atp_). A separate class from the site '
    'provisioning key and the per-device key; scoped, expiring and revocable. '
    'Managed from the console by an ADMIN; no public route consumes one yet.';

COMMIT;
