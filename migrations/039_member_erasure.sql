-- 039_member_erasure.sql
--
-- Erasure instead of soft deletion: what it takes for the schema to let a
-- deleted member, or a deleted operator, actually stop existing.
--
-- ---------------------------------------------------------------------------
-- WHY THE SCHEMA HAS TO CHANGE AT ALL
-- ---------------------------------------------------------------------------
--
-- events and audit_events refuse every UPDATE (013). Their references to a
-- person, a credential and an operator are ON DELETE SET NULL -- and SET NULL
-- is an UPDATE. So a hard delete of any of those rows fails while a single
-- event or audit record still points at it, and until now nothing could make
-- one stop pointing. Soft deletion was not only a choice; it was the only
-- thing the schema allowed.
--
-- The exception added here is as narrow as the purge's. Named functions set a
-- transaction-local flag; while it is set the trigger admits an UPDATE that
-- changes ONLY identity columns, and for events only towards NULL. A row's
-- facts -- what happened, when, where, with what decision -- cannot be edited
-- by this route, and neither table can be touched by it without the flag.
--
-- ---------------------------------------------------------------------------
-- THE DELETED-SUBJECT LEDGER
-- ---------------------------------------------------------------------------
--
-- A deleted member's number keeps arriving after the deletion: an offline
-- terminal uploads door events it queued before it heard about the deletion,
-- and a restored backup brings the whole row back. The ledger lets the
-- platform recognise both WITHOUT keeping the number: it holds a keyed hash of
-- (company, member number), never the number itself. See
-- database/deletion.go for the key and for how long an entry lives.

BEGIN;

-- ---------------------------------------------------------------------------
-- The immutability trigger, with the anonymisation exception
-- ---------------------------------------------------------------------------

CREATE OR REPLACE FUNCTION reject_history_mutation()
RETURNS TRIGGER AS $$
DECLARE
    unchanged BOOLEAN;
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('accesslink.purging', true) = 'on' THEN
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND current_setting('accesslink.anonymizing', true) = 'on' THEN
        IF TG_TABLE_NAME = 'events' THEN
            -- Identity may only be REMOVED: every allowed column ends NULL or
            -- unchanged, and nothing else moves.
            unchanged := (to_jsonb(NEW) - ARRAY['person_id', 'credential_id', 'subject_external_id'])
                       = (to_jsonb(OLD) - ARRAY['person_id', 'credential_id', 'subject_external_id']);
            IF unchanged
               AND (NEW.person_id IS NULL OR NEW.person_id = OLD.person_id)
               AND (NEW.credential_id IS NULL OR NEW.credential_id = OLD.credential_id)
               AND (NEW.subject_external_id IS NULL
                    OR NEW.subject_external_id = OLD.subject_external_id) THEN
                RETURN NEW;
            END IF;
        ELSIF TG_TABLE_NAME = 'audit_events' THEN
            -- Who acted, and the label and diff of what they acted on. The
            -- action, the time, the target's type and id and the request id
            -- are never editable.
            unchanged := (to_jsonb(NEW) - ARRAY['actor_user_id', 'actor_email', 'ip_address',
                                                'user_agent', 'target_label', 'changes'])
                       = (to_jsonb(OLD) - ARRAY['actor_user_id', 'actor_email', 'ip_address',
                                                'user_agent', 'target_label', 'changes']);
            IF unchanged
               AND (NEW.actor_user_id IS NULL OR NEW.actor_user_id = OLD.actor_user_id)
               AND (NEW.ip_address IS NULL OR NEW.ip_address = OLD.ip_address)
               AND (NEW.user_agent IS NULL OR NEW.user_agent = OLD.user_agent) THEN
                RETURN NEW;
            END IF;
        END IF;
    END IF;

    RAISE EXCEPTION
        '% on % is refused: this is an immutable history table. '
        'Rows are removed only by the retention purge and de-identified only by '
        'the erasure functions.',
        TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

-- A deleted member's door history keeps what happened and loses who it
-- happened to. Matches the person's own rows, rows that name their member
-- number with no person attached (an attempt recorded before the link
-- resolved), and rows that carry one of their credentials.
CREATE OR REPLACE FUNCTION anonymize_person_history(p_company_id BIGINT,
                                                    p_person_id BIGINT,
                                                    p_external_id VARCHAR)
RETURNS BIGINT AS $$
DECLARE
    events_changed BIGINT := 0;
    logs_changed BIGINT := 0;
BEGIN
    PERFORM set_config('accesslink.anonymizing', 'on', true);

    UPDATE events
       SET person_id = NULL, credential_id = NULL, subject_external_id = NULL
     WHERE company_id = p_company_id
       AND (person_id = p_person_id
            OR (person_id IS NULL AND p_external_id IS NOT NULL
                AND subject_external_id = p_external_id)
            OR credential_id IN (SELECT id FROM credentials WHERE person_id = p_person_id));
    GET DIAGNOSTICS events_changed = ROW_COUNT;

    PERFORM set_config('accesslink.anonymizing', 'off', true);

    -- access_logs has no immutability trigger; the same rows lose the same
    -- identity.
    UPDATE access_logs
       SET person_id = NULL, person_external_id = NULL
     WHERE company_id = p_company_id
       AND (person_id = p_person_id
            OR (person_id IS NULL AND p_external_id IS NOT NULL
                AND person_external_id = p_external_id));
    GET DIAGNOSTICS logs_changed = ROW_COUNT;

    RETURN events_changed + logs_changed;
END;
$$ LANGUAGE plpgsql;

-- A deleted member's audit trail keeps every action, its time and the operator
-- who took it. What goes is the member: the label that named them becomes
-- p_label, and their personal fields leave the diff.
CREATE OR REPLACE FUNCTION redact_person_audit(p_company_id BIGINT,
                                               p_external_id VARCHAR,
                                               p_label VARCHAR)
RETURNS BIGINT AS $$
DECLARE
    changed BIGINT := 0;
BEGIN
    PERFORM set_config('accesslink.anonymizing', 'on', true);

    UPDATE audit_events
       SET target_label = p_label,
           changes = CASE WHEN changes IS NULL THEN NULL
                          ELSE changes - ARRAY['full_name', 'name', 'email', 'phone',
                                               'member_id', 'external_id', 'person_name']
                     END
     WHERE company_id = p_company_id
       AND target_type IN ('PERSON', 'PERMISSION', 'CREDENTIAL')
       AND target_label = p_external_id;
    GET DIAGNOSTICS changed = ROW_COUNT;

    PERFORM set_config('accesslink.anonymizing', 'off', true);
    RETURN changed;
END;
$$ LANGUAGE plpgsql;

-- A deleted operator becomes a pseudonym in every audit row they took or were
-- the subject of. The pseudonym is stable per operator, so a reviewer can still
-- see that one account did a sequence of things -- just not whose it was. The
-- actor's address and browser go, and their user id is released so the account
-- row can be deleted.
CREATE OR REPLACE FUNCTION anonymize_audit_actor(p_company_id BIGINT,
                                                 p_user_id BIGINT,
                                                 p_user_public_id UUID,
                                                 p_pseudonym VARCHAR)
RETURNS BIGINT AS $$
DECLARE
    acted BIGINT := 0;
    targeted BIGINT := 0;
BEGIN
    PERFORM set_config('accesslink.anonymizing', 'on', true);

    UPDATE audit_events
       SET actor_user_id = NULL, actor_email = p_pseudonym,
           ip_address = NULL, user_agent = NULL
     WHERE company_id = p_company_id AND actor_user_id = p_user_id;
    GET DIAGNOSTICS acted = ROW_COUNT;

    UPDATE audit_events
       SET target_label = p_pseudonym,
           changes = CASE WHEN changes IS NULL THEN NULL
                          ELSE changes - ARRAY['email', 'full_name', 'name']
                     END
     WHERE company_id = p_company_id
       AND target_type = 'OPERATOR'
       AND target_public_id = p_user_public_id;
    GET DIAGNOSTICS targeted = ROW_COUNT;

    PERFORM set_config('accesslink.anonymizing', 'off', true);
    RETURN acted + targeted;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- Audit retention gets a platform default, as door history did in 038
-- ---------------------------------------------------------------------------

DROP FUNCTION IF EXISTS purge_audit_events(BIGINT);

-- p_default_days applies to companies with no audit window of their own. Below
-- 30 is treated as no default, matching companies_audit_retention_check's floor.
CREATE OR REPLACE FUNCTION purge_audit_events(p_company_id BIGINT DEFAULT NULL,
                                              p_default_days INTEGER DEFAULT NULL)
RETURNS BIGINT AS $$
DECLARE
    removed BIGINT := 0;
    fallback INTEGER := CASE WHEN p_default_days >= 30 THEN p_default_days END;
BEGIN
    PERFORM set_config('accesslink.purging', 'on', true);

    WITH expired AS (
        SELECT a.id
          FROM audit_events a
          JOIN companies c ON c.id = a.company_id
         WHERE COALESCE(c.audit_retention_days, fallback) IS NOT NULL
           AND (p_company_id IS NULL OR c.id = p_company_id)
           AND a.occurred_at < CURRENT_TIMESTAMP
                               - make_interval(days => COALESCE(c.audit_retention_days, fallback))
    )
    DELETE FROM audit_events WHERE id IN (SELECT id FROM expired);

    GET DIAGNOSTICS removed = ROW_COUNT;
    PERFORM set_config('accesslink.purging', 'off', true);
    RETURN removed;
END;
$$ LANGUAGE plpgsql;

COMMENT ON COLUMN companies.audit_retention_days IS
    'Days to retain operator audit records. NULL uses the platform default '
    '(AUDIT_RETENTION_DEFAULT_DAYS). Floor of 30 days.';

-- ---------------------------------------------------------------------------
-- The ledger
-- ---------------------------------------------------------------------------

-- The key the ledger's hashes are made with, when the deployment provides none
-- (DELETION_LEDGER_KEY). Generated once, on first use. A key held beside the
-- hashes protects far less than one held outside the database, and the
-- application says so at startup; see database/deletion.go.
CREATE TABLE IF NOT EXISTS deletion_ledger_keys (
    key_id     VARCHAR(16) PRIMARY KEY,
    secret     BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT deletion_ledger_keys_secret_check CHECK (octet_length(secret) >= 32)
);

CREATE TABLE IF NOT EXISTS deleted_subjects (
    id           BIGSERIAL PRIMARY KEY,
    company_id   BIGINT NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    subject_type VARCHAR(20) NOT NULL DEFAULT 'PERSON',

    -- HMAC-SHA256 of the company id and the member number, under key_id.
    subject_hash CHAR(64) NOT NULL,
    key_id       VARCHAR(16) NOT NULL,

    deleted_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- The stripped person row awaiting finalisation. NOT a foreign key: the
    -- point of the row is to outlive that one. Cleared when it is deleted.
    person_id    BIGINT,

    -- Set when every terminal has let go and the row was deleted. An entry is
    -- never removed before this: until then an offline terminal may still be
    -- holding events to upload.
    finalized_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,

    CONSTRAINT deleted_subjects_type_check CHECK (subject_type IN ('PERSON')),
    CONSTRAINT deleted_subjects_hash_check CHECK (subject_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT deleted_subjects_expiry_check
        CHECK (expires_at IS NULL OR finalized_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_deleted_subjects_lookup
    ON deleted_subjects(company_id, subject_hash);
CREATE INDEX IF NOT EXISTS idx_deleted_subjects_pending
    ON deleted_subjects(person_id) WHERE finalized_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_deleted_subjects_expiry
    ON deleted_subjects(expires_at) WHERE expires_at IS NOT NULL;

COMMIT;
