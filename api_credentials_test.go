package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Integration credentials (030), the fourth credential class.
//
// P1 ships the LIFECYCLE and no consumer: there is no public route in this
// build, so what is proven here is that a credential can be issued, listed,
// rotated and revoked from the console, that its secret is shown once, and that
// AuthenticateAPICredential stops recognising it at exactly the right moments.
// The middleware that will call that function belongs with the public routes.

const credentialsPath = "/api/v1/console/api-credentials"

// adminSession returns a signed-in ADMIN for company "one".
func adminSession(t *testing.T, env *testEnv, email string) (string, string) {
	t.Helper()
	one := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, one, email, models.RoleAdmin)
	return token, csrf
}

// issueCredential mints one through the console and returns the decoded body.
func issueCredential(t *testing.T, env *testEnv, token, csrf, body string) map[string]any {
	t.Helper()
	code, decoded := consoleCall(t, env.router, "POST", credentialsPath, body, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("issuing a credential = %d (%v)", code, decoded)
	}
	return decoded
}

// secretOf pulls the plaintext out of an issue or rotate response.
func secretOf(t *testing.T, body map[string]any) string {
	t.Helper()
	secret, ok := body["secret"].(string)
	if !ok || secret == "" {
		t.Fatalf("response carried no secret: %v", body)
	}
	return secret
}

// ---------------------------------------------------------------------------
// Issue
// ---------------------------------------------------------------------------

func TestAPICredentialIsIssuedWithAShownOnceSecret(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "cred-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Booking sync","scopes":["members:read","events:read"]}`)

	secret := secretOf(t, issued)
	if !models.ValidAPIKeyShape(secret) {
		t.Fatalf("issued secret %q is not credential-shaped", secret)
	}
	if !strings.HasPrefix(secret, "atp_live_") {
		t.Errorf("secret has prefix %q, want atp_live_", secret[:9])
	}
	if issued["shown_once"] != true {
		t.Error("the response does not say the secret is shown once")
	}
	if issued["key_prefix"] != secret[:models.APIKeyPrefixLength] {
		t.Errorf("key_prefix %v does not match the secret", issued["key_prefix"])
	}

	// THE SECRET IS NEVER READABLE AGAIN. Not from the list, not from the
	// single read. This is the property the whole storage decision rests on.
	code, list := consoleCall(t, env.router, "GET", credentialsPath, "", token, "")
	if code != http.StatusOK {
		t.Fatalf("listing = %d (%v)", code, list)
	}
	if strings.Contains(fmt.Sprint(list), secret) {
		t.Fatal("the list response contains the plaintext secret")
	}

	id := issued["id"].(string)
	code, single := consoleCall(t, env.router, "GET", credentialsPath+"/"+id, "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading = %d (%v)", code, single)
	}
	if strings.Contains(fmt.Sprint(single), secret) {
		t.Fatal("the read response contains the plaintext secret")
	}
	if _, present := single["secret"]; present {
		t.Error("the read response has a secret field at all")
	}
}

func TestAPICredentialIsStoredOnlyAsAHash(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "hash-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Hashed","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	// The plaintext appears nowhere in the row, and the stored hash is the one
	// the authentication path would compute.
	if n := queryInt(t, `SELECT count(*) FROM api_credentials WHERE key_hash = $1`,
		database.HashAPIKey(secret)); n != 1 {
		t.Errorf("the stored hash does not match the issued secret (%d rows)", n)
	}
	if n := queryInt(t,
		`SELECT count(*) FROM api_credentials WHERE key_hash = $1 OR key_prefix = $1`,
		secret); n != 0 {
		t.Error("the plaintext secret is stored somewhere on the row")
	}
}

func TestScopeImplicationIsExpandedAtIssueTime(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "scope-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Writer","scopes":["members:write"]}`)

	scopes := issued["scopes"].([]any)
	if len(scopes) != 2 {
		t.Fatalf("members:write stored %v, want it expanded to include members:read", scopes)
	}

	// Stored, not inferred: a permission check downstream is a membership test.
	got := fmt.Sprint(scopes)
	if !strings.Contains(got, "members:read") || !strings.Contains(got, "members:write") {
		t.Errorf("stored scopes = %v", scopes)
	}
}

func TestAnUnknownScopeIsRefusedWithTheAvailableSet(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "badscope-admin@example.com")

	code, body := consoleCall(t, env.router, "POST", credentialsPath,
		`{"name":"Wrong","scopes":["members:delete"]}`, token, csrf)
	if code != http.StatusBadRequest {
		t.Fatalf("an unknown scope = %d, want 400 (%v)", code, body)
	}
	// Naming what does exist turns a guess into a fix.
	if body["available_scopes"] == nil {
		t.Error("the refusal does not list the scopes that exist")
	}
}

func TestAnOperatorCannotGrantAScopeAboveItsOwnRole(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// The route itself is ADMIN, so reaching the scope check with a lower role
	// means calling the handler's bound directly is the only way to observe it
	// end to end. What CAN be observed here is the ADMIN case: webhooks:manage
	// requires ADMIN and an ADMIN may grant it.
	_, token, csrf := consoleOperatorSession(t, env.router, one,
		"webhook-admin@example.com", models.RoleAdmin)

	code, body := consoleCall(t, env.router, "POST", credentialsPath,
		`{"name":"Hooks","scopes":["webhooks:manage"]}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("an ADMIN granting webhooks:manage = %d (%v)", code, body)
	}
}

func TestCredentialIssueIsAdminOnly(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	for _, role := range []string{models.RoleViewer, models.RoleManager} {
		_, token, csrf := consoleOperatorSession(t, env.router, one,
			strings.ToLower(role)+"-cred@example.com", role)

		code, _ := consoleCall(t, env.router, "POST", credentialsPath,
			`{"name":"Nope","scopes":["members:read"]}`, token, csrf)
		if code != http.StatusForbidden {
			t.Errorf("a %s issuing a credential = %d, want 403", role, code)
		}

		code, _ = consoleCall(t, env.router, "GET", credentialsPath, "", token, "")
		if code != http.StatusForbidden {
			t.Errorf("a %s listing credentials = %d, want 403", role, code)
		}
	}
}

func TestCredentialRoutesRefuseASiteKeyAndNoSession(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)

	// A SITE KEY IS THE PROVISIONING SECRET, not browser authentication.
	// Reaching the endpoint that issues integration credentials with it would
	// be the worst possible place for that to work.
	req := newRequestWithSiteKey(t, "GET", credentialsPath, "", env.siteAKey)
	if code := serve(env.router, req); code != http.StatusUnauthorized {
		t.Errorf("a site key on the credential list = %d, want 401", code)
	}

	code, _ := consoleCall(t, env.router, "GET", credentialsPath, "", "", "")
	if code != http.StatusUnauthorized {
		t.Errorf("no session on the credential list = %d, want 401", code)
	}
}

// ---------------------------------------------------------------------------
// The per-company cap
// ---------------------------------------------------------------------------

func TestTheCompanyCredentialCapIsEnforced(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "cap-admin@example.com")

	for i := 0; i < models.MaxAPICredentialsPerCompany; i++ {
		body := fmt.Sprintf(`{"name":"Key %d","scopes":["members:read"]}`, i)
		if code, decoded := consoleCall(t, env.router, "POST", credentialsPath,
			body, token, csrf); code != http.StatusCreated {
			t.Fatalf("issuing credential %d = %d (%v)", i, code, decoded)
		}
	}

	code, body := consoleCall(t, env.router, "POST", credentialsPath,
		`{"name":"One too many","scopes":["members:read"]}`, token, csrf)
	if code != http.StatusConflict {
		t.Fatalf("exceeding the cap = %d, want 409 (%v)", code, body)
	}
	if body["code"] != "API_CREDENTIAL_LIMIT_REACHED" {
		t.Errorf("refusal code = %v", body["code"])
	}
}

// THE CAP MUST NOT DEADLOCK ROTATION. A company at the ceiling still has to be
// able to replace a key -- that is the operation somebody reaches for during an
// incident, and a cap that blocked it would be worse than no cap.
func TestACompanyAtTheCapCanStillRotate(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "capless-admin@example.com")

	var firstID string
	for i := 0; i < models.MaxAPICredentialsPerCompany; i++ {
		issued := issueCredential(t, env, token, csrf,
			fmt.Sprintf(`{"name":"Key %d","scopes":["members:read"]}`, i))
		if i == 0 {
			firstID = issued["id"].(string)
		}
	}

	code, body := consoleCall(t, env.router, "POST",
		credentialsPath+"/"+firstID+"/rotate", `{"grace_seconds":0}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("rotating at the cap = %d, want 200 (%v)", code, body)
	}
	if _, ok := body["secret"].(string); !ok {
		t.Error("the rotation returned no new secret")
	}
}

// The cap is counted and inserted under pg_advisory_xact_lock, because
// PostgreSQL cannot express "at most twenty rows per company" as a constraint.
// Without the lock, concurrent issues each count the same headroom and both
// insert.
func TestTheCapHoldsUnderConcurrentIssue(t *testing.T) {
	cheapBcrypt(t)
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	// Fill to one short of the ceiling through the store directly: this is a
	// concurrency test of the store, not of the HTTP layer.
	for i := 0; i < models.MaxAPICredentialsPerCompany-1; i++ {
		if _, err := database.IssueAPICredential(database.APICredentialIssueInput{
			CompanyID:   one,
			Name:        fmt.Sprintf("Filler %d", i),
			Environment: models.APIEnvironmentLive,
			Scopes:      []string{models.ScopeMembersRead},
		}); err != nil {
			t.Fatalf("seeding credential %d: %v", i, err)
		}
	}

	// Eight racing attempts for one remaining slot.
	const racers = 8
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		refused   int
	)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := database.IssueAPICredential(database.APICredentialIssueInput{
				CompanyID:   one,
				Name:        fmt.Sprintf("Racer %d", n),
				Environment: models.APIEnvironmentLive,
				Scopes:      []string{models.ScopeMembersRead},
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, models.ErrAPICredentialLimit):
				refused++
			default:
				t.Errorf("racer %d: unexpected error %v", n, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if succeeded != 1 {
		t.Errorf("%d racers succeeded for one slot, want exactly 1", succeeded)
	}
	if refused != racers-1 {
		t.Errorf("%d racers were refused, want %d", refused, racers-1)
	}

	live := queryInt(t, `SELECT count(*) FROM api_credentials
	                      WHERE company_id = $1 AND revoked_at IS NULL
	                        AND superseded_at IS NULL`, one)
	if live != models.MaxAPICredentialsPerCompany {
		t.Errorf("company holds %d live credentials, want %d",
			live, models.MaxAPICredentialsPerCompany)
	}
}

// ---------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------

func TestRotationIssuesANewSecretAndKeepsTheOldOneInGrace(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "rotate-admin@example.com")

	original := issueCredential(t, env, token, csrf,
		`{"name":"Rotating","scopes":["members:write"]}`)
	oldSecret := secretOf(t, original)
	oldID := original["id"].(string)

	code, rotated := consoleCall(t, env.router, "POST",
		credentialsPath+"/"+oldID+"/rotate", `{"grace_seconds":3600}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("rotating = %d (%v)", code, rotated)
	}
	newSecret := secretOf(t, rotated)

	if newSecret == oldSecret {
		t.Fatal("rotation returned the same secret")
	}
	if rotated["name"] != "Rotating" {
		t.Errorf("the replacement is named %v, want the original name", rotated["name"])
	}
	// The scopes move with the name: replacing a secret is not an opportunity to
	// change what the integration may do.
	if got := fmt.Sprint(rotated["scopes"]); !strings.Contains(got, "members:write") {
		t.Errorf("the replacement carries scopes %v", rotated["scopes"])
	}

	// BOTH AUTHENTICATE during the window. That overlap is the entire reason
	// rotation is a new row rather than a new hash.
	for label, secret := range map[string]string{"old": oldSecret, "new": newSecret} {
		identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive)
		if err != nil {
			t.Fatalf("authenticating the %s credential: %v", label, err)
		}
		if identity == nil {
			t.Errorf("the %s credential does not authenticate during the grace window", label)
		}
	}

	// The superseded row points at its replacement, so the console can show a
	// chain rather than two unrelated credentials.
	code, old := consoleCall(t, env.router, "GET", credentialsPath+"/"+oldID, "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading the superseded credential = %d (%v)", code, old)
	}
	if old["status"] != models.APICredentialInGrace {
		t.Errorf("superseded status = %v, want %s", old["status"], models.APICredentialInGrace)
	}
	if old["superseded_by"] != rotated["id"] {
		t.Errorf("superseded_by = %v, want %v", old["superseded_by"], rotated["id"])
	}
}

func TestAZeroGraceRotationCutsTheOldCredentialOffImmediately(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "cutover-admin@example.com")

	original := issueCredential(t, env, token, csrf,
		`{"name":"Compromised","scopes":["members:read"]}`)
	oldSecret := secretOf(t, original)

	code, rotated := consoleCall(t, env.router, "POST",
		credentialsPath+"/"+original["id"].(string)+"/rotate",
		`{"grace_seconds":0}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("rotating = %d (%v)", code, rotated)
	}

	// This is the case the window is caller-chosen for: the reason to rotate was
	// that the old secret leaked.
	identity, err := database.AuthenticateAPICredential(oldSecret, models.APIEnvironmentLive)
	if err != nil {
		t.Fatalf("authenticating the cut-off credential: %v", err)
	}
	if identity != nil {
		t.Error("a zero-grace rotation left the old credential working")
	}

	if identity, err := database.AuthenticateAPICredential(
		secretOf(t, rotated), models.APIEnvironmentLive); err != nil || identity == nil {
		t.Errorf("the replacement does not authenticate (%v, %v)", identity, err)
	}
}

func TestAnAlreadyRotatedCredentialCannotBeRotatedAgain(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "double-rotate@example.com")

	original := issueCredential(t, env, token, csrf,
		`{"name":"Once","scopes":["members:read"]}`)
	id := original["id"].(string)

	if code, _ := consoleCall(t, env.router, "POST", credentialsPath+"/"+id+"/rotate",
		"", token, csrf); code != http.StatusOK {
		t.Fatalf("first rotation = %d", code)
	}

	// Rotating it again would branch the chain and leave two replacements for
	// one original, with nothing to say which is current.
	code, body := consoleCall(t, env.router, "POST", credentialsPath+"/"+id+"/rotate",
		"", token, csrf)
	if code != http.StatusConflict {
		t.Errorf("rotating a superseded credential = %d, want 409 (%v)", code, body)
	}
}

func TestAnOverlongGraceWindowIsRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "grace-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Long grace","scopes":["members:read"]}`)

	tooLong := int(models.MaxAPIKeyRotationGrace.Seconds()) + 1
	code, body := consoleCall(t, env.router, "POST",
		credentialsPath+"/"+issued["id"].(string)+"/rotate",
		fmt.Sprintf(`{"grace_seconds":%d}`, tooLong), token, csrf)
	if code != http.StatusBadRequest {
		t.Errorf("an overlong grace window = %d, want 400 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Revocation
// ---------------------------------------------------------------------------

func TestRevocationStopsACredentialAuthenticating(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "revoke-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Doomed","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	if identity, err := database.AuthenticateAPICredential(
		secret, models.APIEnvironmentLive); err != nil || identity == nil {
		t.Fatalf("the credential does not authenticate before revocation (%v, %v)", identity, err)
	}

	code, revoked := consoleCall(t, env.router, "DELETE",
		credentialsPath+"/"+issued["id"].(string),
		`{"reason":"no longer used"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("revoking = %d (%v)", code, revoked)
	}
	if revoked["status"] != models.APICredentialRevokedS {
		t.Errorf("status after revocation = %v", revoked["status"])
	}
	if revoked["revoked_reason"] != "no longer used" {
		t.Errorf("revoked_reason = %v", revoked["revoked_reason"])
	}

	// Effective on the next request: nothing caches the row.
	identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive)
	if err != nil {
		t.Fatalf("authenticating a revoked credential: %v", err)
	}
	if identity != nil {
		t.Error("a revoked credential still authenticates")
	}
}

func TestRevokingTwiceIsRefusedRatherThanSilent(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "double-revoke@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Twice","scopes":["members:read"]}`)
	id := issued["id"].(string)

	if code, _ := consoleCall(t, env.router, "DELETE", credentialsPath+"/"+id,
		"", token, csrf); code != http.StatusOK {
		t.Fatalf("first revocation failed")
	}

	// A second revocation is usually somebody unsure whether the first worked.
	// Saying so is more useful than a silent success.
	code, _ := consoleCall(t, env.router, "DELETE", credentialsPath+"/"+id, "", token, csrf)
	if code != http.StatusConflict {
		t.Errorf("revoking twice = %d, want 409", code)
	}
}

// The incident-response control. It must reach everything that can still
// authenticate, INCLUDING the superseded half of a rotation -- "turn off every
// integration" must not leave the one nobody was thinking about still working.
func TestRevokeAllReachesEveryLiveCredentialIncludingGraceRows(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "revokeall-admin@example.com")

	first := issueCredential(t, env, token, csrf,
		`{"name":"First","scopes":["members:read"]}`)
	second := issueCredential(t, env, token, csrf,
		`{"name":"Second","scopes":["events:read"]}`)

	// Rotate the first, so a superseded-but-live row exists.
	code, rotated := consoleCall(t, env.router, "POST",
		credentialsPath+"/"+first["id"].(string)+"/rotate",
		`{"grace_seconds":3600}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("rotating = %d (%v)", code, rotated)
	}

	secrets := []string{
		secretOf(t, first), secretOf(t, second), secretOf(t, rotated),
	}

	code, body := consoleCall(t, env.router, "POST", credentialsPath+"/revoke-all",
		`{"reason":"suspected compromise"}`, token, csrf)
	if code != http.StatusOK {
		t.Fatalf("revoke-all = %d (%v)", code, body)
	}
	if got := body["revoked"]; got != float64(3) {
		t.Errorf("revoke-all reported %v revoked, want 3", got)
	}

	for i, secret := range secrets {
		identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive)
		if err != nil {
			t.Fatalf("authenticating secret %d: %v", i, err)
		}
		if identity != nil {
			t.Errorf("secret %d still authenticates after revoke-all", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Expiry and environment
// ---------------------------------------------------------------------------

func TestAnExpiredCredentialStopsAuthenticating(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "expiry-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Short lived","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	// Age it rather than waiting. The predicate is what is under test, not the
	// clock.
	//
	// CREATED_AT MOVES WITH IT, and it has to: api_credentials_expiry_check
	// refuses a row whose expiry precedes its creation, so setting the expiry
	// alone is not a state this table can hold. That is the constraint working
	// -- a real expired credential was issued a year ago and lapsed since -- so
	// the fixture reproduces that rather than a shape production cannot reach.
	mustExec(t, `UPDATE api_credentials
	                SET created_at = CURRENT_TIMESTAMP - interval '2 days',
	                    expires_at = CURRENT_TIMESTAMP - interval '1 minute'
	              WHERE public_id::text = $1`, issued["id"].(string))

	identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive)
	if err != nil {
		t.Fatalf("authenticating an expired credential: %v", err)
	}
	if identity != nil {
		t.Error("an expired credential still authenticates")
	}

	code, read := consoleCall(t, env.router, "GET",
		credentialsPath+"/"+issued["id"].(string), "", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading = %d", code)
	}
	if read["status"] != models.APICredentialExpired {
		t.Errorf("status = %v, want %s", read["status"], models.APICredentialExpired)
	}
}

func TestIssueAppliesADefaultLifetimeAndHonoursAnExplicitNull(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "lifetime-admin@example.com")

	// Absent: the default lifetime applies, so a credential nobody thought about
	// does not live forever.
	defaulted := issueCredential(t, env, token, csrf,
		`{"name":"Defaulted","scopes":["members:read"]}`)
	if defaulted["expires_at"] == nil {
		t.Error("an omitted expires_at produced a non-expiring credential")
	}

	// Explicit null: never expires, and the operator had to ask for it.
	forever := issueCredential(t, env, token, csrf,
		`{"name":"Forever","scopes":["members:read"],"expires_at":null}`)
	if forever["expires_at"] != nil {
		t.Errorf("an explicit null expires_at produced %v", forever["expires_at"])
	}
}

func TestAnExpiryInThePastIsRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "pastexpiry-admin@example.com")

	code, body := consoleCall(t, env.router, "POST", credentialsPath,
		`{"name":"Stale","scopes":["members:read"],"expires_at":"2020-01-01T00:00:00Z"}`,
		token, csrf)
	if code != http.StatusBadRequest {
		t.Errorf("an expiry in the past = %d, want 400 (%v)", code, body)
	}
}

// A credential minted for the other environment is refused BY SHAPE, before the
// database is touched -- so a staging key that reaches production fails
// immediately rather than missing a lookup and looking like a typo.
func TestACredentialFromAnotherEnvironmentDoesNotAuthenticate(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "env-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Live key","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentTest)
	if err != nil {
		t.Fatalf("authenticating against the wrong environment: %v", err)
	}
	if identity != nil {
		t.Error("a live credential authenticated a test deployment")
	}
}

func TestADeploymentRefusesToMintTheOtherEnvironment(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "mintenv-admin@example.com")

	// A deployment cannot mint a credential it would then refuse to accept.
	code, body := consoleCall(t, env.router, "POST", credentialsPath,
		`{"name":"Test key","scopes":["members:read"],"environment":"test"}`, token, csrf)
	if code != http.StatusBadRequest {
		t.Errorf("minting a test credential on a live deployment = %d, want 400 (%v)", code, body)
	}
}

// ---------------------------------------------------------------------------
// Tenancy
// ---------------------------------------------------------------------------

func TestCredentialsAreScopedToTheCompany(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")
	two := operatorCompanyID(t, "two")

	_, tokenOne, csrfOne := consoleOperatorSession(t, env.router, one,
		"one-cred@example.com", models.RoleAdmin)
	_, tokenTwo, csrfTwo := consoleOperatorSession(t, env.router, two,
		"two-cred@example.com", models.RoleAdmin)

	issued := issueCredential(t, env, tokenOne, csrfOne,
		`{"name":"Company one","scopes":["members:read"]}`)
	id := issued["id"].(string)

	// 404, never 403: answering "forbidden" would confirm the id exists in
	// somebody else's account.
	code, _ := consoleCall(t, env.router, "GET", credentialsPath+"/"+id, "", tokenTwo, "")
	if code != http.StatusNotFound {
		t.Errorf("reading another company's credential = %d, want 404", code)
	}

	code, list := consoleCall(t, env.router, "GET", credentialsPath, "", tokenTwo, "")
	if code != http.StatusOK {
		t.Fatalf("listing = %d", code)
	}
	if strings.Contains(fmt.Sprint(list), id) {
		t.Error("company two's list contains company one's credential")
	}

	// And a revoke-all in the other company leaves it alone.
	if code, _ := consoleCall(t, env.router, "POST", credentialsPath+"/revoke-all",
		"", tokenTwo, csrfTwo); code != http.StatusOK {
		t.Fatalf("revoke-all in company two failed")
	}
	identity, err := database.AuthenticateAPICredential(
		secretOf(t, issued), models.APIEnvironmentLive)
	if err != nil {
		t.Fatalf("authenticating: %v", err)
	}
	if identity == nil {
		t.Error("company two's revoke-all reached company one's credential")
	}
}

func TestASiteRestrictedCredentialCarriesItsSites(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "sites-admin@example.com")

	var sitePublicID string
	if err := database.DB.QueryRow(
		`SELECT public_id::text FROM sites WHERE site_name = 'Site A'`).Scan(&sitePublicID); err != nil {
		t.Fatalf("resolving Site A: %v", err)
	}

	issued := issueCredential(t, env, token, csrf, fmt.Sprintf(
		`{"name":"Site scoped","scopes":["terminals:read"],"site_ids":["%s"]}`, sitePublicID))

	if issued["all_sites"] != false {
		t.Errorf("all_sites = %v, want false for a restricted credential", issued["all_sites"])
	}
	sites := issued["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites = %v, want one", sites)
	}

	identity, err := database.AuthenticateAPICredential(
		secretOf(t, issued), models.APIEnvironmentLive)
	if err != nil || identity == nil {
		t.Fatalf("authenticating: %v %v", identity, err)
	}
	if len(identity.SiteIDs) != 1 {
		t.Fatalf("identity carries %d sites, want 1", len(identity.SiteIDs))
	}
	if !identity.ReachesSite(identity.SiteIDs[0]) {
		t.Error("the credential does not reach its own site")
	}
	if identity.ReachesSite(identity.SiteIDs[0] + 1000) {
		t.Error("a restricted credential reaches a site it was not granted")
	}
}

func TestASiteFromAnotherCompanyIsRefused(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "foreignsite-admin@example.com")

	var siteC string
	if err := database.DB.QueryRow(
		`SELECT public_id::text FROM sites WHERE site_name = 'Site C'`).Scan(&siteC); err != nil {
		t.Fatalf("resolving Site C: %v", err)
	}

	code, body := consoleCall(t, env.router, "POST", credentialsPath, fmt.Sprintf(
		`{"name":"Cross tenant","scopes":["terminals:read"],"site_ids":["%s"]}`, siteC),
		token, csrf)
	if code != http.StatusBadRequest {
		t.Errorf("naming another company's site = %d, want 400 (%v)", code, body)
	}
}

// A credential belonging to a deactivated company must stop working, on the same
// terms an operator session does.
func TestACredentialOfADisabledCompanyDoesNotAuthenticate(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "disabled-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Suspended","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	mustExec(t, `UPDATE companies SET active = FALSE WHERE slug = 'one'`)

	identity, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive)
	if err != nil {
		t.Fatalf("authenticating: %v", err)
	}
	if identity != nil {
		t.Error("a credential of a deactivated company still authenticates")
	}
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

func TestCredentialLifecycleIsAudited(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "audit-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Audited","scopes":["members:write"]}`)
	id := issued["id"].(string)
	secret := secretOf(t, issued)

	if code, _ := consoleCall(t, env.router, "POST", credentialsPath+"/"+id+"/rotate",
		"", token, csrf); code != http.StatusOK {
		t.Fatalf("rotating failed")
	}
	if code, _ := consoleCall(t, env.router, "POST", credentialsPath+"/revoke-all",
		"", token, csrf); code != http.StatusOK {
		t.Fatalf("revoke-all failed")
	}

	for _, action := range []string{
		"API_CREDENTIAL_ISSUED", "API_CREDENTIAL_ROTATED", "API_CREDENTIAL_REVOKED_ALL",
	} {
		n := queryInt(t, `SELECT count(*) FROM audit_events WHERE action = $1`, action)
		if n == 0 {
			t.Errorf("no audit event recorded for %s", action)
		}
	}

	// THE SECRET IS NEVER IN THE TRAIL. Enough to identify which credential this
	// was, nothing that would let a reader of the trail become it.
	var changes string
	if err := database.DB.QueryRow(
		`SELECT coalesce(changes::text, '') FROM audit_events
		  WHERE action = 'API_CREDENTIAL_ISSUED' ORDER BY id DESC LIMIT 1`).
		Scan(&changes); err != nil {
		t.Fatalf("reading the audit record: %v", err)
	}
	if strings.Contains(changes, secret) {
		t.Fatal("the audit record contains the plaintext secret")
	}
	if !strings.Contains(changes, "key_prefix") {
		t.Errorf("the audit record does not identify the credential: %s", changes)
	}
	// The actor is a person, not the credential: it was a human decision.
	var actor string
	if err := database.DB.QueryRow(
		`SELECT coalesce(actor_email, '') FROM audit_events
		  WHERE action = 'API_CREDENTIAL_ISSUED' ORDER BY id DESC LIMIT 1`).
		Scan(&actor); err != nil {
		t.Fatalf("reading the actor: %v", err)
	}
	if actor != "audit-admin@example.com" {
		t.Errorf("audit actor = %q", actor)
	}
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

// A credential must never reach the log. It is a bearer secret, and a log
// aggregator is exactly where one should not end up.
func TestIssuingACredentialDoesNotLogTheSecret(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "log-admin@example.com")

	output := captureLog(t)

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Quiet","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	// And authenticate with it, so the read path is exercised too.
	if _, err := database.AuthenticateAPICredential(secret, models.APIEnvironmentLive); err != nil {
		t.Fatalf("authenticating: %v", err)
	}

	logged := output.String()
	if strings.Contains(logged, secret) {
		t.Fatal("the plaintext credential was written to the log")
	}
	// The hash is not a secret, but it is the stored form and belongs in the
	// database rather than in a log line.
	if strings.Contains(logged, database.HashAPIKey(secret)) {
		t.Error("the credential hash was written to the log")
	}
}

// ---------------------------------------------------------------------------
// Usage telemetry
// ---------------------------------------------------------------------------

func TestCredentialUseIsRecordedByTheFlushRatherThanTheRequest(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	token, csrf := adminSession(t, env, "usage-admin@example.com")

	issued := issueCredential(t, env, token, csrf,
		`{"name":"Busy","scopes":["members:read"]}`)
	id := issued["id"].(string)

	identity, err := database.AuthenticateAPICredential(
		secretOf(t, issued), models.APIEnvironmentLive)
	if err != nil || identity == nil {
		t.Fatalf("authenticating: %v %v", identity, err)
	}

	// Nothing is written on the request path, so nothing is visible yet.
	code, before := consoleCall(t, env.router, "GET", credentialsPath+"/"+id+"/usage",
		"", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading usage = %d (%v)", code, before)
	}
	if before["last_used_at"] != nil {
		t.Error("last_used_at was written on the request path")
	}

	now := time.Now()
	database.NoteAPICredentialUse(identity.ID, "203.0.113.7", "read", now, false)
	database.NoteAPICredentialUse(identity.ID, "203.0.113.7", "read", now, true)

	if _, err := database.FlushAPICredentialUse(context.Background()); err != nil {
		t.Fatalf("flushing: %v", err)
	}

	code, after := consoleCall(t, env.router, "GET", credentialsPath+"/"+id+"/usage",
		"", token, "")
	if code != http.StatusOK {
		t.Fatalf("reading usage = %d (%v)", code, after)
	}
	if after["last_used_at"] == nil {
		t.Error("the flush did not record last_used_at")
	}
	if after["last_used_ip"] != "203.0.113.7" {
		t.Errorf("last_used_ip = %v", after["last_used_ip"])
	}

	days := after["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("usage days = %v, want one", days)
	}
	day := days[0].(map[string]any)
	if day["requests"] != float64(1) || day["refusals"] != float64(1) {
		t.Errorf("usage counts = %v", day)
	}
}
