package models

import (
	"encoding/json"
	"errors"
	"time"
)

// Application modules (migrations/009_applications.sql).
//
// AccessLink is a general-purpose biometric terminal platform. An application is
// a CAPABILITY the platform offers -- not an industry, not a customer, not a
// workflow baked into the product. The same hardware and the same schema serve a
// company running door access, a company recording attendance and a company
// doing nothing but identity verification; which of those a company gets is
// configuration it chooses, held in company_applications.
//
// Nothing here presumes a company wants any particular capability, including
// none at all.

// Application codes. Adding one means adding it here and to the two CHECK
// constraints in 009; nothing else in the platform enumerates them.
const (
	// AppAccessControl decides whether to release a door, barrier or lock.
	AppAccessControl = "ACCESS_CONTROL"
	// AppAttendance records presence against a schedule.
	AppAttendance = "ATTENDANCE"
	// AppRegistration enrols people and their credentials.
	AppRegistration = "REGISTRATION"
	// AppCheckIn records arrival at an event or appointment.
	AppCheckIn = "CHECK_IN"
	// AppVerification confirms a person is who they claim and reports it.
	AppVerification = "VERIFICATION"
	// AppTimeTracking accumulates worked time from arrivals and departures.
	AppTimeTracking = "TIME_TRACKING"
	// AppVisitorManagement admits and records people who are not on the roster.
	AppVisitorManagement = "VISITOR_MANAGEMENT"

	// AppMultiPurpose is a DEVICE mode, never a company capability: a terminal
	// that serves every capability its company has enabled. It is the default
	// for every device, which is what keeps this concept invisible to terminals
	// already in the field.
	AppMultiPurpose = "MULTI_PURPOSE"
)

// Applications is the closed set a company may enable, validated in Go as well
// as in the schema so a bad value is a 400 rather than a constraint violation
// the handler cannot tell apart from the database being down.
var Applications = map[string]bool{
	AppAccessControl:     true,
	AppAttendance:        true,
	AppRegistration:      true,
	AppCheckIn:           true,
	AppVerification:      true,
	AppTimeTracking:      true,
	AppVisitorManagement: true,
}

// ApplicationOrder is the canonical order for listing capabilities, so a
// dashboard's navigation does not reshuffle between requests. Not a priority
// and not a default -- purely presentational stability.
var ApplicationOrder = []string{
	AppAccessControl,
	AppAttendance,
	AppRegistration,
	AppCheckIn,
	AppVerification,
	AppTimeTracking,
	AppVisitorManagement,
}

// IsApplication reports whether code is a capability a company may enable.
// MULTI_PURPOSE is deliberately NOT one: it is a device mode.
func IsApplication(code string) bool { return Applications[code] }

// IsDeviceApplicationMode reports whether code may be assigned to a terminal.
func IsDeviceApplicationMode(code string) bool {
	return code == AppMultiPurpose || Applications[code]
}

// Application module errors.
var (
	ErrInvalidApplication = errors.New("unknown application")

	// ErrApplicationNotEnabled reports an attempt to point a terminal at a
	// capability its company has not turned on. Refused rather than allowed:
	// a terminal running a module the company does not have is a configuration
	// mistake that would only be discovered at the door.
	ErrApplicationNotEnabled = errors.New("application is not enabled for this company")
)

// CompanyApplication is one capability as configured for one company.
type CompanyApplication struct {
	PublicID  string          `json:"id"`
	Code      string          `json:"code"`
	Enabled   bool            `json:"enabled"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// EnabledApplication is the lean form carried in the session response, from
// which a dashboard builds its navigation.
//
// An OBJECT rather than a bare code string: settings already differ per module,
// and a list of strings could not grow to carry them without breaking every
// client that had parsed it.
type EnabledApplication struct {
	Code     string          `json:"code"`
	Settings json.RawMessage `json:"settings"`
}

// DeviceApplication describes what one terminal is configured to do and whether
// that configuration currently means anything.
//
// Mode is what the terminal is assigned. Effective is what it actually serves
// right now, which is empty when the assigned capability has been disabled at
// the company. Keeping both is what makes a disabled module recoverable rather
// than destructive: the assignment survives, it simply stops resolving.
type DeviceApplication struct {
	SerialNumber string   `json:"serial_number"`
	Mode         string   `json:"application_mode"`
	Effective    []string `json:"effective_applications"`
}
