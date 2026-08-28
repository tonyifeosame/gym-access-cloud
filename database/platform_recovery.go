package database

import (
	"database/sql"
	"errors"

	"access-terminal-cloud-api/models"
)

// Recovery of last resort, for a tenant whose administration has locked itself
// out of its own console.
//
// ---------------------------------------------------------------------------
// THE HOLE THIS FILLS
// ---------------------------------------------------------------------------
//
// Self-service signup (handlers/signup.go) creates a company, its first site and
// exactly ONE account -- an OWNER. That is the right shape for somebody trying
// the product, and it leaves them with no recovery whatsoever:
//
//	POST /auth/forgot-password   mints a reset token and has nowhere to send it.
//	                             This platform has no transactional email, so the
//	                             token reaches the operational log and stops.
//	POST /console/operators/:id/reset
//	                             needs a SECOND administrator in the company. A
//	                             company of one has none.
//	POST /platform/.../operators is onboarding only, refused into a company that
//	                             already has an operator.
//
// So the single most common new customer on the installation -- one owner, one
// site, no colleagues -- had no route back into their own account that did not
// involve somebody reading a token out of a production log by hand. That is not
// a recovery path; it is the absence of one, wearing a support process.
//
// ---------------------------------------------------------------------------
// WHY THE PREDICATE IS THE WHOLE DESIGN
// ---------------------------------------------------------------------------
//
// The obvious fix -- let the platform surface reset any operator -- is a
// standing back door into every customer on the installation, and it is exactly
// what CreateFirstOperatorForCompany refuses to be. A vendor credential that
// could take over any owner account at any time is the most valuable secret
// here, and "we only use it when asked" is a policy rather than a boundary.
//
// So this is bounded by a query predicate, not by intent: it resolves an
// operator ONLY when that operator is the company's sole administrator -- the
// one case where nobody inside the company can do it instead. The moment a
// customer has a second OWNER or ADMIN, this returns ErrCompanyHasOtherAdmins
// and the vendor's reach ends; recovery is then the customer's own console,
// where the role matrix applies and the trail names one of their own people.
//
// MANAGER AND VIEWER ARE NOT ADMINISTRATORS and are deliberately not counted.
// An owner whose only colleague is a viewer is just as stranded as one with no
// colleague at all: POST /console/operators/:id/reset is ADMIN-gated, so a
// viewer cannot issue anything. Counting them would refuse recovery to somebody
// who genuinely has none.
//
// AND IT RESOLVES, IT DOES NOT RESET. This returns the account; minting the
// token is IssueCredentialToken, the same call the console's own administrative
// reset makes. There is no second token mechanism, no second expiry sweep and no
// second place for the single-use guarantee to be got wrong.

var (
	// ErrCompanyHasOtherAdmins refuses recovery for a company that can recover
	// itself. Not a failure -- the customer's own console is the correct route,
	// and this is the boundary that keeps the platform surface out of healthy
	// tenants.
	ErrCompanyHasOtherAdmins = errors.New(
		"company has more than one administrator, so it can issue the reset itself")

	// ErrCompanyHasNoAdmin covers a company with no OWNER or ADMIN at all.
	//
	// Resetting a manager or a viewer would hand back an account that still
	// cannot administer anything, so it would look like a recovery and fix
	// nothing. A company in this state has either never been onboarded -- in
	// which case the first-operator route applies -- or has had its last
	// administrator removed, which needs a person to look at it.
	ErrCompanyHasNoAdmin = errors.New("company has no administrator to recover")
)

// SoleAdministratorForRecovery resolves the one account a company's recovery
// could possibly be about, or explains why there is not exactly one.
//
// ACTIVE, NON-DELETED ACCOUNTS ONLY, matching FindOperatorForReset: a suspended
// account is not a route back in and reactivating it is a different decision
// made by a different person.
//
// The count and the row come from ONE query over the same predicate, so two
// concurrent calls cannot disagree about how many administrators the company
// had -- and the caller cannot resolve a row under one rule and count under
// another.
func SoleAdministratorForRecovery(companyPublicID string) (*models.User, error) {
	if !looksLikeUUID(companyPublicID) {
		return nil, models.ErrCompanyNotFound
	}

	companyID, err := CompanyIDByPublicID(companyPublicID)
	if err != nil {
		return nil, err
	}

	rows, err := DB.Query(`
		SELECT u.id, u.public_id, u.company_id, u.email, u.full_name, u.role,
		       u.active, u.last_login_at, u.created_at, u.updated_at
		  FROM users u
		 WHERE u.company_id = $1
		   AND u.deleted_at IS NULL
		   AND u.active
		   AND u.role IN ($2, $3)
		 ORDER BY u.id
		 LIMIT 2`, companyID, models.RoleOwner, models.RoleAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []models.User
	for rows.Next() {
		var u models.User
		var lastLogin sql.NullTime
		if err := rows.Scan(&u.ID, &u.PublicID, &u.CompanyID, &u.Email, &u.FullName,
			&u.Role, &u.Active, &lastLogin, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		if lastLogin.Valid {
			t := lastLogin.Time
			u.LastLoginAt = &t
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// LIMIT 2 rather than a count: this only ever needs to know "none, exactly
	// one, or more than one", and reading two rows answers all three without
	// loading an administrator list the platform surface has no business holding.
	switch len(found) {
	case 0:
		return nil, ErrCompanyHasNoAdmin
	case 1:
		return &found[0], nil
	default:
		return nil, ErrCompanyHasOtherAdmins
	}
}
