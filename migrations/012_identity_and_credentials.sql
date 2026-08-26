-- ---------------------------------------------------------------------------
-- 012: identity, person vocabulary, and credentials as a first-class entity
-- ---------------------------------------------------------------------------
--
-- Three problems, one migration, because they are the same problem seen from
-- three angles: THE PLATFORM MODELLED A PERSON AS A GYM MEMBER WITH ONE FINGER.
--
-- 1. PERSON CLASSIFICATION WAS A FIXED BUSINESS TAXONOMY.
--
--    people.person_type carried
--        CHECK (person_type IN ('MEMBER','STAFF','CONTRACTOR','VISITOR'))
--    with DEFAULT 'MEMBER'. That is a decision about what industry the customer
--    is in, enforced by the database. A school cannot record STUDENT, a clinic
--    cannot record PATIENT, a venue cannot record ATTENDEE. Worse, the column
--    was invisible: no API exposed it, so what the console called "person type"
--    was actually membership_type -- a second, unconstrained, differently-named
--    column holding the same kind of value.
--
--    Two overlapping classification fields, one constrained and hidden, one free
--    and shown, is not a model. This migration replaces both with a PER-COMPANY
--    VOCABULARY: each company defines the categories it uses, and the platform
--    has no opinion about what they are.
--
-- 2. A CREDENTIAL WAS A COLUMN, NOT A THING.
--
--    people.fingerprint_template was a single nullable TEXT column. Everything
--    follows from that:
--
--      * one credential per person, for ever;
--      * one credential TYPE -- no card, no PIN, no mobile, no second finger;
--      * one vendor implicitly assumed, because a column has no format;
--      * no revocation of a credential without deleting the person;
--      * no record of WHERE a credential physically lives, which is why a
--        person enrolled at one terminal is unknown at every other one.
--
--    Credentials become an entity, with placements onto devices as their own
--    entity beneath them. That second table is what makes multi-terminal
--    identity expressible at all.
--
-- 3. BIOMETRIC MATERIAL HAD NOWHERE SAFE TO LIVE.
--
--    The firmware's position -- templates stay in the sensor, the cloud gets a
--    locator string -- is privacy-correct and operationally useless: it means a
--    person is only recognised by the one terminal that enrolled them, and a
--    replaced terminal loses every enrolment on it with no backup.
--
--    The answer here is SEALED MATERIAL. A template is encrypted by the
--    enrolling terminal under a key the server never holds, and the server
--    stores only ciphertext it cannot read. It can route that ciphertext to the
--    other terminals that need it, and it can prove which plaintext it
--    corresponds to, without ever being able to recover a biometric.
--
--    See the sealed_* columns below for exactly what is and is not guaranteed.
--
-- WHAT THIS MIGRATION DOES NOT DO. It changes no wire contract. people keeps
-- external_id, membership_type and fingerprint_template, all still populated,
-- because deployed terminals and the /api/v1/members surface speak them. The new
-- model is written alongside and back-fills from them; the legacy columns become
-- derived rather than authoritative in a later migration, once no client reads
-- them.

BEGIN;

-- ---------------------------------------------------------------------------
-- person_categories: what a company calls its people
-- ---------------------------------------------------------------------------
--
-- PER COMPANY, deliberately. The whole point is that the platform does not know
-- whether a customer records employees, students, residents, patients, guests or
-- something nobody has thought of yet.
--
-- `code` is the stable machine value and is what the legacy membership_type
-- column carries on the wire, so an existing terminal keeps seeing the string it
-- already sees. `label` is what an operator reads and may be changed freely.
--
-- No seed rows, and no default category. A company that has never defined one
-- has people with no category, which is a legitimate state -- the same rule
-- 009_applications.sql established for capabilities.
CREATE TABLE IF NOT EXISTS person_categories (
    id          BIGSERIAL PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id  BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,

    -- Uppercase, no spaces: this value travels to firmware in the person sync
    -- payload, where it lands in a fixed-width buffer.
    code        VARCHAR(30) NOT NULL,
    label       VARCHAR(80) NOT NULL,
    description TEXT,

    -- Presentation order in the console. Not a priority and not a default.
    sort_order  SMALLINT NOT NULL DEFAULT 100,
    active      BOOLEAN NOT NULL DEFAULT TRUE,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT person_categories_code_check
        CHECK (code = upper(code) AND code ~ '^[A-Z0-9_]{1,30}$'),
    CONSTRAINT person_categories_label_check
        CHECK (length(btrim(label)) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS person_categories_company_code_key
    ON person_categories(company_id, code);
CREATE UNIQUE INDEX IF NOT EXISTS person_categories_public_id_key
    ON person_categories(public_id);

-- ---------------------------------------------------------------------------
-- people: adopt the vocabulary, drop the taxonomy
-- ---------------------------------------------------------------------------
ALTER TABLE people ADD COLUMN IF NOT EXISTS category_id BIGINT
    REFERENCES person_categories(id) ON DELETE SET NULL;

-- Every distinct membership_type a company already uses becomes one of its
-- categories. This is a data-preserving read of what customers actually put in
-- the free-text column, rather than an imposed set.
--
-- Values that cannot be a code (lowercase, spaces, punctuation) are normalised
-- rather than dropped: losing a category would silently reclassify people.
INSERT INTO person_categories (company_id, code, label, sort_order)
SELECT DISTINCT
       p.company_id,
       upper(regexp_replace(btrim(p.membership_type), '[^A-Za-z0-9_]+', '_', 'g')),
       btrim(p.membership_type),
       100
  FROM people p
 WHERE p.membership_type IS NOT NULL
   AND btrim(p.membership_type) <> ''
   AND length(regexp_replace(btrim(p.membership_type), '[^A-Za-z0-9_]+', '_', 'g')) BETWEEN 1 AND 30
ON CONFLICT (company_id, code) DO NOTHING;

UPDATE people p
   SET category_id = c.id
  FROM person_categories c
 WHERE c.company_id = p.company_id
   AND c.code = upper(regexp_replace(btrim(p.membership_type), '[^A-Za-z0-9_]+', '_', 'g'))
   AND p.category_id IS NULL;

-- The taxonomy goes. The column stays for one more migration so that anything
-- still reading it gets its last value rather than an error, but nothing may
-- constrain what a company is allowed to call its people.
ALTER TABLE people DROP CONSTRAINT IF EXISTS people_type_check;
COMMENT ON COLUMN people.person_type IS
    'DEPRECATED. Superseded by people.category_id -> person_categories. Retained '
    'only so that a client reading it during the transition sees its last value. '
    'No constraint: a fixed taxonomy here was an industry assumption.';
COMMENT ON COLUMN people.membership_type IS
    'LEGACY WIRE FIELD. Carried in the device sync payload as membership_type, so '
    'it cannot be renamed while deployed terminals parse it. Kept in step with '
    'person_categories.code by the application layer.';

CREATE INDEX IF NOT EXISTS idx_people_category_id
    ON people(category_id) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- credentials: what a person presents
-- ---------------------------------------------------------------------------
--
-- One row per credential, many per person. The type is open text with a CHECK
-- listing what the platform currently understands, because a credential type is
-- a platform capability rather than a customer choice -- unlike a person
-- category, adding one means writing code to verify it.
--
-- WHAT IS SECRET HERE, AND WHAT IS NOT.
--
--   identifier          NOT SECRET. A card number, a mobile device id. Indexed,
--                       searchable, shown in the console. For a biometric it is
--                       null: the template is the credential, and there is no
--                       non-secret handle for it.
--
--   sealed_material     CIPHERTEXT. The server cannot decrypt this and holds no
--                       key that could. See the sealing note below.
--
--   material_digest     SHA-256 of the PLAINTEXT material, computed on the
--                       device. Lets the server detect that two terminals hold
--                       the same template, and lets a device verify it applied
--                       the right one, WITHOUT the server learning the template.
--                       A digest of a fingerprint template is not a fingerprint
--                       and is not reversible; it is also not a stable biometric
--                       identifier across vendors, because the template encoding
--                       differs. That is deliberate -- it must not become one.
--
-- ON SEALING, AND WHAT IT DOES NOT PROMISE.
--
-- The enrolling terminal encrypts the template under a key derived from a
-- per-company secret that is provisioned to terminals and never sent to the
-- server. The server stores ciphertext, routes it to the other terminals that
-- need it, and cannot read it.
--
-- This is a real reduction in exposure: a database compromise, a backup, a
-- replica or a support engineer with SELECT yields no biometric data. It is NOT
-- a claim that the material is safe against an attacker with physical access to
-- a terminal, because that attacker can read the key out of NVS on a part with
-- no flash encryption. Flash encryption is a separate, tracked piece of work,
-- and this scheme's strength depends on it. Recorded here rather than in a
-- design document, because the column is what a future reader will find first.
CREATE TABLE IF NOT EXISTS credentials (
    id          BIGSERIAL PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id  BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    person_id   BIGINT NOT NULL REFERENCES people(id) ON DELETE CASCADE,

    credential_type VARCHAR(30) NOT NULL,

    -- Vendor abstraction. The platform must not assume one sensor: an R307, a
    -- suprema module and a match-on-card reader produce incompatible templates,
    -- and a placement is only valid on a device that speaks the same format.
    vendor          VARCHAR(40),
    template_format VARCHAR(40),

    identifier      VARCHAR(128),

    sealed_material   BYTEA,
    sealed_key_id     VARCHAR(64),
    sealed_algorithm  VARCHAR(32),
    material_digest   CHAR(64),

    -- Lifecycle. PENDING covers a credential an operator has requested but
    -- nobody has presented a finger for yet, which is the state the enrolment
    -- workflow needs and the old column could not express.
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING',

    -- Where it was captured. Null once that terminal is retired -- the
    -- credential outlives the hardware that enrolled it, which is the entire
    -- point of sealing it.
    enrolled_device_id BIGINT REFERENCES devices(id) ON DELETE SET NULL,
    enrolled_at        TIMESTAMPTZ,

    -- Independent of the person's own validity window. A contractor's badge may
    -- expire before the contractor's record does.
    valid_from  TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,

    revoked_at     TIMESTAMPTZ,
    revoked_reason VARCHAR(120),

    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,

    CONSTRAINT credentials_type_check CHECK (credential_type IN (
        'FINGERPRINT',
        'CARD',
        'PIN',
        'MOBILE',
        'FACE',
        'QR'
    )),

    CONSTRAINT credentials_status_check CHECK (status IN (
        'PENDING',    -- requested, not yet captured
        'ACTIVE',     -- usable
        'SUSPENDED',  -- temporarily withdrawn, reversible
        'REVOKED'     -- permanently withdrawn
    )),

    -- A revoked credential must say when. Enforced rather than assumed, because
    -- "is this revoked" is asked on the authorization path.
    CONSTRAINT credentials_revocation_check
        CHECK ((status = 'REVOKED') = (revoked_at IS NOT NULL)),

    CONSTRAINT credentials_validity_check
        CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),

    -- 64 lowercase hex, the same storage shape every other digest in this
    -- schema uses.
    CONSTRAINT credentials_digest_check
        CHECK (material_digest IS NULL OR material_digest ~ '^[0-9a-f]{64}$'),

    -- Sealed material is meaningless without knowing what sealed it. Either all
    -- three are present or none is.
    CONSTRAINT credentials_sealing_check CHECK (
        (sealed_material IS NULL AND sealed_key_id IS NULL AND sealed_algorithm IS NULL)
        OR (sealed_material IS NOT NULL AND sealed_key_id IS NOT NULL AND sealed_algorithm IS NOT NULL)
    ),

    -- A credential that is neither sealed material nor a non-secret identifier
    -- identifies nobody. PENDING is exempt: it is precisely the state before
    -- either exists.
    CONSTRAINT credentials_substance_check CHECK (
        status = 'PENDING'
        OR sealed_material IS NOT NULL
        OR identifier IS NOT NULL
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS credentials_public_id_key ON credentials(public_id);

CREATE INDEX IF NOT EXISTS idx_credentials_person
    ON credentials(person_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_credentials_company
    ON credentials(company_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_credentials_active
    ON credentials(company_id, credential_type)
    WHERE deleted_at IS NULL AND status = 'ACTIVE';

-- A non-secret identifier must be unique within its type and company, or two
-- people could present the same card and the reader could not decide.
-- Biometrics carry no identifier and are unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS credentials_company_type_identifier_key
    ON credentials(company_id, credential_type, identifier)
    WHERE identifier IS NOT NULL AND deleted_at IS NULL AND status <> 'REVOKED';

-- ---------------------------------------------------------------------------
-- credential_placements: which terminal physically holds which credential
-- ---------------------------------------------------------------------------
--
-- THIS IS THE TABLE THAT MAKES MULTI-TERMINAL IDENTITY POSSIBLE, and its absence
-- is why the platform did not have it.
--
-- A fingerprint template is not data the cloud can act on -- it is data a SENSOR
-- must physically hold, in a numbered slot, before it can match anything. The
-- old model recorded that fact as a string ("terminal:AABBCC:slot:5") in a
-- column meant for the template itself, which is why it could describe exactly
-- one terminal and could not be reconciled, retried or revoked.
--
-- A placement is the server's record of an intention and its outcome: this
-- credential should be on this device, in this slot, and here is whether it got
-- there. That makes distribution a convergent process with a visible state
-- rather than a side effect of where somebody happened to stand.
--
-- SLOTS ARE ALLOCATED BY THE DEVICE, NOT THE SERVER. The sensor picks the lowest
-- free slot and the device reports back which one it used. A server-assigned
-- slot would be wrong the moment a sensor was replaced, a template failed to
-- write, or a device was re-adopted with templates already on it.
CREATE TABLE IF NOT EXISTS credential_placements (
    id            BIGSERIAL PRIMARY KEY,
    public_id     UUID NOT NULL DEFAULT gen_random_uuid(),
    credential_id BIGINT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    device_id     BIGINT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,

    -- Reported by the device once it has stored the template. Null until then.
    slot INTEGER,

    state VARCHAR(20) NOT NULL DEFAULT 'PENDING',

    -- Why a placement failed, in the device's words, so an operator sees
    -- "sensor full" rather than "failed".
    last_error   VARCHAR(160),
    attempts     SMALLINT NOT NULL DEFAULT 0,

    placed_at    TIMESTAMPTZ,
    removed_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT credential_placements_state_check CHECK (state IN (
        'PENDING',   -- should be here, not yet delivered
        'PLACED',    -- device confirmed it holds this credential
        'FAILED',    -- device could not store it; last_error says why
        'REMOVING',  -- should no longer be here, removal not yet confirmed
        'REMOVED'    -- device confirmed it no longer holds it
    )),

    -- A device reporting a slot must be reporting a real one. Slot 0 is the
    -- "unbound" sentinel in the firmware and can never be a placement.
    CONSTRAINT credential_placements_slot_check
        CHECK (slot IS NULL OR slot > 0),

    CONSTRAINT credential_placements_placed_check
        CHECK (state <> 'PLACED' OR (slot IS NOT NULL AND placed_at IS NOT NULL)),

    CONSTRAINT credential_placements_attempts_check
        CHECK (attempts >= 0)
);

-- One placement per credential per device. Two would make "is it on that
-- terminal" ambiguous, which is the question the whole table exists to answer.
CREATE UNIQUE INDEX IF NOT EXISTS credential_placements_credential_device_key
    ON credential_placements(credential_id, device_id);
CREATE UNIQUE INDEX IF NOT EXISTS credential_placements_public_id_key
    ON credential_placements(public_id);

-- One slot holds one credential. Partial, so a REMOVED placement frees its slot
-- for the next enrolment -- which is exactly what the sensor does.
CREATE UNIQUE INDEX IF NOT EXISTS credential_placements_device_slot_key
    ON credential_placements(device_id, slot)
    WHERE slot IS NOT NULL AND state IN ('PLACED', 'REMOVING');

-- The operational read: "what does this terminal still owe me".
CREATE INDEX IF NOT EXISTS idx_credential_placements_device_state
    ON credential_placements(device_id, state);
CREATE INDEX IF NOT EXISTS idx_credential_placements_credential
    ON credential_placements(credential_id);

-- ---------------------------------------------------------------------------
-- Back-fill: adopt what the old column already recorded
-- ---------------------------------------------------------------------------
--
-- Every person with a non-empty fingerprint_template has a credential today,
-- even though what is stored is a locator rather than material. Creating the
-- credential row preserves the FACT of enrolment, which is what the console
-- reports and what an operator would otherwise lose.
--
-- Status ACTIVE, not PENDING: these people can open doors right now, and
-- describing them as awaiting enrolment would be a lie that the authorization
-- path would then act on.
--
-- No sealed_material: there is none to adopt. The identifier carries the legacy
-- locator so the origin of the row is traceable, and so that
-- credentials_substance_check is satisfied by something true.
INSERT INTO credentials (company_id, person_id, credential_type, identifier,
                         status, enrolled_at, created_at)
SELECT p.company_id,
       p.id,
       'FINGERPRINT',
       'legacy:' || left(p.fingerprint_template, 100),
       'ACTIVE',
       p.updated_at,
       p.created_at
  FROM people p
 WHERE p.fingerprint_template IS NOT NULL
   AND btrim(p.fingerprint_template) <> ''
   AND p.deleted_at IS NULL
ON CONFLICT DO NOTHING;

-- Where the legacy locator names a terminal in the documented
-- "terminal:<device>:slot:<n>" form, recover the placement it was describing.
-- This is the one chance to learn which sensor actually holds these templates;
-- after this the information exists nowhere else.
--
-- Matched on serial_number because that is what the firmware puts in the string
-- (device_info::deviceId()). A locator naming a device this installation does
-- not have is left unplaced rather than guessed at.
INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
SELECT c.id,
       d.id,
       NULLIF(split_part(c.identifier, ':slot:', 2), '')::int,
       'PLACED',
       c.enrolled_at
  FROM credentials c
  JOIN devices d
    ON d.serial_number = split_part(split_part(c.identifier, 'legacy:terminal:', 2), ':slot:', 1)
   AND d.deleted_at IS NULL
 WHERE c.identifier LIKE 'legacy:terminal:%:slot:%'
   AND split_part(c.identifier, ':slot:', 2) ~ '^[0-9]+$'
   AND (split_part(c.identifier, ':slot:', 2))::int > 0
ON CONFLICT DO NOTHING;

-- The credential now knows where it was enrolled, which the column never did.
UPDATE credentials c
   SET enrolled_device_id = p.device_id
  FROM credential_placements p
 WHERE p.credential_id = c.id
   AND c.enrolled_device_id IS NULL;

-- ---------------------------------------------------------------------------
-- updated_at triggers
-- ---------------------------------------------------------------------------
DROP TRIGGER IF EXISTS update_person_categories_updated_at ON person_categories;
CREATE TRIGGER update_person_categories_updated_at BEFORE UPDATE ON person_categories
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_credentials_updated_at ON credentials;
CREATE TRIGGER update_credentials_updated_at BEFORE UPDATE ON credentials
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS update_credential_placements_updated_at ON credential_placements;
CREATE TRIGGER update_credential_placements_updated_at BEFORE UPDATE ON credential_placements
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMIT;
