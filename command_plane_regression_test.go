package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Regressions for the defects found on the 2026-08-30 bench acceptance runs.
//
// EVERY TEST HERE IS A BUG THAT SHIPPED, written as the behaviour that was
// wrong rather than as the behaviour that is now right, so that a future change
// that reintroduces it fails with the original symptom in the failure message.
//
// The firmware half of these lives in test_command_job: the executed-command
// history, the busy result code, and the display detail that must fit its
// field. This file owns the platform half and the contract between them.

// ---------------------------------------------------------------------------
// D2 -- unknown parameters were silently accepted
// ---------------------------------------------------------------------------

// TestAnUnknownParameterIsRefusedRatherThanNormalisedAway.
//
// THE BUG: `{"target":"display"}` on a DIAGNOSTIC_SNAPSHOT returned 202 and was
// rewritten to the default section list. `target` is a DEVICE_TEST parameter,
// so the request that came back bore no relation to the one that was sent, and
// the caller was told it had been accepted.
//
// The handler one level up already refuses a parameter object for a command
// that takes none, with exactly this reasoning. This closes the same hole for
// commands that take SOME parameters.
func TestAnUnknownParameterIsRefusedRatherThanNormalisedAway(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-STRICT")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"strict@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-STRICT", token, csrf,
		map[string]any{
			"type":   models.CommandDiagnosticSnapshot,
			"params": map[string]any{"target": "display"},
		})

	if code != http.StatusBadRequest {
		t.Fatalf("issue with an unknown parameter = %d, want 400 -- a "+
			"parameter the platform discards is one an operator believes "+
			"took effect (%v)", code, body)
	}
	if body["code"] != models.CommandRefusedBadParams {
		t.Errorf("refusal code = %v, want %s", body["code"],
			models.CommandRefusedBadParams)
	}
	if rows := commandRowsFor(t, "CMD-STRICT"); len(rows) != 0 {
		t.Errorf("%d rows queued, want 0 -- a refused request must queue "+
			"nothing", len(rows))
	}
}

// The same leniency existed on DEVICE_TEST: a valid target beside an unknown
// field was accepted and the unknown field dropped.
func TestAnUnknownParameterOnADeviceTestIsAlsoRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-STRICT2")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"strict2@example.com", models.RoleAdmin)

	code, body := issueCommand(t, env, "CMD-STRICT2", token, csrf,
		map[string]any{
			"type": models.CommandDeviceTest,
			"params": map[string]any{
				"target":   "self_test",
				"duration": 30,
			},
		})

	if code != http.StatusBadRequest {
		t.Fatalf("device test with an unknown parameter = %d, want 400 (%v)",
			code, body)
	}
}

// A VALID REQUEST MUST STILL PASS. A strict decoder that refuses the ordinary
// case has replaced one defect with a worse one.
func TestStrictParametersDoNotBreakTheOrdinaryRequests(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-STRICT3")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"strict3@example.com", models.RoleAdmin)

	// Absent parameters.
	if code, body := issueCommand(t, env, "CMD-STRICT3", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("a bare snapshot = %d, want 202 (%v)", code, body)
	}

	// An explicit, known section list.
	if code, body := issueCommand(t, env, "CMD-STRICT3", token, csrf,
		map[string]any{
			"type":   models.CommandDiagnosticSnapshot,
			"params": map[string]any{"include": []string{"network"}},
		}); code != http.StatusConflict {
		// One is already outstanding, so this is the D3 refusal below rather
		// than a parameter refusal -- which is itself the assertion that the
		// parameters parsed.
		t.Fatalf("an explicit section list = %d, want 409 (%v)", code, body)
	}

	// And a valid device test, which has its own outstanding slot.
	if code, body := issueCommand(t, env, "CMD-STRICT3", token, csrf,
		deviceTestRequest("self_test")); code != http.StatusAccepted {
		t.Fatalf("a valid device test = %d, want 202 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// D3 -- a different request was answered with somebody else's command
// ---------------------------------------------------------------------------

// TestASecondCommandWithDifferentParametersIsRefusedNotSwallowed.
//
// THE BUG, EXACTLY AS OBSERVED: with a `display` test outstanding, a request
// for a `self_test` returned 202 Accepted carrying the DISPLAY command's id and
// parameters. The operator was told their self-test was accepted. It never ran,
// and no row for it was ever created.
//
// A success code for work that will never happen is the worst of the available
// answers, so this is now a conflict -- the request is well-formed and what
// refused it is the terminal's state.
func TestASecondCommandWithDifferentParametersIsRefusedNotSwallowed(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-DIFFER")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"differ@example.com", models.RoleAdmin)

	code, first := issueCommand(t, env, "CMD-DIFFER", token, csrf,
		deviceTestRequest("display"))
	if code != http.StatusAccepted {
		t.Fatalf("first issue = %d, want 202 (%v)", code, first)
	}

	code, second := issueCommand(t, env, "CMD-DIFFER", token, csrf,
		deviceTestRequest("self_test"))

	if code != http.StatusConflict {
		t.Fatalf("a self_test issued while a display test is outstanding = "+
			"%d, want 409 -- returning 202 with the display test's id told an "+
			"operator their self_test was accepted when it was discarded (%v)",
			code, second)
	}
	if second["code"] != models.CommandRefusedAlreadyPending {
		t.Errorf("refusal code = %v, want %s", second["code"],
			models.CommandRefusedAlreadyPending)
	}

	// AND THE OUTSTANDING COMMAND IS UNTOUCHED. The refusal must not cancel,
	// replace or re-parameterise the command already waiting.
	rows := commandRowsFor(t, "CMD-DIFFER")
	if len(rows) != 1 {
		t.Fatalf("%d rows queued, want 1", len(rows))
	}
	if payload, _ := rows[0]["payload"].(string); payload == "" ||
		!containsJSONTarget(payload, "display") {
		t.Errorf("the outstanding command's payload changed: %q", payload)
	}
	if status, _ := rows[0]["status"].(string); status != "PENDING" {
		t.Errorf("the outstanding command is %q, want PENDING", status)
	}
}

// The repeated button press must still be idempotent. This is the behaviour the
// refusal above must not have broken: identical parameters are the same
// request, and an operator pressing a button twice has done nothing wrong.
func TestAnIdenticalRepeatIsStillIdempotent(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	commandCapableTerminal(t, env, "CMD-SAME")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"same@example.com", models.RoleAdmin)

	code, first := issueCommand(t, env, "CMD-SAME", token, csrf,
		deviceTestRequest("display"))
	if code != http.StatusAccepted {
		t.Fatalf("first issue = %d, want 202 (%v)", code, first)
	}

	code, second := issueCommand(t, env, "CMD-SAME", token, csrf,
		deviceTestRequest("display"))
	if code != http.StatusAccepted {
		t.Fatalf("an identical repeat = %d, want 202 (%v)", code, second)
	}
	if second["already_pending"] != true {
		t.Errorf("the repeat does not report already_pending: %v", second)
	}
	if first["id"] != second["id"] {
		t.Errorf("the repeat returned a different command (%v vs %v)",
			first["id"], second["id"])
	}
	if rows := commandRowsFor(t, "CMD-SAME"); len(rows) != 1 {
		t.Errorf("%d rows queued, want 1", len(rows))
	}
}

// ---------------------------------------------------------------------------
// A -- the command-slot concurrency mismatch
// ---------------------------------------------------------------------------

// TestTwoCommandTypesReachTheTerminalTogether.
//
// THE CONDITION THE FIRMWARE ASSUMED COULD NOT HAPPEN. Its refusal branch
// carried the comment "this is a situation that should not arise", on the
// belief that the one-outstanding index guaranteed one command at a time. The
// index is keyed (device_id, job_type), so it guarantees one per TYPE, and a
// batched poll hands both over in the same cycle.
//
// This test exists to keep that fact visible on this side of the wire: if the
// batch is ever narrowed to one command, the firmware's single slot stops being
// a liability and this test should be the thing that says so.
func TestTwoCommandTypesReachTheTerminalTogether(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-BOTHDELIVER")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"both@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-BOTHDELIVER", token, csrf,
		diagnosticRequest()); code != http.StatusAccepted {
		t.Fatalf("snapshot = %d, want 202 (%v)", code, body)
	}
	if code, body := issueCommand(t, env, "CMD-BOTHDELIVER", token, csrf,
		deviceTestRequest("self_test")); code != http.StatusAccepted {
		t.Fatalf("device test = %d, want 202 (%v)", code, body)
	}

	commands := 0
	for _, job := range pollJobs(t, env, key) {
		if jobType, _ := job["job_type"].(string); jobType ==
			models.CommandDiagnosticSnapshot ||
			jobType == models.CommandDeviceTest {
			commands++
		}
	}
	// ASSERTED, NOT SKIPPED. This started as a t.Skipf, which is a vacuous pass:
	// if the batch were ever narrowed the test would go quiet and nobody would
	// learn that the firmware's single slot had stopped being contended.
	//
	// If you are here because you deliberately narrowed delivery to one command
	// per poll: that is a real fix for the concurrency mismatch, and the things
	// to update with it are this test and the refusal comment in main.cpp that
	// explains why a busy branch is reachable at all.
	if commands < 2 {
		t.Fatalf("only %d command(s) delivered in one poll, want 2 -- this "+
			"test records that the one-outstanding index is keyed by TYPE, so "+
			"the terminal's single command slot is genuinely contended in "+
			"ordinary use", commands)
	}
}

// TestABusyRefusalDoesNotSpendTheCommandsAttempts.
//
// THE BUG: the terminal refused the losing command as UNSUPPORTED, the platform
// recorded it through the ordinary failure path, and the command was charged an
// attempt and a backoff for work it had never begun. Repeated often enough that
// parks a perfectly good command in FAILED without it ever running once.
//
// A busy terminal is a terminal that will be ready in one pass of its loop, so
// the command goes straight back into the queue.
func TestABusyRefusalDoesNotSpendTheCommandsAttempts(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-BUSY")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"busy@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-BUSY", token, csrf,
		deviceTestRequest("self_test")); code != http.StatusAccepted {
		t.Fatalf("issue = %d, want 202 (%v)", code, body)
	}

	// FILTERED BY TYPE, not jobs[0]: registration queues STATE work too, and
	// acknowledging the wrong job would leave the command untouched and let
	// this test pass without asserting anything.
	jobID := commandJobID(t, env, key, models.CommandDeviceTest)

	res := ackJob(t, env, key, jobID, map[string]any{
		"status":      "FAILED",
		"result_code": models.CommandResultBusy,
		"error":       "the terminal is already running another command",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("busy acknowledgement = %d, want 200 (%s)", res.Code, res.Raw)
	}

	var status string
	var attempts int
	if err := database.DB.QueryRow(
		`SELECT status, attempts FROM sync_jobs WHERE id = $1`,
		int64(jobID)).Scan(&status, &attempts); err != nil {
		t.Fatalf("reading the job back: %v", err)
	}

	if status != "PENDING" {
		t.Errorf("a busy-refused command is %q, want PENDING -- it never ran, "+
			"so it must still be deliverable", status)
	}
	if attempts != 0 {
		t.Errorf("a busy refusal spent %d attempt(s), want 0 -- the terminal "+
			"never started the command", attempts)
	}

	// AND IT IS DELIVERABLE AGAIN IMMEDIATELY, rather than sitting out a
	// backoff for a terminal that is ready now. A short-validity command that
	// waits out a backoff lapses instead of running.
	if again := pollJobs(t, env, key); len(again) == 0 {
		t.Error("a busy-refused command was not re-offered on the next poll")
	}
}

// TestADuplicateSuppressionIsRecordedAsSuccess.
//
// The terminal's answer to an at-least-once redelivery of a command it has
// already run. It reports COMPLETED -- because the command was asked for once
// and performed once -- with a code that distinguishes "ran once" from "ran
// twice", which for a door is the whole question.
func TestADuplicateSuppressionIsRecordedAsSuccess(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	key := commandCapableTerminal(t, env, "CMD-DUP")

	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"dup@example.com", models.RoleAdmin)

	if code, body := issueCommand(t, env, "CMD-DUP", token, csrf,
		deviceTestRequest("self_test")); code != http.StatusAccepted {
		t.Fatalf("issue = %d, want 202 (%v)", code, body)
	}

	jobID := commandJobID(t, env, key, models.CommandDeviceTest)

	res := ackJob(t, env, key, jobID, map[string]any{
		"status":      "COMPLETED",
		"result_code": models.CommandResultDuplicateSuppressed,
	})
	if res.Code != http.StatusOK {
		t.Fatalf("duplicate acknowledgement = %d, want 200 (%s)", res.Code, res.Raw)
	}

	rows := commandRowsFor(t, "CMD-DUP")
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	// result_code is scanned as *string by commandRowsFor, so it is read
	// through the pointer rather than asserted as a string -- a failed type
	// assertion here would read as an empty code and pass for the wrong reason.
	code, _ := rows[0]["result_code"].(*string)
	if code == nil || *code != models.CommandResultDuplicateSuppressed {
		t.Errorf("result_code = %v, want %s", rows[0]["result_code"],
			models.CommandResultDuplicateSuppressed)
	}
	if status, _ := rows[0]["status"].(string); status != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED -- the command did run, once",
			status)
	}
}

// commandJobID polls as the terminal and returns the id of the one COMMAND of
// the given type. Fails the test if it was not delivered, so a test can never
// quietly acknowledge a job that is not the one under test.
func commandJobID(t *testing.T, env *testEnv, key, jobType string) float64 {
	t.Helper()
	for _, job := range pollJobs(t, env, key) {
		if got, _ := job["job_type"].(string); got == jobType {
			id, _ := job["id"].(float64)
			return id
		}
	}
	t.Fatalf("no %s was delivered to the terminal", jobType)
	return 0
}

// containsJSONTarget reports whether an encoded parameter object names a target.
// Deliberately a substring check: the assertion is that the stored payload was
// not swapped for another command's, and the exact encoding is not the point.
func containsJSONTarget(payload, target string) bool {
	return payload != "" && target != "" &&
		strings.Contains(payload, `"`+target+`"`)
}
