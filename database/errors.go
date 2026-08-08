package database

import (
	"errors"

	"github.com/lib/pq"
)

// PostgreSQL error classification.
//
// Handlers need to tell "the caller asked for something impossible" apart from
// "the database is unwell", because the first is a 4xx the client should not
// retry and the second is a 5xx it should. Without this, a duplicate member id
// and a dead connection both surfaced as 500.
//
// The driver's error codes are matched rather than the message text, since
// message wording is not part of PostgreSQL's compatibility promise.

// SQLSTATE codes, from the PostgreSQL error code appendix
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateCheckViolation      = "23514"
	sqlStateNotNullViolation    = "23502"
)

// IsUniqueViolation reports whether err is a duplicate-key rejection.
func IsUniqueViolation(err error) bool { return hasSQLState(err, sqlStateUniqueViolation) }

// IsForeignKeyViolation reports whether err is a foreign key rejection.
func IsForeignKeyViolation(err error) bool { return hasSQLState(err, sqlStateForeignKeyViolation) }

// IsConstraintViolation reports whether err is any rejection caused by the data
// the caller supplied, rather than by the database being unavailable.
func IsConstraintViolation(err error) bool {
	return hasSQLState(err, sqlStateUniqueViolation) ||
		hasSQLState(err, sqlStateForeignKeyViolation) ||
		hasSQLState(err, sqlStateCheckViolation) ||
		hasSQLState(err, sqlStateNotNullViolation)
}

// hasSQLState unwraps err looking for a driver error carrying the given code
func hasSQLState(err error, code string) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code) == code
	}
	return false
}
