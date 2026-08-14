-- ---------------------------------------------------------------------------
-- 009: application modules
-- ---------------------------------------------------------------------------
--
-- This platform is general purpose. The same terminal hardware is sold to
-- companies that use it for entirely different things, and until now nothing in
-- the schema said what any of it was FOR. The tables described a company, its
-- sites, its terminals and its people, and then silently assumed the answer was
-- access control -- an assumption visible in `doors`, in `permissions`, and in
-- an event table whose only verdict is granted or denied.
--
-- An application is a CAPABILITY the platform offers, not an industry and not a
-- customer. A company enables the ones it needs:
--
--   ACCESS_CONTROL       decide whether to release a door or barrier
--   ATTENDANCE           record presence against a schedule
--   REGISTRATION         enrol people and their credentials
--   CHECK_IN             record arrival at an event or appointment
--   VERIFICATION         confirm a person is who they claim, and report it
--   TIME_TRACKING        accumulate worked time from arrivals and departures
--   VISITOR_MANAGEMENT   admit and record people who are not on the roster
--
-- NOTHING IS ENABLED BY DEFAULT. A company with no rows in company_applications
-- has no modules, which is a legitimate state and the one every existing company
-- starts in after this migration. The console must render that rather than
-- assume a default set -- the entire point is that no workflow is presumed.
--
-- Adding a capability later is a one-line change to two CHECK constraints and
-- costs no data migration. Removing one is deliberately harder, which is the
-- right asymmetry: a code that disappears would orphan whatever a customer had
-- configured against it.
--
-- WHAT THIS MIGRATION DOES NOT DO. It changes no existing behaviour. No handler
-- reads these tables yet, no device is told anything new, and the device
-- protocol is untouched -- a terminal in the field cannot tell this migration
-- ran. devices.application_mode defaults to MULTI_PURPOSE precisely so that
-- every already-registered terminal keeps behaving exactly as it does today.

BEGIN;

-- ---------------------------------------------------------------------------
-- company_applications: which capabilities a company has turned on
-- ---------------------------------------------------------------------------
--
-- An ABSENT row means "not enabled", the same as enabled = FALSE. Both states
-- exist because they carry different intent: no row is "never configured",
-- while enabled = FALSE preserves the settings a company had chosen so that
-- turning a module back on does not lose them.
--
-- No deleted_at. This is configuration, not an entity with a history worth
-- retaining; disabling is what "removing" a module means, and a row can be
-- deleted outright when a company wants it genuinely gone.
CREATE TABLE IF NOT EXISTS company_applications (
    id          BIGSERIAL PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id  BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    application VARCHAR(30) NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,

    -- Per-company configuration for this module, opaque to the platform. The
    -- same shape sites.settings uses, and for the same reason: what a module
    -- needs to be told differs per module, and a column per setting would mean
    -- a migration every time one gained an option.
    settings    JSONB NOT NULL DEFAULT '{}'::jsonb,

    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT company_applications_application_check CHECK (application IN (
        'ACCESS_CONTROL',
        'ATTENDANCE',
        'REGISTRATION',
        'CHECK_IN',
        'VERIFICATION',
        'TIME_TRACKING',
        'VISITOR_MANAGEMENT'
    )),

    CONSTRAINT company_applications_settings_object_check
        CHECK (jsonb_typeof(settings) = 'object')
);

-- One row per capability per company. A company either has a module configured
-- or it does not; two rows for the same pair would make "is this on" ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS company_applications_company_application_key
    ON company_applications(company_id, application);
CREATE UNIQUE INDEX IF NOT EXISTS company_applications_public_id_key
    ON company_applications(public_id);

-- The hot read: "what is this company allowed to show me", on every console load.
CREATE INDEX IF NOT EXISTS idx_company_applications_enabled
    ON company_applications(company_id) WHERE enabled;

-- ---------------------------------------------------------------------------
-- devices.application_mode: what one terminal is for
-- ---------------------------------------------------------------------------
--
-- Per DEVICE, not per site. A single site routinely mixes purposes -- a door
-- terminal at the entrance and a registration desk in the office are the same
-- hardware at the same address doing different jobs -- so a site-level setting
-- could not express the common case.
--
-- MULTI_PURPOSE means the terminal serves every capability its company has
-- enabled, and is the default for one specific reason: every terminal already
-- registered gets it, so this column changes the behaviour of exactly nothing
-- that is currently deployed.
--
-- A mode is only meaningful while its company has that capability enabled. The
-- column deliberately does NOT enforce that with a foreign key to
-- company_applications: a company disabling a module would then either fail or
-- silently rewrite its terminals. Instead the mode is retained and resolves to
-- "not effective" until the capability is switched back on, which is
-- recoverable and visible.
ALTER TABLE devices ADD COLUMN IF NOT EXISTS application_mode VARCHAR(30)
    NOT NULL DEFAULT 'MULTI_PURPOSE';

-- MULTI_PURPOSE plus every value company_applications accepts. Adding a
-- capability means adding it to both lists.
ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_application_mode_check;
ALTER TABLE devices ADD CONSTRAINT devices_application_mode_check
    CHECK (application_mode IN (
        'MULTI_PURPOSE',
        'ACCESS_CONTROL',
        'ATTENDANCE',
        'REGISTRATION',
        'CHECK_IN',
        'VERIFICATION',
        'TIME_TRACKING',
        'VISITOR_MANAGEMENT'
    ));

-- "Which terminals are running which module", for the fleet view.
CREATE INDEX IF NOT EXISTS idx_devices_application_mode
    ON devices(application_mode) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- updated_at trigger (update_updated_at_column() is defined in 001)
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_company_applications_updated_at ON company_applications;
CREATE TRIGGER update_company_applications_updated_at BEFORE UPDATE ON company_applications
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- No seed rows, deliberately. Enabling a module for a company is a decision
-- somebody makes, not a default this migration is entitled to choose.

COMMIT;
