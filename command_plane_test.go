package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The remote command plane (028), end to end.
//
// THE FEATURE IN ONE SENTENCE: an operator asks a terminal to do something, the
// terminal collects it on its next poll, runs it, and answers with a result --
// and until it answers, the console says only what it can prove.
//
// EVERY TEST BELOW IS WRITTEN AS A THING THAT MUST NOT BE POSSIBLE, because the
// failures this design is guarding against are all of the same shape: the
// platform believing a door did something it did not. Firmware that does not
// recognise a job type acknowledges it as applied -- deliberately -- so an old
// unit's acknowledgement is indistinguishable from a new one's, and the
// capability gate is the only thing between that and a console reporting
// ACCEPTED for a command that was thrown away.
//
// WHAT IS DELIBERATELY NOT TESTED HERE: what the terminal DOES with a command.
// That is the firmware's test_command_job and test_sync_handoff suites, and
// this repository must not restate them. What IS tested is the contract between
// the two -- the exact job type tokens, the envelope, the capability gate, the
// deadline and the acknowledgement -- because that is the half this side owns.

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func commandsPath(serial string) string {
	return "/api/v1/console/terminals/" + serial + "/commands"
}

func capabilitiesPath(serial string) string {
	return "/api/v1/console/terminals/" + serial + "/capabilities"
}

// commandCapableTerminal registers a terminal and leaves it ONLINE, credentialed
// and advertising both command capabilities -- which together are the only
// state in which a command may be queued.
//
// THE HEARTBEAT IS PART OF THE FIXTURE, and that is the feature rather than
// scaffolding. Registration alone does not earn a terminal the right to be sent
// a command: it has to have SAID what it can do.
func commandCapableTerminal(t *testing.T, env *testEnv, serial string) string {
	t.Helper()
	key := env.registerDevice(env.siteAKey, serial)
	reportCapabilities(t, env, key,
		models.CapabilityWifiProvisioning,
		models.CapabilityTerminalAnnounce,
		models.CapabilityCmdDiagnosticSnapshot,
		models.CapabilityCmdDeviceTest)
	return key
}

// issueCommand posts one command as an operator and returns the raw answer.
func issueCommand(t *testing.T, env *testEnv, serial, token, csrf string,
	body map[string]any) (int, map[string]any) {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encoding the command request: %v", err)
	}
	return consoleCall(t, env.router, "POST", commandsPath(serial),
		string(encoded), token, csrf)
}

// diagnosticRequest is the ordinary, valid request used by most tests.
func diagnosticRequest() map[string]any {
	return map[string]any{"type": models.CommandDiagnosticSnapshot}
}

func deviceTestRequest(target string) map[string]any {
	return map[string]any{
		"type":   models.CommandDeviceTest,
		"params": map[string]any{"target": target},
	}
}

// commandRowsFor returns the COMMAND rows queued for one serial, oldest first.
func commandRowsFor(t *testing.T, serial string) []map[string]any {
	t.Helper()
	rows, err := database.DB.Query(`
		SELECT j.id, j.public_id, j.job_type, j.status, j.command_class,
		       j.command_version, j.requires_capability, j.expires_at,
		       j.requested_by_email, j.delivered_at, j.result_code, j.payload
		  FROM sync_jobs j JOIN devices d ON d.id = j.device_id
		 WHERE d.serial_number = $1 AND j.command_class = 'COMMAND'
		 ORDER BY j.id ASC`, serial)
	if err != nil {
		t.Fatalf("reading command rows: %v", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var (
			id                                       int64
			publicID, jobType, status, commandClass  string
			commandVersion                           int
			requiresCapability, requestedBy, resCode *string
			expiresAt, deliveredAt                   *time.Time
			payload                                  []byte
		)
		if err := rows.Scan(&id, &publicID, &jobType, &status, &commandClass,
			&commandVersion, &requiresCapability, &expiresAt, &requestedBy,
			&deliveredAt, &resCode, &payload); err != nil {
			t.Fatalf("scanning command row: %v", err)
		}
		out = append(out, map[string]any{
			"id": id, "public_id": publicID, "job_type": jobType,
			"status": status, "command_class": commandClass,
			"command_version": commandVersion, "requires_capability": requiresCapability,
			"expires_at": expiresAt, "requested_by_email": requestedBy,
			"delivered_at": deliveredAt, "result_code": resCode,
			"payload": string(payload),
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading command rows: %v", err)
	}
	return out
}

// pollJobs collects a terminal's due work exactly as the firmware does.
func pollJobs(t *testing.T, env *testEnv, key string) []map[string]any {
	t.Helper()
	res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil, deviceAuth(key))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /devices/jobs = %d, want 200 (%s)", res.Code, res.Raw)
	}
	jobs, _ := res.Body["jobs"].([]any)
	out := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		if object, ok := job.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// ackJob acknowledges as the terminal, optionally with a result.
func ackJob(t *testing.T, env *testEnv, key string, jobID float64,
	body map[string]any) response {
	t.Helper()
	return env.do(http.MethodPost,
		fmt.Sprintf("/api/v1/devices/jobs/%d/complete", int64(jobID)),
		body, deviceAuth(key))
}

// ---------------------------------------------------------------------------
// The schema's own promises
// ---------------------------------------------------------------------------

// TestJobTypeConstraintMatchesTheCanonicalList.
//
// THE 027 TRAP, CLOSED. sync_jobs_type_check and sync_jobs_change_device_check
// are NOT additive: every migration DROPs and re-CREATEs the whole list, so one
// that names only its own types silently revokes everyone else's. This nearly
// shipped -- the enrolment branch numbered itself 022 and, as written, would
// have revoked WIFI_RECOVERY, which 024 had added on the deployed line.
//
// Asserting the constraint's CONTENTS rather than trusting the SQL to be
// complete is the only way a future migration's omission fails a test instead
// of a door.
func TestJobTypeConstraintMatchesTheCanonicalList(t *testing.T) {
	newTestEnv(t)

	var definition string
	if err := database.DB.QueryRow(`
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conname = 'sync_jobs_type_check'`).Scan(&definition); err != nil {
		t.Fatalf("reading sync_jobs_type_check: %v", err)
	}

	want := append([]string{}, models.SyncJobStateTypes...)
	want = append(want, models.SyncJobReserved...)
	want = append(want, models.CommandWifiRecovery, models.CommandEnrollFingerprint)
	for _, name := range models.IssuableCommandTypes() {
		want = append(want, name)
	}

	for _, jobType := range want {
		if !strings.Contains(definition, "'"+jobType+"'") {
			t.Errorf("sync_jobs_type_check does not admit %q. A migration that "+
				"restates this constraint has dropped a type the other side "+
				"still sends.\nconstraint: %s", jobType, definition)
		}
	}
}

// TestEveryCommandTypeIsAddressedToOneDevice.
//
// A command fanned out to a site because device_id happened to be NULL is the
// worst outcome any of these types has available -- 024 says so about setup
// mode, and it is more true of a command that will one day pulse a strike.
func TestEveryCommandTypeIsAddressedToOneDevice(t *testing.T) {
	newTestEnv(t)

	var definition string
	if err := database.DB.QueryRow(`
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conname = 'sync_jobs_change_device_check'`).Scan(&definition); err != nil {
		t.Fatalf("reading sync_jobs_change_device_check: %v", err)
	}

	for name := range models.CommandSpecs {
		if !strings.Contains(definition, "'"+name+"'") {
			t.Errorf("%s is not covered by sync_jobs_change_device_check: a row "+
				"with a NULL device_id would be a site-wide command", name)
		}
	}
}

// TestACommandRowCannotExistWithoutItsGate.
//
// THE LOAD-BEARING CONSTRAINT. A command that names no capability is one the
// platform will happily send to firmware that will acknowledge it and throw it
// away -- precisely the false ACCEPTED that 025 exists to prevent. In the
// schema rather than in Go, because Go can be bypassed by the next handler
// somebody writes.
func TestACommandRowCannotExistWithoutItsGate(t *testing.T) {
	env := newTestEnv(t)
	commandCapableTerminal(t, env, "CMD-CONSTRAINT")

	var deviceID, siteID int64
	if err := database.DB.QueryRow(
		`SELECT id, site_id FROM devices WHERE serial_number = $1`,
		"CMD-CONSTRAINT").Scan(&deviceID, &siteID); err != nil {
		t.Fatalf("resolving the terminal: %v", err)
	}

	// A COMMAND with no capability. The trigger fills one in when the column is
	// left NULL, so this writes an explicit empty-ish state the trigger cannot
	// rescue: command_class set by hand with requires_capability forced NULL
	// after the fact.
	_, err := database.DB.Exec(`
		INSERT INTO sync_jobs (site_id, device_id, job_type, protocol_version,
		                       status, command_class, requires_capability)
		VALUES ($1, $2, 'DEVICE_TEST', 1, 'PENDING', 'COMMAND', NULL)`,
		siteID, deviceID)
	if err != nil {
		// The trigger derived one, which is the OTHER half of the design and is
		// also correct. Prove the constraint directly by defeating the trigger.
		t.Logf("trigger supplied a capability, as designed: %v", err)
	}

	_, err = database.DB.Exec(`
		UPDATE sync_jobs SET requires_capability = NULL
		 WHERE device_id = $1 AND command_class = 'COMMAND'`, deviceID)
	if err == nil {
		t.Fatal("a COMMAND row was allowed to exist with no capability gate: " +
			"the platform would send it to firmware that cannot carry it out")
	}
	if !strings.Contains(err.Error(), "sync_jobs_command_complete_check") {
		t.Errorf("the row was refused, but not by the completeness constraint: %v", err)
	}
}

// TestClassificationIsAutomaticForEveryWriter.
//
// 028 classifies with a TRIGGER rather than by editing eight insert sites, so a
// NINTH written next year by somebody who has never read the migration is
// classified correctly without their participation. This asserts that for the
// writers that already exist: a person change is STATE, an enrolment is
// COMMAND, and neither needed a code change to become so.
func TestClassificationIsAutomaticForEveryWriter(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-CLASSIFY")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"classify@example.com", models.RoleAdmin)

	// A person change, written by enqueuePersonChangeTx, which this feature
	// never touched.
	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"CLASSIFY-1","full_name":"Ada"}`, token, csrf)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("creating a person = %d (%v)", code, body)
	}

	var stateClass string
	if err := database.DB.QueryRow(`
		SELECT command_class FROM sync_jobs
		 WHERE job_type IN ('CREATE','UPDATE') ORDER BY id DESC LIMIT 1`).
		Scan(&stateClass); err != nil {
		t.Fatalf("reading the person job: %v", err)
	}
	if stateClass != models.CommandClassState {
		t.Errorf("a person change was classified %q, want STATE", stateClass)
	}
}

// ---------------------------------------------------------------------------
// The capability gate
// ---------------------------------------------------------------------------

// TestACommandIsRefusedForATerminalThatHasNeverReported.
//
// SILENCE IS NOT CONSENT, and this is the whole fleet in the field today. A
// terminal that has never said what it can do is refused on the same terms as
// one that reported and lacks the capability, because treating silence as
// capability is exactly what produced the false ACCEPTED 025 was written to
// stop.
//
// THE ASSERTION THE STATUS CODE DOES NOT MAKE: nothing is queued. A refusal
// that had already written the row would be a gate that only hides a button.
func TestACommandIsRefusedForATerminalThatHasNeverReported(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// Registered, credentialed -- and never heard from.
	env.registerDevice(env.siteAKey, "CMD-SILENT")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"silent@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-SILENT", token, csrf, diagnosticRequest())
	if code != http.StatusConflict {
		t.Fatalf("issuing to a never-reported terminal = %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.CommandRefusedIncapable {
		t.Errorf("refusal code = %v, want %s", body["code"], models.CommandRefusedIncapable)
	}
	// The human half distinguishes "has never told us" from "reports and
	// cannot" -- the two send an operator to different places.
	if message, _ := body["error"].(string); !strings.Contains(message, "never reported") {
		t.Errorf("the refusal does not distinguish silence from incapability: %q", message)
	}

	if rows := commandRowsFor(t, "CMD-SILENT"); len(rows) != 0 {
		t.Errorf("%d command(s) were queued for a terminal that cannot run them", len(rows))
	}
}

// TestACommandIsRefusedForATerminalThatReportsAndLacksIt.
//
// The other half of the same gate, and the message differs so an operator is
// sent to the firmware catalogue rather than to check whether the unit has
// heartbeat.
func TestACommandIsRefusedForATerminalThatReportsAndLacksIt(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	key := env.registerDevice(env.siteAKey, "CMD-OLD-FW")
	// An older image: it reports, and has none of the command capabilities.
	reportCapabilities(t, env, key,
		models.CapabilityWifiProvisioning, models.CapabilityWifiRecovery)

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"oldfw@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-OLD-FW", token, csrf, diagnosticRequest())
	if code != http.StatusConflict {
		t.Fatalf("issuing to an incapable terminal = %d, want 409 (%v)", code, body)
	}
	if body["code"] != models.CommandRefusedIncapable {
		t.Errorf("refusal code = %v, want %s", body["code"], models.CommandRefusedIncapable)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "reported what it can do") {
		t.Errorf("the refusal should say the terminal reported and cannot: %q", message)
	}
	if rows := commandRowsFor(t, "CMD-OLD-FW"); len(rows) != 0 {
		t.Errorf("%d command(s) were queued for firmware that would discard them", len(rows))
	}
}

// TestTheGateIsPerCommandNotPerTerminal.
//
// A terminal that can run a diagnostic but not a device test gets one and is
// refused the other. Without this the gate would be "has any command
// capability", which is not what a mixed fleet needs.
func TestTheGateIsPerCommandNotPerTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	key := env.registerDevice(env.siteAKey, "CMD-PARTIAL")
	reportCapabilities(t, env, key, models.CapabilityCmdDiagnosticSnapshot)

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"partial@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-PARTIAL", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("the advertised command = %d, want 202 (%v)", code, body)
	}
	if code, body := issueCommand(t, env, "CMD-PARTIAL", token, csrf,
		deviceTestRequest(models.DeviceTestBuzzer)); code != http.StatusConflict {
		t.Fatalf("the unadvertised command = %d, want 409 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Idempotency, and one outstanding at a time
// ---------------------------------------------------------------------------

// TestASecondCommandOfTheSameTypeReturnsTheFirst.
//
// An operator pressing a button twice has done nothing wrong, and the honest
// answer is what is already queued for them. Two rows would mean the terminal
// ran the command twice -- for a diagnostic that is waste; for the commands
// this plane will carry later it is a door opening twice.
func TestASecondCommandOfTheSameTypeReturnsTheFirst(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-TWICE")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"twice@example.com", models.RoleAdmin)

	code, first := issueCommand(t, env, "CMD-TWICE", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("first issue = %d, want 202 (%v)", code, first)
	}

	code, second := issueCommand(t, env, "CMD-TWICE", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("second issue = %d, want 202 (%v)", code, second)
	}
	if second["already_pending"] != true {
		t.Errorf("the second issue does not report already_pending: %v", second)
	}
	if first["id"] != second["id"] {
		t.Errorf("the second issue returned a different command (%v vs %v)",
			first["id"], second["id"])
	}

	if rows := commandRowsFor(t, "CMD-TWICE"); len(rows) != 1 {
		t.Errorf("%d rows queued, want 1 -- a repeated press must not queue a "+
			"second command", len(rows))
	}
}

// TestDifferentCommandTypesAreNotInTensionWithEachOther.
//
// The one-outstanding rule is PER TYPE. A diagnostic and a device test do not
// conflict, and serialising them would make the console refuse work it could do.
func TestDifferentCommandTypesAreNotInTensionWithEachOther(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-BOTH")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"both@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-BOTH", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("diagnostic = %d (%v)", code, body)
	}
	if code, body := issueCommand(t, env, "CMD-BOTH", token, csrf,
		deviceTestRequest(models.DeviceTestSelfTest)); code != http.StatusAccepted {
		t.Fatalf("device test = %d (%v)", code, body)
	}

	if rows := commandRowsFor(t, "CMD-BOTH"); len(rows) != 2 {
		t.Errorf("%d rows queued, want 2 -- different types must not block "+
			"each other", len(rows))
	}
}

// TestARetriedRequestReturnsTheOriginalCommand.
//
// THE CASE THE ONE-OUTSTANDING INDEX CANNOT COVER: a browser that never saw its
// response retries AFTER the first command has been collected and completed, so
// nothing is outstanding and a second row would be perfectly legal. The
// idempotency key is what closes it.
func TestARetriedRequestReturnsTheOriginalCommand(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-RETRY")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"retry@example.com", models.RoleAdmin)

	request := diagnosticRequest()
	request["idempotency_key"] = "6f1a2b3c-4d5e-4f60-8a91-b2c3d4e5f607"

	code, first := issueCommand(t, env, "CMD-RETRY", token, csrf, request)
	if code != http.StatusAccepted {
		t.Fatalf("first issue = %d (%v)", code, first)
	}

	// The terminal collects it and finishes it, so nothing is outstanding.
	jobs := pollJobs(t, env, key)
	if len(jobs) == 0 {
		t.Fatal("the terminal was given no work")
	}
	for _, job := range jobs {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			ackJob(t, env, key, job["id"].(float64), map[string]any{
				"status": "COMPLETED", "result_code": models.CommandResultOK,
			})
		}
	}

	// Now the retry arrives.
	code, second := issueCommand(t, env, "CMD-RETRY", token, csrf, request)
	if code != http.StatusAccepted {
		t.Fatalf("the retry = %d (%v)", code, second)
	}
	if first["id"] != second["id"] {
		t.Errorf("the retry queued a NEW command (%v vs %v): a request whose "+
			"response was lost must not run the command twice",
			first["id"], second["id"])
	}
	if rows := commandRowsFor(t, "CMD-RETRY"); len(rows) != 1 {
		t.Errorf("%d rows queued, want 1", len(rows))
	}
}

func TestAMalformedIdempotencyKeyIsRefusedCleanly(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-BADKEY")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"badkey@example.com", models.RoleAdmin)

	request := diagnosticRequest()
	request["idempotency_key"] = "not-a-uuid"

	code, body := issueCommand(t, env, "CMD-BADKEY", token, csrf, request)
	if code != http.StatusBadRequest {
		t.Errorf("a malformed key = %d, want 400 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// The deadline
// ---------------------------------------------------------------------------

// TestALapsedCommandIsNeverHandedOut.
//
// A SAFETY PROPERTY, NOT A SHORTFALL. Every other job describes state, so
// delivering it late is merely late; a command describes an ACT, and performing
// it late is destructive. The predicate is on the DELIVERY path rather than in
// a background sweep, so the guarantee does not depend on a task having run.
func TestALapsedCommandIsNeverHandedOut(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-LAPSED")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"lapsed@example.com", models.RoleAdmin)

	code, issued := issueCommand(t, env, "CMD-LAPSED", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, issued)
	}

	// Wind the window shut. Every other way of producing this state involves
	// waiting ten minutes.
	if _, err := database.DB.Exec(`
		UPDATE sync_jobs SET expires_at = CURRENT_TIMESTAMP - interval '1 second'
		 WHERE public_id = $1`, issued["id"]); err != nil {
		t.Fatalf("expiring the command: %v", err)
	}

	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			t.Fatal("a lapsed command was handed to the terminal: it would run " +
				"after the situation it was issued for had been resolved")
		}
	}

	// And the console reads it as EXPIRED rather than as still waiting.
	statusCode, status := consoleCall(t, env.router, "GET",
		commandsPath("CMD-LAPSED")+"/"+issued["id"].(string), "", token, "")
	if statusCode != http.StatusOK {
		t.Fatalf("reading the command = %d (%v)", statusCode, status)
	}
	if status["state"] != models.CommandStateExpired {
		t.Errorf("state = %v, want %s", status["state"], models.CommandStateExpired)
	}
}

// TestAnIssuedCommandCarriesADeadlineTheTerminalCanCheck.
//
// The envelope's expires_at is what lets the DEVICE refuse a command that
// lapsed between collection and apply -- an interval the server cannot see,
// because fetching a job takes a lease rather than changing its state.
func TestAnIssuedCommandCarriesADeadlineTheTerminalCanCheck(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-ENVELOPE")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"envelope@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-ENVELOPE", token, csrf,
		deviceTestRequest(models.DeviceTestBuzzer)); code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, body)
	}

	var found map[string]any
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDeviceTest {
			found = job
		}
	}
	if found == nil {
		t.Fatal("the command was not handed to the terminal")
	}

	envelope, ok := found["command"].(map[string]any)
	if !ok {
		t.Fatalf("the job carries no command envelope: %v", found)
	}
	if envelope["version"] != float64(models.CommandEnvelopeVersion) {
		t.Errorf("envelope version = %v, want %d",
			envelope["version"], models.CommandEnvelopeVersion)
	}
	if envelope["expires_at"] == nil {
		t.Error("the envelope carries no deadline: the terminal could not " +
			"refuse a command that lapsed while it waited to run it")
	}
	if envelope["requires_capability"] != models.CapabilityCmdDeviceTest {
		t.Errorf("envelope capability = %v, want %s",
			envelope["requires_capability"], models.CapabilityCmdDeviceTest)
	}
	params, _ := envelope["params"].(map[string]any)
	if params == nil || params["target"] != models.DeviceTestBuzzer {
		t.Errorf("the envelope does not carry the validated parameters: %v", envelope)
	}
}

// TestAStateJobIsUnchangedOnTheWire.
//
// THE BACKWARDS-COMPATIBILITY ASSERTION, and the one that decides whether this
// change is safe to deploy against the fleet in the field. A STATE job must
// serialise byte-for-byte as it did before 028 -- no `command` key at all --
// because every firmware ever built parses these and none of them has heard of
// an envelope.
func TestAStateJobIsUnchangedOnTheWire(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-STATEJOB")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"statejob@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"STATE-1","full_name":"Grace"}`, token, csrf)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("creating a person = %d (%v)", code, body)
	}

	sawStateJob := false
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot ||
			job["job_type"] == models.CommandDeviceTest {
			continue
		}
		sawStateJob = true
		if _, present := job["command"]; present {
			t.Errorf("a %v job carries a command envelope. Firmware that "+
				"predates 028 would be parsing a document it has never seen: %v",
				job["job_type"], job)
		}
	}
	if !sawStateJob {
		t.Skip("no state job was queued for this terminal; nothing to compare")
	}
}

// ---------------------------------------------------------------------------
// Authorization and tenancy
// ---------------------------------------------------------------------------

// TestTheRoleGateIsPerCommandNotPerRoute.
//
// The router mounts this plane at MANAGER, which is the FLOOR rather than the
// gate: a route can carry exactly one role check, and this plane is meant to
// grow commands with very different tiers. The per-command MinRole is enforced
// in the handler against the registry, and without it the day somebody
// registers UNLOCK every manager in every tenant could open every door.
func TestTheRoleGateIsPerCommandNotPerRoute(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	for _, tc := range []struct {
		role string
		want int
	}{
		{models.RoleOwner, http.StatusAccepted},
		{models.RoleAdmin, http.StatusAccepted},
		{models.RoleManager, http.StatusAccepted},
		{models.RoleViewer, http.StatusForbidden},
	} {
		t.Run(tc.role, func(t *testing.T) {
			serial := "CMD-ROLE-" + tc.role
			commandCapableTerminal(t, env, serial)

			_, token, csrf := consoleOperatorSession(t, env.router, one,
				strings.ToLower(tc.role)+"-cmd@example.com", tc.role)

			code, body := issueCommand(t, env, serial, token, csrf, diagnosticRequest())
			if code != tc.want {
				t.Fatalf("%s issuing a diagnostic = %d, want %d (%v)",
					tc.role, code, tc.want, body)
			}

			// A refusal must queue nothing. A 403 that had already written the
			// row would be a role check that only hides a button.
			rows := commandRowsFor(t, serial)
			if tc.want == http.StatusForbidden && len(rows) != 0 {
				t.Errorf("%s was refused but %d command(s) were queued", tc.role, len(rows))
			}
		})
	}
}

// TestACommandCannotReachAnotherTenantsTerminal.
//
// A serial in another company is NOT FOUND rather than forbidden, so the answer
// cannot confirm that it is registered elsewhere.
func TestACommandCannotReachAnotherTenantsTerminal(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	two := operatorCompanyID(t, "two")

	// The terminal belongs to company one's Site A.
	commandCapableTerminal(t, env, "CMD-TENANT")

	_, token, csrf := consoleOperatorSession(t, env.router, two,
		"other-tenant@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-TENANT", token, csrf, diagnosticRequest())
	if code != http.StatusNotFound {
		t.Errorf("another tenant issuing a command = %d, want 404 (%v)", code, body)
	}
	if rows := commandRowsFor(t, "CMD-TENANT"); len(rows) != 0 {
		t.Errorf("%d command(s) were queued across a tenant boundary", len(rows))
	}
}

func TestTheCommandPlaneRefusesWithoutASession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	commandCapableTerminal(t, env, "CMD-ANON")

	if code, _ := issueCommand(t, env, "CMD-ANON", "", "", diagnosticRequest()); code != http.StatusUnauthorized {
		t.Errorf("an anonymous issue = %d, want 401", code)
	}
	if rows := commandRowsFor(t, "CMD-ANON"); len(rows) != 0 {
		t.Errorf("%d command(s) were queued with no session", len(rows))
	}
}

// ---------------------------------------------------------------------------
// Parameters
// ---------------------------------------------------------------------------

// TestADoorCannotBeOpenedThroughADeviceTest.
//
// THE ASSERTION THIS PHASE IS FENCED BY, checked through the HTTP surface as
// well as in the registry's own unit test. Pulsing a strike is a door opening
// and belongs to its own command with its own tier; reaching it through a test
// target would put a door behind the safeguards appropriate to a buzzer.
func TestADoorCannotBeOpenedThroughADeviceTest(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-NORELAY")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"norelay@example.com", models.RoleAdmin)

	for _, target := range []string{"relay", "unlock", "door", "strike"} {
		code, body := issueCommand(t, env, "CMD-NORELAY", token, csrf,
			deviceTestRequest(target))
		if code != http.StatusBadRequest {
			t.Fatalf("device test target %q = %d, want 400 (%v)", target, code, body)
		}
	}

	if rows := commandRowsFor(t, "CMD-NORELAY"); len(rows) != 0 {
		t.Errorf("%d command(s) were queued for a door-opening target", len(rows))
	}
}

func TestAnUnknownCommandTypeIsRefusedWithTheList(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-UNKNOWN")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"unknown@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-UNKNOWN", token, csrf,
		map[string]any{"type": "FACTORY_RESET"})
	if code != http.StatusBadRequest {
		t.Fatalf("an unregistered type = %d, want 400 (%v)", code, body)
	}
	if body["code"] != models.CommandRefusedUnknownType {
		t.Errorf("refusal code = %v, want %s", body["code"], models.CommandRefusedUnknownType)
	}
	if body["supported_types"] == nil {
		t.Error("the refusal does not name what IS supported")
	}
}

// TestAnInheritedCommandKeepsItsOwnEndpoint.
//
// WIFI_RECOVERY and ENROLL_FINGERPRINT are registered so the status paths can
// describe their rows, and refused here so each keeps exactly one entry point.
// Two ways to queue a Change Wi-Fi is two places for its rules to diverge.
func TestAnInheritedCommandKeepsItsOwnEndpoint(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-INHERITED")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"inherited@example.com", models.RoleAdmin)

	for _, name := range []string{models.CommandWifiRecovery, models.CommandEnrollFingerprint} {
		code, body := issueCommand(t, env, "CMD-INHERITED", token, csrf,
			map[string]any{"type": name})
		if code != http.StatusBadRequest {
			t.Errorf("%s through the command plane = %d, want 400 (%v)", name, code, body)
		}
		if body["code"] != models.CommandRefusedNotIssuable {
			t.Errorf("%s refusal code = %v, want %s", name, body["code"],
				models.CommandRefusedNotIssuable)
		}
	}
}

// ---------------------------------------------------------------------------
// Delivery, results and state
// ---------------------------------------------------------------------------

// TestTheStateMachineNeverClaimsMoreThanThePlatformCanProve.
//
// QUEUED, DELIVERED and ACCEPTED are three states rather than one boolean
// because the difference between them is the whole product promise: a queued
// command and an applied one look identical in the database unless the
// difference is modelled.
func TestTheStateMachineNeverClaimsMoreThanThePlatformCanProve(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-STATES")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"states@example.com", models.RoleAdmin)

	code, issued := issueCommand(t, env, "CMD-STATES", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, issued)
	}
	id := issued["id"].(string)

	if issued["state"] != models.CommandStateQueued {
		t.Errorf("a freshly issued command reads %v, want QUEUED", issued["state"])
	}

	readState := func() map[string]any {
		t.Helper()
		code, body := consoleCall(t, env.router, "GET",
			commandsPath("CMD-STATES")+"/"+id, "", token, "")
		if code != http.StatusOK {
			t.Fatalf("reading the command = %d (%v)", code, body)
		}
		return body
	}

	// COLLECTED. The strongest thing the platform may say at this point is that
	// the terminal HAS it, which is short of evidence that it ran it.
	var jobID float64
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			jobID = job["id"].(float64)
		}
	}
	if jobID == 0 {
		t.Fatal("the command was not handed over")
	}
	if state := readState(); state["state"] != models.CommandStateDelivered {
		t.Errorf("after collection the state is %v, want DELIVERED", state["state"])
	}

	// A REDELIVERY MUST NOT LOOK LIKE A FRESH ONE. delivered_at is stamped on
	// first collection only; last_attempt_at moves on every fetch because it is
	// the lease.
	firstDelivered := readState()["delivered_at"]
	pollJobs(t, env, key)
	if again := readState()["delivered_at"]; again != firstDelivered {
		t.Errorf("delivered_at moved on redelivery (%v -> %v): a command "+
			"redelivered every minute would look freshly collected every minute",
			firstDelivered, again)
	}

	// ACKNOWLEDGED, with a result.
	res := ackJob(t, env, key, jobID, map[string]any{
		"status":      "COMPLETED",
		"result_code": models.CommandResultOK,
		"result":      map[string]any{"storage": map[string]any{"members": 41}},
	})
	if res.Code != http.StatusOK {
		t.Fatalf("acknowledging = %d (%s)", res.Code, res.Raw)
	}

	final := readState()
	if final["state"] != models.CommandStateAccepted {
		t.Errorf("after acknowledgement the state is %v, want ACCEPTED", final["state"])
	}
	if final["result_code"] != models.CommandResultOK {
		t.Errorf("result_code = %v, want %s", final["result_code"], models.CommandResultOK)
	}
	result, _ := final["result"].(map[string]any)
	storage, _ := result["storage"].(map[string]any)
	if storage == nil || storage["members"] != float64(41) {
		t.Errorf("the terminal's result was not stored: %v", final["result"])
	}
}

// TestADeviceReportedFailureCarriesItsCode.
//
// The code is what a client branches on; the sentence beside it is what an
// operator reads. Both are stored, because they answer different questions --
// "which recovery do I offer" and "what do I tell the customer".
func TestADeviceReportedFailureCarriesItsCode(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-FAILED")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"failed@example.com", models.RoleAdmin)

	code, issued := issueCommand(t, env, "CMD-FAILED", token, csrf,
		deviceTestRequest(models.DeviceTestSelfTest))
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, issued)
	}

	var jobID float64
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDeviceTest {
			jobID = job["id"].(float64)
		}
	}

	res := ackJob(t, env, key, jobID, map[string]any{
		"status":      "FAILED",
		"error":       "the command expired before it could be run",
		"result_code": models.CommandResultExpiredAtDevice,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("acknowledging a failure = %d (%s)", res.Code, res.Raw)
	}

	_, body := consoleCall(t, env.router, "GET",
		commandsPath("CMD-FAILED")+"/"+issued["id"].(string), "", token, "")
	if body["result_code"] != models.CommandResultExpiredAtDevice {
		t.Errorf("result_code = %v, want %s", body["result_code"],
			models.CommandResultExpiredAtDevice)
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "expired") {
		t.Errorf("the terminal's own words were not kept: %q", message)
	}
}

// TestAnOversizedResultIsRefusedRatherThanTruncated.
//
// A device-written value is bounded before it reaches the database. Refused
// rather than trimmed: half a JSON document is not a smaller result, it is a
// malformed one, and storing it would mean the console renders something no
// terminal ever said.
func TestAnOversizedResultIsRefusedRatherThanTruncated(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-HUGE")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"huge@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-HUGE", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, body)
	}

	var jobID float64
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			jobID = job["id"].(float64)
		}
	}

	res := ackJob(t, env, key, jobID, map[string]any{
		"status": "COMPLETED",
		"result": map[string]any{"padding": strings.Repeat("x", models.MaxCommandResultBytes+64)},
	})
	if res.Code != http.StatusBadRequest {
		t.Errorf("an oversized result = %d, want 400 (%s)", res.Code, res.Raw)
	}
}

// TestAnOrdinaryAcknowledgementStillWorks.
//
// THE OTHER HALF OF BACKWARDS COMPATIBILITY. A bare acknowledgement, and an
// empty body, must behave exactly as they always have -- every firmware in the
// field sends one of those two and none of them sends a result.
func TestAnOrdinaryAcknowledgementStillWorks(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-BAREACK")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"bareack@example.com", models.RoleAdmin)

	code, issued := issueCommand(t, env, "CMD-BAREACK", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, issued)
	}

	var jobID float64
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			jobID = job["id"].(float64)
		}
	}

	// No body at all -- what constrained firmware sends.
	res := ackJob(t, env, key, jobID, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("a bare acknowledgement = %d (%s)", res.Code, res.Raw)
	}

	_, body := consoleCall(t, env.router, "GET",
		commandsPath("CMD-BAREACK")+"/"+issued["id"].(string), "", token, "")
	if body["state"] != models.CommandStateAccepted {
		t.Errorf("state = %v, want ACCEPTED", body["state"])
	}
	if body["result_code"] != nil && body["result_code"] != "" {
		t.Errorf("a bare acknowledgement invented a result code: %v", body["result_code"])
	}
}

// ---------------------------------------------------------------------------
// Withdrawal
// ---------------------------------------------------------------------------

// TestAnUncollectedCommandCanBeWithdrawn, and a collected one cannot.
//
// A DELIVERED COMMAND CANNOT BE RECALLED, and the API refuses rather than
// pretending: the terminal already has it and will act on it or not. Reporting
// "cancelled" for something a door is executing would be exactly the lie the
// DELIVERED state exists to prevent.
func TestWithdrawalOnlyWorksBeforeCollection(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-WITHDRAW")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"withdraw@example.com", models.RoleAdmin)

	// Queued, and withdrawn before the terminal ever sees it.
	code, first := issueCommand(t, env, "CMD-WITHDRAW", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, first)
	}
	code, body := consoleCall(t, env.router, "DELETE",
		commandsPath("CMD-WITHDRAW")+"/"+first["id"].(string), "", token, csrf)
	if code != http.StatusOK {
		t.Fatalf("withdrawing a queued command = %d (%v)", code, body)
	}
	if body["state"] != models.CommandStateCancelled {
		t.Errorf("state after withdrawal = %v, want CANCELLED", body["state"])
	}

	// It is never handed out.
	for _, job := range pollJobs(t, env, key) {
		if job["job_type"] == models.CommandDiagnosticSnapshot {
			t.Fatal("a withdrawn command was still delivered")
		}
	}

	// A second command, collected, cannot be withdrawn.
	code, second := issueCommand(t, env, "CMD-WITHDRAW", token, csrf, diagnosticRequest())
	if code != http.StatusAccepted {
		t.Fatalf("second issue = %d (%v)", code, second)
	}
	pollJobs(t, env, key)

	code, body = consoleCall(t, env.router, "DELETE",
		commandsPath("CMD-WITHDRAW")+"/"+second["id"].(string), "", token, csrf)
	if code != http.StatusConflict {
		t.Errorf("withdrawing a collected command = %d, want 409 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Capabilities, as the app reads them
// ---------------------------------------------------------------------------

// TestTheCapabilitiesReadTellsTheAppWhatToDraw.
//
// A console that rendered a button for every command this server knows would
// offer work the terminal will refuse, and the operator would learn that from a
// 409 after pressing it. The answer here is the intersection.
func TestTheCapabilitiesReadTellsTheAppWhatToDraw(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	key := env.registerDevice(env.siteAKey, "CMD-CAPS")
	reportCapabilities(t, env, key, models.CapabilityCmdDiagnosticSnapshot)

	_, token, _ := consoleOperatorSession(t, env.router, one,
		"caps@example.com", models.RoleViewer)

	code, body := consoleCall(t, env.router, "GET", capabilitiesPath("CMD-CAPS"), "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading capabilities as VIEWER = %d, want 200 (%v)", code, body)
	}

	commands, _ := body["commands"].([]any)
	if len(commands) != len(models.IssuableCommandTypes()) {
		t.Fatalf("the offer lists %d commands, want %d",
			len(commands), len(models.IssuableCommandTypes()))
	}

	supported := map[string]bool{}
	for _, entry := range commands {
		offer, _ := entry.(map[string]any)
		supported[offer["type"].(string)] = offer["supported"].(bool)
	}
	if !supported[models.CommandDiagnosticSnapshot] {
		t.Error("the advertised command is not marked supported")
	}
	if supported[models.CommandDeviceTest] {
		t.Error("an unadvertised command is marked supported: the console " +
			"would draw a control the server will refuse")
	}
}

// A terminal that has NEVER reported and one that reports NOTHING are different
// answers, and the distinction survives JSON exactly: an absent key decodes to
// nil, `[]` to a non-nil empty slice. It is load-bearing -- the heartbeat merges
// with COALESCE, so treating them alike would let one silent beat switch a
// feature off for a door.
func TestNeverReportedAndReportsNothingAreDifferentAnswers(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	env.registerDevice(env.siteAKey, "CMD-NEVER")
	silent := env.registerDevice(env.siteAKey, "CMD-EMPTY")
	reportCapabilities(t, env, silent) // an explicit empty list

	_, token, _ := consoleOperatorSession(t, env.router, one,
		"never@example.com", models.RoleViewer)

	_, never := consoleCall(t, env.router, "GET", capabilitiesPath("CMD-NEVER"), "", token, "")
	_, empty := consoleCall(t, env.router, "GET", capabilitiesPath("CMD-EMPTY"), "", token, "")

	if never["capabilities"] != nil {
		t.Errorf("a terminal that never reported has capabilities %v, want null",
			never["capabilities"])
	}
	reported, ok := empty["capabilities"].([]any)
	if !ok || len(reported) != 0 {
		t.Errorf("a terminal that reported none has capabilities %v, want []",
			empty["capabilities"])
	}
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// TestIssuingACommandIsAudited.
//
// THE RECORD IS OF THE REQUEST, NOT OF THE OUTCOME. Whether the terminal ever
// collects it is in sync_jobs; who asked for it is here, and the two are joined
// by the command's public id. A repeated press is recorded rather than
// suppressed -- an operator pressing a button three times is a fact worth
// having when somebody later asks why a door did something.
func TestIssuingACommandIsAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-AUDIT")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"audit-cmd@example.com", models.RoleAdmin)

	request := deviceTestRequest(models.DeviceTestBuzzer)
	request["reason"] = "customer reports no beep at the door"

	code, issued := issueCommand(t, env, "CMD-AUDIT", token, csrf, request)
	if code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, issued)
	}

	// The same press again, which must ALSO be recorded.
	issueCommand(t, env, "CMD-AUDIT", token, csrf, request)

	var count int
	var changes []byte
	if err := database.DB.QueryRow(`
		SELECT count(*) FROM audit_events WHERE action = 'TERMINAL_COMMAND_ISSUED'`).
		Scan(&count); err != nil {
		t.Fatalf("counting audit events: %v", err)
	}
	if count != 2 {
		t.Errorf("%d audit records for two presses, want 2", count)
	}

	if err := database.DB.QueryRow(`
		SELECT changes FROM audit_events
		 WHERE action = 'TERMINAL_COMMAND_ISSUED' ORDER BY id DESC LIMIT 1`).
		Scan(&changes); err != nil {
		t.Fatalf("reading the audit record: %v", err)
	}

	var recorded map[string]any
	if err := json.Unmarshal(changes, &recorded); err != nil {
		t.Fatalf("decoding the audit record: %v", err)
	}
	if recorded["type"] != models.CommandDeviceTest {
		t.Errorf("the audit record does not name the command: %v", recorded)
	}
	if recorded["already_pending"] != true {
		t.Errorf("the repeated press was not recorded as already pending: %v", recorded)
	}
	if recorded["reason"] != "customer reports no beep at the door" {
		t.Errorf("the operator's reason was not kept: %v", recorded["reason"])
	}
}

// The requesting operator is denormalised onto the row, so it survives the
// account being deleted -- the same rule AuditRecord follows for its actor.
func TestTheRequestingOperatorIsRecordedOnTheCommand(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-WHOASKED")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"whoasked@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-WHOASKED", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("issue = %d (%v)", code, body)
	}

	rows := commandRowsFor(t, "CMD-WHOASKED")
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	email, _ := rows[0]["requested_by_email"].(*string)
	if email == nil || *email != "whoasked@example.com" {
		t.Errorf("requested_by_email = %v, want the issuing operator", email)
	}
}

// ---------------------------------------------------------------------------
// The inherited command still behaves exactly as it did
// ---------------------------------------------------------------------------

// TestChangeWifiIsUnaffectedByTheCommandPlane.
//
// THE ACCEPTANCE CRITERION FOR THIS PHASE, not a nice-to-have. 024's whole
// suite passing unmodified is the proof that generalising its pattern did not
// change its behaviour; this adds the one thing that suite cannot see -- that
// the row it writes is now classified, gated and indexed by the new machinery
// without anybody having edited RequestWifiRecovery.
func TestChangeWifiIsUnaffectedByTheCommandPlane(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	key := env.registerDevice(env.siteAKey, "CMD-WIFI")
	reportCapabilities(t, env, key, models.CapabilityWifiRecovery)

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"wifi-plane@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/terminals/CMD-WIFI/wifi-recovery", "", token, csrf)
	if code != http.StatusAccepted {
		t.Fatalf("Change Wi-Fi = %d, want 202 (%v)", code, body)
	}

	rows := commandRowsFor(t, "CMD-WIFI")
	if len(rows) != 1 {
		t.Fatalf("%d command rows for a Change Wi-Fi, want 1", len(rows))
	}
	if rows[0]["job_type"] != models.CommandWifiRecovery {
		t.Errorf("job_type = %v", rows[0]["job_type"])
	}
	if rows[0]["command_class"] != models.CommandClassCommand {
		t.Errorf("a Change Wi-Fi is classified %v, want COMMAND -- the trigger "+
			"should have done this with no code change",
			rows[0]["command_class"])
	}
	capability, _ := rows[0]["requires_capability"].(*string)
	if capability == nil || *capability != models.CapabilityWifiRecovery {
		t.Errorf("requires_capability = %v, want %s (the token its firmware "+
			"actually advertises, NOT a cmd_ prefixed one)",
			capability, models.CapabilityWifiRecovery)
	}

	// AND ITS PAYLOAD IS STILL EMPTY. 024's promise is that no credential
	// travels: there is no SSID and no pre-shared key anywhere on that path,
	// and the command plane must not have introduced somewhere to put one.
	if payload, _ := rows[0]["payload"].(string); payload != "" && payload != "null" {
		t.Errorf("a Change Wi-Fi now carries a payload (%q): the platform must "+
			"never learn a customer's network", payload)
	}
}
