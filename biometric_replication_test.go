package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Single-enrolment biometric replication, server half (026).
//
// docs/biometric-replication.md is the design; docs/sealing-key-lifecycle.md is
// the key's lifecycle and threat model, which was reviewed and approved before
// any of this was written.
//
// ---------------------------------------------------------------------------
// WHAT THESE TESTS ARE MOSTLY ABOUT
// ---------------------------------------------------------------------------
//
// Not the happy path. The happy path is four assertions; the rest of this file
// is the list of terminals that must NOT receive a fingerprint, because that is
// where the harm lives. A template routed to a door it should not reach is
// biometric data disclosed to a device somebody else controls, and unlike a
// password it cannot be reissued afterwards.
//
// Three gates decide, and each has its own test that it fails CLOSED:
//
//	the roster rule          TestFanOutRespectsTheRoster
//	the import capability    TestFanOutSkipsTerminalsThatCannotImport
//	sensor profile equality  TestFanOutRequiresAByteEqualSensorProfile
//
// ---------------------------------------------------------------------------
// THE MODULE B TEST IS THE THIRD ONE, AND IT IS NOT A COMPATIBILITY TEST
// ---------------------------------------------------------------------------
//
// Nothing here establishes that a template from one fingerprint module works on
// another. That is a hardware fact, it has NOT been tested, and no code in this
// repository asserts it. What TestFanOutRequiresAByteEqualSensorProfile proves
// is the opposite and is the only claim available to a test with no hardware in
// the room: a terminal reporting a DIFFERENT module receives nothing at all, and
// falls back to the re-enrolment work list that predates this feature.
//
// So the untested case is safe by construction rather than by assumption. When a
// second module actually arrives, HV-4 in docs/biometric-replication.md §9 is
// what would license widening this, and it is still PENDING.

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

// testSealingMasterKey is 32 bytes of nothing in particular, base64.
//
// A FIXED VALUE, not a random one: these tests assert that a key survives a
// round trip, and a random master key per run would turn a genuine wrap/unwrap
// regression into a flake that only reproduces on some seeds.
const testSealingMasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// useSealingMasterKey configures the deployment master key for one test.
//
// The key is resolved once per process, so the cache has to be reset on both
// sides -- otherwise the first test to run decides the value for every test
// after it, including the ones that need it ABSENT.
func useSealingMasterKey(t *testing.T) {
	t.Helper()
	t.Setenv(database.EnvSealingMasterKey, testSealingMasterKey)
	database.ResetSealingMasterKeyCache()
	t.Cleanup(database.ResetSealingMasterKeyCache)
}

// The sensor profile a terminal reports. `vendor:system_id:capacity`, exactly as
// the firmware would compose it from what getParameters() answered.
const (
	profileModuleA = "ZFM:0x0009:1000"

	// profileModuleB is a DIFFERENT module. Deliberately plausible -- same
	// vendor, same family, different system id -- because the dangerous mistake
	// is not routing a template to obviously alien hardware, it is routing one
	// to hardware that looks close enough that somebody assumed.
	profileModuleB = "ZFM:0x000A:1000"
)

type replicationFixture struct {
	env       *testEnv
	companyID int64

	// source is the enrolling terminal. It collected through the announce flow,
	// so it holds a real sealing key issued by the real code path.
	sourceKey    string
	sourceSerial string

	// target is a second terminal at the same site, capable and matching.
	targetKey    string
	targetSerial string

	personID  int64
	sealingID string
	sealing   string
}

func newReplicationFixture(t *testing.T) *replicationFixture {
	t.Helper()
	useSealingMasterKey(t)

	f := newAnnounceFixture(t)
	companyID := f.companyID

	// The source collects its credential through announce -> adopt -> approve ->
	// collect, which is also what mints the company's sealing key. Using the
	// real journey rather than seeding a row is the point: if key delivery
	// breaks, every test in this file fails rather than none of them.
	code, token := f.announce(t, "ESP32-SRC")
	status, body := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopting = %d: %v", status, body)
	}
	id, _ := body["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Source Door"); status != http.StatusOK {
		t.Fatalf("approving = %d: %v", status, body)
	}
	res := f.poll(t, token)
	sourceKey, _ := res.Body["api_key"].(string)
	sealing, _ := res.Body["sealing_key"].(string)
	sealingID, _ := res.Body["sealing_key_id"].(string)
	if sourceKey == "" || sealing == "" || sealingID == "" {
		t.Fatalf("collection did not deliver key + sealing key: %s", res.Raw)
	}

	targetKey := f.env.registerDevice(f.env.siteAKey, "ESP32-TGT")

	// Both terminals report a module and both halves of the transfer. A test
	// that needs one of these missing takes it away explicitly, so the default
	// is the configuration that WORKS and every refusal below is visibly the
	// result of one removal.
	setCapabilities(t, "ESP32-SRC", models.CapabilityBiometricExport, models.CapabilityBiometricImport)
	setCapabilities(t, "ESP32-TGT", models.CapabilityBiometricExport, models.CapabilityBiometricImport)
	setSensorProfile(t, "ESP32-SRC", profileModuleA)
	setSensorProfile(t, "ESP32-TGT", profileModuleA)

	personID := seedPerson(t, companyID, "P-REP", "Replication Subject")
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)

	return &replicationFixture{
		env: f.env, companyID: companyID,
		sourceKey: sourceKey, sourceSerial: "ESP32-SRC",
		targetKey: targetKey, targetSerial: "ESP32-TGT",
		personID: personID, sealingID: sealingID, sealing: sealing,
	}
}

func setCapabilities(t *testing.T, serial string, caps ...string) {
	t.Helper()
	// A variadic call with no arguments yields a NIL slice, which marshals to
	// `null` -- and `null` is not an array, so the 025 CHECK refuses it. An
	// empty capability list is a real answer ("reports, and has none") and has
	// to be written as `[]`.
	if caps == nil {
		caps = []string{}
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("encoding capabilities: %v", err)
	}
	mustExec(t, `UPDATE devices SET capabilities = $2::jsonb WHERE serial_number = $1`,
		serial, string(raw))
}

func setSensorProfile(t *testing.T, serial, profile string) {
	t.Helper()
	if profile == "" {
		mustExec(t, `UPDATE devices SET sensor_profile = NULL WHERE serial_number = $1`, serial)
		return
	}
	mustExec(t, `UPDATE devices SET sensor_profile = $2 WHERE serial_number = $1`, serial, profile)
}

// aTemplate is stand-in ciphertext. Opaque to the platform by design -- the
// server never decrypts it, so a test does not need it to be a real seal.
func aTemplate(n int) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("T", n)))
}

// Digests deliberately contain hex LETTERS. An all-numeric digest would make
// the "uppercase digest" case below vacuous -- ToUpper("1") is "1" -- and the
// test would pass while asserting nothing.
const (
	digestOne = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	digestTwo = "f6e5d4c3b2a1f6e5d4c3b2a1f6e5d4c3b2a1f6e5d4c3b2a1f6e5d4c3b2a1f6e5"
)

// upload posts sealed material as the enrolling terminal.
func (f *replicationFixture) upload(t *testing.T, body map[string]any) response {
	t.Helper()
	return f.env.do(http.MethodPost, "/api/v1/devices/credentials/material",
		body, deviceAuth(f.sourceKey))
}

// goodUpload is the body that works, so every test below can name exactly the
// one field it is changing.
func (f *replicationFixture) goodUpload() map[string]any {
	return map[string]any{
		"member_id":       "P-REP",
		"credential_type": models.CredentialFingerprint,
		"vendor":          "ZFM",
		"sensor_profile":  profileModuleA,
		"sealed": map[string]any{
			"ciphertext": aTemplate(512),
			"key_id":     f.sealingID,
			"algorithm":  "AES-256-GCM",
			"digest":     digestOne,
		},
	}
}

// placementState reads what the platform thinks a terminal owes.
func placementState(t *testing.T, serial, memberID string) string {
	t.Helper()
	var state string
	err := database.DB.QueryRow(`
		SELECT pl.state
		  FROM credential_placements pl
		  JOIN devices d ON d.id = pl.device_id
		  JOIN credentials c ON c.id = pl.credential_id
		  JOIN people p ON p.id = c.person_id
		 WHERE d.serial_number = $1 AND p.external_id = $2`, serial, memberID).Scan(&state)
	if err != nil {
		return ""
	}
	return state
}

// ---------------------------------------------------------------------------
// The journey
// ---------------------------------------------------------------------------

// TestMaterialUploadFansOutToMatchingTerminals is the whole feature in one test:
// enrol once, and a second door is told to expect the person.
func TestMaterialUploadFansOutToMatchingTerminals(t *testing.T) {
	f := newReplicationFixture(t)

	res := f.upload(t, f.goodUpload())
	if res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}
	if got := res.Body["placements_created"]; got != float64(1) {
		t.Errorf("placements_created = %v, want 1: %s", got, res.Raw)
	}

	// The enrolling terminal is never given work for what it already holds.
	if state := placementState(t, f.sourceSerial, "P-REP"); state != "" {
		t.Errorf("the enrolling terminal was given a placement (%s)", state)
	}
	if state := placementState(t, f.targetSerial, "P-REP"); state != models.PlacementPending {
		t.Errorf("target placement = %q, want PENDING", state)
	}

	// The target's work list now says a template exists, WITHOUT carrying it.
	pending := f.env.do(http.MethodGet, "/api/v1/devices/credentials/pending",
		nil, deviceAuth(f.targetKey))
	items := pending.Body["credentials"].([]any)
	if len(items) != 1 {
		t.Fatalf("target sees %d pending, want 1: %s", len(items), pending.Raw)
	}
	item := items[0].(map[string]any)
	if item["material_available"] != true {
		t.Errorf("material_available = %v, want true", item["material_available"])
	}
	if item["template_format"] != models.TemplateFormatVendorTemplate {
		t.Errorf("template_format = %v, want VENDOR_TEMPLATE", item["template_format"])
	}
	if strings.Contains(pending.Raw, aTemplate(512)) {
		t.Error("the pending list carried the ciphertext itself")
	}

	// And the target can collect the material it was told to expect.
	credentialID, _ := item["credential_id"].(string)
	fetch := f.env.do(http.MethodGet,
		"/api/v1/devices/credentials/"+credentialID+"/material", nil, deviceAuth(f.targetKey))
	if fetch.Code != http.StatusOK {
		t.Fatalf("fetch = %d: %s", fetch.Code, fetch.Raw)
	}
	sealed := fetch.Body["sealed"].(map[string]any)
	if sealed["ciphertext"] != aTemplate(512) {
		t.Error("fetched ciphertext did not round-trip")
	}
	if sealed["key_id"] != f.sealingID {
		t.Errorf("key_id = %v, want %v", sealed["key_id"], f.sealingID)
	}
	// The member id must come back, or the receiving terminal cannot rebuild the
	// AAD the enrolling terminal sealed under and the unseal fails.
	if fetch.Body["member_id"] != "P-REP" {
		t.Errorf("member_id = %v, want P-REP", fetch.Body["member_id"])
	}
}

// TestMaterialUploadIsIdempotent. A terminal that never heard the response
// retries, and a retry must not read as a failure or double the placements.
func TestMaterialUploadIsIdempotent(t *testing.T) {
	f := newReplicationFixture(t)

	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusOK {
		t.Fatalf("first upload = %d: %s", res.Code, res.Raw)
	}
	res := f.upload(t, f.goodUpload())
	if res.Code != http.StatusOK {
		t.Fatalf("retry = %d, want 200: %s", res.Code, res.Raw)
	}
	if res.Body["duplicate"] != true {
		t.Errorf("duplicate = %v, want true", res.Body["duplicate"])
	}
	if got := res.Body["placements_created"]; got != float64(0) {
		t.Errorf("retry created %v placements, want 0", got)
	}

	if n := queryInt(t, `SELECT COUNT(*) FROM credential_placements`); n != 1 {
		t.Errorf("placement rows = %d, want 1", n)
	}
	if n := queryInt(t, `SELECT COUNT(*) FROM credentials`); n != 1 {
		t.Errorf("credential rows = %d, want 1", n)
	}
}

// TestMaterialUploadRefusesASecondDifferentTemplate.
//
// Refused rather than overwritten. Overwriting would leave terminals that
// already placed the first template holding one finger and every terminal placed
// afterwards holding another -- silently, and presenting as exactly the "works
// at some doors and not others" complaint this feature exists to end.
func TestMaterialUploadRefusesASecondDifferentTemplate(t *testing.T) {
	f := newReplicationFixture(t)

	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusOK {
		t.Fatalf("first upload = %d: %s", res.Code, res.Raw)
	}

	second := f.goodUpload()
	second["sealed"].(map[string]any)["digest"] = digestTwo
	res := f.upload(t, second)
	if res.Code != http.StatusConflict {
		t.Fatalf("second template = %d, want 409: %s", res.Code, res.Raw)
	}
	if res.Body["code"] != "MATERIAL_ALREADY_PRESENT" {
		t.Errorf("code = %v, want MATERIAL_ALREADY_PRESENT", res.Body["code"])
	}
}

// TestMaterialUploadValidatesTheSeal.
//
// The platform cannot check that ciphertext decrypts -- it has no key. What it
// CAN check is that the envelope is describable: a scheme it understands, this
// company's current key, a well-formed digest, and a length a template could
// actually be. Anything else is material it could never route to a terminal that
// could read it, so storing it would be storing a permanent mystery.
func TestMaterialUploadValidatesTheSeal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   int
	}{
		{"unknown algorithm", func(b map[string]any) {
			b["sealed"].(map[string]any)["algorithm"] = "ROT13"
		}, http.StatusBadRequest},

		{"malformed digest", func(b map[string]any) {
			b["sealed"].(map[string]any)["digest"] = "not-a-digest"
		}, http.StatusBadRequest},

		{"uppercase digest", func(b map[string]any) {
			b["sealed"].(map[string]any)["digest"] = strings.ToUpper(digestOne)
		}, http.StatusBadRequest},

		{"ciphertext over the bound", func(b map[string]any) {
			b["sealed"].(map[string]any)["ciphertext"] =
				aTemplate(models.MaxSealedMaterialBytes + 1)
		}, http.StatusBadRequest},

		{"empty ciphertext", func(b map[string]any) {
			b["sealed"].(map[string]any)["ciphertext"] = ""
		}, http.StatusBadRequest},

		{"not base64", func(b map[string]any) {
			b["sealed"].(map[string]any)["ciphertext"] = "!!!not base64!!!"
		}, http.StatusBadRequest},

		{"no sensor profile", func(b map[string]any) {
			b["sensor_profile"] = ""
		}, http.StatusBadRequest},

		{"a card cannot be sealed", func(b map[string]any) {
			b["credential_type"] = models.CredentialCard
		}, http.StatusBadRequest},

		// 409 rather than 400: the body is fine, the terminal is simply sealing
		// under a key this company is not using. The remedy is on the terminal
		// -- re-collect -- and firmware needs to tell that apart from a body it
		// should stop sending.
		{"a key this company does not use", func(b map[string]any) {
			b["sealed"].(map[string]any)["key_id"] = "ck_ffffffffffff"
		}, http.StatusConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A FIXTURE PER CASE. Sharing one across the table let a case that
			// legitimately stored material leave a row behind that every later
			// case then reported as its own failure.
			f := newReplicationFixture(t)
			body := f.goodUpload()
			tc.mutate(body)
			res := f.upload(t, body)
			if res.Code != tc.want {
				t.Fatalf("%s = %d, want %d: %s", tc.name, res.Code, tc.want, res.Raw)
			}
			if n := queryInt(t, `SELECT COUNT(*) FROM credentials WHERE sealed_material IS NOT NULL`); n != 0 {
				t.Errorf("%s stored material anyway (%d rows)", tc.name, n)
			}
		})
	}
}

// TestMaterialUploadRequiresTheExportCapability.
//
// A terminal that has never said it can export a template has, by definition,
// not been shown to produce one this platform can route. NULL capabilities are
// the same answer as an empty list here: fail closed.
func TestMaterialUploadRequiresTheExportCapability(t *testing.T) {
	f := newReplicationFixture(t)

	for _, tc := range []struct {
		name string
		set  func()
	}{
		{"reports no capabilities at all", func() {
			mustExec(t, `UPDATE devices SET capabilities = NULL WHERE serial_number = 'ESP32-SRC'`)
		}},
		{"reports capabilities but not export", func() {
			setCapabilities(t, "ESP32-SRC", models.CapabilityBiometricImport)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set()
			res := f.upload(t, f.goodUpload())
			if res.Code != http.StatusConflict {
				t.Fatalf("%s = %d, want 409: %s", tc.name, res.Code, res.Raw)
			}
			if res.Body["code"] != "CAPABILITY_NOT_REPORTED" {
				t.Errorf("code = %v, want CAPABILITY_NOT_REPORTED", res.Body["code"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Where material must NOT go
// ---------------------------------------------------------------------------

// TestFanOutSkipsTerminalsThatCannotImport.
//
// Sending a template to a terminal that has never said it can install one is a
// guess, and the cost of the guess is biometric material sitting on a device
// that cannot use it and was never meant to hold it.
func TestFanOutSkipsTerminalsThatCannotImport(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T)
	}{
		{"has never reported capabilities", func(t *testing.T) {
			mustExec(t, `UPDATE devices SET capabilities = NULL WHERE serial_number = 'ESP32-TGT'`)
		}},
		{"reports capabilities and not import", func(t *testing.T) {
			setCapabilities(t, "ESP32-TGT", models.CapabilityBiometricExport)
		}},
		{"reports an empty list", func(t *testing.T) {
			setCapabilities(t, "ESP32-TGT")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplicationFixture(t)
			tc.set(t)

			res := f.upload(t, f.goodUpload())
			if res.Code != http.StatusOK {
				t.Fatalf("upload = %d: %s", res.Code, res.Raw)
			}
			if got := res.Body["placements_created"]; got != float64(0) {
				t.Errorf("placements_created = %v, want 0", got)
			}
			if state := placementState(t, f.targetSerial, "P-REP"); state != "" {
				t.Errorf("a terminal that cannot import was given a placement (%s)", state)
			}
		})
	}
}

// TestFanOutRequiresAByteEqualSensorProfile.
//
// ---------------------------------------------------------------------------
// THIS IS THE MODULE B CASE, AND IT ASSERTS A REFUSAL, NOT A COMPATIBILITY
// ---------------------------------------------------------------------------
//
// Whether a template exported from one fingerprint module imports and MATCHES on
// a different one has not been tested on hardware and is claimed nowhere in this
// repository. HV-4 in docs/biometric-replication.md §9 is the test that would
// establish it, and it is PENDING -- the second module has not arrived.
//
// So the platform refuses the pairing rather than assuming it. What this test
// proves is only that the refusal is real: a terminal reporting a different
// module gets no material, and works the person as a re-enrolment task exactly
// as every terminal in the field does today. Nobody is locked out and nothing
// untested is relied upon.
//
// Note that profileModuleB shares a vendor and a capacity with profileModuleA
// and differs only in system id. Being close is not being compatible, and the
// rule is EQUALITY precisely so that "close enough" is never a judgement any
// code here gets to make.
func TestFanOutRequiresAByteEqualSensorProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
	}{
		{"a different module", profileModuleB},
		{"has never reported a module", ""},
		{"differs only in case", strings.ToLower(profileModuleA)},
		{"differs only in whitespace", profileModuleA + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReplicationFixture(t)
			setSensorProfile(t, "ESP32-TGT", tc.profile)

			res := f.upload(t, f.goodUpload())
			if res.Code != http.StatusOK {
				t.Fatalf("upload = %d: %s", res.Code, res.Raw)
			}
			if got := res.Body["placements_created"]; got != float64(0) {
				t.Errorf("placements_created = %v, want 0 for %q", got, tc.profile)
			}
			if state := placementState(t, f.targetSerial, "P-REP"); state != "" {
				t.Errorf("material was routed to a %q module (%s)", tc.profile, state)
			}
		})
	}
}

// TestFanOutRespectsTheRoster.
//
// The replication surface is exactly as narrow as the access surface, using the
// SAME predicate rather than a second copy of it. A terminal is never handed the
// fingerprint of somebody it would refuse at the door.
func TestFanOutRespectsTheRoster(t *testing.T) {
	f := newReplicationFixture(t)

	// The person is permitted at the SOURCE terminal only. The target would
	// refuse them at the door, so it has no business holding their finger.
	mustExec(t, `DELETE FROM permissions WHERE person_id = $1`, f.personID)
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, device_id, effect, active)
	             VALUES ($1, $2, 'TERMINAL', $3, 'ALLOW', TRUE)`,
		f.companyID, f.personID, deviceIDBySerial(t, f.sourceSerial))

	res := f.upload(t, f.goodUpload())
	if res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}
	if got := res.Body["placements_created"]; got != float64(0) {
		t.Errorf("placements_created = %v, want 0", got)
	}
	if state := placementState(t, f.targetSerial, "P-REP"); state != "" {
		t.Errorf("a terminal that would refuse this person was sent their finger (%s)", state)
	}
}

// ---------------------------------------------------------------------------
// Fetching: one answer for every refusal
// ---------------------------------------------------------------------------

// credentialPublicID reads the credential a person holds.
func credentialPublicID(t *testing.T, memberID string) string {
	t.Helper()
	var id string
	mustScan(t, `SELECT c.public_id FROM credentials c
	              JOIN people p ON p.id = c.person_id
	             WHERE p.external_id = '`+memberID+`'`, &id)
	return id
}

// TestMaterialFetchIsRefusedWithoutAnInstruction.
//
// The gate is a PENDING or FAILED placement: a terminal may collect what it has
// been TOLD to hold and nothing else. Without this clause the endpoint would be
// a way to read the fingerprint of anybody on the roster, which is precisely
// what it must never be.
func TestMaterialFetchIsRefusedWithoutAnInstruction(t *testing.T) {
	f := newReplicationFixture(t)
	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}
	credentialID := credentialPublicID(t, "P-REP")

	cases := []struct {
		name string
		set  func(t *testing.T)
	}{
		{"no placement at all", func(t *testing.T) {
			mustExec(t, `DELETE FROM credential_placements`)
		}},
		{"already placed", func(t *testing.T) {
			mustExec(t, `UPDATE credential_placements
			                SET state = 'PLACED', slot = 3, placed_at = CURRENT_TIMESTAMP`)
		}},
		{"being withdrawn", func(t *testing.T) {
			mustExec(t, `UPDATE credential_placements SET state = 'REMOVING'`)
		}},
		{"withdrawn", func(t *testing.T) {
			mustExec(t, `UPDATE credential_placements
			                SET state = 'REMOVED', removed_at = CURRENT_TIMESTAMP`)
		}},
		{"credential revoked", func(t *testing.T) {
			mustExec(t, `UPDATE credentials
			                SET status = 'REVOKED', revoked_at = CURRENT_TIMESTAMP`)
		}},
		{"credential suspended", func(t *testing.T) {
			mustExec(t, `UPDATE credentials SET status = 'SUSPENDED'`)
		}},
		{"person deactivated", func(t *testing.T) {
			mustExec(t, `UPDATE people SET active = FALSE WHERE external_id = 'P-REP'`)
		}},
		{"capability withdrawn", func(t *testing.T) {
			setCapabilities(t, "ESP32-TGT", models.CapabilityBiometricExport)
		}},
		{"sensor replaced with a different module", func(t *testing.T) {
			setSensorProfile(t, "ESP32-TGT", profileModuleB)
		}},
		{"roster no longer admits them", func(t *testing.T) {
			mustExec(t, `DELETE FROM permissions WHERE person_id = $1`, f.personID)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case mutates one thing and is then undone, so the fixture
			// stays the working configuration and each refusal is visibly the
			// result of that single change.
			defer func() {
				mustExec(t, `UPDATE credential_placements SET state = 'PENDING',
				                slot = NULL, placed_at = NULL, removed_at = NULL`)
				mustExec(t, `UPDATE credentials SET status = 'ACTIVE', revoked_at = NULL`)
				mustExec(t, `UPDATE people SET active = TRUE WHERE external_id = 'P-REP'`)
				setCapabilities(t, "ESP32-TGT",
					models.CapabilityBiometricExport, models.CapabilityBiometricImport)
				setSensorProfile(t, "ESP32-TGT", profileModuleA)
				// The roster case deletes the permission, and restoring it here
				// rather than relying on being last is what keeps the next case
				// somebody adds from silently inheriting a refusal.
				mustExec(t, `INSERT INTO permissions
				                (company_id, person_id, scope_type, effect, active)
				             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)
				             ON CONFLICT DO NOTHING`, f.companyID, f.personID)
			}()

			tc.set(t)
			res := f.env.do(http.MethodGet,
				"/api/v1/devices/credentials/"+credentialID+"/material",
				nil, deviceAuth(f.targetKey))

			if res.Code != http.StatusNotFound {
				t.Fatalf("%s = %d, want 404: %s", tc.name, res.Code, res.Raw)
			}
			// ONE ANSWER. A refused terminal learns which rule stopped it from
			// nothing in the body -- not the state, not the profile, not whether
			// the credential exists at all.
			if res.Body["code"] != "MATERIAL_NOT_AVAILABLE" {
				t.Errorf("%s: code = %v, want MATERIAL_NOT_AVAILABLE", tc.name, res.Body["code"])
			}
			for _, leak := range []string{"PLACED", "REVOKED", "SUSPENDED", profileModuleB, "P-REP"} {
				if strings.Contains(res.Raw, leak) {
					t.Errorf("%s: refusal disclosed %q: %s", tc.name, leak, res.Raw)
				}
			}
		})
	}
}

// TestMaterialFetchIsRefusedAcrossTenants.
//
// A terminal in another company must not reach this credential even holding a
// perfectly valid device credential of its own.
func TestMaterialFetchIsRefusedAcrossTenants(t *testing.T) {
	f := newReplicationFixture(t)
	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}
	credentialID := credentialPublicID(t, "P-REP")

	// Company Two's terminal, configured as favourably as possible: same module,
	// both capabilities. Only the tenancy differs.
	otherKey := f.env.registerDevice(f.env.siteCKey, "ESP32-OTHER")
	setCapabilities(t, "ESP32-OTHER", models.CapabilityBiometricExport, models.CapabilityBiometricImport)
	setSensorProfile(t, "ESP32-OTHER", profileModuleA)

	res := f.env.do(http.MethodGet,
		"/api/v1/devices/credentials/"+credentialID+"/material", nil, deviceAuth(otherKey))
	if res.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant fetch = %d, want 404: %s", res.Code, res.Raw)
	}
}

// TestMaterialUploadCannotNameAnotherTenantsPerson.
func TestMaterialUploadCannotNameAnotherTenantsPerson(t *testing.T) {
	f := newReplicationFixture(t)
	seedPerson(t, companyIDBySlug(t, "two"), "P-OTHER", "Other Tenant")

	body := f.goodUpload()
	body["member_id"] = "P-OTHER"
	res := f.upload(t, body)
	if res.Code != http.StatusNotFound {
		t.Fatalf("upload for another tenant = %d, want 404: %s", res.Code, res.Raw)
	}
	if n := queryInt(t, `SELECT COUNT(*) FROM credentials WHERE sealed_material IS NOT NULL`); n != 0 {
		t.Errorf("material was stored for another tenant's person (%d rows)", n)
	}
}

// ---------------------------------------------------------------------------
// Applying, and the digest the server cannot verify
// ---------------------------------------------------------------------------

// TestAppliedDigestIsRecordedAndComparable.
//
// The receiving terminal says what it wrote. The server stores it and it can be
// compared to what the enrolling terminal reported -- which is how a template
// that arrived corrupted becomes visible rather than silent.
//
// THE SERVER CANNOT VERIFY IT and this test does not pretend otherwise: it
// asserts only that the value is recorded and that a disagreement is legible.
func TestAppliedDigestIsRecordedAndComparable(t *testing.T) {
	f := newReplicationFixture(t)
	if res := f.upload(t, f.goodUpload()); res.Code != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.Code, res.Raw)
	}

	res := f.env.do(http.MethodPost, "/api/v1/devices/credentials/placement", map[string]any{
		"member_id":      "P-REP",
		"state":          models.PlacementPlaced,
		"slot":           7,
		"applied_digest": digestOne,
	}, deviceAuth(f.targetKey))
	if res.Code != http.StatusOK {
		t.Fatalf("placement report = %d: %s", res.Code, res.Raw)
	}

	var applied, expected string
	mustScan(t, `SELECT pl.applied_digest, c.material_digest
	               FROM credential_placements pl
	               JOIN credentials c ON c.id = pl.credential_id
	               JOIN devices d ON d.id = pl.device_id
	              WHERE d.serial_number = 'ESP32-TGT'`, &applied, &expected)
	if applied != digestOne {
		t.Errorf("applied_digest = %q, want %q", applied, digestOne)
	}
	if applied != expected {
		t.Errorf("applied %q does not match stored %q", applied, expected)
	}

	// A later FAILED report carries no digest, and must not erase the record
	// that the right template once reached this door.
	if res := f.env.do(http.MethodPost, "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "P-REP", "state": models.PlacementFailed, "error": "sensor full",
	}, deviceAuth(f.targetKey)); res.Code != http.StatusOK {
		t.Fatalf("failed report = %d: %s", res.Code, res.Raw)
	}
	mustScan(t, `SELECT applied_digest FROM credential_placements pl
	               JOIN devices d ON d.id = pl.device_id
	              WHERE d.serial_number = 'ESP32-TGT'`, &applied)
	if applied != digestOne {
		t.Errorf("a FAILED report erased applied_digest (now %q)", applied)
	}
}

// TestPlacementReportRefusesAMalformedDigest.
func TestPlacementReportRefusesAMalformedDigest(t *testing.T) {
	f := newReplicationFixture(t)
	res := f.env.do(http.MethodPost, "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "P-REP", "state": models.PlacementPlaced, "slot": 2,
		"applied_digest": "nonsense",
	}, deviceAuth(f.targetKey))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("malformed applied_digest = %d, want 400: %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// The key itself
// ---------------------------------------------------------------------------

// TestSealingKeyIsDeliveredOnceAndIsNotRecoverable.
//
// Same contract as the device credential it rides beside: handed over exactly
// once, at collection, and readable nowhere afterwards. The database holds only
// a wrapped form, and the audit trail -- which records that the collection
// happened -- must not contain it.
func TestSealingKeyIsDeliveredOnceAndIsNotRecoverable(t *testing.T) {
	useSealingMasterKey(t)
	f := newAnnounceFixture(t)

	code, token := f.announce(t, "ESP32-KEY")
	status, body := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, body)
	}
	id, _ := body["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Key Door"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	first := f.poll(t, token)
	sealing, _ := first.Body["sealing_key"].(string)
	keyID, _ := first.Body["sealing_key_id"].(string)
	if sealing == "" || keyID == "" {
		t.Fatalf("collection delivered no sealing key: %s", first.Raw)
	}
	raw, err := base64.StdEncoding.DecodeString(sealing)
	if err != nil || len(raw) != 32 {
		t.Fatalf("sealing key is not 32 bytes of base64 (%d bytes, err %v)", len(raw), err)
	}
	if !strings.HasPrefix(keyID, "ck_") {
		t.Errorf("key id = %q, want a ck_ label", keyID)
	}

	// A second poll is refused, exactly as it is for the API key.
	if second := f.poll(t, token); second.Body["sealing_key"] != nil {
		t.Errorf("the sealing key was delivered twice: %s", second.Raw)
	}

	// The stored form is not the key.
	var wrapped []byte
	mustScan(t, `SELECT wrapped_key FROM company_sealing_keys WHERE key_id = '`+keyID+`'`, &wrapped)
	if strings.Contains(string(wrapped), string(raw)) {
		t.Error("the plaintext key is recoverable from the stored row")
	}

	// And it is in no audit record.
	var payloads string
	mustScan(t, `SELECT COALESCE(string_agg(changes::text, ' '), '') FROM audit_events`, &payloads)
	if strings.Contains(payloads, sealing) || strings.Contains(payloads, keyID) {
		t.Error("the sealing key reached the audit trail")
	}
}

// TestOneSealingKeyPerCompany.
//
// Two ACTIVE keys would mean two terminals of the same company sealing under
// different keys, and material only some doors could read -- which presents as
// "this member works at three doors and not the fourth", the exact complaint
// this feature exists to end. Enforced by a partial unique index, not by hope.
func TestOneSealingKeyPerCompany(t *testing.T) {
	f := newReplicationFixture(t)

	// A second terminal of the same company collects.
	af := &announceFixture{env: f.env, companyID: f.companyID}
	_, token, csrf := consoleOperatorSession(t, f.env.router, f.companyID,
		"second-admin@example.com", models.RoleAdmin)
	af.token, af.csrf = token, csrf

	code, announceToken := af.announce(t, "ESP32-THIRD")
	status, body := af.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, body)
	}
	id, _ := body["id"].(string)
	if status, body := af.approve(t, id, "Site B", "Third Door"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}
	res := af.poll(t, announceToken)

	if got, _ := res.Body["sealing_key_id"].(string); got != f.sealingID {
		t.Errorf("second terminal got key %q, want the company's existing %q", got, f.sealingID)
	}
	if got, _ := res.Body["sealing_key"].(string); got != f.sealing {
		t.Error("second terminal got a different sealing key from the first")
	}
	if n := queryInt(t, `SELECT COUNT(*) FROM company_sealing_keys WHERE company_id = `+itoa(f.companyID)); n != 1 {
		t.Errorf("company holds %d sealing keys, want 1", n)
	}
}

// TestCollectionSucceedsWithoutAMasterKey.
//
// A deployment that has not configured SEALING_MASTER_KEY must still be able to
// put a terminal on a wall. That path is the product working and it predates
// this feature by a long way; a terminal simply collects with no sealing key,
// cannot replicate, and behaves exactly as the fleet already in the field does.
//
// The loud version of this misconfiguration is at startup, not on a customer's
// claim.
func TestCollectionSucceedsWithoutAMasterKey(t *testing.T) {
	t.Setenv(database.EnvSealingMasterKey, "")
	database.ResetSealingMasterKeyCache()
	t.Cleanup(database.ResetSealingMasterKeyCache)

	f := newAnnounceFixture(t)
	key := f.setUp(t, "ESP32-NOKEY", "Site A", "No Key Door")
	if key == "" {
		t.Fatal("collection failed with no master key configured")
	}
	if n := queryInt(t, `SELECT COUNT(*) FROM company_sealing_keys`); n != 0 {
		t.Errorf("a sealing key was minted with no master key (%d rows)", n)
	}

	// The terminal works. It just cannot replicate.
	res := f.env.do(http.MethodGet, "/api/v1/devices/credentials/pending",
		nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("pending = %d, want a working terminal: %s", res.Code, res.Raw)
	}
}
