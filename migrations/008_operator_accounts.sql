-- ---------------------------------------------------------------------------
-- 008: operator accounts, site grants and browser sessions
-- ---------------------------------------------------------------------------
--
-- Every credential this system has so far belongs to a MACHINE. The site API
-- key provisions terminals, the device key authenticates one terminal. Both are
-- high-entropy secrets generated server-side and handed to firmware.
--
-- A dashboard needs the other kind: a human, logging in with a password from a
-- browser. That cannot be bolted onto the site key. The site key is the
-- PROVISIONING SECRET -- whoever holds it can register a terminal and rotate any
-- device credential at that site (see handlers/devices.go) -- so putting it in a
-- browser, where it would sit in JavaScript reachable storage on a machine the
-- operator does not control, would hand the door hardware to anything that can
-- run script on that page. This migration creates the separate identity that
-- lets the site key stay where it belongs.
--
-- Three tables:
--
--   users             an operator account, owned by exactly one company
--   user_site_grants  which sites within that company the operator may act on
--   user_sessions     server-side browser sessions, one row per login
--
-- ONE COMPANY PER OPERATOR. Every function in database/ takes a companyID as
-- its first argument and every query filters on it; that single-tenant-per-call
-- contract is what makes the tenancy boundary checkable. A cross-company
-- operator would dissolve it everywhere for a case this product does not have.
-- Multi-SITE operators are the real requirement and are handled by
-- user_site_grants, which stays inside one company.
--
-- TWO HASHING SCHEMES, DELIBERATELY. users.password_hash is bcrypt;
-- user_sessions.token_hash is SHA-256. That asymmetry is not an oversight, it
-- is the whole point:
--
--   * A password is human-chosen and therefore low-entropy and dictionary
--     attackable. It needs a per-hash salt and a deliberately slow KDF. bcrypt
--     carries its own salt and cost factor inside the 60-character hash, so the
--     cost can be raised later by rehashing on next login -- no migration.
--
--   * A session token is 256 bits from crypto/rand. It is not guessable and
--     needs no KDF; what it needs is to be verifiable on EVERY dashboard
--     request without burning CPU. This is the same reasoning, and the same
--     storage shape, as devices.api_key_hash in 005_device_identity.sql -- which
--     documents the matching asymmetry against the still-plaintext
--     sites.api_key.
--
-- Neither the session token nor the password is ever stored recoverably. A lost
-- password is reset by an operator with rights over the account; a lost session
-- is simply a new login.
--
-- WHAT THIS MIGRATION DOES NOT DO. It adds no seed rows. A database built from
-- migrations/ has no operator credentials at all, matching the rule 001 adopted
-- after shipping publicly-known API keys: the first account is created
-- deliberately, from BOOTSTRAP_OPERATOR_* at startup, and only into an empty
-- users table.
--
-- It also grants nothing. Roles here are a stored label; the matrix that says
-- what each role may call is enforced in Go, where the routes are. Putting an
-- authorization engine in the schema before there is a single authenticated
-- request to authorize would be building the second thing first.

BEGIN;

-- ---------------------------------------------------------------------------
-- users: an operator account
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id                  BIGSERIAL PRIMARY KEY,
    public_id           UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id          BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    email               VARCHAR(255) NOT NULL,
    full_name           VARCHAR(150) NOT NULL,
    -- bcrypt output is exactly 60 characters, so CHAR(60) never pads.
    password_hash       CHAR(60) NOT NULL,
    role                VARCHAR(20) NOT NULL DEFAULT 'VIEWER',
    active              BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at       TIMESTAMP,
    password_changed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- Brute-force bookkeeping. Lockout is time-bounded and self-healing, so it
    -- can slow an attacker without permanently locking out the only owner.
    failed_login_count  SMALLINT NOT NULL DEFAULT 0,
    locked_until        TIMESTAMP,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at          TIMESTAMP,

    CONSTRAINT users_role_check
        CHECK (role IN ('OWNER', 'ADMIN', 'MANAGER', 'VIEWER')),

    -- Addresses are stored lowercased so the unique index below is genuinely
    -- case-insensitive. Enforced here rather than trusted from the application:
    -- two accounts differing only in case would each look unique to the index
    -- and ambiguous to a login form.
    CONSTRAINT users_email_check
        CHECK (email = lower(email) AND position('@' in email) > 1),

    -- Rejects anything that is not a bcrypt hash: a plaintext password written
    -- here by a future code path fails loudly instead of being stored in the
    -- clear and compared successfully.
    CONSTRAINT users_password_hash_check
        CHECK (password_hash ~ '^\$2[aby]\$\d{2}\$'),

    CONSTRAINT users_full_name_check
        CHECK (length(btrim(full_name)) > 0),

    CONSTRAINT users_failed_login_count_check
        CHECK (failed_login_count >= 0)
);

-- Email is unique GLOBALLY, not per company. The login form is email plus
-- password with no tenant selector, so an address has to resolve to exactly one
-- account for authentication to be decidable. Partial, so soft-deleting an
-- account frees its address for reuse.
CREATE UNIQUE INDEX IF NOT EXISTS users_email_key
    ON users(email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS users_public_id_key ON users(public_id);
CREATE INDEX IF NOT EXISTS idx_users_company_id
    ON users(company_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- user_site_grants: which sites an operator may act on
-- ---------------------------------------------------------------------------
--
-- An empty grant set means "every site in the operator's company", which is
-- what OWNER and ADMIN always have. Absence is the default rather than a
-- wildcard row so that adding a site does not require touching every account.
--
-- No updated_at and no soft delete, unlike the entity tables: this is an
-- association, and revoking a grant is a delete. Keeping a revoked grant as a
-- soft-deleted row would mean every authorization check had to remember to
-- filter it, which is exactly the kind of check that gets forgotten once.
--
-- The FK to sites is ON DELETE CASCADE while sites themselves are soft-deleted,
-- so in practice a grant outlives its site's soft delete. Authorization joins
-- sites and filters deleted_at IS NULL, so a grant to a deleted site conveys
-- nothing.
CREATE TABLE IF NOT EXISTS user_site_grants (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    site_id    BIGINT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, site_id)
);

-- "Who can act on this site", for the operator admin screen. The reverse
-- direction is served by the primary key.
CREATE INDEX IF NOT EXISTS idx_user_site_grants_site_id ON user_site_grants(site_id);

-- ---------------------------------------------------------------------------
-- user_sessions: one row per browser login
-- ---------------------------------------------------------------------------
--
-- Server-side sessions rather than a self-contained token, because revocation
-- has to be immediate. Logging out, disabling an account or changing a role
-- must take effect on the next request -- a signed token stays valid until it
-- expires no matter what the database says. This system already has one
-- credential class it cannot revoke without re-keying a whole site; repeating
-- that for the humans would be worse.
--
-- TWO EXPIRIES. idle_expires_at slides forward as the session is used, so an
-- operator working all day is not logged out mid-task. absolute_expires_at
-- never moves, so a session cannot be kept alive indefinitely by a browser tab
-- polling in the background. The CHECK below binds the first to the second:
-- the sliding update must clamp, and here that is enforced rather than assumed.
--
-- csrf_token_hash is a per-session synchronizer token. It is delivered in the
-- login and /me response BODIES, not as a second cookie: in a split-origin
-- deployment (app.accesslink.store calling api.accesslink.store) the dashboard's
-- JavaScript cannot read a cookie scoped to the API host, so the double-submit
-- cookie pattern would silently fail there. Hashed for the same reason the
-- session token is -- neither needs to be recoverable.
--
-- No updated_at trigger: last_used_at is this table's updated_at, and it is
-- written on a throttle rather than on every request.
CREATE TABLE IF NOT EXISTS user_sessions (
    id                  BIGSERIAL PRIMARY KEY,
    public_id           UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash          CHAR(64) NOT NULL,
    csrf_token_hash     CHAR(64) NOT NULL,
    issued_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    idle_expires_at     TIMESTAMP NOT NULL,
    absolute_expires_at TIMESTAMP NOT NULL,
    revoked_at          TIMESTAMP,
    ip_address          VARCHAR(45),
    user_agent          VARCHAR(255),

    -- 64 lowercase hex characters, the same storage form and the same check as
    -- devices.api_key_hash.
    CONSTRAINT user_sessions_token_hash_check
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT user_sessions_csrf_token_hash_check
        CHECK (csrf_token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT user_sessions_expiry_check
        CHECK (absolute_expires_at > issued_at),
    CONSTRAINT user_sessions_idle_bound_check
        CHECK (idle_expires_at <= absolute_expires_at)
);

-- The hot path: resolve a session from the hash of the presented cookie. One
-- index probe per authenticated request, so it is unique and complete.
CREATE UNIQUE INDEX IF NOT EXISTS user_sessions_token_hash_key
    ON user_sessions(token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS user_sessions_public_id_key
    ON user_sessions(public_id);

-- "Revoke everything for this user", on logout-all, password change, or when an
-- account is disabled.
CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions(user_id);

-- The maintenance sweep that purges rows past their absolute expiry.
CREATE INDEX IF NOT EXISTS idx_user_sessions_absolute_expires_at
    ON user_sessions(absolute_expires_at);

-- ---------------------------------------------------------------------------
-- updated_at trigger (update_updated_at_column() is defined in 001)
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_users_updated_at ON users;
CREATE TRIGGER update_users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
