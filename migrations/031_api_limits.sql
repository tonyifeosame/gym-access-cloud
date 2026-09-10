-- 031_api_limits.sql
--
-- Shared rate limiting and idempotency.
--
-- ---------------------------------------------------------------------------
-- WHY THE RATE LIMITER MOVES INTO THE DATABASE
-- ---------------------------------------------------------------------------
--
-- The limiter in middleware/rate_limit.go is an in-process map. That is honest
-- for one instance and silently wrong for two: the effective allowance
-- multiplies by the instance count, and the comment there has said so since it
-- was written. SEC-09 is that finding.
--
-- Postgres rather than Redis, because Render offers no shared cache on the plans
-- in use and adding one is infrastructure to operate. The refill arithmetic runs
-- in SQL in a single statement, so a request costs one indexed upsert. The Go
-- side is behind an interface, so moving to Redis later is a constructor change.
--
-- CONTINUOUS REFILL, NOT A FIXED WINDOW. A fixed window lets a caller spend a
-- full allowance at the end of one window and another at the start of the next,
-- which is twice the intended rate at exactly the moment it matters. The
-- in-process limiter already makes this choice; the shared one makes the same
-- one, so behaviour does not change when the store does.
--
-- ---------------------------------------------------------------------------
-- WHAT IS DELIBERATELY NOT MOVED YET
-- ---------------------------------------------------------------------------
--
-- THE ANNOUNCE LIMITER STAYS IN PROCESS. Its own file argues that resolving a
-- terminal's identity inside a limiter "would mean a database lookup inside a
-- rate limiter -- which is the work the limiter exists to avoid doing", and a
-- twenty-five terminal site polling every five seconds is about three hundred
-- limiter writes a minute before the handler does any work of its own. Login
-- and adopt move; announce does not, and middleware/rate_limit.go records why.
--
-- The consequence is that api_rate_buckets holds credential and company subjects
-- and the two credential-endpoint address subjects, not the announce fleet's.

BEGIN;

-- ---------------------------------------------------------------------------
-- Token buckets
-- ---------------------------------------------------------------------------
--
-- ONE ROW PER (subject, class), holding a token count and the instant it was
-- last refilled. The row is read, refilled and spent in one statement, so two
-- instances cannot both believe they hold the last token.
--
-- subject_key IS TEXT, NOT A BIGINT. The three subject kinds do not share an
-- identifier space: a credential and a company have row ids, and an address has
-- no integer form at all. Hashing an address into a bigint would make the table
-- unreadable during an incident -- "which address is 8391027714472" is not a
-- question anybody should have to answer at 3 a.m. -- for no gain, since the
-- index is on the whole key either way.
CREATE TABLE IF NOT EXISTS api_rate_buckets (
    -- credential | company | address
    subject_type VARCHAR(12) NOT NULL,
    -- Canonical textual identifier for the subject. For credential and company
    -- this is the decimal row id; for address it is the client address exactly
    -- as ClientIP() reports it.
    subject_key  TEXT NOT NULL,
    -- The endpoint class. Public API classes (read, search, write,
    -- webhook_admin, auth_failure) and the credential-endpoint classes the
    -- existing limiters move onto.
    --
    -- login, claim and platform_login are SEPARATE CLASSES rather than one,
    -- because they are three separate allowances today and collapsing them
    -- would be a behaviour change: router.go builds a distinct limiter for each
    -- precisely so that an installer retrying a mistyped claim code cannot
    -- exhaust the allowance an operator needs to sign in and fix it.
    class        VARCHAR(16) NOT NULL,

    -- Fractional, because refill is continuous. NUMERIC rather than double
    -- precision so the arithmetic is exact and two instances computing the same
    -- refill from the same last_refill agree to the last digit.
    tokens      NUMERIC(12,4) NOT NULL,
    last_refill TIMESTAMPTZ NOT NULL,

    -- Monotonic count of tokens actually spent, for the usage rollup. Not used
    -- by the limiter itself.
    spent_total BIGINT NOT NULL DEFAULT 0,

    PRIMARY KEY (subject_type, subject_key, class),

    CONSTRAINT api_rate_buckets_subject_type_check
        CHECK (subject_type IN ('credential', 'company', 'address')),
    CONSTRAINT api_rate_buckets_class_check
        CHECK (class IN ('read', 'search', 'write', 'webhook_admin',
                         'auth_failure',
                         'login', 'claim', 'platform_login', 'adopt')),
    CONSTRAINT api_rate_buckets_tokens_check
        CHECK (tokens >= 0),
    CONSTRAINT api_rate_buckets_subject_key_check
        CHECK (length(btrim(subject_key)) > 0 AND length(subject_key) <= 64)
);

-- NO updated_at AND NO TRIGGER on this table, deliberately. It is written on
-- every rate-limited request and it is the hottest statement in the system; a
-- BEFORE UPDATE trigger would double the cost of it to maintain a column
-- last_refill already carries.

-- The address-subject sweep. Credential and company subjects are bounded by the
-- number of credentials and companies and never grow with traffic, but an
-- attacker rotating source addresses adds a row per address -- so the idle rows
-- are swept the way the in-process limiter sweeps its map.
CREATE INDEX IF NOT EXISTS idx_api_rate_buckets_idle
    ON api_rate_buckets(last_refill)
    WHERE subject_type = 'address';

-- ---------------------------------------------------------------------------
-- Usage rollup
-- ---------------------------------------------------------------------------
--
-- Per-day, per-class counts behind GET /console/api-credentials/{id}/usage. The
-- question it answers is "is this key still in use, and by what", which is what
-- an operator needs before revoking something they no longer recognise.
CREATE TABLE IF NOT EXISTS api_usage_daily (
    credential_id BIGINT NOT NULL REFERENCES api_credentials(id) ON DELETE CASCADE,
    day           DATE NOT NULL,
    class         VARCHAR(16) NOT NULL,
    requests      BIGINT NOT NULL DEFAULT 0,
    refusals      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (credential_id, day, class),

    CONSTRAINT api_usage_daily_counts_check
        CHECK (requests >= 0 AND refusals >= 0)
);

CREATE INDEX IF NOT EXISTS idx_api_usage_daily_day
    ON api_usage_daily(day);

-- ---------------------------------------------------------------------------
-- Idempotency
-- ---------------------------------------------------------------------------
--
-- A retried POST must not create a second member. The record holds the response
-- that was sent, so a replay returns it byte for byte rather than re-running the
-- mutation and hoping it is harmless.
--
-- THE KEY IS SCOPED BY COMPANY AND BY CREDENTIAL. Two integrations at one
-- customer must not be able to collide on a key one of them generated
-- carelessly, and two customers must never be able to reach each other's stored
-- response at all.
--
-- THE FINGERPRINT is what separates a retry from a reuse. Same key and same
-- request is a replay; same key and a different request is a caller bug, and
-- answering it with the first response would be worse than refusing it.
--
-- NOTHING WRITES TO THIS TABLE YET. The middleware exists and is tested; no
-- route mounts it, because the routes it is for do not exist.
CREATE TABLE IF NOT EXISTS idempotency_records (
    id         BIGSERIAL PRIMARY KEY,
    company_id BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    credential_id BIGINT NOT NULL REFERENCES api_credentials(id) ON DELETE CASCADE,

    idempotency_key      VARCHAR(255) NOT NULL,
    -- sha256(method + path + canonical body)
    request_fingerprint  CHAR(64) NOT NULL,

    -- IN_PROGRESS | COMPLETED
    state VARCHAR(12) NOT NULL DEFAULT 'IN_PROGRESS',

    response_status SMALLINT,
    response_body   JSONB,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL,

    CONSTRAINT idempotency_records_state_check
        CHECK (state IN ('IN_PROGRESS', 'COMPLETED')),
    CONSTRAINT idempotency_records_fingerprint_check
        CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    -- A completed record has an answer; an in-progress one does not. Without
    -- this a crashed request could leave a row that replays an empty response.
    CONSTRAINT idempotency_records_completion_check
        CHECK ((state = 'COMPLETED') = (response_status IS NOT NULL)),
    CONSTRAINT idempotency_records_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT idempotency_records_key_check
        CHECK (length(btrim(idempotency_key)) > 0)
);

-- The replay lookup AND the concurrency guard in one index: a second request
-- with the same key cannot insert while the first is in flight, which is what
-- turns a race into a 409 rather than two mutations.
CREATE UNIQUE INDEX IF NOT EXISTS idempotency_records_key
    ON idempotency_records(company_id, credential_id, idempotency_key);

CREATE INDEX IF NOT EXISTS idx_idempotency_records_expiry
    ON idempotency_records(expires_at);

COMMENT ON TABLE api_rate_buckets IS
    'Shared token buckets for rate limiting. One row per (subject, class); '
    'refilled continuously in SQL so the limit holds across instances.';
COMMENT ON TABLE idempotency_records IS
    'Stored responses for Idempotency-Key replay, scoped by company and '
    'credential. 24-hour retention, swept by the maintenance scheduler.';

COMMIT;
