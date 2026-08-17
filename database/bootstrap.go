package database

import (
	"database/sql"
	"errors"

	"access-terminal-cloud-api/models"
)

// First-operator bootstrap.
//
// A database built from migrations/ has no operator accounts and therefore no
// way to reach the console -- deliberately, so that a fresh deployment ships
// with no credentials rather than with known ones. Something has to create the
// first account, and on a managed platform there is no dependable shell to do it
// from.
//
// The rule that makes an environment-driven bootstrap safe is that it may only
// ever act on an EMPTY system. It cannot overwrite an account, cannot re-enable
// one, cannot reset a password and cannot change a role. If a single live
// operator exists, this does nothing at all -- which is what stops a stale or
// mistyped variable from mattering on an established deployment, and what stops
// the variables from being a permanent back door into a running system.

// bootstrapAdvisoryLock serialises the check-then-create across processes.
//
// Two instances starting at once -- a rolling deploy, a restart that overlaps,
// two containers scheduled together -- would otherwise both read "zero
// operators" and both insert. The unique index on email would reject the second
// insert, so the failure would be safe but noisy: one instance would exit on a
// duplicate key. Serialising makes the second instance see the first's committed
// row and skip cleanly instead.
//
// A transaction-scoped lock, so it is released on commit OR rollback and cannot
// be leaked by a process that dies holding it.
const bootstrapAdvisoryLock int64 = 8142026001

// CreateFirstOperator creates the initial OWNER, and only on an empty system.
//
// Returns models.ErrOperatorsExist -- not an error the caller should treat as a
// failure -- when any live operator already exists. Returns
// models.ErrCompanyNotFound when the requested company slug does not resolve, or
// when the installation somehow has no company at all.
//
// companySlug may be empty, in which case the OWNER joins the oldest company.
// That is the same "adopted by the oldest company" convention
// 007_firmware_tenancy.sql used, and migration 002 guarantees one exists.
// A slug that is given and does not match is an error rather than a silent
// fallback: inventing a tenant, or quietly putting the owner in the wrong one,
// is worse than refusing to start.
func CreateFirstOperator(companySlug string, in models.NewUser) (*models.User, error) {
	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Everything below happens under the lock: the count, the company lookup and
	// the insert. Checking outside it would leave exactly the race this exists
	// to close.
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, bootstrapAdvisoryLock); err != nil {
		return nil, err
	}

	var liveOperators int
	if err := tx.QueryRow(
		`SELECT count(*) FROM users WHERE deleted_at IS NULL`).Scan(&liveOperators); err != nil {
		return nil, err
	}
	if liveOperators > 0 {
		return nil, models.ErrOperatorsExist
	}

	var companyID int64
	if companySlug == "" {
		err = tx.QueryRow(`
			SELECT id FROM companies
			 WHERE deleted_at IS NULL
			 ORDER BY id
			 LIMIT 1`).Scan(&companyID)
	} else {
		err = tx.QueryRow(`
			SELECT id FROM companies
			 WHERE slug = $1 AND deleted_at IS NULL`, companySlug).Scan(&companyID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrCompanyNotFound
	}
	if err != nil {
		return nil, err
	}

	// Role is forced rather than taken from the caller. The first account has to
	// be able to create the others, and there is nobody to grant it anything.
	in.Role = models.RoleOwner

	user, err := createUser(tx, companyID, in)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return user, nil
}
