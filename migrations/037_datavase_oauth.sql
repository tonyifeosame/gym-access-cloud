-- 037_datavase_oauth.sql
--
-- OAuth 2.0 authorization-code + PKCE, and the site fields a self-serve
-- integration needs to create one.
--
-- ---------------------------------------------------------------------------
-- WHY AN AUTHORIZATION SERVER AT ALL, WHEN 030 ALREADY ISSUES CREDENTIALS
-- ---------------------------------------------------------------------------
--
-- An integration credential (atp_) is minted by an ADMIN inside the console and
-- pasted into somebody else's system. That is the right shape for a customer's
-- own back-office job and the wrong shape for a product a customer connects
-- from the OTHER side: Datavase cannot ask every gym to open a console, find a
-- settings page and copy a secret into a form, and it must never hold a
-- credential a customer could not withdraw without our help.
--
-- So this migration adds the second way a bearer token comes to exist: the
-- customer signs in to AccessLink once, sees exactly what is being asked for,
-- and allows it. What Datavase ends up holding is a SHORT-LIVED token plus a
-- rotating refresh token, both revocable, both narrowed to scopes the consenting
-- operator was themselves entitled to.
--
-- NOTHING ABOUT THE atp_ CLASS CHANGES. The two classes share the scope
-- registry, the resource routes and the tenant context, and nothing else: a
-- credential row cannot mint a token, and a token cannot become a credential.
--
-- ---------------------------------------------------------------------------
-- NO DYNAMIC CLIENT REGISTRATION
-- ---------------------------------------------------------------------------
--
-- oauth_clients is written by the DEPLOYMENT, from configuration, and by no
-- request. A registration endpoint would let anybody who can reach the server
-- create a client with a redirect URI of their choosing, which is the whole of
-- the defence an authorization-code flow has. One row, configured, allow-listed.
--
-- ---------------------------------------------------------------------------
-- EVERY SECRET IS STORED AS SHA-256, AS EVERY OTHER CREDENTIAL CLASS IS
-- ---------------------------------------------------------------------------
--
-- Codes, access tokens, refresh tokens and the client secret are 256 bits from
-- crypto/rand and are stored hashed, hex. The reasoning migration 030 already
-- records applies unchanged: random of that size is not open to dictionary
-- attack, does not need a slow KDF, and has to be verifiable on every request
-- without burning CPU. The consequence is the one that matters here -- a dump of
-- this database contains no bearer token anybody can present.

BEGIN;

-- ---------------------------------------------------------------------------
-- Sites: country
-- ---------------------------------------------------------------------------
--
-- NULLABLE, and every existing row keeps NULL. A site created before this
-- migration has no country and nobody knows what it should be; inventing one
-- from the timezone would be a guess written into a customer's record. The
-- public write path REQUIRES it on create, which is where the value can
-- actually be asked for.
--
-- CHAR(2) holding ISO 3166-1 alpha-2, uppercase. The CHECK is the SHAPE only;
-- membership of the assigned set is enforced in Go (models/country.go), for the
-- same reason the scope CHECK is a list and this one is not: a scope set is
-- seven entries that change with a code change, and the alpha-2 set is 249
-- entries maintained by ISO. A 249-element CHECK would have to be migrated
-- every time a country is assigned or withdrawn, and a database that refuses a
-- newly assigned code is an outage nobody can fix without DDL.
ALTER TABLE sites ADD COLUMN IF NOT EXISTS country CHAR(2);

ALTER TABLE sites DROP CONSTRAINT IF EXISTS sites_country_check;
ALTER TABLE sites ADD CONSTRAINT sites_country_check
    CHECK (country IS NULL OR country ~ '^[A-Z]{2}$');

-- ---------------------------------------------------------------------------
-- The scope set gains sites:write
-- ---------------------------------------------------------------------------
--
-- Widened, not replaced, exactly as 036 widened the rate-limit class check. The
-- registry in models/scopes.go is the other half of this: a scope that is in one
-- and not the other is a scope that can be granted and not stored, or stored and
-- not understood.
ALTER TABLE api_credentials DROP CONSTRAINT IF EXISTS api_credentials_scopes_check;
ALTER TABLE api_credentials ADD CONSTRAINT api_credentials_scopes_check CHECK (
    scopes <@ ARRAY[
        'members:read',
        'members:write',
        'sites:read',
        'sites:write',
        'terminals:read',
        'events:read',
        'access:read',
        'webhooks:manage'
    ]::text[]
    AND array_length(scopes, 1) >= 1
);

-- The OAuth endpoints' own rate-limit class. Address-keyed, its own allowance:
-- a client hammering the token endpoint must not exhaust the allowance an
-- operator needs to sign in, which is the same argument the claim class already
-- makes against sharing the login one.
ALTER TABLE api_rate_buckets DROP CONSTRAINT IF EXISTS api_rate_buckets_class_check;
ALTER TABLE api_rate_buckets ADD CONSTRAINT api_rate_buckets_class_check
    CHECK (class IN ('read', 'search', 'write', 'webhook_admin',
                     'auth_failure',
                     'login', 'claim', 'platform_login', 'adopt',
                     'assistant', 'assistant_ok',
                     'oauth'));

-- ---------------------------------------------------------------------------
-- Clients
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS oauth_clients (
    id        BIGSERIAL PRIMARY KEY,

    -- The identifier the client sends. Not a secret and not a UUID: it appears
    -- in an authorization URL a human may look at, and "datavase" is readable
    -- where a UUID is noise.
    client_id VARCHAR(64) NOT NULL,

    -- Shown on the consent screen: "Datavase would like to ...". This is the
    -- name a customer decides about, so it is stored rather than derived from
    -- the client_id.
    name      VARCHAR(80) NOT NULL,

    -- SHA-256 of the client secret, or NULL for a public client. A public
    -- client is permitted BECAUSE PKCE is mandatory here: the code is useless
    -- without the verifier, which never leaves the client. A confidential
    -- client additionally proves possession at the token endpoint.
    secret_hash CHAR(64),

    -- The allow-list. Compared EXACTLY -- no prefix match, no wildcard, no
    -- "same host is close enough". A redirect URI that is matched loosely is
    -- an open redirector with an authorization code attached to it.
    redirect_uris TEXT[] NOT NULL,

    -- The most this client may ever be granted. The consent screen offers the
    -- intersection of this, what the request asks for, and what the signed-in
    -- operator is themselves entitled to grant.
    --
    -- THE CHECK BELOW IS THE REGISTRY'S FULL SET, NOT THE PHASE'S. Which scopes
    -- may be put behind a consent screen at all is a POLICY that moves with the
    -- work -- today sites only, because a grant cannot key an idempotency
    -- record -- and it is enforced in Go (models.OAuthGrantableScopes) so that
    -- widening it is a code change rather than a migration. What the database
    -- refuses is the thing that is genuinely structural: a scope this build
    -- does not know at all.
    scopes TEXT[] NOT NULL,

    active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT oauth_clients_client_id_check
        CHECK (client_id ~ '^[a-z0-9][a-z0-9_-]{2,63}$'),
    CONSTRAINT oauth_clients_secret_hash_check
        CHECK (secret_hash IS NULL OR secret_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT oauth_clients_name_check
        CHECK (length(btrim(name)) > 0),
    -- A client with no redirect URI cannot complete a flow, and a client with
    -- no scope cannot be granted anything. Both are configuration mistakes that
    -- would otherwise surface as a confusing refusal mid-flow.
    CONSTRAINT oauth_clients_redirect_uris_check
        CHECK (array_length(redirect_uris, 1) >= 1),
    CONSTRAINT oauth_clients_scopes_check CHECK (
        scopes <@ ARRAY[
            'members:read',
            'members:write',
            'sites:read',
            'sites:write',
            'terminals:read',
            'events:read',
            'access:read',
            'webhooks:manage'
        ]::text[]
        AND array_length(scopes, 1) >= 1
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_clients_client_id_key
    ON oauth_clients(client_id);

-- ---------------------------------------------------------------------------
-- Authorization codes
-- ---------------------------------------------------------------------------
--
-- ONE ROW PER CONSENT, consumed exactly once. Every value the token endpoint
-- has to check back against is on this row -- the client, the redirect URI, the
-- PKCE challenge, the scopes, the owner and their company -- so an exchange is
-- one indexed read and a set of equality tests, and there is nothing the client
-- can restate at exchange time that is trusted over what was bound at consent.
--
-- WHY consumed_at RATHER THAN A DELETE. A replayed code must be DETECTABLE. A
-- deleted row is indistinguishable from a code that never existed, and RFC 6749
-- section 4.1.2 asks an authorization server that sees a code used twice to
-- revoke what it already issued -- which needs to know what that was, hence
-- issued_family below.
CREATE TABLE IF NOT EXISTS oauth_authorization_codes (
    id        BIGSERIAL PRIMARY KEY,
    code_hash CHAR(64) NOT NULL,

    client_id  BIGINT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    -- The operator who consented. ON DELETE CASCADE: an account that is gone
    -- cannot have an outstanding consent, and a code that outlived its owner
    -- would be a grant nobody is accountable for.
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Bound at consent and compared at exchange. RFC 6749 section 4.1.3
    -- requires it, and the reason is concrete: without it a code intercepted
    -- from one redirect can be exchanged as though it came from another.
    redirect_uri TEXT NOT NULL,

    scopes TEXT[] NOT NULL,

    -- PKCE (RFC 7636). S256 only: the `plain` method puts the verifier in the
    -- authorization request, which is the exact place the attacker this
    -- mechanism defends against is already looking.
    code_challenge        VARCHAR(128) NOT NULL,
    code_challenge_method VARCHAR(8) NOT NULL,

    -- The refresh-token family this code produced, set when it is consumed. A
    -- replay of the code revokes that family; see oauth_refresh_tokens.
    issued_family UUID,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,

    CONSTRAINT oauth_authorization_codes_hash_check
        CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT oauth_authorization_codes_method_check
        CHECK (code_challenge_method = 'S256'),
    -- 43 characters is the base64url length of a SHA-256 digest, unpadded.
    CONSTRAINT oauth_authorization_codes_challenge_check
        CHECK (code_challenge ~ '^[A-Za-z0-9_-]{43}$'),
    CONSTRAINT oauth_authorization_codes_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT oauth_authorization_codes_scopes_check
        CHECK (array_length(scopes, 1) >= 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_authorization_codes_hash_key
    ON oauth_authorization_codes(code_hash);

-- The expiry sweep. Partial on the tail that is still worth deleting.
CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expiry
    ON oauth_authorization_codes(expires_at);

-- ---------------------------------------------------------------------------
-- Refresh tokens
-- ---------------------------------------------------------------------------
--
-- ROTATION WITH REUSE DETECTION, which is the only refresh scheme worth
-- shipping for a client that runs on somebody else's infrastructure.
--
--   * every refresh mints a NEW refresh token and marks the presented one
--     rotated. A stolen token is therefore usable at most once.
--   * family_id is constant along a rotation chain. Presenting a token that is
--     already rotated, or already revoked, means two parties hold tokens from
--     one chain -- one of them is the thief and the server cannot tell which --
--     so the WHOLE FAMILY is revoked, including the access tokens issued under
--     it, and both parties are forced back through consent.
--
-- That is the RFC 6819 section 5.2.2.3 recommendation, and it is the behaviour
-- that makes a leaked refresh token a detectable event rather than a permanent
-- one.
CREATE TABLE IF NOT EXISTS oauth_refresh_tokens (
    id        BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),

    token_hash CHAR(64) NOT NULL,

    client_id  BIGINT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- The rotation chain. Constant from the first token minted out of an
    -- authorization code to the last one rotated out of it.
    family_id UUID NOT NULL,

    scopes TEXT[] NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,

    -- Set when this token was exchanged. A second presentation of a rotated
    -- token is the reuse signal.
    rotated_at  TIMESTAMPTZ,
    replaced_by BIGINT REFERENCES oauth_refresh_tokens(id) ON DELETE SET NULL,

    revoked_at     TIMESTAMPTZ,
    -- Kept short and from a closed set in Go: CLIENT_REVOKED, REUSE_DETECTED,
    -- CODE_REPLAYED, OWNER_REVOKED. Not free text -- this column is read during
    -- an incident.
    revoked_reason VARCHAR(40),

    CONSTRAINT oauth_refresh_tokens_hash_check
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT oauth_refresh_tokens_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT oauth_refresh_tokens_revocation_check
        CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL)),
    CONSTRAINT oauth_refresh_tokens_rotation_check
        CHECK (replaced_by IS NULL OR rotated_at IS NOT NULL),
    CONSTRAINT oauth_refresh_tokens_scopes_check
        CHECK (array_length(scopes, 1) >= 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_refresh_tokens_hash_key
    ON oauth_refresh_tokens(token_hash);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_refresh_tokens_public_id_key
    ON oauth_refresh_tokens(public_id);

-- The family revocation, which is the statement that runs when reuse is
-- detected. It has to be fast: it happens while a request is waiting.
CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_family
    ON oauth_refresh_tokens(family_id);

CREATE INDEX IF NOT EXISTS idx_oauth_refresh_tokens_expiry
    ON oauth_refresh_tokens(expires_at);

-- ---------------------------------------------------------------------------
-- Access tokens
-- ---------------------------------------------------------------------------
--
-- OPAQUE AND LOOKED UP, not a self-describing JWT. The trade is one indexed
-- read per request against the ability to revoke in the same instant a customer
-- asks -- and revocation is the whole reason a customer will agree to connect a
-- third party to their door system at all. The same decision migration 030 made
-- for atp_, for the same reason, and it keeps ONE authentication path in the
-- resource middleware rather than two.
--
-- SHORT-LIVED regardless. The lifetime is minutes, so the window in which a
-- leaked token works is bounded even before anybody notices.
--
-- THE SITE RESTRICTION IS NOT STORED HERE, deliberately. It is computed at
-- authentication time from the consenting operator's CURRENT site grants, so
-- narrowing an operator's access narrows every token they granted, immediately
-- and without anybody having to remember that tokens exist.
CREATE TABLE IF NOT EXISTS oauth_access_tokens (
    id        BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),

    token_hash CHAR(64) NOT NULL,

    client_id  BIGINT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- The refresh family this token was issued under, so revoking a family
    -- reaches the access tokens as well as the refresh tokens. NULL is
    -- possible in principle (a grant issued without a refresh token) and is
    -- simply a token that only its own expiry or an explicit revocation ends.
    family_id UUID,

    -- live | test, matching the credential class in 030: a token minted by a
    -- staging deployment is refused by a live one on shape, before any lookup.
    environment VARCHAR(8) NOT NULL DEFAULT 'live',

    scopes TEXT[] NOT NULL,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_reason VARCHAR(40),

    CONSTRAINT oauth_access_tokens_hash_check
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT oauth_access_tokens_environment_check
        CHECK (environment IN ('live', 'test')),
    CONSTRAINT oauth_access_tokens_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT oauth_access_tokens_revocation_check
        CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL)),
    CONSTRAINT oauth_access_tokens_scopes_check
        CHECK (array_length(scopes, 1) >= 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_access_tokens_hash_key
    ON oauth_access_tokens(token_hash);

CREATE UNIQUE INDEX IF NOT EXISTS oauth_access_tokens_public_id_key
    ON oauth_access_tokens(public_id);

CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_family
    ON oauth_access_tokens(family_id)
    WHERE family_id IS NOT NULL AND revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_expiry
    ON oauth_access_tokens(expires_at);

COMMENT ON TABLE oauth_clients IS
    'Configured OAuth clients. Written by the deployment from configuration; '
    'there is no registration endpoint, so a redirect URI can only be '
    'allow-listed by whoever operates the service.';
COMMENT ON TABLE oauth_authorization_codes IS
    'Single-use, short-lived authorization codes bound to a client, a redirect '
    'URI, a PKCE challenge and the operator who consented.';
COMMENT ON TABLE oauth_refresh_tokens IS
    'Rotating refresh tokens. A presented token that was already rotated or '
    'revoked revokes its whole family: two holders of one chain means one of '
    'them is a thief and the server cannot tell which.';
COMMENT ON TABLE oauth_access_tokens IS
    'Opaque, short-lived bearer tokens for the public API. Stored as SHA-256 '
    'so a database dump contains nothing presentable.';

COMMIT;
