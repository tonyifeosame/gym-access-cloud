package main

import (
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The authorization engine (APP-02).
//
// WHAT THESE TESTS EXIST TO CATCH. Before this work, `permissions` and
// `schedules` were read by zero lines of Go: every active person with a bound
// credential opened every terminal in their company, permanently, and no
// customer could express anything narrower. A test suite that only checked
// "an allowed person is allowed" would have passed against that broken state,
// because everybody was allowed.
//
// So the load-bearing cases here are the REFUSALS -- and in particular
// TestDenyByDefault, which is the one case the old behaviour could not have
// passed under any configuration.

// authFixture is a company with a site, a terminal and a person, ready to have
// rules written about it.
type authFixture struct {
	env        *testEnv
	companyID  int64
	siteID     int64
	deviceID   int64
	personID   int64
	externalID string
	serial     string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	env := newTestEnv(t)

	companyID := companyIDBySlug(t, "one")
	env.registerDevice(env.siteAKey, "ESP32-AUTH")

	var siteID, deviceID int64
	mustScan(t, `SELECT d.id, d.site_id FROM devices d WHERE d.serial_number = 'ESP32-AUTH'`,
		&deviceID, &siteID)

	personID := seedPerson(t, companyID, "P-AUTH", "Auth Subject")

	// The person is seeded with NO permission. That is the state a person
	// created after this engine ships is in, and the state every assertion
	// below starts from.
	return &authFixture{
		env: env, companyID: companyID, siteID: siteID, deviceID: deviceID,
		personID: personID, externalID: "P-AUTH", serial: "ESP32-AUTH",
	}
}

// decide runs one evaluation at a moment.
func (f *authFixture) decide(t *testing.T, at time.Time) *models.AccessDecision {
	t.Helper()
	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID,
		DeviceID:   f.deviceID,
		At:         at,
	})
	if err != nil {
		t.Fatalf("authorizing: %v", err)
	}
	if decision == nil {
		t.Fatal("Authorize returned no decision, which must never happen")
	}
	return decision
}

// allowCompanyWide writes the broadest possible grant.
func (f *authFixture) allowCompanyWide(t *testing.T) {
	t.Helper()
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, f.companyID, f.personID)
}

// ---------------------------------------------------------------------------
// The default
// ---------------------------------------------------------------------------

// TestDenyByDefault is the single most important test in this file.
//
// A person who exists, is active, and is at a working terminal in their own
// company is REFUSED when nothing grants them access. The previous
// implementation could not have passed this: it had no permission check at all,
// so this person would have been admitted.
func TestDenyByDefault(t *testing.T) {
	f := newAuthFixture(t)

	decision := f.decide(t, time.Now().UTC())

	if decision.Granted {
		t.Fatal("a person with no permission was admitted; absence of permission is not permission")
	}
	if decision.Reason != models.ReasonNoPermission {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonNoPermission)
	}
	// The decision still names who it was about, so the refusal is actionable.
	if decision.ExternalID != f.externalID {
		t.Errorf("external id = %q, want %q", decision.ExternalID, f.externalID)
	}
}

func TestCompanyScopedAllowAdmits(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	decision := f.decide(t, time.Now().UTC())

	if !decision.Granted {
		t.Fatalf("a company-scoped ALLOW did not admit: %s", decision.Reason)
	}
	// The reason is carried on a GRANT too. An audit trail that says why
	// somebody was refused but not why they were admitted answers half the
	// question a security review asks.
	if decision.Reason != models.ReasonAllowed {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonAllowed)
	}
	if decision.MatchedPermission == "" {
		t.Error("a grant must name the rule that decided it")
	}
}

// ---------------------------------------------------------------------------
// DENY beats ALLOW
// ---------------------------------------------------------------------------

// TestDenyBeatsAllowAtEveryScope is the exclusion case: "everyone in Operations,
// except this one person at this one terminal". Expressing that with allow-only
// rules means enumerating everybody who is NOT excluded and re-enumerating them
// whenever anyone joins.
func TestDenyBeatsAllowAtEveryScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		insert func(f *authFixture, t *testing.T)
	}{
		{"company-scoped deny", func(f *authFixture, t *testing.T) {
			mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
			             VALUES ($1, $2, 'COMPANY', 'DENY', TRUE)`, f.companyID, f.personID)
		}},
		{"site-scoped deny", func(f *authFixture, t *testing.T) {
			mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active)
			             VALUES ($1, $2, 'SITE', $3, 'DENY', TRUE)`, f.companyID, f.personID, f.siteID)
		}},
		{"terminal-scoped deny", func(f *authFixture, t *testing.T) {
			mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, device_id, effect, active)
			             VALUES ($1, $2, 'TERMINAL', $3, 'DENY', TRUE)`, f.companyID, f.personID, f.deviceID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.allowCompanyWide(t)
			tc.insert(f, t)

			decision := f.decide(t, time.Now().UTC())

			if decision.Granted {
				t.Fatal("a DENY did not beat an ALLOW; exclusion cannot be expressed")
			}
			if decision.Reason != models.ReasonExplicitDeny {
				t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonExplicitDeny)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scope
// ---------------------------------------------------------------------------

// TestScopeDoesNotReachBeyondItself proves a narrow grant stays narrow. A rule
// written for Site B must not open a terminal at Site A.
func TestScopeDoesNotReachBeyondItself(t *testing.T) {
	f := newAuthFixture(t)

	var otherSiteID int64
	mustScan(t, `SELECT id FROM sites WHERE site_name = 'Site B'`, &otherSiteID)

	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active)
	             VALUES ($1, $2, 'SITE', $3, 'ALLOW', TRUE)`, f.companyID, f.personID, otherSiteID)

	decision := f.decide(t, time.Now().UTC())

	if decision.Granted {
		t.Fatal("a grant scoped to another site admitted at this one")
	}
	if decision.Reason != models.ReasonNoPermission {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonNoPermission)
	}
}

// TestTerminalScopedAllowAdmitsOnlyThatTerminal.
func TestTerminalScopedAllowAdmitsOnlyThatTerminal(t *testing.T) {
	f := newAuthFixture(t)

	// A second terminal at the same site, so the only difference is the device.
	f.env.registerDevice(f.env.siteAKey, "ESP32-OTHER")
	var otherDeviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = 'ESP32-OTHER'`, &otherDeviceID)

	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, device_id, effect, active)
	             VALUES ($1, $2, 'TERMINAL', $3, 'ALLOW', TRUE)`, f.companyID, f.personID, f.deviceID)

	if decision := f.decide(t, time.Now().UTC()); !decision.Granted {
		t.Fatalf("the named terminal refused: %s", decision.Reason)
	}

	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, DeviceID: otherDeviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing at the other terminal: %v", err)
	}
	if decision.Granted {
		t.Fatal("a terminal-scoped grant admitted at a terminal it did not name")
	}
}

// ---------------------------------------------------------------------------
// Person and credential state
// ---------------------------------------------------------------------------

func TestInactiveAndExpiredPeopleAreRefused(t *testing.T) {
	now := time.Now().UTC()

	for _, tc := range []struct {
		name   string
		mutate string
		args   func(f *authFixture) []any
		reason string
	}{
		{"inactive", `UPDATE people SET active = FALSE WHERE id = $1`,
			func(f *authFixture) []any { return []any{f.personID} },
			models.ReasonPersonInactive},
		{"validity not yet open", `UPDATE people SET valid_from = $2 WHERE id = $1`,
			func(f *authFixture) []any { return []any{f.personID, now.Add(24 * time.Hour)} },
			models.ReasonPermissionNotYet},
		{"validity expired", `UPDATE people SET valid_until = $2 WHERE id = $1`,
			func(f *authFixture) []any { return []any{f.personID, now.Add(-24 * time.Hour)} },
			models.ReasonPermissionExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.allowCompanyWide(t)
			mustExec(t, tc.mutate, tc.args(f)...)

			decision := f.decide(t, now)

			if decision.Granted {
				t.Fatal("a person who should not be admitted was")
			}
			if decision.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", decision.Reason, tc.reason)
			}
		})
	}
}

// TestUnknownSubjectIsRecordableRatherThanInvisible.
//
// An unrecognised credential is the more interesting half of an audit trail. The
// decision must still name what the terminal read, or the attempt is untraceable.
func TestUnknownSubjectIsRefusedButRecorded(t *testing.T) {
	f := newAuthFixture(t)

	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: "NOBODY-AT-ALL", DeviceID: f.deviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing an unknown subject: %v", err)
	}
	if decision.Granted {
		t.Fatal("an unknown subject was admitted")
	}
	if decision.Reason != models.ReasonPersonUnknown {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonPersonUnknown)
	}
	if decision.ExternalID != "NOBODY-AT-ALL" {
		t.Errorf("external id = %q, want what the terminal read", decision.ExternalID)
	}
}

// TestRevokedCredentialIsRefusedWithoutTouchingThePerson proves credentials and
// people have independent lifecycles (HW-03): revoking a lost card must not
// require deactivating its owner.
func TestRevokedCredentialIsRefusedWithoutTouchingThePerson(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	credID := seedCredential(t, f.companyID, f.personID, "CARD-REVOKED")
	var credPublicID string
	mustScan(t, `SELECT public_id FROM credentials WHERE id = `+itoa(credID), &credPublicID)

	// Active first: the same call must admit, so the refusal below is caused by
	// the revocation and not by the credential being named at all.
	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, CredentialID: credPublicID,
		DeviceID: f.deviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing with an active credential: %v", err)
	}
	if !decision.Granted {
		t.Fatalf("an active credential was refused: %s", decision.Reason)
	}

	mustExec(t, `UPDATE credentials SET status = 'REVOKED', revoked_at = CURRENT_TIMESTAMP
	             WHERE id = $1`, credID)

	decision, err = database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, CredentialID: credPublicID,
		DeviceID: f.deviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing with a revoked credential: %v", err)
	}
	if decision.Granted {
		t.Fatal("a revoked credential still opened the door")
	}
	if decision.Reason != models.ReasonCredentialRevoked {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonCredentialRevoked)
	}

	// The person is untouched and still admitted by another means.
	if plain := f.decide(t, time.Now().UTC()); !plain.Granted {
		t.Error("revoking one credential deactivated its owner")
	}
}

// TestCredentialOfAnotherPersonIsRefused is the IDOR case at the door: a
// credential presented alongside somebody else's external id.
func TestCredentialOfAnotherPersonIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	otherID := seedPerson(t, f.companyID, "P-OTHER", "Other Person")
	credID := seedCredential(t, f.companyID, otherID, "CARD-OTHER")
	var credPublicID string
	mustScan(t, `SELECT public_id FROM credentials WHERE id = `+itoa(credID), &credPublicID)

	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, CredentialID: credPublicID,
		DeviceID: f.deviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing a mismatched credential: %v", err)
	}
	if decision.Granted {
		t.Fatal("a credential belonging to somebody else was accepted")
	}
	if decision.Reason != models.ReasonCredentialUnknown {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonCredentialUnknown)
	}
}

// TestCredentialOfAnotherCompanyIsRefused is the cross-tenant version.
func TestCredentialOfAnotherCompanyIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	otherCompany := companyIDBySlug(t, "two")
	foreignPerson := seedPerson(t, otherCompany, "P-FOREIGN", "Foreign Person")
	credID := seedCredential(t, otherCompany, foreignPerson, "CARD-FOREIGN")
	var credPublicID string
	mustScan(t, `SELECT public_id FROM credentials WHERE id = `+itoa(credID), &credPublicID)

	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, CredentialID: credPublicID,
		DeviceID: f.deviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing a foreign credential: %v", err)
	}
	if decision.Granted {
		t.Fatal("a credential from another tenant was accepted")
	}
}

// ---------------------------------------------------------------------------
// Terminal, site and company state
// ---------------------------------------------------------------------------

// TestOutOfServiceContextRefusesBeforeResolvingThePerson.
//
// A revoked terminal must not be able to learn whether an external id is known
// to the platform, which it would if an unknown person and a revoked terminal
// gave different answers.
func TestOutOfServiceContextRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(f *authFixture, t *testing.T)
		reason string
	}{
		{"disabled terminal", func(f *authFixture, t *testing.T) {
			mustExec(t, `UPDATE devices SET status = 'DISABLED', active = FALSE WHERE id = $1`, f.deviceID)
		}, models.ReasonTerminalDisabled},
		{"revoked credential", func(f *authFixture, t *testing.T) {
			mustExec(t, `UPDATE devices SET api_key_hash = NULL,
			             credential_revoked_at = CURRENT_TIMESTAMP WHERE id = $1`, f.deviceID)
		}, models.ReasonTerminalDisabled},
		{"inactive site", func(f *authFixture, t *testing.T) {
			mustExec(t, `UPDATE sites SET active = FALSE WHERE id = $1`, f.siteID)
		}, models.ReasonSiteInactive},
		{"inactive company", func(f *authFixture, t *testing.T) {
			mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, f.companyID)
		}, models.ReasonCompanyInactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.allowCompanyWide(t)
			tc.mutate(f, t)

			decision := f.decide(t, time.Now().UTC())

			if decision.Granted {
				t.Fatal("an out-of-service context still admitted somebody")
			}
			if decision.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", decision.Reason, tc.reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

// seedSchedule writes a schedule with one window and attaches it to a rule.
func seedSchedule(t *testing.T, companyID int64, name, timezone string,
	days int, start, end string) int64 {

	t.Helper()

	// database.DB directly rather than mustScan: that helper takes no query
	// parameters, and interpolating a timezone into SQL to work around it would
	// be a worse habit than four extra lines here.
	var scheduleID int64
	if err := database.DB.QueryRow(`
		INSERT INTO schedules (company_id, name, timezone)
		VALUES ($1, $2, NULLIF($3, '')) RETURNING id`,
		companyID, name, timezone).Scan(&scheduleID); err != nil {
		t.Fatalf("seeding schedule %q: %v", name, err)
	}

	mustExec(t, `INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
	             VALUES ($1, $2, $3::time, $4::time)`, scheduleID, days, start, end)
	return scheduleID
}

// TestScheduleBoundsAGrant. The rule is otherwise identical either side of the
// boundary, so the window is the only thing that can decide it.
func TestScheduleBoundsAGrant(t *testing.T) {
	f := newAuthFixture(t)

	// Weekdays 09:00-17:00, UTC so the test does not depend on a host zone.
	scheduleID := seedSchedule(t, f.companyID, "Office hours", "UTC",
		models.DayMonday|models.DayTuesday|models.DayWednesday|
			models.DayThursday|models.DayFriday, "09:00", "17:00")

	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, effect, schedule_id, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', $3, TRUE)`,
		f.companyID, f.personID, scheduleID)

	// A Wednesday, chosen explicitly so the assertion does not depend on when
	// the suite happens to run.
	inside := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)
	before := time.Date(2026, 8, 12, 8, 59, 0, 0, time.UTC)
	after := time.Date(2026, 8, 12, 17, 0, 0, 0, time.UTC)
	weekend := time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC) // Saturday

	if d := f.decide(t, inside); !d.Granted {
		t.Errorf("inside the window was refused: %s", d.Reason)
	}
	for name, moment := range map[string]time.Time{
		"before the window": before,
		"at the end bound":  after,
		"on a weekend":      weekend,
	} {
		if d := f.decide(t, moment); d.Granted {
			t.Errorf("%s was admitted", name)
		} else if d.Reason != models.ReasonNoPermission {
			// The rule did not match, so the outcome is deny-by-default rather
			// than an explicit schedule refusal.
			t.Errorf("%s: reason = %q, want %q", name, d.Reason, models.ReasonNoPermission)
		}
	}
}

// TestMidnightCrossingWindowIsOneWindow.
//
// A night shift 22:00-06:00 starting Friday admits Friday 23:00 and Saturday
// 02:00, and refuses Friday 05:00. Reading the day mask against the moment's own
// day would make Sunday night's shift look like Monday's, which is the bug this
// case exists to catch.
func TestMidnightCrossingWindowIsOneWindow(t *testing.T) {
	f := newAuthFixture(t)

	scheduleID := seedSchedule(t, f.companyID, "Night shift", "UTC",
		models.DayFriday, "22:00", "06:00")

	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, effect, schedule_id, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', $3, TRUE)`,
		f.companyID, f.personID, scheduleID)

	// 2026-08-14 is a Friday.
	fridayNight := time.Date(2026, 8, 14, 23, 0, 0, 0, time.UTC)
	saturdayEarly := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	fridayEarly := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	saturdayNight := time.Date(2026, 8, 15, 23, 0, 0, 0, time.UTC)

	if d := f.decide(t, fridayNight); !d.Granted {
		t.Errorf("Friday 23:00 refused: %s", d.Reason)
	}
	if d := f.decide(t, saturdayEarly); !d.Granted {
		t.Error("Saturday 02:00 refused; the window did not carry into the next day")
	}
	if d := f.decide(t, fridayEarly); d.Granted {
		t.Error("Friday 05:00 admitted; the window was read against the wrong day")
	}
	if d := f.decide(t, saturdayNight); d.Granted {
		t.Error("Saturday 23:00 admitted; the shift starts on Friday only")
	}
}

// TestScheduleIsEvaluatedInItsOwnTimezone. The same instant is inside the window
// in one zone and outside it in another; only an explicit zone can express a
// company that runs one shift pattern across several countries.
func TestScheduleIsEvaluatedInItsOwnTimezone(t *testing.T) {
	f := newAuthFixture(t)

	// 09:00-17:00 Tokyo. 2026-08-12 01:00 UTC is 10:00 in Tokyo -- inside --
	// and would be outside if the window were read in UTC.
	scheduleID := seedSchedule(t, f.companyID, "Tokyo hours", "Asia/Tokyo",
		models.DayEveryDay, "09:00", "17:00")

	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, effect, schedule_id, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', $3, TRUE)`,
		f.companyID, f.personID, scheduleID)

	insideTokyo := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	outsideTokyo := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) // 21:00 Tokyo

	if d := f.decide(t, insideTokyo); !d.Granted {
		t.Errorf("10:00 Tokyo refused: %s -- the window was not read in its own zone", d.Reason)
	}
	if d := f.decide(t, outsideTokyo); d.Granted {
		t.Error("21:00 Tokyo admitted")
	}
}

// ---------------------------------------------------------------------------
// Validity windows on the rule
// ---------------------------------------------------------------------------

func TestPermissionValidityWindowIsEnforced(t *testing.T) {
	f := newAuthFixture(t)
	now := time.Now().UTC()

	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, effect, starts_at, ends_at, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', $3, $4, TRUE)`,
		f.companyID, f.personID, now.Add(-time.Hour), now.Add(time.Hour))

	if d := f.decide(t, now); !d.Granted {
		t.Fatalf("inside the validity window was refused: %s", d.Reason)
	}
	if d := f.decide(t, now.Add(-2*time.Hour)); d.Granted {
		t.Error("before the window opened was admitted")
	}
	if d := f.decide(t, now.Add(2*time.Hour)); d.Granted {
		t.Error("after the window closed was admitted")
	}
}

// TestInactivePermissionDoesNotAdmit. `active` is the reversible off switch;
// a rule turned off must stop admitting without being deleted.
func TestInactivePermissionDoesNotAdmit(t *testing.T) {
	f := newAuthFixture(t)

	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', FALSE)`, f.companyID, f.personID)

	if d := f.decide(t, time.Now().UTC()); d.Granted {
		t.Fatal("an inactive rule admitted somebody")
	}
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

// TestDisabledApplicationRefuses. 009 kept a terminal's mode when a company
// disables the capability so re-enabling is recoverable; this is the other half
// of that decision -- the mode must not authorize anybody while it is off.
func TestDisabledApplicationRefuses(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	mustExec(t, `UPDATE devices SET application_mode = 'ACCESS_CONTROL' WHERE id = $1`, f.deviceID)

	// Never configured is not enabled.
	if d := f.decide(t, time.Now().UTC()); d.Granted {
		t.Fatal("a capability the company never enabled still authorized somebody")
	} else if d.Reason != models.ReasonApplicationOff {
		t.Errorf("reason = %q, want %q", d.Reason, models.ReasonApplicationOff)
	}

	mustExec(t, `INSERT INTO company_applications (company_id, application, enabled)
	             VALUES ($1, 'ACCESS_CONTROL', TRUE)`, f.companyID)

	if d := f.decide(t, time.Now().UTC()); !d.Granted {
		t.Fatalf("an enabled capability still refused: %s", d.Reason)
	}
	if d := f.decide(t, time.Now().UTC()); d.Application != "ACCESS_CONTROL" {
		t.Errorf("decision application = %q, want the terminal's mode", d.Application)
	}

	mustExec(t, `UPDATE company_applications SET enabled = FALSE WHERE company_id = $1`, f.companyID)

	if d := f.decide(t, time.Now().UTC()); d.Granted {
		t.Fatal("a disabled capability still authorized somebody")
	}
}

// TestApplicationScopedRuleDoesNotAdmitUnderAnother.
func TestApplicationScopedRuleDoesNotAdmitUnderAnother(t *testing.T) {
	f := newAuthFixture(t)

	mustExec(t, `INSERT INTO company_applications (company_id, application, enabled)
	             VALUES ($1, 'ACCESS_CONTROL', TRUE), ($1, 'ATTENDANCE', TRUE)`, f.companyID)
	mustExec(t, `UPDATE devices SET application_mode = 'ACCESS_CONTROL' WHERE id = $1`, f.deviceID)

	// A rule that only applies to ATTENDANCE.
	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, application, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ATTENDANCE', 'ALLOW', TRUE)`,
		f.companyID, f.personID)

	if d := f.decide(t, time.Now().UTC()); d.Granted {
		t.Fatal("a rule scoped to ATTENDANCE admitted under ACCESS_CONTROL")
	}

	mustExec(t, `UPDATE devices SET application_mode = 'ATTENDANCE' WHERE id = $1`, f.deviceID)

	if d := f.decide(t, time.Now().UTC()); !d.Granted {
		t.Fatalf("the rule did not admit under its own application: %s", d.Reason)
	}
}

// ---------------------------------------------------------------------------
// Cross-tenant
// ---------------------------------------------------------------------------

// TestAuthorizationDoesNotCrossCompanies. A person's external id in one tenant
// must mean nothing at another tenant's terminal, even when the string matches.
func TestAuthorizationDoesNotCrossCompanies(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	// The SAME external id in company two, permitted company-wide there.
	otherCompany := companyIDBySlug(t, "two")
	otherPerson := seedPerson(t, otherCompany, f.externalID, "Same Id, Other Tenant")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, otherCompany, otherPerson)

	// A terminal in company two.
	f.env.registerDevice(f.env.siteCKey, "ESP32-TENANT-2")
	var foreignDeviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = 'ESP32-TENANT-2'`, &foreignDeviceID)

	// Company one's person is refused at company two's terminal -- the grant
	// that admitted them is in the other tenant, and the one that resolves here
	// belongs to a different person entirely.
	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, DeviceID: foreignDeviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing across tenants: %v", err)
	}
	// It resolves to company two's own person, who IS permitted there. What
	// must be true is that the decision is about THAT person, not company one's.
	if decision.PersonID == "" {
		t.Fatal("no person resolved at all")
	}
	var resolvedCompany int64
	mustScan(t, `SELECT company_id FROM people WHERE public_id = '`+decision.PersonID+`'`,
		&resolvedCompany)
	if resolvedCompany != otherCompany {
		t.Fatalf("a terminal in company %d resolved a person in company %d",
			otherCompany, resolvedCompany)
	}
}

// TestForeignPermissionDoesNotAdmit is the sharper version: company one's person
// has a grant, and a company two terminal must not honour it.
func TestForeignPermissionDoesNotAdmit(t *testing.T) {
	f := newAuthFixture(t)
	f.allowCompanyWide(t)

	f.env.registerDevice(f.env.siteCKey, "ESP32-TENANT-2")
	var foreignDeviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = 'ESP32-TENANT-2'`, &foreignDeviceID)

	// No person with this external id exists in company two.
	decision, err := database.Authorize(models.AccessRequest{
		ExternalID: f.externalID, DeviceID: foreignDeviceID, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("authorizing across tenants: %v", err)
	}
	if decision.Granted {
		t.Fatal("a grant in one tenant admitted at another tenant's terminal")
	}
	if decision.Reason != models.ReasonPersonUnknown {
		t.Errorf("reason = %q, want %q", decision.Reason, models.ReasonPersonUnknown)
	}
}
