package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// §4 The device-facing credential API.
//
// What this exists to make possible: "person A is recognised at terminals A, B
// and C". That could not previously be EXPRESSED, and the reason was a schema
// one rather than a sensor one -- `people.fingerprint_template` is a single
// column, so enrolling somebody at a second door overwrote the record of the
// first.
//
// The security property that matters most here is negative and is asserted
// repeatedly below: NO BIOMETRIC MATERIAL CROSSES THIS BOUNDARY.

// credentialFixture is a terminal, a person permitted at it, and a credential.
type credentialFixture struct {
	env       *testEnv
	companyID int64
	deviceKey string
	serial    string
	personID  int64
}

func newCredentialFixture(t *testing.T) *credentialFixture {
	t.Helper()
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	key := env.registerDevice(env.siteAKey, "ESP32-CRED")

	personID := seedPerson(t, companyID, "P-CRED", "Credential Subject")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)

	return &credentialFixture{
		env: env, companyID: companyID, deviceKey: key,
		serial: "ESP32-CRED", personID: personID,
	}
}

// seedPendingCredential creates a credential awaiting capture.
func seedPendingCredential(t *testing.T, companyID, personID int64) string {
	t.Helper()
	var publicID string
	mustScan(t, `
		INSERT INTO credentials (company_id, person_id, credential_type,
		                         template_format, vendor, status)
		VALUES (`+itoa(companyID)+`, `+itoa(personID)+`, 'FINGERPRINT',
		        'SENSOR_LOCAL', 'ZFM', 'PENDING')
		RETURNING public_id`, &publicID)
	return publicID
}

func TestPendingCredentialsListsWhatTheTerminalLacks(t *testing.T) {
	f := newCredentialFixture(t)
	credentialID := seedPendingCredential(t, f.companyID, f.personID)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /devices/credentials/pending = %d: %s", res.Code, res.Raw)
	}

	items := res.Body["credentials"].([]any)
	if len(items) != 1 {
		t.Fatalf("got %d pending credentials, want 1: %s", len(items), res.Raw)
	}

	item := items[0].(map[string]any)

	// The exact field names the firmware's credential model uses
	// (credential_ref.h) -- and `member_id`, matching what every other device
	// payload calls a person.
	for field, want := range map[string]any{
		"credential_id":   credentialID,
		"member_id":       "P-CRED",
		"credential_type": models.CredentialFingerprint,
		"template_format": "SENSOR_LOCAL",
		"vendor":          "ZFM",
		"state":           models.PlacementPending,
	} {
		if item[field] != want {
			t.Errorf("%s = %v, want %v", field, item[field], want)
		}
	}

	// The generation from 019, so a placement written before a sensor wipe can
	// be told from one written after.
	if item["generation"] != float64(1) {
		t.Errorf("generation = %v, want 1", item["generation"])
	}
}

// TestPendingCredentialsCarryNoBiometricMaterial is the security assertion.
//
// Checked against the RAW BODY rather than the decoded object, so a field added
// at any nesting level is caught.
func TestPendingCredentialsCarryNoBiometricMaterial(t *testing.T) {
	f := newCredentialFixture(t)

	// A credential carrying every kind of material the schema can hold.
	mustExec(t, `
		INSERT INTO credentials (company_id, person_id, credential_type, status,
		                         sealed_material, sealed_key_id, sealed_algorithm,
		                         material_digest, identifier)
		VALUES ($1, $2, 'FINGERPRINT', 'ACTIVE',
		        '\xdeadbeef'::bytea, 'key-1', 'AES-256-GCM',
		        'aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111',
		        'SECRET-IDENTIFIER')`, f.companyID, f.personID)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("pending = %d", res.Code)
	}

	for _, forbidden := range []string{
		"sealed_material", "sealed_key_id", "sealed_algorithm",
		"material_digest", "identifier",
		"deadbeef", "SECRET-IDENTIFIER", "AES-256-GCM", "key-1",
	} {
		if strings.Contains(res.Raw, forbidden) {
			t.Errorf("the pending list leaked %q: %s", forbidden, res.Raw)
		}
	}
}

// TestPendingCredentialsAreScopedToPermissions.
//
// The enrolment surface is exactly as narrow as the access surface: a terminal
// is not handed a work list naming people it would refuse at the door.
func TestPendingCredentialsAreScopedToPermissions(t *testing.T) {
	f := newCredentialFixture(t)
	seedPendingCredential(t, f.companyID, f.personID)

	// Somebody with a credential and NO permission at this terminal.
	other := seedPerson(t, f.companyID, "P-UNPERMITTED", "Not Allowed Here")
	seedPendingCredential(t, f.companyID, other)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	items := res.Body["credentials"].([]any)

	if len(items) != 1 {
		t.Fatalf("got %d entries, want only the permitted person: %s", len(items), res.Raw)
	}
	if strings.Contains(res.Raw, "P-UNPERMITTED") {
		t.Error("a terminal was told to enrol somebody it would refuse at the door")
	}
}

// TestPendingCredentialsDoNotCrossTenants.
func TestPendingCredentialsDoNotCrossTenants(t *testing.T) {
	f := newCredentialFixture(t)

	otherCompany := companyIDBySlug(t, "two")
	foreign := seedPerson(t, otherCompany, "P-FOREIGN-CRED", "Foreign Person")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, otherCompany, foreign)
	seedPendingCredential(t, otherCompany, foreign)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if strings.Contains(res.Raw, "P-FOREIGN-CRED") {
		t.Fatalf("a terminal saw another tenant's credential: %s", res.Raw)
	}
}

// TestPendingCredentialsExcludeWhatIsAlreadyPlaced, and what is withdrawn.
func TestPendingCredentialsExcludeSettledStates(t *testing.T) {
	f := newCredentialFixture(t)
	credentialID := seedPendingCredential(t, f.companyID, f.personID)
	deviceID := deviceIDOf(t, f.serial)

	mustExec(t, `
		INSERT INTO credential_placements (credential_id, device_id, slot, state, placed_at)
		SELECT id, $2, 5, 'PLACED', CURRENT_TIMESTAMP FROM credentials WHERE public_id = $1::uuid`,
		credentialID, deviceID)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if items := res.Body["credentials"].([]any); len(items) != 0 {
		t.Fatalf("a PLACED credential is still listed as work to do: %s", res.Raw)
	}

	// A REVOKED credential must never appear as work either.
	//
	// The identifier is set alongside the status because 012's substance check
	// refuses a non-PENDING credential that is neither sealed material nor a
	// non-secret identifier -- a credential that identifies nobody.
	mustExec(t, `UPDATE credential_placements SET state = 'FAILED'`)
	mustExec(t, `UPDATE credentials
	                SET status = 'REVOKED',
	                    revoked_at = CURRENT_TIMESTAMP,
	                    identifier = 'REVOKED-CARD-1'
	              WHERE public_id = $1::uuid`, credentialID)

	res = f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if items := res.Body["credentials"].([]any); len(items) != 0 {
		t.Fatalf("a REVOKED credential was offered for enrolment: %s", res.Raw)
	}
}

// TestPendingCredentialsRequireADeviceCredential. A site key must not reach it:
// the site key is the provisioning secret and this is a per-terminal work list.
func TestPendingCredentialsRequireADeviceCredential(t *testing.T) {
	f := newCredentialFixture(t)

	res := f.env.do("GET", "/api/v1/devices/credentials/pending", nil,
		map[string]string{"X-API-Key": f.env.siteAKey})
	if res.Code != http.StatusUnauthorized {
		t.Errorf("a site key reached the device credential list (got %d)", res.Code)
	}

	res = f.env.do("GET", "/api/v1/devices/credentials/pending", nil, nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated caller reached it (got %d)", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Reporting a placement
// ---------------------------------------------------------------------------

func TestPlacementReportBindsACredentialToThisTerminal(t *testing.T) {
	f := newCredentialFixture(t)
	credentialID := seedPendingCredential(t, f.companyID, f.personID)

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"credential_id":   credentialID,
		"member_id":       "P-CRED",
		"credential_type": models.CredentialFingerprint,
		"template_format": "SENSOR_LOCAL",
		"vendor":          "ZFM",
		"slot":            5,
		"state":           models.PlacementPlaced,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("placement report = %d: %s", res.Code, res.Raw)
	}

	var state string
	var slot int
	mustScan(t, `
		SELECT pl.state, pl.slot FROM credential_placements pl
		  JOIN credentials c ON c.id = pl.credential_id
		 WHERE c.public_id = '`+credentialID+`'`, &state, &slot)
	if state != models.PlacementPlaced || slot != 5 {
		t.Errorf("stored placement = %s slot %d, want PLACED slot 5", state, slot)
	}

	// A credential that has been placed somewhere is ACTIVE -- which is what
	// PENDING existed to be distinguished from.
	var status string
	mustScan(t, `SELECT status FROM credentials WHERE public_id = '`+credentialID+`'`, &status)
	if status != models.CredentialActive {
		t.Errorf("credential status = %s, want ACTIVE after placement", status)
	}

	// And it drops off the work list.
	pending := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	if items := pending.Body["credentials"].([]any); len(items) != 0 {
		t.Errorf("the credential is still pending after being placed: %s", pending.Raw)
	}
}

// TestPlacementReportIsIdempotent. A terminal retrying a report whose response
// it never heard must not create a second placement.
func TestPlacementReportIsIdempotent(t *testing.T) {
	f := newCredentialFixture(t)
	credentialID := seedPendingCredential(t, f.companyID, f.personID)

	body := map[string]any{
		"credential_id": credentialID,
		"member_id":     "P-CRED",
		"slot":          5,
		"state":         models.PlacementPlaced,
	}

	for i := 0; i < 3; i++ {
		if res := f.env.do("POST", "/api/v1/devices/credentials/placement", body,
			deviceAuth(f.deviceKey)); res.Code != http.StatusOK {
			t.Fatalf("report %d = %d: %s", i, res.Code, res.Raw)
		}
	}

	if n := queryInt(t, `SELECT count(*) FROM credential_placements`); n != 1 {
		t.Errorf("three identical reports produced %d placements, want 1", n)
	}
}

// TestFailedPlacementCarriesTheReason, so an operator sees "sensor full" rather
// than "failed".
func TestFailedPlacementCarriesTheReason(t *testing.T) {
	f := newCredentialFixture(t)
	credentialID := seedPendingCredential(t, f.companyID, f.personID)

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"credential_id": credentialID,
		"member_id":     "P-CRED",
		"state":         models.PlacementFailed,
		"error":         "sensor full",
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("failed placement report = %d: %s", res.Code, res.Raw)
	}

	var state, lastError string
	mustScan(t, `SELECT state, COALESCE(last_error, '') FROM credential_placements`,
		&state, &lastError)
	if state != models.PlacementFailed || lastError != "sensor full" {
		t.Errorf("stored = %s/%q, want FAILED/'sensor full'", state, lastError)
	}

	// A FAILED placement stays on the work list -- it is still work to do -- and
	// carries the reason so the terminal can decide not to retry it forever.
	pending := f.env.do("GET", "/api/v1/devices/credentials/pending", nil, deviceAuth(f.deviceKey))
	items := pending.Body["credentials"].([]any)
	if len(items) != 1 {
		t.Fatalf("a FAILED placement left the work list: %s", pending.Raw)
	}
	if items[0].(map[string]any)["last_error"] != "sensor full" {
		t.Errorf("the work list does not carry the failure reason: %s", pending.Raw)
	}
}

// TestPlacementReportRefusesAnotherTenantsPerson.
func TestPlacementReportRefusesAnotherTenantsPerson(t *testing.T) {
	f := newCredentialFixture(t)
	seedPerson(t, companyIDBySlug(t, "two"), "P-OTHER-TENANT", "Other Tenant")

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "P-OTHER-TENANT",
		"slot":      3,
		"state":     models.PlacementPlaced,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("a terminal placed a credential for another tenant's person (got %d): %s",
			res.Code, res.Raw)
	}
	if n := queryInt(t, `SELECT count(*) FROM credential_placements`); n != 0 {
		t.Errorf("a cross-tenant placement was written anyway (%d rows)", n)
	}
}

// TestPlacementReportRefusesAnotherPersonsCredential is the IDOR case.
func TestPlacementReportRefusesAnotherPersonsCredential(t *testing.T) {
	f := newCredentialFixture(t)

	other := seedPerson(t, f.companyID, "P-SOMEONE-ELSE", "Someone Else")
	othersCredential := seedPendingCredential(t, f.companyID, other)

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"credential_id": othersCredential,
		"member_id":     "P-CRED",
		"slot":          4,
		"state":         models.PlacementPlaced,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("a credential was bound to a person it does not belong to (got %d): %s",
			res.Code, res.Raw)
	}
}

// TestPlacementStateIsConstrained. PENDING and REMOVING are the PLATFORM's
// intentions; a terminal claiming either would be a device deciding what the
// platform wants.
func TestPlacementStateIsConstrained(t *testing.T) {
	f := newCredentialFixture(t)

	for _, state := range []string{"PENDING", "REMOVING", "NONSENSE", ""} {
		res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
			"member_id": "P-CRED", "slot": 1, "state": state,
		}, deviceAuth(f.deviceKey))
		if res.Code != http.StatusBadRequest {
			t.Errorf("state %q was accepted (got %d)", state, res.Code)
		}
	}

	// PLACED without a slot names nothing.
	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "P-CRED", "state": models.PlacementPlaced,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusBadRequest {
		t.Errorf("a PLACED report with no slot was accepted (got %d)", res.Code)
	}
}

// TestEnrolmentResultWritesAPlacement covers the §4 note that binding the
// `credential` object the firmware ALREADY SENDS turns an enrolment into a real
// placement with no firmware change at all.
func TestEnrolmentResultWritesAPlacement(t *testing.T) {
	f := newCredentialFixture(t)

	res := f.env.do("POST", "/api/v1/devices/enrollment/result", map[string]any{
		"member_id":            "P-CRED",
		"fingerprint_template": "terminal:ESP32-CRED:slot:7",
		"credential": map[string]any{
			"credential_type": "FINGERPRINT",
			"template_format": "SENSOR_LOCAL",
			"vendor":          "ZFM",
			"terminal":        "ESP32-CRED",
			"slot":            7,
		},
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("enrolment result = %d: %s", res.Code, res.Raw)
	}

	var state string
	var slot int
	mustScan(t, `SELECT state, slot FROM credential_placements`, &state, &slot)
	if state != models.PlacementPlaced || slot != 7 {
		t.Errorf("placement = %s slot %d, want PLACED slot 7", state, slot)
	}

	// The LEGACY column is still written. Deployed firmware and the 012
	// back-fill both depend on it, and the compatibility policy freezes it.
	var locator string
	mustScan(t, `SELECT COALESCE(fingerprint_template, '') FROM people
	              WHERE external_id = 'P-CRED'`, &locator)
	if locator != "terminal:ESP32-CRED:slot:7" {
		t.Errorf("the legacy locator was not written: %q", locator)
	}
}

// TestEnrolmentResultStillWorksWithoutTheCredentialObject. Older firmware omits
// it, and the enrolment must complete exactly as it did.
func TestEnrolmentResultStillWorksWithoutTheCredentialObject(t *testing.T) {
	f := newCredentialFixture(t)

	res := f.env.do("POST", "/api/v1/devices/enrollment/result", map[string]any{
		"member_id":            "P-CRED",
		"fingerprint_template": "terminal:ESP32-CRED:slot:2",
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("enrolment without a credential object = %d: %s", res.Code, res.Raw)
	}

	var locator string
	mustScan(t, `SELECT COALESCE(fingerprint_template, '') FROM people
	              WHERE external_id = 'P-CRED'`, &locator)
	if locator != "terminal:ESP32-CRED:slot:2" {
		t.Errorf("the legacy path changed behaviour: %q", locator)
	}
}
