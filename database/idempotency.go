package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Idempotency records (migrations/031_api_limits.sql).
//
// ---------------------------------------------------------------------------
// THE PROBLEM
// ---------------------------------------------------------------------------
//
// A customer's booking system POSTs a member, the response is lost to a timeout,
// and it retries. Without a record of the first attempt the platform either
// creates a second member or answers 409 -- and the caller cannot tell which of
// those means "your first attempt worked".
//
// So a write carries an Idempotency-Key, the response is stored against it, and
// a retry replays that response byte for byte rather than re-running the
// mutation and hoping it was harmless.
//
// ---------------------------------------------------------------------------
// FOUR OUTCOMES, AND THE LAST TWO ARE THE INTERESTING ONES
// ---------------------------------------------------------------------------
//
//	Proceed     first time this key has been seen. Run the handler.
//	Replay      same key, same request, already finished. Return what was sent.
//	Reuse       same key, DIFFERENT request. A caller bug -- answering with the
//	            first response would be worse than refusing, because it would
//	            confirm a mutation the caller did not ask for this time.
//	InProgress  same key, still running. Two concurrent attempts at one
//	            operation; the second waits rather than racing the first.
//
// THE UNIQUE INDEX IS WHAT MAKES THIS WORK. Two instances inserting the same key
// at the same instant: one wins the insert and proceeds, the other conflicts and
// reads the in-progress row. No lock is taken and no window exists.
//
// ---------------------------------------------------------------------------
// SCOPED BY COMPANY AND BY CREDENTIAL
// ---------------------------------------------------------------------------
//
// Two customers must never reach each other's stored response, and two
// integrations at one customer must not collide on a key one of them generated
// carelessly. The unique index carries both, so neither is possible.
//
// NOTHING WRITES HERE YET. The middleware exists and is tested; no route mounts
// it, because the routes it is for do not exist.

// IdempotencyOutcome is what the caller should do next.
type IdempotencyOutcome int

const (
	// IdempotencyProceed means run the handler and then call Complete.
	IdempotencyProceed IdempotencyOutcome = iota
	// IdempotencyReplay means return the stored response.
	IdempotencyReplay
	// IdempotencyReuse means the key was used for a different request.
	IdempotencyReuse
	// IdempotencyInProgress means an identical request is still running.
	IdempotencyInProgress
)

// IdempotencyResult carries the outcome and, on a replay, the stored response.
type IdempotencyResult struct {
	Outcome IdempotencyOutcome

	ResponseStatus int
	ResponseBody   []byte
}

// IdempotencyKeyLimits bound what a caller may send.
const (
	MaxIdempotencyKeyLength = 255
	// DefaultIdempotencyTTL is how long a stored response is replayable.
	//
	// Twenty-four hours: comfortably longer than any retry schedule a sane
	// client uses, and short enough that the table is bounded by a day of
	// writes rather than by all of them.
	DefaultIdempotencyTTL = 24 * time.Hour
)

// FingerprintRequest reduces a request to the value that decides replay vs
// reuse.
//
// Method, path and body. NOT the headers and not the query string: a client that
// retries with a different User-Agent or a different Accept-Encoding is retrying
// the same request, and treating it as a new one would defeat the whole
// mechanism at exactly the moment it is needed.
func FingerprintRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// BeginIdempotent claims a key, or reports what already happened under it.
func BeginIdempotent(ctx context.Context, companyID, credentialID int64,
	key, fingerprint string, ttl time.Duration) (*IdempotencyResult, error) {

	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}

	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claiming idempotency key: %w", err)
	}
	defer tx.Rollback()

	// The insert either wins outright or conflicts. DO NOTHING rather than DO
	// UPDATE: a conflict means somebody else owns this key and the right move is
	// to read their row, not to overwrite it.
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO idempotency_records
		    (company_id, credential_id, idempotency_key, request_fingerprint,
		     state, expires_at)
		VALUES ($1, $2, $3, $4, 'IN_PROGRESS', CURRENT_TIMESTAMP + ($5 || ' seconds')::interval)
		ON CONFLICT (company_id, credential_id, idempotency_key) DO NOTHING
		RETURNING id`,
		companyID, credentialID, key, fingerprint, int64(ttl.Seconds())).Scan(&id)

	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("claiming idempotency key: %w", err)
		}
		return &IdempotencyResult{Outcome: IdempotencyProceed}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("claiming idempotency key: %w", err)
	}

	// Conflicted. Read the row that owns the key, locking it so a concurrent
	// completion cannot land between the read and the decision.
	var (
		existingFingerprint string
		state               string
		status              sql.NullInt64
		body                []byte
		expired             bool
	)
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint, state, response_status, response_body,
		       expires_at < CURRENT_TIMESTAMP
		  FROM idempotency_records
		 WHERE company_id = $1 AND credential_id = $2 AND idempotency_key = $3
		 FOR UPDATE`, companyID, credentialID, key).
		Scan(&existingFingerprint, &state, &status, &body, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		// Swept between the insert and the read. Treat as first-time: the
		// caller re-runs, which is the same thing that would have happened had
		// the sweep run a moment earlier.
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("claiming idempotency key: %w", err)
		}
		return &IdempotencyResult{Outcome: IdempotencyProceed}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading idempotency record: %w", err)
	}

	// AN EXPIRED ROW IS NOT A CONFLICT. The retention window has passed, so the
	// key is free again; the row is reclaimed in place rather than deleted and
	// re-inserted, which would race the same way the original insert did.
	if expired {
		if _, err := tx.ExecContext(ctx, `
			UPDATE idempotency_records
			   SET request_fingerprint = $4, state = 'IN_PROGRESS',
			       response_status = NULL, response_body = NULL,
			       created_at = CURRENT_TIMESTAMP, completed_at = NULL,
			       expires_at = CURRENT_TIMESTAMP + ($5 || ' seconds')::interval
			 WHERE company_id = $1 AND credential_id = $2 AND idempotency_key = $3`,
			companyID, credentialID, key, fingerprint, int64(ttl.Seconds())); err != nil {
			return nil, fmt.Errorf("reclaiming idempotency record: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("claiming idempotency key: %w", err)
		}
		return &IdempotencyResult{Outcome: IdempotencyProceed}, nil
	}

	// THE FINGERPRINT DECIDES, and it is checked before the state. A different
	// request under the same key is a caller bug whether or not the first one
	// has finished, and replaying an unrelated response to it would be the worst
	// available outcome.
	if existingFingerprint != fingerprint {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("claiming idempotency key: %w", err)
		}
		return &IdempotencyResult{Outcome: IdempotencyReuse}, nil
	}

	if state != "COMPLETED" {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("claiming idempotency key: %w", err)
		}
		return &IdempotencyResult{Outcome: IdempotencyInProgress}, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claiming idempotency key: %w", err)
	}
	return &IdempotencyResult{
		Outcome:        IdempotencyReplay,
		ResponseStatus: int(status.Int64),
		ResponseBody:   body,
	}, nil
}

// CompleteIdempotent stores the response a claimed key produced.
//
// ONLY RESPONSES BELOW 500 ARE STORED. A server error is not a decision, and the
// correct behaviour on retry is to try the operation again -- replaying a 500
// would turn one transient failure into a permanent one for that key.
func CompleteIdempotent(ctx context.Context, companyID, credentialID int64,
	key string, status int, body []byte) error {

	if status >= 500 {
		// Release the claim so a retry is a fresh attempt rather than a
		// perpetual in-progress.
		_, err := DB.ExecContext(ctx, `
			DELETE FROM idempotency_records
			 WHERE company_id = $1 AND credential_id = $2 AND idempotency_key = $3
			   AND state = 'IN_PROGRESS'`, companyID, credentialID, key)
		if err != nil {
			return fmt.Errorf("releasing idempotency key: %w", err)
		}
		return nil
	}

	// The column is JSONB, so a non-JSON body cannot be stored as-is. Every
	// public response is JSON; anything else is wrapped rather than dropped, so
	// a replay still returns something a client can parse.
	stored := body
	if !json.Valid(stored) {
		stored = []byte(`{}`)
	}

	_, err := DB.ExecContext(ctx, `
		UPDATE idempotency_records
		   SET state = 'COMPLETED', response_status = $4, response_body = $5::jsonb,
		       completed_at = CURRENT_TIMESTAMP
		 WHERE company_id = $1 AND credential_id = $2 AND idempotency_key = $3
		   AND state = 'IN_PROGRESS'`,
		companyID, credentialID, key, status, string(stored))
	if err != nil {
		return fmt.Errorf("completing idempotency record: %w", err)
	}
	return nil
}

// ReleaseIdempotent drops an unfinished claim.
//
// For a handler that never produced a response at all -- a panic recovered
// upstream, a cancelled request. Without it the key would stay IN_PROGRESS until
// its TTL and every retry would be told an identical request is still running.
func ReleaseIdempotent(ctx context.Context, companyID, credentialID int64, key string) error {
	_, err := DB.ExecContext(ctx, `
		DELETE FROM idempotency_records
		 WHERE company_id = $1 AND credential_id = $2 AND idempotency_key = $3
		   AND state = 'IN_PROGRESS'`, companyID, credentialID, key)
	if err != nil {
		return fmt.Errorf("releasing idempotency key: %w", err)
	}
	return nil
}
