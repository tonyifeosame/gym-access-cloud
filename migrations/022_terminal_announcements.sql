-- ---------------------------------------------------------------------------
-- 022: terminal announcements (announce-and-approve provisioning)
-- ---------------------------------------------------------------------------
--
-- THE PROBLEM CLAIM CODES DID NOT SOLVE. 019 removed the site key from an
-- installer's laptop, and it was right to. What it could not remove is the
-- SERIAL: a claim code is minted FOR a serial, and the serial is derived from
-- the factory MAC and printed only on the terminal's USB console at boot. So
-- issuing a code requires a cable to read the serial, and redeeming one requires
-- a cable to type the code. A customer with a box and a phone cannot start.
--
-- THE EXCHANGE, INVERTED. The terminal introduces itself to the cloud and
-- displays an eight-character PAIRING CODE on its own panel. An authenticated
-- operator types that code into the console, picks a site, and approves. The
-- device then collects its credential.
--
-- Nothing is bound to a company until a signed-in operator types a code that is
-- physically displayed on the unit in front of them. That single fact is what
-- makes an unauthenticated announce endpoint safe, and it is what replaces the
-- serial-binding property claim codes rest on. It is NOT an open claim code:
-- there is no server-minted secret that arbitrary hardware can redeem. The
-- secret travels in the other direction and is bound to exactly one
-- announcement, from one serial, from the moment it exists.
--
-- WHY A TABLE AND NOT A COLUMN ON devices. An announcement exists BEFORE any
-- device row does -- that is the whole point -- and it must survive being
-- rejected, expired or superseded. "Which unit asked to join, who let it in, and
-- when" is an audit question that needs the history, and the row is also the
-- only place the answer to "why did that terminal never appear" is written down.
--
-- WHY THE CREDENTIAL IS NOT MINTED AT APPROVAL. Approval records an intention;
-- collection produces the key. A credential minted at approval would have to be
-- STORED in plaintext until the terminal came to fetch it, which is the one
-- thing every other secret in this schema is careful never to do. So APPROVED
-- carries the company, the site and the name, and the key is generated inside
-- the transaction that hands it over -- through the same registerDeviceTx a site
-- key or a claim code would use.

BEGIN;

CREATE TABLE IF NOT EXISTS terminal_announcements (
    id        BIGSERIAL PRIMARY KEY,
    public_id UUID NOT NULL DEFAULT gen_random_uuid(),

    -- The serial the device derives from its factory MAC and sends. Not trusted
    -- as an identity here -- it is trusted at COLLECTION, after a human has
    -- confirmed the physical unit -- but constrained to what the firmware can
    -- actually hold so that an impossible serial is refused at the door it came
    -- in through rather than at the one it was going to.
    serial_number VARCHAR(64) NOT NULL,

    -- ---------------------------------------------------------------------
    -- The two secrets. Both hashed, like every other secret in this schema.
    -- ---------------------------------------------------------------------
    --
    -- PAIRING CODE: shown on the terminal's panel, typed by the operator. Eight
    -- characters in two groups of four, from the same alphabet claim codes use
    -- -- Crockford's minus the letters misread off a screen.
    --
    -- ANNOUNCE TOKEN: returned to the device once, presented on every poll. This
    -- is what makes the status endpoint AUTHENTICATED rather than a second
    -- unauthenticated read, and it is why a credential can only ever be
    -- delivered to the caller that created the announcement.
    pairing_code_hash   CHAR(64) NOT NULL,
    pairing_code_prefix VARCHAR(8) NOT NULL,

    announce_token_hash   CHAR(64) NOT NULL,
    announce_token_prefix VARCHAR(12) NOT NULL,

    -- The lifecycle. See database/announcements.go for the transitions; the
    -- CHECK constraints below are the same rules expressed where they cannot be
    -- bypassed by a code path that forgot one.
    state VARCHAR(16) NOT NULL DEFAULT 'PENDING',

    -- NULL until an operator adopts. An announcement with no company is visible
    -- to NOBODY: there is no route that lists un-adopted rows, which is what
    -- keeps one customer's unclaimed hardware invisible to every other.
    company_id       BIGINT REFERENCES companies(id) ON DELETE RESTRICT,
    adopted_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    adopted_by_email VARCHAR(255),
    adopted_at       TIMESTAMPTZ,

    -- NULL until approved. RESTRICT rather than CASCADE for the same reason the
    -- claim code table uses it: this is a security record, and it must not
    -- vanish because somebody removed the site it names.
    site_id           BIGINT REFERENCES sites(id) ON DELETE RESTRICT,
    device_name       VARCHAR(100),
    approved_by       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    approved_by_email VARCHAR(255),
    approved_at       TIMESTAMPTZ,

    -- A separate, longer window than expires_at. An approval must survive a
    -- terminal that is being physically mounted, or that lost its network for an
    -- hour; the fifteen minutes that bound a code somebody is reading off a
    -- screen would strand it.
    approval_expires_at TIMESTAMPTZ,

    rejected_by_email VARCHAR(255),
    rejected_at       TIMESTAMPTZ,
    rejected_reason   VARCHAR(200),

    -- The device row the credential was finally issued into.
    --
    -- RESTRICT, on the same reasoning site_id above is RESTRICT and not for
    -- symmetry: this is a security record and it must not lose the answer to
    -- "which terminal did that key go into" because somebody removed a row.
    --
    -- SET NULL WAS ALSO UNREACHABLE, which is what made this worth fixing
    -- rather than merely worth arguing about. The referential action is an
    -- UPDATE setting device_id to NULL, and terminal_announcements_collection_
    -- check below forbids exactly that on a COLLECTED row -- so a DELETE on
    -- devices failed with a CHECK VIOLATION naming neither the device nor the
    -- foreign key. The column behaved as RESTRICT already; all that SET NULL
    -- bought was an error message nobody could act on.
    --
    -- Reachable only by hand today: devices are soft-deleted everywhere in the
    -- Go, so nothing in the application performs this DELETE. That is why this
    -- is a correction to an unapplied migration rather than an incident.
    device_id    BIGINT REFERENCES devices(id) ON DELETE RESTRICT,
    collected_at TIMESTAMPTZ,

    -- Corroboration for the human doing the approving. "Seen from 81.2.x.x four
    -- seconds ago" is what distinguishes the unit in front of them from a code
    -- somebody read to them over the phone.
    first_seen_ip  VARCHAR(45),
    last_seen_ip   VARCHAR(45),
    last_seen_at   TIMESTAMPTZ,
    announce_count INTEGER NOT NULL DEFAULT 1,

    -- Reported by the device, shown before approval. Never trusted for anything.
    firmware_version  VARCHAR(50),
    hardware_revision VARCHAR(50),

    -- WHAT THE UNIT SAYS IT CAN DO, in the same shape devices.capabilities uses
    -- (025). Reported on the announce so an administrator can see it BEFORE
    -- they approve -- which is the moment somebody is deciding whether to mount
    -- this hardware on a door, and the moment "it cannot be recovered over the
    -- network" is cheapest to learn.
    --
    -- NULL, [] AND [...] ARE THREE DIFFERENT ANSWERS, exactly as on devices:
    -- never reported, reports and has none, reports and has these. A console
    -- that collapsed the first two would be claiming a terminal cannot do
    -- something when all it knows is that nobody asked.
    --
    -- NEVER TRUSTED, like everything else a device says here. It is shown to a
    -- human before an approval and gates nothing on its own -- the gate is on
    -- devices.capabilities, which is written by an AUTHENTICATED heartbeat.
    capabilities JSONB,

    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT terminal_announcements_state_check
        CHECK (state IN ('PENDING', 'ADOPTED', 'APPROVED', 'COLLECTED',
                         'REJECTED', 'EXPIRED', 'SUPERSEDED')),

    -- An ARRAY or nothing, matching devices_capabilities_check. A scalar here
    -- would be a client bug, and it would surface where the console renders it
    -- rather than where it was written.
    CONSTRAINT terminal_announcements_capabilities_check
        CHECK (capabilities IS NULL OR jsonb_typeof(capabilities) = 'array'),

    CONSTRAINT terminal_announcements_pairing_hash_check
        CHECK (pairing_code_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT terminal_announcements_token_hash_check
        CHECK (announce_token_hash ~ '^[0-9a-f]{64}$'),

    -- The serial the firmware can actually present. device_identity.h sizes it
    -- at char[16], and the character set is the one deviceSerialIsWellFormed
    -- accepts.
    CONSTRAINT terminal_announcements_serial_check
        CHECK (serial_number ~ '^[A-Za-z0-9_-]{1,15}$'),

    -- THE TENANCY INVARIANT OF THIS TABLE: a row that any company can see has a
    -- company, and a row with no company is visible to nobody. PENDING is the
    -- un-adopted state and must have none; everything from ADOPTED onwards keeps
    -- the company that took responsibility for the unit.
    --
    -- EXPIRED is permitted on BOTH sides, because a row can run out of time
    -- before it is adopted or after it -- and an adopted row that expires must
    -- keep naming the company whose operator is owed the explanation.
    -- SUPERSEDED only ever happens to a PENDING row (a unit re-announcing), so
    -- it stays on the company-less side.
    CONSTRAINT terminal_announcements_company_state_check
        CHECK (
            (state IN ('PENDING', 'SUPERSEDED') AND company_id IS NULL)
            OR (state IN ('ADOPTED', 'APPROVED', 'COLLECTED', 'REJECTED')
                AND company_id IS NOT NULL)
            OR state = 'EXPIRED'
        ),

    -- Approval names a site and a moment, or it did not happen.
    CONSTRAINT terminal_announcements_approval_check
        CHECK (
            state NOT IN ('APPROVED', 'COLLECTED')
            OR (site_id IS NOT NULL AND approved_at IS NOT NULL
                AND approval_expires_at IS NOT NULL)
        ),

    -- Collection names the device row the credential went into. "Issued, but we
    -- cannot say to what" is not an answer an audit can accept -- the same rule
    -- 019 applies to a redeemed claim code.
    CONSTRAINT terminal_announcements_collection_check
        CHECK (
            (state = 'COLLECTED') = (device_id IS NOT NULL AND collected_at IS NOT NULL)
        ),

    -- Approval cannot precede adoption. The ordering is the authorization story:
    -- a company took responsibility for this unit before a site was chosen for
    -- it.
    CONSTRAINT terminal_announcements_order_check
        CHECK (approved_at IS NULL OR adopted_at IS NOT NULL),

    CONSTRAINT terminal_announcements_rejection_check
        CHECK ((state = 'REJECTED') = (rejected_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS terminal_announcements_public_id_key
    ON terminal_announcements(public_id);

-- THE LIVE STATES, named once here and repeated in every partial index below.
-- PENDING, ADOPTED and APPROVED are the rows that can still do something;
-- everything else is history.

-- The adopt lookup: by pairing code hash, globally unique among live rows so a
-- typed code resolves WITHOUT the operator naming anything. That is what makes
-- the console screen a single field.
--
-- Uniqueness is scoped to PENDING and ADOPTED rather than to all live states: an
-- already-approved row cannot be adopted again, so its code is spent and must
-- not keep a slot reserved out of the 30^8 space.
CREATE UNIQUE INDEX IF NOT EXISTS terminal_announcements_pairing_key
    ON terminal_announcements(pairing_code_hash)
    WHERE state IN ('PENDING', 'ADOPTED');

-- The device's own lookup, on every poll.
--
-- UNIQUE OVER EVERY ROW, not only the live ones, and the difference matters. A
-- terminal whose announcement was rejected or expired has to be TOLD so, or it
-- waits for ever on an approval that is never coming; a partial index would make
-- its token unresolvable the moment the row left the live states, and "unknown
-- token" and "your announcement was refused" are different instructions.
--
-- Unqualified uniqueness is safe here in a way it would not be for the pairing
-- code: the token is 32 bytes from crypto/rand, so a collision across the whole
-- history of the table is not a thing that happens. The pairing code is 39 bits
-- and WILL repeat across history, which is exactly why its index is partial.
CREATE UNIQUE INDEX IF NOT EXISTS terminal_announcements_token_key
    ON terminal_announcements(announce_token_hash);

-- AT MOST ONE LIVE ANNOUNCEMENT PER SERIAL. Two live rows for one unit would
-- mean two valid pairing codes for the same hardware, so an operator could adopt
-- one while the terminal was displaying the other -- and the terminal would wait
-- for ever on an approval attached to a row nobody was going to act on.
--
-- Re-announcing supersedes a PENDING row. It deliberately does NOT supersede an
-- ADOPTED or APPROVED one: a reboot must not destroy an operator's in-flight
-- adoption.
CREATE UNIQUE INDEX IF NOT EXISTS terminal_announcements_live_serial_key
    ON terminal_announcements(serial_number)
    WHERE state IN ('PENDING', 'ADOPTED', 'APPROVED');

-- The console's pending list.
CREATE INDEX IF NOT EXISTS idx_terminal_announcements_company
    ON terminal_announcements(company_id, state, created_at DESC);

-- The expiry sweep. Both windows, because APPROVED expires on a different clock
-- from PENDING and a single index would not serve the second.
CREATE INDEX IF NOT EXISTS idx_terminal_announcements_expiry
    ON terminal_announcements(expires_at)
    WHERE state IN ('PENDING', 'ADOPTED');
CREATE INDEX IF NOT EXISTS idx_terminal_announcements_approval_expiry
    ON terminal_announcements(approval_expires_at)
    WHERE state = 'APPROVED';

-- "Has this serial been through here before, and how did it end" -- the question
-- a support call about a terminal that will not come up turns into.
CREATE INDEX IF NOT EXISTS idx_terminal_announcements_serial
    ON terminal_announcements(serial_number, created_at DESC);

COMMENT ON TABLE terminal_announcements IS
    'A terminal introducing itself to the cloud and waiting for an authenticated '
    'operator to adopt and approve it (022_terminal_announcements.sql). The '
    'pairing code is displayed on the unit and typed into the console; the '
    'announce token is held by the device. Both are stored hashed, and the '
    'device credential is minted at COLLECTION rather than at approval so that '
    'no plaintext key is ever stored.';

COMMIT;
