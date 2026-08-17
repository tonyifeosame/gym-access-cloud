-- ---------------------------------------------------------------------------
-- 020: credential placement generation, and sensor-local substance
-- ---------------------------------------------------------------------------
--
-- Both requested by, or forced by, docs/firmware-protocol-requirements.md
-- section 4 in the FIRMWARE repository.

BEGIN;

-- Requested by the firmware side, and the reasoning is theirs: the sensor hands
-- out the LOWEST FREE SLOT, so a slot freed by a deletion is the next one an
-- enrolment receives. `(device, slot)` therefore names a different finger before
-- and after a deletion, and a stale placement is ambiguous about which era it
-- belongs to.
--
-- The firmware's rebind logic is written so the ambiguity is harmless in
-- practice -- a slot already claimed by a known person is never taken -- but
-- "harmless in practice" is not the same as "cannot happen", and a monotonic
-- counter removes it outright.
--
-- MONOTONIC PER DEVICE, not per placement: the question being answered is "is
-- this placement from the current era of this sensor, or from before it was
-- wiped". A per-device counter answers that; a global one would too, but would
-- leak how many placements the whole installation has ever made.
ALTER TABLE devices
    ADD COLUMN IF NOT EXISTS placement_generation INTEGER NOT NULL DEFAULT 1;

ALTER TABLE credential_placements
    ADD COLUMN IF NOT EXISTS generation INTEGER NOT NULL DEFAULT 1;

ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_placement_generation_check;
ALTER TABLE devices ADD CONSTRAINT devices_placement_generation_check
    CHECK (placement_generation > 0);

ALTER TABLE credential_placements
    DROP CONSTRAINT IF EXISTS credential_placements_generation_check;
ALTER TABLE credential_placements ADD CONSTRAINT credential_placements_generation_check
    CHECK (generation > 0);

COMMENT ON COLUMN credential_placements.generation IS
    'The sensor era this placement belongs to, copied from '
    'devices.placement_generation when it was written. A placement whose '
    'generation is behind its device''s has survived a sensor wipe and its slot '
    'number names a different finger (020).';

CREATE INDEX IF NOT EXISTS idx_credential_placements_generation
    ON credential_placements(device_id, generation);

-- ---------------------------------------------------------------------------
-- A sensor-local credential has substance the platform cannot hold
-- ---------------------------------------------------------------------------
--
-- 012 wrote:
--
--   CHECK (status = 'PENDING'
--          OR sealed_material IS NOT NULL
--          OR identifier IS NOT NULL)
--
-- with the reasoning that "a credential that is neither sealed material nor a
-- non-secret identifier identifies nobody". That is right for a card, a PIN or
-- a QR code, and it is WRONG for the one credential type the fitted hardware
-- actually produces.
--
-- A SENSOR_LOCAL fingerprint's substance lives in the sensor's own flash, in a
-- numbered slot, in a proprietary format. Matching happens on the module. The
-- platform never holds the template and -- with the fitted driver, which
-- implements template export but NOT import -- could not usefully do anything
-- with it if it did. What the platform holds is the FACT that the credential
-- exists and WHERE it is placed, which is a credential_placements row.
--
-- Left as it was, the constraint makes such a credential unable to leave
-- PENDING: promoting it to ACTIVE when a terminal reports a successful
-- placement fails. Which means the status lifecycle 012 added -- the whole
-- point of which was to express "requested but not yet captured" separately
-- from "usable" -- could never reach ACTIVE for the only credential type this
-- product ships with.
--
-- The two rejected alternatives, so the choice is on the record:
--
--   * Leave it PENDING for ever. Then `status` says nothing, and the
--     authorization engine -- which admits only ACTIVE credentials -- would
--     have to special-case fingerprints, which is the assumption leaking into
--     a second place.
--
--   * Write the locator ("terminal:AT-0001:slot:5") into `identifier`. It would
--     satisfy the constraint, but `identifier` is documented as a NON-SECRET
--     IDENTIFIER A PERSON PRESENTS -- a card number, a QR payload. A locator is
--     neither presented nor an identity; it is an address. Putting it there
--     would repeat exactly the mistake `people.fingerprint_template` made,
--     which is storing a placement in a field named for material.
--
-- So the constraint gains the case it was missing: a credential whose format
-- says the material is on the sensor is substantiated by its placements. It is
-- NOT a blanket relaxation -- a credential with no format, or an IDENTIFIER
-- format, still has to carry something.
ALTER TABLE credentials DROP CONSTRAINT IF EXISTS credentials_substance_check;
ALTER TABLE credentials ADD CONSTRAINT credentials_substance_check CHECK (
    status = 'PENDING'
    OR sealed_material IS NOT NULL
    OR identifier IS NOT NULL
    -- The material is on the sensor that captured it; credential_placements
    -- records which sensor and which slot.
    OR template_format = 'SENSOR_LOCAL'
);

COMMIT;
