package main

import (
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Roster scoping (SEC-04).
//
// WHAT THIS SUITE EXISTS TO CATCH. Person changes fanned out to every terminal
// in the COMPANY, through `sites.company_id`. A company with four sites and one
// roster pushed the whole roster to all of them: a terminal at a public
// reception desk held the entire staff list, a site-scoped permission meant
// nothing at the door because every terminal held everybody anyway, and person
// 65 exhausted a terminal's table and parked FAILED -- which is half of FW-01.
//
// The tests below would all have passed against that behaviour if they only
// asserted that an authorized person IS delivered. The load-bearing assertions
// are the ones counting jobs a terminal must NOT receive.

// jobsFor counts the live PERSON jobs queued for one terminal.
func jobsFor(t *testing.T, serial string) int {
	t.Helper()
	return queryInt(t, `
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1
		   AND j.entity_type = 'PERSON'
		   AND j.job_type <> 'DELETE'`, serial)
}

func deleteJobsFor(t *testing.T, serial string) int {
	t.Helper()
	return queryInt(t, `
		SELECT count(*) FROM sync_jobs j
		  JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1
		   AND j.entity_type = 'PERSON'
		   AND j.job_type = 'DELETE'`, serial)
}

// newScopedFixture builds a company with a terminal at Site A and one at Site B,
// and a company whose policy grants new people nothing -- so every roster entry
// below is there because a test put it there.
func newScopedFixture(t *testing.T) (*testEnv, int64) {
	t.Helper()
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	// Deny-by-default, which is what a company created through the platform API
	// starts with. The fixture companies are raw inserts and therefore carry the
	// legacy COMPANY_ALLOW default, so this is set explicitly.
	mustExec(t, `UPDATE companies SET default_person_access = 'NONE' WHERE id = $1`, companyID)

	env.registerDevice(env.siteAKey, "TERM-A")
	env.registerDevice(env.siteBKey, "TERM-B")
	return env, companyID
}

func deviceIDOf(t *testing.T, serial string) int64 {
	t.Helper()
	var id int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = '`+serial+`'`, &id)
	return id
}

func siteIDOf(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	mustScan(t, `SELECT id FROM sites WHERE site_name = '`+name+`'`, &id)
	return id
}

// TestAPersonWithNoPermissionReachesNoTerminal is deny-by-default at the sync
// layer. Under the old fan-out this person would have been pushed to both.
func TestAPersonWithNoPermissionReachesNoTerminal(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	member := &models.Member{
		MemberID: "P-NOWHERE", FullName: "Unpermitted Person",
		MembershipType: "STANDARD", Active: true,
	}
	if err := database.CreateMember(companyID, member); err != nil {
		t.Fatalf("creating person: %v", err)
	}

	if n := jobsFor(t, "TERM-A"); n != 0 {
		t.Errorf("TERM-A received %d jobs for a person with no permission", n)
	}
	if n := jobsFor(t, "TERM-B"); n != 0 {
		t.Errorf("TERM-B received %d jobs for a person with no permission", n)
	}
}

// TestSiteScopedPermissionReachesOnlyThatSite is the finding, stated as a test.
func TestSiteScopedPermissionReachesOnlyThatSite(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	personID := seedPerson(t, companyID, "P-SITE-A", "Site A Only")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect, active)
	             VALUES ($1, $2, 'SITE', $3, 'ALLOW', TRUE)`,
		companyID, personID, siteIDOf(t, "Site A"))

	// A reconcile is what turns a permission written directly into queued work,
	// and is also the path a validity window's expiry travels.
	added, removed, err := database.ReconcileDeviceRoster(deviceIDOf(t, "TERM-A"))
	if err != nil {
		t.Fatalf("reconciling TERM-A: %v", err)
	}
	if added != 1 || removed != 0 {
		t.Fatalf("TERM-A reconcile added %d removed %d, want 1 and 0", added, removed)
	}

	added, _, err = database.ReconcileDeviceRoster(deviceIDOf(t, "TERM-B"))
	if err != nil {
		t.Fatalf("reconciling TERM-B: %v", err)
	}
	if added != 0 {
		t.Fatalf("TERM-B received %d roster entries for a Site A permission", added)
	}

	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("TERM-A holds %d person jobs, want 1", n)
	}
	if n := jobsFor(t, "TERM-B"); n != 0 {
		t.Errorf("TERM-B holds %d person jobs, want 0", n)
	}
}

// TestTerminalScopedPermissionReachesOnlyThatTerminal, at the sync layer.
func TestTerminalScopedPermissionReachesOnlyThatTerminal(t *testing.T) {
	env, companyID := newScopedFixture(t)
	env.registerDevice(env.siteAKey, "TERM-A2")

	personID := seedPerson(t, companyID, "P-ONE-DOOR", "One Door Only")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, device_id, effect, active)
	             VALUES ($1, $2, 'TERMINAL', $3, 'ALLOW', TRUE)`,
		companyID, personID, deviceIDOf(t, "TERM-A"))

	for _, serial := range []string{"TERM-A", "TERM-A2", "TERM-B"} {
		if _, _, err := database.ReconcileDeviceRoster(deviceIDOf(t, serial)); err != nil {
			t.Fatalf("reconciling %s: %v", serial, err)
		}
	}

	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("the named terminal holds %d jobs, want 1", n)
	}
	// The second terminal is at the SAME SITE, so this proves the scope is the
	// terminal and not the site it happens to stand at.
	if n := jobsFor(t, "TERM-A2"); n != 0 {
		t.Errorf("a terminal at the same site holds %d jobs, want 0", n)
	}
	if n := jobsFor(t, "TERM-B"); n != 0 {
		t.Errorf("TERM-B holds %d jobs, want 0", n)
	}
}

// TestUnconditionalDenyRemovesSomebodyFromTheRoster.
//
// This is what makes an exclusion survive a network outage. A terminal caches a
// flat roster and does not evaluate permissions, so the only way an offline
// terminal can honour "not this person, not here" is for them not to be on it.
func TestUnconditionalDenyRemovesSomebodyFromTheRoster(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	personID := seedPerson(t, companyID, "P-EXCLUDED", "Excluded Here")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)

	deviceA := deviceIDOf(t, "TERM-A")
	if _, _, err := database.ReconcileDeviceRoster(deviceA); err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Fatalf("TERM-A holds %d jobs before the deny, want 1", n)
	}

	// A terminal-scoped DENY with no application and no schedule: they can never
	// be admitted here.
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, device_id, effect, active)
	             VALUES ($1, $2, 'TERMINAL', $3, 'DENY', TRUE)`, companyID, personID, deviceA)

	_, removed, err := database.ReconcileDeviceRoster(deviceA)
	if err != nil {
		t.Fatalf("reconciling after the deny: %v", err)
	}
	if removed != 1 {
		t.Fatalf("the reconcile queued %d removals, want 1", removed)
	}
	if n := deleteJobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("TERM-A has %d DELETE jobs, want 1", n)
	}
}

// TestConditionalDenyLeavesThemOnTheRoster is the documented limitation, asserted
// so it cannot change silently.
//
// A DENY narrowed to one capability is CONDITIONAL: the person is still allowed
// under other capabilities, and a terminal that dropped them entirely would
// refuse them when they should be admitted. They stay on the roster and the
// server decides at the door.
func TestConditionalDenyLeavesThemOnTheRoster(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	personID := seedPerson(t, companyID, "P-PARTIAL", "Partially Excluded")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)
	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, device_id, application, effect, active)
	             VALUES ($1, $2, 'TERMINAL', $3, 'ATTENDANCE', 'DENY', TRUE)`,
		companyID, personID, deviceIDOf(t, "TERM-A"))

	if _, _, err := database.ReconcileDeviceRoster(deviceIDOf(t, "TERM-A")); err != nil {
		t.Fatalf("reconciling: %v", err)
	}

	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("an application-scoped DENY removed somebody from the roster entirely (%d jobs)", n)
	}
	if n := deleteJobsFor(t, "TERM-A"); n != 0 {
		t.Errorf("an application-scoped DENY queued %d removals, want 0", n)
	}
}

// TestExpiredPermissionIsReconciledOffTheRoster.
//
// Roster membership changes WITH THE CLOCK, and nothing edits a row when a
// permission expires. Without the reconciler, "access expires on Friday" is a
// promise the platform makes and does not keep at an offline terminal.
func TestExpiredPermissionIsReconciledOffTheRoster(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	personID := seedPerson(t, companyID, "P-TEMPORARY", "Temporary Contractor")

	future := time.Now().UTC().Add(time.Hour)
	mustExec(t, `INSERT INTO permissions
	                (company_id, person_id, scope_type, effect, ends_at, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', $3, TRUE)`, companyID, personID, future)

	deviceA := deviceIDOf(t, "TERM-A")
	if _, _, err := database.ReconcileDeviceRoster(deviceA); err != nil {
		t.Fatalf("reconciling: %v", err)
	}
	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Fatalf("TERM-A holds %d jobs while the permission is live, want 1", n)
	}

	// The engagement ends. Nothing touches the person's row.
	mustExec(t, `UPDATE permissions SET ends_at = $2 WHERE person_id = $1`,
		personID, time.Now().UTC().Add(-time.Minute))

	_, removed, err := database.ReconcileDeviceRoster(deviceA)
	if err != nil {
		t.Fatalf("reconciling after expiry: %v", err)
	}
	if removed != 1 {
		t.Fatalf("the reconcile queued %d removals after expiry, want 1", removed)
	}
}

// TestReconcileIsIdempotent. Running it twice before the terminal has polled must
// not queue the same person twice, or a converged fleet would grow a backlog
// every time the scheduled task ran.
func TestReconcileIsIdempotent(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	personID := seedPerson(t, companyID, "P-STABLE", "Stable Person")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)

	deviceA := deviceIDOf(t, "TERM-A")
	added, _, err := database.ReconcileDeviceRoster(deviceA)
	if err != nil || added != 1 {
		t.Fatalf("first reconcile added %d (err %v), want 1", added, err)
	}

	for i := 0; i < 3; i++ {
		added, removed, err := database.ReconcileDeviceRoster(deviceA)
		if err != nil {
			t.Fatalf("repeat reconcile: %v", err)
		}
		if added != 0 || removed != 0 {
			t.Fatalf("repeat reconcile %d changed things (added %d removed %d); "+
				"a converged terminal must produce no work", i, added, removed)
		}
	}

	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("TERM-A holds %d jobs after four reconciles, want 1", n)
	}
}

// TestDeletionReachesTerminalsRegardlessOfPermission.
//
// A person losing their access must be WITHDRAWN from terminals that were
// holding them. By the time the delete runs, the rule that put them there may
// already be gone -- so testing roster membership would skip exactly the
// terminals that need telling, and the person would stay in the local table of
// every offline terminal for ever.
func TestDeletionReachesTerminalsRegardlessOfPermission(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	member := &models.Member{
		MemberID: "P-LEAVER", FullName: "Leaver", MembershipType: "STANDARD", Active: true,
	}
	mustExec(t, `UPDATE companies SET default_person_access = 'COMPANY_ALLOW' WHERE id = $1`, companyID)
	if err := database.CreateMember(companyID, member); err != nil {
		t.Fatalf("creating person: %v", err)
	}
	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Fatalf("TERM-A holds %d jobs after creation, want 1", n)
	}

	// Remove the permission FIRST, so the delete runs with no rule to match.
	mustExec(t, `UPDATE permissions SET deleted_at = CURRENT_TIMESTAMP
	             WHERE company_id = $1`, companyID)

	if err := database.DeleteMember(companyID, "P-LEAVER"); err != nil {
		t.Fatalf("deleting person: %v", err)
	}

	if n := deleteJobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("TERM-A received %d DELETE jobs, want 1 -- a person who lost their "+
			"permission before being removed would otherwise stay on the terminal for ever", n)
	}
	if n := deleteJobsFor(t, "TERM-B"); n != 1 {
		t.Errorf("TERM-B received %d DELETE jobs, want 1", n)
	}
}

// TestDefaultPersonAccessIsAPolicyRatherThanAConstant.
//
// Existing tenants were migrated to COMPANY_ALLOW so nothing they had built
// changed underneath them; tenants created through the platform API start at
// NONE. Both are asserted here because the whole value of the setting is that
// the two differ.
func TestDefaultPersonAccessIsAPolicyRatherThanAConstant(t *testing.T) {
	env, companyID := newScopedFixture(t)
	_ = env

	// NONE: creating somebody grants nothing.
	member := &models.Member{
		MemberID: "P-DENY-DEFAULT", FullName: "No Default Access",
		MembershipType: "STANDARD", Active: true,
	}
	if err := database.CreateMember(companyID, member); err != nil {
		t.Fatalf("creating person: %v", err)
	}
	if n := queryInt(t, `SELECT count(*) FROM permissions WHERE person_id = $1`, member.ID); n != 0 {
		t.Errorf("a NONE company granted %d permissions on create, want 0", n)
	}

	// COMPANY_ALLOW: creating somebody reproduces pre-014 behaviour exactly.
	mustExec(t, `UPDATE companies SET default_person_access = 'COMPANY_ALLOW' WHERE id = $1`, companyID)
	legacy := &models.Member{
		MemberID: "P-LEGACY-DEFAULT", FullName: "Legacy Default Access",
		MembershipType: "STANDARD", Active: true,
	}
	if err := database.CreateMember(companyID, legacy); err != nil {
		t.Fatalf("creating person: %v", err)
	}

	var scope, effect string
	mustScan(t, `SELECT scope_type, effect FROM permissions WHERE person_id = `+itoa(legacy.ID),
		&scope, &effect)
	if scope != models.ScopeCompany || effect != models.EffectAllow {
		t.Errorf("default grant = %s/%s, want COMPANY/ALLOW", scope, effect)
	}

	// And it is a REAL permission row, not a special case in the evaluator: it
	// can be seen and removed like any other.
	if n := jobsFor(t, "TERM-A"); n != 1 {
		t.Errorf("TERM-A holds %d jobs, want 1 for the legacy-default person only", n)
	}
}
