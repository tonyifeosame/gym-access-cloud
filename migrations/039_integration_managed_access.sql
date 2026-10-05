-- 039: integration-managed access.
--
-- An integration that creates members through the public API can also give
-- them the standard door access its credential covers -- and nothing more:
--
--   access:write                 a new scope; issuing it needs an ADMIN. It is
--                                the company's consent that this integration may
--                                give access to the members it manages.
--   api_credentials.lineage_id   the first credential of a rotation chain. A
--                                rotated key inherits it, so ownership survives
--                                rotation; a new key starts its own lineage.
--   people.managed_by_lineage_id the integration that created the person through
--                                the public API. NULL = unmanaged: no integration
--                                may give it access.
--   permissions.granted_by_lineage_id
--                                marks the rules an integration created, so its
--                                removal endpoint can only ever remove those.
--
-- NOTHING ELSE CHANGES. companies.default_person_access is not touched, no
-- permission row is written here, and a revoked credential keeps its rules.
--
-- ---------------------------------------------------------------------------
-- THE SCOPE CHECK, AND THE MERGE RULE IT DEPENDS ON
-- ---------------------------------------------------------------------------
--
-- api_credentials_scopes_check is a closed list rebuilt by 030, by 038 on this
-- branch's parent, and by 037 on feat/datavase-oauth-sites. Migrations run once
-- each through deploy/migrate.sh's ledger, in filename order among the PENDING
-- files -- so the rebuild APPLIED LAST wins, whatever its number. A higher
-- number here would not protect anything.
--
-- This rebuild holds exactly this branch's registry (models.AllScopes()): 038's
-- set plus access:write. It deliberately does NOT add sites:write, which is not
-- a scope on this branch. TestScopeCheckConstraintMatchesTheRegistry reads the
-- live constraint and fails if it differs from AllScopes(), so any merge order
-- that leaves them apart is caught in CI, not at a deploy.
--
-- Merge rule:
--   preferred: PR #26 (038) -> this branch (039) -> OAuth. OAuth's migration is
--   then rebased to the next free number and its list gains enrollments:write
--   and access:write (and its oauth_clients_scopes_check is OAuth's decision).
--   If OAuth merges first: rebase PR #26 and this branch so 038's and 039's
--   lists include sites:write.
--   Either way the CI guard must pass after the final order.

BEGIN;

ALTER TABLE api_credentials DROP CONSTRAINT IF EXISTS api_credentials_scopes_check;
ALTER TABLE api_credentials ADD CONSTRAINT api_credentials_scopes_check CHECK (
    scopes <@ ARRAY[
        'members:read',
        'members:write',
        'sites:read',
        'terminals:read',
        'events:read',
        'access:read',
        'access:write',
        'webhooks:manage',
        'enrollments:write'
    ]::text[]
    AND array_length(scopes, 1) >= 1
);

-- Lineage. Roots are credentials no other credential was superseded by; each
-- chain inherits its root by following superseded_by forward.
ALTER TABLE api_credentials ADD COLUMN IF NOT EXISTS lineage_id BIGINT REFERENCES api_credentials(id);
WITH RECURSIVE chain(id, root) AS (
    SELECT c.id, c.id FROM api_credentials c
     WHERE NOT EXISTS (SELECT 1 FROM api_credentials p WHERE p.superseded_by = c.id)
    UNION ALL
    SELECT c.superseded_by, chain.root FROM chain
      JOIN api_credentials c ON c.id = chain.id
     WHERE c.superseded_by IS NOT NULL
)
UPDATE api_credentials c SET lineage_id = chain.root FROM chain
 WHERE c.id = chain.id AND c.lineage_id IS NULL;

-- A credential issued without a lineage starts its own. Rotation passes the
-- predecessor's lineage explicitly (database.RotateAPICredential).
CREATE OR REPLACE FUNCTION api_credentials_default_lineage() RETURNS trigger AS $$
BEGIN
    NEW.lineage_id := COALESCE(NEW.lineage_id, NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS api_credentials_default_lineage ON api_credentials;
CREATE TRIGGER api_credentials_default_lineage BEFORE INSERT ON api_credentials
    FOR EACH ROW EXECUTE FUNCTION api_credentials_default_lineage();
ALTER TABLE api_credentials ALTER COLUMN lineage_id SET NOT NULL;

ALTER TABLE people ADD COLUMN IF NOT EXISTS managed_by_lineage_id BIGINT REFERENCES api_credentials(id);
CREATE INDEX IF NOT EXISTS idx_people_managed_by ON people(managed_by_lineage_id) WHERE managed_by_lineage_id IS NOT NULL;

ALTER TABLE permissions ADD COLUMN IF NOT EXISTS granted_by_lineage_id BIGINT REFERENCES api_credentials(id);
CREATE INDEX IF NOT EXISTS idx_permissions_granted_by ON permissions(person_id, granted_by_lineage_id)
    WHERE granted_by_lineage_id IS NOT NULL AND deleted_at IS NULL;

-- Adopting people created before provenance existed: ONLY from the immutable
-- audit trail, and only when every link is unambiguous. A person is adopted
-- when all of these hold, and is left unmanaged otherwise:
--   * exactly one PERSON_CREATED audit event names them, in their own company;
--   * that event's actor is INTEGRATION and it records via = public_api;
--   * its key prefix (actor_email, repeated in changes) matches exactly one
--     credential of that company;
--   * the person is not deleted and has no provenance yet.
-- A function rather than a one-off statement so the rule is testable; the
-- migration runs it once, and it is safe to run again (it only fills NULLs).
CREATE OR REPLACE FUNCTION adopt_integration_members_from_audit() RETURNS integer AS $$
DECLARE adopted integer;
BEGIN
    WITH created AS (
        SELECT a.company_id, a.target_public_id,
               count(*) OVER (PARTITION BY a.target_public_id) AS events,
               a.actor_role, a.actor_email, a.changes
          FROM audit_events a
         WHERE a.action = 'PERSON_CREATED' AND a.target_public_id IS NOT NULL
    ), candidates AS (
        SELECT p.id AS person_id, c.id AS credential_id, c.lineage_id,
               count(*) OVER (PARTITION BY p.id) AS credentials
          FROM created e
          JOIN people p ON p.public_id = e.target_public_id AND p.company_id = e.company_id
          JOIN api_credentials c ON c.company_id = e.company_id AND c.key_prefix = e.actor_email
         WHERE e.events = 1
           AND e.actor_role = 'INTEGRATION'
           AND e.changes->>'via' = 'public_api'
           AND e.changes->>'credential_key_prefix' = e.actor_email
           AND p.deleted_at IS NULL
           AND p.managed_by_lineage_id IS NULL
    )
    UPDATE people p SET managed_by_lineage_id = candidates.lineage_id
      FROM candidates
     WHERE p.id = candidates.person_id AND candidates.credentials = 1;
    GET DIAGNOSTICS adopted = ROW_COUNT;
    RETURN adopted;
END;
$$ LANGUAGE plpgsql;

SELECT adopt_integration_members_from_audit();

COMMIT;
