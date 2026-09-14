-- 033_retire_tunnel_firmware_rows.sql
--
-- Retire every firmware catalogue row whose download URL was a temporary
-- Cloudflare tunnel.
--
-- ---------------------------------------------------------------------------
-- WHAT HAPPENED
-- ---------------------------------------------------------------------------
--
-- Between 2026-09-07 and 2026-09-09 the OTA rollback trial published 1.3.1
-- through 1.3.5 into the production catalogue, each hosted on a
-- `*.trycloudflare.com` tunnel that lived only as long as the process behind
-- it (audit: FIRMWARE_PUBLISHED / FIRMWARE_TARGET_SET, actor the owner, ip
-- ::1 -- a locally run API against this database). Three of them carry the
-- note "TEST ONLY, not for production" in their own release_notes. The
-- tunnels are gone, so the current target (1.3.3) is unreachable by every
-- terminal, and the trial's 1.3.5 row -- an image no commit reproduces --
-- occupies the version number the real release needs. The unique index on
-- (company_id, device_type, version) is partial on deleted_at IS NULL, and
-- every reader -- list, promote, the device offer -- filters the same way.
--
-- ---------------------------------------------------------------------------
-- WHY RETIRE AND RE-PUBLISH RATHER THAN EDIT
-- ---------------------------------------------------------------------------
--
-- The console says it in so many words: a published version cannot be
-- edited; publish a corrected one. A row's checksum and size are what a
-- terminal verifies before it switches boot partitions, and a row that can
-- be edited after promotion is a row whose audit trail no longer describes
-- what the fleet installed. So nothing here changes a URL, a checksum or a
-- size. The rows are soft-deleted -- kept, with a note -- and the corrected
-- entries are created through POST /console/firmware, which validates them
-- and writes FIRMWARE_PUBLISHED with the actor, exactly as before.
--
-- Idempotent (WHERE deleted_at IS NULL), and a no-op on any database that
-- never had a tunnel-hosted row, which is every bench and test database.
-- Expected in production: 5 rows (ids 1-5), one of them current.
--
-- AFTER THIS RUNS THERE IS NO CURRENT 1.3.3 ROW until it is re-published
-- from its durable URL and promoted (see the release runbook). In that gap a
-- terminal is told nothing -- no offer -- which is what it has effectively
-- been told since the tunnel died.

UPDATE firmware_versions
   SET deleted_at    = CURRENT_TIMESTAMP,
       is_current    = FALSE,
       release_notes = COALESCE(release_notes, '')
                       || ' [retired by 033: download_url was a temporary trycloudflare tunnel that no longer resolves; re-published from api.accesslink.store where the image is embedded]',
       updated_at    = CURRENT_TIMESTAMP
 WHERE deleted_at IS NULL
   AND download_url LIKE 'https://%.trycloudflare.com/%';
