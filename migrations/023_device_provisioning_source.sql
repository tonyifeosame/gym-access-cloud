-- ---------------------------------------------------------------------------
-- 023: how each terminal got its credential
-- ---------------------------------------------------------------------------
--
-- There are now three ways a device row acquires a working key, and they are not
-- equally trusted:
--
--   SITE_KEY      somebody held the site's provisioning secret and registered
--                 the serial. The legacy path. Still supported, still audited,
--                 but it is the one whose blast radius is the whole site.
--   CLAIM_CODE    an operator pre-authorised a known serial and an installer
--                 redeemed a single-use code at the unit (019).
--   ANNOUNCEMENT  the unit introduced itself and an operator adopted and
--                 approved it (022).
--
-- "How did this door get here" is an audit question with no answer today. The
-- audit trail records each provisioning ACTION, but the device row -- the thing
-- an operator is looking at when they ask -- carries nothing, and joining a
-- terminal back to the trail entry that created it means knowing which of three
-- action names to look for and searching a window nobody recorded.
--
-- NULLABLE, AND DELIBERATELY NOT BACKFILLED. Every row that exists today was
-- provisioned before this column did, and the honest value for those is "not
-- recorded" rather than a guess. Backfilling them all to SITE_KEY would be
-- probably-true and occasionally false, and a column that is occasionally false
-- is worse than one that is sometimes empty -- the console can say "not
-- recorded" but it cannot un-say "site key".

BEGIN;

ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS provisioned_via VARCHAR(16);

-- A CHECK rather than an enum type, matching how every other small vocabulary in
-- this schema is expressed since 015 -- and NULL passes, which is what makes the
-- constraint safe to add to a table full of pre-existing rows.
ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_provisioned_via_check;
ALTER TABLE devices
    ADD CONSTRAINT devices_provisioned_via_check
    CHECK (provisioned_via IS NULL
           OR provisioned_via IN ('SITE_KEY', 'CLAIM_CODE', 'ANNOUNCEMENT'));

COMMENT ON COLUMN devices.provisioned_via IS
    'Which provisioning path issued this terminal''s current credential: '
    'SITE_KEY, CLAIM_CODE or ANNOUNCEMENT. NULL on rows that predate '
    '023_device_provisioning_source.sql, which is reported as "not recorded" '
    'rather than guessed.';

COMMIT;
