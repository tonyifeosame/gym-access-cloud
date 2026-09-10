package database

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// The scoped transaction wrapper for public API requests.
//
// ---------------------------------------------------------------------------
// WHY A TRANSACTION IS MANDATORY HERE
// ---------------------------------------------------------------------------
//
// Two per-request settings have to be applied, and both are only expressible
// inside a transaction:
//
//	statement_timeout   bounds one expensive query so it cannot occupy the
//	                    single instance. Set globally in the DSN it would apply
//	                    to the console and the device sync path too, which is a
//	                    behaviour change to shipped code that P1 does not make.
//
//	app.company_id      the tenant, for the row-level security policies that
//	                    migration 034 will add. Nothing reads it yet.
//
// SET LOCAL IS A SILENT NO-OP OUTSIDE A TRANSACTION. PostgreSQL emits a warning
// and carries on, so a wrapper that forgot to open one would appear to work,
// would set nothing, and -- once RLS is enforcing -- would leave every query
// running with no tenant set. That is why this opens a transaction
// unconditionally rather than only when it has something to do.
//
// ---------------------------------------------------------------------------
// WHY LOCAL IS THE WHOLE POINT
// ---------------------------------------------------------------------------
//
// database/sql hands out pooled connections and a connection carries session
// state back to the pool with it. `SET` or `SET SESSION` would therefore leak a
// tenant id from one request to the next request that happened to draw the same
// connection -- a cross-tenant defect with no code path you could point at.
//
// SET LOCAL is scoped to the transaction and is reverted on COMMIT or ROLLBACK
// by PostgreSQL itself, so the leak is not merely avoided by convention. There
// is a test that proves it: run a scoped transaction, return the connection to
// the pool, and read the setting back on a fresh query.
//
// NOTHING CALLS THIS YET. The public routes it is for do not exist.

// DefaultPublicStatementTimeout bounds one public API query.
//
// Five seconds. Long enough for any indexed read this API offers over a
// realistic tenant, short enough that a query which is not using an index cannot
// hold a connection while a fleet of terminals waits for one.
const DefaultPublicStatementTimeout = 5 * time.Second

// TenantSessionSetting is the GUC the RLS policies will read.
//
// A custom parameter with a dot in the name, which is what PostgreSQL requires
// for one it does not itself define. Read with
// current_setting('app.company_id', true) -- the second argument makes a missing
// value NULL instead of an error, and NULL fails every policy comparison, so a
// transaction that forgot to set it sees NOTHING rather than everything.
const TenantSessionSetting = "app.company_id"

// ScopedTx is a transaction with the tenant and the timeout already applied.
type ScopedTx struct {
	*sql.Tx
	companyID int64
}

// CompanyID reports the tenant this transaction is scoped to.
func (s *ScopedTx) CompanyID() int64 { return s.companyID }

// BeginScoped opens a transaction bound to one tenant.
//
// The caller must Commit or Rollback. WithTenant below is the form to prefer,
// because it cannot be forgotten.
func BeginScoped(ctx context.Context, companyID int64, timeout time.Duration) (*ScopedTx, error) {
	if companyID <= 0 {
		// A tenant of zero would set the GUC to a value no row matches, which
		// is the safe direction -- but it is a programming error, and failing
		// here names it instead of producing an empty result somebody debugs as
		// missing data.
		return nil, fmt.Errorf("scoped transaction requires a company id")
	}
	if timeout <= 0 {
		timeout = DefaultPublicStatementTimeout
	}

	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("opening scoped transaction: %w", err)
	}

	// SET LOCAL does not accept a bind parameter for its value, so the value is
	// rendered. It is an int64 from the credential row, never a caller-supplied
	// string, and strconv guarantees the rendering -- there is nothing here a
	// caller could influence.
	if _, err := tx.ExecContext(ctx,
		"SET LOCAL "+TenantSessionSetting+" = "+strconv.FormatInt(companyID, 10)); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("scoping transaction to company: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"SET LOCAL statement_timeout = "+strconv.FormatInt(timeout.Milliseconds(), 10)); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("setting statement timeout: %w", err)
	}

	return &ScopedTx{Tx: tx, companyID: companyID}, nil
}

// WithTenant runs fn inside a tenant-scoped transaction.
//
// Commits when fn returns nil and rolls back otherwise, so the settings above
// are reverted on both paths and neither can be forgotten. A panic rolls back
// too and is re-raised: a transaction left open by a panic would hold its
// connection until the pool recycled it.
func WithTenant(ctx context.Context, companyID int64, timeout time.Duration,
	fn func(tx *ScopedTx) error) error {

	tx, err := BeginScoped(ctx, companyID, timeout)
	if err != nil {
		return err
	}

	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing scoped transaction: %w", err)
	}
	committed = true
	return nil
}

// TenantSetting reads the tenant a query would be scoped to.
//
// Returns "" when nothing is set. Exists for the leakage test: after a scoped
// transaction has finished and its connection has gone back to the pool, a fresh
// query must see nothing here.
func TenantSetting(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, error) {
	var value sql.NullString
	if err := q.QueryRowContext(ctx,
		"SELECT current_setting($1, true)", TenantSessionSetting).Scan(&value); err != nil {
		return "", fmt.Errorf("reading tenant setting: %w", err)
	}
	if !value.Valid {
		return "", nil
	}
	return value.String, nil
}
