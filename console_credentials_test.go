package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// D1: credential and enrolment visibility from an operator session.
//
// ---------------------------------------------------------------------------
// THE QUESTION THIS ENDPOINT EXISTS TO ANSWER
// ---------------------------------------------------------------------------
//
// An operator's member works at the front desk and is refused at the east gate.
// Until this endpoint, nothing anywhere could tell them why: the console had
// `biometric_enrolled`, a boolean, and a boolean cannot say that an enrolment
// binds to the sensor that captured it. `usable_at_terminal_count` is the field
// that makes that legible, and most of what follows is about it being HONEST --
// counting doors that actually hold the credential and not doors that are merely
// supposed to.
//
// ---------------------------------------------------------------------------
// THE SECURITY ASSERTION IS NEGATIVE AND IS THE MOST IMPORTANT TEST HERE
// ---------------------------------------------------------------------------
//
// No template, no sealed material, no digest, no key id, no algorithm, no sensor
// slot, no locator, no vendor, no template format, no sensor profile. The
// endpoint reads the same tables the parked replication work writes, so the
// scan below is what stops a future edit widening this into a way to read
// biometric material out of the platform with an operator session.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type credentialViewFixture struct {
	env       *testEnv
	companyID int64
	token     string
	csrf      string
	personID  int64
	deviceID  int64
	serial    string
}

func newCredentialViewFixture(t *testing.T) *credentialViewFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"creds-view@example.com", models.RoleOwner)

	env.registerDevice(env.siteAKey, "AT-FRONT")

	personID := seedPerson(t, companyID, "P-CRED", "Credential Subject")

	return &credentialViewFixture{
		env: env, companyID: companyID, token: token, csrf: csrf,
		personID: personID,
		deviceID: deviceIDBySerial(t, "AT-FRONT"),
		serial:   "AT-FRONT",
	}
}

// seedFingerprintCredential writes one credential row and returns its public id.
//
// Named apart from platform_primitives_test.go's seedCredential, which seeds a
// CARD by identifier for the authorization tests.
//
// `template_format = SENSOR_LOCAL` satisfies migration 020's substance check for
// a non-PENDING credential without inventing any material -- which is exactly
// what the fitted hardware produces and what this endpoint must be able to
// describe without disclosing.
func seedFingerprintCredential(t *testing.T, companyID, personID, deviceID int64, status string) string {
	t.Helper()
	var publicID string
	mustScan(t, `
		INSERT INTO credentials (company_id, person_id, credential_type, template_format,
		                         status, enrolled_device_id, enrolled_at,
		                         revoked_at)
		VALUES (`+itoa(companyID)+`, `+itoa(personID)+`, 'FINGERPRINT', 'SENSOR_LOCAL',
		        '`+status+`', `+itoa(deviceID)+`, CURRENT_TIMESTAMP,
		        CASE WHEN '`+status+`' = 'REVOKED' THEN CURRENT_TIMESTAMP ELSE NULL END)
		RETURNING public_id`, &publicID)
	return publicID
}

// seedPlacement records that a terminal holds (or owes) a credential.
func seedPlacement(t *testing.T, credentialPublicID string, deviceID int64, state string, slot int) {
	t.Helper()
	var slotValue any
	var placedAt any
	if state == models.PlacementPlaced {
		slotValue = slot
		placedAt = "now"
	}
	mustExec(t, `
		INSERT INTO credential_placements
		    (credential_id, device_id, slot, state, placed_at)
		VALUES ((SELECT id FROM credentials WHERE public_id = $1::uuid), $2, $3, $4,
		        CASE WHEN $5::text IS NULL THEN NULL ELSE CURRENT_TIMESTAMP END)`,
		credentialPublicID, deviceID, slotValue, state, placedAt)
}

func (f *credentialViewFixture) get(t *testing.T, externalID string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/"+externalID+"/credentials", "", f.token, "")
}

// ---------------------------------------------------------------------------
// The shape
// ---------------------------------------------------------------------------

func TestPersonCredentialsReportWhereAnEnrolmentLives(t *testing.T) {
	f := newCredentialViewFixture(t)
	credentialID := seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialActive)
	seedPlacement(t, credentialID, f.deviceID, models.PlacementPlaced, 4)

	code, body := f.get(t, "P-CRED")
	if code != http.StatusOK {
		t.Fatalf("GET credentials = %d (%v)", code, body)
	}
	if body["enrolment_source"] != models.EnrolmentSourceCredential {
		t.Errorf("enrolment_source = %v, want CREDENTIAL", body["enrolment_source"])
	}
	if body["count"] != float64(1) {
		t.Fatalf("count = %v, want 1", body["count"])
	}

	item := listOf(t, body, "credentials")[0].(map[string]any)
	if item["id"] != credentialID {
		t.Errorf("id = %v, want %v", item["id"], credentialID)
	}
	if item["type"] != models.CredentialFingerprint {
		t.Errorf("type = %v, want FINGERPRINT", item["type"])
	}
	if item["state"] != models.CredentialActive {
		t.Errorf("state = %v, want ACTIVE", item["state"])
	}
	if item["enrolled_at"] == nil {
		t.Error("enrolled_at is absent")
	}
	if item["usable_at_terminal_count"] != float64(1) {
		t.Errorf("usable_at_terminal_count = %v, want 1", item["usable_at_terminal_count"])
	}

	terminal, ok := item["enrolled_at_terminal"].(map[string]any)
	if !ok {
		t.Fatalf("enrolled_at_terminal missing: %v", item)
	}
	if terminal["serial_number"] != f.serial {
		t.Errorf("serial = %v, want %v", terminal["serial_number"], f.serial)
	}
	if terminal["retired"] != false {
		t.Errorf("a live terminal is reported retired")
	}
}

// TestUsableCountMeansTerminalsThatActuallyHoldIt.
//
// The number an operator reads to learn that "enrolled" means "recognised at one
// door". Counting a PENDING or FAILED placement would tell them a person works
// somewhere they will be refused -- which is worse than showing no number,
// because it is a confident wrong answer to the exact question being asked.
func TestUsableCountMeansTerminalsThatActuallyHoldIt(t *testing.T) {
	f := newCredentialViewFixture(t)
	credentialID := seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialActive)

	f.env.registerDevice(f.env.siteAKey, "AT-EAST")
	f.env.registerDevice(f.env.siteAKey, "AT-WEST")
	f.env.registerDevice(f.env.siteAKey, "AT-DOCK")
	east := deviceIDBySerial(t, "AT-EAST")
	west := deviceIDBySerial(t, "AT-WEST")
	dock := deviceIDBySerial(t, "AT-DOCK")

	// One door holds it. Three do not, in three different ways.
	seedPlacement(t, credentialID, f.deviceID, models.PlacementPlaced, 4)
	seedPlacement(t, credentialID, east, models.PlacementPending, 0)
	seedPlacement(t, credentialID, west, models.PlacementFailed, 0)
	seedPlacement(t, credentialID, dock, models.PlacementRemoved, 0)

	_, body := f.get(t, "P-CRED")
	item := listOf(t, body, "credentials")[0].(map[string]any)
	if item["usable_at_terminal_count"] != float64(1) {
		t.Errorf("usable_at_terminal_count = %v, want 1 -- only PLACED counts",
			item["usable_at_terminal_count"])
	}
}

// TestUsableCountIgnoresRetiredTerminals.
//
// A retired terminal holds nothing, whatever its placement row still says, and
// counting it would tell an operator a door works that is in a cupboard.
func TestUsableCountIgnoresRetiredTerminals(t *testing.T) {
	f := newCredentialViewFixture(t)
	credentialID := seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialActive)
	seedPlacement(t, credentialID, f.deviceID, models.PlacementPlaced, 4)

	mustExec(t, `UPDATE devices SET deleted_at = CURRENT_TIMESTAMP WHERE id = $1`, f.deviceID)

	_, body := f.get(t, "P-CRED")
	item := listOf(t, body, "credentials")[0].(map[string]any)
	if item["usable_at_terminal_count"] != float64(0) {
		t.Errorf("usable_at_terminal_count = %v, want 0 for a retired terminal",
			item["usable_at_terminal_count"])
	}

	// But the enrolling terminal is still NAMED, and marked retired. Losing
	// where an enrolment came from is worse than naming a retired unit -- it is
	// usually the explanation for why the person stopped being recognised.
	terminal, ok := item["enrolled_at_terminal"].(map[string]any)
	if !ok {
		t.Fatalf("a retired enrolling terminal was dropped entirely: %v", item)
	}
	if terminal["retired"] != true {
		t.Errorf("retired = %v, want true", terminal["retired"])
	}
	if terminal["serial_number"] != f.serial {
		t.Errorf("serial = %v, want it still named", terminal["serial_number"])
	}
}

// ---------------------------------------------------------------------------
// The two stores, and why enrolment_source exists
// ---------------------------------------------------------------------------

// TestLegacyOnlyEnrolmentIsDistinguishableRatherThanContradictory.
//
// THE CASE THE WHOLE FIELD EXISTS FOR. Somebody enrolled through the site-key
// path has people.fingerprint_template and no credential row. Reporting the
// credential list alone would call them unenrolled, directly contradicting the
// `biometric_enrolled` badge beside it on the same screen.
func TestLegacyOnlyEnrolmentIsDistinguishableRatherThanContradictory(t *testing.T) {
	f := newCredentialViewFixture(t)
	mustExec(t, `UPDATE people SET fingerprint_template = 'terminal:AT-OLD:slot:7' WHERE id = $1`,
		f.personID)

	code, body := f.get(t, "P-CRED")
	if code != http.StatusOK {
		t.Fatalf("GET credentials = %d", code)
	}
	if body["enrolment_source"] != models.EnrolmentSourceLegacy {
		t.Errorf("enrolment_source = %v, want LEGACY_ONLY", body["enrolment_source"])
	}
	if body["count"] != float64(0) {
		t.Errorf("count = %v -- there is no structured record to list", body["count"])
	}

	// And the person read agrees rather than contradicting it.
	_, person := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/P-CRED", "", f.token, "")
	if person["biometric_enrolled"] != true {
		t.Error("a legacy-enrolled person is reported as not enrolled")
	}
	if person["enrolment_source"] != models.EnrolmentSourceLegacy {
		t.Errorf("person enrolment_source = %v, want LEGACY_ONLY", person["enrolment_source"])
	}

	// The legacy locator itself never travels.
	if strings.Contains(mustJSON(t, body), "AT-OLD") ||
		strings.Contains(mustJSON(t, person), "slot:7") {
		t.Error("the legacy locator leaked")
	}
}

// TestPlacementOnlyEnrolmentIsReportedAsEnrolled.
//
// The other direction: the placement endpoint writes a credential and never
// touches the legacy column, so a person enrolled by current firmware has a row
// and an empty column. Reading only the column would call them unenrolled.
func TestPlacementOnlyEnrolmentIsReportedAsEnrolled(t *testing.T) {
	f := newCredentialViewFixture(t)
	seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialActive)

	_, person := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/P-CRED", "", f.token, "")
	if person["biometric_enrolled"] != true {
		t.Error("a person with a credential row is reported as not enrolled")
	}
	if person["enrolment_source"] != models.EnrolmentSourceCredential {
		t.Errorf("enrolment_source = %v, want CREDENTIAL", person["enrolment_source"])
	}
}

// TestRevokedCredentialIsShownButDoesNotCountAsEnrolled.
//
// "She had a credential and it was revoked on Tuesday" is the answer to why
// somebody stopped working at every door at once. Dropping the row would leave
// an operator staring at an empty panel for a person who was enrolled an hour
// ago -- but counting it as enrolment would keep a dismissed employee reading as
// enrolled for ever.
func TestRevokedCredentialIsShownButDoesNotCountAsEnrolled(t *testing.T) {
	f := newCredentialViewFixture(t)
	seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialRevoked)

	code, body := f.get(t, "P-CRED")
	if code != http.StatusOK {
		t.Fatalf("GET credentials = %d", code)
	}
	if body["count"] != float64(1) {
		t.Errorf("count = %v -- a revoked credential is still shown", body["count"])
	}
	if body["enrolment_source"] != models.EnrolmentSourceNone {
		t.Errorf("enrolment_source = %v, want NONE for a revoked credential",
			body["enrolment_source"])
	}

	_, person := consoleCall(t, f.env.router, "GET",
		"/api/v1/console/people/P-CRED", "", f.token, "")
	if person["biometric_enrolled"] != false {
		t.Error("a revoked credential still reads as enrolled")
	}
}

// ---------------------------------------------------------------------------
// The security assertion
// ---------------------------------------------------------------------------

// TestPersonCredentialsCarryNoBiometricMaterial.
//
// Checked against the RAW BODY rather than the decoded object, so a field added
// at any nesting level is caught. The credential is seeded carrying every kind
// of material and locator the schema can hold, including the sealing columns the
// parked replication work uses -- this endpoint reads those same tables, and this
// test is what keeps it from becoming a way to read biometric material out of
// the platform with an operator session.
func TestPersonCredentialsCarryNoBiometricMaterial(t *testing.T) {
	f := newCredentialViewFixture(t)
	credentialID := seedFingerprintCredential(t, f.companyID, f.personID, f.deviceID, models.CredentialActive)
	seedPlacement(t, credentialID, f.deviceID, models.PlacementPlaced, 9)

	mustExec(t, `
		UPDATE credentials
		   SET sealed_material  = '\xdeadbeef'::bytea,
		       sealed_key_id    = 'ck_secretkey01',
		       sealed_algorithm = 'AES-256-GCM',
		       material_digest  = 'aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111',
		       sensor_profile   = 'ZFM:0x0009:1000',
		       vendor           = 'SECRETVENDOR',
		       identifier       = NULL
		 WHERE public_id = $1::uuid`, credentialID)

	_, body := f.get(t, "P-CRED")
	raw := mustJSON(t, body)

	for _, forbidden := range []string{
		"sealed_material", "sealed_key_id", "sealed_algorithm", "material_digest",
		"sensor_profile", "template_format", "vendor", "identifier", "slot",
		"deadbeef", "ck_secretkey01", "AES-256-GCM", "ZFM:0x0009:1000",
		"SECRETVENDOR", "SENSOR_LOCAL",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the credentials endpoint leaked %q: %s", forbidden, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// Absence, and tenancy
// ---------------------------------------------------------------------------

// TestPersonWithNoCredentialIsAnEmptyListNotAMissingPerson.
func TestPersonWithNoCredentialIsAnEmptyListNotAMissingPerson(t *testing.T) {
	f := newCredentialViewFixture(t)

	code, body := f.get(t, "P-CRED")
	if code != http.StatusOK {
		t.Fatalf("GET credentials = %d, want 200 for a person who exists", code)
	}
	if body["count"] != float64(0) {
		t.Errorf("count = %v, want 0", body["count"])
	}
	if body["enrolment_source"] != models.EnrolmentSourceNone {
		t.Errorf("enrolment_source = %v, want NONE", body["enrolment_source"])
	}
	// `[]`, not null, so a client can iterate unconditionally.
	if listOf(t, body, "credentials") == nil {
		t.Error("credentials was null rather than an empty array")
	}
}

func TestUnknownPersonCredentialsIs404(t *testing.T) {
	f := newCredentialViewFixture(t)
	if code, _ := f.get(t, "NOBODY"); code != http.StatusNotFound {
		t.Errorf("GET credentials for an unknown person = %d, want 404", code)
	}
}

// TestPersonCredentialsStayTenantScoped.
//
// Another company's person is NOT FOUND rather than forbidden: telling a caller
// an id exists somewhere they cannot reach is itself a cross-tenant disclosure.
func TestPersonCredentialsStayTenantScoped(t *testing.T) {
	f := newCredentialViewFixture(t)
	other := operatorCompanyID(t, "two")
	otherPerson := seedPerson(t, other, "P-OTHER", "Other Tenant")
	f.env.registerDevice(f.env.siteCKey, "AT-OTHER")
	seedFingerprintCredential(t, other, otherPerson, deviceIDBySerial(t, "AT-OTHER"), models.CredentialActive)

	if code, _ := f.get(t, "P-OTHER"); code != http.StatusNotFound {
		t.Errorf("reading another tenant's credentials = %d, want 404", code)
	}
}
