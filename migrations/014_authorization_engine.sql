-- ---------------------------------------------------------------------------
-- 014: the authorization engine -- permissions, schedules, scopes
-- ---------------------------------------------------------------------------
--
-- 002_core_schema.sql created `permissions` with the note that the engine
-- "lands in a later sprint". It did not. The audit found the table read by
-- ZERO lines of Go, which means every person with an active record and a bound
-- finger could open every terminal in their company, permanently, and no
-- customer could express anything narrower.
--
-- This migration builds the engine's data model. It replaces the 002 shape
-- rather than extending it, which is safe precisely because nothing has ever
-- read it: there is no client to break and no behaviour to preserve.
--
-- ---------------------------------------------------------------------------
-- WHAT CHANGED FROM THE 002 SHAPE, AND WHY
-- ---------------------------------------------------------------------------
--
-- 1. SCOPE IS COMPANY / SITE / TERMINAL, NOT DOOR.
--
--    002 scoped a permission to a `door` or a site. A door is an access-control
--    noun, and this platform is not an access-control product -- the same
--    terminal is sold to record attendance at a factory gate, check people into
--    a clinic, and verify identity at a depot counter. None of those is a door,
--    and forcing them through one was the industry assumption in the schema.
--
--    The general-purpose unit is the TERMINAL: a physical thing at a place that
--    a person presents a credential to. What it is bolted to -- a door, a
--    turnstile, a barrier, a locker, a desk, nothing at all -- is the customer's
--    business. `doors` is deprecated below rather than dropped, because
--    devices.door_id and access_logs.door_id still reference it.
--
-- 2. ALLOW AND DENY, WITH DENY WINNING.
--
--    002 had only implicit allow. Real deployments need exclusion: "everyone in
--    Operations, except this one person, except this one terminal". Expressing
--    that with allow-only rules means enumerating every person who is not
--    excluded, and re-enumerating them whenever anyone joins.
--
--    An explicit DENY that beats any ALLOW is the standard resolution and the
--    only one that fails safe: a rule that was meant to exclude somebody and is
--    evaluated wrongly must exclude them, not admit them.
--
-- 3. SCHEDULES ARE NAMED AND REUSABLE, NOT INLINE.
--
--    002 carried days_of_week, start_time and end_time on the permission row.
--    That expresses exactly one window, so "weekdays 09:00-17:00 and Saturday
--    mornings" was not representable, and "office hours" had to be retyped on
--    every permission and re-edited on every one of them when it changed.
--
--    A schedule is a named object with MANY windows, referenced by many
--    permissions. The inline columns are migrated into generated schedules and
--    then dropped -- no data is lost and there is no second way to say the same
--    thing.
--
-- 4. access_level IS GONE.
--
--    CHECK (access_level IN ('STANDARD','RESTRICTED','ADMIN')) was a three-tier
--    model with no defined semantics -- nothing read it, so nothing decided what
--    RESTRICTED restricted. Replaced by things that mean something: an
--    application scope, a schedule, a validity window and an effect.
--
-- ---------------------------------------------------------------------------
-- HOW A DECISION IS REACHED
-- ---------------------------------------------------------------------------
--
--   1. Collect every live permission for the person that MATCHES the request:
--      scope covers the terminal, application matches or is unscoped, the
--      validity window is open, and the schedule admits the moment.
--   2. If any matching permission has effect DENY -> DENIED.
--   3. Otherwise if any has effect ALLOW -> GRANTED.
--   4. Otherwise -> DENIED, because absence of permission is not permission.
--
-- Step 4 is the one worth stating. A platform whose default is "allow unless
-- denied" cannot be made safe by adding rules; this default means a customer
-- who has configured nothing admits nobody, and has to say who may come in.
--
-- MIGRATION NOTE ON THAT DEFAULT. Deny-by-default would lock out every existing
-- deployment on the day it ships, because no permission rows exist. The
-- back-fill at the bottom of this migration grants each existing person a
-- company-scoped ALLOW, reproducing exactly the behaviour they have today. The
-- default becomes meaningful for people created after it, and no door stops
-- working on upgrade.

BEGIN;

-- ---------------------------------------------------------------------------
-- schedules
-- ---------------------------------------------------------------------------
--
-- Evaluated in the SCHEDULE'S OWN timezone, not the server's and not the site's.
-- A company operating across timezones needs "09:00 local to each site", and a
-- company operating one shift pattern across all of them needs "09:00 in head
-- office time". Only an explicit zone can express both; inheriting the site's
-- would silently pick one of them.
CREATE TABLE IF NOT EXISTS schedules (
    id          BIGSERIAL PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id  BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,

    name        VARCHAR(80) NOT NULL,
    description TEXT,

    -- NULL means "evaluate in the timezone of the site the terminal is at",
    -- which is the common case and the one that needs no configuration.
    timezone    VARCHAR(64),

    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at  TIMESTAMPTZ,

    CONSTRAINT schedules_name_check CHECK (length(btrim(name)) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS schedules_public_id_key ON schedules(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS schedules_company_name_key
    ON schedules(company_id, name) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_schedules_company
    ON schedules(company_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- schedule_windows
-- ---------------------------------------------------------------------------
--
-- Day-of-week bitmask, matching the convention 002 established and the console
-- already renders: Mon=1, Tue=2, Wed=4, Thu=8, Fri=16, Sat=32, Sun=64.
--
-- WINDOWS MAY CROSS MIDNIGHT. end_time <= start_time means the window runs into
-- the following day -- a night shift starting 22:00 and ending 06:00 is one
-- window, not two, and days_of_week names the day it STARTS on. Splitting it
-- into two rows would make Sunday night's shift look like Monday's.
CREATE TABLE IF NOT EXISTS schedule_windows (
    id          BIGSERIAL PRIMARY KEY,
    schedule_id BIGINT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,

    days_of_week SMALLINT NOT NULL DEFAULT 127,
    start_time   TIME NOT NULL,
    end_time     TIME NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT schedule_windows_days_check
        CHECK (days_of_week BETWEEN 1 AND 127),

    -- Equal times would be a zero-length window that admits nobody, which is
    -- never what somebody meant to configure.
    CONSTRAINT schedule_windows_span_check
        CHECK (start_time <> end_time)
);

CREATE INDEX IF NOT EXISTS idx_schedule_windows_schedule
    ON schedule_windows(schedule_id);

-- ---------------------------------------------------------------------------
-- permissions: rebuilt
-- ---------------------------------------------------------------------------
--
-- The 002 table is replaced in place. Its columns are dropped and the new ones
-- added, rather than a new table being created beside it, so that nothing is
-- left holding a stale definition of the same concept.
ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_scope_check;
ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_level_check;
ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_days_check;
ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_window_check;

DROP INDEX IF EXISTS permissions_person_door_key;
DROP INDEX IF EXISTS permissions_person_site_key;
DROP INDEX IF EXISTS idx_permissions_door_id;

-- Tenancy, denormalised onto the row. Every other entity carries company_id and
-- every query filters on it; permissions reached it only through people, which
-- made the tenancy filter a join the authorization path had to remember.
ALTER TABLE permissions ADD COLUMN IF NOT EXISTS company_id BIGINT;

UPDATE permissions p
   SET company_id = pe.company_id
  FROM people pe
 WHERE pe.id = p.person_id
   AND p.company_id IS NULL;

-- A permission whose person has vanished cannot be scoped to a tenant and
-- cannot be evaluated. There is nothing to preserve.
DELETE FROM permissions WHERE company_id IS NULL;

ALTER TABLE permissions ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_company_id_fkey;
ALTER TABLE permissions ADD CONSTRAINT permissions_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE;

ALTER TABLE permissions ADD COLUMN IF NOT EXISTS scope_type VARCHAR(10) NOT NULL DEFAULT 'COMPANY';
ALTER TABLE permissions ADD COLUMN IF NOT EXISTS device_id BIGINT
    REFERENCES devices(id) ON DELETE CASCADE;
ALTER TABLE permissions ADD COLUMN IF NOT EXISTS application VARCHAR(30);
ALTER TABLE permissions ADD COLUMN IF NOT EXISTS schedule_id BIGINT
    REFERENCES schedules(id) ON DELETE SET NULL;
ALTER TABLE permissions ADD COLUMN IF NOT EXISTS effect VARCHAR(10) NOT NULL DEFAULT 'ALLOW';

-- Existing rows: a site-scoped row keeps its site, everything else (including
-- the door-scoped rows, whose concept is going away) becomes company-scoped.
-- Widening rather than dropping, because these rows were never evaluated and
-- narrowing them would invent a restriction nobody configured.
UPDATE permissions
   SET scope_type = CASE WHEN site_id IS NOT NULL THEN 'SITE' ELSE 'COMPANY' END
 WHERE scope_type = 'COMPANY';

-- ---------------------------------------------------------------------------
-- Carry the inline schedule into a real one
-- ---------------------------------------------------------------------------
--
-- Only rows that actually restricted something. days_of_week = 127 with no
-- times is "always", which is the absence of a schedule rather than a schedule.
DO $$
DECLARE
    perm RECORD;
    sched_id BIGINT;
    sched_name TEXT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'permissions' AND column_name = 'days_of_week') THEN
        RETURN;
    END IF;

    FOR perm IN
        SELECT id, company_id, days_of_week, start_time, end_time
          FROM permissions
         WHERE (days_of_week IS NOT NULL AND days_of_week <> 127)
            OR start_time IS NOT NULL
            OR end_time IS NOT NULL
    LOOP
        sched_name := 'Migrated window ' || perm.id;

        INSERT INTO schedules (company_id, name, description)
        VALUES (perm.company_id, sched_name,
                'Generated by 014_authorization_engine.sql from the inline '
                'day/time columns on permission ' || perm.id || '.')
        RETURNING id INTO sched_id;

        INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
        VALUES (sched_id,
                COALESCE(NULLIF(perm.days_of_week, 0), 127),
                COALESCE(perm.start_time, TIME '00:00'),
                -- 23:59:59 rather than 00:00, which would read as a
                -- midnight-crossing window covering the whole next day.
                COALESCE(perm.end_time, TIME '23:59:59'));

        UPDATE permissions SET schedule_id = sched_id WHERE id = perm.id;
    END LOOP;
END $$;

ALTER TABLE permissions DROP COLUMN IF EXISTS days_of_week;
ALTER TABLE permissions DROP COLUMN IF EXISTS start_time;
ALTER TABLE permissions DROP COLUMN IF EXISTS end_time;
ALTER TABLE permissions DROP COLUMN IF EXISTS access_level;
ALTER TABLE permissions DROP COLUMN IF EXISTS door_id;

-- ---------------------------------------------------------------------------
-- The constraints that make a permission evaluable
-- ---------------------------------------------------------------------------

-- Exactly one scope, and it must carry the thing it names. A SITE-scoped row
-- with no site is a rule that matches nothing and would sit in the table
-- looking like access somebody had been granted.
ALTER TABLE permissions ADD CONSTRAINT permissions_scope_check CHECK (
    (scope_type = 'COMPANY'  AND site_id IS NULL     AND device_id IS NULL)
 OR (scope_type = 'SITE'     AND site_id IS NOT NULL AND device_id IS NULL)
 OR (scope_type = 'TERMINAL' AND site_id IS NULL     AND device_id IS NOT NULL)
);

ALTER TABLE permissions ADD CONSTRAINT permissions_effect_check
    CHECK (effect IN ('ALLOW', 'DENY'));

ALTER TABLE permissions ADD CONSTRAINT permissions_application_format_check
    CHECK (application IS NULL OR application ~ '^[A-Z0-9_]{1,30}$');

ALTER TABLE permissions ADD CONSTRAINT permissions_window_check
    CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at);

-- One rule per (person, scope, application, effect). Two identical ALLOWs are
-- redundant; two identical rules with different effects would make the outcome
-- depend on row order, which is exactly the ambiguity DENY-wins exists to remove.
CREATE UNIQUE INDEX IF NOT EXISTS permissions_person_company_key
    ON permissions(person_id, effect, COALESCE(application, ''))
    WHERE scope_type = 'COMPANY' AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS permissions_person_site_key
    ON permissions(person_id, site_id, effect, COALESCE(application, ''))
    WHERE scope_type = 'SITE' AND deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS permissions_person_device_key
    ON permissions(person_id, device_id, effect, COALESCE(application, ''))
    WHERE scope_type = 'TERMINAL' AND deleted_at IS NULL;

-- The authorization read: "every live rule for this person". Ordered so the
-- engine can stop at the first DENY.
CREATE INDEX IF NOT EXISTS idx_permissions_person_live
    ON permissions(person_id, effect)
    WHERE deleted_at IS NULL AND active;

CREATE INDEX IF NOT EXISTS idx_permissions_company
    ON permissions(company_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_permissions_device
    ON permissions(device_id) WHERE deleted_at IS NULL AND device_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Back-fill: preserve today's behaviour exactly
-- ---------------------------------------------------------------------------
--
-- Today every active person opens every terminal in their company, because
-- nothing evaluates permissions. Deny-by-default without this would lock out
-- every deployed installation the moment the engine is switched on.
--
-- One company-scoped ALLOW per person, with no application scope, no schedule
-- and no validity window -- which is precisely "as it is now", written down. A
-- customer can then narrow it; the platform has not decided anything for them.
INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
SELECT p.company_id, p.id, 'COMPANY', 'ALLOW', TRUE
  FROM people p
 WHERE p.deleted_at IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM permissions x
         WHERE x.person_id = p.id
           AND x.scope_type = 'COMPANY'
           AND x.effect = 'ALLOW'
           AND x.application IS NULL
           AND x.deleted_at IS NULL
   );

-- ---------------------------------------------------------------------------
-- doors: deprecated
-- ---------------------------------------------------------------------------
--
-- Not dropped: devices.door_id and access_logs.door_id reference it, and
-- dropping a table out from under two live foreign keys to remove a concept
-- nobody reads is a bigger change than the problem. It is removed from the
-- MODEL -- nothing in the authorization path, the sync path or the console
-- touches it -- and scheduled for removal once those two columns go.
COMMENT ON TABLE doors IS
    'DEPRECATED and unused. A door is an access-control noun; this platform is '
    'general purpose and scopes permissions to COMPANY, SITE or TERMINAL '
    'instead (014_authorization_engine.sql). Retained only because '
    'devices.door_id and access_logs.door_id still reference it. Scheduled for '
    'removal with those columns.';

-- ---------------------------------------------------------------------------
-- updated_at triggers
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_schedules_updated_at ON schedules;
CREATE TRIGGER update_schedules_updated_at BEFORE UPDATE ON schedules
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
