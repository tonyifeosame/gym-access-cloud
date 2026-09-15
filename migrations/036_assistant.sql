-- 036_assistant.sql
--
-- The in-console assistant (Phase 1): conversations, their transcript, the
-- record of every tool the model asked to run, the confirmations a human gave
-- before a consequential one ran, and a per-company token ledger.
--
-- ---------------------------------------------------------------------------
-- WHAT THE ASSISTANT IS, IN DATA TERMS
-- ---------------------------------------------------------------------------
--
-- Nothing here grants the model any access. Every tool it calls is replayed
-- through the console's own HTTP routes as the signed-in operator, so the
-- authorisation, tenant scoping and audit rows are the existing ones. These
-- tables record what was ASKED and what came of it, and they link each
-- executed tool call to the audit trail through the request id the internal
-- request carried (assistant_tool_calls.request_id = audit_events.request_id).
--
-- THE TRANSCRIPT IS SERVER-SIDE (assistant_messages) so a browser cannot forge
-- history the model is then shown, and so a turn cut off by a dropped stream
-- can be replayed from what was persisted rather than run again.
--
-- CONFIRMATIONS ARE ROWS, NOT JUST TOKENS. A consequential tool -- granting or
-- withdrawing access, starting a fingerprint enrolment -- first records a
-- confirmation with the exact arguments it will run with. The token the
-- browser presents is an HMAC over that row; approval executes the stored
-- arguments (never the browser's), consumes the row, and a second approval
-- is refused. The row outlives the token so the trail shows who approved
-- what, and what was refused or left to expire.
--
-- RETENTION IS ITS OWN SETTING (ASSISTANT_RETENTION_DAYS), shorter than the
-- audit retention: a conversation is working material, the audit row is the
-- record. Purging a conversation cascades to its messages; tool calls and
-- confirmations are kept, with the conversation reference nulled, for as
-- long as the audit trail is.

CREATE TABLE IF NOT EXISTS assistant_conversations (
    id           BIGSERIAL PRIMARY KEY,
    public_id    UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id   BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title        VARCHAR(120) NOT NULL DEFAULT '',
    status       VARCHAR(16) NOT NULL DEFAULT 'OPEN',
    model        VARCHAR(64) NOT NULL,
    prompt_hash  CHAR(64) NOT NULL,
    turn_count   INTEGER NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_message_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT assistant_conversations_status_check
        CHECK (status IN ('OPEN', 'CLOSED', 'EXPIRED'))
);

CREATE UNIQUE INDEX IF NOT EXISTS assistant_conversations_public_id_key
    ON assistant_conversations(public_id);
CREATE INDEX IF NOT EXISTS assistant_conversations_owner_idx
    ON assistant_conversations(company_id, user_id, last_message_at DESC);
-- The retention purge selects by idleness alone; the user cascade by user
-- alone. Neither is a prefix of the owner index.
CREATE INDEX IF NOT EXISTS assistant_conversations_idle_idx
    ON assistant_conversations(last_message_at);
CREATE INDEX IF NOT EXISTS assistant_conversations_user_idx
    ON assistant_conversations(user_id);

-- One row per message in the order the model saw them. `content` is the
-- assistant's own neutral block representation (text / tool_use / tool_result),
-- already projected and redacted -- never a raw API body.
CREATE TABLE IF NOT EXISTS assistant_messages (
    id              BIGSERIAL PRIMARY KEY,
    conversation_id BIGINT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    role            VARCHAR(12) NOT NULL,
    content         JSONB NOT NULL,
    -- The id a client gave its own message, so a re-send after a dropped
    -- stream is answered from what was persisted rather than run again.
    client_message_id VARCHAR(64),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT assistant_messages_role_check CHECK (role IN ('user', 'assistant')),
    CONSTRAINT assistant_messages_seq_key UNIQUE (conversation_id, seq)
);

CREATE UNIQUE INDEX IF NOT EXISTS assistant_messages_client_id_key
    ON assistant_messages(conversation_id, client_message_id)
    WHERE client_message_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS assistant_confirmations (
    id              BIGSERIAL PRIMARY KEY,
    token_id        UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id BIGINT REFERENCES assistant_conversations(id) ON DELETE SET NULL,
    company_id      BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id      BIGINT NOT NULL,
    tool_name       VARCHAR(64) NOT NULL,
    arguments       JSONB NOT NULL,
    arguments_hash  CHAR(64) NOT NULL,
    phrase_required VARCHAR(64),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    outcome         VARCHAR(16),
    CONSTRAINT assistant_confirmations_outcome_check
        CHECK (outcome IS NULL OR outcome IN ('APPROVED', 'REJECTED', 'EXPIRED'))
);

CREATE UNIQUE INDEX IF NOT EXISTS assistant_confirmations_token_id_key
    ON assistant_confirmations(token_id);
CREATE INDEX IF NOT EXISTS assistant_confirmations_company_idx
    ON assistant_confirmations(company_id, issued_at DESC);
-- EVERY REFERENCING COLUMN AN ON DELETE ACTION WALKS IS INDEXED. Purging a
-- conversation sets conversation_id to NULL here and on the tool calls;
-- deleting a user cascades by user_id. Without these, each deleted parent
-- row is a sequential scan of the child table.
CREATE INDEX IF NOT EXISTS assistant_confirmations_conversation_idx
    ON assistant_confirmations(conversation_id);
CREATE INDEX IF NOT EXISTS assistant_confirmations_user_idx
    ON assistant_confirmations(user_id);

CREATE TABLE IF NOT EXISTS assistant_tool_calls (
    id              BIGSERIAL PRIMARY KEY,
    public_id       UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id BIGINT REFERENCES assistant_conversations(id) ON DELETE SET NULL,
    turn_id         UUID NOT NULL,
    company_id      BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id      BIGINT NOT NULL,
    tool_name       VARCHAR(64) NOT NULL,
    arguments       JSONB NOT NULL,
    route           VARCHAR(160) NOT NULL DEFAULT '',
    request_id      VARCHAR(64) NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL,
    http_status     INTEGER,
    confirmation_id BIGINT REFERENCES assistant_confirmations(id) ON DELETE SET NULL,
    result_digest   CHAR(64),
    duration_ms     INTEGER,
    client_ip       VARCHAR(64),
    user_agent      VARCHAR(512),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT assistant_tool_calls_status_check CHECK (status IN (
        'EXECUTED', 'REFUSED_ROLE', 'REFUSED_SCOPE', 'NOT_FOUND', 'INVALID', 'FAILED',
        'CONFIRMATION_REQUESTED', 'CONFIRMED_EXECUTED',
        'CONFIRMATION_EXPIRED', 'CONFIRMATION_REJECTED'))
);

CREATE UNIQUE INDEX IF NOT EXISTS assistant_tool_calls_public_id_key
    ON assistant_tool_calls(public_id);
CREATE INDEX IF NOT EXISTS assistant_tool_calls_company_idx
    ON assistant_tool_calls(company_id, created_at DESC);
CREATE INDEX IF NOT EXISTS assistant_tool_calls_request_idx
    ON assistant_tool_calls(request_id);
CREATE INDEX IF NOT EXISTS assistant_tool_calls_conversation_idx
    ON assistant_tool_calls(conversation_id);
CREATE INDEX IF NOT EXISTS assistant_tool_calls_user_idx
    ON assistant_tool_calls(user_id);
CREATE INDEX IF NOT EXISTS assistant_tool_calls_confirmation_idx
    ON assistant_tool_calls(confirmation_id);

-- One row per company per calendar month. Checked before every model call;
-- updated from the model's own usage figures after it.
CREATE TABLE IF NOT EXISTS assistant_company_usage (
    company_id         BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    period             DATE NOT NULL,
    input_tokens       BIGINT NOT NULL DEFAULT 0,
    output_tokens      BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    turns              INTEGER NOT NULL DEFAULT 0,
    tool_calls         INTEGER NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (company_id, period)
);

-- The assistant's two rate-limit classes -- one for messages, one for
-- confirmations -- join the shared bucket table so the allowance is per
-- session across instances, like the login and adopt limiters. The existing
-- classes are unchanged; the check is widened, not replaced.
ALTER TABLE api_rate_buckets DROP CONSTRAINT IF EXISTS api_rate_buckets_class_check;
ALTER TABLE api_rate_buckets ADD CONSTRAINT api_rate_buckets_class_check
    CHECK (class IN ('read', 'search', 'write', 'webhook_admin',
                     'auth_failure',
                     'login', 'claim', 'platform_login', 'adopt',
                     'assistant', 'assistant_ok'));
