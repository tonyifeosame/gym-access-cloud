package models

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Integration credentials, as the console API exposes them.
//
// THE PLAINTEXT SECRET APPEARS ON EXACTLY ONE STRUCT IN THIS FILE, and only on
// the way out of the call that created it. Nothing that a list or a read
// endpoint serialises carries it, and the database package never returns one
// except from issue and rotate -- the same discipline SiteCredential already
// keeps for the site provisioning key.

// Credential string shape.
//
//	atp_live_<64 hex>
//	└┬┘ └─┬┘ └───┬──┘
//	 │    │      └── 32 bytes from crypto/rand
//	 │    └───────── environment; a test key cannot authenticate a live server
//	 └────────────── class, beside ats_ (site) and atd_ (device)
//
// The class prefix has to be distinguishable at a glance from the other two, for
// the reason generateSiteKey already records: one gets pasted where another
// belongs, and the first thing anybody does is look at the front of it.
const (
	APIKeyClassPrefix = "atp_"

	// APIEnvironmentLive is the default. A deployment that configures nothing
	// accepts live credentials only, which is the safe direction: a test key
	// never works unless somebody deliberately opted in.
	APIEnvironmentLive = "live"

	// APIEnvironmentTest is for staging. Credentials minted here are refused by
	// a live deployment by shape, before any database lookup.
	APIEnvironmentTest = "test"

	// APIKeySecretHexLength is the hex length of the random half.
	APIKeySecretHexLength = 64

	// APIKeyPrefixLength is how much of the string is kept for display:
	// "atp_live_" plus eight hex characters.
	APIKeyPrefixLength = 17
)

// APIEnvironments is the closed set the environment column accepts.
var APIEnvironments = map[string]bool{
	APIEnvironmentLive: true,
	APIEnvironmentTest: true,
}

// apiKeyPattern is the full credential shape, anchored.
var apiKeyPattern = regexp.MustCompile(`^atp_(live|test)_[0-9a-f]{64}$`)

// Credential policy.
const (
	// MaxAPICredentialsPerCompany caps live credentials per tenant.
	//
	// COUNTS NON-REVOKED, NON-SUPERSEDED ROWS. A credential in its rotation
	// grace window does not count, so a company sitting at the cap can still
	// rotate -- a cap that deadlocks the operation you reach for during an
	// incident is worse than no cap.
	MaxAPICredentialsPerCompany = 20

	// DefaultAPICredentialLifetime is applied when the caller names no expiry.
	//
	// A year, and not "never". A non-expiring credential is a deliberate choice
	// somebody makes rather than the path of least resistance.
	DefaultAPICredentialLifetime = 365 * 24 * time.Hour

	// DefaultAPIKeyRotationGrace is how long the superseded credential keeps
	// working when the caller names no window.
	//
	// Seventy-two hours: long enough to deploy on a working day without being so
	// long that a rotation performed because a key leaked leaves it live for a
	// week. Zero is permitted and is the right choice for exactly that case.
	DefaultAPIKeyRotationGrace = 72 * time.Hour

	// MaxAPIKeyRotationGrace bounds the window. Beyond this, "rotated" stops
	// meaning anything.
	MaxAPIKeyRotationGrace = 30 * 24 * time.Hour

	// MaxAPICredentialNameLength matches the column.
	MaxAPICredentialNameLength = 80

	// MaxAPICredentialReasonLength matches revoked_reason.
	MaxAPICredentialReasonLength = 160
)

// Errors surfaced by the credential store. Handlers map these to status codes;
// anything else coming back is a server fault and is reported as one.
var (
	ErrAPICredentialNotFound = errors.New("integration credential not found")

	// ErrAPICredentialLimit is the per-company cap. A 409 rather than a 400: the
	// request is well formed and the platform is refusing on state.
	ErrAPICredentialLimit = fmt.Errorf(
		"this company already has %d live integration credentials", MaxAPICredentialsPerCompany)

	ErrAPICredentialNameRequired = errors.New("a name is required")
	ErrAPICredentialNameTooLong  = fmt.Errorf(
		"name must be %d characters or fewer", MaxAPICredentialNameLength)
	ErrAPICredentialNameTaken = errors.New(
		"a live integration credential with that name already exists")

	ErrAPICredentialRevoked = errors.New("this credential has already been revoked")

	// ErrAPICredentialSuperseded is returned when a rotation names a credential
	// that has already been rotated. Rotating a superseded row would branch the
	// chain and leave two replacements for one original.
	ErrAPICredentialSuperseded = errors.New("this credential has already been rotated")

	ErrAPICredentialExpiryInPast = errors.New("expires_at must be in the future")
	ErrAPIKeyGraceTooLong        = fmt.Errorf(
		"the rotation grace window must be %d days or fewer",
		int(MaxAPIKeyRotationGrace.Hours()/24))

	ErrAPICredentialSiteUnknown = errors.New("one of the named sites is not in this company")

	ErrUnknownEnvironment = errors.New("environment must be live or test")
)

// ValidAPIKeyShape reports whether a presented string could be a credential.
//
// Checked before the database is touched. A caller sending a session cookie
// value or a site key into the Authorization header should be refused without
// costing a query, and the shape check is what makes an environment mismatch
// visible before a lookup that would have missed anyway.
func ValidAPIKeyShape(key string) bool {
	return apiKeyPattern.MatchString(key)
}

// APIKeyEnvironment extracts the environment segment from a credential.
//
// Returns "" when the string is not credential-shaped, so a caller that skipped
// ValidAPIKeyShape cannot accidentally read a live environment out of nonsense.
func APIKeyEnvironment(key string) string {
	if !ValidAPIKeyShape(key) {
		return ""
	}
	rest := strings.TrimPrefix(key, APIKeyClassPrefix)
	environment, _, found := strings.Cut(rest, "_")
	if !found {
		return ""
	}
	return environment
}

// NormaliseEnvironment resolves a configured environment name.
//
// Anything unset defaults to live. Anything set and unrecognised is an ERROR
// rather than a silent fallback: a typo in the variable that decides whether
// test credentials work must not be what enables them.
func NormaliseEnvironment(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return APIEnvironmentLive, nil
	}
	if !APIEnvironments[value] {
		return "", fmt.Errorf("%w: got %q", ErrUnknownEnvironment, value)
	}
	return value, nil
}

// APICredential is an integration credential as the console lists it.
//
// There is no secret field of any kind. The hash is read and written inside the
// database package and never carried here, so no handler can serialise it by
// accident -- the same property models.User has for a password hash.
type APICredential struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	KeyPrefix   string `json:"key_prefix"`

	Scopes []string `json:"scopes"`

	// Sites is empty when the credential reaches every site in the company.
	// AllSites says which of the two an empty list means, so a console does not
	// have to infer it.
	Sites    []SiteGrant `json:"sites"`
	AllSites bool        `json:"all_sites"`

	CreatedByEmail string    `json:"created_by_email,omitempty"`
	CreatedAt      time.Time `json:"created_at"`

	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`

	// Rotation. SupersededAt is set on the OLD credential; GraceExpiresAt is
	// when it stops authenticating.
	SupersededAt   *time.Time `json:"superseded_at,omitempty"`
	GraceExpiresAt *time.Time `json:"grace_expires_at,omitempty"`
	SupersededByID string     `json:"superseded_by,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	RevokedReason  string     `json:"revoked_reason,omitempty"`
	RevokedByEmail string     `json:"revoked_by_email,omitempty"`

	// Status is the resolved lifecycle state, computed rather than stored:
	// ACTIVE, IN_GRACE, EXPIRED or REVOKED. A console rendering four boolean
	// columns and asking the reader to combine them is a console that will be
	// misread.
	Status string `json:"status"`
}

// Credential lifecycle states, as reported by APICredential.Status.
const (
	APICredentialActive   = "ACTIVE"
	APICredentialInGrace  = "IN_GRACE"
	APICredentialExpired  = "EXPIRED"
	APICredentialRevokedS = "REVOKED"
)

// APICredentialIdentity is the authenticated caller behind a public API
// request, the integration counterpart to OperatorIdentity and DeviceIdentity.
//
// NOTHING CONSTRUCTS ONE FROM A REQUEST YET. AuthenticateAPICredential produces
// it and the tests exercise it; the middleware that would put it in a gin
// context belongs with the public routes, which do not exist.
//
// CompanyID is the ONLY source of tenancy for a public request. It comes from
// the credential row and from nowhere else -- no query parameter, no header and
// no body field may influence it.
type APICredentialIdentity struct {
	ID       int64
	PublicID string

	CompanyID int64

	Name        string
	KeyPrefix   string
	Environment string

	// Scopes is the stored, already-expanded set. A permission check is a
	// membership test against this slice with no inference in it.
	Scopes []string

	// SiteIDs restricts the credential. EMPTY MEANS EVERY SITE IN THE COMPANY,
	// matching the operator grant rule -- absence is the default rather than a
	// wildcard row, so adding a site does not require revisiting the credential.
	SiteIDs []int64
}

// HasScope reports whether the credential carries a scope.
//
// Membership only. Implication was expanded at issue time precisely so that
// this function cannot be the place an inference goes wrong.
func (i *APICredentialIdentity) HasScope(scope string) bool {
	for _, held := range i.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

// ReachesSite reports whether the credential may act on a site.
func (i *APICredentialIdentity) ReachesSite(siteID int64) bool {
	if len(i.SiteIDs) == 0 {
		return true
	}
	for _, id := range i.SiteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}

// APICredentialIssued is a freshly minted credential, returned ONCE.
//
// Secret is populated only by IssueAPICredential and RotateAPICredential.
// Nothing else in the database package can produce one, because nothing else
// knows it: the plaintext exists in one local variable, is hashed, and is
// returned on this struct without ever being stored.
type APICredentialIssued struct {
	APICredential

	// Secret is the plaintext. ShownOnce is not decoration -- it is what the
	// console renders the warning from, and it is always true.
	Secret    string `json:"secret"`
	ShownOnce bool   `json:"shown_once"`
}

// APICredentialRequest is the body of POST /console/api-credentials.
//
// Only presence is validated by the binding tag. Substance -- the scope set
// being known, the sites being in this company, the expiry being in the future
// -- is checked in the database package, so the same rules apply however a
// credential is created and there is one place to read them.
type APICredentialRequest struct {
	Name   string   `json:"name" binding:"required"`
	Scopes []string `json:"scopes" binding:"required"`

	// SiteIDs are site public_ids. Empty or absent means every site in the
	// company, matching the operator grant rule.
	SiteIDs []string `json:"site_ids,omitempty"`

	// ExpiresAt is optional. Absent applies DefaultAPICredentialLifetime;
	// explicit null means never.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// Environment is optional and defaults to the deployment's own. Naming the
	// other one is refused rather than ignored -- a deployment cannot mint a
	// credential it would then refuse to accept.
	Environment string `json:"environment,omitempty"`
}

// APICredentialRotateRequest is the body of POST
// /console/api-credentials/{id}/rotate.
type APICredentialRotateRequest struct {
	// GraceSeconds is how long the superseded credential keeps working. Nil
	// applies DefaultAPIKeyRotationGrace; 0 is a hard cutover and is the right
	// choice for a compromised key.
	//
	// A pointer so "not specified" and "zero" are different requests. They mean
	// opposite things here, which is exactly the case a plain int cannot carry.
	GraceSeconds *int `json:"grace_seconds,omitempty"`

	Reason string `json:"reason,omitempty"`
}

// APICredentialRevokeRequest is the body of DELETE
// /console/api-credentials/{id} and of the bulk revoke.
type APICredentialRevokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

// APICredentialList is the console list response.
type APICredentialList struct {
	Count       int             `json:"count"`
	Credentials []APICredential `json:"credentials"`
}

// APICredentialRevokedAll reports a bulk revocation.
type APICredentialRevokedAll struct {
	Revoked int `json:"revoked"`
}

// APICredentialUsageDay is one day of one class.
type APICredentialUsageDay struct {
	Day      string `json:"day"` // YYYY-MM-DD
	Class    string `json:"class"`
	Requests int64  `json:"requests"`
	Refusals int64  `json:"refusals"`
}

// APICredentialUsage is the answer to "is this key still in use, and by what".
type APICredentialUsage struct {
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	KeyPrefix  string                  `json:"key_prefix"`
	LastUsedAt *time.Time              `json:"last_used_at,omitempty"`
	LastUsedIP string                  `json:"last_used_ip,omitempty"`
	Days       []APICredentialUsageDay `json:"days"`
}

// ValidateAPICredentialName applies the name policy.
func ValidateAPICredentialName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrAPICredentialNameRequired
	}
	if len(trimmed) > MaxAPICredentialNameLength {
		return "", ErrAPICredentialNameTooLong
	}
	return trimmed, nil
}
