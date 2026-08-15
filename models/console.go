package models

import (
	"encoding/json"
	"time"
)

// Console resources: what the operator dashboard sees.
//
// These are PROJECTIONS, not the internal models. Two things make that
// necessary rather than tidy-minded:
//
//   - The site provisioning key registers terminals and rotates their device
//     credentials, so returning it to a browser would undo the entire reason
//     operator authentication exists. It appears in exactly ONE response type,
//     ConsoleSiteCredential, returned only by site creation and key rotation.
//     Nothing below carries it, and since 011_site_credentials.sql there is no
//     plaintext in the database to carry: the column holds a SHA-256 hash.
//
//   - models.Member carries fingerprint_template. Despite the name it holds a
//     credential LOCATOR (terminal:<serial>:slot:<n>), not biometric data, and
//     the frontend must model biometrics as an abstraction rather than parse
//     that string. The console reports only WHETHER a person has a credential.
//
// Everything here is domain-neutral. A person is a person, a terminal is a
// terminal, and nothing assumes what the company uses the platform for.

// ConsoleCompany is the tenant as its own operators see it.
type ConsoleCompany struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	ContactEmail string    `json:"contact_email,omitempty"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
}

// ConsoleSite is a location.
//
// NO api_key FIELD, EVER. See the note above; this is the one type where the
// omission is load-bearing rather than cosmetic.
type ConsoleSite struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address,omitempty"`
	Timezone    string    `json:"timezone"`
	Active      bool      `json:"active"`
	DeviceCount int       `json:"terminal_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// ConsolePerson is someone the platform knows about.
//
// Category maps to people.membership_type, which is a legacy column name from
// when this was a single-purpose product. It is free text and optional: a
// company doing visitor management has no "membership", and requiring one would
// be the product assuming a workflow.
//
// BiometricEnrolled is the entire biometric surface here -- a boolean. The
// credential itself is an abstraction the backend owns, so a proper credentials
// table can replace the current column without the dashboard noticing.
type ConsolePerson struct {
	ID                string    `json:"id"`
	ExternalID        string    `json:"external_id"`
	FullName          string    `json:"full_name"`
	Category          string    `json:"category,omitempty"`
	Active            bool      `json:"active"`
	BiometricEnrolled bool      `json:"biometric_enrolled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// TerminalDetail is one terminal, in full.
//
// DeviceInventory is embedded ANONYMOUSLY, so its fields marshal at the top
// level exactly as they do in the list. That is what makes this addition
// backwards compatible: the three fields the endpoint returned before --
// serial_number, application_mode, effective_applications -- are all still
// present at the same paths, and everything else is new alongside them.
//
// The two concepts stay distinct and are both needed to read a terminal
// honestly. Mode is what the terminal is ASSIGNED to do; Effective is what that
// resolves to right now, and it goes empty when the company disables the
// capability. The assignment is retained rather than rewritten, so a detail view
// showing only one of the two would be misleading in exactly the case that
// matters.
type TerminalDetail struct {
	DeviceInventory
	ApplicationMode       string   `json:"application_mode"`
	EffectiveApplications []string `json:"effective_applications"`
}

// ConsolePeoplePage is a page of people.
//
// Count is the size of THIS page and Total the size of the whole match, which
// is the pair a client needs to render "showing 50 of 1,284" and to know when
// to stop paging. Count keeps the meaning it had before pagination existed.
type ConsolePeoplePage struct {
	Count   int             `json:"count"`
	Total   int             `json:"total"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
	People  []ConsolePerson `json:"people"`
}

// ConsoleOperator is an operator account as the console lists it.
type ConsoleOperator struct {
	ID          string      `json:"id"`
	Email       string      `json:"email"`
	FullName    string      `json:"full_name"`
	Role        string      `json:"role"`
	Active      bool        `json:"active"`
	LastLoginAt *time.Time  `json:"last_login_at,omitempty"`
	Sites       []SiteGrant `json:"sites,omitempty"`
	AllSites    bool        `json:"all_sites"`
	CreatedAt   time.Time   `json:"created_at"`
}

// ConsoleApplications is the module configuration screen's payload: what this
// company has configured, and what the platform offers.
//
// Available is the catalog, so a dashboard does not have to hard-code the list
// of capabilities -- adding one to the platform makes it appear here.
type ConsoleApplications struct {
	Configured []CompanyApplication `json:"configured"`
	Enabled    []string             `json:"enabled"`
	Available  []string             `json:"available"`
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

// ConsolePersonRequest creates or updates a person.
//
// Active is a pointer so "not supplied" is distinguishable from false: on
// create it defaults to true, and on update an omitted field leaves the value
// alone. A plain bool would silently deactivate everyone who was edited without
// mentioning it.
type ConsolePersonRequest struct {
	ExternalID string `json:"external_id"`
	FullName   string `json:"full_name" binding:"required"`
	Category   string `json:"category,omitempty"`
	Active     *bool  `json:"active,omitempty"`
}

// ConsoleOperatorRequest creates an operator.
//
// PASSWORD IS OPTIONAL, AND OMITTING IT IS THE PREFERRED PATH (PPL-02). An
// absent password means the account is created with a credential nobody holds
// and the response carries a single-use invitation instead -- so the
// administrator creating the account never knows how to sign in as it.
//
// It was `binding:"required"` until that finding, which made the invitation flow
// unreachable through the API no matter what the store supported: gin refused
// the request before any handler ran. Supplying a password still works, for a
// deployment that cannot deliver a link at all.
type ConsoleOperatorRequest struct {
	Email    string   `json:"email" binding:"required"`
	FullName string   `json:"full_name" binding:"required"`
	Password string   `json:"password,omitempty"`
	Role     string   `json:"role" binding:"required"`
	SiteIDs  []string `json:"site_ids,omitempty"`
}

// ConsoleOperatorUpdateRequest changes an operator. Every field is optional;
// only what is supplied is applied.
type ConsoleOperatorUpdateRequest struct {
	Role     *string `json:"role,omitempty"`
	Active   *bool   `json:"active,omitempty"`
	Password *string `json:"password,omitempty"`
}

// ConsoleSiteGrantsRequest replaces an operator's site grants wholesale. An
// empty list means "not scoped", which is every site in the company.
type ConsoleSiteGrantsRequest struct {
	SiteIDs []string `json:"site_ids"`
}

// ConsoleApplicationRequest enables, disables or configures a capability.
type ConsoleApplicationRequest struct {
	Enabled  *bool           `json:"enabled,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// ConsoleTerminalModeRequest assigns what a terminal is for. MULTI_PURPOSE is
// accepted here and only here -- it is a device mode, never a company
// capability.
type ConsoleTerminalModeRequest struct {
	ApplicationMode string `json:"application_mode" binding:"required"`
}

// ConsoleSiteRequest creates a site.
//
// No api_key field in either direction. The key is GENERATED by the server --
// a caller-chosen provisioning secret is a caller-chosen weak provisioning
// secret -- and comes back once, in the response.
type ConsoleSiteRequest struct {
	Name    string `json:"name" binding:"required"`
	Address string `json:"address,omitempty"`
	// IANA zone, e.g. "Africa/Lagos". Describes where the HARDWARE stands, which
	// is a different question from the zone an operator reads timestamps in.
	// Empty defaults to UTC rather than to the API server's incidental location.
	Timezone string `json:"timezone,omitempty"`
}

// ConsoleSiteUpdateRequest changes a site's metadata. Every field is optional;
// only what is supplied is applied, so correcting an address cannot blank a
// timezone the caller never mentioned.
//
// `active` here is DEACTIVATION, which is reversible and is not retirement.
// It stops the site key and every terminal at the site from authenticating,
// and nothing is destroyed. DELETE is the one-way door.
type ConsoleSiteUpdateRequest struct {
	Name     *string `json:"name,omitempty"`
	Address  *string `json:"address,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
	Active   *bool   `json:"active,omitempty"`
}

// ConsoleSiteCreated is the ONLY response shape that carries a site key, and it
// is returned only by site creation and key rotation.
//
// The name is deliberate. A reviewer scanning for where a provisioning secret
// could escape needs one type to look for, and a handler returning this by
// mistake is obvious in a way that returning a struct with an incidental
// api_key field is not.
//
// APIKey IS NOT RECOVERABLE. The database holds a SHA-256 hash and nothing
// else, so an operator who does not store this value must rotate the key to get
// a working one. Every client rendering this must say so before the operator
// navigates away.
type ConsoleSiteCredential struct {
	// APIKey is the plaintext provisioning key. Shown once. Never returned by
	// any GET.
	APIKey string `json:"api_key"`
	// APIKeyPrefix is the non-secret first 12 characters, which DO appear in
	// later reads -- enough to identify which key a site is on, useless for
	// authenticating as it.
	APIKeyPrefix string `json:"api_key_prefix"`
	// Warning is machine-flagged rather than left to the client to remember.
	ShownOnce bool `json:"shown_once"`
}

// ConsoleSiteCreatedResponse is a new site plus its one-time credential.
type ConsoleSiteCreatedResponse struct {
	Site       ConsoleSite           `json:"site"`
	Credential ConsoleSiteCredential `json:"credential"`
}

// ConsoleSiteRotatedResponse is a replacement credential plus what it broke.
//
// LegacyTerminals counts terminals at the site that have never been issued a
// device key of their own and therefore certainly still authenticate with the
// SITE key -- the ones this rotation just locked out. Reported so the warning
// can be specific about which hardware needs re-provisioning.
type ConsoleSiteRotatedResponse struct {
	Credential      ConsoleSiteCredential `json:"credential"`
	LegacyTerminals int                   `json:"legacy_terminals"`
}

// ConsoleSiteRetiredResponse reports what a retirement did.
//
// TerminalsRetired is not decoration: retiring a site soft-deletes its
// terminals in the same transaction, and every one of them stops opening a door
// immediately. A client that does not surface this number is not describing
// what happened.
type ConsoleSiteRetiredResponse struct {
	Retired          bool `json:"retired"`
	TerminalsRetired int  `json:"terminals_retired"`
}
