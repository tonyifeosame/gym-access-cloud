-- 034_google_identity.sql
--
-- Sign in with Google for operator accounts.
--
-- ---------------------------------------------------------------------------
-- WHAT THIS ADDS, AND WHAT IT DELIBERATELY DOES NOT
-- ---------------------------------------------------------------------------
--
-- Two columns on `users`. A Google account is keyed on its SUBJECT -- the
-- stable identifier Google issues -- and never on its email address: a Google
-- user can change their address and keep their account, and an address can be
-- released and re-registered by somebody else. The email in the ID token is
-- used exactly once, to LINK an existing operator account on first sign-in,
-- and only when Google vouches for it (`email_verified`).
--
-- THERE IS NO SECOND ACCOUNT TABLE, no second session table and no second
-- cookie. A Google sign-in ends in the same `user_sessions` row that a
-- password sign-in does, built by the same function. This migration adds a
-- way to prove who somebody is; it does not add a way to be signed in.
--
-- ---------------------------------------------------------------------------
-- WHY THE INDEX IS PARTIAL ON deleted_at
-- ---------------------------------------------------------------------------
--
-- One Google account resolves to at most one LIVE operator. A soft-deleted
-- account keeps its subject so the record of who it was stays readable, and
-- the partial index frees that subject to be linked to a replacement account
-- -- the same shape `users_email_key` already has for addresses.

BEGIN;

ALTER TABLE users ADD COLUMN IF NOT EXISTS google_subject VARCHAR(255);
ALTER TABLE users ADD COLUMN IF NOT EXISTS google_linked_at TIMESTAMPTZ;

COMMENT ON COLUMN users.google_subject IS
    'The `sub` claim of the Google account linked to this operator, or NULL '
    'when none is. Set on first Google sign-in against a verified matching '
    'address; never set from an unverified one. The subject, not the email, '
    'is what a returning Google sign-in is matched on.';

COMMENT ON COLUMN users.google_linked_at IS
    'When google_subject was set. Written once, at linking.';

-- Google subjects are opaque numeric strings today; the check refuses an
-- empty string so that "" cannot be linked as if it were an identity.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_google_subject_check;
ALTER TABLE users ADD CONSTRAINT users_google_subject_check
    CHECK (google_subject IS NULL OR length(google_subject) > 0);

CREATE UNIQUE INDEX IF NOT EXISTS users_google_subject_key
    ON users(google_subject)
    WHERE google_subject IS NOT NULL AND deleted_at IS NULL;

COMMIT;
