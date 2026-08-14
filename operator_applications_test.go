package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Application modules (migrations/009_applications.sql, database/applications.go).
//
// The property that matters most here is what the platform does NOT assume. A
// company with nothing enabled is a legitimate, fully-working state, and no code
// path may quietly substitute a default set of capabilities for it.

func siteID(t *testing.T, siteName string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM sites WHERE site_name = $1`, siteName).Scan(&id); err != nil {
		t.Fatalf("site %q: %v", siteName, err)
	}
	return id
}

func mustCreateDevice(t *testing.T, siteName, serial string) {
	t.Helper()
	mustExec(t, `INSERT INTO devices (site_id, serial_number, device_name, device_type, status)
	             VALUES ($1, $2, $3, 'TERMINAL', 'ONLINE')`,
		siteID(t, siteName), serial, serial)
}

func applicationCodes(applications []models.EnabledApplication) []string {
	codes := make([]string, 0, len(applications))
	for _, app := range applications {
		codes = append(codes, app.Code)
	}
	return codes
}

// ---------------------------------------------------------------------------
// Company capabilities
// ---------------------------------------------------------------------------

func TestCompanyStartsWithNoApplicationsEnabled(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// The state every company is in after migration 009, and a legitimate one:
	// the platform has no opinion about what a customer uses it for.
	enabled, err := database.EnabledApplications(one)
	if err != nil {
		t.Fatalf("listing enabled applications: %v", err)
	}
	if len(enabled) != 0 {
		t.Errorf("a new company has %v enabled, want nothing", applicationCodes(enabled))
	}

	configured, err := database.ListCompanyApplications(one)
	if err != nil {
		t.Fatalf("listing configured applications: %v", err)
	}
	if len(configured) != 0 {
		t.Errorf("a new company has %d configured rows, want none", len(configured))
	}
}

func TestCompanyApplicationConfiguration(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	// Enabled out of canonical order, to prove the read imposes one.
	for _, code := range []string{models.AppVerification, models.AppAttendance} {
		if _, err := database.SetCompanyApplication(one, code, true, nil); err != nil {
			t.Fatalf("enabling %s: %v", code, err)
		}
	}

	enabled, err := database.EnabledApplications(one)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	codes := applicationCodes(enabled)
	if len(codes) != 2 || codes[0] != models.AppAttendance || codes[1] != models.AppVerification {
		t.Errorf("enabled = %v, want ATTENDANCE then VERIFICATION in canonical order", codes)
	}
	if string(enabled[0].Settings) != "{}" {
		t.Errorf("settings = %s, want an empty object", enabled[0].Settings)
	}

	// Enabling twice is idempotent -- the console's toggle must not fail on a
	// second click.
	if _, err := database.SetCompanyApplication(one, models.AppAttendance, true, nil); err != nil {
		t.Errorf("re-enabling an enabled capability: %v", err)
	}

	// Settings are replaced when supplied.
	settings := json.RawMessage(`{"grace_minutes":10}`)
	app, err := database.SetCompanyApplication(one, models.AppAttendance, true, settings)
	if err != nil {
		t.Fatalf("configuring: %v", err)
	}
	if !strings.Contains(string(app.Settings), "grace_minutes") {
		t.Errorf("settings = %s, want the configured object", app.Settings)
	}

	// Disabling preserves the row and its settings, so turning the module back
	// on does not lose what the company had configured.
	if _, err := database.SetCompanyApplication(one, models.AppAttendance, false, nil); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	if codes := applicationCodes(mustEnabled(t, one)); len(codes) != 1 || codes[0] != models.AppVerification {
		t.Errorf("after disabling, enabled = %v, want only VERIFICATION", codes)
	}
	configured, err := database.ListCompanyApplications(one)
	if err != nil || len(configured) != 2 {
		t.Fatalf("configured = %d rows, %v; want both retained", len(configured), err)
	}
	for _, entry := range configured {
		if entry.Code == models.AppAttendance {
			if entry.Enabled {
				t.Error("ATTENDANCE is still marked enabled")
			}
			if !strings.Contains(string(entry.Settings), "grace_minutes") {
				t.Errorf("disabling lost the settings: %s", entry.Settings)
			}
		}
	}

	// Re-enabling without resending settings restores them.
	if _, err := database.SetCompanyApplication(one, models.AppAttendance, true, nil); err != nil {
		t.Fatalf("re-enabling: %v", err)
	}
	restored := mustEnabled(t, one)
	for _, entry := range restored {
		if entry.Code == models.AppAttendance && !strings.Contains(string(entry.Settings), "grace_minutes") {
			t.Errorf("re-enabling lost the settings: %s", entry.Settings)
		}
	}

	// One company's configuration is not another's.
	if enabled, err := database.EnabledApplications(two); err != nil || len(enabled) != 0 {
		t.Errorf("company two sees %v, want nothing", applicationCodes(enabled))
	}

	// Validation happens in Go, so a bad value is a rejection the handler can
	// tell apart from the database being unwell.
	invalid := []struct {
		name string
		code string
		want error
	}{
		{"unknown capability", "GYM_MEMBERSHIP", models.ErrInvalidApplication},
		{"lowercase code", "attendance", models.ErrInvalidApplication},
		{"a device mode is not a company capability", models.AppMultiPurpose, models.ErrInvalidApplication},
		{"empty code", "", models.ErrInvalidApplication},
	}
	for _, tc := range invalid {
		if _, err := database.SetCompanyApplication(one, tc.code, true, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, err, tc.want)
		}
	}

	// Settings must be an object, matching the column's CHECK.
	if _, err := database.SetCompanyApplication(one, models.AppCheckIn, true,
		json.RawMessage(`[1,2,3]`)); err == nil {
		t.Error("a JSON array was accepted as application settings")
	}
}

func mustEnabled(t *testing.T, companyID int64) []models.EnabledApplication {
	t.Helper()
	enabled, err := database.EnabledApplications(companyID)
	if err != nil {
		t.Fatalf("listing enabled applications: %v", err)
	}
	return enabled
}

// ---------------------------------------------------------------------------
// Terminal application mode
// ---------------------------------------------------------------------------

func TestDeviceApplicationMode(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	mustCreateDevice(t, "Site A", "MODE-1")
	mustCreateDevice(t, "Site C", "OTHER-TENANT-1") // company two

	// Every device that already existed gets MULTI_PURPOSE, which is what makes
	// this column invisible to terminals already in the field.
	device, err := database.GetDeviceApplication(one, "MODE-1")
	if err != nil {
		t.Fatalf("reading device application: %v", err)
	}
	if device.Mode != models.AppMultiPurpose {
		t.Errorf("default mode = %q, want MULTI_PURPOSE", device.Mode)
	}
	// A multi-purpose terminal at a company with nothing enabled serves nothing,
	// and that is the honest answer rather than a failure.
	if len(device.Effective) != 0 {
		t.Errorf("effective = %v, want nothing while the company has no modules", device.Effective)
	}

	// MULTI_PURPOSE follows whatever the company enables.
	for _, code := range []string{models.AppCheckIn, models.AppRegistration} {
		if _, err := database.SetCompanyApplication(one, code, true, nil); err != nil {
			t.Fatalf("enabling %s: %v", code, err)
		}
	}
	device, _ = database.GetDeviceApplication(one, "MODE-1")
	if len(device.Effective) != 2 {
		t.Errorf("effective = %v, want both enabled capabilities", device.Effective)
	}

	// A terminal may only be pointed at a capability the company has enabled.
	if err := database.SetDeviceApplicationMode(one, "MODE-1", models.AppAttendance); !errors.Is(err, models.ErrApplicationNotEnabled) {
		t.Errorf("assigning a disabled capability = %v, want ErrApplicationNotEnabled", err)
	}
	if device, _ := database.GetDeviceApplication(one, "MODE-1"); device.Mode != models.AppMultiPurpose {
		t.Error("a refused assignment changed the terminal's mode")
	}

	if err := database.SetDeviceApplicationMode(one, "MODE-1", models.AppCheckIn); err != nil {
		t.Fatalf("assigning an enabled capability: %v", err)
	}
	device, _ = database.GetDeviceApplication(one, "MODE-1")
	if device.Mode != models.AppCheckIn {
		t.Errorf("mode = %q, want CHECK_IN", device.Mode)
	}
	if len(device.Effective) != 1 || device.Effective[0] != models.AppCheckIn {
		t.Errorf("effective = %v, want only CHECK_IN", device.Effective)
	}

	// Disabling the capability retains the assignment and stops it resolving.
	// Recoverable and visible, rather than the database rewriting a terminal.
	if _, err := database.SetCompanyApplication(one, models.AppCheckIn, false, nil); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	device, _ = database.GetDeviceApplication(one, "MODE-1")
	if device.Mode != models.AppCheckIn {
		t.Errorf("mode = %q, want the assignment retained", device.Mode)
	}
	if len(device.Effective) != 0 {
		t.Errorf("effective = %v, want nothing while the capability is off", device.Effective)
	}
	// Turning it back on restores the terminal without touching the device row.
	if _, err := database.SetCompanyApplication(one, models.AppCheckIn, true, nil); err != nil {
		t.Fatalf("re-enabling: %v", err)
	}
	if device, _ = database.GetDeviceApplication(one, "MODE-1"); len(device.Effective) != 1 {
		t.Errorf("effective = %v after re-enabling, want CHECK_IN", device.Effective)
	}

	// MULTI_PURPOSE is always assignable: it claims no particular capability.
	if err := database.SetDeviceApplicationMode(one, "MODE-1", models.AppMultiPurpose); err != nil {
		t.Errorf("assigning MULTI_PURPOSE: %v", err)
	}

	// An unknown mode is rejected in Go, before it reaches the CHECK constraint.
	for _, mode := range []string{"DOOR_OPENER", "", "check_in"} {
		if err := database.SetDeviceApplicationMode(one, "MODE-1", mode); !errors.Is(err, models.ErrInvalidApplication) {
			t.Errorf("mode %q = %v, want ErrInvalidApplication", mode, err)
		}
	}

	// Tenancy: another company's terminal is not found, never modified.
	if err := database.SetDeviceApplicationMode(one, "OTHER-TENANT-1", models.AppMultiPurpose); !errors.Is(err, models.ErrDeviceNotFound) {
		t.Errorf("assigning across tenants = %v, want ErrDeviceNotFound", err)
	}
	if _, err := database.GetDeviceApplication(one, "OTHER-TENANT-1"); !errors.Is(err, models.ErrDeviceNotFound) {
		t.Errorf("reading across tenants = %v, want ErrDeviceNotFound", err)
	}
	if _, err := database.GetDeviceApplication(one, "NO-SUCH-SERIAL"); !errors.Is(err, models.ErrDeviceNotFound) {
		t.Errorf("unknown serial = %v, want ErrDeviceNotFound", err)
	}
	// And company two's terminal is untouched by any of it.
	if device, err := database.GetDeviceApplication(two, "OTHER-TENANT-1"); err != nil ||
		device.Mode != models.AppMultiPurpose {
		t.Error("the other tenant's terminal was modified")
	}
}

// ---------------------------------------------------------------------------
// The session response
// ---------------------------------------------------------------------------

func TestSessionResponseCarriesEnabledApplications(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	mustCreateOperator(t, one, "apps@example.com", models.RoleOwner)

	// A company with nothing enabled must get an empty ARRAY, never null and
	// never a default set. A dashboard that saw null here would have to guess.
	token, _ := login(t, env.router, "apps@example.com", testPassword)
	code, body, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	if code != http.StatusOK {
		t.Fatalf("/me = %d (%v)", code, body)
	}
	applications, present := body["applications"]
	if !present {
		t.Fatal("/me does not carry an applications field")
	}
	if applications == nil {
		t.Fatal("applications is null; it must be an empty array")
	}
	if list := applications.([]any); len(list) != 0 {
		t.Errorf("applications = %v, want empty for a company with nothing enabled", list)
	}

	// Enabling capabilities makes them appear, with their settings, in canonical
	// order regardless of the order they were switched on.
	if _, err := database.SetCompanyApplication(one, models.AppTimeTracking, true,
		json.RawMessage(`{"rounding_minutes":15}`)); err != nil {
		t.Fatalf("enabling TIME_TRACKING: %v", err)
	}
	if _, err := database.SetCompanyApplication(one, models.AppAccessControl, true, nil); err != nil {
		t.Fatalf("enabling ACCESS_CONTROL: %v", err)
	}
	// Configured but off: it must not appear.
	if _, err := database.SetCompanyApplication(one, models.AppRegistration, false, nil); err != nil {
		t.Fatalf("configuring REGISTRATION: %v", err)
	}

	_, body, _ = doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: token,
	})
	list := body["applications"].([]any)
	if len(list) != 2 {
		t.Fatalf("applications = %v, want the two enabled ones", list)
	}
	first := list[0].(map[string]any)
	second := list[1].(map[string]any)
	if first["code"] != models.AppAccessControl || second["code"] != models.AppTimeTracking {
		t.Errorf("order = %v, %v; want canonical order", first["code"], second["code"])
	}
	// An object per capability, not a bare string, so settings can travel with
	// it and the shape can grow.
	settings, ok := second["settings"].(map[string]any)
	if !ok || settings["rounding_minutes"] != float64(15) {
		t.Errorf("TIME_TRACKING settings = %v, want the configured object", second["settings"])
	}
	if strings.Contains(mustJSON(t, list), models.AppRegistration) {
		t.Error("a configured-but-disabled capability appears as enabled")
	}

	// Login and /me must agree: they are the same body from the same builder.
	loginCode, loginBody, _ := doAuth(t, env.router, authCall{
		method: http.MethodPost, path: "/api/v1/auth/login",
		body: loginBody("apps@example.com", testPassword),
	})
	if loginCode != http.StatusOK {
		t.Fatalf("login = %d", loginCode)
	}
	if mustJSON(t, loginBody["applications"]) != mustJSON(t, body["applications"]) {
		t.Errorf("login applications %v differ from /me %v",
			loginBody["applications"], body["applications"])
	}

	// Another tenant's operator sees its own (empty) configuration.
	two := operatorCompanyID(t, "two")
	mustCreateOperator(t, two, "other-apps@example.com", models.RoleOwner)
	otherToken, _ := login(t, env.router, "other-apps@example.com", testPassword)
	_, otherBody, _ := doAuth(t, env.router, authCall{
		method: http.MethodGet, path: "/api/v1/auth/me", token: otherToken,
	})
	if list := otherBody["applications"].([]any); len(list) != 0 {
		t.Errorf("another tenant sees %v, want its own empty configuration", list)
	}
}
