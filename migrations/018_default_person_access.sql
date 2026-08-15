-- ---------------------------------------------------------------------------
-- 018: what a newly created person is allowed, by default
-- ---------------------------------------------------------------------------
--
-- 014_authorization_engine.sql established deny-by-default and back-filled a
-- company-scoped ALLOW for every EXISTING person, so that no deployed door
-- stopped working the day the engine shipped. It said the default "becomes
-- meaningful for people created after it".
--
-- That leaves a gap this migration closes. A deployed installation creating
-- people through the legacy /api/v1/members surface -- which the compatibility
-- policy keeps working for deployed tooling -- would find that those people
-- silently reach no terminal and open no door. It is the SAME lockout 014's
-- back-fill exists to prevent, just deferred until the next person is added,
-- and it would be discovered at a door rather than at an upgrade.
--
-- ---------------------------------------------------------------------------
-- THE DECISION, AND WHY IT IS A POLICY RATHER THAN A CONSTANT
-- ---------------------------------------------------------------------------
--
-- Two customers want opposite things and both are right:
--
--   A warehouse that adds a hundred seasonal staff a week does not want to
--   grant each one access as a separate act. Its roster IS its access list.
--
--   A secure facility wants a new person to be able to open nothing until
--   somebody deliberately says otherwise. That is the entire value of the
--   engine to them.
--
-- Neither is a sensible default for the other, so the platform does not pick.
-- It asks, and it records the answer per company.
--
-- MIGRATION DEFAULTS, WHICH DIFFER BY DESIGN:
--
--   Existing companies  -> COMPANY_ALLOW. Preserves exactly today's behaviour.
--                          Nothing an operator has already built changes
--                          underneath them.
--
--   New companies       -> NONE. Set by the platform administration API when it
--                          creates a tenant, not by this column's default, so
--                          the safe choice is what a new customer starts with
--                          and the permissive one is only ever a legacy
--                          carry-over that a company can see and change.
--
-- The column default below is COMPANY_ALLOW because that is what the ALTER has
-- to give existing rows. handlers/platform.go overrides it on create.

BEGIN;

ALTER TABLE companies
    ADD COLUMN IF NOT EXISTS default_person_access VARCHAR(20) NOT NULL
        DEFAULT 'COMPANY_ALLOW';

ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_default_access_check;
ALTER TABLE companies ADD CONSTRAINT companies_default_access_check
    CHECK (default_person_access IN (
        -- A new person is granted a company-scoped ALLOW at creation, exactly
        -- as 014's back-fill did for existing people. The rule is a REAL
        -- permission row, visible and removable in the console -- not a special
        -- case in the evaluator. The engine still denies by default; this only
        -- decides what gets written when somebody is added.
        'COMPANY_ALLOW',
        -- A new person is granted nothing and can open nothing until a rule
        -- says otherwise.
        'NONE'
    ));

COMMENT ON COLUMN companies.default_person_access IS
    'What permission is written when a person is created. COMPANY_ALLOW '
    'reproduces pre-014 behaviour and is what existing tenants were migrated '
    'to; NONE is deny-by-default and is what new tenants are created with. '
    'This does NOT change how the engine decides -- it decides what rule is '
    'written at person creation (018_default_person_access.sql).';

COMMIT;
