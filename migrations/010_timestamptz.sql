-- ---------------------------------------------------------------------------
-- 010: every timestamp becomes an instant
-- ---------------------------------------------------------------------------
--
-- Every timestamp column in this schema was TIMESTAMP WITHOUT TIME ZONE, which
-- stores a wall-clock reading and no indication of which clock read it. That is
-- not a formatting detail. It made the recorded time of every event ambiguous,
-- and -- because two different code paths wrote into the same columns from two
-- different clocks -- it made some of them disagree with each other.
--
-- WHAT WAS ACTUALLY WRONG
--
-- Three writers, three answers, one column:
--
--   CURRENT_TIMESTAMP      writes the DATABASE server's local wall clock.
--   a Go time.Time         is sent with its offset, which PostgreSQL then
--                          DISCARDS on the way into a naive column -- so it
--                          writes the API process's local wall clock.
--   a device's RFC3339 Z   parses to a true UTC instant, whose offset is
--                          likewise discarded -- so it writes UTC.
--
-- On the deployment this was written against (server TimeZone Africa/Lagos,
-- UTC+1) that was measured: a device reporting 17:00:00Z and the server default
-- firing at the same moment landed an hour apart in access_logs.occurred_at.
-- The audit trail that answers "was this door released at 14:05" was internally
-- inconsistent depending on which side of the request filled the field in.
--
-- On top of that, lib/pq labels a naive column UTC on the way out. So the API
-- returned 18:00:00Z for something that happened at 17:00:00Z, and every
-- consumer -- the console about to render these, any customer integration --
-- would believe it, because the value says Z and looks entirely well-formed.
--
-- Three places in the Go code had already been written around this: account
-- lockout, session expiry, and the session_expires_at in the login response all
-- compute a REMAINING DURATION in SQL rather than compare a stored timestamp to
-- time.Now(). Those were correct, but they were three local escapes from one
-- global defect, and every other timestamp the API returns still carried it.
--
-- WHAT THIS DOES
--
-- Converts every timestamp column in the schema to TIMESTAMPTZ, which stores an
-- instant. After this, all three writers above mean the same thing, comparisons
-- against time.Now() are correct, and the session time zone affects only how a
-- value is DISPLAYED rather than what it means.
--
-- Existing values are wall-clock readings, so they have to be told which clock
-- took them. See the conversion note below.
--
-- DEFAULTS are dropped and re-applied around each ALTER rather than left to be
-- re-cast implicitly, so the stored expression is exactly what it was.
--
-- The loop is driven from information_schema rather than from a hand-written
-- list of 61 columns. A list is a thing that gets out of date; the catalog
-- cannot. The assertion at the end proves nothing was missed.
--
-- COST. Each ALTER ... TYPE rewrites its table and takes an ACCESS EXCLUSIVE
-- lock for the duration, and the 45 indexes over these columns are rebuilt. On
-- a large access_logs this is not instant and the table is unavailable while it
-- runs. Apply it during a maintenance window on a populated deployment.

BEGIN;

DO $$
DECLARE
    legacy_tz   text;
    explicit_tz text;
    populated   boolean;
    col         record;
    converted   int := 0;
    remaining   int;
BEGIN
    -- ------------------------------------------------------------------
    -- Which clock took the existing readings
    -- ------------------------------------------------------------------
    --
    -- The stored values are the database server's local wall clock, so they are
    -- reinterpreted in the server's own time zone. reset_val is used rather
    -- than current_setting('TimeZone') because it reports the DATABASE's
    -- configured zone and ignores a SET in this session -- and the API pins its
    -- own connections to UTC, so the session value is exactly the thing that
    -- must not be trusted here.
    --
    -- That is still only a good default. It is right when the server's zone has
    -- not changed since the data was written, and wrong if the database has
    -- since been moved or reconfigured. Set the override when you know better:
    --
    --   SET accesslink.legacy_timezone = 'Africa/Lagos';
    --
    -- On a FRESH database every table is empty, so the choice cannot affect any
    -- value and the default is always safe. It matters only when converting a
    -- database that already holds rows.
    explicit_tz := nullif(current_setting('accesslink.legacy_timezone', true), '');

    legacy_tz := coalesce(
        explicit_tz,
        (SELECT reset_val FROM pg_settings WHERE name = 'TimeZone'),
        'UTC');

    -- Fail now, with a message that says what to do, rather than partway
    -- through the loop with "time zone not recognized".
    BEGIN
        PERFORM now() AT TIME ZONE legacy_tz;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION
            'legacy time zone % is not recognised; set accesslink.legacy_timezone to a valid zone',
            legacy_tz;
    END;

    -- ------------------------------------------------------------------
    -- Say so out loud when the guess is actually load-bearing
    -- ------------------------------------------------------------------
    --
    -- On an empty database the zone cannot change any value, so the default is
    -- always safe and this is not worth a word. On a populated one the choice
    -- silently shifts every historical timestamp, and there is a real way to get
    -- it wrong: reset_val reports the zone of the CONNECTION APPLYING THIS, and
    -- the API pins its own connections to UTC. Applied down that path against
    -- real data, the default would assume the values were already UTC and leave
    -- them uncorrected.
    --
    -- deploy/migrate.sh runs psql, which pins nothing and therefore reports the
    -- server's true zone -- so the intended path is correct by default. This
    -- warning is for every other path, and it names the zone so that a wrong
    -- assumption is visible in the deploy log rather than discovered later in
    -- an access log that reads an hour out.
    SELECT (EXISTS (SELECT 1 FROM people)
         OR EXISTS (SELECT 1 FROM access_logs)
         OR EXISTS (SELECT 1 FROM devices)
         OR EXISTS (SELECT 1 FROM users)
         OR EXISTS (SELECT 1 FROM sites))
      INTO populated;

    IF populated AND explicit_tz IS NULL THEN
        RAISE WARNING
            'converting EXISTING data and reading it as % local time (from this connection''s TimeZone). '
            'If the values were written while the database server was on a different zone, roll back and '
            're-run with: SET accesslink.legacy_timezone = ''<zone>'';', legacy_tz;
    END IF;

    RAISE NOTICE 'converting naive timestamps, reading existing values as % local time', legacy_tz;

    -- ------------------------------------------------------------------
    -- The conversion
    -- ------------------------------------------------------------------
    FOR col IN
        SELECT c.table_name, c.column_name, c.column_default
          FROM information_schema.columns c
          JOIN information_schema.tables t
            ON t.table_schema = c.table_schema
           AND t.table_name   = c.table_name
         WHERE c.table_schema = 'public'
           AND t.table_type   = 'BASE TABLE'
           AND c.data_type    = 'timestamp without time zone'
         ORDER BY c.table_name, c.ordinal_position
    LOOP
        IF col.column_default IS NOT NULL THEN
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I DROP DEFAULT',
                           col.table_name, col.column_name);
        END IF;

        -- `x AT TIME ZONE 'Zone'` on a naive value reads x AS a local reading in
        -- that zone and yields the instant it denotes. This is the whole
        -- conversion; everything else here is bookkeeping around it.
        EXECUTE format(
            'ALTER TABLE public.%I ALTER COLUMN %I TYPE timestamptz USING %I AT TIME ZONE %L',
            col.table_name, col.column_name, col.column_name, legacy_tz);

        IF col.column_default IS NOT NULL THEN
            EXECUTE format('ALTER TABLE public.%I ALTER COLUMN %I SET DEFAULT %s',
                           col.table_name, col.column_name, col.column_default);
        END IF;

        converted := converted + 1;
    END LOOP;

    -- ------------------------------------------------------------------
    -- Prove it
    -- ------------------------------------------------------------------
    SELECT count(*) INTO remaining
      FROM information_schema.columns c
      JOIN information_schema.tables t
        ON t.table_schema = c.table_schema AND t.table_name = c.table_name
     WHERE c.table_schema = 'public'
       AND t.table_type   = 'BASE TABLE'
       AND c.data_type    = 'timestamp without time zone';

    IF remaining <> 0 THEN
        RAISE EXCEPTION 'timestamptz conversion incomplete: % naive column(s) remain', remaining;
    END IF;

    RAISE NOTICE 'converted % column(s) to timestamptz', converted;
END $$;

-- The updated_at trigger from 001 assigns CURRENT_TIMESTAMP, which is already a
-- timestamptz and now lands in a column that can hold one. Nothing to change.

COMMIT;
