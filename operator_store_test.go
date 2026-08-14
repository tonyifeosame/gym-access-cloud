package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"golang.org/x/crypto/bcrypt"
)

// Operator account and session storage (database/users.go, database/sessions.go
// against migrations/008_operator_accounts.sql).
//
// These run against the same real PostgreSQL the rest of the suite uses. The
// behaviour worth protecting is mostly in SQL and in what the schema refuses --
// the partial unique index on email, the tenant filters, the expiry clamp, the
// cascade from a revoked account to its sessions -- and none of that is
// observable through a mock.
//
// Every test drops bcrypt to its floor cost. The work factor is not what is
// under test here, and at the production cost of 12 a lockout test alone would
// spend several seconds hashing.

const testPassword = "correct-horse-battery-staple"

// loginLockCapSeconds mirrors the unexported lockout ceiling in
// database/users.go. If that policy changes, this assertion changes with it.
const loginLockCapSeconds = 900

func cheapBcrypt(t *testing.T) {
	t.Helper()
	t.Setenv("BCRYPT_COST", "10")
}

func queryString(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := database.DB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

func queryTime(t *testing.T, query string, args ...any) time.Time {
	t.Helper()
	var out time.Time
	if err := database.DB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// operatorCompanyID resolves one of the tenants newTestEnv creates
func operatorCompanyID(t *testing.T, slug string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM companies WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("company %q: %v", slug, err)
	}
	return id
}

// operatorSitePublicID resolves a site created by newTestEnv
func operatorSitePublicID(t *testing.T, siteName string) string {
	t.Helper()
	return queryString(t, `SELECT public_id FROM sites WHERE site_name = $1`, siteName)
}

func mustCreateOperator(t *testing.T, companyID int64, email, role string) *models.User {
	t.Helper()
	user, err := database.CreateUser(companyID, models.NewUser{
		Email:    email,
		FullName: "Test Operator",
		Password: testPassword,
		Role:     role,
	})
	if err != nil {
		t.Fatalf("creating operator %s: %v", email, err)
	}
	return user
}

// mustOpenSession logs a session in and resolves it, so a test has both the
// plaintext credentials and the internal session id.
func mustOpenSession(t *testing.T, userID int64) (*models.SessionCredentials, *models.OperatorIdentity) {
	t.Helper()
	creds, err := database.CreateSession(userID, "203.0.113.7", "Mozilla/5.0 (test)")
	if err != nil {
		t.Fatalf("creating session: %v", err)
	}
	identity, err := database.AuthenticateSession(creds.Token)
	if err != nil {
		t.Fatalf("authenticating fresh session: %v", err)
	}
	if identity == nil {
		t.Fatal("a session that was just created did not authenticate")
	}
	return creds, identity
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestOperatorCredentialPolicy(t *testing.T) {
	tooLong := strings.Repeat("a", models.MaxPasswordBytes+1)
	// 40 two-byte runes: long enough by character count, too long for bcrypt,
	// which is the case a naive length check gets wrong.
	multibyte := strings.Repeat("é", 40)

	passwords := []struct {
		password string
		want     error
	}{
		{"short", models.ErrPasswordTooShort},
		{strings.Repeat("a", models.MinPasswordLength-1), models.ErrPasswordTooShort},
		{strings.Repeat("a", models.MinPasswordLength), nil},
		{tooLong, models.ErrPasswordTooLong},
		{multibyte, models.ErrPasswordTooLong},
		{testPassword, nil},
	}
	for _, tc := range passwords {
		if got := database.ValidatePassword(tc.password); !errors.Is(got, tc.want) {
			t.Errorf("ValidatePassword(%d chars/%d bytes) = %v, want %v",
				len([]rune(tc.password)), len(tc.password), got, tc.want)
		}
	}

	if got := database.NormalizeEmail("  Ops@Example.COM \n"); got != "ops@example.com" {
		t.Errorf("NormalizeEmail = %q, want %q", got, "ops@example.com")
	}

	emails := map[string]bool{
		"ops@example.com":         true,
		"ops+dash@sub.example.co": true,
		"Ops <ops@example.com>":   false, // a display name must not round-trip
		"not-an-address":          false,
		"":                        false,
		strings.Repeat("a", 250) + "@example.com": false, // over the column width
	}
	for email, wantValid := range emails {
		err := database.ValidateEmail(email)
		if wantValid && err != nil {
			t.Errorf("ValidateEmail(%q) = %v, want nil", email, err)
		}
		if !wantValid && err == nil {
			t.Errorf("ValidateEmail(%q) = nil, want rejection", email)
		}
	}
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func TestOperatorCreationAndTenancy(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	user := mustCreateOperator(t, one, "owner@example.com", models.RoleOwner)
	switch {
	case user.PublicID == "":
		t.Error("created operator has no public_id")
	case user.Role != models.RoleOwner:
		t.Errorf("role = %q, want OWNER", user.Role)
	case !user.Active:
		t.Error("a new operator should be active")
	case user.LastLoginAt != nil:
		t.Error("a new operator should have no last_login_at")
	}

	// The password must not be recoverable from the row, and the column must
	// hold a bcrypt hash rather than anything that merely looks hashed.
	hash := queryString(t, `SELECT password_hash FROM users WHERE id = $1`, user.ID)
	if strings.Contains(hash, testPassword) {
		t.Fatal("the plaintext password is present in password_hash")
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("password_hash = %q, want a bcrypt hash", hash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(testPassword)); err != nil {
		t.Errorf("stored hash does not verify the password: %v", err)
	}

	// Addresses are normalised on the way in, which is what makes the partial
	// unique index case-insensitive.
	mixed, err := database.CreateUser(one, models.NewUser{
		Email: "Mixed.Case@Example.COM", FullName: "Mixed", Password: testPassword,
	})
	if err != nil {
		t.Fatalf("creating an operator with a mixed-case address: %v", err)
	}
	if mixed.Email != "mixed.case@example.com" {
		t.Errorf("stored email = %q, want it lowercased", mixed.Email)
	}
	if mixed.Role != models.RoleViewer {
		t.Errorf("default role = %q, want VIEWER", mixed.Role)
	}

	// Validation happens in Go, so a bad value is a rejection the handler can
	// tell apart from the database being unwell -- never a constraint violation.
	invalid := []struct {
		name string
		in   models.NewUser
		want error
	}{
		{"unknown role", models.NewUser{Email: "a@example.com", FullName: "A",
			Password: testPassword, Role: "SUPERUSER"}, models.ErrInvalidRole},
		{"malformed email", models.NewUser{Email: "nope", FullName: "A",
			Password: testPassword}, models.ErrInvalidEmail},
		{"short password", models.NewUser{Email: "b@example.com", FullName: "A",
			Password: "short"}, models.ErrPasswordTooShort},
	}
	for _, tc := range invalid {
		if _, err := database.CreateUser(one, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("CreateUser(%s) = %v, want %v", tc.name, err, tc.want)
		}
	}

	// A duplicate address is the caller's mistake and must be reportable as 409.
	_, err = database.CreateUser(one, models.NewUser{
		Email: "owner@example.com", FullName: "Duplicate", Password: testPassword,
	})
	if !database.IsUniqueViolation(err) {
		t.Errorf("duplicate email = %v, want a unique violation", err)
	}

	// Global uniqueness: the login form has no tenant selector, so an address
	// cannot belong to two companies at once.
	_, err = database.CreateUser(two, models.NewUser{
		Email: "owner@example.com", FullName: "Other tenant", Password: testPassword,
	})
	if !database.IsUniqueViolation(err) {
		t.Errorf("same email in another company = %v, want a unique violation", err)
	}

	// Reads are company-scoped, and a foreign id is not found rather than
	// forbidden -- the API must not confirm that an id exists elsewhere.
	if _, err := database.GetUserByPublicID(one, user.PublicID); err != nil {
		t.Errorf("fetching an operator in its own company: %v", err)
	}
	if _, err := database.GetUserByPublicID(two, user.PublicID); !errors.Is(err, models.ErrUserNotFound) {
		t.Errorf("cross-tenant fetch = %v, want ErrUserNotFound", err)
	}
	// A malformed id is a 404, not a 500 from a uuid syntax error.
	if _, err := database.GetUserByPublicID(one, "not-a-uuid"); !errors.Is(err, models.ErrUserNotFound) {
		t.Errorf("malformed public id = %v, want ErrUserNotFound", err)
	}

	if users, err := database.ListUsers(one); err != nil || len(users) != 2 {
		t.Errorf("ListUsers(one) = %d users, %v; want 2", len(users), err)
	}
	if users, err := database.ListUsers(two); err != nil || len(users) != 0 {
		t.Errorf("ListUsers(two) = %d users, %v; want 0", len(users), err)
	}

	if count, err := database.CountLiveOperators(); err != nil || count != 2 {
		t.Errorf("CountLiveOperators = %d, %v; want 2", count, err)
	}
}

func TestOperatorAuthenticationOutcomes(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "auth@example.com", models.RoleAdmin)

	authenticated, err := database.AuthenticatePassword("auth@example.com", testPassword)
	if err != nil {
		t.Fatalf("valid login: %v", err)
	}
	if authenticated.ID != user.ID || authenticated.LastLoginAt == nil {
		t.Error("a successful login should return the operator and stamp last_login_at")
	}

	// The address is normalised on the way in, so case must not matter.
	if _, err := database.AuthenticatePassword("AUTH@Example.com", testPassword); err != nil {
		t.Errorf("login with a differently-cased address: %v", err)
	}

	// Every rejection the caller is not entitled to tell apart is the same error.
	rejections := []struct {
		name, email, password string
	}{
		{"wrong password", "auth@example.com", "wrong-password-entirely"},
		{"unknown address", "nobody@example.com", testPassword},
	}
	for _, tc := range rejections {
		if _, err := database.AuthenticatePassword(tc.email, tc.password); !errors.Is(err, models.ErrInvalidCredentials) {
			t.Errorf("%s = %v, want ErrInvalidCredentials", tc.name, err)
		}
	}

	// A failed attempt is counted; a successful one clears the count.
	if count := queryInt(t, `SELECT failed_login_count FROM users WHERE id = $1`, user.ID); count != 1 {
		t.Errorf("failed_login_count = %d after one bad password, want 1", count)
	}
	if _, err := database.AuthenticatePassword("auth@example.com", testPassword); err != nil {
		t.Fatalf("login after a failure: %v", err)
	}
	if count := queryInt(t, `SELECT failed_login_count FROM users WHERE id = $1`, user.ID); count != 0 {
		t.Errorf("failed_login_count = %d after a success, want 0", count)
	}

	// A disabled account, a disabled company and a deleted account are all
	// reported as bad credentials -- the password being right changes nothing.
	if err := database.SetUserActive(one, user.ID, false); err != nil {
		t.Fatalf("disabling operator: %v", err)
	}
	if _, err := database.AuthenticatePassword("auth@example.com", testPassword); !errors.Is(err, models.ErrInvalidCredentials) {
		t.Errorf("login as a disabled operator = %v, want ErrInvalidCredentials", err)
	}
	if err := database.SetUserActive(one, user.ID, true); err != nil {
		t.Fatalf("re-enabling operator: %v", err)
	}

	mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, one)
	if _, err := database.AuthenticatePassword("auth@example.com", testPassword); !errors.Is(err, models.ErrInvalidCredentials) {
		t.Errorf("login into a disabled company = %v, want ErrInvalidCredentials", err)
	}
	mustExec(t, `UPDATE companies SET active = TRUE WHERE id = $1`, one)

	if err := database.SoftDeleteUser(one, user.ID); err != nil {
		t.Fatalf("soft-deleting operator: %v", err)
	}
	if _, err := database.AuthenticatePassword("auth@example.com", testPassword); !errors.Is(err, models.ErrInvalidCredentials) {
		t.Errorf("login as a deleted operator = %v, want ErrInvalidCredentials", err)
	}

	// Deleting frees the address, and the replacement account is a new identity.
	replacement := mustCreateOperator(t, one, "auth@example.com", models.RoleViewer)
	if replacement.ID == user.ID {
		t.Error("re-creating a deleted operator reused the old row")
	}
	if _, err := database.AuthenticatePassword("auth@example.com", testPassword); err != nil {
		t.Errorf("login as the replacement operator: %v", err)
	}
}

func TestOperatorLockoutBacksOffAndRecovers(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "locked@example.com", models.RoleViewer)

	// Five failures reach the threshold; each of them still reports bad
	// credentials, because the lock is only consulted on the NEXT attempt.
	for attempt := 1; attempt <= 5; attempt++ {
		_, err := database.AuthenticatePassword("locked@example.com", "wrong-password-here")
		if !errors.Is(err, models.ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v, want ErrInvalidCredentials", attempt, err)
		}
	}

	var locked *models.AccountLockedError
	_, err := database.AuthenticatePassword("locked@example.com", "wrong-password-here")
	if !errors.As(err, &locked) {
		t.Fatalf("sixth attempt = %v, want AccountLockedError", err)
	}
	// Resolved by the database, so it is a real remaining duration rather than
	// a stored wall-clock time this process would have to interpret.
	if locked.RetryAfterSeconds <= 0 || locked.RetryAfterSeconds > loginLockCapSeconds {
		t.Errorf("RetryAfterSeconds = %d, want between 1 and %d",
			locked.RetryAfterSeconds, loginLockCapSeconds)
	}
	if retry := locked.RetryAfter(); retry < 1 {
		t.Errorf("RetryAfter = %d, want at least 1", retry)
	}

	// The lock precedes the password comparison, so the right password does not
	// unlock the account either.
	if _, err := database.AuthenticatePassword("locked@example.com", testPassword); !errors.As(err, &locked) {
		t.Errorf("correct password while locked = %v, want AccountLockedError", err)
	}

	// The delay doubles with each further failure, and is capped.
	firstLock := queryTime(t, `SELECT locked_until FROM users WHERE id = $1`, user.ID)
	mustExec(t, `UPDATE users SET locked_until = NULL, failed_login_count = 6 WHERE id = $1`, user.ID)
	if _, err := database.AuthenticatePassword("locked@example.com", "wrong-password-here"); !errors.Is(err, models.ErrInvalidCredentials) {
		t.Fatalf("failure after clearing the lock: %v", err)
	}
	secondLock := queryTime(t, `SELECT locked_until FROM users WHERE id = $1`, user.ID)
	if !secondLock.After(firstLock) {
		t.Errorf("seventh failure locked until %v, which is not longer than %v", secondLock, firstLock)
	}

	mustExec(t, `UPDATE users SET locked_until = NULL, failed_login_count = 40 WHERE id = $1`, user.ID)
	if _, err := database.AuthenticatePassword("locked@example.com", "wrong-password-here"); !errors.Is(err, models.ErrInvalidCredentials) {
		t.Fatalf("failure at a high count: %v", err)
	}
	// Measured in SQL. Reading locked_until into Go and subtracting time.Now()
	// would be off by the database server's UTC offset, because the column is
	// TIMESTAMP WITHOUT TIME ZONE holding that server's local wall clock.
	capped := queryInt(t, `SELECT CEIL(EXTRACT(EPOCH FROM (locked_until - CURRENT_TIMESTAMP)))::int
	                         FROM users WHERE id = $1`, user.ID)
	if capped > loginLockCapSeconds {
		t.Errorf("lock of %d seconds exceeds the %d second cap", capped, loginLockCapSeconds)
	}
	if capped < loginLockCapSeconds-5 {
		t.Errorf("lock of %d seconds is below the cap it should have reached", capped)
	}

	// The lock is time-bounded, so an account recovers on its own.
	mustExec(t, `UPDATE users SET locked_until = CURRENT_TIMESTAMP - interval '1 second' WHERE id = $1`, user.ID)
	if _, err := database.AuthenticatePassword("locked@example.com", testPassword); err != nil {
		t.Errorf("login after the lock expired: %v", err)
	}
	if count := queryInt(t, `SELECT failed_login_count FROM users WHERE id = $1`, user.ID); count != 0 {
		t.Errorf("failed_login_count = %d after recovery, want 0", count)
	}
	if queryBool(t, `SELECT locked_until IS NOT NULL FROM users WHERE id = $1`, user.ID) {
		t.Error("locked_until survived a successful login")
	}
}

func TestOperatorPasswordRehashesWhenCostRises(t *testing.T) {
	t.Setenv("BCRYPT_COST", "10")
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "rehash@example.com", models.RoleViewer)

	storedCost := func() int {
		t.Helper()
		cost, err := bcrypt.Cost([]byte(queryString(t,
			`SELECT password_hash FROM users WHERE id = $1`, user.ID)))
		if err != nil {
			t.Fatalf("reading bcrypt cost: %v", err)
		}
		return cost
	}

	if got := storedCost(); got != 10 {
		t.Fatalf("cost at creation = %d, want 10", got)
	}

	t.Setenv("BCRYPT_COST", "11")
	if _, err := database.AuthenticatePassword("rehash@example.com", testPassword); err != nil {
		t.Fatalf("login after raising the cost: %v", err)
	}
	if got := storedCost(); got != 11 {
		t.Errorf("cost after login = %d, want the hash upgraded to 11", got)
	}
	// The upgraded hash must still verify the same password.
	if _, err := database.AuthenticatePassword("rehash@example.com", testPassword); err != nil {
		t.Errorf("login with the rehashed credential: %v", err)
	}

	// The floor is enforced: a configured cost below 10 does not take effect.
	t.Setenv("BCRYPT_COST", "4")
	if got := database.BcryptCost(); got != 10 {
		t.Errorf("BcryptCost with BCRYPT_COST=4 is %d, want the floor of 10", got)
	}
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestSessionIssuanceAndResolution(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "session@example.com", models.RoleManager)

	creds, identity := mustOpenSession(t, user.ID)

	if !strings.HasPrefix(creds.Token, "ats_") {
		t.Errorf("session token %q does not carry the ats_ prefix", creds.Token)
	}
	if len(creds.Token) != len("ats_")+64 {
		t.Errorf("session token is %d characters, want %d", len(creds.Token), len("ats_")+64)
	}
	if creds.CSRFToken == "" || creds.CSRFToken == creds.Token {
		t.Error("the CSRF token must exist and must not be the session token")
	}

	// Only the hash is stored. Searching for the plaintext must find nothing.
	if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE token_hash = $1`, creds.Token); n != 0 {
		t.Error("the plaintext session token is stored in token_hash")
	}
	if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE csrf_token_hash = $1`, creds.CSRFToken); n != 0 {
		t.Error("the plaintext CSRF token is stored in csrf_token_hash")
	}
	if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE token_hash ~ '^[0-9a-f]{64}$'`); n != 1 {
		t.Error("token_hash is not a 64-character hex digest")
	}

	if identity.UserID != user.ID || identity.CompanyID != one {
		t.Error("the resolved identity does not match the operator")
	}
	if identity.Role != models.RoleManager || identity.Email != "session@example.com" {
		t.Errorf("identity = role %q, email %q", identity.Role, identity.Email)
	}
	if identity.SessionPublicID != creds.PublicID {
		t.Error("the resolved session is not the one that was issued")
	}
	if !identity.AbsoluteExpiresAt.After(identity.IdleExpiresAt) &&
		!identity.AbsoluteExpiresAt.Equal(identity.IdleExpiresAt) {
		t.Error("idle expiry must never exceed the absolute expiry")
	}

	// CSRF comparison is by hash, and only the right token matches.
	if !database.CSRFMatches(identity.CSRFTokenHash, creds.CSRFToken) {
		t.Error("the issued CSRF token does not match its own session")
	}
	for _, wrong := range []string{"", creds.CSRFToken + "x", creds.Token} {
		if database.CSRFMatches(identity.CSRFTokenHash, wrong) {
			t.Errorf("CSRFMatches accepted %q", wrong)
		}
	}
	if database.CSRFMatches("", creds.CSRFToken) {
		t.Error("CSRFMatches accepted an empty stored hash")
	}

	// An unknown or empty token resolves to nobody, and is not an error: the
	// caller answers 401, and only a database fault is a 500.
	for _, bogus := range []string{"", "ats_" + strings.Repeat("0", 64), "garbage"} {
		got, err := database.AuthenticateSession(bogus)
		if err != nil || got != nil {
			t.Errorf("AuthenticateSession(%q) = %v, %v; want nil, nil", bogus, got, err)
		}
	}
}

func TestSessionRejectionPaths(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// Each case gets its own operator and session, so one rejection cannot mask
	// another.
	cases := []struct {
		name    string
		email   string
		disable func(t *testing.T, userID, sessionID int64)
	}{
		{"revoked", "revoked@example.com", func(t *testing.T, _, sessionID int64) {
			if err := database.RevokeSession(sessionID); err != nil {
				t.Fatalf("revoking: %v", err)
			}
		}},
		{"idle expired", "idle@example.com", func(t *testing.T, _, sessionID int64) {
			mustExec(t, `UPDATE user_sessions
			                SET idle_expires_at = CURRENT_TIMESTAMP - interval '1 second'
			              WHERE id = $1`, sessionID)
		}},
		// Absolute expiry implies idle expiry, because the schema constrains
		// idle_expires_at <= absolute_expires_at. issued_at has to move back
		// with them, or user_sessions_expiry_check rejects the fixture itself.
		// The assertion is that a session past its hard cap is refused.
		{"absolute expired", "absolute@example.com", func(t *testing.T, _, sessionID int64) {
			mustExec(t, `UPDATE user_sessions
			                SET issued_at = CURRENT_TIMESTAMP - interval '8 days',
			                    idle_expires_at = CURRENT_TIMESTAMP - interval '2 seconds',
			                    absolute_expires_at = CURRENT_TIMESTAMP - interval '1 second'
			              WHERE id = $1`, sessionID)
		}},
		{"operator disabled", "disabled@example.com", func(t *testing.T, userID, _ int64) {
			mustExec(t, `UPDATE users SET active = FALSE WHERE id = $1`, userID)
		}},
		{"operator deleted", "deleted@example.com", func(t *testing.T, userID, _ int64) {
			mustExec(t, `UPDATE users SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1`, userID)
		}},
		{"company disabled", "companyoff@example.com", func(t *testing.T, userID, _ int64) {
			mustExec(t, `UPDATE companies SET active = FALSE
			              WHERE id = (SELECT company_id FROM users WHERE id = $1)`, userID)
		}},
		{"company deleted", "companygone@example.com", func(t *testing.T, userID, _ int64) {
			mustExec(t, `UPDATE companies SET deleted_at = CURRENT_TIMESTAMP
			              WHERE id = (SELECT company_id FROM users WHERE id = $1)`, userID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh tenant per case: the company-level cases would otherwise
			// take every other operator down with them.
			var companyID int64
			if strings.HasPrefix(tc.name, "company") {
				companyID = operatorCompanyID(t, "two")
				mustExec(t, `UPDATE companies SET active = TRUE, deleted_at = NULL WHERE id = $1`, companyID)
			} else {
				companyID = one
			}

			user := mustCreateOperator(t, companyID, tc.email, models.RoleViewer)
			creds, identity := mustOpenSession(t, user.ID)

			tc.disable(t, user.ID, identity.SessionID)

			got, err := database.AuthenticateSession(creds.Token)
			if err != nil {
				t.Fatalf("AuthenticateSession returned an error, not a refusal: %v", err)
			}
			if got != nil {
				t.Errorf("a %s session still authenticates", tc.name)
			}
		})
	}
}

func TestSessionIdleWindowSlidesAndClamps(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "sliding@example.com", models.RoleViewer)
	creds, identity := mustOpenSession(t, user.ID)

	lastUsed := func() time.Time {
		return queryTime(t, `SELECT last_used_at FROM user_sessions WHERE id = $1`, identity.SessionID)
	}
	idleExpiry := func() time.Time {
		return queryTime(t, `SELECT idle_expires_at FROM user_sessions WHERE id = $1`, identity.SessionID)
	}

	// Within the throttle window the row is left alone: an active dashboard
	// must not turn one write per request into the session table's workload.
	before := lastUsed()
	if _, err := database.AuthenticateSession(creds.Token); err != nil {
		t.Fatalf("re-authenticating: %v", err)
	}
	if got := lastUsed(); !got.Equal(before) {
		t.Errorf("last_used_at moved from %v to %v inside the throttle window", before, got)
	}

	// Past the throttle window it slides forward.
	mustExec(t, `UPDATE user_sessions
	                SET last_used_at = CURRENT_TIMESTAMP - interval '5 minutes',
	                    idle_expires_at = CURRENT_TIMESTAMP + interval '1 hour'
	              WHERE id = $1`, identity.SessionID)
	previousExpiry := idleExpiry()
	slid, err := database.AuthenticateSession(creds.Token)
	if err != nil || slid == nil {
		t.Fatalf("authenticating a stale-but-valid session: %v, %v", slid, err)
	}
	if !idleExpiry().After(previousExpiry) {
		t.Error("the idle window did not slide forward")
	}
	if !slid.IdleExpiresAt.Equal(idleExpiry()) {
		t.Error("the returned identity does not carry the refreshed expiry")
	}

	// The slide clamps to the absolute expiry rather than pushing past it,
	// which is also what the schema's CHECK requires.
	mustExec(t, `UPDATE user_sessions
	                SET last_used_at = CURRENT_TIMESTAMP - interval '5 minutes',
	                    absolute_expires_at = CURRENT_TIMESTAMP + interval '1 minute',
	                    idle_expires_at = CURRENT_TIMESTAMP + interval '30 seconds'
	              WHERE id = $1`, identity.SessionID)
	clamped, err := database.AuthenticateSession(creds.Token)
	if err != nil || clamped == nil {
		t.Fatalf("authenticating near the absolute expiry: %v, %v", clamped, err)
	}
	if clamped.IdleExpiresAt.After(clamped.AbsoluteExpiresAt) {
		t.Errorf("idle expiry %v was slid past the absolute expiry %v",
			clamped.IdleExpiresAt, clamped.AbsoluteExpiresAt)
	}
}

func TestSessionRevocationOnAccountChanges(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	alive := func(token string) bool {
		t.Helper()
		identity, err := database.AuthenticateSession(token)
		if err != nil {
			t.Fatalf("authenticating: %v", err)
		}
		return identity != nil
	}

	t.Run("password change keeps only the current session", func(t *testing.T) {
		user := mustCreateOperator(t, one, "pw@example.com", models.RoleAdmin)
		keep, keepIdentity := mustOpenSession(t, user.ID)
		other, _ := mustOpenSession(t, user.ID)
		third, _ := mustOpenSession(t, user.ID)

		newPassword := "an-entirely-different-secret"
		if err := database.SetUserPassword(user.ID, newPassword, keepIdentity.SessionID); err != nil {
			t.Fatalf("changing password: %v", err)
		}

		if !alive(keep.Token) {
			t.Error("the session that changed the password was logged out")
		}
		if alive(other.Token) || alive(third.Token) {
			t.Error("a sibling session survived the password change")
		}
		if _, err := database.AuthenticatePassword("pw@example.com", testPassword); !errors.Is(err, models.ErrInvalidCredentials) {
			t.Error("the old password still works")
		}
		if _, err := database.AuthenticatePassword("pw@example.com", newPassword); err != nil {
			t.Errorf("the new password does not work: %v", err)
		}
		if err := database.SetUserPassword(user.ID, "short", 0); !errors.Is(err, models.ErrPasswordTooShort) {
			t.Errorf("weak password accepted: %v", err)
		}
	})

	t.Run("disabling an operator cuts off every session", func(t *testing.T) {
		user := mustCreateOperator(t, one, "off@example.com", models.RoleViewer)
		creds, _ := mustOpenSession(t, user.ID)
		if err := database.SetUserActive(one, user.ID, false); err != nil {
			t.Fatalf("disabling: %v", err)
		}
		if alive(creds.Token) {
			t.Error("a disabled operator's session still authenticates")
		}
	})

	t.Run("changing a role forces re-authentication", func(t *testing.T) {
		user := mustCreateOperator(t, one, "role@example.com", models.RoleViewer)
		creds, _ := mustOpenSession(t, user.ID)
		if err := database.SetUserRole(one, user.ID, models.RoleAdmin); err != nil {
			t.Fatalf("changing role: %v", err)
		}
		if alive(creds.Token) {
			t.Error("a session opened under the previous role survived")
		}
		if err := database.SetUserRole(one, user.ID, "SUPERUSER"); !errors.Is(err, models.ErrInvalidRole) {
			t.Errorf("unknown role accepted: %v", err)
		}
		// Cross-tenant writes must not land either.
		two := operatorCompanyID(t, "two")
		if err := database.SetUserRole(two, user.ID, models.RoleOwner); !errors.Is(err, models.ErrUserNotFound) {
			t.Errorf("cross-tenant role change = %v, want ErrUserNotFound", err)
		}
	})

	t.Run("deleting an operator cuts off every session", func(t *testing.T) {
		user := mustCreateOperator(t, one, "gone@example.com", models.RoleViewer)
		creds, _ := mustOpenSession(t, user.ID)
		if err := database.SoftDeleteUser(one, user.ID); err != nil {
			t.Fatalf("deleting: %v", err)
		}
		if alive(creds.Token) {
			t.Error("a deleted operator's session still authenticates")
		}
		if err := database.SoftDeleteUser(one, user.ID); !errors.Is(err, models.ErrUserNotFound) {
			t.Errorf("deleting twice = %v, want ErrUserNotFound", err)
		}
	})

	t.Run("logout-all spares the nominated session", func(t *testing.T) {
		user := mustCreateOperator(t, one, "all@example.com", models.RoleViewer)
		keep, keepIdentity := mustOpenSession(t, user.ID)
		other, _ := mustOpenSession(t, user.ID)

		revoked, err := database.RevokeUserSessions(user.ID, keepIdentity.SessionID)
		if err != nil {
			t.Fatalf("revoking sessions: %v", err)
		}
		if revoked != 1 {
			t.Errorf("revoked %d sessions, want 1", revoked)
		}
		if !alive(keep.Token) || alive(other.Token) {
			t.Error("logout-all revoked the wrong sessions")
		}

		// Revoking the rest is idempotent and reports what it actually did.
		if again, _ := database.RevokeUserSessions(user.ID, 0); again != 1 {
			t.Errorf("second sweep revoked %d, want the 1 remaining", again)
		}
		if again, _ := database.RevokeUserSessions(user.ID, 0); again != 0 {
			t.Error("revoking already-revoked sessions reported work")
		}
	})
}

func TestSessionListingAndPurge(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	user := mustCreateOperator(t, one, "purge@example.com", models.RoleViewer)

	live, liveIdentity := mustOpenSession(t, user.ID)
	stale, staleIdentity := mustOpenSession(t, user.ID)
	revoked, revokedIdentity := mustOpenSession(t, user.ID)
	_ = live

	sessions, err := database.ListUserSessions(user.ID)
	if err != nil || len(sessions) != 3 {
		t.Fatalf("ListUserSessions = %d sessions, %v; want 3", len(sessions), err)
	}
	for _, s := range sessions {
		if strings.Contains(s.PublicID, "ats_") {
			t.Error("a listed session exposes something token-shaped")
		}
		if s.IPAddress != "203.0.113.7" {
			t.Errorf("session ip = %q, want the address it was opened from", s.IPAddress)
		}
	}

	// Age one past its absolute expiry and revoke another, both beyond any
	// retention window.
	// issued_at moves with them: user_sessions_expiry_check requires the
	// absolute expiry to be after the moment the session was issued.
	mustExec(t, `UPDATE user_sessions
	                SET issued_at = CURRENT_TIMESTAMP - interval '47 days',
	                    idle_expires_at = CURRENT_TIMESTAMP - interval '40 days',
	                    absolute_expires_at = CURRENT_TIMESTAMP - interval '40 days'
	              WHERE id = $1`, staleIdentity.SessionID)
	mustExec(t, `UPDATE user_sessions
	                SET revoked_at = CURRENT_TIMESTAMP - interval '40 days'
	              WHERE id = $1`, revokedIdentity.SessionID)

	if sessions, _ := database.ListUserSessions(user.ID); len(sessions) != 1 {
		t.Errorf("ListUserSessions = %d, want only the live session", len(sessions))
	}
	if _, err := database.AuthenticateSession(stale.Token); err != nil {
		t.Fatalf("authenticating an expired session errored: %v", err)
	}
	_ = revoked

	// Inside the retention window nothing is removed; outside it, both dead
	// rows go and the live one stays.
	if removed, err := database.PurgeExpiredSessions(90); err != nil || removed != 0 {
		t.Errorf("purge with 90 day retention removed %d, %v; want 0", removed, err)
	}
	removedCount, err := database.PurgeExpiredSessions(30)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if removedCount != 2 {
		t.Errorf("purge removed %d sessions, want 2", removedCount)
	}
	if n := queryInt(t, `SELECT count(*) FROM user_sessions WHERE user_id = $1`, user.ID); n != 1 {
		t.Errorf("%d sessions remain, want the live one", n)
	}
	if identity, _ := database.AuthenticateSession(live.Token); identity == nil ||
		identity.SessionID != liveIdentity.SessionID {
		t.Error("the purge took the live session with it")
	}
}

// ---------------------------------------------------------------------------
// Site grants
// ---------------------------------------------------------------------------

func TestSiteGrantsStayInsideTheCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_ = env
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")
	user := mustCreateOperator(t, one, "grants@example.com", models.RoleManager)

	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	siteC := operatorSitePublicID(t, "Site C") // company two

	// No grants at all is the default, and means "every site in the company".
	if grants, err := database.ListSiteGrants(user.ID); err != nil || len(grants) != 0 {
		t.Fatalf("a new operator has %d grants, %v; want none", len(grants), err)
	}

	if err := database.ReplaceSiteGrants(one, user.ID, []string{siteB, siteA}); err != nil {
		t.Fatalf("granting two sites: %v", err)
	}
	grants, err := database.ListSiteGrants(user.ID)
	if err != nil || len(grants) != 2 {
		t.Fatalf("ListSiteGrants = %d, %v; want 2", len(grants), err)
	}
	if grants[0].SiteName != "Site A" || grants[1].SiteName != "Site B" {
		t.Errorf("grants are not ordered by site name: %v, %v", grants[0].SiteName, grants[1].SiteName)
	}

	// A site in another company cannot be granted, and the attempt must leave
	// the existing grants exactly as they were -- the whole call rolls back
	// rather than applying the sites it happened to resolve first.
	err = database.ReplaceSiteGrants(one, user.ID, []string{siteA, siteC})
	if !errors.Is(err, models.ErrSiteNotFound) {
		t.Errorf("granting another company's site = %v, want ErrSiteNotFound", err)
	}
	if grants, _ := database.ListSiteGrants(user.ID); len(grants) != 2 {
		t.Errorf("a refused grant left %d grants behind, want the original 2", len(grants))
	}

	if err := database.ReplaceSiteGrants(one, user.ID, []string{"not-a-uuid"}); !errors.Is(err, models.ErrSiteNotFound) {
		t.Errorf("malformed site id = %v, want ErrSiteNotFound", err)
	}
	if grants, _ := database.ListSiteGrants(user.ID); len(grants) != 2 {
		t.Error("a malformed site id disturbed the existing grants")
	}

	// The operator must belong to the company doing the granting.
	if err := database.ReplaceSiteGrants(two, user.ID, []string{siteC}); !errors.Is(err, models.ErrUserNotFound) {
		t.Errorf("cross-tenant grant = %v, want ErrUserNotFound", err)
	}

	// A retired site stops conveying anything without the grant row needing to
	// be cleaned up.
	mustExec(t, `UPDATE sites SET deleted_at = CURRENT_TIMESTAMP WHERE site_name = 'Site B'`)
	if grants, _ := database.ListSiteGrants(user.ID); len(grants) != 1 {
		t.Error("a soft-deleted site is still listed as a grant")
	}

	// Grants are replaced wholesale, so an empty list clears them.
	if err := database.ReplaceSiteGrants(one, user.ID, nil); err != nil {
		t.Fatalf("clearing grants: %v", err)
	}
	if n := queryInt(t, `SELECT count(*) FROM user_site_grants WHERE user_id = $1`, user.ID); n != 0 {
		t.Errorf("%d grant rows remain after clearing", n)
	}
}
