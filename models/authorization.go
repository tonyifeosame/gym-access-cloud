package models

import (
	"errors"
	"time"
)

// The authorization engine's types (014_authorization_engine.sql).
//
// THE RULE THIS FILE EXISTS TO ENFORCE, stated once:
//
//	Absence of permission is not permission.
//
// A platform whose default is "allow unless denied" cannot be made safe by
// adding rules, because the failure mode of every bug is admission. The engine
// below denies unless something says otherwise, and the one place that could
// have been tempted to relax it -- an empty rule set -- is the case it is most
// important for.
//
// The previous behaviour was the opposite and was not a policy at all: nothing
// read the permissions table, so every active person with a bound credential
// opened every terminal in their company, permanently.

// Permission scopes. A rule names exactly one.
const (
	// ScopeCompany reaches every terminal the company has, including ones
	// installed after the rule was written. The broadest grant, and what the
	// 014 back-fill gave every existing person so that no deployed door stopped
	// working on upgrade.
	ScopeCompany = "COMPANY"
	// ScopeSite reaches every terminal at one site.
	ScopeSite = "SITE"
	// ScopeTerminal reaches exactly one terminal.
	ScopeTerminal = "TERMINAL"
)

// Permission effects.
const (
	EffectAllow = "ALLOW"
	// EffectDeny beats any ALLOW at any scope. Exclusion cannot be expressed by
	// allow-only rules without enumerating everyone who is not excluded, and
	// re-enumerating them whenever anyone joins.
	EffectDeny = "DENY"
)

// PermissionScopes and PermissionEffects are the closed sets, validated in Go so
// a bad value is a 400 rather than a constraint violation.
var (
	PermissionScopes  = map[string]bool{ScopeCompany: true, ScopeSite: true, ScopeTerminal: true}
	PermissionEffects = map[string]bool{EffectAllow: true, EffectDeny: true}
)

// Authorization errors.
var (
	ErrPermissionNotFound  = errors.New("permission not found")
	ErrInvalidScope        = errors.New("scope must be COMPANY, SITE or TERMINAL")
	ErrInvalidEffect       = errors.New("effect must be ALLOW or DENY")
	ErrScopeTargetRequired = errors.New("a SITE or TERMINAL scope must name one")
	ErrScopeTargetRefused  = errors.New("a COMPANY scope must not name a site or terminal")
	ErrScheduleNotFound    = errors.New("schedule not found")
	ErrScheduleNameTaken   = errors.New("a schedule with that name already exists")
	ErrScheduleNoWindows   = errors.New("a schedule must have at least one window")
	ErrInvalidWindow       = errors.New("a schedule window needs a day mask and two different times")

	// ErrScheduleInUse refuses to delete a schedule that rules still reference.
	//
	// The foreign key is ON DELETE SET NULL, which on a soft delete would
	// silently WIDEN every rule that used it: a permission restricted to office
	// hours would become one with no time restriction at all. Refusing is the
	// only answer that cannot quietly grant access.
	ErrScheduleInUse = errors.New("schedule is still used by permissions")
)

// Permission is one rule about one person.
type Permission struct {
	ID        string `json:"id"`
	PersonID  string `json:"person_id"`
	ScopeType string `json:"scope_type"`

	// Exactly one of these is set, matching ScopeType. Public ids, because this
	// type crosses the API boundary.
	SiteID       string `json:"site_id,omitempty"`
	SiteName     string `json:"site_name,omitempty"`
	DeviceSerial string `json:"device_serial,omitempty"`
	DeviceName   string `json:"device_name,omitempty"`

	// Application narrows the rule to one capability. Empty means every
	// capability -- a person allowed at a terminal is allowed at whatever that
	// terminal does, unless somebody says otherwise.
	Application string `json:"application,omitempty"`

	Effect string `json:"effect"`

	ScheduleID   string `json:"schedule_id,omitempty"`
	ScheduleName string `json:"schedule_name,omitempty"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PermissionRequest is the body of a console permission create or update.
type PermissionRequest struct {
	ScopeType    string     `json:"scope_type"`
	SiteID       string     `json:"site_id,omitempty"`
	DeviceSerial string     `json:"device_serial,omitempty"`
	Application  string     `json:"application,omitempty"`
	Effect       string     `json:"effect,omitempty"`
	ScheduleID   string     `json:"schedule_id,omitempty"`
	StartsAt     *time.Time `json:"starts_at,omitempty"`
	EndsAt       *time.Time `json:"ends_at,omitempty"`
	Active       *bool      `json:"active,omitempty"`
}

// Validate checks a permission request's internal consistency before it reaches
// the database, so a caller gets a message naming what is wrong.
func (r PermissionRequest) Validate() error {
	if !PermissionScopes[r.ScopeType] {
		return ErrInvalidScope
	}
	if r.Effect != "" && !PermissionEffects[r.Effect] {
		return ErrInvalidEffect
	}

	switch r.ScopeType {
	case ScopeCompany:
		if r.SiteID != "" || r.DeviceSerial != "" {
			return ErrScopeTargetRefused
		}
	case ScopeSite:
		if r.SiteID == "" || r.DeviceSerial != "" {
			return ErrScopeTargetRequired
		}
	case ScopeTerminal:
		if r.DeviceSerial == "" || r.SiteID != "" {
			return ErrScopeTargetRequired
		}
	}

	if r.StartsAt != nil && r.EndsAt != nil && !r.EndsAt.After(*r.StartsAt) {
		return errors.New("ends_at must be after starts_at")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

// Day-of-week bits, matching the convention 002 established and the console
// renders. Mon=1 through Sun=64; 127 is every day.
const (
	DayMonday    = 1
	DayTuesday   = 2
	DayWednesday = 4
	DayThursday  = 8
	DayFriday    = 16
	DaySaturday  = 32
	DaySunday    = 64
	DayEveryDay  = 127
)

// Schedule is a named, reusable set of time windows.
type Schedule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Timezone empty means "the timezone of the site the terminal is at", which
	// is the common case and needs no configuration. Set it explicitly for a
	// company that runs one shift pattern across several zones.
	Timezone string `json:"timezone,omitempty"`

	Windows []ScheduleWindow `json:"windows"`

	// PermissionCount lets the console say what a schedule costs before it is
	// edited or removed.
	PermissionCount int `json:"permission_count"`

	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScheduleWindow is one span within a schedule.
//
// StartTime and EndTime are "HH:MM" or "HH:MM:SS" wall-clock times in the
// schedule's zone. A window where End is not after Start CROSSES MIDNIGHT: a
// night shift 22:00-06:00 is one window, and DaysOfWeek names the day it starts
// on. Splitting it into two rows would make Sunday night's shift look like
// Monday's.
type ScheduleWindow struct {
	DaysOfWeek int    `json:"days_of_week"`
	StartTime  string `json:"start_time"`
	EndTime    string `json:"end_time"`
}

// CrossesMidnight reports whether this window runs into the following day.
func (w ScheduleWindow) CrossesMidnight() bool {
	return w.EndTime <= w.StartTime
}

// ScheduleRequest is the body of a console schedule create or update.
type ScheduleRequest struct {
	Name        string           `json:"name"`
	Description *string          `json:"description,omitempty"`
	Timezone    *string          `json:"timezone,omitempty"`
	Active      *bool            `json:"active,omitempty"`
	Windows     []ScheduleWindow `json:"windows"`
}

// ---------------------------------------------------------------------------
// The decision
// ---------------------------------------------------------------------------

// Reason codes. These travel to the console, to the event trail and to the
// terminal, and they are the difference between "denied" and an answer an
// operator can act on.
//
// Open set by design -- an application may define its own -- but these are the
// platform's, and every one of them is a distinct thing an operator would do
// something different about.
const (
	ReasonAllowed             = "ALLOWED"
	ReasonNoPermission        = "NO_PERMISSION"
	ReasonExplicitDeny        = "EXPLICIT_DENY"
	ReasonOutsideSchedule     = "OUTSIDE_SCHEDULE"
	ReasonPermissionExpired   = "PERMISSION_EXPIRED"
	ReasonPermissionNotYet    = "PERMISSION_NOT_YET_VALID"
	ReasonPersonInactive      = "PERSON_INACTIVE"
	ReasonPersonUnknown       = "PERSON_UNKNOWN"
	ReasonCredentialUnknown   = "CREDENTIAL_UNKNOWN"
	ReasonCredentialRevoked   = "CREDENTIAL_REVOKED"
	ReasonCredentialSuspended = "CREDENTIAL_SUSPENDED"
	ReasonCredentialExpired   = "CREDENTIAL_EXPIRED"
	ReasonCredentialNotYet    = "CREDENTIAL_NOT_YET_VALID"
	ReasonApplicationOff      = "APPLICATION_NOT_ENABLED"
	ReasonTerminalDisabled    = "TERMINAL_DISABLED"
	ReasonSiteInactive        = "SITE_INACTIVE"
	ReasonCompanyInactive     = "COMPANY_INACTIVE"
	ReasonOfflinePolicy       = "OFFLINE_POLICY"
)

// AccessDecision is the outcome of evaluating one presentation.
//
// Carries the reason ALWAYS, including on a grant. An audit trail that records
// why somebody was refused but not why they were admitted answers half the
// question a security review asks.
type AccessDecision struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason"`

	// PersonID and CredentialID are empty when the presentation matched nobody,
	// which is a real outcome and must still be recorded.
	PersonID     string `json:"person_id,omitempty"`
	PersonName   string `json:"person_name,omitempty"`
	ExternalID   string `json:"external_id,omitempty"`
	CredentialID string `json:"credential_id,omitempty"`

	// Application is the capability the decision was made under, which is what
	// the terminal was assigned to do.
	Application string `json:"application,omitempty"`

	// MatchedPermission names the rule that decided it, for the console's "why".
	// Empty on a deny-by-default, which is itself the answer.
	MatchedPermission string `json:"matched_permission,omitempty"`

	DecidedAt time.Time `json:"decided_at"`
}

// AccessRequest is one presentation to be evaluated.
type AccessRequest struct {
	// ExternalID is what the terminal read. The platform resolves it to a person
	// rather than trusting the terminal's own view of who that is.
	ExternalID string

	// CredentialID, where the terminal can name the credential it matched.
	// Empty for the legacy path, which knew only an external id.
	CredentialID string

	// DeviceID is the authenticated terminal. Never taken from a request body:
	// a terminal must not be able to ask "would you admit this person at some
	// other door".
	DeviceID int64

	// Application the terminal is acting under. Empty means the terminal's
	// configured mode is used.
	Application string

	At time.Time
}

// AccessEvaluationRequest asks the engine what it would decide.
//
// The console's "would this person get in at this terminal, and why". The
// terminal is NOT in the body -- it is the :serial in the path, already resolved
// and authorized by RequireTerminalGrant, so an operator cannot preview a
// decision at a terminal they are not scoped to.
//
// At makes a schedule testable without waiting for Tuesday. Absent means now.
type AccessEvaluationRequest struct {
	ExternalID   string     `json:"external_id"`
	CredentialID string     `json:"credential_id,omitempty"`
	Application  string     `json:"application,omitempty"`
	At           *time.Time `json:"at,omitempty"`
}

// OfflinePolicy is what a terminal does when it cannot reach the platform.
//
// EXPLICIT, PER SITE, AND WITH NO SAFE DEFAULT THE PLATFORM CAN PICK. A
// warehouse that must keep running through an outage and a secure store that
// must not are both correct, and neither is a sensible default for the other.
// The platform therefore requires a site to state one, and states the
// consequence beside it in the console.
const (
	// OfflineDenyAll refuses everything while offline. Safest, and wrong for
	// any site where being unable to reach the network must not trap people or
	// stop work.
	OfflineDenyAll = "DENY_ALL"
	// OfflineCachedGrace honours the terminal's cached authorization for a
	// bounded window, then falls back to DENY_ALL. The default a new site is
	// created with, because it degrades rather than failing instantly, and the
	// window is visible.
	OfflineCachedGrace = "CACHED_GRACE"
	// OfflineCachedIndefinite honours the cache with no expiry. What every
	// terminal does today, and what a customer must opt into knowingly: a
	// terminal cut off from the platform keeps admitting whoever it last knew
	// about, including people revoked since.
	OfflineCachedIndefinite = "CACHED_INDEFINITE"
)

// OfflinePolicies is the closed set.
var OfflinePolicies = map[string]bool{
	OfflineDenyAll:          true,
	OfflineCachedGrace:      true,
	OfflineCachedIndefinite: true,
}

// IsOfflinePolicy reports whether a value is one the platform understands.
func IsOfflinePolicy(name string) bool { return OfflinePolicies[name] }
