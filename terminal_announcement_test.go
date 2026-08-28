package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Announce and approve (022).
//
// A terminal introduces itself, displays a pairing code, and an authenticated
// administrator adopts and approves it. Almost every test here is about a
// security property rather than a feature, for the same reason the claim-code
// suite is: one endpoint is UNAUTHENTICATED and another hands over a device
// credential, so what has to hold is a set of invariants rather than a happy
// path.
//
// The load-bearing ones, each with a test whose failure would be a real defect:
//
//   - announcing grants nothing, and an un-adopted announcement is invisible
//   - a serial owned by another company is refused at adopt, at approve, and at
//     collect -- three separate checks, because minutes pass between them
//   - the credential is minted once and only for the token that created the
//     announcement
//   - no response anywhere carries a pairing code back, a token back, or a key
//     to anybody but the terminal
//   - claim codes still work exactly as they did

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

type announceFixture struct {
	env       *testEnv
	companyID int64
	token     string
	csrf      string
}

func newAnnounceFixture(t *testing.T) *announceFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"announce-admin@example.com", models.RoleAdmin)
	return &announceFixture{env: env, companyID: companyID, token: token, csrf: csrf}
}

// announce posts an announcement exactly as a terminal with no credential does.
func (f *announceFixture) announce(t *testing.T, serial string) (string, string) {
	t.Helper()
	res := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":     serial,
		"firmware_version":  "1.4.0",
		"hardware_revision": "rev-C",
	}, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("announcing %s = %d: %s", serial, res.Code, res.Raw)
	}

	code, _ := res.Body["pairing_code"].(string)
	token, _ := res.Body["announce_token"].(string)
	if code == "" || token == "" {
		t.Fatalf("announce returned no code/token: %s", res.Raw)
	}
	return code, token
}

// announceHeader is the header a terminal presents its token in.
func announceHeader(token string) map[string]string {
	return map[string]string{"X-Announce-Token": token}
}

// poll reads the announcement status as the terminal does.
func (f *announceFixture) poll(t *testing.T, token string) response {
	t.Helper()
	return f.env.do(http.MethodGet, "/api/v1/devices/announce", nil, announceHeader(token))
}

// adopt types a pairing code into the console.
func (f *announceFixture) adopt(t *testing.T, code string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, f.token, f.csrf)
}

// approve places an adopted terminal at a site.
func (f *announceFixture) approve(t *testing.T, id, siteName, deviceName string) (int, map[string]any) {
	t.Helper()
	return consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/approve",
		`{"site_id":"`+sitePublicIDByName(t, siteName)+`","device_name":"`+deviceName+`"}`,
		f.token, f.csrf)
}

// setUp runs the whole customer journey and returns the collected credential.
func (f *announceFixture) setUp(t *testing.T, serial, siteName, deviceName string) string {
	t.Helper()
	code, token := f.announce(t, serial)

	status, body := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopting = %d: %v", status, body)
	}
	id, _ := body["id"].(string)

	if status, body := f.approve(t, id, siteName, deviceName); status != http.StatusOK {
		t.Fatalf("approving = %d: %v", status, body)
	}

	res := f.poll(t, token)
	key, _ := res.Body["api_key"].(string)
	if key == "" {
		t.Fatalf("collection returned no api_key: %s", res.Raw)
	}
	return key
}

// ---------------------------------------------------------------------------
// The journey
// ---------------------------------------------------------------------------

// TestAnnounceApproveCollect is the whole customer flow, and it asserts the
// response shapes both halves of the product depend on.
func TestAnnounceApproveCollect(t *testing.T) {
	f := newAnnounceFixture(t)

	// 1. The terminal announces. It has no credential and presents none.
	res := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number":     "AT-A1B2C3",
		"firmware_version":  "1.4.0",
		"hardware_revision": "rev-C",
	}, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("announce = %d: %s", res.Code, res.Raw)
	}

	code, _ := res.Body["pairing_code"].(string)
	token, _ := res.Body["announce_token"].(string)

	// THE CODE SHAPE IS A CONTRACT with a 16x2 panel and with somebody typing it
	// into a browser: two groups of four, from an alphabet with no character
	// that is misread off a screen.
	if len(code) != 9 || code[4] != '-' {
		t.Errorf("pairing code %q is not XXXX-XXXX", code)
	}
	for _, ch := range code {
		if ch == '-' {
			continue
		}
		if strings.ContainsRune("ILOU01", ch) {
			t.Errorf("pairing code %q contains %q, which is misread off a panel", code, ch)
		}
	}

	// 2. Nothing exists yet. No device, no company, nothing an operator can see.
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-A1B2C3'`); n != 0 {
		t.Errorf("announcing created %d device rows; it must create none", n)
	}
	if n := queryInt(t,
		`SELECT count(*) FROM terminal_announcements WHERE company_id IS NOT NULL`); n != 0 {
		t.Errorf("announcing attached the row to %d companies; it must attach to none", n)
	}

	// 3. The terminal polls and is told to keep showing its code.
	if res := f.poll(t, token); res.Body["state"] != "ANNOUNCED" {
		t.Errorf("state before adoption = %v, want ANNOUNCED", res.Body["state"])
	}

	// 4. The operator types the code.
	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	if adopted["verdict"] != database.VerdictNew {
		t.Errorf("verdict = %v, want NEW for a serial nobody has", adopted["verdict"])
	}
	if adopted["serial_number"] != "AT-A1B2C3" {
		t.Errorf("adopt did not report the serial: %v", adopted)
	}

	// The terminal can now say somebody is dealing with it.
	if res := f.poll(t, token); res.Body["state"] != "ADOPTED" {
		t.Errorf("state after adoption = %v, want ADOPTED", res.Body["state"])
	}

	// 5. Approval. STILL NO CREDENTIAL: this records a decision.
	id, _ := adopted["id"].(string)
	status, approved := f.approve(t, id, "Site A", "Front Door")
	if status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, approved)
	}
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-A1B2C3'`); n != 0 {
		t.Errorf("approval created %d device rows; the credential is minted at collection", n)
	}

	// 6. The terminal collects, once.
	res = f.poll(t, token)
	if res.Code != http.StatusOK || res.Body["state"] != "APPROVED" {
		t.Fatalf("collection = %d %v: %s", res.Code, res.Body["state"], res.Raw)
	}

	key, _ := res.Body["api_key"].(string)
	// The key format the firmware refuses anything else for.
	if !strings.HasPrefix(key, "atd_") || len(key) != 68 || strings.ToLower(key) != key {
		t.Errorf("api_key = %q, want atd_ + 64 lower-case hex", key)
	}
	if res.Body["company_name"] != "Company One" {
		t.Errorf("company_name = %v, want the adopting company", res.Body["company_name"])
	}
	if res.Body["site_name"] != "Site A" {
		t.Errorf("site_name = %v, want the approved site", res.Body["site_name"])
	}
	if res.Body["device_name"] != "Front Door" {
		t.Errorf("device_name = %v, want the name the operator chose", res.Body["device_name"])
	}

	// 7. It is now an ordinary terminal: the credential authenticates, and the
	// row records how it got here.
	if settings := f.env.do(http.MethodGet, "/api/v1/devices/settings", nil,
		deviceAuth(key)); settings.Code != http.StatusOK {
		t.Errorf("the collected credential does not authenticate (got %d)", settings.Code)
	}
	if via := queryString(t,
		`SELECT provisioned_via FROM devices WHERE serial_number = 'AT-A1B2C3'`); via != "ANNOUNCEMENT" {
		t.Errorf("provisioned_via = %q, want ANNOUNCEMENT", via)
	}

	// 8. And it is out of the pending list, because it is a terminal now.
	status, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("listing pending = %d: %v", status, list)
	}
	if count, _ := list["count"].(float64); count != 0 {
		t.Errorf("a collected terminal is still in the pending list (count %v)", count)
	}
}

// TestAnnouncementAuditTrail proves every decision is recorded.
func TestAnnouncementAuditTrail(t *testing.T) {
	f := newAnnounceFixture(t)
	f.setUp(t, "AT-AUDIT", "Site A", "Audited Door")

	for _, action := range []string{"TERMINAL_ADOPTED", "TERMINAL_APPROVED",
		"TERMINAL_CREDENTIAL_COLLECTED"} {
		if n := queryInt(t,
			`SELECT count(*) FROM audit_events WHERE action = $1`, action); n != 1 {
			t.Errorf("%s appears %d times in the audit trail, want 1", action, n)
		}
	}

	// AND NO SECRET IN ANY OF THEM. The whole trail is searched rather than one
	// record, because the rule is about the table and not about one writer.
	if n := queryInt(t,
		`SELECT count(*) FROM audit_events WHERE changes::text LIKE '%atd_%'`); n != 0 {
		t.Error("a device credential was written into the audit trail")
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events
	                      WHERE changes::text ~ '[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}-[23456789ABCDEFGHJKMNPQRSTVWXYZ]{4}'`); n != 0 {
		t.Error("something shaped like a pairing code was written into the audit trail")
	}
}

// ---------------------------------------------------------------------------
// Nothing is granted by announcing
// ---------------------------------------------------------------------------

// TestUnadoptedAnnouncementIsInvisible is the property that makes an
// unauthenticated announce endpoint acceptable: an announcement nobody has
// adopted is visible to NO company, including one that goes looking.
func TestUnadoptedAnnouncementIsInvisible(t *testing.T) {
	f := newAnnounceFixture(t)
	f.announce(t, "AT-INVISIBLE")

	status, body := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("listing = %d: %v", status, body)
	}
	if count, _ := body["count"].(float64); count != 0 {
		t.Errorf("an un-adopted announcement is listed (count %v); it belongs to nobody", count)
	}
	if strings.Contains(strings.ToUpper(jsonBody(t, body)), "AT-INVISIBLE") {
		t.Error("an un-adopted serial was disclosed to a company that has not adopted it")
	}
}

// TestAnnouncementIsInvisibleToAnotherCompany covers the tenancy boundary once a
// company HAS adopted: the other tenant must not see it in a list or by id.
func TestAnnouncementIsInvisibleToAnotherCompany(t *testing.T) {
	f := newAnnounceFixture(t)

	code, _ := f.announce(t, "AT-TENANT")
	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	id, _ := adopted["id"].(string)

	// A different tenant entirely.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"other-admin@example.com", models.RoleAdmin)

	status, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", otherToken, otherCSRF)
	if count, _ := list["count"].(float64); status != http.StatusOK || count != 0 {
		t.Errorf("another tenant sees %v pending terminal(s): %v", count, list)
	}

	// 404 rather than 403 when naming it directly -- answering "forbidden" would
	// confirm the id exists in somebody else's account.
	status, _ = consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements/"+id, "", otherToken, otherCSRF)
	if status != http.StatusNotFound {
		t.Errorf("reading another tenant's announcement = %d, want 404", status)
	}

	// And they cannot act on it either.
	status, _ = consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/approve",
		`{"site_id":"`+sitePublicIDByName(t, "Site C")+`"}`, otherToken, otherCSRF)
	if status != http.StatusNotFound {
		t.Errorf("approving another tenant's announcement = %d, want 404", status)
	}
}

// TestPairingCodeIsNotReadableBack. Nothing gives a code back, ever.
func TestPairingCodeIsNotReadableBack(t *testing.T) {
	f := newAnnounceFixture(t)
	code, token := f.announce(t, "AT-ONCE-ONLY")

	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	id, _ := adopted["id"].(string)

	// Not in the adopt response, the detail read, the list, or the device poll.
	_, detail := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements/"+id, "", f.token, f.csrf)
	_, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", f.token, f.csrf)
	polled := f.poll(t, token)

	for name, body := range map[string]string{
		"adopt":  jsonBody(t, adopted),
		"detail": jsonBody(t, detail),
		"list":   jsonBody(t, list),
		"poll":   polled.Raw,
	} {
		if strings.Contains(body, code) {
			t.Errorf("the %s response carries the pairing code back", name)
		}
		if strings.Contains(body, token) {
			t.Errorf("the %s response carries the announce token back", name)
		}
	}

	// Stored hashed, never in the clear.
	if n := queryInt(t,
		`SELECT count(*) FROM terminal_announcements WHERE pairing_code_hash !~ '^[0-9a-f]{64}$'`); n != 0 {
		t.Error("a pairing code is stored as something other than a SHA-256 hash")
	}
}

// ---------------------------------------------------------------------------
// Cross-company refusal -- the rule that is never relaxed
// ---------------------------------------------------------------------------

// TestSerialOwnedByAnotherCompanyIsRefusedAtAdoption is the anti-hijack test.
//
// Company Two owns a terminal. Company One learns its serial -- which is not
// secret, it is derived from the MAC and printed at boot -- gets it to announce,
// and tries to adopt it. Refused, with nothing disclosed about who does own it.
func TestSerialOwnedByAnotherCompanyIsRefusedAtAdoption(t *testing.T) {
	f := newAnnounceFixture(t)

	// Company Two registers the terminal the ordinary way.
	f.env.registerDevice(f.env.siteCKey, "AT-THEIRS")

	code, _ := f.announce(t, "AT-THEIRS")
	status, body := f.adopt(t, code)

	if status != http.StatusConflict {
		t.Fatalf("adopting another company's serial = %d, want 409: %v", status, body)
	}
	if body["code"] != "TERMINAL_OWNED_ELSEWHERE" {
		t.Errorf("refusal code = %v, want TERMINAL_OWNED_ELSEWHERE", body["code"])
	}

	// NOTHING ABOUT THE OWNER. Not the company, not the site, not an operator.
	disclosure := strings.ToLower(jsonBody(t, body))
	for _, secret := range []string{"company two", "site c", "two"} {
		if strings.Contains(disclosure, secret) {
			t.Errorf("the refusal disclosed %q about the owning tenant: %v", secret, body)
		}
	}

	// And it did not half-adopt: the row still belongs to nobody.
	if n := queryInt(t, `SELECT count(*) FROM terminal_announcements
	                      WHERE serial_number = 'AT-THEIRS' AND company_id IS NOT NULL`); n != 0 {
		t.Error("a refused adoption still attached the announcement to the company")
	}

	// The victim's terminal is untouched and still authenticates.
	if n := queryInt(t, `SELECT count(*) FROM devices
	                      WHERE serial_number = 'AT-THEIRS' AND api_key_hash IS NOT NULL`); n != 1 {
		t.Error("the refused adoption disturbed the owning company's terminal")
	}
}

// TestSerialOwnedByAnotherCompanyIsRefusedAtApproval covers the SECOND check.
//
// The serial is unowned when the code is adopted and acquires an owner before
// approval -- exactly the window a single check at adoption would miss.
func TestSerialOwnedByAnotherCompanyIsRefusedAtApproval(t *testing.T) {
	f := newAnnounceFixture(t)

	code, _ := f.announce(t, "AT-RACE")
	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	if adopted["verdict"] != database.VerdictNew {
		t.Fatalf("verdict = %v, want NEW", adopted["verdict"])
	}
	id, _ := adopted["id"].(string)

	// Between adoption and approval, another tenant registers the serial.
	f.env.registerDevice(f.env.siteCKey, "AT-RACE")

	status, body := f.approve(t, id, "Site A", "Front Door")
	if status != http.StatusConflict {
		t.Fatalf("approving a serial that became another company's = %d, want 409: %v", status, body)
	}
	if body["code"] != "TERMINAL_OWNED_ELSEWHERE" {
		t.Errorf("refusal code = %v, want TERMINAL_OWNED_ELSEWHERE", body["code"])
	}
}

// TestSerialOwnedByAnotherCompanyIsRefusedAtCollection covers the THIRD check,
// the one enforced by registerDeviceTx itself.
//
// Approval succeeds against an unowned serial; the serial acquires an owner
// before the terminal comes to collect. The credential must not be issued, and
// the other company's terminal must not be re-keyed.
func TestSerialOwnedByAnotherCompanyIsRefusedAtCollection(t *testing.T) {
	f := newAnnounceFixture(t)

	code, token := f.announce(t, "AT-LATE")
	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	id, _ := adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Front Door"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	// The window: another tenant takes the serial before the unit collects.
	victimKey := f.env.registerDevice(f.env.siteCKey, "AT-LATE")

	res := f.poll(t, token)
	if res.Body["state"] != "REFUSED" {
		t.Fatalf("collection against a hijacked serial = %v, want REFUSED: %s",
			res.Body["state"], res.Raw)
	}
	if strings.Contains(res.Raw, "atd_") {
		t.Fatal("a credential was issued for a serial owned by another company")
	}

	// THE VICTIM IS UNHARMED. registerDeviceTx writes a key hash before it
	// checks the site, so a caller that failed to roll back would have silently
	// re-keyed somebody else's door.
	if settings := f.env.do(http.MethodGet, "/api/v1/devices/settings", nil,
		deviceAuth(victimKey)); settings.Code != http.StatusOK {
		t.Errorf("the other company's terminal was re-keyed by a refused collection (got %d)",
			settings.Code)
	}
}

// TestDisabledTerminalIsRefusedWithItsOwnRemedy.
//
// The same company's own terminal, taken out of service. Named distinctly from
// the cross-company refusal because the fix is theirs and is one click.
func TestDisabledTerminalIsRefusedWithItsOwnRemedy(t *testing.T) {
	f := newAnnounceFixture(t)

	f.env.registerDevice(f.env.siteAKey, "AT-OFF")
	if status, body := consoleCall(t, f.env.router, http.MethodPut,
		"/api/v1/console/terminals/AT-OFF/state", `{"disabled":true}`,
		f.token, f.csrf); status != http.StatusOK {
		t.Fatalf("disabling = %d: %v", status, body)
	}

	code, _ := f.announce(t, "AT-OFF")
	status, body := f.adopt(t, code)
	if status != http.StatusConflict {
		t.Fatalf("adopting a disabled terminal = %d, want 409: %v", status, body)
	}
	if body["code"] != "TERMINAL_DISABLED" {
		t.Errorf("refusal code = %v, want TERMINAL_DISABLED", body["code"])
	}
}

// TestReprovisionRotatesTheExistingCredential.
//
// A factory-reset terminal announces again. Its own company adopts it, is warned
// that this is a re-provision, and approving rotates the key -- the old one must
// stop working, because that is the recovery path's whole point.
func TestReprovisionRotatesTheExistingCredential(t *testing.T) {
	f := newAnnounceFixture(t)

	oldKey := f.env.registerDevice(f.env.siteAKey, "AT-RESET")

	code, token := f.announce(t, "AT-RESET")
	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	if adopted["verdict"] != database.VerdictReprovision {
		t.Fatalf("verdict = %v, want RE_PROVISION for our own existing serial",
			adopted["verdict"])
	}

	// The console is given what it needs to warn with: the terminal that is
	// about to be affected, by name and site.
	existing, ok := adopted["existing_terminal"].(map[string]any)
	if !ok {
		t.Fatalf("RE_PROVISION carried no existing_terminal to warn about: %v", adopted)
	}
	if existing["site_name"] != "Site A" {
		t.Errorf("existing_terminal.site_name = %v, want Site A", existing["site_name"])
	}

	id, _ := adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Front Door"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	res := f.poll(t, token)
	newKey, _ := res.Body["api_key"].(string)
	if newKey == "" || newKey == oldKey {
		t.Fatalf("re-provisioning did not issue a new credential: %s", res.Raw)
	}

	if old := f.env.do(http.MethodGet, "/api/v1/devices/settings", nil,
		deviceAuth(oldKey)); old.Code == http.StatusOK {
		t.Error("the previous credential still authenticates after a re-provision")
	}
	if fresh := f.env.do(http.MethodGet, "/api/v1/devices/settings", nil,
		deviceAuth(newKey)); fresh.Code != http.StatusOK {
		t.Errorf("the re-provisioned credential does not authenticate (got %d)", fresh.Code)
	}

	// Still one terminal, not two.
	if n := queryInt(t,
		`SELECT count(*) FROM devices WHERE serial_number = 'AT-RESET' AND deleted_at IS NULL`); n != 1 {
		t.Errorf("re-provisioning produced %d device rows, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// One-shot credential delivery
// ---------------------------------------------------------------------------

// TestCredentialIsDeliveredExactlyOnce.
//
// A replayed token must not yield a second key. The recovery for a terminal that
// lost what it was given is to announce again, not to ask again.
func TestCredentialIsDeliveredExactlyOnce(t *testing.T) {
	f := newAnnounceFixture(t)

	code, token := f.announce(t, "AT-SHOT")
	_, adopted := f.adopt(t, code)
	id, _ := adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Once"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	first := f.poll(t, token)
	if _, ok := first.Body["api_key"].(string); !ok {
		t.Fatalf("the first collection carried no key: %s", first.Raw)
	}

	second := f.poll(t, token)
	if second.Body["state"] != "REFUSED" {
		t.Errorf("a replayed token = %v, want REFUSED", second.Body["state"])
	}
	if strings.Contains(second.Raw, "atd_") {
		t.Fatal("a replayed announce token was issued a second credential")
	}
}

// TestCollectionRequiresTheAnnouncingUnitsToken.
//
// A second terminal that knows the serial, and even watches the approval happen,
// cannot collect: the token is what proves it is the unit that announced.
func TestCollectionRequiresTheAnnouncingUnitsToken(t *testing.T) {
	f := newAnnounceFixture(t)

	code, _ := f.announce(t, "AT-TOKEN")
	_, adopted := f.adopt(t, code)
	id, _ := adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Guarded"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	for _, wrong := range []string{"", "not-a-token",
		strings.Repeat("a", 64)} {
		res := f.env.do(http.MethodGet, "/api/v1/devices/announce", nil, announceHeader(wrong))
		if res.Code != http.StatusUnauthorized {
			t.Errorf("polling with token %q = %d, want 401", wrong, res.Code)
		}
		if strings.Contains(res.Raw, "atd_") {
			t.Fatalf("a credential was handed to a caller presenting %q", wrong)
		}
	}
}

// ---------------------------------------------------------------------------
// Reboots, retries and expiry
// ---------------------------------------------------------------------------

// TestReAnnouncingWithTheSameTokenKeepsTheCode.
//
// The failure this prevents: a terminal that re-announces on a cycle, or reboots
// while the customer is reading its panel, rotating the code out from under
// them.
func TestReAnnouncingWithTheSameTokenKeepsTheCode(t *testing.T) {
	f := newAnnounceFixture(t)
	code, token := f.announce(t, "AT-REBOOT")

	res := f.env.do(http.MethodPost, "/api/v1/devices/announce", map[string]any{
		"serial_number": "AT-REBOOT",
	}, announceHeader(token))
	if res.Code != http.StatusOK {
		t.Fatalf("re-announcing with a held token = %d, want 200: %s", res.Code, res.Raw)
	}
	if _, present := res.Body["pairing_code"]; present {
		t.Error("re-announcing minted a new pairing code; the displayed one must stand")
	}
	if _, present := res.Body["announce_token"]; present {
		t.Error("re-announcing rotated the announce token")
	}

	// One row, and the original code still works.
	if n := queryInt(t, `SELECT count(*) FROM terminal_announcements
	                      WHERE serial_number = 'AT-REBOOT' AND state = 'PENDING'`); n != 1 {
		t.Errorf("%d live announcements for one serial, want 1", n)
	}
	if status, body := f.adopt(t, code); status != http.StatusOK {
		t.Errorf("the originally displayed code stopped working after a reboot: %d %v",
			status, body)
	}
}

// TestReAnnouncingWithoutATokenSupersedes.
//
// The recovery path for a unit that lost its stored code: announcing with no
// token replaces the pending row and mints a fresh pair. The old code must stop
// working, or two codes would be live for one door.
func TestReAnnouncingWithoutATokenSupersedes(t *testing.T) {
	f := newAnnounceFixture(t)
	oldCode, _ := f.announce(t, "AT-LOST")
	newCode, _ := f.announce(t, "AT-LOST")

	if oldCode == newCode {
		t.Fatal("re-announcing without a token returned the same code")
	}

	if status, _ := f.adopt(t, oldCode); status != http.StatusNotFound {
		t.Errorf("a superseded code was accepted (got %d)", status)
	}
	if status, body := f.adopt(t, newCode); status != http.StatusOK {
		t.Errorf("the current code was refused: %d %v", status, body)
	}
}

// TestReAnnouncingDoesNotDestroyAnAdoptionInFlight.
//
// A reboot at the wrong moment must not throw away work an operator has already
// done. The unit is told the state instead, and the operator's approval still
// lands.
func TestReAnnouncingDoesNotDestroyAnAdoptionInFlight(t *testing.T) {
	f := newAnnounceFixture(t)
	code, token := f.announce(t, "AT-FLIGHT")

	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	id, _ := adopted["id"].(string)

	// The terminal reboots and re-announces WITHOUT its token.
	res := f.env.do(http.MethodPost, "/api/v1/devices/announce",
		map[string]any{"serial_number": "AT-FLIGHT"}, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("re-announcing over an adopted row = %d, want 200: %s", res.Code, res.Raw)
	}
	if res.Body["state"] != "ADOPTED" {
		t.Errorf("state = %v, want ADOPTED -- the row must be left alone", res.Body["state"])
	}
	if _, present := res.Body["pairing_code"]; present {
		t.Error("re-announcing over an adopted row minted a new code")
	}

	// The operator's approval still works, and the unit still collects with the
	// token it kept.
	if status, body := f.approve(t, id, "Site A", "Survivor"); status != http.StatusOK {
		t.Fatalf("approving after a reboot = %d: %v", status, body)
	}
	if key, _ := f.poll(t, token).Body["api_key"].(string); key == "" {
		t.Error("the terminal could not collect after re-announcing mid-adoption")
	}
}

// TestExpiredAnnouncementCannotBeAdopted.
func TestExpiredAnnouncementCannotBeAdopted(t *testing.T) {
	f := newAnnounceFixture(t)
	code, token := f.announce(t, "AT-STALE")

	expireAnnouncement(t, "AT-STALE")

	if status, body := f.adopt(t, code); status != http.StatusNotFound {
		t.Errorf("an expired code was adopted: %d %v", status, body)
	}
	if res := f.poll(t, token); res.Body["state"] != "REFUSED" {
		t.Errorf("the terminal was not told its announcement lapsed: %v", res.Body["state"])
	}

	// And it can start again -- the lapsed row must not hold the serial's slot.
	if _, _, err := announceRaw(f, "AT-STALE"); err != "" {
		t.Errorf("a terminal could not re-announce after expiry: %s", err)
	}
}

// TestExpiredAdoptionCannotBeApproved covers the second window: the operator
// adopted and then walked away.
func TestExpiredAdoptionCannotBeApproved(t *testing.T) {
	f := newAnnounceFixture(t)
	code, _ := f.announce(t, "AT-WALKAWAY")

	status, adopted := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d: %v", status, adopted)
	}
	id, _ := adopted["id"].(string)

	expireAnnouncement(t, "AT-WALKAWAY")

	status, body := f.approve(t, id, "Site A", "Too Late")
	if status != http.StatusNotFound {
		t.Errorf("approving an expired adoption = %d, want 404: %v", status, body)
	}

	// The console still SHOWS it, reported as expired rather than vanishing --
	// the operator is owed the explanation.
	_, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", f.token, f.csrf)
	pending := listOf(t, list, "pending")
	if len(pending) != 1 {
		t.Fatalf("an expired adoption vanished from the list: %v", list)
	}
	if row, _ := pending[0].(map[string]any); row["state"] != "EXPIRED" {
		t.Errorf("state = %v, want EXPIRED", row["state"])
	}
}

// TestExpirySweepClearsLapsedRows exercises the maintenance path.
func TestExpirySweepClearsLapsedRows(t *testing.T) {
	f := newAnnounceFixture(t)
	f.announce(t, "AT-SWEEP")
	expireAnnouncement(t, "AT-SWEEP")

	n, err := database.ExpireAnnouncements()
	if err != nil {
		t.Fatalf("expiring announcements: %v", err)
	}
	if n != 1 {
		t.Errorf("the sweep expired %d rows, want 1", n)
	}
	if state := queryString(t,
		`SELECT state FROM terminal_announcements WHERE serial_number = 'AT-SWEEP'`); state != "EXPIRED" {
		t.Errorf("state after the sweep = %q, want EXPIRED", state)
	}

	// Purging keeps recent history and removes old.
	purged, err := database.PurgeAnnouncements(30)
	if err != nil {
		t.Fatalf("purging: %v", err)
	}
	if purged != 0 {
		t.Errorf("a row created seconds ago was purged under a 30-day window")
	}
	mustExec(t, `UPDATE terminal_announcements
	                SET created_at = CURRENT_TIMESTAMP - interval '60 days'`)
	if purged, err = database.PurgeAnnouncements(30); err != nil || purged != 1 {
		t.Errorf("purge removed %d rows (err %v), want 1", purged, err)
	}
}

// TestRejectionFreesTheSerial.
//
// The operational undo. A terminal approved and then factory-reset has lost the
// token it needed to collect with; rejecting releases the slot so it can
// announce again.
func TestRejectionFreesTheSerial(t *testing.T) {
	f := newAnnounceFixture(t)

	code, _ := f.announce(t, "AT-UNDO")
	_, adopted := f.adopt(t, code)
	id, _ := adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Mistake"); status != http.StatusOK {
		t.Fatalf("approve = %d: %v", status, body)
	}

	status, body := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/reject",
		`{"reason":"wrong unit"}`, f.token, f.csrf)
	if status != http.StatusOK {
		t.Fatalf("rejecting = %d: %v", status, body)
	}

	if n := queryInt(t, `SELECT count(*) FROM audit_events
	                      WHERE action = 'TERMINAL_SETUP_REJECTED'`); n != 1 {
		t.Error("the rejection was not audited")
	}

	// The serial is free, and no credential was ever issued.
	newCode, newToken := f.announce(t, "AT-UNDO")
	if newCode == code {
		t.Error("the rejected code was reissued")
	}
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-UNDO'`); n != 0 {
		t.Errorf("a rejected setup left %d device rows behind", n)
	}

	// And the whole flow works second time round.
	status, adopted = f.adopt(t, newCode)
	if status != http.StatusOK {
		t.Fatalf("re-adopting after a rejection = %d: %v", status, adopted)
	}
	id, _ = adopted["id"].(string)
	if status, body := f.approve(t, id, "Site A", "Correct"); status != http.StatusOK {
		t.Fatalf("re-approving = %d: %v", status, body)
	}
	if key, _ := f.poll(t, newToken).Body["api_key"].(string); key == "" {
		t.Error("the terminal could not be set up after a rejection")
	}
}

// ---------------------------------------------------------------------------
// Roles and authentication
// ---------------------------------------------------------------------------

// TestAnnouncementRoleGates. MANAGER reads; ADMIN acts.
func TestAnnouncementRoleGates(t *testing.T) {
	f := newAnnounceFixture(t)
	code, _ := f.announce(t, "AT-ROLES")

	manager := companyIDBySlug(t, "one")
	_, mToken, mCSRF := consoleOperatorSession(t, f.env.router, manager,
		"manager@example.com", models.RoleManager)

	// A manager cannot adopt.
	status, _ := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, mToken, mCSRF)
	if status != http.StatusForbidden {
		t.Errorf("MANAGER adopting = %d, want 403", status)
	}

	// The admin adopts, and the manager can then SEE it.
	if status, body := f.adopt(t, code); status != http.StatusOK {
		t.Fatalf("ADMIN adopting = %d: %v", status, body)
	}
	status, list := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", mToken, mCSRF)
	if status != http.StatusOK {
		t.Fatalf("MANAGER listing = %d: %v", status, list)
	}
	if count, _ := list["count"].(float64); count != 1 {
		t.Errorf("MANAGER sees %v pending terminals, want 1", count)
	}

	// A viewer sees nothing at all.
	_, vToken, vCSRF := consoleOperatorSession(t, f.env.router, manager,
		"viewer@example.com", models.RoleViewer)
	if status, _ := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", vToken, vCSRF); status != http.StatusForbidden {
		t.Errorf("VIEWER listing = %d, want 403", status)
	}
}

// TestAnnouncementConsoleRoutesRequireAuthentication.
func TestAnnouncementConsoleRoutesRequireAuthentication(t *testing.T) {
	f := newAnnounceFixture(t)
	code, _ := f.announce(t, "AT-NOAUTH")

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/console/terminal-announcements", ""},
		{http.MethodPost, "/api/v1/console/terminal-announcements/adopt",
			`{"pairing_code":"` + code + `"}`},
	}
	for _, route := range routes {
		status, _ := consoleCall(t, f.env.router, route.method, route.path, route.body, "", "")
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s with no session = %d, want 401", route.method, route.path, status)
		}
	}

	// And CSRF is enforced on the unsafe one.
	status, _ := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, f.token, "")
	if status != http.StatusForbidden {
		t.Errorf("adopting without a CSRF token = %d, want 403", status)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// TestAnnounceIsRateLimited bounds what one address can do unauthenticated.
func TestAnnounceIsRateLimited(t *testing.T) {
	t.Setenv("ANNOUNCE_RATE_LIMIT_PER_MINUTE", "3")
	cheapBcrypt(t)
	env := newTestEnv(t)

	limited := false
	for i := 0; i < 8; i++ {
		res := env.do(http.MethodPost, "/api/v1/devices/announce",
			map[string]any{"serial_number": "AT-FLOOD"}, nil)
		if res.Code == http.StatusTooManyRequests {
			limited = true
			if res.Raw == "" || !strings.Contains(res.Raw, "Too many attempts") {
				t.Errorf("the limit response is not the standard one: %s", res.Raw)
			}
			break
		}
	}
	if !limited {
		t.Error("the announce endpoint accepted 8 requests with a limit of 3 per minute")
	}
}

// TestAdoptIsRateLimitedPerSession.
//
// The limiter that actually protects the pairing code: an attacker guessing
// codes would already hold an operator session, which an address-keyed bucket
// does not bound.
func TestAdoptIsRateLimitedPerSession(t *testing.T) {
	t.Setenv("ADOPT_RATE_LIMIT_PER_MINUTE", "3")
	f := newAnnounceFixture(t)

	limited := false
	for i := 0; i < 8; i++ {
		status, _ := f.adopt(t, "AAAA-BBBB")
		if status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("the adopt endpoint accepted 8 guesses with a limit of 3 per minute")
	}

	// READING IS NOT LIMITED BY THE SAME BUCKET. An operator who exhausted their
	// guesses must still be able to see the list and work out what went wrong.
	if status, _ := consoleCall(t, f.env.router, http.MethodGet,
		"/api/v1/console/terminal-announcements", "", f.token, f.csrf); status != http.StatusOK {
		t.Errorf("listing after the adopt limit was hit = %d, want 200", status)
	}
}

// ---------------------------------------------------------------------------
// Platform release
// ---------------------------------------------------------------------------

// TestPlatformReleaseAllowsAnotherCompanyToAdopt is the full RMA story.
func TestPlatformReleaseAllowsAnotherCompanyToAdopt(t *testing.T) {
	f := newAnnounceFixture(t)

	// Company One sets the terminal up.
	firstKey := f.setUp(t, "AT-RMA", "Site A", "Old Door")

	// Company Two acquires the hardware and cannot take it.
	other := companyIDBySlug(t, "two")
	_, otherToken, otherCSRF := consoleOperatorSession(t, f.env.router, other,
		"rma-admin@example.com", models.RoleAdmin)

	code, token := f.announce(t, "AT-RMA")
	status, body := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusConflict {
		t.Fatalf("adopting a serial owned by another company = %d, want 409: %v", status, body)
	}

	// A platform administrator releases it.
	mustCreatePlatformAdmin(t, "platform@example.com")
	pToken, pCSRF := platformLogin(t, f.env.router, "platform@example.com", testPlatformPassword)

	status, released := platformCall(t, f.env.router, http.MethodPost,
		"/api/v1/platform/terminals/AT-RMA/release", `{"reason":"resold"}`, pToken, pCSRF)
	if status != http.StatusOK {
		t.Fatalf("release = %d: %v", status, released)
	}
	if released["previous_company"] != "Company One" {
		t.Errorf("previous_company = %v, want Company One", released["previous_company"])
	}

	// The losing company's credential is dead and their trail says why.
	if old := f.env.do(http.MethodGet, "/api/v1/devices/settings", nil,
		deviceAuth(firstKey)); old.Code == http.StatusOK {
		t.Error("a released terminal still authenticates against its old company")
	}
	if n := queryInt(t, `SELECT count(*) FROM audit_events a
	                      JOIN companies c ON c.id = a.company_id
	                     WHERE a.action = 'TERMINAL_RELEASED' AND c.slug = 'one'`); n != 1 {
		t.Error("the release was not audited into the company that lost the terminal")
	}

	// The announcement in flight was voided, so the unit announces afresh.
	if res := f.poll(t, token); res.Body["state"] != "REFUSED" {
		t.Errorf("an announcement outstanding at release = %v, want REFUSED", res.Body["state"])
	}

	// And now the new owner can set it up normally.
	code, token = f.announce(t, "AT-RMA")
	status, adopted := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/adopt",
		`{"pairing_code":"`+code+`"}`, otherToken, otherCSRF)
	if status != http.StatusOK {
		t.Fatalf("adopting after release = %d: %v", status, adopted)
	}
	if adopted["verdict"] != database.VerdictNew {
		t.Errorf("verdict after release = %v, want NEW", adopted["verdict"])
	}

	id, _ := adopted["id"].(string)
	status, approved := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/console/terminal-announcements/"+id+"/approve",
		`{"site_id":"`+sitePublicIDByName(t, "Site C")+`","device_name":"New Door"}`,
		otherToken, otherCSRF)
	if status != http.StatusOK {
		t.Fatalf("approving after release = %d: %v", status, approved)
	}

	res := f.poll(t, token)
	if key, _ := res.Body["api_key"].(string); key == "" {
		t.Fatalf("the released terminal could not be set up by its new owner: %s", res.Raw)
	}
	if res.Body["company_name"] != "Company Two" {
		t.Errorf("company_name = %v, want Company Two", res.Body["company_name"])
	}
}

// TestOnlyThePlatformCanRelease. No operator role reaches it, in either company.
func TestOnlyThePlatformCanRelease(t *testing.T) {
	f := newAnnounceFixture(t)
	f.setUp(t, "AT-LOCKED", "Site A", "Held")

	// The owner's own OWNER cannot -- release is not a tenant operation at all.
	_, ownerToken, ownerCSRF := consoleOperatorSession(t, f.env.router, f.companyID,
		"owner@example.com", models.RoleOwner)
	status, _ := consoleCall(t, f.env.router, http.MethodPost,
		"/api/v1/platform/terminals/AT-LOCKED/release", "", ownerToken, ownerCSRF)
	if status != http.StatusUnauthorized {
		t.Errorf("an OWNER reaching the platform release route = %d, want 401", status)
	}

	// And an unauthenticated caller certainly cannot.
	res := f.env.do(http.MethodPost, "/api/v1/platform/terminals/AT-LOCKED/release", nil, nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated release = %d, want 401", res.Code)
	}

	// The terminal is untouched.
	if n := queryInt(t, `SELECT count(*) FROM devices
	                      WHERE serial_number = 'AT-LOCKED' AND deleted_at IS NULL`); n != 1 {
		t.Error("a refused release removed the terminal anyway")
	}
}

// ---------------------------------------------------------------------------
// Input validation
// ---------------------------------------------------------------------------

// TestAnnounceRefusesAnUnusableSerial.
//
// The serial arrives on an unauthenticated endpoint and is eventually written
// into `devices` as an identity, so it is validated against the firmware's own
// rule rather than left to a CHECK constraint -- a constraint violation would
// surface as a 500 nobody can act on.
func TestAnnounceRefusesAnUnusableSerial(t *testing.T) {
	f := newAnnounceFixture(t)

	for _, serial := range []string{
		"", // absent
		"AT-THIS-IS-FAR-TOO-LONG-FOR-THE-FIRMWARE", // over 15 characters
		"AT-BAD SERIAL",       // a space
		"AT-'; DROP TABLE --", // punctuation the firmware cannot hold either
	} {
		res := f.env.do(http.MethodPost, "/api/v1/devices/announce",
			map[string]any{"serial_number": serial}, nil)
		if res.Code != http.StatusBadRequest {
			t.Errorf("announcing serial %q = %d, want 400: %s", serial, res.Code, res.Raw)
		}
	}

	if n := queryInt(t, `SELECT count(*) FROM terminal_announcements`); n != 0 {
		t.Errorf("%d announcements were created from unusable serials", n)
	}
}

// TestClaimCodesStillWorkAlongsideAnnouncements.
//
// The pre-authorised path must be untouched by any of this. Same fixture, both
// mechanisms, neither interfering with the other.
func TestClaimCodesStillWorkAlongsideAnnouncements(t *testing.T) {
	f := newAnnounceFixture(t)

	// The claim path, unchanged.
	claimCode := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-CLAIMED")
	res := f.env.do(http.MethodPost, "/api/v1/devices/claim", map[string]any{
		"claim_code":    claimCode,
		"serial_number": "AT-CLAIMED",
	}, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("claiming = %d: %s", res.Code, res.Raw)
	}
	if via := queryString(t,
		`SELECT provisioned_via FROM devices WHERE serial_number = 'AT-CLAIMED'`); via != "CLAIM_CODE" {
		t.Errorf("provisioned_via = %q, want CLAIM_CODE", via)
	}

	// The announce path, on the same site, at the same time.
	f.setUp(t, "AT-ANNOUNCED", "Site A", "Announced Door")
	if via := queryString(t,
		`SELECT provisioned_via FROM devices WHERE serial_number = 'AT-ANNOUNCED'`); via != "ANNOUNCEMENT" {
		t.Errorf("provisioned_via = %q, want ANNOUNCEMENT", via)
	}

	// A pairing code is not a claim code and must not be redeemable as one.
	pairing, _ := f.announce(t, "AT-CROSS")
	crossed := f.env.do(http.MethodPost, "/api/v1/devices/claim", map[string]any{
		"claim_code":    pairing,
		"serial_number": "AT-CROSS",
	}, nil)
	if crossed.Code != http.StatusUnauthorized {
		t.Errorf("a pairing code was accepted by the claim endpoint (got %d)", crossed.Code)
	}

	// And a site key registration records itself honestly too.
	f.env.registerDevice(f.env.siteBKey, "AT-SITEKEY")
	if via := queryString(t,
		`SELECT provisioned_via FROM devices WHERE serial_number = 'AT-SITEKEY'`); via != "SITE_KEY" {
		t.Errorf("provisioned_via = %q, want SITE_KEY", via)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// expireAnnouncement winds both of a serial's clocks past their windows, rather
// than making a test wait fifteen minutes.
func expireAnnouncement(t *testing.T, serial string) {
	t.Helper()
	mustExec(t, `UPDATE terminal_announcements
	                SET expires_at = CURRENT_TIMESTAMP - interval '1 minute',
	                    approval_expires_at = CASE WHEN approval_expires_at IS NULL THEN NULL
	                        ELSE CURRENT_TIMESTAMP - interval '1 minute' END
	              WHERE serial_number = $1
	                AND state IN ('PENDING', 'ADOPTED', 'APPROVED')`, serial)
}

// announceRaw announces and reports the failure rather than ending the test, for
// the cases that are asserting an announcement is possible at all.
func announceRaw(f *announceFixture, serial string) (code, token, failure string) {
	res := f.env.do(http.MethodPost, "/api/v1/devices/announce",
		map[string]any{"serial_number": serial}, nil)
	if res.Code != http.StatusCreated {
		return "", "", res.Raw
	}
	code, _ = res.Body["pairing_code"].(string)
	token, _ = res.Body["announce_token"].(string)
	return code, token, ""
}
