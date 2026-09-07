-- 029: quarantine a terminal from roster-driven change.
--
-- WHY THIS EXISTS. A terminal under investigation must be able to keep talking
-- to the platform -- heartbeats, access-log delivery, diagnostics -- WITHOUT
-- the platform reshaping its roster underneath the investigation.
--
-- The incident: a bench terminal was holding two fingerprint templates that
-- were the only evidence of a live defect. Bringing the API up so the terminal
-- could drain its queued access events also woke the 15-minute roster
-- reconciler, which promptly queued DELETE jobs for both people. A DELETE is
-- applied at the terminal by removing the member row AND calling
-- removeTemplate() on their sensor slot, so delivering those jobs would have
-- erased the evidence. Nothing was wrong with the reconciler; it did exactly
-- what it is for. There was simply no way to say "leave this one alone".
--
-- THIS IS NOT `disabled_at` AND NOT `active = FALSE`. Those take a terminal out
-- of service: it stops admitting people. A quarantined terminal keeps working
-- as a door and keeps reporting; it is only excluded from roster membership
-- changes and the jobs they generate. Conflating the two would mean an
-- investigator had to choose between preserving evidence and keeping a door
-- open, which is not a choice anybody should be offered.
--
-- REVERSIBLE BY DESIGN: clearing the column returns the terminal to ordinary
-- reconciliation, and the next pass converges it. Nothing is lost by pausing,
-- only deferred.

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS sync_paused_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS sync_paused_by   VARCHAR(255),
    ADD COLUMN IF NOT EXISTS sync_pause_reason TEXT;

COMMENT ON COLUMN devices.sync_paused_at IS
    'When set, this terminal is excluded from roster reconciliation and from '
    'the CREATE/UPDATE/DELETE/FULL_SYNC jobs it generates. The terminal keeps '
    'admitting people, heartbeating and uploading access events. Used to stop '
    'the platform reshaping a terminal that is under investigation. Clearing '
    'the column resumes normal reconciliation.';

COMMENT ON COLUMN devices.sync_pause_reason IS
    'Why this terminal is quarantined, for the operator who finds it paused '
    'weeks later and has to decide whether it still needs to be.';

-- Partial index: the predicate is `sync_paused_at IS NULL` on every roster
-- query, and quarantined terminals are rare, so the useful index is over the
-- few rows that are paused rather than the many that are not.
CREATE INDEX IF NOT EXISTS idx_devices_sync_paused
    ON devices (id) WHERE sync_paused_at IS NOT NULL;
