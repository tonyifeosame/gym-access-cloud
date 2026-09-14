-- 035_people_company_created_index.sql
--
-- One index for the three reads that walk a company's roster newest-first.
--
-- ---------------------------------------------------------------------------
-- WHAT THE PLANNER WAS DOING
-- ---------------------------------------------------------------------------
--
-- Every people read orders by (created_at DESC, id DESC) inside one company:
-- the console's list page (database.ListConsolePeople), the site-key roster
-- (GetAllMembers, now with an optional page) and the public API's keyset
-- listing (MembersAfter, whose `(created_at, id) < (...)` predicate is the
-- same ordering expressed as a range). The only index on people that
-- selects a company was idx_people_company_id, which finds the rows and
-- says nothing about their order -- so every page, including the first,
-- read the company's whole roster and top-N sorted it.
--
-- Measured on a 20,000-person company (EXPLAIN ANALYZE, PostgreSQL 18):
--
--   first page of 50, before:  Seq Scan + top-N heapsort, 930 buffers, 9.7 ms
--   first page of 50, after:   Index Scan, 6 buffers, 0.14 ms
--   page at offset 5,000:      Index Scan, 107 buffers, 2.0 ms
--
-- The cost was proportional to the roster on every page load of the People
-- screen and on every full roster read a terminal integration makes, and it
-- grows with the customer. The index makes each page cost its own size.
--
-- ---------------------------------------------------------------------------
-- WHY THESE COLUMNS, AND WHY PARTIAL
-- ---------------------------------------------------------------------------
--
-- company_id leads so the scan starts inside the tenant; created_at DESC and
-- id DESC match the ORDER BY exactly, tiebreak included, so no sort node is
-- needed and a keyset range is a contiguous slice of the index. Partial on
-- deleted_at IS NULL because every one of those reads filters on it and the
-- index need not carry rows nobody lists.
--
-- idx_people_company_id stays: other reads select a company without this
-- ordering, and a wider index is not a substitute for a narrower one on the
-- hot path they use.

BEGIN;

CREATE INDEX IF NOT EXISTS idx_people_company_created
    ON people(company_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

COMMIT;
