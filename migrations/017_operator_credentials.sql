-- ---------------------------------------------------------------------------
-- 017: operator credential handover -- invitations, forced change, reset
-- ---------------------------------------------------------------------------
--
-- PPL-02 and SEC-10, which are the same problem at two ends of an account's
-- life: THE PLATFORM HAD NO WAY TO GIVE SOMEBODY A CREDENTIAL, AND NO WAY FOR
-- THEM TO RECOVER ONE.
--
-- POST /console/operators required the CALLER to choose the new operator's
-- password and hand it over themselves. There was no invitation, no single-use
-- link, and no "must change at first sign-in" flag -- users.password_changed_at
-- existed and nothing read it as a policy.
--
-- The realistic consequence is not hypothetical: initial credentials travel by
-- chat or email in plain text, and typically stay unchanged. The administrator
-- who created the account knows the password indefinitely, and nothing records
-- that they do.
--
-- At the other end there was no reset at all. A sole OWNER who forgot their
-- password needed database access to recover, which is the same failure the
-- company-creation blocker had: an ordinary operation that only somebody with
-- SQL could perform.
--
-- ---------------------------------------------------------------------------
-- ONE TABLE FOR BOTH, AND WHY
-- ---------------------------------------------------------------------------
--
-- An invitation and a password reset are the same object: a single-use,
-- short-lived, high-entropy token that authorises setting a password for one
-- account without knowing the current one. They differ in what the account looks
-- like beforehand and in what the email says, not in the mechanism.
--
-- Two tables would mean two token stores, two expiry sweeps, two redemption
-- paths and two places for the single-use guarantee to be got wrong. `purpose`
-- distinguishes them where it matters -- an INVITE may only be redeemed by an
-- account that has never signed in, a RESET only by one that has.
--
-- STORED AS A HASH, like every other credential here. The plaintext exists in
-- the response that mints it and nowhere else, so a database read yields nothing
-- redeemable. Same reasoning as user_sessions.token_hash and devices.api_key_hash.

BEGIN;

-- ---------------------------------------------------------------------------
-- users: the policy flag that was missing
-- ---------------------------------------------------------------------------
--
-- Read on every authenticated request and returned in the session body, so the
-- console can route to a change-password screen rather than letting somebody
-- work all day on a credential their administrator chose for them.
--
-- NOT enforced by refusing requests. An operator who must change their password
-- can still read; what the console does is insist before they do anything else.
-- Refusing every request would leave an account that cannot even reach the
-- endpoint that fixes it.
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE;

-- Existing accounts are NOT retro-flagged. Forcing a password change on every
-- operator of every deployment because a column was added is a migration making
-- a policy decision that belongs to the customer.
COMMENT ON COLUMN users.must_change_password IS
    'Set when a password was chosen by somebody other than its owner -- an '
    'invitation, or an administrative reset. Cleared when the operator sets '
    'their own. The console insists on the change; the API does not refuse '
    'requests, because an account that cannot reach the endpoint that fixes it '
    'is worse.';

-- ---------------------------------------------------------------------------
-- user_credential_tokens
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_credential_tokens (
    id         BIGSERIAL PRIMARY KEY,
    public_id  UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- 64 lowercase hex, the storage shape every other token here uses.
    token_hash CHAR(64) NOT NULL,

    purpose    VARCHAR(16) NOT NULL,

    -- Who minted it, denormalised for the same reason audit_events denormalises
    -- its actor: the administrator's account may be gone by the time somebody
    -- asks who issued this.
    issued_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    issued_by_email   VARCHAR(255),

    expires_at TIMESTAMPTZ NOT NULL,

    -- SINGLE USE. Redemption stamps this, and the redeem statement matches on
    -- `redeemed_at IS NULL`, so two concurrent redemptions cannot both succeed.
    redeemed_at TIMESTAMPTZ,
    redeemed_ip VARCHAR(45),

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT user_credential_tokens_hash_check
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT user_credential_tokens_purpose_check
        CHECK (purpose IN ('INVITE', 'RESET')),
    CONSTRAINT user_credential_tokens_expiry_check
        CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS user_credential_tokens_hash_key
    ON user_credential_tokens(token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS user_credential_tokens_public_id_key
    ON user_credential_tokens(public_id);

-- "Does this operator have an invitation outstanding", for the console, and
-- "invalidate everything for this account" when a new one is issued.
CREATE INDEX IF NOT EXISTS idx_user_credential_tokens_user
    ON user_credential_tokens(user_id) WHERE redeemed_at IS NULL;

-- The expiry sweep.
CREATE INDEX IF NOT EXISTS idx_user_credential_tokens_expires_at
    ON user_credential_tokens(expires_at);

-- ---------------------------------------------------------------------------
-- Site grants and role changes (PPL-06)
-- ---------------------------------------------------------------------------
--
-- Grants are ignored for ADMIN and OWNER, which reach every site by role. The
-- audit found that SetUserRole did not clear them, so:
--
--   a MANAGER restricted to one site is promoted to ADMIN and correctly reaches
--   everything; months later they are moved back to MANAGER, and THE OLD
--   RESTRICTION SILENTLY REAPPLIES -- possibly naming a site that has since been
--   retired, and certainly nothing anyone involved remembers setting.
--
-- Not a security hole: the grant narrows rather than widens, and the server is
-- consistent throughout. It is a surprise, and the surprising direction is the
-- one that removes access unexpectedly.
--
-- FIXED BY RECORDING RATHER THAN BY DELETING. Clearing grants on promotion loses
-- information an administrator may want back; keeping them silently is what
-- caused the problem. The column below makes the state visible, and the console
-- says so on the role dialog. Demotion then re-applies grants the operator has
-- been told about rather than ones they have forgotten.
ALTER TABLE user_site_grants ADD COLUMN IF NOT EXISTS dormant_since TIMESTAMPTZ;

COMMENT ON COLUMN user_site_grants.dormant_since IS
    'Set when the operator was promoted to a role that ignores site grants. The '
    'grant is retained and stops applying; on demotion it applies again. Exists '
    'so that reapplication is visible rather than a surprise months later.';

-- ---------------------------------------------------------------------------
-- Session policy on privilege change
-- ---------------------------------------------------------------------------
--
-- An explicit policy, because the audit asked for one and because "it depends
-- what the code happens to do" is not an answer to give a customer.
--
--   Role change      -> every session for that operator is revoked. A session
--                       carries a role resolved at authentication time; leaving
--                       it alive means a demoted operator keeps the privileges
--                       they were demoted from until they happen to log out.
--   Grant change     -> sessions SURVIVE. Grants are resolved per request, from
--                       the database, on every site-scoped route -- so a
--                       narrowed operator is narrowed on their next request
--                       without being logged out mid-task.
--   Password change  -> every OTHER session is revoked, current one survives.
--                       Already implemented; recorded here so the three rules
--                       sit together.
--   Deactivation     -> every session revoked, in the same transaction.
--
-- The distinction between the first two is not arbitrary: role is cached on the
-- identity, grants are not.

COMMIT;
