package main

import (
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
)

// Tests for the platform primitives introduced by migrations 012-015.
//
// These cover the SCHEMA, not the handlers: the back-fills that must not lose
// data, the constraints that must not be bypassable, and the compatibility
// shims that keep existing callers working. The handler-level behaviour is
// covered by the suites for each subsystem.
//
// Every test here would have failed before its migration, which is the bar the
// remediation register sets for calling a finding FIXED.

// ---------------------------------------------------------------------------
// 012: identity and credentials
// ---------------------------------------------------------------------------

// TestPersonTaxonomyIsNoLongerFixed proves GP-02: the database no longer
// enumerates what a company may call its people.
//
// The old constraint was
//
//	CHECK (person_type IN ('MEMBER','STAFF','CONTRACTOR','VISITOR'))
//
// which is a decision about what industry the customer is in. A school storing
// STUDENT, a clinic storing PATIENT and a venue storing ATTENDEE all failed.
func TestPersonTaxonomyIsNoLongerFixed(t *testing.T) {
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	for _, category := range []string{"STUDENT", "PATIENT", "ATTENDEE", "RESIDENT", "CREW"} {
		mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, person_type)
		             VALUES ($1, $2, $3, $4, $5)`,
			companyID, "P-"+category, "Person "+category, category, category)
	}

	var stored int
	mustScan(t, `SELECT count(*) FROM people WHERE company_id = `+itoa(companyID), &stored)
	if stored != 5 {
		t.Fatalf("expected 5 people across five vocabularies, got %d", stored)
	}
	_ = env
}

// TestPersonCategoriesAreCompanyScoped proves the vocabulary is per tenant.
//
// Two companies must be able to use the same code for different things without
// colliding, and neither may see the other's.
func TestPersonCategoriesAreCompanyScoped(t *testing.T) {
	newTestEnv(t)
	one := companyIDBySlug(t, "one")
	two := companyIDBySlug(t, "two")

	mustExec(t, `INSERT INTO person_categories (company_id, code, label) VALUES ($1, 'STAFF', 'Teaching staff')`, one)
	mustExec(t, `INSERT INTO person_categories (company_id, code, label) VALUES ($1, 'STAFF', 'Ground crew')`, two)

	var label string
	mustScan(t, `SELECT label FROM person_categories WHERE company_id = `+itoa(one)+` AND code = 'STAFF'`, &label)
	if label != "Teaching staff" {
		t.Fatalf("company one's STAFF label = %q, want Teaching staff", label)
	}

	// The same code twice in ONE company is the collision that must be refused.
	_, err := database.DB.Exec(
		`INSERT INTO person_categories (company_id, code, label) VALUES ($1, 'STAFF', 'Duplicate')`, one)
	if err == nil {
		t.Fatal("a duplicate category code within one company was accepted")
	}
}

// TestLegacyFingerprintBecomesACredential proves the 012 back-fill preserves the
// FACT of enrolment.
//
// Before 012 a person's enrolment was a string in people.fingerprint_template.
// Losing it during the migration would have silently unenrolled everyone, which
// the console would then report as "no biometric" for people who can open doors
// right now.
func TestLegacyFingerprintBecomesACredential(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	var deviceID int64
	mustScan(t, `INSERT INTO devices (site_id, serial_number, device_name, status)
	             VALUES (`+itoa(siteID)+`, 'ESP32-LEGACY', 'Legacy terminal', 'ONLINE')
	             RETURNING id`, &deviceID)

	// Exactly the locator shape the firmware uploads.
	mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, fingerprint_template)
	             VALUES ($1, 'M-LEGACY', 'Legacy Person', 'STANDARD', 'terminal:ESP32-LEGACY:slot:7')`,
		companyID)

	// Re-run the back-fill the way the migration does, since newTestEnv
	// truncated the rows the migration originally saw.
	backfillLegacyCredentials(t)

	var credentialCount int
	mustScan(t, `SELECT count(*) FROM credentials c
	              JOIN people p ON p.id = c.person_id
	             WHERE p.external_id = 'M-LEGACY'`, &credentialCount)
	if credentialCount != 1 {
		t.Fatalf("expected the legacy enrolment to become one credential, got %d", credentialCount)
	}

	var status, credType string
	mustScan(t, `SELECT c.status, c.credential_type FROM credentials c
	              JOIN people p ON p.id = c.person_id
	             WHERE p.external_id = 'M-LEGACY'`, &status, &credType)
	if status != "ACTIVE" {
		t.Errorf("legacy credential status = %q, want ACTIVE -- these people can open doors today", status)
	}
	if credType != "FINGERPRINT" {
		t.Errorf("legacy credential type = %q, want FINGERPRINT", credType)
	}

	// The placement is the part that could only be recovered once: after the
	// migration, which sensor holds the template exists nowhere else.
	var slot int
	var placementState string
	mustScan(t, `SELECT pl.slot, pl.state
	               FROM credential_placements pl
	               JOIN credentials c ON c.id = pl.credential_id
	               JOIN people p ON p.id = c.person_id
	              WHERE p.external_id = 'M-LEGACY'`, &slot, &placementState)
	if slot != 7 {
		t.Errorf("recovered slot = %d, want 7 from the locator string", slot)
	}
	if placementState != "PLACED" {
		t.Errorf("recovered placement state = %q, want PLACED", placementState)
	}
}

// TestCredentialSealingIsAllOrNothing proves a credential cannot carry
// half a sealing envelope.
//
// Material with no key id and no algorithm is material nothing can ever decrypt,
// and it would sit in the table looking like a working credential.
func TestCredentialSealingIsAllOrNothing(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	personID := seedPerson(t, companyID, "M-SEAL", "Seal Test")

	_, err := database.DB.Exec(`
		INSERT INTO credentials (company_id, person_id, credential_type, status, sealed_material)
		VALUES ($1, $2, 'FINGERPRINT', 'ACTIVE', '\x0102030405'::bytea)`, companyID, personID)
	if err == nil {
		t.Fatal("sealed material without a key id or algorithm was accepted")
	}

	// The complete envelope is accepted.
	mustExec(t, `INSERT INTO credentials
	             (company_id, person_id, credential_type, status,
	              sealed_material, sealed_key_id, sealed_algorithm, material_digest)
	             VALUES ($1, $2, 'FINGERPRINT', 'ACTIVE',
	                     '\x0102030405'::bytea, 'company-1-v1', 'AES-256-GCM', $3)`,
		companyID, personID, strings.Repeat("a", 64))
}

// TestRevokedCredentialMustRecordWhen proves the revocation invariant.
//
// "Is this revoked" is asked on the authorization path. A REVOKED row with no
// timestamp would answer yes without being able to say since when, which is the
// first question an incident review asks.
func TestRevokedCredentialMustRecordWhen(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	personID := seedPerson(t, companyID, "M-REVOKE", "Revoke Test")

	_, err := database.DB.Exec(`
		INSERT INTO credentials (company_id, person_id, credential_type, status, identifier)
		VALUES ($1, $2, 'CARD', 'REVOKED', 'CARD-1')`, companyID, personID)
	if err == nil {
		t.Fatal("a REVOKED credential with no revoked_at was accepted")
	}
}

// TestOneSlotHoldsOneCredential proves the placement uniqueness that keeps a
// sensor's slot map unambiguous.
//
// The firmware allocates the lowest free slot, so the slot a deletion frees is
// the very next one an enrolment gets. Two live placements on one slot would
// resolve a finger to whichever row was read first.
func TestOneSlotHoldsOneCredential(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	var deviceID int64
	mustScan(t, `INSERT INTO devices (site_id, serial_number, device_name, status)
	             VALUES (`+itoa(siteID)+`, 'ESP32-SLOTS', 'Slots', 'ONLINE') RETURNING id`, &deviceID)

	firstPerson := seedPerson(t, companyID, "M-SLOT-1", "First")
	secondPerson := seedPerson(t, companyID, "M-SLOT-2", "Second")

	firstCred := seedCredential(t, companyID, firstPerson, "CARD-SLOT-1")
	secondCred := seedCredential(t, companyID, secondPerson, "CARD-SLOT-2")

	mustExec(t, `INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
	             VALUES ($1, $2, 3, 'PLACED', CURRENT_TIMESTAMP)`, firstCred, deviceID)

	_, err := database.DB.Exec(`
		INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
		VALUES ($1, $2, 3, 'PLACED', CURRENT_TIMESTAMP)`, secondCred, deviceID)
	if err == nil {
		t.Fatal("two live placements were accepted on the same device slot")
	}

	// Removing the first frees the slot, exactly as the sensor does.
	mustExec(t, `UPDATE credential_placements SET state = 'REMOVED', removed_at = CURRENT_TIMESTAMP
	              WHERE credential_id = $1`, firstCred)
	mustExec(t, `INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
	             VALUES ($1, $2, 3, 'PLACED', CURRENT_TIMESTAMP)`, secondCred, deviceID)
}

// TestPlacedPlacementMustNameItsSlot proves a device cannot claim to hold a
// credential without saying where.
func TestPlacedPlacementMustNameItsSlot(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	var deviceID int64
	mustScan(t, `INSERT INTO devices (site_id, serial_number, device_name, status)
	             VALUES (`+itoa(siteID)+`, 'ESP32-NOSLOT', 'No slot', 'ONLINE') RETURNING id`, &deviceID)

	personID := seedPerson(t, companyID, "M-NOSLOT", "No Slot")
	credID := seedCredential(t, companyID, personID, "CARD-NOSLOT")

	_, err := database.DB.Exec(`
		INSERT INTO credential_placements (credential_id, device_id, state)
		VALUES ($1, $2, 'PLACED')`, credID, deviceID)
	if err == nil {
		t.Fatal("a PLACED placement with no slot and no placed_at was accepted")
	}
}

// ---------------------------------------------------------------------------
// 013: events and audit
// ---------------------------------------------------------------------------

// TestEventsAreImmutable proves the audit trail cannot be rewritten by the
// application that writes it.
//
// Enforced at the database, not by convention, because the application is the
// thing most likely to try to "correct" an event.
func TestEventsAreImmutable(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	var eventID int64
	mustScan(t, `INSERT INTO events (company_id, event_type, decision, subject_external_id)
	             VALUES (`+itoa(companyID)+`, 'ACCESS_GRANTED', 'GRANTED', 'M-1')
	             RETURNING id`, &eventID)

	if _, err := database.DB.Exec(
		`UPDATE events SET decision = 'DENIED' WHERE id = $1`, eventID); err == nil {
		t.Error("an event was updatable; the trail is not immutable")
	}

	if _, err := database.DB.Exec(
		`DELETE FROM events WHERE id = $1`, eventID); err == nil {
		t.Error("an event was deletable outside the retention purge")
	}

	var decision string
	mustScan(t, `SELECT decision FROM events WHERE id = `+itoa(eventID), &decision)
	if decision != "GRANTED" {
		t.Errorf("event decision changed to %q despite the refusal", decision)
	}
}

// TestAuditEventsAreImmutable proves the same for the operator trail, which is
// the one somebody would have a motive to edit.
func TestAuditEventsAreImmutable(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	var auditID int64
	mustScan(t, `INSERT INTO audit_events (company_id, action, actor_email)
	             VALUES (`+itoa(companyID)+`, 'SITE_CREATED', 'ops@example.com')
	             RETURNING id`, &auditID)

	if _, err := database.DB.Exec(
		`UPDATE audit_events SET actor_email = 'someone.else@example.com' WHERE id = $1`,
		auditID); err == nil {
		t.Error("an audit record was updatable")
	}
	if _, err := database.DB.Exec(
		`DELETE FROM audit_events WHERE id = $1`, auditID); err == nil {
		t.Error("an audit record was deletable outside the retention purge")
	}
}

// TestRetentionPurgeRemovesOnlyExpiredRows proves the one sanctioned exception
// to immutability is narrow.
//
// It must delete by AGE against the company's own configured window, and must
// not touch a company that has configured none -- NULL means keep for ever, and
// a purge that treated it as zero would silently destroy a customer's history.
func TestRetentionPurgeRemovesOnlyExpiredRows(t *testing.T) {
	newTestEnv(t)
	retained := companyIDBySlug(t, "one")
	unconfigured := companyIDBySlug(t, "two")

	mustExec(t, `UPDATE companies SET event_retention_days = 30 WHERE id = $1`, retained)

	old := time.Now().AddDate(0, 0, -90)
	recent := time.Now().AddDate(0, 0, -5)

	mustExec(t, `INSERT INTO events (company_id, event_type, decision, occurred_at)
	             VALUES ($1, 'ACCESS_GRANTED', 'GRANTED', $2)`, retained, old)
	mustExec(t, `INSERT INTO events (company_id, event_type, decision, occurred_at)
	             VALUES ($1, 'ACCESS_GRANTED', 'GRANTED', $2)`, retained, recent)
	mustExec(t, `INSERT INTO events (company_id, event_type, decision, occurred_at)
	             VALUES ($1, 'ACCESS_GRANTED', 'GRANTED', $2)`, unconfigured, old)

	var removed int
	mustScan(t, `SELECT purge_events(NULL)`, &removed)
	if removed != 1 {
		t.Fatalf("purge removed %d rows, want exactly the one past its window", removed)
	}

	var retainedLeft, unconfiguredLeft int
	mustScan(t, `SELECT count(*) FROM events WHERE company_id = `+itoa(retained), &retainedLeft)
	mustScan(t, `SELECT count(*) FROM events WHERE company_id = `+itoa(unconfigured), &unconfiguredLeft)

	if retainedLeft != 1 {
		t.Errorf("configured company kept %d events, want the one inside its window", retainedLeft)
	}
	if unconfiguredLeft != 1 {
		t.Errorf("company with NULL retention lost history: %d events left, want 1", unconfiguredLeft)
	}
}

// TestEventDecisionIsConstrainedButTypeIsNot proves the deliberate asymmetry
// described in 013.
//
// `decision` is a platform concept the authorization path branches on, so it is
// closed. `event_type` is what an application defines, so it is open -- closing
// it would repeat exactly the mistake 009 made with capability codes, which the
// audit found as GP-03.
func TestEventDecisionIsConstrainedButTypeIsNot(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	// An application the platform has never heard of may define its own type.
	mustExec(t, `INSERT INTO events (company_id, event_type, decision)
	             VALUES ($1, 'LOCKER_RELEASED', 'GRANTED')`, companyID)
	mustExec(t, `INSERT INTO events (company_id, event_type, decision)
	             VALUES ($1, 'SHIFT_HANDOVER_CONFIRMED', 'RECORDED')`, companyID)

	// A decision outside the platform's four is refused.
	if _, err := database.DB.Exec(`INSERT INTO events (company_id, event_type, decision)
	                               VALUES ($1, 'ACCESS_GRANTED', 'MAYBE')`, companyID); err == nil {
		t.Error("an unknown decision was accepted; the authorization path branches on this column")
	}

	// A type that could not survive the trip to firmware is refused on format.
	if _, err := database.DB.Exec(`INSERT INTO events (company_id, event_type, decision)
	                               VALUES ($1, 'lower case type', 'RECORDED')`, companyID); err == nil {
		t.Error("a malformed event type was accepted")
	}
}

// TestAccessLogsWereCarriedForwardAsEvents proves the 013 back-fill preserves
// history, and preserves the device-generated idempotency key with it.
//
// Without carrying public_id across, a terminal retrying an upload from before
// the migration would create a duplicate audit line in the new table.
func TestAccessLogsWereCarriedForwardAsEvents(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	var publicID string
	mustScan(t, `INSERT INTO access_logs
	             (company_id, site_id, person_external_id, granted, source, site_name, occurred_at)
	             VALUES (`+itoa(companyID)+`, `+itoa(siteID)+`, 'M-HIST', TRUE, 'FINGERPRINT', 'Site A',
	                     CURRENT_TIMESTAMP)
	             RETURNING public_id`, &publicID)

	backfillEventsFromAccessLogs(t)

	var eventType, decision, application string
	mustScan(t, `SELECT event_type, decision, COALESCE(application,'')
	               FROM events WHERE public_id = '`+publicID+`'`,
		&eventType, &decision, &application)

	if eventType != "ACCESS_GRANTED" || decision != "GRANTED" {
		t.Errorf("carried-forward event = %s/%s, want ACCESS_GRANTED/GRANTED", eventType, decision)
	}
	if application != "ACCESS_CONTROL" {
		t.Errorf("carried-forward event application = %q, want ACCESS_CONTROL", application)
	}

	// The idempotency key survived, so a replay still collides.
	backfillEventsFromAccessLogs(t)
	var copies int
	mustScan(t, `SELECT count(*) FROM events WHERE public_id = '`+publicID+`'`, &copies)
	if copies != 1 {
		t.Errorf("re-running the back-fill produced %d copies; the idempotency key was not preserved", copies)
	}
}

// ---------------------------------------------------------------------------
// 014: the authorization engine
// ---------------------------------------------------------------------------

// TestPermissionBackfillPreservesExistingBehaviour proves the engine can be
// switched on without locking out a deployed installation.
//
// Deny-by-default is correct and is what the engine does. Applied to a database
// with no permission rows -- which is every existing deployment -- it would
// refuse everybody on upgrade day. The back-fill writes down the behaviour they
// have today so the default only governs people created after it.
func TestPermissionBackfillPreservesExistingBehaviour(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	seedPerson(t, companyID, "M-EXISTING-1", "Existing One")
	seedPerson(t, companyID, "M-EXISTING-2", "Existing Two")

	backfillCompanyPermissions(t)

	var granted int
	mustScan(t, `SELECT count(*) FROM permissions
	              WHERE company_id = `+itoa(companyID)+`
	                AND scope_type = 'COMPANY' AND effect = 'ALLOW'
	                AND deleted_at IS NULL`, &granted)
	if granted != 2 {
		t.Fatalf("back-fill granted %d company-scoped ALLOWs, want one per existing person", granted)
	}

	// Idempotent: re-running must not double-grant.
	backfillCompanyPermissions(t)
	mustScan(t, `SELECT count(*) FROM permissions
	              WHERE company_id = `+itoa(companyID)+` AND deleted_at IS NULL`, &granted)
	if granted != 2 {
		t.Errorf("re-running the back-fill produced %d rows, want 2", granted)
	}
}

// TestPermissionScopeMustCarryItsTarget proves a rule cannot name a scope
// without the thing it scopes to.
//
// A SITE-scoped row with no site matches nothing, and would sit in the table
// looking like access somebody had been granted.
func TestPermissionScopeMustCarryItsTarget(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	personID := seedPerson(t, companyID, "M-SCOPE", "Scope Test")

	cases := []struct {
		name  string
		query string
	}{
		{"site scope with no site", `INSERT INTO permissions (company_id, person_id, scope_type, effect)
		                             VALUES ($1, $2, 'SITE', 'ALLOW')`},
		{"terminal scope with no terminal", `INSERT INTO permissions (company_id, person_id, scope_type, effect)
		                                     VALUES ($1, $2, 'TERMINAL', 'ALLOW')`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := database.DB.Exec(tc.query, companyID, personID); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}

	// A COMPANY scope naming a site is equally wrong in the other direction.
	siteID := siteIDByKey(t, "test-site-a-key")
	if _, err := database.DB.Exec(`INSERT INTO permissions (company_id, person_id, scope_type, site_id, effect)
	                               VALUES ($1, $2, 'COMPANY', $3, 'ALLOW')`,
		companyID, personID, siteID); err == nil {
		t.Error("a COMPANY-scoped permission naming a site was accepted")
	}
}

// TestScheduleWindowsSupportMultipleSpans proves the model can express what the
// 002 inline columns could not.
//
// "Weekdays 09:00-17:00 and Saturday mornings" is two windows on one schedule.
// The old shape had one day mask and one time pair per permission, so it could
// say only one of those.
func TestScheduleWindowsSupportMultipleSpans(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	var scheduleID int64
	mustScan(t, `INSERT INTO schedules (company_id, name) VALUES (`+itoa(companyID)+`, 'Office hours')
	             RETURNING id`, &scheduleID)

	// Mon-Fri = 1+2+4+8+16 = 31
	mustExec(t, `INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
	             VALUES ($1, 31, '09:00', '17:00')`, scheduleID)
	// Saturday = 32
	mustExec(t, `INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
	             VALUES ($1, 32, '10:00', '13:00')`, scheduleID)
	// A night shift crossing midnight: end before start is one window, not two.
	mustExec(t, `INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
	             VALUES ($1, 64, '22:00', '06:00')`, scheduleID)

	var windows int
	mustScan(t, `SELECT count(*) FROM schedule_windows WHERE schedule_id = `+itoa(scheduleID), &windows)
	if windows != 3 {
		t.Fatalf("schedule holds %d windows, want 3", windows)
	}

	// A zero-length window admits nobody and is never what somebody meant.
	if _, err := database.DB.Exec(`INSERT INTO schedule_windows (schedule_id, days_of_week, start_time, end_time)
	                               VALUES ($1, 127, '09:00', '09:00')`, scheduleID); err == nil {
		t.Error("a zero-length schedule window was accepted")
	}
}

// TestDoorsAreNoLongerInTheAuthorizationModel proves the industry noun is gone
// from the live model.
//
// `doors` is retained only because devices.door_id and access_logs.door_id still
// reference it. Nothing in the authorization path may.
func TestDoorsAreNoLongerInTheAuthorizationModel(t *testing.T) {
	newTestEnv(t)

	var hasDoorColumn bool
	mustScan(t, `SELECT EXISTS (
	                SELECT 1 FROM information_schema.columns
	                 WHERE table_name = 'permissions' AND column_name = 'door_id')`, &hasDoorColumn)
	if hasDoorColumn {
		t.Error("permissions still carries door_id; the access-control noun is back in the model")
	}

	var hasAccessLevel bool
	mustScan(t, `SELECT EXISTS (
	                SELECT 1 FROM information_schema.columns
	                 WHERE table_name = 'permissions' AND column_name = 'access_level')`, &hasAccessLevel)
	if hasAccessLevel {
		t.Error("permissions still carries access_level, a three-tier model with no semantics")
	}
}

// ---------------------------------------------------------------------------
// 015: platform administration and the capability catalogue
// ---------------------------------------------------------------------------

// TestApplicationCatalogueStatesWhatItActuallyDoes proves the platform records
// an honest maturity per capability.
//
// The audit's finding was that the console offered seven capabilities of which
// none had behaviour, and no part of the system was in a position to say so.
// This is that statement, and it must never be more optimistic than the code.
func TestApplicationCatalogueStatesWhatItActuallyDoes(t *testing.T) {
	newTestEnv(t)

	var total int
	mustScan(t, `SELECT count(*) FROM applications`, &total)
	if total < 7 {
		t.Fatalf("catalogue holds %d capabilities, want at least the seven declared", total)
	}

	// Every row must carry a status the platform defined, and a detail
	// explaining it. A status with no detail is a claim with no evidence.
	rows, err := database.DB.Query(`SELECT code, status, COALESCE(status_detail, '') FROM applications`)
	if err != nil {
		t.Fatalf("reading catalogue: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var code, status, detail string
		if err := rows.Scan(&code, &status, &detail); err != nil {
			t.Fatal(err)
		}
		if status == "IMPLEMENTED" && detail == "" {
			t.Errorf("%s claims IMPLEMENTED with no detail", code)
		}
		if detail == "" {
			t.Errorf("%s carries no status detail", code)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating catalogue: %v", err)
	}
}

// TestCapabilitiesAreDataNotAConstraint proves GP-03.
//
// 009 put the capability list in two CHECK constraints, so adding one to a
// platform whose selling point is configurability needed a migration in two
// places plus a Go constant. A new capability must now be an INSERT.
func TestCapabilitiesAreDataNotAConstraint(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `INSERT INTO applications (code, name, description, status, status_detail)
	             VALUES ('LOCKER_ACCESS', 'Locker Access', 'Release a locker.',
	                     'NOT_IMPLEMENTED', 'Declared for catalogue completeness.')`)

	// A company can configure it with no schema change anywhere.
	mustExec(t, `INSERT INTO company_applications (company_id, application, enabled)
	             VALUES ($1, 'LOCKER_ACCESS', TRUE)`, companyID)

	// And a terminal can be pointed at it.
	siteID := siteIDByKey(t, "test-site-a-key")
	mustExec(t, `INSERT INTO devices (site_id, serial_number, device_name, status, application_mode)
	             VALUES (`+itoa(siteID)+`, 'ESP32-LOCKER', 'Locker', 'ONLINE', 'LOCKER_ACCESS')`)

	// Something not in the catalogue is still refused, at both places.
	if _, err := database.DB.Exec(`INSERT INTO company_applications (company_id, application, enabled)
	                               VALUES ($1, 'INVENTED', TRUE)`, companyID); err == nil {
		t.Error("a capability absent from the catalogue was configurable")
	}
	if _, err := database.DB.Exec(`INSERT INTO devices (site_id, serial_number, device_name, status, application_mode)
	                               VALUES ($1, 'ESP32-BAD', 'Bad', 'ONLINE', 'INVENTED')`,
		siteID); err == nil {
		t.Error("a terminal was assignable to a capability absent from the catalogue")
	}
}

// TestCompanyApplicationResolvesItsCatalogueRow proves the compatibility shim.
//
// Every existing writer names a capability by its CODE -- the console, the
// device settings payload, and the upsert in database/applications.go. The
// foreign key is maintained by the database from whichever column the caller
// supplied, so those callers keep working unchanged.
func TestCompanyApplicationResolvesItsCatalogueRow(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `INSERT INTO company_applications (company_id, application, enabled)
	             VALUES ($1, 'ACCESS_CONTROL', TRUE)`, companyID)

	var appID int64
	var code string
	mustScan(t, `SELECT ca.application_id, a.code
	               FROM company_applications ca
	               JOIN applications a ON a.id = ca.application_id
	              WHERE ca.company_id = `+itoa(companyID), &appID, &code)
	if code != "ACCESS_CONTROL" {
		t.Fatalf("resolved catalogue row = %q, want ACCESS_CONTROL", code)
	}
}

// TestCompanyDeactivationStaysConsistent proves the flag and the timestamp
// cannot disagree, and that the ordinary write still works.
//
// A bare CHECK would have rejected `UPDATE companies SET active = FALSE`, which
// is what every existing caller and runbook does, and forced each of them to
// remember a second column. The trigger derives one from the other.
func TestCompanyDeactivationStaysConsistent(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")

	mustExec(t, `UPDATE companies SET active = FALSE WHERE id = $1`, companyID)

	var active bool
	var deactivatedAt *time.Time
	mustScan(t, `SELECT active, deactivated_at FROM companies WHERE id = `+itoa(companyID),
		&active, &deactivatedAt)
	if active {
		t.Fatal("company is still active after being deactivated")
	}
	if deactivatedAt == nil {
		t.Fatal("deactivated_at was not stamped; the pair can disagree")
	}

	mustExec(t, `UPDATE companies SET active = TRUE WHERE id = $1`, companyID)
	mustScan(t, `SELECT active, deactivated_at FROM companies WHERE id = `+itoa(companyID),
		&active, &deactivatedAt)
	if !active {
		t.Fatal("company did not reactivate")
	}
	if deactivatedAt != nil {
		t.Error("deactivated_at survived reactivation; the pair can disagree")
	}
}

// TestPlatformAdminIsASeparateCredentialClass proves the tenancy invariant
// survived the addition of a platform identity.
//
// 008 argued that one company per operator is what makes the tenancy boundary
// checkable, and that a cross-company operator would dissolve it everywhere.
// That reasoning holds: users.company_id must still be NOT NULL, and the
// platform identity must live somewhere else entirely.
func TestPlatformAdminIsASeparateCredentialClass(t *testing.T) {
	newTestEnv(t)

	var nullable string
	mustScan(t, `SELECT is_nullable FROM information_schema.columns
	              WHERE table_name = 'users' AND column_name = 'company_id'`, &nullable)
	if nullable != "NO" {
		t.Fatal("users.company_id became nullable; the single-tenant-per-operator " +
			"contract that makes every query's tenancy filter checkable is gone")
	}

	// The platform identity exists, and in its own table with its own sessions.
	for _, table := range []string{"platform_admins", "platform_sessions"} {
		var exists bool
		mustScan(t, `SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                             WHERE table_name = '`+table+`')`, &exists)
		if !exists {
			t.Errorf("%s does not exist", table)
		}
	}

	// And it carries no company, because it is not a tenant.
	var hasCompany bool
	mustScan(t, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
	                             WHERE table_name = 'platform_admins' AND column_name = 'company_id')`,
		&hasCompany)
	if hasCompany {
		t.Error("platform_admins carries a company_id; it is supposed to be outside the tenancy model")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// The back-fills, re-run against fixture data. newTestEnv truncates the tables
// the migration originally read, so a test that wants to prove a back-fill has
// to run the same statement the migration runs.
//
// These are deliberately COPIES of the migration SQL rather than a shared
// function: the thing under test is what the migration will actually do to a
// customer's database, and a shared helper could drift from it silently.

func backfillLegacyCredentials(t *testing.T) {
	t.Helper()
	mustExec(t, `
		INSERT INTO credentials (company_id, person_id, credential_type, identifier,
		                         status, enrolled_at, created_at)
		SELECT p.company_id, p.id, 'FINGERPRINT',
		       'legacy:' || left(p.fingerprint_template, 100),
		       'ACTIVE', p.updated_at, p.created_at
		  FROM people p
		 WHERE p.fingerprint_template IS NOT NULL
		   AND btrim(p.fingerprint_template) <> ''
		   AND p.deleted_at IS NULL
		ON CONFLICT DO NOTHING`)

	mustExec(t, `
		INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
		SELECT c.id, d.id,
		       NULLIF(split_part(c.identifier, ':slot:', 2), '')::int,
		       'PLACED', c.enrolled_at
		  FROM credentials c
		  JOIN devices d
		    ON d.serial_number = split_part(split_part(c.identifier, 'legacy:terminal:', 2), ':slot:', 1)
		   AND d.deleted_at IS NULL
		 WHERE c.identifier LIKE 'legacy:terminal:%:slot:%'
		   AND split_part(c.identifier, ':slot:', 2) ~ '^[0-9]+$'
		   AND (split_part(c.identifier, ':slot:', 2))::int > 0
		ON CONFLICT DO NOTHING`)
}

func backfillEventsFromAccessLogs(t *testing.T) {
	t.Helper()
	mustExec(t, `
		INSERT INTO events (public_id, company_id, site_id, device_id, person_id,
		                    subject_external_id, event_type, application, decision,
		                    direction, occurred_at, recorded_at, occurred_at_trusted)
		SELECT al.public_id, al.company_id, al.site_id, al.device_id, al.person_id,
		       al.person_external_id,
		       CASE WHEN al.granted THEN 'ACCESS_GRANTED' ELSE 'ACCESS_DENIED' END,
		       'ACCESS_CONTROL',
		       CASE WHEN al.granted THEN 'GRANTED' ELSE 'DENIED' END,
		       NULL, al.occurred_at, al.created_at, FALSE
		  FROM access_logs al
		ON CONFLICT (public_id) DO NOTHING`)
}

func backfillCompanyPermissions(t *testing.T) {
	t.Helper()
	mustExec(t, `
		INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
		SELECT p.company_id, p.id, 'COMPANY', 'ALLOW', TRUE
		  FROM people p
		 WHERE p.deleted_at IS NULL
		   AND NOT EXISTS (
		        SELECT 1 FROM permissions x
		         WHERE x.person_id = p.id
		           AND x.scope_type = 'COMPANY'
		           AND x.effect = 'ALLOW'
		           AND x.application IS NULL
		           AND x.deleted_at IS NULL
		   )`)
}

func companyIDBySlug(t *testing.T, slug string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM companies WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("resolving company %q: %v", slug, err)
	}
	return id
}

func seedPerson(t *testing.T, companyID int64, externalID, name string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`INSERT INTO people (company_id, external_id, full_name, membership_type)
		 VALUES ($1, $2, $3, 'STANDARD') RETURNING id`,
		companyID, externalID, name).Scan(&id); err != nil {
		t.Fatalf("seeding person %q: %v", externalID, err)
	}
	return id
}

func seedCredential(t *testing.T, companyID, personID int64, identifier string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`INSERT INTO credentials (company_id, person_id, credential_type, identifier, status)
		 VALUES ($1, $2, 'CARD', $3, 'ACTIVE') RETURNING id`,
		companyID, personID, identifier).Scan(&id); err != nil {
		t.Fatalf("seeding credential %q: %v", identifier, err)
	}
	return id
}
