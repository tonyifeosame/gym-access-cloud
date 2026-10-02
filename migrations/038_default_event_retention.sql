-- 038_default_event_retention.sql
--
-- A retention window for every company's door history, and the first one the
-- legacy access_logs table has ever had.
--
-- Numbered 038, not 037: 037 is taken by the unmerged OAuth work (PR #24).
-- deploy/migrate.sh records each applied file in schema_migrations with a
-- checksum and applies only pending ones, so if PR #24 merges later its 037
-- is applied after this in production while a from-empty build applies it
-- before. Neither depends on the other, so the order does not matter.
--
-- ---------------------------------------------------------------------------
-- WHAT CHANGES
-- ---------------------------------------------------------------------------
--
-- 013 made retention a per-company setting and left NULL meaning "keep for
-- ever". The per-company setting is unchanged and still wins. What NULL now
-- means is "the platform default", which the application passes in
-- (EVENT_RETENTION_DEFAULT_DAYS; see maintenance/tasks.go). A platform default of
-- NULL reproduces 013 exactly: nothing is purged for a company without a window.
--
-- The functions decide only WHICH rows are past their window. They never
-- choose the window themselves, so the number lives in one place a deployment
-- can see and change.
--
-- ---------------------------------------------------------------------------
-- WHY access_logs IS PURGED AND NOT DROPPED
-- ---------------------------------------------------------------------------
--
-- 013 described access_logs as superseded with "nothing new written to" it.
-- That stopped being true: deployed firmware uploads door events through the
-- frozen /devices/access/log contract, and that handler writes access_logs
-- first -- its UNIQUE public_id is what tells a terminal's retry it is a
-- duplicate -- and events beside it (handlers/access.go). The legacy
-- /access/logs reads are still served from it. So it is live, it holds the same
-- personal data as events, and it had no retention at all.
--
-- It is purged on the SAME window as events. Keeping one copy of a door event
-- longer than the other would make the shorter window meaningless.
--
-- access_logs has no immutability trigger (013 put one on events and
-- audit_events only), so its purge is a plain DELETE. It is still a function,
-- so every age-based delete of door history is in one reviewable place.

BEGIN;

-- 013's one-argument purge_events is replaced, not overloaded: with both a
-- (bigint) and a (bigint, integer DEFAULT NULL) version present, the existing
-- call purge_events(NULL) would be ambiguous and fail.
DROP FUNCTION IF EXISTS purge_events(BIGINT);

-- p_default_days applies to companies with no window of their own. Below 1 is
-- treated as no default, matching companies_event_retention_check's floor.
CREATE OR REPLACE FUNCTION purge_events(p_company_id BIGINT DEFAULT NULL,
                                        p_default_days INTEGER DEFAULT NULL)
RETURNS BIGINT AS $$
DECLARE
    removed BIGINT := 0;
    fallback INTEGER := CASE WHEN p_default_days >= 1 THEN p_default_days END;
BEGIN
    PERFORM set_config('accesslink.purging', 'on', true);

    WITH expired AS (
        SELECT e.id
          FROM events e
          JOIN companies c ON c.id = e.company_id
         WHERE COALESCE(c.event_retention_days, fallback) IS NOT NULL
           AND (p_company_id IS NULL OR c.id = p_company_id)
           AND e.occurred_at < CURRENT_TIMESTAMP
                               - make_interval(days => COALESCE(c.event_retention_days, fallback))
    )
    DELETE FROM events WHERE id IN (SELECT id FROM expired);

    GET DIAGNOSTICS removed = ROW_COUNT;
    PERFORM set_config('accesslink.purging', 'off', true);
    RETURN removed;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION purge_access_logs(p_company_id BIGINT DEFAULT NULL,
                                             p_default_days INTEGER DEFAULT NULL)
RETURNS BIGINT AS $$
DECLARE
    removed BIGINT := 0;
    fallback INTEGER := CASE WHEN p_default_days >= 1 THEN p_default_days END;
BEGIN
    WITH expired AS (
        SELECT al.id
          FROM access_logs al
          JOIN companies c ON c.id = al.company_id
         WHERE COALESCE(c.event_retention_days, fallback) IS NOT NULL
           AND (p_company_id IS NULL OR c.id = p_company_id)
           AND al.occurred_at < CURRENT_TIMESTAMP
                                - make_interval(days => COALESCE(c.event_retention_days, fallback))
    )
    DELETE FROM access_logs WHERE id IN (SELECT id FROM expired);

    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$ LANGUAGE plpgsql;

-- The purge's own access path. access_logs has single-column indexes on
-- company_id and on created_at, but the window is judged on occurred_at -- the
-- door's time, not the upload's -- and per company.
CREATE INDEX IF NOT EXISTS idx_access_logs_company_occurred
    ON access_logs(company_id, occurred_at);

COMMENT ON COLUMN companies.event_retention_days IS
    'Days to retain door history: events and the legacy access_logs. NULL uses '
    'the platform default (EVENT_RETENTION_DEFAULT_DAYS).';

COMMENT ON TABLE access_logs IS
    'Legacy door log, still written by the device upload path as the idempotency '
    'record for the frozen /devices/access/log contract and read by the legacy '
    '/access/logs surface. events is the model going forward. Purged on the '
    'same window as events (038).';

COMMIT;
