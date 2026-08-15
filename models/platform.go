package models

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Platform administration (migrations/015_platform_and_applications.sql).
//
// THE THIRD CREDENTIAL CLASS. A platform administrator is not an operator with
// more privileges -- it is a different identity, in a different table, with
// different sessions and a different cookie, and it cannot authenticate to a
// console route any more than a site key can.
//
// That separation is what preserved the tenancy invariant while closing the
// audit's first blocker. 008_operator_accounts.sql argued that one company per
// operator is what makes the boundary checkable, because every function in
// database/ takes a companyID and every query filters on it. Widening an
// operator to reach several companies would have dissolved that everywhere; a
// separate identity that reaches `companies` and nothing inside them does not
// touch it at all.
//
// WHAT THIS IDENTITY DELIBERATELY CANNOT DO: read a tenant's people,
// credentials, events, terminals or site keys. Enforced by there being no such
// route and no such query, not by a role check -- a support credential that can
// read every customer's biometric roster is the most valuable secret on the
// installation, and the safest way to not leak it is to never be able to load it.

// PlatformAdmin is an administrator account as the platform sees it.
//
// No password field of any kind, matching models.User. A type that cannot carry
// a hash cannot serialise one by accident.
type PlatformAdmin struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	FullName    string     `json:"full_name"`
	Active      bool       `json:"active"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// NewPlatformAdmin is the input to creating one.
type NewPlatformAdmin struct {
	Email    string
	FullName string
	Password string
}

// PlatformIdentity is the authenticated administrator behind a request.
//
// The mirror of OperatorIdentity, and deliberately NOT the same type: a handler
// that took either would be a handler that could be mounted on the wrong tree,
// and the compiler is a better guard against that than a comment.
type PlatformIdentity struct {
	SessionID     int64
	AdminID       int64
	AdminPublicID string
	Email         string
	FullName      string
	Active        bool

	CSRFTokenHash string
	// CSRFToken is recomputed from the session token on every request, never
	// read from storage -- only the hash is stored. Same construction the
	// operator session uses, and for the same reason: /me has to hand it back
	// after a page reload.
	CSRFToken        string
	ExpiresInSeconds int
}

// PlatformCompany is a tenant as a platform administrator sees it.
//
// COUNTS, NEVER CONTENTS. The counts answer "is this tenant healthy and how big
// is it", which is what onboarding and support need. There is deliberately no
// field here that could carry a person, a credential, an event or a site key,
// because there is no platform route that loads one.
type PlatformCompany struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	ContactEmail string `json:"contact_email,omitempty"`
	Timezone     string `json:"timezone"`
	Active       bool   `json:"active"`

	// NULL means keep for ever, which is the default. Rendered as null rather
	// than as a number so the console can say "indefinite" instead of showing a
	// retention period nobody chose.
	EventRetentionDays *int `json:"event_retention_days"`
	AuditRetentionDays *int `json:"audit_retention_days"`

	SiteCount     int `json:"site_count"`
	TerminalCount int `json:"terminal_count"`
	PersonCount   int `json:"person_count"`
	OperatorCount int `json:"operator_count"`

	CreatedAt time.Time `json:"created_at"`
}

// PlatformCompanyRequest creates a tenant.
type PlatformCompanyRequest struct {
	Name string `json:"name"`

	// Optional. Derived from the name when absent, because refusing
	// "Acme Logistics" to enforce a format the platform invented is less useful
	// than deriving "acme-logistics" from it.
	Slug         string `json:"slug,omitempty"`
	ContactEmail string `json:"contact_email,omitempty"`
	Timezone     string `json:"timezone,omitempty"`
}

// PlatformCompanyUpdateRequest changes a tenant. Every field is optional.
//
// THERE IS NO SLUG FIELD. It appears in the bootstrap environment variable and
// in operator-facing URLs, so renaming it would silently break whatever refers
// to the company by it. The display name is what somebody wants to change after
// a rebrand, and that is here.
type PlatformCompanyUpdateRequest struct {
	Name               *string `json:"name,omitempty"`
	ContactEmail       *string `json:"contact_email,omitempty"`
	Timezone           *string `json:"timezone,omitempty"`
	Active             *bool   `json:"active,omitempty"`
	DeactivatedReason  *string `json:"deactivated_reason,omitempty"`
	EventRetentionDays *int    `json:"event_retention_days,omitempty"`
	AuditRetentionDays *int    `json:"audit_retention_days,omitempty"`
}

// PlatformFirstOperatorRequest mints a company's initial OWNER.
//
// Password is optional: when absent an INVITATION is issued instead and the
// response carries a single-use link rather than a credential. That is the
// preferred path and the one the console offers -- see PPL-02. Supplying a
// password is kept for an environment that cannot deliver email, and the
// response says plainly that it must be communicated separately.
type PlatformFirstOperatorRequest struct {
	Email    string `json:"email"`
	FullName string `json:"full_name,omitempty"`
	Password string `json:"password,omitempty"`
}

// PlatformSessionResponse is what login and /me return.
type PlatformSessionResponse struct {
	Admin                   PlatformAdmin `json:"admin"`
	CSRFToken               string        `json:"csrf_token"`
	SessionExpiresAt        time.Time     `json:"session_expires_at"`
	SessionExpiresInSeconds int           `json:"session_expires_in_seconds"`
}

// ---------------------------------------------------------------------------
// Slugs
// ---------------------------------------------------------------------------

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)

// ErrInvalidSlug reports a slug the schema would refuse.
var ErrInvalidSlug = errors.New(
	"slug must be 3-64 characters of a-z, 0-9 and hyphen, starting and ending alphanumeric")

// NormalizeSlug derives a storable slug from arbitrary text.
//
// Lowercased, with runs of anything else collapsed to a single hyphen. A caller
// typing "Acme Logistics (UK)" gets "acme-logistics-uk" rather than a validation
// error, which is the same courtesy NormalizeCategoryCode extends.
func NormalizeSlug(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))

	var b strings.Builder
	lastHyphen := false
	for _, r := range lower {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen && b.Len() > 0 {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	return strings.Trim(b.String(), "-")
}

// ValidateSlug reports whether a normalized slug is storable.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return ErrInvalidSlug
	}
	return nil
}
