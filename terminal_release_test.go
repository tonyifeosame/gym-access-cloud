package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Terminal release (032).
//
// The property under test is the one the design was written for: THERE IS NO
// STATE IN WHICH THE NEXT OWNER HAS THE HARDWARE WHILE THE PREVIOUS OWNER'S
// DATA IS USABLE ON IT. Every test here is one edge of that argument:
//
//   - an order keeps the row live and authenticating (the terminal must be
//     able to fetch it, flush events, and confirm), and is idempotent
//   - the serial is not freed until the terminal proves the wipe with a
//     receipt, or an operator forces it with an attestation
//   - adoption is refused while any live row exists, ORDERED or not
//   - a wiped terminal announcing with its receipt finalizes the release in
//     the same transaction that creates its fresh announcement
//   - the next owner's row is seeded with a snapshot and is SETTING_UP until
//     the terminal acknowledges it
//   - a queued event from before this row existed is refused, not attributed
//   - the HMAC vectors match the firmware's copy byte for byte

// ---------------------------------------------------------------------------
// Shared vectors
// ---------------------------------------------------------------------------

// The same values live in the firmware's test_release_order suite. A change to
// either side's message layout fails one of the two fixtures rather than
// producing a platform and a fleet that quietly disagree.
const (
	vectorDeviceKey = "vector-device-key"
	vectorSerial    = "AT-VECTOR01"
	vectorReleaseID = "0f5b1e7c-9a2d-4c3e-8f10-5a6b7c8d9e0f"
	vectorOrderedAt = int64(1757700000)
	vectorMAC       = "129b952d0316ed140cefee3b205c55999039f5d9626daf504b1786e2cc54da9f"
	vectorReceipt   = "11a15d44d186e926a522fd4654ede97efb4ab3df5a97a3ba29f38d58e3723be1"
)

func TestReleaseVectorsMatchTheFirmware(t *testing.T) {
	sum := sha256.Sum256([]byte(vectorDeviceKey))
	hash := hex.EncodeToString(sum[:])

	mac, err := database.ComputeReleaseOrderMAC(hash, vectorSerial, vectorReleaseID,
		time.Unix(vectorOrderedAt, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(mac); got != vectorMAC {
		t.Errorf("order MAC = %s, want %s", got, vectorMAC)
	}

	receipt, err := database.ComputeReleaseReceipt(hash, vectorSerial, vectorReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(receipt); got != vectorReceipt {
		t.Errorf("receipt = %s, want %s", got, vectorReceipt)
	}

	// A hash that is not a digest is refused rather than used as a short key.
	if _, err := database.ComputeReleaseReceipt("not-hex", vectorSerial, vectorReleaseID); err == nil {
		t.Error("a malformed hash keyed a receipt")
	}
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type releaseFixture struct {
	*announceFixture
	serial string
	key    string
}

// newReleaseFixture sets a terminal up in Company One through the announce
// flow, so it has a credential issued the way a customer's would be.
func newReleaseFixture(t *testing.T, serial string) *releaseFixture {
	t.Helper()
	f := newAnnounceFixture(t)
	key := f.setUp(t, serial, "Site A", "Front Door")
	return &releaseFixture{announceFixture: f, serial: serial, key: key}
}

func (f *releaseFixture) order(t *testing.T, reason string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminals/"+f.serial+"/release",
		`{"reason":"`+reason+`"}`, f.token, f.csrf)
}

func (f *releaseFixture) readRelease(t *testing.T) map[string]any {
	t.Helper()
	status, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals/"+f.serial+"/release", "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("reading release = %d: %v", status, body)
	}
	return body
}

func (f *releaseFixture) heartbeat(t *testing.T, capabilities []string) map[string]any {
	t.Helper()
	body := map[string]any{"firmware_version": "1.5.0", "status": "ONLINE"}
	if capabilities != nil {
		body["capabilities"] = capabilities
	}
	res := f.env.do(http.MethodPost, "/api/v1/devices/heartbeat", body, deviceAuth(f.key))
	if res.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d: %s", res.Code, res.Raw)
	}
	return res.Body
}

// orderFromHeartbeat is what the terminal reads off its heartbeat.
func orderFromHeartbeat(t *testing.T, beat map[string]any) (releaseID, mac string, orderedAt int64) {
	t.Helper()
	raw, ok := beat["release_order"].(map[string]any)
	if !ok {
		t.Fatalf("heartbeat carries no release_order: %v", beat)
	}
	releaseID, _ = raw["release_id"].(string)
	mac, _ = raw["mac"].(string)
	at, _ := raw["ordered_at"].(float64)
	return releaseID, mac, int64(at)
}

// receiptFor computes what the terminal would, from the key it holds.
func receiptFor(t *testing.T, key, serial, releaseID string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(key))
	receipt, err := database.ComputeReleaseReceipt(hex.EncodeToString(sum[:]), serial, releaseID)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(receipt)
}

func (f *releaseFixture) confirm(t *testing.T, releaseID, receipt string, report any) response {
	t.Helper()
	body := map[string]any{"release_id": releaseID, "receipt": receipt}
	if report != nil {
		body["report"] = report
	}
	return f.env.do(http.MethodPost, "/api/v1/devices/release/confirm", body, deviceAuth(f.key))
}

func releaseRow(t *testing.T, serial string) (state, confirmedBy string, deleted bool) {
	t.Helper()
	var s, by *string
	var del *time.Time
	mustScan(t, `SELECT release_state, release_confirmed_by, deleted_at FROM devices
	              WHERE serial_number = '`+serial+`' ORDER BY id LIMIT 1`, &s, &by, &del)
	if s != nil {
		state = *s
	}
	if by != nil {
		confirmedBy = *by
	}
	return state, confirmedBy, del != nil
}

func releaseAuditCount(t *testing.T, slug, action string) int {
	t.Helper()
	return queryInt(t, `SELECT count(*) FROM audit_events a
	                     JOIN companies c ON c.id = a.company_id
	                    WHERE a.action = '`+action+`' AND c.slug = '`+slug+`'`)
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

func TestReleaseOrderIsIdempotentAndKeepsTheRowLive(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-ORDER")

	status, first := f.order(t, "moving to the new branch")
	if status != http.StatusOK {
		t.Fatalf("order = %d: %v", status, first)
	}
	if first["state"] != database.ReleaseStateOrdered {
		t.Errorf("state = %v, want ORDERED", first["state"])
	}
	if first["order_verifiable"] != true {
		t.Error("an order on a credentialed row is not verifiable")
	}
	// No capabilities have been reported yet, so the console must not offer
	// the automated flow.
	if first["terminal_capable"] != false {
		t.Errorf("terminal_capable = %v before any capability was reported", first["terminal_capable"])
	}

	// A retry returns the SAME order and audits nothing new.
	status, second := f.order(t, "retried")
	if status != http.StatusOK || second["release_id"] != first["release_id"] {
		t.Errorf("a second order minted a new release: %d %v vs %v", status, second, first)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_ORDERED"); n != 1 {
		t.Errorf("ORDERED audited %d times, want 1", n)
	}

	// The row is live and the credential still authenticates -- the terminal
	// has to be able to fetch the order.
	if state, _, deleted := releaseRow(t, f.serial); state != "ORDERED" || deleted {
		t.Errorf("row after order: state=%s deleted=%v", state, deleted)
	}
	beat := f.heartbeat(t, []string{"terminal_announce", database.CapabilityTerminalRelease})
	if _, mac, _ := orderFromHeartbeat(t, beat); len(mac) != 64 {
		t.Errorf("heartbeat mac = %q, want 64 hex", mac)
	}

	// And now the console can see the terminal is capable.
	if got := f.readRelease(t); got["terminal_capable"] != true {
		t.Errorf("terminal_capable after a capable heartbeat = %v", got["terminal_capable"])
	}

	// The fleet list and the detail carry the summary.
	status, detail := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals/"+f.serial, "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("detail = %d: %v", status, detail)
	}
	release, _ := detail["release"].(map[string]any)
	if release["state"] != database.ReleaseStateOrdered {
		t.Errorf("detail.release = %v, want ORDERED", detail["release"])
	}
}

func TestReleaseRequiresAnAdministrator(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-ROLE")
	_, mgrToken, mgrCSRF := consoleOperatorSession(t, f.env.router, f.companyID,
		"rel-manager@example.com", models.RoleManager)

	status, _ := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminals/"+f.serial+"/release", "{}", mgrToken, mgrCSRF)
	if status != http.StatusForbidden {
		t.Errorf("MANAGER ordering a release = %d, want 403", status)
	}
	if state, _, _ := releaseRow(t, f.serial); state != "" {
		t.Error("a refused order changed the row")
	}

	// Another company's administrator cannot reach it at all.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-other@example.com", models.RoleAdmin)
	status, _ = consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminals/"+f.serial+"/release", "{}", otherToken, otherCSRF)
	if status != http.StatusNotFound {
		t.Errorf("another company ordering a release = %d, want 404", status)
	}
}

// ---------------------------------------------------------------------------
// The terminal executes the order
// ---------------------------------------------------------------------------

func TestConfirmFinalizesAndTheNextOwnerIsGatedUntilReady(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-XFER")
	f.env.createMember(f.env.siteAKey, "A-001", "Old Member")

	if status, body := f.order(t, "sold"); status != http.StatusOK {
		t.Fatalf("order = %d: %v", status, body)
	}
	releaseID, _, _ := orderFromHeartbeat(t, f.heartbeat(t, nil))

	// The terminal wipes and confirms, with its report.
	report := map[string]any{"members": 1, "templates_sensor": 1, "events_flushed": 0}
	res := f.confirm(t, releaseID, receiptFor(t, f.key, f.serial, releaseID), report)
	if res.Code != http.StatusOK || res.Body["released"] != true {
		t.Fatalf("confirm = %d: %s", res.Code, res.Raw)
	}

	// Finalized: deleted, TERMINAL, credential dead, audited into Company One
	// with the report, work cancelled.
	if state, by, deleted := releaseRow(t, f.serial); state != "RELEASED" || by != "TERMINAL" || !deleted {
		t.Errorf("row after confirm: state=%s by=%s deleted=%v", state, by, deleted)
	}
	if again := f.env.do(http.MethodPost, "/api/v1/devices/heartbeat", nil, deviceAuth(f.key)); again.Code != http.StatusUnauthorized {
		t.Errorf("the old credential still authenticates: %d", again.Code)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_CONFIRMED"); n != 1 {
		t.Errorf("CONFIRMED audited %d times in Company One, want 1", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events
	                      WHERE action = 'TERMINAL_RELEASE_CONFIRMED'
	                        AND changes->'report'->>'members' = '1'`); n != 1 {
		t.Error("the wipe report did not reach the audit line")
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	                      WHERE d.serial_number = 'AT-REL-XFER' AND j.status IN ('PENDING','FAILED')`); n != 0 {
		t.Errorf("%d jobs still queued for a released row", n)
	}

	// A confirm that arrives again -- lost response -- is a 401 now, which the
	// terminal reads as "already released". Nothing else could be said: the
	// credential no longer resolves.
	if res := f.confirm(t, releaseID, receiptFor(t, f.key, f.serial, releaseID), nil); res.Code != http.StatusUnauthorized {
		t.Errorf("a repeated confirm = %d, want 401", res.Code)
	}

	// Company Two adopts, and the serial is NEW to it.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-two@example.com", models.RoleAdmin)
	code, token := f.announce(t, f.serial)
	status, adopted := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusOK || adopted["verdict"] != database.VerdictNew {
		t.Fatalf("adoption after release = %d %v", status, adopted)
	}
	id, _ := adopted["id"].(string)
	if status, body := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/approve",
		`{"site_id":"`+sitePublicIDByName(t, "Site C")+`","device_name":"New Door"}`,
		otherToken, otherCSRF); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}
	collected := f.poll(t, token)
	newKey, _ := collected.Body["api_key"].(string)
	if newKey == "" {
		t.Fatalf("collection: %s", collected.Raw)
	}

	// The new row is a NEW row, seeded with a snapshot, and SETTING_UP.
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-REL-XFER'`); n != 2 {
		t.Errorf("%d rows for the serial, want 2 (released + new)", n)
	}
	status, detail := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals/"+f.serial, "", otherToken, otherCSRF)
	if status != http.StatusOK {
		t.Fatalf("detail in Company Two = %d: %v", status, detail)
	}
	readiness, _ := detail["readiness"].(map[string]any)
	if readiness["state"] != models.ReadinessSettingUp {
		t.Errorf("readiness after collection = %v, want SETTING_UP", detail["readiness"])
	}
	if detail["release"] != nil {
		t.Errorf("the new row carries a release summary: %v", detail["release"])
	}

	jobs := f.env.jobs(newKey)
	snapshots := jobsOfType(jobs, "FULL_SYNC")
	if len(snapshots) != 1 {
		t.Fatalf("new row seeded with %v, want exactly one FULL_SYNC", jobTypes(jobs))
	}
	// The snapshot names Company Two's roster, not Company One's member.
	payload, _ := snapshots[0]["payload"].(map[string]any)
	if ids, _ := payload["member_ids"].([]any); len(ids) != 0 {
		t.Errorf("snapshot for Company Two carries members %v", ids)
	}

	// Acknowledging the snapshot is what makes it READY, and is audited.
	ack := f.env.do(http.MethodPost, "/api/v1/devices/jobs/"+itoa(jobID(t, snapshots[0]))+"/complete",
		map[string]any{"status": "COMPLETED"}, deviceAuth(newKey))
	if ack.Code != http.StatusOK {
		t.Fatalf("ack = %d: %s", ack.Code, ack.Raw)
	}
	_, detail = consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals/"+f.serial, "", otherToken, otherCSRF)
	readiness, _ = detail["readiness"].(map[string]any)
	if readiness["state"] != models.ReadinessReady {
		t.Errorf("readiness after the snapshot ack = %v, want READY", detail["readiness"])
	}
	if n := releaseAuditCount(t, "two", "TERMINAL_READY"); n != 1 {
		t.Errorf("READY audited %d times in Company Two, want 1", n)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_READY"); n != 0 {
		t.Error("Company One's trail carries the new owner's readiness")
	}

	// Company One's member never reached the new owner.
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	                      WHERE d.serial_number = 'AT-REL-XFER' AND d.deleted_at IS NULL
	                        AND j.entity_external_id = 'A-001'`); n != 0 {
		t.Error("the previous owner's member was queued for the new owner")
	}
}

func TestConfirmWithABadReceiptChangesNothing(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-BAD")
	f.order(t, "")
	releaseID, _, _ := orderFromHeartbeat(t, f.heartbeat(t, nil))

	wrong := receiptFor(t, "some-other-key", f.serial, releaseID)
	if res := f.confirm(t, releaseID, wrong, nil); res.Code != http.StatusConflict {
		t.Errorf("wrong receipt = %d, want 409: %s", res.Code, res.Raw)
	}
	if res := f.confirm(t, "0f5b1e7c-9a2d-4c3e-8f10-5a6b7c8d9e0f",
		receiptFor(t, f.key, f.serial, "0f5b1e7c-9a2d-4c3e-8f10-5a6b7c8d9e0f"), nil); res.Code != http.StatusNotFound {
		t.Errorf("receipt for a different order = %d, want 404: %s", res.Code, res.Raw)
	}
	if res := f.confirm(t, releaseID, "zz", nil); res.Code != http.StatusBadRequest {
		t.Errorf("non-hex receipt = %d, want 400", res.Code)
	}

	if state, _, deleted := releaseRow(t, f.serial); state != "ORDERED" || deleted {
		t.Errorf("row after refused confirms: state=%s deleted=%v", state, deleted)
	}
	// Still authenticating: the terminal can try again.
	f.heartbeat(t, nil)
}

func TestAnnounceWithAReceiptFinalizesBeforeItAnnounces(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-ANN")
	f.order(t, "")
	releaseID, _, _ := orderFromHeartbeat(t, f.heartbeat(t, nil))
	receipt := receiptFor(t, f.key, f.serial, releaseID)

	// The terminal wiped, cleared its key, rebooted, and announces carrying
	// the receipt -- the path a unit takes when its confirm never got through.
	res := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":   f.serial,
		"release_receipt": map[string]any{"release_id": releaseID, "receipt": receipt},
	}, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("announce with receipt = %d: %s", res.Code, res.Raw)
	}
	if res.Body["receipt_status"] != database.ReceiptStatusConsumed {
		t.Errorf("receipt_status = %v, want CONSUMED", res.Body["receipt_status"])
	}
	code, _ := res.Body["pairing_code"].(string)
	if code == "" {
		t.Fatal("the announce that finalized the release produced no pairing code")
	}
	if state, by, deleted := releaseRow(t, f.serial); state != "RELEASED" || by != "TERMINAL" || !deleted {
		t.Errorf("row after announce receipt: state=%s by=%s deleted=%v", state, by, deleted)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_CONFIRMED"); n != 1 {
		t.Errorf("CONFIRMED audited %d times, want 1", n)
	}
	// The announcement this call created survived the voiding of in-flight
	// rows: it is PENDING and adoptable.
	if n := queryInt(t, `SELECT count(*) FROM terminal_announcements
	                      WHERE serial_number = 'AT-REL-ANN' AND state = 'PENDING'`); n != 1 {
		t.Error("the fresh announcement was voided by its own receipt")
	}

	// Re-announcing with the same receipt (response lost) is CONSUMED again,
	// audits nothing new, and keeps the code.
	token, _ := res.Body["announce_token"].(string)
	again := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":   f.serial,
		"release_receipt": map[string]any{"release_id": releaseID, "receipt": receipt},
	}, announceHeader(token))
	if again.Code != http.StatusOK || again.Body["receipt_status"] != database.ReceiptStatusConsumed {
		t.Errorf("repeated receipt = %d %v", again.Code, again.Body["receipt_status"])
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_CONFIRMED"); n != 1 {
		t.Errorf("a repeated receipt audited again: %d", n)
	}

	// A garbage receipt is UNKNOWN and the announce still works.
	garbage := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":   "AT-REL-NOISE",
		"release_receipt": map[string]any{"release_id": releaseID, "receipt": receipt},
	}, nil)
	if garbage.Code != http.StatusCreated || garbage.Body["receipt_status"] != database.ReceiptStatusUnknown {
		t.Errorf("announce with a receipt for another serial = %d %v", garbage.Code, garbage.Body["receipt_status"])
	}

	// And Company Two adopts it.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-ann-two@example.com", models.RoleAdmin)
	status, adopted := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusOK || adopted["verdict"] != database.VerdictNew {
		t.Errorf("adoption after an announce receipt = %d %v", status, adopted)
	}
}

// ---------------------------------------------------------------------------
// Gating
// ---------------------------------------------------------------------------

func TestAdoptionIsRefusedWhileAReleaseIsOrdered(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-GATE")
	f.order(t, "")

	// The terminal has NOT wiped (no receipt), but somebody typed `clear key`
	// and it announces. Nobody may adopt it yet.
	code, _ := f.announce(t, f.serial)

	// Not its own company: the remedy is theirs.
	status, body := f.adopt(t, code)
	if status != http.StatusConflict || body["code"] != "RELEASE_IN_PROGRESS" {
		t.Errorf("own-company adoption during a release = %d %v, want 409 RELEASE_IN_PROGRESS", status, body)
	}

	// Not another company: and they learn only the uniform refusal.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-gate-two@example.com", models.RoleAdmin)
	status, body = consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusConflict || body["code"] != "TERMINAL_OWNED_ELSEWHERE" {
		t.Errorf("other-company adoption during a release = %d %v, want 409 TERMINAL_OWNED_ELSEWHERE", status, body)
	}

	// Nothing was written for either attempt.
	if n := queryInt(t, `SELECT count(*) FROM terminal_announcements
	                      WHERE serial_number = 'AT-REL-GATE' AND state = 'ADOPTED'`); n != 0 {
		t.Error("a refused adoption left the announcement adopted")
	}
}

func TestOrderedTerminalReceivesNoRosterWork(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-SYNC")
	f.env.createMember(f.env.siteAKey, "A-100", "Before")
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	                      WHERE d.serial_number = 'AT-REL-SYNC' AND j.status = 'PENDING'`); n == 0 {
		t.Fatal("fixture: no work queued before the order")
	}

	f.order(t, "")

	// The backlog was cancelled by the order.
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	                      WHERE d.serial_number = 'AT-REL-SYNC' AND j.status = 'PENDING'`); n != 0 {
		t.Errorf("%d jobs still pending after the order", n)
	}

	// Neither a new person nor the reconciler queues anything for it.
	f.env.createMember(f.env.siteAKey, "A-101", "After")
	var deviceID int64
	mustScan(t, `SELECT id FROM devices WHERE serial_number = 'AT-REL-SYNC' AND deleted_at IS NULL`, &deviceID)
	added, removed, err := database.ReconcileDeviceRoster(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || removed != 0 {
		t.Errorf("reconciler queued %d/%d for an ordered terminal", added, removed)
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs j JOIN devices d ON d.id = j.device_id
	                      WHERE d.serial_number = 'AT-REL-SYNC' AND j.status = 'PENDING'`); n != 0 {
		t.Errorf("%d jobs queued for an ordered terminal", n)
	}
}

// ---------------------------------------------------------------------------
// Cancel and force
// ---------------------------------------------------------------------------

func TestCancelRestoresTheTerminalAndQueuesASnapshot(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-CANCEL")
	f.env.createMember(f.env.siteAKey, "A-200", "Kept")
	f.order(t, "")

	status, body := consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/terminals/"+f.serial+"/release", "", f.token, f.csrf)
	if status != http.StatusOK || body["state"] != "" {
		t.Fatalf("cancel = %d: %v", status, body)
	}
	if state, _, deleted := releaseRow(t, f.serial); state != "" || deleted {
		t.Errorf("row after cancel: state=%q deleted=%v", state, deleted)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_CANCELLED"); n != 1 {
		t.Errorf("CANCELLED audited %d times", n)
	}

	// The terminal converges: a snapshot naming the company's roster.
	jobs := f.env.jobs(f.key)
	if len(jobsOfType(jobs, "FULL_SYNC")) != 1 {
		t.Errorf("jobs after cancel = %v, want a FULL_SYNC", jobTypes(jobs))
	}
	if !contains(jobTypes(jobs), "CREATE") {
		t.Errorf("jobs after cancel = %v, want the roster records", jobTypes(jobs))
	}

	// The heartbeat no longer carries an order, and a second cancel is 409.
	if beat := f.heartbeat(t, nil); beat["release_order"] != nil {
		t.Errorf("heartbeat after cancel still carries %v", beat["release_order"])
	}
	status, body = consoleCall(t, f.env.router, http.MethodDelete,
		"/api/v1/console/terminals/"+f.serial+"/release", "", f.token, f.csrf)
	if status != http.StatusConflict || body["code"] != "RELEASE_NOT_ORDERED" {
		t.Errorf("second cancel = %d %v, want 409 RELEASE_NOT_ORDERED", status, body)
	}
}

func TestForceRequiresAnOrderAndAnAttestation(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-FORCE")

	force := func(body string) (int, map[string]any) {
		return consoleCall(t, f.env.router, http.MethodPost,
			"/api/v1/console/terminals/"+f.serial+"/release/force", body, f.token, f.csrf)
	}

	if status, body := force(`{"attest":true}`); status != http.StatusConflict || body["code"] != "RELEASE_NOT_ORDERED" {
		t.Errorf("force with nothing ordered = %d %v, want 409", status, body)
	}

	f.order(t, "unit is in a box")

	if status, body := force(`{"attest":false,"reason":"x"}`); status != http.StatusBadRequest || body["code"] != "ATTESTATION_REQUIRED" {
		t.Errorf("force without attestation = %d %v, want 400", status, body)
	}
	if state, _, deleted := releaseRow(t, f.serial); state != "ORDERED" || deleted {
		t.Error("a refused force changed the row")
	}

	status, body := force(`{"attest":true,"reason":"unit is in a box"}`)
	if status != http.StatusOK || body["released"] != true || body["confirmed_by"] != "OPERATOR" {
		t.Fatalf("force = %d: %v", status, body)
	}
	if state, by, deleted := releaseRow(t, f.serial); state != "RELEASED" || by != "OPERATOR" || !deleted {
		t.Errorf("row after force: state=%s by=%s deleted=%v", state, by, deleted)
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASE_FORCED"); n != 1 {
		t.Errorf("FORCED audited %d times", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events
	                      WHERE action = 'TERMINAL_RELEASE_FORCED' AND (changes->>'attested')::boolean`); n != 1 {
		t.Error("the attestation was not recorded with the force")
	}

	// The old credential is dead...
	if res := f.env.do(http.MethodPost, "/api/v1/devices/heartbeat", nil, deviceAuth(f.key)); res.Code != http.StatusUnauthorized {
		t.Errorf("credential after force = %d, want 401", res.Code)
	}

	// ...and the terminal, when it reconnects and sees that 401, can fetch the
	// order by serial and verify it with the key it still holds.
	res := f.env.do(http.MethodGet, "/api/v1/devices/release-order?serial="+f.serial, nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("release order by serial after force = %d: %s", res.Code, res.Raw)
	}
	order, _ := res.Body["release_order"].(map[string]any)
	if order["serial_number"] != f.serial || order["release_id"] != body["release_id"] {
		t.Errorf("by-serial order = %v, want the forced release", order)
	}
	mac, _ := order["mac"].(string)
	at, _ := order["ordered_at"].(float64)
	sum := sha256.Sum256([]byte(f.key))
	expected, err := database.ComputeReleaseOrderMAC(hex.EncodeToString(sum[:]), f.serial,
		order["release_id"].(string), time.Unix(int64(at), 0))
	if err != nil || hex.EncodeToString(expected) != mac {
		t.Errorf("the by-serial order does not verify with the terminal's key: %v", err)
	}

	// The serial is free.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-force-two@example.com", models.RoleAdmin)
	code, _ := f.announce(t, f.serial)
	status, adopted := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusOK || adopted["verdict"] != database.VerdictNew {
		t.Errorf("adoption after force = %d %v", status, adopted)
	}
}

func TestReleaseOrderBySerialDisclosesLittle(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-FETCH")

	get := func(query string) response {
		return f.env.do(http.MethodGet, "/api/v1/devices/release-order"+query, nil, nil)
	}
	if res := get(""); res.Code != http.StatusBadRequest {
		t.Errorf("no serial = %d, want 400", res.Code)
	}
	if res := get("?serial=" + f.serial); res.Code != http.StatusNoContent {
		t.Errorf("no order = %d, want 204", res.Code)
	}
	if res := get("?serial=AT-NOBODY"); res.Code != http.StatusNoContent {
		t.Errorf("unknown serial = %d, want 204 (same as no order)", res.Code)
	}

	_, ordered := f.order(t, "")
	res := get("?serial=" + f.serial)
	if res.Code != http.StatusOK {
		t.Fatalf("ordered = %d: %s", res.Code, res.Raw)
	}
	order, _ := res.Body["release_order"].(map[string]any)
	if order["release_id"] != ordered["release_id"] {
		t.Errorf("by-serial order = %v, want %v", order["release_id"], ordered["release_id"])
	}
	for _, forbidden := range []string{"api_key", "api_key_hash", "company", "site"} {
		if _, present := order[forbidden]; present {
			t.Errorf("by-serial order discloses %s", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// Platform route keeps its shape, now order-then-force
// ---------------------------------------------------------------------------

func TestPlatformReleaseIsOrderThenForce(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-PLAT")
	mustCreatePlatformAdmin(t, "rel-platform@example.com")
	pToken, pCSRF := platformLogin(t, f.env.router, "rel-platform@example.com", testPlatformPassword)

	status, released := platformCall(t, f.env.router, http.MethodPost,
		"/api/v1/platform/terminals/"+f.serial+"/release", `{"reason":"resold"}`, pToken, pCSRF)
	if status != http.StatusOK || released["released"] != true {
		t.Fatalf("platform release = %d: %v", status, released)
	}
	if state, by, deleted := releaseRow(t, f.serial); state != "RELEASED" || by != "PLATFORM" || !deleted {
		t.Errorf("row after platform release: state=%s by=%s deleted=%v", state, by, deleted)
	}
	// An order was minted on the way, so the unit can still verify a release
	// by serial when it next connects.
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-REL-PLAT'
	                        AND release_order_mac IS NOT NULL`); n != 1 {
		t.Error("the platform release left no verifiable order behind")
	}
	if n := releaseAuditCount(t, "one", "TERMINAL_RELEASED"); n != 1 {
		t.Errorf("TERMINAL_RELEASED audited %d times", n)
	}
}

// ---------------------------------------------------------------------------
// Event attribution
// ---------------------------------------------------------------------------

func TestQueuedEventsFromThePreviousOwnerAreRefused(t *testing.T) {
	f := newReleaseFixture(t, "AT-REL-EVT")
	f.order(t, "")
	releaseID, _, _ := orderFromHeartbeat(t, f.heartbeat(t, nil))
	if res := f.confirm(t, releaseID, receiptFor(t, f.key, f.serial, releaseID), nil); res.Code != http.StatusOK {
		t.Fatalf("confirm = %d", res.Code)
	}

	// Company Two takes it.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rel-evt-two@example.com", models.RoleAdmin)
	code, token := f.announce(t, f.serial)
	_, adopted := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	id, _ := adopted["id"].(string)
	consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/approve",
		`{"site_id":"`+sitePublicIDByName(t, "Site C")+`","device_name":"Door"}`, otherToken, otherCSRF)
	newKey, _ := f.poll(t, token).Body["api_key"].(string)
	if newKey == "" {
		t.Fatal("no key collected")
	}

	upload := func(eventID string, occurredAt string) response {
		body := map[string]any{"event_id": eventID, "member_id": "A-OLD", "granted": true,
			"source": "FINGERPRINT"}
		if occurredAt != "" {
			body["occurred_at"] = occurredAt
		}
		return f.env.do(http.MethodPost, "/api/v1/devices/access/log", body, deviceAuth(newKey))
	}

	// An event from an hour before the new row existed: refused, not stored.
	stale := upload("11111111-1111-1111-1111-111111111111",
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	if stale.Code != http.StatusOK || stale.Body["recorded"] != false || stale.Body["refused"] == nil {
		t.Errorf("stale event = %d %s", stale.Code, stale.Raw)
	}
	if n := queryInt(t, `SELECT count(*) FROM access_logs WHERE public_id = '11111111-1111-1111-1111-111111111111'`); n != 0 {
		t.Error("a pre-registration event was stored for the new owner")
	}
	if n := releaseAuditCount(t, "two", "EVENTS_REFUSED_PRE_REGISTRATION"); n != 1 {
		t.Errorf("refusal audited %d times in Company Two", n)
	}

	// A current event, and a clockless one, are recorded.
	fresh := upload("22222222-2222-2222-2222-222222222222", time.Now().UTC().Format(time.RFC3339))
	if fresh.Code != http.StatusOK || fresh.Body["recorded"] != true {
		t.Errorf("current event = %d %s", fresh.Code, fresh.Raw)
	}
	clockless := upload("33333333-3333-3333-3333-333333333333", "")
	if clockless.Code != http.StatusOK || clockless.Body["recorded"] != true {
		t.Errorf("clockless event = %d %s", clockless.Code, clockless.Raw)
	}

	// Company One's trail is untouched by any of it.
	if n := queryInt(t, `SELECT count(*) FROM access_logs al JOIN companies c ON c.id = al.company_id
	                      WHERE c.slug = 'one' AND al.public_id::text IN
	                        ('11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222')`); n != 0 {
		t.Error("an event uploaded under Company Two's credential reached Company One")
	}
}

// ---------------------------------------------------------------------------
// First-time adoption is unchanged in shape, plus readiness
// ---------------------------------------------------------------------------

func TestFirstTimeAdoptionSeedsASnapshotAndReadsSettingUp(t *testing.T) {
	f := newAnnounceFixture(t)
	f.env.createMember(f.env.siteAKey, "F-001", "First")
	key := f.setUp(t, "AT-FIRST-READY", "Site A", "Lobby")

	jobs := f.env.jobs(key)
	types := jobTypes(jobs)
	if len(jobsOfType(jobs, "FULL_SYNC")) != 1 || !contains(types, "CREATE") || !contains(types, "SETTINGS") {
		t.Errorf("first-time seeding = %v, want FULL_SYNC + CREATE + SETTINGS", types)
	}
	// The snapshot arrives BEFORE the records, so a unit applies "hold
	// exactly these" first.
	if jobs[0]["job_type"] != "FULL_SYNC" {
		t.Errorf("first job = %v, want FULL_SYNC", jobs[0]["job_type"])
	}

	status, detail := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals/AT-FIRST-READY", "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("detail = %d: %v", status, detail)
	}
	readiness, _ := detail["readiness"].(map[string]any)
	if readiness["state"] != models.ReadinessSettingUp {
		t.Errorf("readiness = %v, want SETTING_UP", detail["readiness"])
	}

	// The list read carries readiness too.
	status, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminals", "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	terminals, _ := list["terminals"].([]any)
	if len(terminals) != 1 {
		t.Fatalf("list = %v", list)
	}
	row, _ := terminals[0].(map[string]any)
	rowReadiness, _ := row["readiness"].(map[string]any)
	if rowReadiness["state"] != models.ReadinessSettingUp {
		raw, _ := json.Marshal(row)
		t.Errorf("list row readiness = %v: %s", row["readiness"], truncate(string(raw), 300))
	}
}

