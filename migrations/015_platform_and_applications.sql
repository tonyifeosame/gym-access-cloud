-- ---------------------------------------------------------------------------
-- 015: platform administration, and capabilities as data
-- ---------------------------------------------------------------------------
--
-- Two problems that share a cause: THE PLATFORM HAD NO WAY TO TALK ABOUT ITSELF.
-- Everything was scoped to a company, including the things that are not a
-- company's business -- who the customers are, and what the product can
-- actually do.
--
-- ---------------------------------------------------------------------------
-- PART 1: platform_admins -- an identity that is not a tenant
-- ---------------------------------------------------------------------------
--
-- The audit's first blocker: no API creates a company. Onboarding a second
-- customer required direct SQL against production.
--
-- The obvious fix -- let some operator role create companies -- is wrong, and
-- 008_operator_accounts.sql already explains why:
--
--     ONE COMPANY PER OPERATOR. Every function in database/ takes a companyID as
--     its first argument and every query filters on it; that single-tenant-per-
--     call contract is what makes the tenancy boundary checkable. A cross-company
--     operator would dissolve it everywhere.
--
-- That reasoning holds. `users.company_id` stays NOT NULL and every console
-- route stays company-scoped. What was missing is not a bigger operator, it is a
-- DIFFERENT CREDENTIAL CLASS -- and this codebase already has the pattern for
-- that, twice over: the site key and the operator session share a URL prefix and
-- nothing else, and neither can be presented where the other belongs.
--
-- So a platform administrator is a third class. Separate table, separate
-- sessions, separate cookie, separate routes under /api/v1/platform. It cannot
-- authenticate to a console route and an operator cannot authenticate to a
-- platform route, and the tenancy filter on every existing query is untouched
-- because nothing about the operator model changed.
--
-- WHAT A PLATFORM ADMIN MAY DO, AND WHAT IT DELIBERATELY MAY NOT.
--
--   May:  create a company, rename it, deactivate it, set its retention policy,
--         and create the FIRST operator inside it.
--   May NOT: read a company's people, credentials, events, terminals or site
--         keys. Running a customer's deployment is the customer's job, and a
--         support identity that can read every tenant's biometric roster is the
--         single most valuable credential on the installation.
--
-- That boundary is enforced by there being no such route, not by a role check.
-- The platform routes operate on `companies` and on operator bootstrap, and
-- there is nothing else for them to reach.
--
-- ---------------------------------------------------------------------------
-- PART 2: applications as rows, with a truthful maturity
-- ---------------------------------------------------------------------------
--
-- 009_applications.sql put the capability list in two CHECK constraints. The
-- audit found the consequence: adding a capability to a platform whose selling
-- point is configurability required a database migration in two places plus a Go
-- constant.
--
-- Worse, the model had no way to say whether a capability DID anything. A
-- company could enable ATTENDANCE, assign a terminal to it, see it in the
-- navigation, and receive nothing -- and no part of the system was in a position
-- to say so. `status` below is that missing statement, and it is set by the
-- PLATFORM, not by the customer.

BEGIN;

-- ---------------------------------------------------------------------------
-- companies: the fields a lifecycle needs
-- ---------------------------------------------------------------------------
ALTER TABLE companies ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) NOT NULL DEFAULT 'UTC';
ALTER TABLE companies ADD COLUMN IF NOT EXISTS deactivated_at TIMESTAMPTZ;
ALTER TABLE companies ADD COLUMN IF NOT EXISTS deactivated_reason VARCHAR(160);

-- Slugs travel in URLs and in the bootstrap environment variable, so the shape
-- is enforced rather than trusted from whoever creates one.
ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_slug_format_check;
ALTER TABLE companies ADD CONSTRAINT companies_slug_format_check
    CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$');

ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_name_check;
ALTER TABLE companies ADD CONSTRAINT companies_name_check
    CHECK (length(btrim(name)) > 0);

-- active and deactivated_at must agree, or "is this tenant switched off" has two
-- answers and the authentication path picks one of them.
--
-- MAINTAINED BY A TRIGGER, NOT ENFORCED BY A CHECK ALONE. A bare CHECK would
-- reject `UPDATE companies SET active = FALSE` -- which is what every existing
-- caller, fixture and operator runbook does -- and force each of them to
-- remember a second column. That is how an invariant becomes something callers
-- work around. The trigger derives the timestamp from the flag, so there is one
-- thing to set and the pair can never disagree.
CREATE OR REPLACE FUNCTION sync_company_deactivation()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.active THEN
        NEW.deactivated_at := NULL;
        NEW.deactivated_reason := NULL;
    ELSIF NEW.deactivated_at IS NULL THEN
        NEW.deactivated_at := CURRENT_TIMESTAMP;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS companies_deactivation_sync ON companies;
CREATE TRIGGER companies_deactivation_sync
    BEFORE INSERT OR UPDATE OF active, deactivated_at, deactivated_reason ON companies
    FOR EACH ROW EXECUTE FUNCTION sync_company_deactivation();

-- Existing rows predate the trigger.
UPDATE companies SET deactivated_at = COALESCE(deactivated_at, updated_at)
 WHERE NOT active AND deactivated_at IS NULL;
UPDATE companies SET deactivated_at = NULL, deactivated_reason = NULL
 WHERE active AND deactivated_at IS NOT NULL;

ALTER TABLE companies DROP CONSTRAINT IF EXISTS companies_deactivation_check;
ALTER TABLE companies ADD CONSTRAINT companies_deactivation_check
    CHECK (active = (deactivated_at IS NULL));

-- ---------------------------------------------------------------------------
-- platform_admins
-- ---------------------------------------------------------------------------
--
-- The same storage decisions as `users`, for the same reasons: bcrypt for a
-- human-chosen secret, a lowercased email so the unique index is genuinely
-- case-insensitive, and a time-bounded self-healing lockout.
CREATE TABLE IF NOT EXISTS platform_admins (
    id                  BIGSERIAL PRIMARY KEY,
    public_id           UUID NOT NULL DEFAULT gen_random_uuid(),
    email               VARCHAR(255) NOT NULL,
    full_name           VARCHAR(150) NOT NULL,
    password_hash       CHAR(60) NOT NULL,
    active              BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at       TIMESTAMPTZ,
    password_changed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    failed_login_count  SMALLINT NOT NULL DEFAULT 0,
    locked_until        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at          TIMESTAMPTZ,

    CONSTRAINT platform_admins_email_check
        CHECK (email = lower(email) AND position('@' in email) > 1),
    CONSTRAINT platform_admins_password_hash_check
        CHECK (password_hash ~ '^\$2[aby]\$\d{2}\$'),
    CONSTRAINT platform_admins_full_name_check
        CHECK (length(btrim(full_name)) > 0),
    CONSTRAINT platform_admins_failed_login_count_check
        CHECK (failed_login_count >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS platform_admins_email_key
    ON platform_admins(email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS platform_admins_public_id_key
    ON platform_admins(public_id);

-- ---------------------------------------------------------------------------
-- platform_sessions
-- ---------------------------------------------------------------------------
--
-- A separate table rather than a nullable subject column on user_sessions.
-- Sharing the table would mean every session lookup returns a row that might be
-- either kind, and the check that distinguishes them would live in Go on the hot
-- authentication path -- exactly the kind of check that gets forgotten once and
-- lets a platform session authenticate a console route.
--
-- SHORTER LIFETIMES than an operator session, deliberately. This credential can
-- create tenants and mint their first owner; it should not survive a forgotten
-- laptop for a week.
CREATE TABLE IF NOT EXISTS platform_sessions (
    id                  BIGSERIAL PRIMARY KEY,
    public_id           UUID NOT NULL DEFAULT gen_random_uuid(),
    admin_id            BIGINT NOT NULL REFERENCES platform_admins(id) ON DELETE CASCADE,
    token_hash          CHAR(64) NOT NULL,
    csrf_token_hash     CHAR(64) NOT NULL,
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    idle_expires_at     TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    ip_address          VARCHAR(45),
    user_agent          VARCHAR(255),

    CONSTRAINT platform_sessions_token_hash_check
        CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT platform_sessions_csrf_token_hash_check
        CHECK (csrf_token_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT platform_sessions_expiry_check
        CHECK (absolute_expires_at > issued_at),
    CONSTRAINT platform_sessions_idle_bound_check
        CHECK (idle_expires_at <= absolute_expires_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS platform_sessions_token_hash_key
    ON platform_sessions(token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS platform_sessions_public_id_key
    ON platform_sessions(public_id);
CREATE INDEX IF NOT EXISTS idx_platform_sessions_admin
    ON platform_sessions(admin_id);
CREATE INDEX IF NOT EXISTS idx_platform_sessions_absolute_expires_at
    ON platform_sessions(absolute_expires_at);

-- ---------------------------------------------------------------------------
-- applications: the catalogue, as rows
-- ---------------------------------------------------------------------------
--
-- `status` is the platform's honest statement about what a capability does, and
-- it exists because the audit found the console offering seven capabilities of
-- which none had behaviour. A customer enabling something must be able to see
-- what they are getting.
--
--   IMPLEMENTED         works end to end; a person is affected and an operator
--                       can see that they were.
--   PARTIAL             some of the chain works; the gap is named in
--                       status_detail and shown in the console.
--   CONFIGURATION_ONLY  the platform records the setting and does nothing else.
--   NOT_IMPLEMENTED     declared so the catalogue is complete; not offered.
--
-- Set by migration and by the platform, never by a customer. A company may still
-- enable a CONFIGURATION_ONLY capability -- there are legitimate reasons to
-- stage configuration ahead of a release -- but the console shows the status
-- beside the switch, and the sales surface must not list one as a feature.
CREATE TABLE IF NOT EXISTS applications (
    id          BIGSERIAL PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    code        VARCHAR(30) NOT NULL,
    name        VARCHAR(80) NOT NULL,
    description TEXT NOT NULL,

    status        VARCHAR(24) NOT NULL DEFAULT 'NOT_IMPLEMENTED',
    status_detail TEXT,

    -- Lowest operator role that may open the module, so the console does not
    -- hard-code a role per capability.
    minimum_role VARCHAR(20) NOT NULL DEFAULT 'VIEWER',

    -- What a terminal assigned to this capability needs to do. Consumed by the
    -- device settings payload so firmware can be told its mode without the
    -- platform enumerating capabilities in Go.
    terminal_behaviour VARCHAR(30) NOT NULL DEFAULT 'IDENTIFY',

    sort_order SMALLINT NOT NULL DEFAULT 100,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT applications_code_check
        CHECK (code ~ '^[A-Z0-9_]{1,30}$'),
    CONSTRAINT applications_status_check CHECK (status IN (
        'IMPLEMENTED', 'PARTIAL', 'CONFIGURATION_ONLY', 'NOT_IMPLEMENTED'
    )),
    CONSTRAINT applications_minimum_role_check
        CHECK (minimum_role IN ('OWNER', 'ADMIN', 'MANAGER', 'VIEWER')),
    CONSTRAINT applications_terminal_behaviour_check CHECK (terminal_behaviour IN (
        'IDENTIFY',   -- match a credential, report who it was, release nothing
        'AUTHORIZE',  -- match, decide, and release an actuator
        'ENROL'       -- capture new credentials
    ))
);

CREATE UNIQUE INDEX IF NOT EXISTS applications_code_key ON applications(code);
CREATE UNIQUE INDEX IF NOT EXISTS applications_public_id_key ON applications(public_id);

-- The catalogue. Statuses are what the audit measured, not what anyone hopes.
INSERT INTO applications (code, name, description, status, status_detail,
                          minimum_role, terminal_behaviour, sort_order)
VALUES
  ('ACCESS_CONTROL', 'Access Control',
   'Decide whether to release a door, barrier, turnstile or lock.',
   'PARTIAL',
   'The decision is evaluated against permissions, schedules and validity '
   'windows, and every outcome is recorded. Offline behaviour follows the '
   'site''s configured policy.',
   'VIEWER', 'AUTHORIZE', 10),

  ('REGISTRATION', 'Registration',
   'Enrol people and issue their credentials.',
   'PARTIAL',
   'Enrolment is operator-initiated from the console and captured at a '
   'terminal. Credentials are distributed to the terminals a person is '
   'permitted to use.',
   'MANAGER', 'ENROL', 20),

  ('ATTENDANCE', 'Attendance',
   'Record presence against a schedule.',
   'CONFIGURATION_ONLY',
   'Presence events are recorded, but nothing computes attendance against a '
   'schedule and there are no reports. Not offered for sale.',
   'VIEWER', 'IDENTIFY', 30),

  ('CHECK_IN', 'Check-in',
   'Record arrival at an event or appointment.',
   'CONFIGURATION_ONLY',
   'Arrival events are recorded. There is no event or appointment model to '
   'record them against. Not offered for sale.',
   'VIEWER', 'IDENTIFY', 40),

  ('VERIFICATION', 'Verification',
   'Confirm a person is who they claim, and report it.',
   'CONFIGURATION_ONLY',
   'Verification outcomes are recorded. There is no claim/confirm workflow. '
   'Not offered for sale.',
   'VIEWER', 'IDENTIFY', 50),

  ('TIME_TRACKING', 'Time Tracking',
   'Accumulate worked time from arrivals and departures.',
   'CONFIGURATION_ONLY',
   'Directional events are recorded. Nothing pairs them or accrues time. '
   'Not offered for sale.',
   'VIEWER', 'IDENTIFY', 60),

  ('VISITOR_MANAGEMENT', 'Visitor Management',
   'Admit and record people who are not on the roster.',
   'CONFIGURATION_ONLY',
   'No host model, no temporary credential issuance, no off-roster admission. '
   'Not offered for sale.',
   'VIEWER', 'IDENTIFY', 70)
ON CONFLICT (code) DO UPDATE
   SET name               = EXCLUDED.name,
       description        = EXCLUDED.description,
       status             = EXCLUDED.status,
       status_detail      = EXCLUDED.status_detail,
       minimum_role       = EXCLUDED.minimum_role,
       terminal_behaviour = EXCLUDED.terminal_behaviour,
       sort_order         = EXCLUDED.sort_order;

-- ---------------------------------------------------------------------------
-- company_applications: reference the catalogue instead of a CHECK
-- ---------------------------------------------------------------------------
--
-- The constraint goes and a foreign key replaces it. Adding a capability is now
-- an INSERT into `applications`; nothing else in the platform enumerates them.
--
-- ON DELETE RESTRICT, not CASCADE: removing a capability that customers have
-- configured must fail loudly rather than silently discarding their settings.
ALTER TABLE company_applications
    DROP CONSTRAINT IF EXISTS company_applications_application_check;

ALTER TABLE company_applications ADD COLUMN IF NOT EXISTS application_id BIGINT;

UPDATE company_applications ca
   SET application_id = a.id
  FROM applications a
 WHERE a.code = ca.application
   AND ca.application_id IS NULL;

-- A configured row naming a capability the catalogue does not have cannot be
-- evaluated and cannot be repaired automatically. Refuse rather than guess.
DO $$
DECLARE
    orphaned int;
BEGIN
    SELECT count(*) INTO orphaned
      FROM company_applications WHERE application_id IS NULL;

    IF orphaned > 0 THEN
        RAISE EXCEPTION
            '% company_applications row(s) name a capability that is not in the '
            'applications catalogue. Add the missing rows to `applications` '
            'before running this migration.', orphaned;
    END IF;
END $$;

ALTER TABLE company_applications ALTER COLUMN application_id SET NOT NULL;
ALTER TABLE company_applications
    DROP CONSTRAINT IF EXISTS company_applications_application_id_fkey;
ALTER TABLE company_applications ADD CONSTRAINT company_applications_application_id_fkey
    FOREIGN KEY (application_id) REFERENCES applications(id) ON DELETE RESTRICT;

-- The two columns are kept in step BY THE DATABASE, from whichever one the
-- caller supplied.
--
-- This is what makes the change compatible. Every existing writer names the
-- capability by its CODE -- that is the value the console sends, the value the
-- device settings payload carries, and the value the upsert in
-- database/applications.go conflicts on. Requiring those callers to resolve an
-- id first would break each of them for an integrity gain the trigger provides
-- anyway, and an unknown code now fails as a foreign key violation rather than
-- as a CHECK, which is the more accurate description of what went wrong.
CREATE OR REPLACE FUNCTION resolve_company_application()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.application_id IS NULL AND NEW.application IS NOT NULL THEN
        SELECT id INTO NEW.application_id FROM applications WHERE code = NEW.application;
        IF NEW.application_id IS NULL THEN
            RAISE EXCEPTION 'unknown application %', NEW.application
                USING ERRCODE = 'foreign_key_violation';
        END IF;
    ELSIF NEW.application IS NULL AND NEW.application_id IS NOT NULL THEN
        SELECT code INTO NEW.application FROM applications WHERE id = NEW.application_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS company_applications_resolve ON company_applications;
CREATE TRIGGER company_applications_resolve
    BEFORE INSERT OR UPDATE OF application, application_id ON company_applications
    FOR EACH ROW EXECUTE FUNCTION resolve_company_application();

-- The NOT NULL above is satisfied by the trigger from here on, so drop it back
-- to a deferred guarantee: the trigger fills it before the constraint is
-- checked, and a row can never be written without one.
ALTER TABLE company_applications ALTER COLUMN application DROP NOT NULL;

COMMENT ON COLUMN company_applications.application IS
    'Denormalised capability code, kept in step with application_id. Retained '
    'because it is the value the console and the device settings payload carry; '
    'application_id is the authoritative reference.';

-- devices.application_mode: the same treatment. The CHECK enumerated the
-- capability list a third time.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_application_mode_check;

-- MULTI_PURPOSE plus any catalogue code. Enforced by trigger rather than by a
-- foreign key, because MULTI_PURPOSE is a device mode with no catalogue row --
-- it means "serve whatever this company has enabled".
CREATE OR REPLACE FUNCTION check_device_application_mode()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.application_mode = 'MULTI_PURPOSE' THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM applications WHERE code = NEW.application_mode) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION
        'application_mode %  is not MULTI_PURPOSE and is not a capability in the '
        'applications catalogue', NEW.application_mode
        USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS devices_application_mode_check ON devices;
CREATE TRIGGER devices_application_mode_check
    BEFORE INSERT OR UPDATE OF application_mode ON devices
    FOR EACH ROW EXECUTE FUNCTION check_device_application_mode();

-- ---------------------------------------------------------------------------
-- updated_at triggers
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_platform_admins_updated_at ON platform_admins;
CREATE TRIGGER update_platform_admins_updated_at BEFORE UPDATE ON platform_admins
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_applications_updated_at ON applications;
CREATE TRIGGER update_applications_updated_at BEFORE UPDATE ON applications
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- No seed platform administrator, matching the rule 001 adopted after shipping
-- publicly-known API keys and 008 repeated for operators: the first account is
-- created deliberately from the environment, and only into an empty table.

COMMIT;
