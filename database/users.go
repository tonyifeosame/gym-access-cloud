package database

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"unicode/utf8"

	"access-terminal-cloud-api/models"

	"golang.org/x/crypto/bcrypt"
)

// Operator accounts (migrations/008_operator_accounts.sql).
//
// Passwords are hashed with bcrypt, NOT with the SHA-256 used for device keys
// and session tokens. The distinction is the entropy of the secret, not a
// preference: a device key is 256 bits from crypto/rand and cannot be guessed,
// so it needs a fast exact-match lookup and no KDF. A password is human-chosen,
// so it needs a per-hash salt and a deliberately slow function. Applying the
// device-key reasoning to a password would be the mistake that reasoning warns
// against.
//
// The bcrypt hash never leaves this package. models.User carries no password
// field at all, so no handler can serialise one by accident.

// Lockout policy for repeated failed logins.
//
// Time-bounded and self-healing on purpose. A permanent lock would be a denial
// of service an attacker could trigger on the only OWNER account by failing to
// log in as them; a doubling delay costs an attacker everything and a legitimate
// operator fifteen minutes at worst.
const (
	loginFailureThreshold = 5   // failures before the first lock
	loginLockBaseSeconds  = 60  // first lock duration
	loginLockMaxSeconds   = 900 // ceiling: 15 minutes
)

// bcrypt cost. 12 is roughly 250ms on current hardware -- slow enough to make
// offline cracking expensive, fast enough that a login does not feel broken.
// The floor exists because a cost below 10 is not meaningfully better than no
// KDF, and this is the one knob an operator could quietly turn all the way down.
const (
	defaultBcryptCost = 12
	minBcryptCost     = 10
)

// BcryptCost resolves the configured work factor, clamped to a sane range.
func BcryptCost() int {
	cost := getEnvInt("BCRYPT_COST", defaultBcryptCost)
	if cost < minBcryptCost {
		cost = minBcryptCost
	}
	if cost > bcrypt.MaxCost {
		cost = bcrypt.MaxCost
	}
	return cost
}

// userColumns is the shared projection for operator reads
const userColumns = `id, public_id, company_id, email, full_name, role, active,
	          last_login_at, created_at, updated_at`

// scanUser reads one row built from userColumns
func scanUser(row interface{ Scan(...any) error }) (*models.User, error) {
	var u models.User
	var lastLogin sql.NullTime
	if err := row.Scan(&u.ID, &u.PublicID, &u.CompanyID, &u.Email, &u.FullName,
		&u.Role, &u.Active, &lastLogin, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		u.LastLoginAt = &lastLogin.Time
	}
	return &u, nil
}

// NormalizeEmail puts an address into the form the schema stores.
//
// users_email_check enforces `email = lower(email)`, so normalising here is what
// keeps the partial unique index genuinely case-insensitive: without it,
// Ops@example.com and ops@example.com would be two accounts that a login form
// could not tell apart.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateEmail rejects an address the schema or a login form could not use.
func ValidateEmail(email string) error {
	if email == "" || len(email) > 255 {
		return models.ErrInvalidEmail
	}
	// ParseAddress alone would accept `Ops <ops@example.com>`; requiring the
	// parse to round-trip keeps display names and angle brackets out.
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return models.ErrInvalidEmail
	}
	return nil
}

// ValidatePassword applies the password policy.
//
// The upper bound is bcrypt's, not ours: it hashes at most 72 bytes and ignores
// the rest silently, so a longer passphrase would authenticate on its first 72
// bytes. Rejecting it says so; truncating it would not.
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < models.MinPasswordLength {
		return models.ErrPasswordTooShort
	}
	if len(password) > models.MaxPasswordBytes {
		return models.ErrPasswordTooLong
	}
	return nil
}

// hashPassword produces the stored form of a password
func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost())
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// timingDummyHash is a real bcrypt hash of a random string, compared against
// when no account matches so that an unknown address costs the same as a known
// one. Without it, response time answers "does this address have an account?"
// however carefully the error message is worded.
var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

func timingDummyHash() []byte {
	dummyHashOnce.Do(func() {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			// A failure here costs timing symmetry, not correctness. Falling
			// back to a fixed string keeps the comparison happening at all.
			raw = []byte("bootstrap-timing-placeholder")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(raw)), BcryptCost())
		if err != nil {
			hash = []byte("$2a$12$" + strings.Repeat("x", 53))
		}
		dummyHash = hash
	})
	return dummyHash
}

// CreateUser adds an operator to a company.
//
// The role and both credential fields are validated here rather than being left
// to the CHECK constraints. The constraints do reject bad values, but as a
// database error the handler cannot tell apart from the database being down --
// the same reason DeviceRegistrationRequest validates device_type in Go.
//
// A duplicate address surfaces as a unique violation for the caller to map to
// 409; IsUniqueViolation reports it.
func CreateUser(companyID int64, in models.NewUser) (*models.User, error) {
	email := NormalizeEmail(in.Email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, err
	}

	role := in.Role
	if role == "" {
		role = models.RoleViewer
	}
	if !models.OperatorRoles[role] {
		return nil, models.ErrInvalidRole
	}

	fullName := strings.TrimSpace(in.FullName)
	if fullName == "" {
		return nil, errors.New("full name is required")
	}

	hash, err := hashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	row := DB.QueryRow(`
		INSERT INTO users (company_id, email, full_name, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+userColumns,
		companyID, email, fullName, hash, role)

	return scanUser(row)
}

// CountLiveOperators reports how many operator accounts exist across every
// company. Used by the startup bootstrap, which must only ever act on a system
// that has none.
func CountLiveOperators() (int, error) {
	var count int
	err := DB.QueryRow(`SELECT count(*) FROM users WHERE deleted_at IS NULL`).Scan(&count)
	return count, err
}

// GetUserByPublicID resolves an operator within one company.
//
// Scoped by company_id like every other read: an operator in another tenant is
// reported as not found rather than forbidden, so the API does not confirm that
// an id exists in someone else's account.
func GetUserByPublicID(companyID int64, publicID string) (*models.User, error) {
	if !looksLikeUUID(publicID) {
		return nil, models.ErrUserNotFound
	}

	row := DB.QueryRow(`
		SELECT `+userColumns+`
		  FROM users
		 WHERE public_id = $1 AND company_id = $2 AND deleted_at IS NULL`,
		publicID, companyID)

	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrUserNotFound
	}
	return user, err
}

// ListUsers returns every live operator in a company, newest last.
func ListUsers(companyID int64) ([]models.User, error) {
	rows, err := DB.Query(`
		SELECT `+userColumns+`
		  FROM users
		 WHERE company_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at, id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *user)
	}
	return users, rows.Err()
}

// AuthenticatePassword verifies a login.
//
// Returns models.ErrInvalidCredentials for every reason the caller is not
// entitled to tell apart -- unknown address, wrong password, disabled account,
// disabled or deleted company. A locked account returns *AccountLockedError,
// which is disclosed deliberately: the operator needs to know to wait, and the
// lock is only reachable by someone who already knows the address.
//
// Anything else is a database fault and must be reported as one. Answering a
// failed query with "invalid password" would send an operator hunting for a
// credential problem during an outage -- the same mistake AuthMiddleware avoids
// for site keys.
func AuthenticatePassword(email, password string) (*models.User, error) {
	email = NormalizeEmail(email)

	var (
		id           int64
		storedHash   string
		userActive   bool
		companyOK    bool
		isLocked     bool
		lockSeconds  int
		foundAccount = true
	)

	// Deliberately NOT filtered on active: an inactive account must still cost
	// a bcrypt comparison, or response time distinguishes "disabled" from
	// "does not exist". Soft-deleted rows ARE excluded -- the partial unique
	// index frees their address for reuse, so a deleted row is not the account
	// this address refers to any more.
	//
	// The lock is evaluated in SQL rather than by comparing locked_until against
	// time.Now(). locked_until is TIMESTAMP WITHOUT TIME ZONE holding the
	// DATABASE server's local wall clock; the driver labels it UTC on the way
	// out, so a Go-side comparison is wrong by that server's UTC offset. Doing
	// it here puts both sides of the comparison on the same clock, which is also
	// how every existing lifetime check in this package works.
	err := DB.QueryRow(`
		SELECT u.id, u.password_hash, u.active,
		       (u.locked_until IS NOT NULL AND u.locked_until > CURRENT_TIMESTAMP)
		           AS is_locked,
		       COALESCE(CEIL(EXTRACT(EPOCH FROM (u.locked_until - CURRENT_TIMESTAMP))), 0)::int
		           AS lock_seconds_remaining,
		       (c.active AND c.deleted_at IS NULL) AS company_ok
		  FROM users u
		  JOIN companies c ON c.id = u.company_id
		 WHERE u.email = $1 AND u.deleted_at IS NULL`,
		email).Scan(&id, &storedHash, &userActive, &isLocked, &lockSeconds, &companyOK)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		foundAccount = false
	case err != nil:
		return nil, err
	}

	if foundAccount && isLocked {
		return nil, &models.AccountLockedError{RetryAfterSeconds: lockSeconds}
	}

	// One bcrypt comparison happens on every path, including the unknown-address
	// path, so the cost of a login does not depend on whether the account exists.
	compareAgainst := []byte(storedHash)
	if !foundAccount {
		compareAgainst = timingDummyHash()
	}
	compareErr := bcrypt.CompareHashAndPassword(compareAgainst, []byte(password))

	if !foundAccount {
		return nil, models.ErrInvalidCredentials
	}
	if compareErr != nil {
		if err := registerFailedLogin(id); err != nil {
			return nil, err
		}
		return nil, models.ErrInvalidCredentials
	}

	// The password was right. Whether the account may be used is a separate
	// question, and answering it after the comparison keeps the timing flat.
	if !userActive || !companyOK {
		return nil, models.ErrInvalidCredentials
	}

	row := DB.QueryRow(`
		UPDATE users
		   SET failed_login_count = 0,
		       locked_until = NULL,
		       last_login_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING `+userColumns, id)

	user, err := scanUser(row)
	if err != nil {
		return nil, err
	}

	// Upgrade the stored hash if the configured cost has risen since it was
	// written. Best effort on purpose: refusing a valid login because an
	// optimisation failed would be a worse outcome than a stale cost.
	if cost, costErr := bcrypt.Cost([]byte(storedHash)); costErr == nil && cost < BcryptCost() {
		if rehashed, hashErr := hashPassword(password); hashErr == nil {
			_, _ = DB.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, rehashed, id)
		}
	}

	return user, nil
}

// registerFailedLogin counts a failure and locks the account once the threshold
// is crossed, with the delay doubling per subsequent failure up to the cap.
//
// Computed in SQL so the read and the write are one statement: two concurrent
// failed attempts then increment the same counter rather than both reading the
// old value and writing the same new one.
func registerFailedLogin(userID int64) error {
	_, err := DB.Exec(`
		UPDATE users
		   SET failed_login_count = failed_login_count + 1,
		       locked_until = CASE
		           WHEN failed_login_count + 1 >= $2
		           THEN CURRENT_TIMESTAMP + make_interval(secs => LEAST(
		                    $4::float8,
		                    $3::float8 * power(2, failed_login_count + 1 - $2)))
		           ELSE locked_until
		       END
		 WHERE id = $1`,
		userID, loginFailureThreshold, loginLockBaseSeconds, loginLockMaxSeconds)
	return err
}

// SetUserPassword replaces an operator's password.
//
// Every other session for that operator is revoked in the same transaction.
// Changing a password is what someone does when they believe it is known to
// somebody else, and leaving the sessions that password opened alive would make
// the change cosmetic. keepSessionID survives so the operator doing it is not
// logged out of the tab they are typing in; pass 0 to revoke all of them.
func SetUserPassword(userID int64, newPassword string, keepSessionID int64) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}

	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE users
		   SET password_hash = $1,
		       password_changed_at = CURRENT_TIMESTAMP,
		       failed_login_count = 0,
		       locked_until = NULL
		 WHERE id = $2 AND deleted_at IS NULL`, hash, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return models.ErrUserNotFound
	}

	if err := revokeUserSessions(tx, userID, keepSessionID); err != nil {
		return err
	}

	return tx.Commit()
}

// SetUserActive enables or disables an operator.
//
// Disabling revokes every session in the same transaction. A disabled account
// whose cookie still works is not disabled -- and unlike a device, which is
// re-checked on its next poll, a browser session could otherwise stay open for
// as long as its absolute expiry allows.
func SetUserActive(companyID, userID int64, active bool) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE users SET active = $1
		 WHERE id = $2 AND company_id = $3 AND deleted_at IS NULL`,
		active, userID, companyID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return models.ErrUserNotFound
	}

	if !active {
		if err := revokeUserSessions(tx, userID, 0); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SetUserRole changes an operator's role and revokes their sessions.
//
// The revocation is the point: role is read from the session's user row on
// every request, but a session that was opened as an OWNER and is now a VIEWER
// is a confusing half-state to leave someone in, and re-authenticating makes
// the new role unambiguous from the first request.
func SetUserRole(companyID, userID int64, role string) error {
	if !models.OperatorRoles[role] {
		return models.ErrInvalidRole
	}

	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE users SET role = $1
		 WHERE id = $2 AND company_id = $3 AND deleted_at IS NULL`,
		role, userID, companyID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return models.ErrUserNotFound
	}

	if err := revokeUserSessions(tx, userID, 0); err != nil {
		return err
	}

	return tx.Commit()
}

// SoftDeleteUser retires an operator, freeing their email address for reuse and
// revoking every session they hold.
func SoftDeleteUser(companyID, userID int64) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE users
		   SET deleted_at = CURRENT_TIMESTAMP, active = FALSE
		 WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL`,
		userID, companyID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return models.ErrUserNotFound
	}

	if err := revokeUserSessions(tx, userID, 0); err != nil {
		return err
	}

	return tx.Commit()
}

// ListSiteGrants returns the sites an operator has been granted, resolved for
// display.
//
// An EMPTY result means "every site in the company", which is what OWNER and
// ADMIN always have. Absence is the default rather than a wildcard row so that
// adding a site to a company does not require touching every account.
//
// Soft-deleted sites are filtered out here, which is why the grant rows
// themselves do not need to be cleaned up when a site is retired.
func ListSiteGrants(userID int64) ([]models.SiteGrant, error) {
	rows, err := DB.Query(`
		SELECT s.id, s.public_id, s.site_name
		  FROM user_site_grants g
		  JOIN sites s ON s.id = g.site_id
		 WHERE g.user_id = $1 AND s.deleted_at IS NULL
		 ORDER BY s.site_name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []models.SiteGrant
	for rows.Next() {
		var g models.SiteGrant
		if err := rows.Scan(&g.SiteID, &g.SitePublicID, &g.SiteName); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// ReplaceSiteGrants sets an operator's site grants to exactly the given sites.
//
// Every site is resolved within the operator's own company before anything is
// written, so a grant can never point across a tenant boundary -- that check is
// the whole reason this takes public ids and a companyID rather than raw site
// ids. An unknown or foreign site fails the call rather than being skipped:
// silently granting three of the four sites asked for is worse than refusing.
func ReplaceSiteGrants(companyID, userID int64, sitePublicIDs []string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Confirm the operator is in this company before granting them anything.
	var exists bool
	err = tx.QueryRow(`
		SELECT EXISTS (SELECT 1 FROM users
		                WHERE id = $1 AND company_id = $2 AND deleted_at IS NULL)`,
		userID, companyID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return models.ErrUserNotFound
	}

	if _, err := tx.Exec(`DELETE FROM user_site_grants WHERE user_id = $1`, userID); err != nil {
		return err
	}

	for _, publicID := range sitePublicIDs {
		if !looksLikeUUID(publicID) {
			return models.ErrSiteNotFound
		}

		var siteID int64
		err := tx.QueryRow(`
			SELECT id FROM sites
			 WHERE public_id = $1 AND company_id = $2 AND deleted_at IS NULL`,
			publicID, companyID).Scan(&siteID)
		if errors.Is(err, sql.ErrNoRows) {
			return models.ErrSiteNotFound
		}
		if err != nil {
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO user_site_grants (user_id, site_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, userID, siteID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SiteAccess is what the authorization layer needs to know about one operator
// and one site, resolved in a single round trip.
//
// GrantCount counts EVERY grant row, including grants to retired sites --
// unlike ListSiteGrants, which filters them out for display. The asymmetry is
// deliberate and the safe direction: grant count answers "is this operator
// scoped at all", and an operator granted one site that is later retired must
// stay scoped to nothing rather than silently widen to the whole company.
type SiteAccess struct {
	SiteID     int64
	SiteName   string
	Granted    bool
	GrantCount int
}

// ResolveSiteAccess looks up a site inside one company and reports whether an
// operator holds a grant to it.
//
// The company filter is the load-bearing part. Whether a role is allowed to
// bypass grants is a policy question answered in the middleware, but NO role
// may reach a site belonging to another tenant -- so the site is resolved
// company-scoped here rather than being looked up globally and checked
// afterwards. A site in another company is reported as ErrSiteNotFound, the same
// way every other cross-tenant read is, so the API does not confirm that an id
// exists elsewhere.
//
// This decides nothing on its own; it only reports facts.
func ResolveSiteAccess(companyID, userID int64, sitePublicID string) (*SiteAccess, error) {
	if !looksLikeUUID(sitePublicID) {
		return nil, models.ErrSiteNotFound
	}

	var access SiteAccess
	err := DB.QueryRow(`
		SELECT s.id,
		       s.site_name,
		       EXISTS (SELECT 1 FROM user_site_grants g
		                WHERE g.user_id = $3 AND g.site_id = s.id) AS granted,
		       (SELECT count(*) FROM user_site_grants g2
		         WHERE g2.user_id = $3) AS grant_count
		  FROM sites s
		 WHERE s.public_id = $1
		   AND s.company_id = $2
		   AND s.deleted_at IS NULL`,
		sitePublicID, companyID, userID).
		Scan(&access.SiteID, &access.SiteName, &access.Granted, &access.GrantCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrSiteNotFound
	}
	if err != nil {
		return nil, err
	}
	return &access, nil
}

// looksLikeUUID reports whether s is shaped like the UUIDs public_id holds.
//
// Checked in Go because the column is typed `uuid`: PostgreSQL raises a syntax
// error on a malformed value, which a handler cannot distinguish from the
// database being unwell. A caller passing junk in a URL should get "not found",
// not a 500.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
