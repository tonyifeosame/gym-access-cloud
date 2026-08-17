package database

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"access-terminal-cloud-api/models"

	"golang.org/x/crypto/bcrypt"
)

// Platform administration (migrations/015_platform_and_applications.sql).
//
// THE THIRD CREDENTIAL CLASS, and the reason there is a third one rather than a
// bigger operator.
//
// The audit's first blocker was that no API creates a company: onboarding a
// second customer required SQL against production. The obvious fix -- let some
// operator role create companies -- is wrong, and 008_operator_accounts.sql
// already explains why:
//
//	ONE COMPANY PER OPERATOR. Every function in database/ takes a companyID as
//	its first argument and every query filters on it; that single-tenant-per-
//	call contract is what makes the tenancy boundary checkable.
//
// That reasoning holds. users.company_id stays NOT NULL, every console query is
// unchanged, and a test asserts the column never becomes nullable. What was
// missing was not a wider operator but a DIFFERENT identity -- and this codebase
// already has the pattern twice over: the site key and the operator session
// share a URL prefix and nothing else, and neither authenticates where the other
// belongs.
//
// WHAT A PLATFORM ADMIN MAY REACH, AND WHAT IT CANNOT.
//
//	May:  create a company, rename it, deactivate it, set its retention policy,
//	      and create the FIRST operator inside one.
//	May NOT: read a company's people, credentials, events, terminals or site
//	      keys. Running a customer's deployment is the customer's job, and a
//	      support identity that can read every tenant's biometric roster is the
//	      single most valuable credential on the installation.
//
// That boundary is enforced by there being no such function in this file and no
// such route, not by a role check that could be got wrong. Everything here
// operates on `companies` and on operator bootstrap; there is nothing else for
// it to reach.

// Platform session lifetimes. SHORTER than an operator's, deliberately: this
// credential can create tenants and mint their first owner, and should not
// survive a forgotten laptop for a week.
const (
	defaultPlatformIdleTimeout     = 2 * time.Hour
	defaultPlatformAbsoluteTimeout = 12 * time.Hour
)

// Platform errors.
var (
	ErrPlatformAdminNotFound = errors.New("platform administrator not found")
	ErrCompanyNameRequired   = errors.New("company name is required")
	ErrCompanySlugInvalid    = errors.New("slug must be 3-64 characters of a-z, 0-9 and hyphen, starting and ending alphanumeric")
	ErrCompanySlugTaken      = errors.New("that slug is already in use")
	ErrCompanyHasOperators   = errors.New("company already has operators")
)

// PlatformIdleTimeout is how long a platform session survives unused.
func PlatformIdleTimeout() time.Duration {
	return getEnvSeconds("PLATFORM_SESSION_IDLE_TIMEOUT_SECONDS", defaultPlatformIdleTimeout)
}

// PlatformAbsoluteTimeout is the longest a platform session may live.
func PlatformAbsoluteTimeout() time.Duration {
	return getEnvSeconds("PLATFORM_SESSION_ABSOLUTE_TIMEOUT_SECONDS", defaultPlatformAbsoluteTimeout)
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// AuthenticatePlatformPassword verifies a platform administrator's credentials.
//
// The SAME construction AuthenticatePassword uses for operators, and for the
// same reasons: one bcrypt comparison is spent on an unknown address so timing
// does not answer a question the error message refuses to, and the lockout is
// time-bounded and self-healing so an attacker cannot permanently lock out the
// only administrator by failing to log in as them.
func AuthenticatePlatformPassword(email, password string) (*models.PlatformAdmin, error) {
	normalized := NormalizeEmail(email)

	var (
		admin       models.PlatformAdmin
		id          int64
		hash        string
		active      bool
		lockedUntil sql.NullTime
		lockSeconds sql.NullInt64
	)

	err := DB.QueryRow(`
		SELECT id, public_id, email, full_name, password_hash, active,
		       locked_until,
		       CASE WHEN locked_until > CURRENT_TIMESTAMP
		            THEN CEIL(EXTRACT(EPOCH FROM (locked_until - CURRENT_TIMESTAMP)))::bigint
		       END
		  FROM platform_admins
		 WHERE email = $1 AND deleted_at IS NULL`, normalized).
		Scan(&id, &admin.ID, &admin.Email, &admin.FullName, &hash, &active,
			&lockedUntil, &lockSeconds)

	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same work an existing account would cost.
		bcrypt.CompareHashAndPassword(timingDummyHash(), []byte(password))
		return nil, models.ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	if lockSeconds.Valid && lockSeconds.Int64 > 0 {
		return nil, &models.AccountLockedError{RetryAfterSeconds: int(lockSeconds.Int64)}
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		if err := recordPlatformLoginFailure(id); err != nil {
			return nil, err
		}
		return nil, models.ErrInvalidCredentials
	}

	// A disabled account is refused AFTER the password check, so the response is
	// indistinguishable from a wrong password. Telling a caller that an address
	// exists but is switched off is an enumeration oracle.
	if !active {
		return nil, models.ErrInvalidCredentials
	}

	if _, err := DB.Exec(`
		UPDATE platform_admins
		   SET failed_login_count = 0, locked_until = NULL,
		       last_login_at = CURRENT_TIMESTAMP
		 WHERE id = $1`, id); err != nil {
		return nil, err
	}

	admin.Active = true
	return &admin, nil
}

// recordPlatformLoginFailure increments the counter and locks with a doubling
// delay once the threshold is crossed.
func recordPlatformLoginFailure(adminID int64) error {
	_, err := DB.Exec(`
		UPDATE platform_admins
		   SET failed_login_count = failed_login_count + 1,
		       locked_until = CASE
		           WHEN failed_login_count + 1 >= $2
		           THEN CURRENT_TIMESTAMP + make_interval(secs => LEAST(
		                   $3 * power(2, failed_login_count + 1 - $2), $4)::float8)
		           ELSE locked_until
		       END
		 WHERE id = $1`,
		adminID, loginFailureThreshold, loginLockBaseSeconds, loginLockMaxSeconds)
	return err
}

// CreatePlatformSession opens a session and returns the plaintext secrets.
//
// The same shape CreateSession uses for operators: a 256-bit token from
// crypto/rand stored only as its SHA-256, and a CSRF token derived from it so
// that /me can return it after a page reload without either being stored
// recoverably.
func CreatePlatformSession(adminID int64, ipAddress, userAgent string) (*models.SessionCredentials, error) {
	token, err := generateSecret(sessionTokenPrefix)
	if err != nil {
		return nil, err
	}
	csrfToken := deriveCSRFToken(token)

	var creds models.SessionCredentials
	creds.Token = token
	creds.CSRFToken = csrfToken

	err = DB.QueryRow(`
		INSERT INTO platform_sessions (admin_id, token_hash, csrf_token_hash,
		                               idle_expires_at, absolute_expires_at,
		                               ip_address, user_agent)
		VALUES ($1, $2, $3,
		        LEAST(CURRENT_TIMESTAMP + make_interval(secs => $4::float8),
		              CURRENT_TIMESTAMP + make_interval(secs => $5::float8)),
		        CURRENT_TIMESTAMP + make_interval(secs => $5::float8),
		        NULLIF($6,''), NULLIF($7,''))
		RETURNING public_id, idle_expires_at, absolute_expires_at,
		          CEIL(EXTRACT(EPOCH FROM (absolute_expires_at - CURRENT_TIMESTAMP)))::int`,
		adminID, hashSessionSecret(token), hashSessionSecret(csrfToken),
		PlatformIdleTimeout().Seconds(), PlatformAbsoluteTimeout().Seconds(),
		truncate(ipAddress, 45), truncate(userAgent, 255)).
		Scan(&creds.PublicID, &creds.IdleExpiresAt, &creds.AbsoluteExpiresAt,
			&creds.ExpiresInSeconds)
	if err != nil {
		return nil, err
	}
	return &creds, nil
}

// AuthenticatePlatformSession resolves the administrator behind a token.
//
// Returns nil for unknown, revoked, expired by either clock, or a disabled
// account -- all answered as 401. An error means the database failed and must be
// a 500: an outage reported as "not logged in" sends people looking for a
// credential problem that does not exist.
func AuthenticatePlatformSession(token string) (*models.PlatformIdentity, error) {
	if token == "" {
		return nil, nil
	}

	var (
		identity   models.PlatformIdentity
		storedHash string
		live       bool
		needsTouch bool
	)

	err := DB.QueryRow(`
		SELECT s.id, s.token_hash, s.csrf_token_hash,
		       CEIL(EXTRACT(EPOCH FROM (s.absolute_expires_at - CURRENT_TIMESTAMP)))::int,
		       (s.revoked_at IS NULL
		        AND s.idle_expires_at > CURRENT_TIMESTAMP
		        AND s.absolute_expires_at > CURRENT_TIMESTAMP) AS live,
		       (s.last_used_at <= CURRENT_TIMESTAMP - make_interval(secs => $2::float8)) AS needs_touch,
		       a.id, a.public_id, a.email, a.full_name, a.active
		  FROM platform_sessions s
		  JOIN platform_admins a ON a.id = s.admin_id
		 WHERE s.token_hash = $1
		   AND a.deleted_at IS NULL`,
		hashSessionSecret(token), sessionTouchInterval.Seconds()).
		Scan(&identity.SessionID, &storedHash, &identity.CSRFTokenHash,
			&identity.ExpiresInSeconds, &live, &needsTouch,
			&identity.AdminID, &identity.AdminPublicID, &identity.Email,
			&identity.FullName, &identity.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashSessionSecret(token))) != 1 {
		return nil, nil
	}
	if !live || !identity.Active {
		return nil, nil
	}

	identity.CSRFToken = deriveCSRFToken(token)

	if needsTouch {
		_, _ = DB.Exec(`
			UPDATE platform_sessions
			   SET last_used_at = CURRENT_TIMESTAMP,
			       idle_expires_at = LEAST(
			           CURRENT_TIMESTAMP + make_interval(secs => $2::float8),
			           absolute_expires_at)
			 WHERE id = $1`, identity.SessionID, PlatformIdleTimeout().Seconds())
	}

	return &identity, nil
}

// RevokePlatformSession ends one session. Idempotent.
func RevokePlatformSession(sessionID int64) error {
	_, err := DB.Exec(`
		UPDATE platform_sessions SET revoked_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND revoked_at IS NULL`, sessionID)
	return err
}

// PurgeExpiredPlatformSessions removes dead platform sessions past the
// retention window, for the maintenance sweep.
func PurgeExpiredPlatformSessions(retentionDays int) (int64, error) {
	if retentionDays < 0 {
		retentionDays = 0
	}
	result, err := DB.Exec(`
		DELETE FROM platform_sessions
		 WHERE (absolute_expires_at < CURRENT_TIMESTAMP - make_interval(days => $1))
		    OR (revoked_at IS NOT NULL
		        AND revoked_at < CURRENT_TIMESTAMP - make_interval(days => $1))`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CreateFirstPlatformAdmin creates the initial administrator, and ONLY on an
// installation that has none.
//
// The same safety rule bootstrap.EnsureFirstOperator applies, for the same
// reason: configuration read at startup is not reachable by anyone who cannot
// already change the deployment's environment, which is a higher bar than any
// endpoint can offer. One live administrator and this does nothing.
func CreateFirstPlatformAdmin(in models.NewPlatformAdmin) (*models.PlatformAdmin, error) {
	email := NormalizeEmail(in.Email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, err
	}

	hash, err := hashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	fullName := strings.TrimSpace(in.FullName)
	if fullName == "" {
		fullName = defaultNameFromEmail(email)
	}

	var admin models.PlatformAdmin
	err = DB.QueryRow(`
		INSERT INTO platform_admins (email, full_name, password_hash)
		SELECT $1, $2, $3
		 WHERE NOT EXISTS (
		        SELECT 1 FROM platform_admins WHERE deleted_at IS NULL AND active
		 )
		RETURNING public_id, email, full_name, active, created_at`,
		email, fullName, hash).
		Scan(&admin.ID, &admin.Email, &admin.FullName, &admin.Active, &admin.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrOperatorsExist
	}
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// PlatformAdminIDByPublicID resolves an administrator's internal id.
//
// Needed once, at login, to open the session: models.PlatformAdmin carries only
// the public identifier because it is a response type, and a response type that
// can hold an internal id is one that eventually serialises it.
func PlatformAdminIDByPublicID(publicID string) (int64, error) {
	if !looksLikeUUID(publicID) {
		return 0, ErrPlatformAdminNotFound
	}
	var id int64
	err := DB.QueryRow(
		`SELECT id FROM platform_admins WHERE public_id = $1 AND deleted_at IS NULL`,
		publicID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPlatformAdminNotFound
	}
	return id, err
}

func defaultNameFromEmail(email string) string {
	if at := strings.LastIndex(email, "@"); at > 0 {
		return email[:at]
	}
	return "Platform Administrator"
}

// ---------------------------------------------------------------------------
// Company lifecycle
// ---------------------------------------------------------------------------

// NewCompany is the input to CreateCompany.
type NewCompany struct {
	Name         string
	Slug         string
	ContactEmail string
	Timezone     string
}

// CreateCompany onboards a tenant.
//
// THE FUNCTION THE PLATFORM DID NOT HAVE. Before this, `companies` had exactly
// one writer -- the INSERT in migration 002 that created 'Default Company' so
// pre-existing rows had a tenant to belong to. Every customer after the first
// needed somebody with database credentials.
//
// The slug is normalised rather than rejected where it can be: it appears in
// URLs and in the bootstrap environment variable, and refusing "Acme Logistics"
// to enforce a format the platform invented is less useful than deriving
// "acme-logistics" from it.
func CreateCompany(in NewCompany) (*models.PlatformCompany, error) {
	company, _, err := createCompany(DB, in)
	return company, err
}

// createCompany inserts a tenant against a connection OR a transaction, and
// also returns its INTERNAL id.
//
// Two callers, and each needs something the other does not. The platform route
// wants the response body and must never see an internal id -- see the note on
// CompanyIDByPublicID for why that matters. Self-service signup creates a
// company, a site and an owner in ONE transaction and needs the id to hang the
// other two off, so it cannot go through the exported wrapper.
//
// Kept as one function rather than two so the rules that make a new tenant
// correct -- the slug derivation, the UTC default, deny-by-default person
// access -- cannot drift between the surface a vendor onboards through and the
// one a customer signs up through.
func createCompany(q queryRower, in NewCompany) (*models.PlatformCompany, int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, 0, ErrCompanyNameRequired
	}

	slug := models.NormalizeSlug(in.Slug)
	if slug == "" {
		slug = models.NormalizeSlug(name)
	}
	if err := models.ValidateSlug(slug); err != nil {
		return nil, 0, err
	}

	timezone := strings.TrimSpace(in.Timezone)
	if timezone == "" {
		// A company whose zone is unknown is better described as UTC than as
		// the API server's incidental location.
		timezone = "UTC"
	}

	var (
		company  models.PlatformCompany
		internal int64
	)
	err := q.QueryRow(`
		-- default_person_access is NONE for a company created here, which is NOT
		-- the column's default (018_default_person_access.sql).
		--
		-- The column defaults to COMPANY_ALLOW because that is what the ALTER had
		-- to give existing rows to preserve their behaviour. A tenant created
		-- after the authorization engine exists has no behaviour to preserve, so
		-- it starts deny-by-default: a person added to a new customer's roster
		-- opens nothing until somebody says otherwise. The permissive setting is
		-- only ever a legacy carry-over, and it is visible and changeable.
		INSERT INTO companies (name, slug, contact_email, timezone, active,
		                       default_person_access)
		VALUES ($1, $2, NULLIF($3, ''), $4, TRUE, 'NONE')
		-- DO NOTHING rather than letting the unique index raise.
		--
		-- Identical behaviour for a caller outside a transaction: no row comes
		-- back and the taken slug is reported below either way. Inside one it is
		-- the difference between a retry and a dead transaction -- a constraint
		-- violation aborts the surrounding transaction, so signup could not try
		-- a second candidate slug after the first collided.
		ON CONFLICT (slug) WHERE deleted_at IS NULL DO NOTHING
		RETURNING id, public_id, name, slug, COALESCE(contact_email, ''), timezone,
		          active, created_at`,
		name, slug, strings.TrimSpace(in.ContactEmail), timezone).
		Scan(&internal, &company.ID, &company.Name, &company.Slug, &company.ContactEmail,
			&company.Timezone, &company.Active, &company.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) || IsUniqueViolation(err) {
		return nil, 0, ErrCompanySlugTaken
	}
	if err != nil {
		return nil, 0, err
	}
	return &company, internal, nil
}

// CompanyUpdate is the input to UpdateCompany. Every field is optional; only
// what is supplied is applied.
type CompanyUpdate struct {
	Name               *string
	ContactEmail       *string
	Timezone           *string
	Active             *bool
	DeactivatedReason  *string
	EventRetentionDays *int
	AuditRetentionDays *int
}

// UpdateCompany changes a tenant's metadata and policy.
//
// THE SLUG IS NOT UPDATABLE. It appears in the bootstrap environment variable
// and in operator-facing URLs, and renaming it would silently break whatever
// refers to the company by it. Renaming the display name is the operation
// somebody actually wants after a rebrand, and that is available.
func UpdateCompany(publicID string, in CompanyUpdate) (*models.PlatformCompany, error) {
	if !looksLikeUUID(publicID) {
		return nil, models.ErrCompanyNotFound
	}

	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, ErrCompanyNameRequired
		}
		in.Name = &name
	}

	var company models.PlatformCompany
	err := DB.QueryRow(`
		UPDATE companies
		   SET name = COALESCE($2, name),
		       contact_email = COALESCE(NULLIF($3, ''), contact_email),
		       timezone = COALESCE($4, timezone),
		       active = COALESCE($5, active),
		       deactivated_reason = CASE
		           WHEN COALESCE($5, active) THEN NULL
		           ELSE COALESCE($6, deactivated_reason)
		       END,
		       event_retention_days = COALESCE($7, event_retention_days),
		       audit_retention_days = COALESCE($8, audit_retention_days),
		       updated_at = CURRENT_TIMESTAMP
		 WHERE public_id = $1 AND deleted_at IS NULL
		RETURNING public_id, name, slug, COALESCE(contact_email, ''), timezone,
		          active, created_at`,
		publicID, in.Name, in.ContactEmail, in.Timezone, in.Active,
		in.DeactivatedReason, in.EventRetentionDays, in.AuditRetentionDays).
		Scan(&company.ID, &company.Name, &company.Slug, &company.ContactEmail,
			&company.Timezone, &company.Active, &company.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrCompanyNotFound
	}
	if err != nil {
		return nil, err
	}
	return &company, nil
}

// ListCompanies returns every tenant on the installation, with the counts a
// platform administrator needs to see what they are looking at.
//
// COUNTS ONLY, never contents. This is the closest the platform identity comes
// to a customer's data and it stops at cardinality: how many sites, terminals
// and people, which is what "is this tenant healthy" needs. Reading the roster
// itself is the customer's business and there is no function here that does it.
func ListCompanies() ([]models.PlatformCompany, error) {
	rows, err := DB.Query(`
		SELECT c.public_id, c.name, c.slug, COALESCE(c.contact_email, ''),
		       c.timezone, c.active, c.created_at,
		       c.event_retention_days, c.audit_retention_days,
		       (SELECT count(*) FROM sites s
		         WHERE s.company_id = c.id AND s.deleted_at IS NULL),
		       (SELECT count(*) FROM devices d
		          JOIN sites s2 ON s2.id = d.site_id
		         WHERE s2.company_id = c.id AND d.deleted_at IS NULL),
		       (SELECT count(*) FROM people p
		         WHERE p.company_id = c.id AND p.deleted_at IS NULL),
		       (SELECT count(*) FROM users u
		         WHERE u.company_id = c.id AND u.deleted_at IS NULL)
		  FROM companies c
		 WHERE c.deleted_at IS NULL
		 ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	companies := []models.PlatformCompany{}
	for rows.Next() {
		var c models.PlatformCompany
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.ContactEmail, &c.Timezone,
			&c.Active, &c.CreatedAt, &c.EventRetentionDays, &c.AuditRetentionDays,
			&c.SiteCount, &c.TerminalCount, &c.PersonCount, &c.OperatorCount); err != nil {
			return nil, err
		}
		companies = append(companies, c)
	}
	return companies, rows.Err()
}

// GetCompanyByPublicID returns one tenant, with the same counts the list carries.
func GetCompanyByPublicID(publicID string) (*models.PlatformCompany, error) {
	if !looksLikeUUID(publicID) {
		return nil, models.ErrCompanyNotFound
	}

	var c models.PlatformCompany
	err := DB.QueryRow(`
		SELECT c.public_id, c.name, c.slug, COALESCE(c.contact_email, ''),
		       c.timezone, c.active, c.created_at,
		       c.event_retention_days, c.audit_retention_days,
		       (SELECT count(*) FROM sites s
		         WHERE s.company_id = c.id AND s.deleted_at IS NULL),
		       (SELECT count(*) FROM devices d
		          JOIN sites s2 ON s2.id = d.site_id
		         WHERE s2.company_id = c.id AND d.deleted_at IS NULL),
		       (SELECT count(*) FROM people p
		         WHERE p.company_id = c.id AND p.deleted_at IS NULL),
		       (SELECT count(*) FROM users u
		         WHERE u.company_id = c.id AND u.deleted_at IS NULL)
		  FROM companies c
		 WHERE c.public_id = $1 AND c.deleted_at IS NULL`, publicID).
		Scan(&c.ID, &c.Name, &c.Slug, &c.ContactEmail, &c.Timezone,
			&c.Active, &c.CreatedAt, &c.EventRetentionDays, &c.AuditRetentionDays,
			&c.SiteCount, &c.TerminalCount, &c.PersonCount, &c.OperatorCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrCompanyNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CompanyIDByPublicID resolves a tenant's internal id.
//
// Exists for the audit trail, which references companies(id) and is written by
// platform handlers that only ever hold the public identifier. Nothing else in
// the platform surface needs it -- and deliberately so: an internal id is the
// argument every tenant-scoped query in database/ takes, and handing one to a
// platform handler that then called a console function is exactly the mistake
// the separate identity exists to make impossible. This returns an id for an
// audit row and nothing here consumes it for anything else.
func CompanyIDByPublicID(publicID string) (int64, error) {
	if !looksLikeUUID(publicID) {
		return 0, models.ErrCompanyNotFound
	}
	var id int64
	err := DB.QueryRow(
		`SELECT id FROM companies WHERE public_id = $1 AND deleted_at IS NULL`,
		publicID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, models.ErrCompanyNotFound
	}
	return id, err
}

// CreateFirstOperatorForCompany mints a company's initial OWNER.
//
// ONLY INTO A COMPANY THAT HAS NO OPERATORS. This is onboarding, not account
// administration: once a customer has an owner, every further account is created
// by them through the console, where the role matrix applies. A platform
// identity that could add operators to a running tenant at any time would be a
// standing back door into every customer.
//
// The refusal is a query predicate rather than a check-then-insert, so two
// concurrent calls cannot both see an empty company.
//
// AN EMPTY PASSWORD MEANS AN INVITATION, and that is the path the console
// offers: the account is created with a credential nobody holds and the returned
// token is the only way in. Supplying a password is kept for an environment that
// cannot deliver a link, and returns a nil token.
//
// EITHER WAY THE ACCOUNT IS FLAGGED must_change_password. A password the
// platform administrator chose is one they still know, and the owner of a
// customer's tenant should not be working under a credential their vendor
// selected.
func CreateFirstOperatorForCompany(companyPublicID string, in models.NewUser) (
	*models.User, *models.CredentialToken, error) {

	if !looksLikeUUID(companyPublicID) {
		return nil, nil, models.ErrCompanyNotFound
	}

	email := NormalizeEmail(in.Email)
	if err := ValidateEmail(email); err != nil {
		return nil, nil, err
	}

	invited := strings.TrimSpace(in.Password) == ""
	if invited {
		generated, err := unredeemablePassword()
		if err != nil {
			return nil, nil, err
		}
		in.Password = generated
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, nil, err
	}

	hash, err := hashPassword(in.Password)
	if err != nil {
		return nil, nil, err
	}

	fullName := strings.TrimSpace(in.FullName)
	if fullName == "" {
		fullName = defaultNameFromEmail(email)
	}

	var companyID int64
	err = DB.QueryRow(`
		SELECT id FROM companies WHERE public_id = $1 AND deleted_at IS NULL`,
		companyPublicID).Scan(&companyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, models.ErrCompanyNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var user models.User
	err = tx.QueryRow(`
		INSERT INTO users (company_id, email, full_name, password_hash, role,
		                   must_change_password)
		SELECT $1, $2, $3, $4, $5, TRUE
		 WHERE NOT EXISTS (
		        SELECT 1 FROM users
		         WHERE company_id = $1 AND deleted_at IS NULL
		 )
		RETURNING id, public_id, company_id, email, full_name, role, active,
		          last_login_at, created_at, updated_at`,
		companyID, email, fullName, hash, models.RoleOwner).
		Scan(&user.ID, &user.PublicID, &user.CompanyID, &user.Email, &user.FullName,
			&user.Role, &user.Active, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrCompanyHasOperators
	}
	if IsUniqueViolation(err) {
		return nil, nil, fmt.Errorf("that email address is already in use: %w", err)
	}
	if err != nil {
		return nil, nil, err
	}

	var token *models.CredentialToken
	if invited {
		// Issued by nobody: a platform administrator is not a row in `users`, and
		// issued_by_user_id references that table. The audit trail records the
		// creation with the platform identity on it, which is where that question
		// is answered.
		token, err = issueCredentialTokenTx(tx, user.ID, models.TokenPurposeInvite, 0, "")
		if err != nil {
			return nil, nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &user, token, nil
}
