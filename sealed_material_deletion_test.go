package main

import (
	"database/sql"
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Deleting a person destroys the sealed biometric material held for them (026).
//
// The person is soft-deleted -- history, audit and the terminals' REMOVED
// reports still need the row -- but their template has no purpose left, so the
// ciphertext, its key label and the digest of the plaintext go in the delete's
// own transaction. The credential row itself stays exactly as resolvable as it
// was, or the terminal's REMOVED report could never converge its placement.

// giveSealedMaterial turns a seeded SENSOR_LOCAL credential into one carrying
// sealed material, as an upload would leave it.
func giveSealedMaterial(t *testing.T, credentialPublicID, digest string) {
	t.Helper()
	mustExec(t, `
		UPDATE credentials
		   SET sealed_material  = '\x0102030405'::bytea,
		       sealed_key_id    = 'key-1',
		       sealed_algorithm = 'AES-256-GCM',
		       material_digest  = $2,
		       template_format  = 'VENDOR_TEMPLATE'
		 WHERE public_id = $1::uuid`, credentialPublicID, digest)
}

type sealedState struct {
	material, keyID, algorithm, digest bool
	format, status                     string
	deleted                            bool
}

func sealedStateOf(t *testing.T, credentialPublicID string) sealedState {
	t.Helper()
	var s sealedState
	var deletedAt sql.NullTime
	err := database.DB.QueryRow(`
		SELECT sealed_material IS NOT NULL, sealed_key_id IS NOT NULL,
		       sealed_algorithm IS NOT NULL, material_digest IS NOT NULL,
		       COALESCE(template_format, ''), status, deleted_at
		  FROM credentials WHERE public_id = $1::uuid`, credentialPublicID).
		Scan(&s.material, &s.keyID, &s.algorithm, &s.digest, &s.format, &s.status, &deletedAt)
	if err != nil {
		t.Fatalf("reading credential %s: %v", credentialPublicID, err)
	}
	s.deleted = deletedAt.Valid
	return s
}

func (s sealedState) holdsAnyMaterial() bool {
	return s.material || s.keyID || s.algorithm || s.digest
}

// TestDeletingAPersonDestroysMaterialAnUploadStored runs the real upload path,
// then the real delete.
func TestDeletingAPersonDestroysMaterialAnUploadStored(t *testing.T) {
	f := newReplicationFixture(t)
	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusCreated && res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}
	credential := credentialPublicID(t, "P-REP")
	if !sealedStateOf(t, credential).material {
		t.Fatal("the upload stored no material; the test would prove nothing")
	}

	if err := database.DeleteMember(f.companyID, "P-REP"); err != nil {
		t.Fatalf("deleting the person: %v", err)
	}

	after := sealedStateOf(t, credential)
	if after.holdsAnyMaterial() {
		t.Errorf("after deleting the person the credential still holds material: %+v", after)
	}
	if after.deleted {
		t.Error("the credential row was soft-deleted; a terminal's REMOVED report could no longer find it")
	}
	if after.format != models.TemplateFormatSensorLocal {
		t.Errorf("template_format = %q, want %q (the only copies left are on sensors)",
			after.format, models.TemplateFormatSensorLocal)
	}
}

// TestMaterialDestructionTouchesOnlyTheDeletedPerson: the same company's other
// people and another company's people keep their material.
func TestMaterialDestructionTouchesOnlyTheDeletedPerson(t *testing.T) {
	f := newDeletionFixture(t)
	giveSealedMaterial(t, f.credential, digestOne)

	colleague := seedPerson(t, f.companyID, "P-COLLEAGUE", "Colleague")
	colleagueCredential := seedFingerprintCredential(t, f.companyID, colleague, f.deviceID, "ACTIVE")
	giveSealedMaterial(t, colleagueCredential, digestTwo)

	otherCompany := operatorCompanyID(t, "two")
	stranger := seedPerson(t, otherCompany, "P-DELETE-2", "Other Tenant")
	strangerCredential := seedFingerprintCredential(t, otherCompany, stranger, f.deviceID, "ACTIVE")
	giveSealedMaterial(t, strangerCredential, digestOne)

	f.deletePerson(t)

	if s := sealedStateOf(t, f.credential); s.holdsAnyMaterial() {
		t.Errorf("deleted person's credential still holds material: %+v", s)
	}
	for name, credential := range map[string]string{
		"same-company colleague": colleagueCredential,
		"other-company person":   strangerCredential,
	} {
		s := sealedStateOf(t, credential)
		if !(s.material && s.keyID && s.algorithm && s.digest) {
			t.Errorf("%s lost material to somebody else's delete: %+v", name, s)
		}
		if s.format != models.TemplateFormatVendorTemplate {
			t.Errorf("%s template_format = %q, want it untouched", name, s.format)
		}
	}
}

// TestDeletingAPersonWithNoMaterialChangesNoCredential: the ordinary case today
// -- SENSOR_LOCAL, nothing sealed -- deletes as it always did.
func TestDeletingAPersonWithNoMaterialChangesNoCredential(t *testing.T) {
	f := newDeletionFixture(t)
	before := sealedStateOf(t, f.credential)

	f.deletePerson(t)

	after := sealedStateOf(t, f.credential)
	if after != before {
		t.Errorf("a credential with no material changed on delete: before %+v, after %+v",
			before, after)
	}
	if f.placementState(t) != models.PlacementRemoving {
		t.Errorf("placement = %s, want REMOVING -- the existing delete behaviour", f.placementState(t))
	}
}

// TestRemovedReportConvergesAfterMaterialIsDestroyed is the regression the
// design turns on: with the material gone, the terminal's REMOVED report --
// byte-for-byte the firmware's shape, no credential_id -- must still find the
// credential and retire the placement, not mint a new credential.
func TestRemovedReportConvergesAfterMaterialIsDestroyed(t *testing.T) {
	f := newDeletionFixture(t)
	giveSealedMaterial(t, f.credential, digestOne)
	f.deletePerson(t)

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id":       f.externalID,
		"state":           models.PlacementRemoved,
		"credential_type": models.CredentialFingerprint,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("firmware-shape REMOVED report = %d: %s", res.Code, res.Raw)
	}
	if state := f.placementState(t); state != models.PlacementRemoved {
		t.Errorf("placement = %s, want REMOVED", state)
	}

	var credentials int
	mustScan(t, `SELECT count(*) FROM credentials WHERE person_id = `+itoa(f.personID), &credentials)
	if credentials != 1 {
		t.Errorf("person has %d credentials after the REMOVED report, want the original 1", credentials)
	}
}

// TestRedeliveredDeleteIsStillIdempotent: a second delete finds no live row and
// does nothing, material or otherwise.
func TestRedeliveredDeleteIsStillIdempotent(t *testing.T) {
	f := newDeletionFixture(t)
	giveSealedMaterial(t, f.credential, digestOne)
	f.deletePerson(t)

	if err := database.DeleteMember(f.companyID, f.externalID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if s := sealedStateOf(t, f.credential); s.holdsAnyMaterial() {
		t.Errorf("material reappeared: %+v", s)
	}
}
