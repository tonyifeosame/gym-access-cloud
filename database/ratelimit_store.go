package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The shared rate-limit store (migrations/031_api_limits.sql).
//
// ---------------------------------------------------------------------------
// WHY THIS EXISTS
// ---------------------------------------------------------------------------
//
// The limiter in middleware/rate_limit.go keeps its buckets in a Go map. Its own
// comment records the consequence: "with more than one instance the effective
// limit multiplies by the instance count". That is SEC-09, and it is the finding
// this file closes for the credential endpoints.
//
// PostgreSQL rather than Redis. Render offers no shared cache on the plans in
// use, and adding one is infrastructure to operate for a service that makes a
// database round trip on every authenticated request anyway. The arithmetic runs
// in SQL, so a decision costs ONE indexed upsert rather than a read, a
// computation and a write with a race in the middle.
//
// ---------------------------------------------------------------------------
// THE ARITHMETIC, AND WHY IT IS IN THE STATEMENT
// ---------------------------------------------------------------------------
//
// A token bucket refilled CONTINUOUSLY: tokens accrue at perSecond and are
// capped at capacity. A fixed window would let a caller spend a full allowance
// at the end of one window and another at the start of the next, which is twice
// the intended rate at exactly the moment it matters. The in-process limiter
// already makes this choice, so behaviour does not change when the store does.
//
// The refill, the cap, the spend and the refusal are one statement. Two
// instances cannot both read four tokens and both spend one, because neither
// ever reads: the row is updated conditionally and the update reports whether it
// happened. The WHERE on the DO UPDATE is what makes a refusal a no-op rather
// than a negative balance.
//
// now() IS THE DATABASE'S CLOCK, which means both instances refill against the
// same clock. There is no skew to reason about, which is the other thing a
// shared store buys beyond a shared count.

// RateDecision is the outcome of asking for one token.
type RateDecision struct {
	Allowed bool
	// RetryAfter is how long until the next token, and is only meaningful when
	// Allowed is false. Never less than a second at the caller, because a
	// Retry-After of zero invites an immediate retry into the same refusal.
	RetryAfter time.Duration
	// Remaining is the balance after the spend, for the RateLimit-Remaining
	// header a well-behaved client paces itself with.
	Remaining float64
}

// PostgresRateStore is the shared implementation.
//
// Holds no state of its own: every decision is a statement against the pool, so
// two instances of this struct in one process behave exactly as two processes
// do. That is what makes the shared-store test able to prove the property
// without a second operating-system process.
type PostgresRateStore struct {
	db *sql.DB
}

// NewPostgresRateStore builds a store over a pool.
//
// Takes the pool rather than reading the package global, so a test can point one
// store at the same database through a second handle and prove that two
// independently constructed limiters share an allowance.
func NewPostgresRateStore(db *sql.DB) *PostgresRateStore {
	return &PostgresRateStore{db: db}
}

// Allow spends one token for a subject, reporting whether it was available.
//
// capacity is the burst ceiling; perSecond is the refill rate. Both are passed
// per call rather than held on the store, because one store serves several
// classes with different allowances and a store that remembered them would need
// one instance per class.
//
// AN ERROR IS NOT A REFUSAL AND NOT A PERMIT. It is returned, and the caller
// decides -- which for the public API means failing closed with 503, because a
// database that cannot answer this cannot serve the request either.
func (s *PostgresRateStore) Allow(ctx context.Context, subjectType, subjectKey, class string,
	capacity, perSecond float64) (RateDecision, error) {

	if subjectKey == "" {
		// No subject means no bucket. Refusing would put every unidentifiable
		// request in one shared allowance an attacker could exhaust on a
		// customer's behalf; permitting leaves them to the limiter above.
		return RateDecision{Allowed: true, Remaining: capacity}, nil
	}
	if len(subjectKey) > 64 {
		// The column is bounded and a caller must not be able to choose how much
		// of the index it occupies.
		subjectKey = subjectKey[:64]
	}

	var remaining float64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO api_rate_buckets AS b
		    (subject_type, subject_key, class, tokens, last_refill, spent_total)
		VALUES ($1, $2, $3, $4::numeric - 1, CURRENT_TIMESTAMP, 1)
		ON CONFLICT (subject_type, subject_key, class) DO UPDATE
		   SET tokens = LEAST($4::numeric,
		                      b.tokens + EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - b.last_refill))
		                                 * $5::numeric) - 1,
		       last_refill = CURRENT_TIMESTAMP,
		       spent_total = b.spent_total + 1
		 WHERE LEAST($4::numeric,
		             b.tokens + EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - b.last_refill))
		                        * $5::numeric) >= 1
		RETURNING tokens`,
		subjectType, subjectKey, class, capacity, perSecond).Scan(&remaining)

	if err == nil {
		return RateDecision{Allowed: true, Remaining: remaining}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RateDecision{}, fmt.Errorf("rate bucket: %w", err)
	}

	// No row returned means the DO UPDATE's WHERE was false: the bucket is
	// empty. The balance is read to compute a truthful Retry-After. A second
	// statement, but only on the refusal path -- the cost belongs to the caller
	// being refused rather than to every caller.
	retryAfter, err := s.retryAfter(ctx, subjectType, subjectKey, class, capacity, perSecond)
	if err != nil {
		return RateDecision{}, err
	}
	return RateDecision{Allowed: false, RetryAfter: retryAfter}, nil
}

// retryAfter computes how long until one token is available.
func (s *PostgresRateStore) retryAfter(ctx context.Context, subjectType, subjectKey, class string,
	capacity, perSecond float64) (time.Duration, error) {

	if perSecond <= 0 {
		return time.Second, nil
	}

	var tokens float64
	err := s.db.QueryRowContext(ctx, `
		SELECT LEAST($4::numeric,
		             tokens + EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - last_refill)) * $5::numeric)
		  FROM api_rate_buckets
		 WHERE subject_type = $1 AND subject_key = $2 AND class = $3`,
		subjectType, subjectKey, class, capacity, perSecond).Scan(&tokens)
	if errors.Is(err, sql.ErrNoRows) {
		// The row was swept between the two statements. Nothing is owed.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("rate bucket retry-after: %w", err)
	}

	deficit := (1 - tokens) / perSecond
	if deficit < 0 {
		deficit = 0
	}
	return time.Duration(deficit * float64(time.Second)), nil
}

// Reset clears one subject's bucket.
//
// For tests, and for an operator who has had to raise a limit and does not want
// to wait out a bucket that was drained under the old one.
func (s *PostgresRateStore) Reset(ctx context.Context, subjectType, subjectKey, class string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM api_rate_buckets
		 WHERE subject_type = $1 AND subject_key = $2 AND class = $3`,
		subjectType, subjectKey, class)
	if err != nil {
		return fmt.Errorf("resetting rate bucket: %w", err)
	}
	return nil
}

// PruneIdleRateBuckets deletes address buckets nothing has touched recently.
//
// CREDENTIAL AND COMPANY SUBJECTS ARE NOT SWEPT. There is one row per credential
// per class and one per company per class, so those are bounded by the number of
// credentials and companies and do not grow with traffic. An ADDRESS subject
// does grow: an attacker rotating source addresses adds a row per address, which
// is exactly what the in-process limiter's idle sweep existed to bound, and the
// shared store needs the same bound for the same reason.
//
// A swept bucket comes back full, which is the same behaviour the in-process
// limiter has and is safe: the row is only removed after an idle period far
// longer than the window it was enforcing.
func PruneIdleRateBuckets(ctx context.Context, idleFor time.Duration) (int64, error) {
	if idleFor <= 0 {
		return 0, nil
	}

	result, err := DB.ExecContext(ctx, `
		DELETE FROM api_rate_buckets
		 WHERE subject_type = 'address'
		   AND last_refill < CURRENT_TIMESTAMP - ($1 || ' seconds')::interval`,
		int64(idleFor.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("pruning idle rate buckets: %w", err)
	}
	return result.RowsAffected()
}

// PurgeExpiredIdempotencyRecords deletes records past their retention window.
//
// Lives here rather than in idempotency.go because it is the same kind of thing
// as the sweep above and the maintenance scheduler registers them together.
func PurgeExpiredIdempotencyRecords(ctx context.Context) (int64, error) {
	result, err := DB.ExecContext(ctx, `
		DELETE FROM idempotency_records WHERE expires_at < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, fmt.Errorf("purging idempotency records: %w", err)
	}
	return result.RowsAffected()
}

// PruneAPIUsage deletes usage rollup rows older than the retention window.
func PruneAPIUsage(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	result, err := DB.ExecContext(ctx, `
		DELETE FROM api_usage_daily WHERE day < (CURRENT_DATE - $1::int)`, retentionDays)
	if err != nil {
		return 0, fmt.Errorf("pruning API usage: %w", err)
	}
	return result.RowsAffected()
}
