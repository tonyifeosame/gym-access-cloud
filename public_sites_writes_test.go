package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// The public site write routes (037), end to end through the real router.
//
// DRIVEN WITH AN INTEGRATION CREDENTIAL here, and with an OAuth access token in
// oauth_test.go. That is the point of the split: the two credential classes
// reach the same handlers through the same scope gate, and neither file should
// be able to pass while the other fails for a reason that is really about
// sites.

const sitesPath = "/api/public/v1/sites"

// siteWriterCredential issues a key that can create and change sites.
func siteWriterCredential(t *testing.T, env *testEnv, slug, email string) string {
	t.Helper()
	return publicCredential(t, env, slug, email,
		`{"name":"site writer","scopes":["sites:write"]}`)
}

// siteReaderCredential issues a key that can only read them.
func siteReaderCredential(t *testing.T, env *testEnv, slug, email string) string {
	t.Helper()
	return publicCredential(t, env, slug, email,
		`{"name":"site reader","scopes":["sites:read"]}`)
}

func siteField(t *testing.T, body map[string]any, key string) any {
	t.Helper()
	value, present := body[key]
	if !present {
		t.Fatalf("site response has no %q: %v", key, body)
	}
	return value
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPublicCreateSiteStoresWhatItWasGiven(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "sitewriter@example.com")

	status, _, body, _ := publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Abuja Studio","address":"3 Shehu Shagari Way","country":"ng","timezone":"Africa/Lagos"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("POST /sites = %d (%v)", status, body)
	}

	if got := siteField(t, body, "name"); got != "Abuja Studio" {
		t.Errorf("name = %v", got)
	}
	// Case-insensitive in, upper case out: two integrations spelling the same
	// country differently cannot produce two different stored values.
	if got := siteField(t, body, "country"); got != "NG" {
		t.Errorf("country = %v, want NG", got)
	}
	if got := siteField(t, body, "timezone"); got != "Africa/Lagos" {
		t.Errorf("timezone = %v", got)
	}
	if got := siteField(t, body, "active"); got != true {
		t.Errorf("a new site is not active: %v", got)
	}
	if got := siteField(t, body, "terminal_count"); got != float64(0) {
		t.Errorf("terminal_count = %v", got)
	}

	// The public id is a UUID, not a row id.
	id, _ := siteField(t, body, "id").(string)
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("id %q is not a public identifier", id)
	}
	var rows int
	scanRow(t, `SELECT count(*) FROM sites WHERE public_id::text = $1 AND country = 'NG'`,
		[]any{id}, &rows)
	if rows != 1 {
		t.Fatalf("the site was not stored under that public id")
	}

	// AND NO PROVISIONING SECRET CAME BACK, though the site has one.
	raw := strings.ToLower(strings.Join([]string{
		mustJSON(t, body),
	}, " "))
	for _, forbidden := range []string{"ats_", "api_key", "provisioning", "secret", "key_hash"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the create response mentions %q", forbidden)
		}
	}
	var hash string
	scanRow(t, `SELECT api_key_hash FROM sites WHERE public_id::text = $1`, []any{id}, &hash)
	if hash == "" {
		t.Error("the site was created with no provisioning key; no terminal could ever be registered at it")
	}
}

func TestPublicCreateSiteIsAuditedAgainstTheCredential(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "siteaudit@example.com")

	status, _, body, _ := publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Audited Site","country":"NG","timezone":"Africa/Lagos"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("POST /sites = %d (%v)", status, body)
	}
	id, _ := body["id"].(string)

	var (
		action, actorRole, actorEmail, changes string
	)
	scanRow(t, `SELECT action, actor_role, actor_email, changes::text
	              FROM audit_events
	             WHERE target_type = 'SITE' AND target_public_id = $1`,
		[]any{id}, &action, &actorRole, &actorEmail, &changes)

	if action != "SITE_CREATED" {
		t.Errorf("action = %q", action)
	}
	if actorRole != "INTEGRATION" {
		t.Errorf("actor_role = %q, want INTEGRATION", actorRole)
	}
	if !strings.HasPrefix(actorEmail, "atp_") {
		t.Errorf("actor_email = %q, want the credential's non-secret prefix", actorEmail)
	}
	if !strings.Contains(changes, `"country"`) || !strings.Contains(changes, `"timezone"`) {
		t.Errorf("the record does not say what was created: %s", changes)
	}
	if strings.Contains(changes, "ats_") {
		t.Fatal("the audit record carries a provisioning key")
	}
}

func TestPublicCreateSiteRefusesADuplicateName(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "dupe@example.com")

	// "Site A" already exists in company one.
	status, headers, body, _ := publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Site A","country":"NG","timezone":"Africa/Lagos"}`, "")
	if code := publicError(t, status, headers, body, http.StatusConflict); code != models.CodeSiteNameExists {
		t.Fatalf("a duplicate name: code %s", code)
	}

	// A RETRY IS THEREFORE SAFE: the second attempt is refused rather than
	// producing a second location.
	var count int
	scanRow(t, `SELECT count(*) FROM sites WHERE company_id = (SELECT id FROM companies WHERE slug='one')
	              AND site_name = 'Site A' AND deleted_at IS NULL`, nil, &count)
	if count != 1 {
		t.Fatalf("company one has %d sites called Site A", count)
	}
}

func TestPublicCreateSiteValidatesItsFields(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "validate@example.com")

	cases := []struct {
		name, body, wantCode, wantParam string
	}{
		{
			"no name",
			`{"country":"NG","timezone":"Africa/Lagos"}`,
			models.CodeMissingField, "name",
		},
		{
			"no country",
			`{"name":"Nameless Country","timezone":"Africa/Lagos"}`,
			models.CodeMissingField, "country",
		},
		{
			"no timezone",
			`{"name":"Zoneless","country":"NG"}`,
			models.CodeMissingField, "timezone",
		},
		{
			// A zone this build cannot evaluate a schedule in.
			"unknown timezone",
			`{"name":"Bad Zone","country":"NG","timezone":"Mars/Olympus_Mons"}`,
			models.CodeInvalidField, "timezone",
		},
		{
			// An offset is a timezone at one moment in the year, not a
			// timezone -- it goes wrong at the next daylight-saving change.
			"an offset is not a zone",
			`{"name":"Offset","country":"NG","timezone":"+01:00"}`,
			models.CodeInvalidField, "timezone",
		},
		{
			// "Local" is whatever the SERVER's zone happens to be, which is
			// not a property of the customer's site.
			"Local is not a zone",
			`{"name":"Local Zone","country":"NG","timezone":"Local"}`,
			models.CodeInvalidField, "timezone",
		},
		{
			"unassigned country",
			`{"name":"Nowhere","country":"XX","timezone":"Africa/Lagos"}`,
			models.CodeInvalidField, "country",
		},
		{
			// A user-assigned code is reserved for private use and is not a
			// country any other system can interpret.
			"user-assigned country",
			`{"name":"Private Use","country":"AA","timezone":"Africa/Lagos"}`,
			models.CodeInvalidField, "country",
		},
		{
			"a country name rather than a code",
			`{"name":"Long Country","country":"Nigeria","timezone":"Africa/Lagos"}`,
			models.CodeInvalidField, "country",
		},
		{
			"empty name",
			`{"name":"  ","country":"NG","timezone":"Africa/Lagos"}`,
			models.CodeMissingField, "name",
		},
		{
			"active on a create",
			`{"name":"Born Inactive","country":"NG","timezone":"Africa/Lagos","active":false}`,
			models.CodeInvalidField, "active",
		},
		{
			"an unknown field",
			`{"name":"Extra","country":"NG","timezone":"Africa/Lagos","api_key":"x"}`,
			models.CodeUnknownField, "api_key",
		},
		{
			"naming the tenant",
			`{"name":"Tenant","country":"NG","timezone":"Africa/Lagos","company_id":"2"}`,
			models.CodeUnknownField, "company_id",
		},
	}

	for _, tc := range cases {
		status, headers, body, _ := publicCall(t, env, secret, http.MethodPost, sitesPath, tc.body, "")
		if status < 400 {
			t.Errorf("%s: accepted with %d (%v)", tc.name, status, body)
			continue
		}
		code := publicError(t, status, headers, body, models.APIErrors[tc.wantCode].Status)
		if code != tc.wantCode {
			t.Errorf("%s: code %s, want %s", tc.name, code, tc.wantCode)
			continue
		}
		detail, _ := body["error"].(map[string]any)
		if param, _ := detail["param"].(string); param != tc.wantParam {
			t.Errorf("%s: param %q, want %q", tc.name, param, tc.wantParam)
		}
	}

	// Nothing above created anything.
	var created int
	scanRow(t, `SELECT count(*) FROM sites WHERE site_name NOT IN ('Site A','Site B','Site C')`,
		nil, &created)
	if created != 0 {
		t.Fatalf("%d site(s) were created by requests that should have been refused", created)
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestPublicUpdateSiteAppliesOnlyWhatItWasSent(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "patcher@example.com")
	siteID := operatorSitePublicID(t, "Site A")

	status, _, body, _ := publicCall(t, env, secret, http.MethodPatch, sitesPath+"/"+siteID,
		`{"country":"GB","timezone":"Europe/London"}`, "")
	if status != http.StatusOK {
		t.Fatalf("PATCH /sites/{id} = %d (%v)", status, body)
	}
	if body["country"] != "GB" || body["timezone"] != "Europe/London" {
		t.Fatalf("the patch was not applied: %v", body)
	}
	// The name was never mentioned and must not have moved.
	if body["name"] != "Site A" {
		t.Errorf("name = %v; a patch blanked a field it was not sent", body["name"])
	}
	if body["active"] != true {
		t.Errorf("active = %v; a patch changed a field it was not sent", body["active"])
	}

	// Deactivation is reachable, reversible, and destroys nothing.
	status, _, body, _ = publicCall(t, env, secret, http.MethodPatch, sitesPath+"/"+siteID,
		`{"active":false}`, "")
	if status != http.StatusOK || body["active"] != false {
		t.Fatalf("deactivation = %d (%v)", status, body)
	}
	var deleted any
	scanRow(t, `SELECT deleted_at FROM sites WHERE public_id::text = $1`, []any{siteID}, &deleted)
	if deleted != nil {
		t.Fatal("deactivating a site through the public API soft-deleted it")
	}

	// The audit trail records what was ASKED FOR, not the whole row.
	var changes string
	scanRow(t, `SELECT changes::text FROM audit_events
	             WHERE action = 'SITE_UPDATED' AND target_public_id = $1
	             ORDER BY id DESC LIMIT 1`, []any{siteID}, &changes)
	if !strings.Contains(changes, `"active"`) {
		t.Errorf("the record does not name the change: %s", changes)
	}
	if strings.Contains(changes, `"timezone"`) {
		t.Errorf("the record restates a field this request did not send: %s", changes)
	}
}

func TestPublicUpdateSiteRefusesAnEmptyOrUnusableChange(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "patchvalidate@example.com")
	siteID := operatorSitePublicID(t, "Site A")

	cases := []struct {
		name, body, wantCode string
	}{
		{"nothing to change", `{}`, models.CodeInvalidField},
		{"an empty name", `{"name":""}`, models.CodeInvalidField},
		{"an empty country", `{"country":""}`, models.CodeInvalidField},
		{"an unassigned country", `{"country":"ZZ"}`, models.CodeInvalidField},
		{"an unknown zone", `{"timezone":"Nowhere/Nothing"}`, models.CodeInvalidField},
		{"an unknown field", `{"offline_policy":"OPEN"}`, models.CodeUnknownField},
		{"a duplicate name", `{"name":"Site B"}`, models.CodeSiteNameExists},
	}
	for _, tc := range cases {
		status, headers, body, _ := publicCall(t, env, secret, http.MethodPatch,
			sitesPath+"/"+siteID, tc.body, "")
		if code := publicError(t, status, headers, body,
			models.APIErrors[tc.wantCode].Status); code != tc.wantCode {
			t.Errorf("%s: code %s, want %s", tc.name, code, tc.wantCode)
		}
	}

	// Site A is exactly as it was. The fixture creates it with no zone, so the
	// column default (UTC) is what "unchanged" means here.
	var name, zone string
	scanRow(t, `SELECT site_name, timezone FROM sites WHERE public_id::text = $1`,
		[]any{siteID}, &name, &zone)
	if name != "Site A" || zone != "UTC" {
		t.Fatalf("a refused patch changed the site: name %q, timezone %q", name, zone)
	}
}

// ---------------------------------------------------------------------------
// Authorization
// ---------------------------------------------------------------------------

func TestPublicSiteWritesRequireTheWriteScope(t *testing.T) {
	env := newTestEnv(t)
	reader := siteReaderCredential(t, env, "one", "readonly@example.com")
	siteID := operatorSitePublicID(t, "Site A")

	// The read still works...
	if status, _, _, _ := publicGet(t, env, reader, sitesPath); status != http.StatusOK {
		t.Fatalf("a sites:read key cannot read sites")
	}

	// ...and neither write does.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, sitesPath, `{"name":"Denied","country":"NG","timezone":"Africa/Lagos"}`},
		{http.MethodPatch, sitesPath + "/" + siteID, `{"active":false}`},
	} {
		status, headers, body, _ := publicCall(t, env, reader, tc.method, tc.path, tc.body, "")
		if code := publicError(t, status, headers, body,
			http.StatusForbidden); code != models.CodeInsufficientScope {
			t.Errorf("%s %s with sites:read: code %s", tc.method, tc.path, code)
		}
	}

	var created int
	scanRow(t, `SELECT count(*) FROM sites WHERE site_name = 'Denied'`, nil, &created)
	if created != 0 {
		t.Fatal("a read-only key created a site")
	}
}

func TestSitesWriteImpliesSitesRead(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "implied@example.com")

	// The key was issued with sites:write alone. Implication is expanded at
	// issue time, so the read is a set membership test rather than an
	// inference at request time.
	if status, _, body, _ := publicGet(t, env, secret, sitesPath); status != http.StatusOK {
		t.Fatalf("a sites:write key cannot read sites: %d (%v)", status, body)
	}
}

func TestPublicSiteWritesCannotReachAnotherCompany(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "crosscompany@example.com")
	otherSite := operatorSitePublicID(t, "Site C") // company two

	status, headers, body, _ := publicCall(t, env, secret, http.MethodPatch,
		sitesPath+"/"+otherSite, `{"name":"Taken Over"}`, "")
	// 404 rather than 403: answering "forbidden" would confirm that the id
	// exists in somebody else's account.
	if code := publicError(t, status, headers, body,
		http.StatusNotFound); code != models.CodeResourceNotFound {
		t.Fatalf("another company's site: code %s", code)
	}

	var name string
	scanRow(t, `SELECT site_name FROM sites WHERE public_id::text = $1`, []any{otherSite}, &name)
	if name != "Site C" {
		t.Fatalf("a cross-company patch changed the other company's site to %q", name)
	}

	// A create lands in the CALLER's company, whatever else is in the request.
	status, _, body, _ = publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Mine Not Theirs","country":"NG","timezone":"Africa/Lagos"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("POST /sites = %d (%v)", status, body)
	}
	var slug string
	scanRow(t, `SELECT c.slug FROM sites s JOIN companies c ON c.id = s.company_id
	             WHERE s.public_id::text = $1`, []any{body["id"]}, &slug)
	if slug != "one" {
		t.Fatalf("the site was created in company %q", slug)
	}
}

func TestPublicSiteWritesRespectTheCredentialsSiteRestriction(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"restricted@example.com", models.RoleAdmin)

	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	secret := secretOf(t, issueCredential(t, env, token, csrf,
		`{"name":"restricted writer","scopes":["sites:write"],"site_ids":["`+siteA+`"]}`))

	// The site it is scoped to.
	if status, _, body, _ := publicCall(t, env, secret, http.MethodPatch,
		sitesPath+"/"+siteA, `{"address":"1 New Road"}`, ""); status != http.StatusOK {
		t.Fatalf("the restricted key cannot change its own site: %d (%v)", status, body)
	}

	// A site in the same company that it is scoped AWAY from: 403, not 404.
	// The site is the caller's own and telling them their key is scoped away
	// from it is the helpful answer.
	status, headers, body, _ := publicCall(t, env, secret, http.MethodPatch,
		sitesPath+"/"+siteB, `{"address":"nope"}`, "")
	if code := publicError(t, status, headers, body,
		http.StatusForbidden); code != models.CodeSiteNotPermitted {
		t.Errorf("a site outside the restriction: code %s", code)
	}
	var address any
	scanRow(t, `SELECT address FROM sites WHERE public_id::text = $1`, []any{siteB}, &address)
	if address != nil {
		t.Fatalf("the refused patch changed Site B's address to %v", address)
	}

	// AND IT MAY NOT CREATE. Creating is a company-wide act, and a key scoped
	// away from company-wide reach would otherwise be able to produce a site it
	// could then neither read nor change. site_not_permitted rather than
	// insufficient_scope: the key HOLDS sites:write; it is the site restriction
	// that forbids this.
	status, headers, body, _ = publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Added By A Narrow Key","country":"NG","timezone":"Africa/Lagos"}`, "")
	if code := publicError(t, status, headers, body,
		http.StatusForbidden); code != models.CodeSiteNotPermitted {
		t.Errorf("a site-restricted key creating a site: code %s", code)
	}
	detail, _ := body["error"].(map[string]any)
	if message, _ := detail["message"].(string); !strings.Contains(message, "cannot create") {
		t.Errorf("the refusal does not say what is wrong: %q", message)
	}
	var created int
	scanRow(t, `SELECT count(*) FROM sites WHERE site_name = 'Added By A Narrow Key'`, nil, &created)
	if created != 0 {
		t.Fatal("a site-restricted key created a site")
	}

	// An UNRESTRICTED key in the same company still can, so what was refused
	// above is the restriction and not the route.
	unrestricted := publicCredential(t, env, "one", "unrestricted-writer@example.com",
		`{"name":"unrestricted writer","scopes":["sites:write"]}`)
	if status, _, body, _ := publicCall(t, env, unrestricted, http.MethodPost, sitesPath,
		`{"name":"Added By A Wide Key","country":"NG","timezone":"Africa/Lagos"}`, ""); status != http.StatusCreated {
		t.Fatalf("an unrestricted key cannot create either: %d (%v)", status, body)
	}
}

func TestPublicSiteRoutesAnswerAMalformedIDAsNotFound(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "malformed@example.com")

	// PERCENT-ENCODED ON THE WIRE, decoded by the router before the handler
	// sees it -- which is the point: what is under test is what a handler does
	// with a nonsense id, and a space or a quote in a raw request target is not
	// a request any client could send in the first place.
	for _, id := range []string{"1", "not-a-uuid", "00000000-0000-0000-0000-000000000000",
		"' OR 1=1 --", strings.Repeat("a", 200)} {
		escaped := url.PathEscape(id)
		status, headers, body, _ := publicCall(t, env, secret, http.MethodPatch,
			sitesPath+"/"+escaped, `{"active":false}`, "")
		if code := publicError(t, status, headers, body,
			http.StatusNotFound); code != models.CodeResourceNotFound {
			t.Errorf("PATCH with id %q: code %s, want resource_not_found", id, code)
		}

		status, headers, body, _ = publicGet(t, env, secret, sitesPath+"/"+escaped)
		if code := publicError(t, status, headers, body,
			http.StatusNotFound); code != models.CodeResourceNotFound {
			t.Errorf("GET with id %q: code %s, want resource_not_found", id, code)
		}
	}

	// A TRAVERSAL-SHAPED ID NEVER REACHES THE HANDLER, and is checked
	// separately because of it. The router resolves the dot segments before it
	// matches, so the request lands on no route at all and gets gin's own
	// plain-text 404 rather than this API's envelope. Still a 404, still
	// nothing read or written -- but asserting the envelope here would be
	// asserting something the handler was never asked.
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		req := httptest.NewRequest(method, sitesPath+"/"+url.PathEscape("../../etc/passwd"), nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s with a traversal-shaped id = %d, want 404", method, w.Code)
		}
	}

	// And nothing was changed by any of them.
	var changed int
	scanRow(t, `SELECT count(*) FROM sites WHERE active = FALSE`, nil, &changed)
	if changed != 0 {
		t.Fatalf("%d site(s) were deactivated by a malformed id", changed)
	}
}

// ---------------------------------------------------------------------------
// The read path still answers what it answered before, plus country
// ---------------------------------------------------------------------------

func TestPublicSiteReadsCarryCountryAndNothingElseNew(t *testing.T) {
	env := newTestEnv(t)
	secret := siteWriterCredential(t, env, "one", "readshape@example.com")

	status, _, created, _ := publicCall(t, env, secret, http.MethodPost, sitesPath,
		`{"name":"Shape Check","country":"GB","timezone":"Europe/London"}`, "")
	if status != http.StatusCreated {
		t.Fatalf("POST /sites = %d (%v)", status, created)
	}
	id, _ := created["id"].(string)

	status, _, read, _ := publicGet(t, env, secret, sitesPath+"/"+id)
	if status != http.StatusOK {
		t.Fatalf("GET /sites/{id} = %d", status)
	}

	want := map[string]bool{
		"id": true, "name": true, "address": true, "country": true, "timezone": true,
		"active": true, "terminal_count": true, "created_at": true,
	}
	for key := range read {
		if !want[key] {
			t.Errorf("the site projection has grown a field: %q", key)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("the site projection lost %q", key)
	}

	// A site the console created has no country, and the field is present and
	// empty rather than absent -- a client should not have to tell an absent
	// key from an unknown country.
	status, _, legacy, _ := publicGet(t, env, secret, sitesPath+"/"+operatorSitePublicID(t, "Site A"))
	if status != http.StatusOK {
		t.Fatalf("GET on a console-created site = %d", status)
	}
	if got, present := legacy["country"]; !present || got != "" {
		t.Errorf("a site with no country reported country = %v (present %v)", got, present)
	}
}
