package database

import (
	"context"
	"database/sql/driver"
	"sync/atomic"

	"github.com/lib/pq"
)

// Statement counting.
//
// A thin wrapper around lib/pq's connector that counts every statement the
// pool sends. It exists for one reason: an N+1 is invisible to a test that
// only looks at the response. A list of twenty operators that makes
// twenty-one round trips answers exactly the same JSON as one that makes two,
// and the only way to hold the line is to count -- so the regression tests in
// query_count_test.go assert that the count does not grow with the result
// size, and /metrics exports the running total for the same reason.
//
// WHAT IS COUNTED. Queries and execs, whether they go through the connection's
// context methods (the normal path) or through a prepared statement (the path
// database/sql falls back to when a driver declines). BEGIN and COMMIT are
// not: they are transaction boundaries, and a test asserting a query count
// wants to see the work, not the framing.
//
// WHAT THIS MUST NOT DO: change how the driver is driven. database/sql
// decides per call whether to use QueryerContext or Prepare based on which
// interfaces the connection implements, so the wrapper implements every
// optional interface pq's conn implements and forwards each. A wrapper that
// dropped QueryerContext would silently turn every query into a prepare plus
// an execute -- two round trips where there was one -- and a performance
// safeguard that halved throughput would be a poor joke.

var statementsTotal atomic.Int64

// StatementCount is the number of statements this process has sent through
// the pool since it started.
func StatementCount() int64 { return statementsTotal.Load() }

// countingConnector wraps pq's connector so every connection it hands out
// counts what it sends.
type countingConnector struct {
	inner *pq.Connector
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn}, nil
}

func (c countingConnector) Driver() driver.Driver { return c.inner.Driver() }

// countingConn forwards every optional interface pq's conn implements.
type countingConn struct {
	driver.Conn
}

var (
	_ driver.QueryerContext     = (*countingConn)(nil)
	_ driver.ExecerContext      = (*countingConn)(nil)
	_ driver.ConnPrepareContext = (*countingConn)(nil)
	_ driver.ConnBeginTx        = (*countingConn)(nil)
	_ driver.Pinger             = (*countingConn)(nil)
	_ driver.SessionResetter    = (*countingConn)(nil)
	_ driver.Validator          = (*countingConn)(nil)
	_ driver.NamedValueChecker  = (*countingConn)(nil)
)

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	statementsTotal.Add(1)
	return q.QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	statementsTotal.Add(1)
	return e.ExecContext(ctx, query, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	p, ok := c.Conn.(driver.ConnPrepareContext)
	if !ok {
		stmt, err := c.Conn.Prepare(query)
		if err != nil {
			return nil, err
		}
		return &countingStmt{Stmt: stmt}, nil
	}
	stmt, err := p.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: stmt}, nil
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: stmt}, nil
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.Conn.Begin() //nolint:staticcheck // the fallback database/sql itself uses
}

func (c *countingConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *countingConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *countingConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *countingConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.Conn.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// countingStmt counts executions of a prepared statement.
type countingStmt struct {
	driver.Stmt
}

var (
	_ driver.StmtQueryContext = (*countingStmt)(nil)
	_ driver.StmtExecContext  = (*countingStmt)(nil)
)

func (s *countingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	statementsTotal.Add(1)
	if q, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	values, err := namedToValues(args)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Query(values) //nolint:staticcheck // the fallback database/sql itself uses
}

func (s *countingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	statementsTotal.Add(1)
	if e, ok := s.Stmt.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	values, err := namedToValues(args)
	if err != nil {
		return nil, err
	}
	return s.Stmt.Exec(values) //nolint:staticcheck // the fallback database/sql itself uses
}

func (s *countingStmt) Query(args []driver.Value) (driver.Rows, error) {
	statementsTotal.Add(1)
	return s.Stmt.Query(args) //nolint:staticcheck
}

func (s *countingStmt) Exec(args []driver.Value) (driver.Result, error) {
	statementsTotal.Add(1)
	return s.Stmt.Exec(args) //nolint:staticcheck
}

func namedToValues(args []driver.NamedValue) ([]driver.Value, error) {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		if arg.Name != "" {
			return nil, driver.ErrSkip
		}
		values[i] = arg.Value
	}
	return values, nil
}
