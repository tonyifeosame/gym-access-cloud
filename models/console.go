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
//   - models.Site carries api_key. That key is the provisioning secret -- it
//     registers terminals and rotates their credentials -- and returning it to a
//     browser would undo the entire reason operator authentication exists. There
//     is deliberately no field for it below, so no console handler can leak it
//     by forgetting to strip one.
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
type ConsoleOperatorRequest struct {
	Email    string   `json:"email" binding:"required"`
	FullName string   `json:"full_name" binding:"required"`
	Password string   `json:"password" binding:"required"`
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
