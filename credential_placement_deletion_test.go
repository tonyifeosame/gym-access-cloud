package main

import (
	"net/http"
	"testing"

	"access-terminal-cloud-api/models"
)

// Placement convergence after a person is deleted (fa6b779).
//
// Deleting somebody soft-deletes the person and queues a DELETE job to every
// terminal holding them. The terminal erases the template and reports the
// removal -- POST /devices/credentials/placement with state REMOVED.
//
// That report used to be REFUSED. RecordPlacement resolved the person with
// `deleted_at IS NULL`, and by the time a REMOVED arrives the delete has
// already set it, so the lookup missed and the handler answered 404. The
// firmware retires a 404 as permanent, correctly, so nothing retried and no
// queue jammed -- the placement simply stayed PLACED for ever. The platform
// went on believing a credential sat on a sensor that had already erased it.
//
// The three cases the fix rests on, and which are asserted here:
//
//	DELETE                              -> placement becomes REMOVING
//	REMOVED for a soft-deleted person   -> placement becomes REMOVED
//	PLACED/FAILED for a deleted person  -> still refused
//
// The last is the one that keeps the fix narrow. PLACED and FAILED say "this
// credential is now on this door" and "it could not be"; a deleted person must
// not acquire either, because accepting one would resurrect a placement for
// somebody an operator removed.

// deletionFixture is a terminal, an operator session that may delete, and a
// person whose credential that terminal physically holds.
type deletionFixture struct {
	env        *testEnv
	companyID  int64
	token      string
	csrf       string
	deviceKey  string
	deviceID   int64
	personID   int64
	externalID string
	credential string
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")

	// MANAGER is the least-privileged role that may delete a person, which is
	// the role this path is actually reached with.
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"placement-delete@example.com", models.RoleManager)

	deviceKey := env.registerDevice(env.siteAKey, "AT-DELETE")
	deviceID := deviceIDBySerial(t, "AT-DELETE")

	personID := seedPerson(t, companyID, "P-DELETE", "Deleted Person")

	// An ACTIVE credential physically placed on that terminal. SENSOR_LOCAL
	// satisfies 020's substance check for a non-PENDING credential without
	// inventing any material.
	credential := seedFingerprintCredential(t, companyID, personID, deviceID, "ACTIVE")
	seedPlacement(t, credential, deviceID, models.PlacementPlaced, 7)

	return &deletionFixture{
		env: env, companyID: companyID, token: token, csrf: csrf,
		deviceKey: deviceKey, deviceID: deviceID, personID: personID,
		externalID: "P-DELETE", credential: credential,
	}
}

// deletePerson removes the person through the console, the way an operator does.
func (f *deletionFixture) deletePerson(t *testing.T) {
	t.Helper()
	code, body := consoleCall(t, f.env.router, "DELETE",
		"/api/v1/console/people/"+f.externalID, "", f.token, f.csrf)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE person = %d, want 204: %v", code, body)
	}
}

// report is one terminal placement report for this fixture's credential.
func (f *deletionFixture) report(t *testing.T, state, errText string) response {
	t.Helper()
	body := map[string]any{
		"credential_id": f.credential,
		"member_id":     f.externalID,
		"slot":          7,
		"state":         state,
	}
	if errText != "" {
		body["error"] = errText
	}
	return f.env.do("POST", "/api/v1/devices/credentials/placement", body,
		deviceAuth(f.deviceKey))
}

func (f *deletionFixture) placementState(t *testing.T) string {
	t.Helper()
	var state string
	mustScan(t, `SELECT state FROM credential_placements
	              WHERE device_id = `+itoa(f.deviceID), &state)
	return state
}

// ---------------------------------------------------------------------------
// 1: DELETE -> placement becomes REMOVING
// ---------------------------------------------------------------------------

// TestDeletingAPersonMarksTheirPlacementsRemoving.
//
// REMOVING is the PLATFORM's intent, recorded in the same transaction as the
// soft-delete and the job fan-out -- exactly as terminal relocation already
// does. Without it a placement jumps PLACED -> REMOVED the instant a terminal
// happens to report back, and in between (however long that door takes to poll,
// or for ever if it is offline) the console shows a credential still placed on
// somebody who has been deleted.
func TestDeletingAPersonMarksTheirPlacementsRemoving(t *testing.T) {
	f := newDeletionFixture(t)

	if state := f.placementState(t); state != models.PlacementPlaced {
		t.Fatalf("fixture placement = %s, want PLACED before the delete", state)
	}

	f.deletePerson(t)

	if state := f.placementState(t); state != models.PlacementRemoving {
		t.Errorf("placement = %s after the delete, want REMOVING -- the console "+
			"still shows this credential as sitting on a door for a person who "+
			"has been removed", state)
	}

	// The intent is a fresh statement, so any stale failure reason is cleared.
	var lastError any
	mustScan(t, `SELECT last_error FROM credential_placements
	              WHERE device_id = `+itoa(f.deviceID), &lastError)
	if lastError != nil {
		t.Errorf("last_error = %v after the delete, want NULL", lastError)
	}

	// The delete must still queue the work that makes the terminal act.
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs
	                      WHERE job_type = 'DELETE' AND device_id = `+itoa(f.deviceID)); n == 0 {
		t.Error("the delete queued no DELETE job to the terminal holding the credential")
	}
}

// TestDeletingAPersonLeavesASettledPlacementAlone.
//
// The sweep is scoped to PENDING and PLACED -- the states that still assert
// something about a live door. A placement already REMOVED is finished, and
// re-opening it as REMOVING would ask a terminal to erase a template it has
// already erased and reported.
func TestDeletingAPersonLeavesASettledPlacementAlone(t *testing.T) {
	f := newDeletionFixture(t)

	mustExec(t, `UPDATE credential_placements SET state = 'REMOVED',
	                    removed_at = CURRENT_TIMESTAMP
	              WHERE device_id = `+itoa(f.deviceID))

	f.deletePerson(t)

	if state := f.placementState(t); state != models.PlacementRemoved {
		t.Errorf("placement = %s after the delete, want REMOVED left untouched", state)
	}
}

// ---------------------------------------------------------------------------
// 2: REMOVED for a soft-deleted person -> placement becomes REMOVED
// ---------------------------------------------------------------------------

// TestRemovedReportIsAcceptedForADeletedPerson is the defect itself.
//
// The terminal has erased the template and is saying so. Before the fix this
// answered 404, the firmware retired the entry as permanent, and the row stayed
// PLACED for ever -- and because the platform never offers work for a credential
// it believes is placed, that stale row is what would block re-enrolling the
// person if they were restored.
func TestRemovedReportIsAcceptedForADeletedPerson(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)

	res := f.report(t, models.PlacementRemoved, "")
	if res.Code != http.StatusOK {
		t.Fatalf("REMOVED report for a deleted person = %d, want 200 -- the "+
			"terminal has erased the template and the platform is refusing to "+
			"hear it: %s", res.Code, res.Raw)
	}

	if state := f.placementState(t); state != models.PlacementRemoved {
		t.Errorf("placement = %s after the REMOVED report, want REMOVED", state)
	}

	// removed_at is what dates the convergence; a REMOVED row without it would
	// record that it happened but not when.
	var removedAt any
	mustScan(t, `SELECT removed_at FROM credential_placements
	              WHERE device_id = `+itoa(f.deviceID), &removedAt)
	if removedAt == nil {
		t.Error("removed_at is NULL on a REMOVED placement")
	}
}

// TestRemovedReportStillRequiresTheDevicesOwnTenant.
//
// The fix widens the person lookup by ONE predicate -- deleted_at -- and must
// not have widened it by any other. company_id still comes from the
// authenticated device's own row, so a terminal cannot reach another tenant's
// person by reporting REMOVED for them.
func TestRemovedReportStillRequiresTheDevicesOwnTenant(t *testing.T) {
	f := newDeletionFixture(t)

	// A person in the OTHER tenant, soft-deleted, so the only thing that could
	// admit them is a missing company filter.
	otherCompany := operatorCompanyID(t, "two")
	seedPerson(t, otherCompany, "P-OTHER-TENANT", "Other Tenant Person")
	mustExec(t, `UPDATE people SET deleted_at = CURRENT_TIMESTAMP
	              WHERE external_id = 'P-OTHER-TENANT'`)

	// A refused request must write nothing. The assertion is a delta: the
	// fixture's own placement for P-DELETE is still present, and what must not
	// appear is a NEW row for this cross-tenant report.
	before := queryInt(t, `SELECT count(*) FROM credential_placements`)

	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "P-OTHER-TENANT",
		"state":     models.PlacementRemoved,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("a terminal reported REMOVED for another tenant's person (got %d): %s",
			res.Code, res.Raw)
	}

	if after := queryInt(t, `SELECT count(*) FROM credential_placements`); after != before {
		t.Errorf("a refused cross-tenant REMOVED created %d placement row(s), want 0",
			after-before)
	}
}

// ---------------------------------------------------------------------------
// 3: PLACED / FAILED for a soft-deleted person -> still refused
// ---------------------------------------------------------------------------

// TestPlacedAndFailedReportsAreStillRefusedForADeletedPerson.
//
// This is what keeps the fix to the one state that needs it. PLACED says "this
// credential is now on this door" and FAILED says "it could not be"; accepting
// either for a deleted person would resurrect a placement for somebody an
// operator removed. Both must still answer 404, and neither may disturb the
// REMOVING the delete wrote.
func TestPlacedAndFailedReportsAreStillRefusedForADeletedPerson(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)

	for _, tc := range []struct {
		state, errText string
	}{
		{models.PlacementPlaced, ""},
		{models.PlacementFailed, "sensor full"},
	} {
		res := f.report(t, tc.state, tc.errText)
		if res.Code != http.StatusNotFound {
			t.Errorf("%s report for a deleted person = %d, want 404 -- a deleted "+
				"person must not acquire a placement: %s",
				tc.state, res.Code, res.Raw)
		}

		if state := f.placementState(t); state != models.PlacementRemoving {
			t.Errorf("placement = %s after a refused %s report, want the REMOVING "+
				"the delete wrote to stand", state, tc.state)
		}
	}

	// And the refusal must not have quietly changed the credential -- it stays
	// exactly as the fixture seeded it, ACTIVE. Asserting the state directly
	// (rather than merely "not PENDING") also catches an unexpected transition
	// to any other state.
	var status string
	mustScan(t, `SELECT status FROM credentials WHERE public_id = '`+f.credential+`'::uuid`,
		&status)
	if status != "ACTIVE" {
		t.Errorf("credential status = %s, want it left ACTIVE -- a refused "+
			"report must not change the credential's state", status)
	}
}

// ---------------------------------------------------------------------------
// 4: the REMOVED report in the EXACT shape the firmware sends
// ---------------------------------------------------------------------------

// TestRemovedReportAcceptsTheFirmwareWireShape.
//
// The other tests in this file send a REMOVED report carrying credential_id and
// slot. THE FIRMWARE SENDS NEITHER. net_service.cpp builds the removal body as
// exactly:
//
//	{"member_id":"<ext>","state":"REMOVED","credential_type":"FINGERPRINT"}
//
// -- no slot ("a removal names a person, not a location") and no credential_id.
// So the earlier coverage never exercised the payload the fleet actually puts on
// the wire, and a REMOVED that stalls at REMOVING in production (device reports,
// row never converges) is exactly the gap that shape would expose. This asserts
// the platform accepts it and converges the placement, resolving the credential
// from person + type alone.
func TestRemovedReportAcceptsTheFirmwareWireShape(t *testing.T) {
	f := newDeletionFixture(t)
	f.deletePerson(t)

	// Byte-for-byte the fields net_service.cpp emits -- and nothing else.
	res := f.env.do("POST", "/api/v1/devices/credentials/placement", map[string]any{
		"member_id":       f.externalID,
		"state":           models.PlacementRemoved,
		"credential_type": models.CredentialFingerprint,
	}, deviceAuth(f.deviceKey))
	if res.Code != http.StatusOK {
		t.Fatalf("firmware-shape REMOVED report = %d, want 200 -- this is the exact "+
			"body the terminal sends (member_id + state + credential_type, NO slot, "+
			"NO credential_id): %s", res.Code, res.Raw)
	}

	if state := f.placementState(t); state != models.PlacementRemoved {
		t.Errorf("placement = %s after the firmware-shape REMOVED report, want REMOVED",
			state)
	}

	var removedAt any
	mustScan(t, `SELECT removed_at FROM credential_placements
	              WHERE device_id = `+itoa(f.deviceID), &removedAt)
	if removedAt == nil {
		t.Error("removed_at is NULL after a firmware-shape REMOVED report")
	}
}
