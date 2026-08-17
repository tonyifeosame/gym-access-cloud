package main

import (
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// SEC-05: the site provisioning key must not be able to authenticate as a
// terminal.
//
// THE DEFECT THESE WOULD HAVE CAUGHT. DeviceAuthMiddleware accepted
// `X-API-Key` plus `X-Device-Serial` unconditionally. That is not a terminal
// authenticating -- it is anybody holding the site's provisioning secret
// asserting which terminal they are, and the server taking their word for it.
// The site key registers devices and rotates their credentials, so it lives on
// installers' laptops; with this path open, holding it was equivalent to
// holding every device key at the site.
//
// The path is now off unless LEGACY_DEVICE_AUTH says otherwise. Both states are
// tested, because "it can be turned back on" is a promise to the deployment
// that still has old hardware in a wall, and an untested escape hatch is not
// one.

// legacyPair is the deprecated credential: a site key plus a claimed serial.
func legacyPair(siteKey, serial string) map[string]string {
	return map[string]string{"X-API-Key": siteKey, "X-Device-Serial": serial}
}

// deviceEndpoints is every route behind DeviceAuthMiddleware.
//
// Listed exhaustively and asserted as a set, because the finding is about the
// MIDDLEWARE and a route added later would otherwise inherit the weakness
// without any test noticing.
var deviceEndpoints = []struct {
	method string
	path   string
	body   any
}{
	{http.MethodPost, "/api/v1/devices/heartbeat", map[string]any{"status": "ONLINE"}},
	{http.MethodGet, "/api/v1/devices/settings", nil},
	{http.MethodGet, "/api/v1/devices/jobs", nil},
	{http.MethodGet, "/api/v1/devices/enrollment/pending", nil},
	{http.MethodGet, "/api/v1/devices/credentials/pending", nil},
	{http.MethodPost, "/api/v1/devices/access/log", map[string]any{
		"event_id": "evt-1", "member_id": "M-1", "granted": true, "source": "FINGERPRINT",
	}},
	{http.MethodPost, "/api/v1/devices/credentials/placement", map[string]any{
		"member_id": "M-1", "state": "REMOVED",
	}},
}

// defaultDeviceAuth pins the flag OFF for a test that is about the default.
//
// Explicit rather than assumed: the suite loads `.env` from the repository root,
// and a developer who set LEGACY_DEVICE_AUTH there while debugging a fleet would
// otherwise turn these assertions into a puzzle.
func defaultDeviceAuth(t *testing.T) {
	t.Helper()
	t.Setenv(middleware.LegacyDeviceAuthEnv, "")
}

// TestSiteKeyCannotImpersonateATerminal is the finding, stated as a test.
func TestSiteKeyCannotImpersonateATerminal(t *testing.T) {
	defaultDeviceAuth(t)
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-0001")

	for _, endpoint := range deviceEndpoints {
		res := env.do(endpoint.method, endpoint.path, endpoint.body,
			legacyPair(env.siteAKey, "ESP32-0001"))

		// 401, and the same answer a caller with no credential at all gets.
		// The site key is valid and names a real terminal; it is simply not a
		// credential this middleware accepts.
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a site key and serial got %d, want 401 (body %s)",
				endpoint.method, endpoint.path, res.Code, res.Raw)
		}
	}
}

// TestTheRefusalNamesTheCredentialATerminalShouldHave.
//
// A deployed terminal that stops working produces a support call, and the
// answer has to be in the response rather than in somebody's memory of a
// migration.
func TestTheRefusalNamesTheCredentialATerminalShouldHave(t *testing.T) {
	defaultDeviceAuth(t)
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-0001")

	res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		legacyPair(env.siteAKey, "ESP32-0001"))

	message, _ := res.Body["error"].(string)
	for _, want := range []string{"X-Device-Key", "claim"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal %q does not mention %q", message, want)
		}
	}
}

// TestDeviceKeyAuthenticationIsUnaffected.
//
// The whole point is that nothing changes for a terminal holding its own
// credential, which is what current firmware does.
func TestDeviceKeyAuthenticationIsUnaffected(t *testing.T) {
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-0001")

	for _, endpoint := range deviceEndpoints {
		res := env.do(endpoint.method, endpoint.path, endpoint.body, deviceAuth(key))
		if res.Code == http.StatusUnauthorized || res.Code == http.StatusForbidden {
			t.Errorf("%s %s with a device key got %d, which is an authentication failure",
				endpoint.method, endpoint.path, res.Code)
		}
	}
}

// TestClaimingIsTheSupportedRouteToACredential.
//
// This is the argument for removing the legacy path rather than merely
// deprecating it: a terminal can obtain its own credential without the site key
// ever reaching it, so the reason the weak path was kept has expired.
func TestClaimingIsTheSupportedRouteToACredential(t *testing.T) {
	defaultDeviceAuth(t)
	cheapBcrypt(t)
	f := newClaimFixture(t)
	code := issueClaimCode(t, f.env, f.token, f.csrf, "Site A", "AT-CLAIMED")

	res := f.claim(t, code, "AT-CLAIMED")
	if res.Code != http.StatusOK {
		t.Fatalf("claim got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	deviceKey, _ := res.Body["api_key"].(string)
	if deviceKey == "" {
		t.Fatalf("claim response carried no device credential (body %s)", res.Raw)
	}

	// The credential it produced works on the endpoints the legacy pair was
	// just refused from, and it was obtained without the site key ever reaching
	// the terminal.
	poll := f.env.do(http.MethodGet, "/api/v1/devices/jobs", nil, deviceAuth(deviceKey))
	if poll.Code != http.StatusOK {
		t.Errorf("a claimed terminal polling for work got %d, want 200 (body %s)",
			poll.Code, poll.Raw)
	}
}

// ---------------------------------------------------------------------------
// The escape hatch
// ---------------------------------------------------------------------------

// TestLegacyDeviceAuthCanBeReopenedDeliberately.
//
// An installation with genuinely old hardware needs a way to keep those doors
// working while it upgrades. The alternative to a documented flag is somebody
// reverting the commit in a hurry.
func TestLegacyDeviceAuthCanBeReopenedDeliberately(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-0001")

	t.Setenv(middleware.LegacyDeviceAuthEnv, "1")

	res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		legacyPair(env.siteAKey, "ESP32-0001"))
	if res.Code != http.StatusOK {
		t.Fatalf("with %s=1 the legacy pair got %d, want 200 (body %s)",
			middleware.LegacyDeviceAuthEnv, res.Code, res.Raw)
	}
}

// TestOnlyAnAffirmativeValueReopensIt.
//
// A typo in the one variable that re-opens a known weakness must not be what
// enables it, so anything unparseable is off.
func TestOnlyAnAffirmativeValueReopensIt(t *testing.T) {
	env := newTestEnv(t)
	env.registerDevice(env.siteAKey, "ESP32-0001")

	for _, value := range []string{"", "0", "false", "no", "yes-please", "maybe"} {
		t.Setenv(middleware.LegacyDeviceAuthEnv, value)

		res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
			legacyPair(env.siteAKey, "ESP32-0001"))
		if res.Code != http.StatusUnauthorized {
			t.Errorf("%s=%q got %d, want 401", middleware.LegacyDeviceAuthEnv, value, res.Code)
		}
	}
}

// TestRevocationSurvivesTheLegacyPath is the regression that matters most about
// the escape hatch.
//
// RevokeTerminalCredential clears the key hash AND sets DISABLED/inactive. The
// device-key path is stopped by the first; only the second is visible on the
// legacy path, which is exactly why revocation sets both. A terminal reported
// stolen must be refused however it presents itself.
func TestRevocationSurvivesTheLegacyPath(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	key := env.registerDevice(env.siteAKey, "ESP32-STOLEN")

	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"revoke-legacy@example.com", models.RoleAdmin)

	t.Setenv(middleware.LegacyDeviceAuthEnv, "1")

	// It works on both credentials to start with, which is what makes the
	// refusal below meaningful.
	if res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		legacyPair(env.siteAKey, "ESP32-STOLEN")); res.Code != http.StatusOK {
		t.Fatalf("fixture: the legacy pair got %d before revocation, want 200", res.Code)
	}

	code, body := consoleCall(t, env.router, http.MethodPost,
		"/api/v1/console/terminals/ESP32-STOLEN/revoke",
		`{"reason":"reported stolen"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("revoke got %d, want 200 (body %v)", code, body)
	}

	if res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		deviceAuth(key)); res.Code == http.StatusOK {
		t.Error("a revoked terminal still authenticated with its device key")
	}

	res := env.do(http.MethodGet, "/api/v1/devices/jobs", nil,
		legacyPair(env.siteAKey, "ESP32-STOLEN"))
	if res.Code == http.StatusOK {
		t.Error("a revoked terminal authenticated through the legacy path, so revocation " +
			"can be defeated by presenting the site key instead")
	}
}
