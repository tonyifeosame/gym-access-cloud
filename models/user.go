package models

import (
	"errors"
	"fmt"
	"time"
)

// Operator identity (migrations/008_operator_accounts.sql).
//
// An operator is a HUMAN with a dashboard login, as opposed to the two machine
// credentials this system already has: the site API key, which provisions
// terminals, and the per-device key, which authenticates one terminal.
//
// The site API key must never reach a browser. It is the provisioning secret --
// whoever holds it can register a terminal and rotate any device credential at
// that site -- so the operator credential defined here is what a dashboard
// authenticates with instead.
//
// Note what is NOT in this file: no password hash and no session token appear
// on any struct that a handler can serialise. The bcrypt hash never leaves the
// database package, and the plaintext session token exists exactly once, in
// SessionCredentials, on its way into a Set-Cookie header.

// Operator roles. The stored value is a label; the matrix of what each role may
// call is enforced in Go at the routes, not in the schema.
const (
	RoleOwner   = "OWNER"
	RoleAdmin   = "ADMIN"
	RoleManager = "MANAGER"
	RoleViewer  = "VIEWER"
)

// OperatorRoles is the closed set the users_role_check constraint accepts.
// Validated in Go as well so a bad role is a 400 rather than a constraint
// violation the handler cannot tell apart from the database being down -- the
// same reasoning as the oneof tags on DeviceRegistrationRequest.
var OperatorRoles = map[string]bool{
	RoleOwner:   true,
	RoleAdmin:   true,
	RoleManager: true,
	RoleViewer:  true,
}

// Password policy.
//
// The maximum is not a preference. bcrypt hashes at most 72 bytes and silently
// ignores everything after them, so a longer passphrase would be accepted at
// signup and then authenticate on its first 72 bytes alone. Rejecting it is
// honest; truncating it quietly is not.
const (
	MinPasswordLength = 12
	MaxPasswordBytes  = 72
)

// Errors surfaced by the operator store. Handlers map these to status codes;
// anything else coming back is a server fault and must be reported as one.
var (
	// ErrInvalidCredentials covers every reason a login may fail that the
	// caller is not entitled to tell apart: unknown address, wrong password,
	// disabled account, disabled company. One error, one message, one status.
	ErrInvalidCredentials = errors.New("invalid email or password")

	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d bytes", MaxPasswordBytes)
	ErrInvalidEmail     = errors.New("email address is not valid")
	ErrInvalidRole      = errors.New("role must be one of OWNER, ADMIN, MANAGER, VIEWER")
	ErrUserNotFound     = errors.New("operator not found")
	ErrSiteNotFound     = errors.New("site not found in this company")
)

// AccountLockedError reports that an account is temporarily locked after
// repeated failed logins. It carries how long to wait so the handler can answer
// with Retry-After rather than leaving the operator to guess.
//
// A duration rather than an instant, deliberately. Every timestamp column in
// this schema is TIMESTAMP WITHOUT TIME ZONE and CURRENT_TIMESTAMP writes the
// DATABASE server's local wall clock, which the driver then hands back to Go
// labelled UTC. Comparing such a value against time.Now() is wrong by the
// server's UTC offset -- an hour, on the deployment this was written against.
// The remaining time is therefore computed in SQL, where both sides of the
// comparison come from the same clock, and arrives here already resolved.
type AccountLockedError struct {
	RetryAfterSeconds int
}

func (e *AccountLockedError) Error() string {
	return fmt.Sprintf("account is temporarily locked for another %d seconds",
		e.RetryAfterSeconds)
}

// RetryAfter is the whole number of seconds a caller should wait, never less
// than one -- a Retry-After of 0 invites an immediate retry into the same lock.
func (e *AccountLockedError) RetryAfter() int {
	if e.RetryAfterSeconds < 1 {
		return 1
	}
	return e.RetryAfterSeconds
}

// User is an operator account as the API exposes it.
//
// There is deliberately no password field of any kind. The bcrypt hash is read
// and written inside the database package and is never carried on this struct,
// so no handler can serialise it by accident.
type User struct {
	ID          int64      `json:"id"`
	PublicID    string     `json:"public_id"`
	CompanyID   int64      `json:"company_id"`
	Email       string     `json:"email"`
	FullName    string     `json:"full_name"`
	Role        string     `json:"role"`
	Active      bool       `json:"active"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// NewUser is the input to CreateUser. Password is plaintext on the way in and
// is hashed before it reaches the database; it is never stored or returned.
type NewUser struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// SiteGrant is one site an operator may act on, resolved for display. Site
// grants live inside a single company -- there is no cross-company operator.
type SiteGrant struct {
	SiteID       int64  `json:"-"`
	SitePublicID string `json:"site_id"`
	SiteName     string `json:"site_name"`
}

// SessionCredentials is what a successful login produces.
//
// Token goes into the session cookie and is never written to a response body.
// CSRFToken goes into the response BODY and never into a cookie: in a
// split-origin deployment (app.accesslink.store calling api.accesslink.store)
// the dashboard's JavaScript cannot read a cookie scoped to the API host, so the
// double-submit cookie pattern would silently fail.
//
// ExpiresInSeconds is a DURATION, resolved by the database. The absolute
// timestamps are TIMESTAMP WITHOUT TIME ZONE holding the database server's local
// wall clock, so a client reading them as UTC would be wrong by that server's
// offset. Anything a caller is meant to act on is expressed as a remaining
// duration, and the handler turns that into a correct absolute instant.
type SessionCredentials struct {
	Token             string    `json:"-"`
	CSRFToken         string    `json:"csrf_token"`
	PublicID          string    `json:"session_id"`
	IdleExpiresAt     time.Time `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time `json:"expires_at"`
	ExpiresInSeconds  int       `json:"expires_in_seconds"`
}

// OperatorIdentity is the authenticated caller behind a dashboard request, the
// operator counterpart to DeviceIdentity.
//
// Middleware resolves this once per request and puts it in the gin context;
// handlers read it from there and never re-derive identity from the cookie.
// CompanyID is set under the same context key the site-key middleware uses, so
// every existing company-scoped query works unchanged for an operator caller.
type OperatorIdentity struct {
	UserID       int64
	UserPublicID string
	CompanyID    int64
	// The operator's company, carried along because the session lookup already
	// joins it. /me would otherwise need a second query to name the tenant.
	CompanyPublicID string
	CompanyName     string
	CompanySlug     string

	Email           string
	FullName        string
	Role            string
	Active          bool
	SessionID       int64
	SessionPublicID string

	// CSRFTokenHash is the stored form of the session's synchronizer token, for
	// the middleware's constant-time comparison.
	CSRFTokenHash string
	// CSRFToken is the plaintext, recomputed from the presented session token
	// rather than stored. See deriveCSRFToken in database/sessions.go.
	CSRFToken string

	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	// ExpiresInSeconds is the remaining life of the session as a duration,
	// resolved by the database. See the note on SessionCredentials.
	ExpiresInSeconds int
}

// LoginRequest is the body of POST /api/v1/auth/login.
//
// Only presence is validated. A malformed address is not rejected as a bad
// request: it simply matches no account and is answered exactly like a wrong
// password, so the endpoint cannot be used to probe which addresses are even
// shaped like accounts here.
type LoginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// PasswordChangeRequest is the body of POST /api/v1/auth/password.
//
// The current password is required even though the caller already holds a valid
// session. A session is not proof of knowing the password -- an unattended
// browser is enough -- and changing a password is exactly the operation that
// must not be reachable without it.
type PasswordChangeRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// SessionInfo describes a live session without disclosing anything that could
// be replayed as one.
type SessionInfo struct {
	PublicID          string    `json:"session_id"`
	IssuedAt          time.Time `json:"issued_at"`
	LastUsedAt        time.Time `json:"last_used_at"`
	IdleExpiresAt     time.Time `json:"idle_expires_at"`
	AbsoluteExpiresAt time.Time `json:"expires_at"`
	IPAddress         string    `json:"ip_address,omitempty"`
}
