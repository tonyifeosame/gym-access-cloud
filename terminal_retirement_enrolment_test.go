package main

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Retiring a terminal ends the fingerprint enrolments that lived only on it, and
// nothing else. See database/terminal_enrolment.go.

type retireEnrolmentFixture struct {
	*credentialViewFixture
	otherDeviceID int64
}

const (
	retireSerial = "AT-FRONT"
	otherSerial  = "AT-OTHER"
)

func newRetireEnrolmentFixture(t *testing.T) *retireEnrolmentFixture {
	t.Helper()
	f := newCredentialViewFixture(t)
	f.env.registerDevice(f.env.siteAKey, otherSerial)
	return &retireEnrolmentFixture{f, deviceIDBySerial(t, otherSerial)}
}

// enrolOn seeds a person with an ACTIVE credential and the matching legacy locator,
// PLACED on the given terminal: what a completed enrolment leaves behind.
func (f *retireEnrolmentFixture) enrolOn(t *testing.T, externalID string, deviceID int64,
	serial string, slot int) (int64, string) {
	t.Helper()
	personID := seedPerson(t, f.companyID, externalID, "Person "+externalID)
	credential := seedFingerprintCredential(t, f.companyID, personID, deviceID, models.CredentialActive)
	seedPlacement(t, credential, deviceID, models.PlacementPlaced, slot)
	mustExec(t, `UPDATE people SET fingerprint_template = $1 WHERE id = $2`,
		"terminal:"+serial+":slot:"+itoa(int64(slot)), personID)
	return personID, credential
}

func (f *retireEnrolmentFixture) retire(t *testing.T) *database.TerminalLifecycleResult {
	t.Helper()
	result, err := database.RetireTerminal(f.companyID, retireSerial, "gone", 0)
	if err != nil {
		t.Fatalf("RetireTerminal: %v", err)
	}
	return result
}

// enrolled is what the People page reads: both the list and the detail page.
func (f *retireEnrolmentFixture) enrolled(t *testing.T, externalID string) bool {
	t.Helper()
	code, body := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/"+externalID, "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("GET person %s = %d (%v)", externalID, code, body)
	}
	got, _ := body["biometric_enrolled"].(bool)

	code, list := consoleCall(t, f.env.router, "GET", "/api/v1/console/people", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("GET people = %d (%v)", code, list)
	}
	for _, raw := range listOf(t, list, "people") {
		person := raw.(map[string]any)
		if person["external_id"] == externalID && person["biometric_enrolled"] != got {
			t.Errorf("%s: list says %v, detail says %v", externalID, person["biometric_enrolled"], got)
		}
	}
	return got
}

// detailEnrolled is the person page's own badge, which reads the legacy column.
func (f *retireEnrolmentFixture) detailEnrolled(t *testing.T, externalID string) bool {
	t.Helper()
	code, body := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/"+externalID+"/enrollment", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("GET enrollment %s = %d (%v)", externalID, code, body)
	}
	got, _ := body["biometric_enrolled"].(bool)
	return got
}

func credentialStatus(t *testing.T, publicID string) string {
	t.Helper()
	return queryString(t, `SELECT status FROM credentials WHERE public_id = $1::uuid`, publicID)
}

func TestRetireResetsEveryPersonEnrolledOnlyOnThatTerminal(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	ids := []string{"P-1", "P-2", "P-3"}
	credentials := make([]string, len(ids))
	for i, id := range ids {
		_, credentials[i] = f.enrolOn(t, id, f.deviceID, retireSerial, i+1)
		if !f.enrolled(t, id) {
			t.Fatalf("%s should start Enrolled", id)
		}
	}

	if got := f.retire(t).EnrolmentsReset; got != len(ids) {
		t.Errorf("EnrolmentsReset = %d, want %d", got, len(ids))
	}

	for i, id := range ids {
		if f.enrolled(t, id) {
			t.Errorf("%s still reads Enrolled", id)
		}
		if f.detailEnrolled(t, id) {
			t.Errorf("%s still reads Enrolled on the person page", id)
		}
		if got := credentialStatus(t, credentials[i]); got != models.CredentialRevoked {
			t.Errorf("%s credential = %s, want REVOKED", id, got)
		}
	}
	if got := queryString(t, `SELECT revoked_reason FROM credentials WHERE public_id = $1::uuid`,
		credentials[0]); got != "terminal retired" {
		t.Errorf("revoked_reason = %q", got)
	}
}

func TestRetireKeepsPeopleAndTheirRecords(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID, credential := f.enrolOn(t, "P-KEEP", f.deviceID, retireSerial, 1)
	mustExec(t, `INSERT INTO access_logs
	             (company_id, site_id, device_id, person_id, person_external_id, granted, source, site_name, occurred_at)
	             VALUES ($1, (SELECT site_id FROM devices WHERE id = $2), $2, $3, 'P-KEEP', TRUE,
	                     'FINGERPRINT', 'Site A', CURRENT_TIMESTAMP)`, f.companyID, f.deviceID, personID)
	logsBefore := queryInt(t, `SELECT count(*) FROM access_logs WHERE person_id = `+itoa(personID))

	f.retire(t)

	code, body := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/P-KEEP", "", f.token, "")
	if code != http.StatusOK {
		t.Fatalf("the person must survive: GET = %d (%v)", code, body)
	}
	if body["full_name"] != "Person P-KEEP" {
		t.Errorf("full_name = %v", body["full_name"])
	}
	if got := queryInt(t, `SELECT count(*) FROM access_logs WHERE person_id = `+itoa(personID)); got != logsBefore {
		t.Errorf("access logs = %d, want %d", got, logsBefore)
	}
	// The credential row stays as history, visible but no longer counted.
	if got := queryInt(t, `SELECT count(*) FROM credentials WHERE public_id = '`+credential+`'
	                        AND deleted_at IS NULL`); got != 1 {
		t.Errorf("revoked credential row should remain, count = %d", got)
	}
}

func TestRetireLeavesPeopleOnOtherTerminalsAlone(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID, credential := f.enrolOn(t, "P-OTHER", f.otherDeviceID, otherSerial, 3)
	f.enrolOn(t, "P-HERE", f.deviceID, retireSerial, 1)

	if got := f.retire(t).EnrolmentsReset; got != 1 {
		t.Errorf("EnrolmentsReset = %d, want 1", got)
	}

	if !f.enrolled(t, "P-OTHER") || !f.detailEnrolled(t, "P-OTHER") {
		t.Error("a person enrolled only on another terminal must stay Enrolled")
	}
	if got := credentialStatus(t, credential); got != models.CredentialActive {
		t.Errorf("credential = %s, want ACTIVE", got)
	}
	if got := queryString(t, `SELECT state FROM credential_placements
	      WHERE device_id = $1`, f.otherDeviceID); got != models.PlacementPlaced {
		t.Errorf("placement on the other terminal = %s, want PLACED", got)
	}
	if got := queryString(t, `SELECT fingerprint_template FROM people WHERE id = $1`,
		personID); got != "terminal:"+otherSerial+":slot:3" {
		t.Errorf("locator = %q", got)
	}
}

// One credential placed on two terminals. The legacy column names only the
// retired one, and the person page reads only that column, so it must move.
func TestRetireKeepsAPersonEnrolledOnAnotherTerminal(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID, credential := f.enrolOn(t, "P-BOTH", f.deviceID, retireSerial, 1)
	seedPlacement(t, credential, f.otherDeviceID, models.PlacementPlaced, 9)

	if got := f.retire(t).EnrolmentsReset; got != 0 {
		t.Errorf("EnrolmentsReset = %d, want 0", got)
	}

	if !f.enrolled(t, "P-BOTH") || !f.detailEnrolled(t, "P-BOTH") {
		t.Error("still enrolled on another terminal, so must read Enrolled everywhere")
	}
	if got := credentialStatus(t, credential); got != models.CredentialActive {
		t.Errorf("credential = %s, want ACTIVE", got)
	}
	if got := queryString(t, `SELECT fingerprint_template FROM people WHERE id = $1`,
		personID); got != "terminal:"+otherSerial+":slot:9" {
		t.Errorf("locator should move to the surviving terminal, got %q", got)
	}
}

// Disabling is reversible and the template is still on the sensor.
func TestRetireCountsADisabledTerminalAsStillHoldingTheEnrolment(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	_, credential := f.enrolOn(t, "P-DIS", f.deviceID, retireSerial, 1)
	seedPlacement(t, credential, f.otherDeviceID, models.PlacementPlaced, 2)
	mustExec(t, `UPDATE devices SET status = 'DISABLED', active = FALSE WHERE id = $1`, f.otherDeviceID)

	f.retire(t)

	if !f.enrolled(t, "P-DIS") {
		t.Error("a disabled terminal can be re-enabled, so the enrolment must survive")
	}
}

// A copy on its way counts only if the platform holds what it needs to deliver it.
func TestRetireKeepsADeliverableCopyAndClosesAnUndeliverableOne(t *testing.T) {
	f := newRetireEnrolmentFixture(t)

	_, sealed := f.enrolOn(t, "P-SEALED", f.deviceID, retireSerial, 1)
	seedPlacement(t, sealed, f.otherDeviceID, models.PlacementPending, 0)
	mustExec(t, `UPDATE credentials
	                SET sealed_material = '\xdeadbeef'::bytea, sealed_key_id = 'ck_test',
	                    sealed_algorithm = 'AES-256-GCM'
	              WHERE public_id = $1::uuid`, sealed)

	_, bare := f.enrolOn(t, "P-BARE", f.deviceID, retireSerial, 2)
	seedPlacement(t, bare, f.otherDeviceID, models.PlacementPending, 0)

	f.retire(t)

	if got := credentialStatus(t, sealed); got != models.CredentialActive {
		t.Errorf("deliverable copy: credential = %s, want ACTIVE", got)
	}
	if got := credentialStatus(t, bare); got != models.CredentialRevoked {
		t.Errorf("undeliverable copy: credential = %s, want REVOKED", got)
	}
	if got := queryString(t, `SELECT pl.state FROM credential_placements pl
	      JOIN credentials c ON c.id = pl.credential_id
	     WHERE c.public_id = $1::uuid AND pl.device_id = $2`, bare, f.otherDeviceID); got != "REMOVED" {
		t.Errorf("undeliverable placement = %s, want REMOVED", got)
	}
}

// The enrolment result and placement writes are not atomic, so a real enrolment on
// a surviving terminal can have no placement row. A stray copy retiring must not
// end it.
func TestRetireTrustsAnEnrollingTerminalThatStillLives(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-SRC", "Source")
	credential := seedFingerprintCredential(t, f.companyID, personID, f.otherDeviceID, models.CredentialActive)
	seedPlacement(t, credential, f.deviceID, models.PlacementPlaced, 1)

	f.retire(t)

	if got := credentialStatus(t, credential); got != models.CredentialActive {
		t.Errorf("credential = %s, want ACTIVE", got)
	}
}

// Column only, with no credential row: what site-key tooling writes.
func TestRetireClearsALegacyOnlyEnrolment(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-LEG", "Legacy")
	mustExec(t, `UPDATE people SET fingerprint_template = $1 WHERE id = $2`,
		"terminal:"+retireSerial+":slot:4", personID)

	if got := f.retire(t).EnrolmentsReset; got != 1 {
		t.Errorf("EnrolmentsReset = %d, want 1", got)
	}
	if f.enrolled(t, "P-LEG") {
		t.Error("legacy-only person still reads Enrolled")
	}
}

// Backfilled credentials name their terminal only in the identifier.
func TestRetireMatchesABackfilledCredentialByItsIdentifier(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-BACK", "Backfilled")
	mustExec(t, `INSERT INTO credentials (company_id, person_id, credential_type, identifier, status)
	             VALUES ($1, $2, 'FINGERPRINT', $3, 'ACTIVE')`,
		f.companyID, personID, "legacy:terminal:"+retireSerial+":slot:5")

	f.retire(t)

	if f.enrolled(t, "P-BACK") {
		t.Error("backfilled credential should have ended with its terminal")
	}
}

// Must not be read as a prefix match on the serial: AT-FRONT is not AT-FRONT2.
func TestRetireDoesNotMatchASerialThatMerelySharesAPrefix(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-PREFIX", "Prefix")
	mustExec(t, `UPDATE people SET fingerprint_template = $1 WHERE id = $2`,
		"terminal:"+retireSerial+"2:slot:1", personID)

	f.retire(t)

	if !f.enrolled(t, "P-PREFIX") {
		t.Error("a different terminal's locator must not be cleared")
	}
}

// A bare PENDING row cannot legally become REVOKED. It must not block retirement.
func TestRetireHandlesABarePendingCredential(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-PEND", "Pending")
	mustExec(t, `INSERT INTO credentials (company_id, person_id, credential_type, status, enrolled_device_id)
	             VALUES ($1, $2, 'FINGERPRINT', 'PENDING', $3)`, f.companyID, personID, f.deviceID)

	if got := f.retire(t).EnrolmentsReset; got != 1 {
		t.Errorf("EnrolmentsReset = %d, want 1", got)
	}
	if f.enrolled(t, "P-PEND") {
		t.Error("person with only a bare PENDING credential still reads Enrolled")
	}
}

// A card is not a fingerprint enrolment, even if it was issued at this terminal.
func TestRetireLeavesOtherCredentialTypesAlone(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID := seedPerson(t, f.companyID, "P-CARD", "Card")
	mustExec(t, `INSERT INTO credentials (company_id, person_id, credential_type, identifier, status, enrolled_device_id)
	             VALUES ($1, $2, 'CARD', 'CARD-1', 'ACTIVE', $3)`, f.companyID, personID, f.deviceID)

	f.retire(t)

	if got := queryString(t, `SELECT status FROM credentials WHERE identifier = 'CARD-1'`); got != "ACTIVE" {
		t.Errorf("card = %s, want ACTIVE", got)
	}
}

func TestRetireWithNobodyEnrolledSucceeds(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	seedPerson(t, f.companyID, "P-NONE", "Nobody")

	if got := f.retire(t).EnrolmentsReset; got != 0 {
		t.Errorf("EnrolmentsReset = %d, want 0", got)
	}
	if f.enrolled(t, "P-NONE") {
		t.Error("nobody was enrolled")
	}
}

func TestRetireOnlyTouchesItsOwnCompany(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	otherCompany := operatorCompanyID(t, "two")
	personID := seedPerson(t, otherCompany, "P-TENANT", "Other tenant")
	// Same text as this company's terminal: serials are not unique across history.
	mustExec(t, `UPDATE people SET fingerprint_template = $1 WHERE id = $2`,
		"terminal:"+retireSerial+":slot:1", personID)

	f.retire(t)

	if got := queryString(t, `SELECT fingerprint_template FROM people WHERE id = $1`,
		personID); got != "terminal:"+retireSerial+":slot:1" {
		t.Errorf("another company's person was changed: %q", got)
	}
}

func TestRepeatedRetireChangesNothing(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	f.enrolOn(t, "P-REP", f.deviceID, retireSerial, 1)
	f.retire(t)

	// A person re-enrolled elsewhere after the retirement must not be undone by a
	// stale repeat of it.
	_, later := f.enrolOn(t, "P-LATER", f.otherDeviceID, otherSerial, 2)
	revokedBefore := queryString(t, `SELECT revoked_at::text FROM credentials
	      WHERE revoked_at IS NOT NULL ORDER BY id LIMIT 1`)

	for i := 0; i < 2; i++ {
		_, err := database.RetireTerminal(f.companyID, retireSerial, "again", 0)
		if !errors.Is(err, models.ErrDeviceNotFound) {
			t.Fatalf("repeat %d: err = %v, want ErrDeviceNotFound", i, err)
		}
	}
	code, body := consoleCall(t, f.env.router, "DELETE",
		"/api/v1/console/terminals/"+retireSerial, "", f.token, f.csrf)
	if code != http.StatusNotFound {
		t.Errorf("repeat through the API = %d (%v), want 404", code, body)
	}

	if got := queryString(t, `SELECT revoked_at::text FROM credentials
	      WHERE revoked_at IS NOT NULL ORDER BY id LIMIT 1`); got != revokedBefore {
		t.Errorf("revoked_at moved on a repeat: %s -> %s", revokedBefore, got)
	}
	if got := credentialStatus(t, later); got != models.CredentialActive {
		t.Errorf("later enrolment = %s, want ACTIVE", got)
	}
}

func TestConcurrentRetireSucceedsOnce(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	f.enrolOn(t, "P-RACE", f.deviceID, retireSerial, 1)

	const callers = 4
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := database.RetireTerminal(f.companyID, retireSerial, "race", 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if !errors.Is(err, models.ErrDeviceNotFound) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if ok != 1 {
		t.Errorf("%d retirements succeeded, want exactly 1", ok)
	}
	if f.enrolled(t, "P-RACE") {
		t.Error("person still reads Enrolled")
	}
}

// failUpdatesOn makes one table refuse updates for the rest of the test, so the
// failure is real and lands inside the retiring transaction.
func failUpdatesOn(t *testing.T, table string) {
	t.Helper()
	mustExec(t, `CREATE OR REPLACE FUNCTION test_refuse_update() RETURNS trigger AS $$
	             BEGIN RAISE EXCEPTION 'refused by test'; END; $$ LANGUAGE plpgsql`)
	mustExec(t, `CREATE TRIGGER test_refuse_update BEFORE UPDATE ON `+table+`
	             FOR EACH ROW EXECUTE FUNCTION test_refuse_update()`)
	t.Cleanup(func() {
		mustExec(t, `DROP TRIGGER IF EXISTS test_refuse_update ON `+table)
	})
}

// Failing at each enrolment step must leave the terminal and everybody as they were.
func TestFailedRetireRollsEverythingBack(t *testing.T) {
	for _, table := range []string{"credentials", "people"} {
		t.Run(table, func(t *testing.T) {
			f := newRetireEnrolmentFixture(t)
			personID, credential := f.enrolOn(t, "P-ROLL", f.deviceID, retireSerial, 1)
			failUpdatesOn(t, table)

			if _, err := database.RetireTerminal(f.companyID, retireSerial, "gone", 0); err == nil {
				t.Fatal("retirement should have failed")
			}

			if got := queryInt(t, `SELECT count(*) FROM devices
			      WHERE id = `+itoa(f.deviceID)+` AND deleted_at IS NULL AND retired_at IS NULL
			        AND api_key_hash IS NOT NULL`); got != 1 {
				t.Error("the terminal must remain live with its key")
			}
			if got := credentialStatus(t, credential); got != models.CredentialActive {
				t.Errorf("credential = %s, want ACTIVE", got)
			}
			if got := queryString(t, `SELECT state FROM credential_placements
			      WHERE device_id = $1`, f.deviceID); got != models.PlacementPlaced {
				t.Errorf("placement = %s, want PLACED", got)
			}
			if got := queryString(t, `SELECT fingerprint_template FROM people WHERE id = $1`,
				personID); got != "terminal:"+retireSerial+":slot:1" {
				t.Errorf("locator = %q", got)
			}
			if !f.enrolled(t, "P-ROLL") {
				t.Error("person must still read Enrolled")
			}

			// The API reports the failure rather than a retirement that did not happen.
			code, body := consoleCall(t, f.env.router, "DELETE",
				"/api/v1/console/terminals/"+retireSerial, "", f.token, f.csrf)
			if code != http.StatusInternalServerError {
				t.Errorf("DELETE = %d (%v), want 500", code, body)
			}
		})
	}
}

func TestRetireReportsTheCountThroughTheAPIAndAudit(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	f.enrolOn(t, "P-API", f.deviceID, retireSerial, 1)

	code, body := consoleCall(t, f.env.router, "DELETE",
		"/api/v1/console/terminals/"+retireSerial, "", f.token, f.csrf)
	if code != http.StatusOK {
		t.Fatalf("DELETE = %d (%v)", code, body)
	}
	if body["enrolments_reset"] != float64(1) {
		t.Errorf("enrolments_reset = %v, want 1", body["enrolments_reset"])
	}
	if got := queryInt(t, `SELECT count(*) FROM audit_events
	      WHERE action = 'TERMINAL_RETIRED' AND changes->>'enrolments_reset' = '1'`); got != 1 {
		t.Errorf("audit rows carrying the count = %d, want 1", got)
	}
}

// Retiring a site retires its terminals, so it has to do the same.
func TestRetireSiteResetsEnrolmentsOfItsTerminals(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	f.enrolOn(t, "P-SITE", f.deviceID, retireSerial, 1)

	var siteID int64
	mustScan(t, `SELECT site_id FROM devices WHERE id = `+itoa(f.deviceID), &siteID)

	// A terminal at another site of this company holds a second person's copy.
	var otherSite int64
	mustScan(t, `SELECT id FROM sites WHERE company_id = `+itoa(f.companyID)+`
	             AND id <> `+itoa(siteID)+` ORDER BY id LIMIT 1`, &otherSite)
	mustExec(t, `UPDATE devices SET site_id = $1 WHERE id = $2`, otherSite, f.otherDeviceID)
	_, survives := f.enrolOn(t, "P-AWAY", f.otherDeviceID, otherSerial, 2)

	result, err := database.RetireSite(f.companyID, siteID)
	if err != nil {
		t.Fatalf("RetireSite: %v", err)
	}
	if result.EnrolmentsReset != 1 {
		t.Errorf("EnrolmentsReset = %d, want 1", result.EnrolmentsReset)
	}
	if f.enrolled(t, "P-SITE") {
		t.Error("person enrolled only at the retired site still reads Enrolled")
	}
	if got := credentialStatus(t, survives); got != models.CredentialActive {
		t.Errorf("credential at the surviving site = %s, want ACTIVE", got)
	}
}

// Re-enrolling after a retirement has to work, or the reset would strand people.
func TestAPersonCanEnrolAgainAfterRetirement(t *testing.T) {
	f := newRetireEnrolmentFixture(t)
	personID, _ := f.enrolOn(t, "P-AGAIN", f.deviceID, retireSerial, 1)
	f.retire(t)

	credential := seedFingerprintCredential(t, f.companyID, personID, f.otherDeviceID, models.CredentialActive)
	seedPlacement(t, credential, f.otherDeviceID, models.PlacementPlaced, 7)

	if !f.enrolled(t, "P-AGAIN") {
		t.Error("a fresh enrolment should read Enrolled")
	}
}
