package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// §6 Device claim codes.
//
// WHAT THIS REPLACES. Enrolling one terminal required the SITE API KEY on an
// installer's laptop. That key registers devices at the site and rotates any of
// their credentials, so bringing up one door left the site's provisioning secret
// in a shell history and a terminal's scrollback -- and the device key it
// produced was then read out and pasted somewhere else.
//
// Every test below is about a security property rather than a feature, because
// the endpoint is UNAUTHENTICATED and its safety rests entirely on the record:
// single use, serial-bound, short-lived, hashed, indistinguishable failures.

// issueClaimCode mints a code through the console, as an operator would.
func issueClaimCode(t *testing.T, env *testEnv, token, csrf, siteName, serial string) string {
	t.Helper()
	code, body := consoleCall(t, env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, siteName)+"/claim-codes",
		`{"serial_number":"`+serial+`"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing a claim code = %d: %v", code, body)
	}
	claim, ok := body["claim_code"].(string)
	if !ok || claim == "" {
		t.Fatalf("the response carried no claim code: %v", body)
	}
	return claim
}

// claimFixture is an installation with an operator who can issue codes.
type claimFixture struct {
	env       *testEnv
	companyID int64
	token     string
	csrf      string
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	env := newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"claim-admin@example.com", models.RoleAdmin)
	return &claimFixture{env: env, companyID: companyID, token: token, csrf: csrf}
}

// claim posts a redemption, unauthenticated, exactly as the firmware does.
func (f *claimFixture) claim(t *testing.T, code, serial string) response {
	t.Helper()
	return f.env.do("POST", "/api/v1/devices/claim", map[string]any{
		"claim_code":    code,
		"serial_number": serial,
	}, nil)
}

// TestClaimExchangesACodeForACredential is the happy path, and it asserts the
// exact response shape the firmware parses (provisioning.cpp).
func TestClaimExchangesACodeForACredential(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-A1B2C3")

	res := f.claim(t, code, "AT-A1B2C3")
	if res.Code != http.StatusOK {
		t.Fatalf("claim = %d: %s", res.Code, res.Raw)
	}

	apiKey, ok := res.Body["api_key"].(string)
	if !ok || apiKey == "" {
		t.Fatalf("the claim response carried no api_key: %s", res.Raw)
	}
	if res.Body["serial_number"] != "AT-A1B2C3" {
		t.Errorf("serial_number = %v, want the claimed serial", res.Body["serial_number"])
	}

	// THE KEY FORMAT IS A CONTRACT. DeviceCredential::keyLooksIssued requires
	// "atd_" followed by exactly 64 lower-case hex characters, and refuses
	// anything else before storing it -- so a key this server considers valid
	// but the firmware does not would be reported as "the server sent something
	// unusable" at a door.
	if !strings.HasPrefix(apiKey, "atd_") {
		t.Errorf("api_key = %q, want the atd_ prefix the firmware requires", apiKey)
	}
	if len(apiKey) != 68 {
		t.Errorf("api_key is %d characters, want 68 (atd_ + 64 hex)", len(apiKey))
	}
	if strings.ToLower(apiKey) != apiKey {
		t.Errorf("api_key contains upper case; the firmware refuses it: %q", apiKey)
	}

	// The response must fit the firmware's 512-byte buffer -- a longer body is
	// read as truncated and the whole claim fails.
	if len(res.Raw) > 512 {
		t.Errorf("the claim response is %d bytes; the firmware refuses over 512", len(res.Raw))
	}

	// And the credential actually works.
	settings := f.env.do("GET", "/api/v1/devices/settings", nil, deviceAuth(apiKey))
	if settings.Code != http.StatusOK {
		t.Errorf("the claimed credential does not authenticate (got %d)", settings.Code)
	}
}

// TestClaimCodeIsSingleUse.
func TestClaimCodeIsSingleUse(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-ONCE")

	if res := f.claim(t, code, "AT-ONCE"); res.Code != http.StatusOK {
		t.Fatalf("first claim = %d: %s", res.Code, res.Raw)
	}

	res := f.claim(t, code, "AT-ONCE")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("a redeemed code was accepted a second time (got %d): %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, "atd_") {
		t.Error("the second claim leaked a credential")
	}
}

// TestClaimCodeCannotClaimAnotherSerial is the property that makes an
// intercepted code close to worthless.
func TestClaimCodeCannotClaimAnotherSerial(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-BOUND")

	res := f.claim(t, code, "AT-ATTACKER")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("a code was redeemed by hardware it was not issued for (got %d): %s",
			res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, "atd_") {
		t.Fatal("a credential was issued to the wrong serial")
	}

	// No device row was created for the serial that tried.
	if n := queryInt(t, `SELECT count(*) FROM devices WHERE serial_number = 'AT-ATTACKER'`); n != 0 {
		t.Errorf("the refused claim created %d device rows", n)
	}

	// The code is still usable by the hardware it WAS issued for -- a failed
	// attempt by somebody else must not burn the installer's code.
	if res := f.claim(t, code, "AT-BOUND"); res.Code != http.StatusOK {
		t.Errorf("a wrong-serial attempt invalidated the legitimate code (got %d)", res.Code)
	}
}

// TestClaimFailuresAreIndistinguishable.
//
// Wrong code, expired code, superseded code, right code with the wrong serial --
// all one answer. Telling them apart would let an unauthenticated caller learn
// that a code is real but the serial is wrong, which is exactly the fact that
// turns an intercepted code back into something worth having.
func TestClaimFailuresAreIndistinguishable(t *testing.T) {
	f := newClaimFixture(t)

	live := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-REAL")

	expired := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-EXPIRED")
	mustExec(t, `UPDATE device_claim_codes SET expires_at = CURRENT_TIMESTAMP - interval '1 hour'
	              WHERE serial_number = 'AT-EXPIRED'`)

	superseded := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-SUPERSEDED")
	issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-SUPERSEDED")

	cases := []struct {
		name  string
		code  string
		seria string
	}{
		{"unknown code", "ZZZZ-ZZZZ", "AT-REAL"},
		{"expired code", expired, "AT-EXPIRED"},
		{"superseded code", superseded, "AT-SUPERSEDED"},
		{"right code, wrong serial", live, "AT-SOMETHING-ELSE"},
	}

	var bodies []string
	for _, tc := range cases {
		res := f.claim(t, tc.code, tc.seria)
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s returned %d, want 401", tc.name, res.Code)
		}
		bodies = append(bodies, res.Raw)
	}

	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("failure bodies differ, which distinguishes the cases:\n  %s: %s\n  %s: %s",
				cases[0].name, bodies[0], cases[i].name, bodies[i])
		}
	}
}

// TestIssuingSupersedesAnOutstandingCode. An installer holding an older printout
// must not be able to claim a unit a colleague just re-issued.
func TestIssuingSupersedesAnOutstandingCode(t *testing.T) {
	f := newClaimFixture(t)

	first := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-REISSUE")
	second := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-REISSUE")

	if res := f.claim(t, first, "AT-REISSUE"); res.Code != http.StatusUnauthorized {
		t.Errorf("a superseded code still worked (got %d)", res.Code)
	}
	if res := f.claim(t, second, "AT-REISSUE"); res.Code != http.StatusOK {
		t.Errorf("the current code did not work (got %d)", res.Code)
	}
}

// TestClaimCodeExpires.
func TestClaimCodeExpires(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-TTL")

	mustExec(t, `UPDATE device_claim_codes SET expires_at = CURRENT_TIMESTAMP - interval '1 minute'`)

	if res := f.claim(t, code, "AT-TTL"); res.Code != http.StatusUnauthorized {
		t.Errorf("an expired code was accepted (got %d)", res.Code)
	}
}

// TestClaimCodeIsStoredHashed. A database disclosure must not hand an attacker a
// working code.
func TestClaimCodeIsStoredHashed(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-HASHED")

	var stored, prefix string
	mustScan(t, `SELECT code_hash, code_prefix FROM device_claim_codes`, &stored, &prefix)

	if strings.Contains(stored, code) || stored == code {
		t.Fatal("the claim code is stored in the clear")
	}
	if len(stored) != 64 {
		t.Errorf("code_hash is %d characters, want a 64-character SHA-256", len(stored))
	}
	// The prefix is deliberately NOT secret -- it lets the console say which
	// code was issued -- but it must not be enough to redeem with.
	if len(prefix) >= len(code) {
		t.Errorf("code_prefix %q is as long as the code itself", prefix)
	}
	if res := f.claim(t, prefix, "AT-HASHED"); res.Code == http.StatusOK {
		t.Error("the non-secret prefix was accepted as a claim code")
	}
}

// TestClaimIsCrossTenantSafe. A code issued in one company must not produce a
// credential in another, and the device it creates must belong to the issuing
// site.
func TestClaimIsCrossTenantSafe(t *testing.T) {
	f := newClaimFixture(t)

	// Company two's operator cannot issue against company one's site: the site
	// does not resolve inside their company at all.
	_, otherToken, otherCsrf := consoleOperatorSession(t, f.env.router,
		companyIDBySlug(t, "two"), "other-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, f.env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/claim-codes",
		`{"serial_number":"AT-CROSS"}`, otherToken, otherCsrf)
	if code != http.StatusNotFound {
		t.Fatalf("an operator issued a claim code against another tenant's site (got %d): %v",
			code, body)
	}

	// And a legitimately issued code produces a device at the ISSUING site.
	claim := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-TENANT")
	if res := f.claim(t, claim, "AT-TENANT"); res.Code != http.StatusOK {
		t.Fatalf("claim = %d", res.Code)
	}

	var siteName string
	mustScan(t, `SELECT s.site_name FROM devices d JOIN sites s ON s.id = d.site_id
	              WHERE d.serial_number = 'AT-TENANT'`, &siteName)
	if siteName != "Site A" {
		t.Errorf("the claimed device landed at %q, want Site A", siteName)
	}
}

// TestClaimDoesNotExposeTheSiteKey. The entire point.
func TestClaimDoesNotExposeTheSiteKey(t *testing.T) {
	f := newClaimFixture(t)

	_, issueBody := consoleCall(t, f.env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/claim-codes",
		`{"serial_number":"AT-NOKEY"}`, f.token, f.csrf)
	issued := mustJSON(t, issueBody)

	claim := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-NOKEY2")
	redeemed := f.claim(t, claim, "AT-NOKEY2")

	for _, body := range []string{issued, redeemed.Raw} {
		if strings.Contains(body, f.env.siteAKey) {
			t.Errorf("the site provisioning key appeared in a claim payload: %s", body)
		}
		// The site key prefix is `ats_`; a device key is `atd_`. Only the
		// latter belongs in a claim response.
		if strings.Contains(body, "ats_") {
			t.Errorf("a site key appeared in a claim payload: %s", body)
		}
	}
}

// TestClaimIsRateLimited. It is unauthenticated, so this is the only thing
// bounding online guessing.
func TestClaimIsRateLimited(t *testing.T) {
	f := newClaimFixture(t)

	limited := false
	// The limiter allows 10/minute per address by default; 25 attempts must hit
	// it well before the end.
	for i := 0; i < 25; i++ {
		res := f.claim(t, "ZZZZ-ZZZZ", "AT-GUESS")
		if res.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("unauthenticated claim attempts are not rate limited")
	}
}

// TestClaimRequiresBothFields, and a malformed body is a 400 rather than a 404 --
// a 404 tells the firmware the server does not support claim codes at all and
// sends the installer to `set key`.
func TestClaimRequiresBothFields(t *testing.T) {
	env := newTestEnv(t)

	for _, body := range []map[string]any{
		{"claim_code": "ABCD-EFGH"},
		{"serial_number": "AT-1"},
		{},
	} {
		res := env.do("POST", "/api/v1/devices/claim", body, nil)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%v returned %d, want 400 (404 would mean 'unsupported')", body, res.Code)
		}
	}
}

// TestClaimRefusesADisabledTerminal. Claiming must not be a way around the one
// revocation this system has.
func TestClaimRefusesADisabledTerminal(t *testing.T) {
	f := newClaimFixture(t)

	// The terminal exists and has been disabled by an operator.
	f.env.registerDevice(f.env.siteAKey, "AT-DISABLED")
	mustExec(t, `UPDATE devices SET status = 'DISABLED', active = FALSE
	              WHERE serial_number = 'AT-DISABLED'`)

	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-DISABLED")

	res := f.claim(t, code, "AT-DISABLED")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("a disabled terminal was re-credentialed by claiming (got %d): %s",
			res.Code, res.Raw)
	}

	var status string
	mustScan(t, `SELECT status FROM devices WHERE serial_number = 'AT-DISABLED'`, &status)
	if status != models.DeviceDisabled {
		t.Errorf("the disabled terminal came back as %s", status)
	}
}

// TestClaimIsAudited. "Which unit claimed this code, from where, and when" is
// what an installation dispute turns into.
func TestClaimIsAudited(t *testing.T) {
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-AUDIT")

	if res := f.claim(t, code, "AT-AUDIT"); res.Code != http.StatusOK {
		t.Fatalf("claim = %d", res.Code)
	}

	// Both halves are recorded: the operator issuing and the terminal redeeming.
	for _, action := range []string{"DEVICE_CLAIM_CODE_ISSUED", "DEVICE_CLAIMED"} {
		n := queryInt(t, `SELECT count(*) FROM audit_events WHERE action = $1`, action)
		if n == 0 {
			t.Errorf("no audit record for %s", action)
		}
	}

	// The audit trail must not become a place to read a code.
	var changes string
	mustScan(t, `SELECT COALESCE(changes::text, '') FROM audit_events
	              WHERE action = 'DEVICE_CLAIM_CODE_ISSUED'`, &changes)
	if strings.Contains(changes, code) {
		t.Errorf("the audit record contains the claim code itself: %s", changes)
	}

	// The redemption records the address it came from.
	var ip string
	mustScan(t, `SELECT COALESCE(ip_address, '') FROM audit_events
	              WHERE action = 'DEVICE_CLAIMED'`, &ip)
	if ip == "" {
		t.Error("the redemption was recorded without the address that made it")
	}
}

// TestClaimCodeRequiresAdmin.
func TestClaimCodeRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router, companyIDBySlug(t, "one"),
		"claim-manager@example.com", models.RoleManager)

	code, _ := consoleCall(t, env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/claim-codes",
		`{"serial_number":"AT-ROLE"}`, token, csrf)
	if code != http.StatusForbidden {
		t.Errorf("a MANAGER issued a claim code (got %d)", code)
	}
}

// TestClaimCodeRefusesAnUnstorableSerial. The firmware holds a serial in
// char[16], so a longer one could never be presented by the hardware the code
// was issued for -- refused when minted rather than discovered at a door.
func TestClaimCodeRefusesAnUnstorableSerial(t *testing.T) {
	f := newClaimFixture(t)

	code, body := consoleCall(t, f.env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/claim-codes",
		`{"serial_number":"AT-THIS-SERIAL-IS-FAR-TOO-LONG"}`, f.token, f.csrf)
	if code != http.StatusBadRequest {
		t.Errorf("a serial no terminal could store was accepted (got %d): %v", code, body)
	}
}

// TestClaimCodeTTLIsBounded. A code that lives for a week is a site key with
// extra steps.
func TestClaimCodeTTLIsBounded(t *testing.T) {
	f := newClaimFixture(t)

	code, body := consoleCall(t, f.env.router, "POST",
		"/api/v1/console/sites/"+sitePublicIDByName(t, "Site A")+"/claim-codes",
		`{"serial_number":"AT-LONGTTL","expires_in_minutes":100000}`, f.token, f.csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing = %d: %v", code, body)
	}

	var expiresAt time.Time
	mustScan(t, `SELECT expires_at FROM device_claim_codes WHERE serial_number = 'AT-LONGTTL'`,
		&expiresAt)

	if time.Until(expiresAt) > database.MaxClaimCodeTTL+time.Minute {
		t.Errorf("the code expires in %s, beyond the %s cap",
			time.Until(expiresAt), database.MaxClaimCodeTTL)
	}
}
