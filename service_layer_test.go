package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/service"
)

// P2: the service layer, exercised against the database with real credentials
// issued through the console. No public route exists; every test here drives
// the services directly, the way a P3 handler will, and proves that the tenant
// a service acts as comes from the credential and from nothing else.

// tenantFor issues a credential in a company through the console and
// authenticates it the way the public middleware will -- so the TenantContext
// under test was produced by the real store, not assembled by the test.
func tenantFor(t *testing.T, env *testEnv, slug, email, body string) (*service.TenantContext, string) {
	t.Helper()
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, slug)
	_, token, csrf := consoleOperatorSession(t, env.router, companyID, email, models.RoleAdmin)
	issued := issueCredential(t, env, token, csrf, body)
	secret := secretOf(t, issued)
	tc, err := service.Authenticate(secret, models.APIEnvironmentLive, "test-request")
	if err != nil {
		t.Fatalf("authenticating a freshly issued credential: %v", err)
	}
	if tc.CompanyID() != companyID {
		t.Fatalf("credential resolved to company %d, want %d", tc.CompanyID(), companyID)
	}
	return tc, secret
}

func cursorSigner(t *testing.T) *models.CursorSigner {
	t.Helper()
	signer, err := models.NewCursorSigner([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	svcErr, ok := service.As(err)
	if !ok {
		t.Fatalf("expected a service error, got %T: %v", err, err)
	}
	return svcErr.Code()
}

// ---------------------------------------------------------------------------
// Tenant context comes from the credential row
// ---------------------------------------------------------------------------

func TestServiceTenantComesFromTheCredentialRowOnly(t *testing.T) {
	env := newTestEnv(t)
	one, _ := tenantFor(t, env, "one", "svc-one@example.com",
		`{"name":"svc one","scopes":["members:read"]}`)
	two, _ := tenantFor(t, env, "two", "svc-two@example.com",
		`{"name":"svc two","scopes":["members:read"]}`)

	if one.CompanyID() == two.CompanyID() {
		t.Fatal("two companies' credentials resolved to the same tenant")
	}
	if one.CompanyID() != operatorCompanyID(t, "one") || two.CompanyID() != operatorCompanyID(t, "two") {
		t.Error("tenant ids do not match the issuing companies")
	}
}

func TestARevokedCredentialNoLongerAuthenticates(t *testing.T) {
	env := newTestEnv(t)
	cheapBcrypt(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID, "svc-rev@example.com", models.RoleAdmin)
	issued := issueCredential(t, env, token, csrf, `{"name":"to revoke","scopes":["members:read"]}`)
	secret := secretOf(t, issued)

	if _, err := service.Authenticate(secret, models.APIEnvironmentLive, "r"); err != nil {
		t.Fatalf("live credential refused: %v", err)
	}
	if code, _ := consoleCall(t, env.router, "DELETE", credentialsPath+"/"+issued["id"].(string),
		`{"reason":"test"}`, token, csrf); code != http.StatusOK {
		t.Fatalf("revoke = %d", code)
	}
	_, err := service.Authenticate(secret, models.APIEnvironmentLive, "r")
	if codeOf(t, err) != models.CodeCredentialInvalid {
		t.Errorf("revoked credential = %v, want api_credential_invalid", err)
	}
}

// ---------------------------------------------------------------------------
// Foreign-tenant lookups are not found
// ---------------------------------------------------------------------------

func TestForeignCompanyMembersResolveAsNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "ONE-001", "One Person")
	env.createMember(env.siteCKey, "TWO-001", "Two Person")

	two, _ := tenantFor(t, env, "two", "svc-two@example.com",
		`{"name":"reader","scopes":["members:read","members:write"]}`)
	members := service.NewMemberService(cursorSigner(t))
	ctx := context.Background()

	// Company two's own person is readable...
	if m, err := members.Get(ctx, two, "TWO-001"); err != nil || m.MemberID != "TWO-001" {
		t.Fatalf("own member: %v %v", m, err)
	}
	// ...and company one's is exactly as absent as a made-up id.
	_, errForeign := members.Get(ctx, two, "ONE-001")
	_, errMissing := members.Get(ctx, two, "NOPE-999")
	if codeOf(t, errForeign) != models.CodeResourceNotFound || codeOf(t, errMissing) != models.CodeResourceNotFound {
		t.Errorf("foreign=%v missing=%v; both must be resource_not_found", errForeign, errMissing)
	}
	if fmt.Sprint(errForeign) != fmt.Sprint(errMissing) {
		t.Error("a foreign row and a missing row must be indistinguishable")
	}

	// Writes cannot reach across either.
	if _, err := members.Update(ctx, two, "ONE-001", service.MemberInput{FullName: "Hijacked"}); codeOf(t, err) != models.CodeResourceNotFound {
		t.Errorf("update of a foreign member = %v", err)
	}
	// A foreign delete is a silent no-op (section 18: 204 for "gone" and
	// "never here" alike), and the row must be untouched.
	if removed, err := members.Delete(ctx, two, "ONE-001"); err != nil || removed {
		t.Errorf("delete of a foreign member = removed %v, err %v; want a no-op", removed, err)
	}
	if name := queryString(t, `SELECT full_name FROM people WHERE external_id = 'ONE-001'`); name != "One Person" {
		t.Errorf("company one's person was touched: %q", name)
	}
	if queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'ONE-001' AND deleted_at IS NULL`) != 1 {
		t.Error("company one's person was deleted through company two's credential")
	}

	// And the list never crosses.
	page, err := members.List(ctx, two, service.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.MemberID == "ONE-001" {
			t.Error("company two's list contains company one's person")
		}
	}
}

func TestForeignCompanySitesResolveAsNotFoundAndRestrictedOnesAsForbidden(t *testing.T) {
	env := newTestEnv(t)
	siteA := operatorSitePublicID(t, "Site A")
	siteB := operatorSitePublicID(t, "Site B")
	siteC := operatorSitePublicID(t, "Site C")

	// Company one, restricted to Site A only.
	one, _ := tenantFor(t, env, "one", "svc-sites@example.com",
		`{"name":"site a only","scopes":["sites:read"],"site_ids":["`+siteA+`"]}`)
	sites := service.NewSiteService()
	ctx := context.Background()

	if s, err := sites.Get(ctx, one, siteA); err != nil || s.ID != siteA || s.Name != "Site A" {
		t.Fatalf("own permitted site: %v %v", s, err)
	}
	// Same company, outside the restriction: the caller's own site, so 403.
	if _, err := sites.Get(ctx, one, siteB); codeOf(t, err) != models.CodeSiteNotPermitted {
		t.Errorf("site outside the credential's restriction = %v, want site_not_permitted", err)
	}
	// Another company's site: 404, never 403 -- 403 would confirm it exists.
	if _, err := sites.Get(ctx, one, siteC); codeOf(t, err) != models.CodeResourceNotFound {
		t.Errorf("foreign site = %v, want resource_not_found", err)
	}
	if _, err := sites.Get(ctx, one, "not-a-uuid"); codeOf(t, err) != models.CodeResourceNotFound {
		t.Errorf("malformed id = %v, want resource_not_found (not a cast error)", err)
	}

	list, err := sites.List(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != siteA {
		t.Errorf("restricted list = %v, want only Site A", list)
	}

	// Unrestricted credential in the same company sees both of its sites and
	// still not company two's.
	all, _ := tenantFor(t, env, "one", "svc-all@example.com",
		`{"name":"all sites","scopes":["sites:read"]}`)
	list, err = sites.List(ctx, all)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Site A,Site B" {
		t.Errorf("unrestricted list = %v, want Site A, Site B", names)
	}
}

// ---------------------------------------------------------------------------
// Scope checks
// ---------------------------------------------------------------------------

func TestServicesRefuseOperationsOutsideTheCredentialScopes(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "ONE-001", "One Person")
	readOnly, _ := tenantFor(t, env, "one", "svc-ro@example.com",
		`{"name":"read only","scopes":["members:read"]}`)
	members := service.NewMemberService(cursorSigner(t))
	sites := service.NewSiteService()
	ctx := context.Background()

	if _, err := members.Get(ctx, readOnly, "ONE-001"); err != nil {
		t.Fatalf("members:read must allow Get: %v", err)
	}
	if _, err := members.Create(ctx, readOnly, service.MemberInput{MemberID: "X", FullName: "X", MembershipType: "X"}); codeOf(t, err) != models.CodeInsufficientScope {
		t.Errorf("create without members:write = %v", err)
	}
	if _, err := members.Update(ctx, readOnly, "ONE-001", service.MemberInput{FullName: "Y"}); codeOf(t, err) != models.CodeInsufficientScope {
		t.Errorf("update without members:write = %v", err)
	}
	if _, err := members.Delete(ctx, readOnly, "ONE-001"); codeOf(t, err) != models.CodeInsufficientScope {
		t.Errorf("delete without members:write = %v", err)
	}
	if _, err := sites.List(ctx, readOnly); codeOf(t, err) != models.CodeInsufficientScope {
		t.Errorf("sites without sites:read = %v", err)
	}
	if queryInt(t, `SELECT count(*) FROM people WHERE external_id = 'X'`) != 0 {
		t.Error("a refused create still wrote a row")
	}
}

// ---------------------------------------------------------------------------
// Member writes take the same path as the console and the legacy API
// ---------------------------------------------------------------------------

func TestServiceMemberWritesFanOutToTerminalsLikeTheLegacyPath(t *testing.T) {
	env := newTestEnv(t)
	deviceKey := env.registerDevice(env.siteAKey, "SVC-TERM-1")
	writer, _ := tenantFor(t, env, "one", "svc-w@example.com",
		`{"name":"writer","scopes":["members:write"]}`)
	members := service.NewMemberService(cursorSigner(t))
	ctx := context.Background()

	created, err := members.Create(ctx, writer, service.MemberInput{
		MemberID: "SVC-001", FullName: "Service Person", MembershipType: "PREMIUM"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.MemberID != "SVC-001" || !created.Active {
		t.Errorf("created projection = %+v", created)
	}
	if !contains(jobTypes(env.jobs(deviceKey)), models.SyncJobCreate) {
		t.Error("the terminal was not told about the new person (no CREATE job)")
	}

	// members:write implies members:read at issue time, so the writer reads.
	got, err := members.Get(ctx, writer, "SVC-001")
	if err != nil || got.FullName != "Service Person" {
		t.Fatalf("read back: %v %v", got, err)
	}

	inactive := false
	updated, err := members.Update(ctx, writer, "SVC-001", service.MemberInput{Active: &inactive})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Active || updated.FullName != "Service Person" {
		t.Errorf("a partial update must keep unmentioned fields: %+v", updated)
	}
	if !contains(jobTypes(env.jobs(deviceKey)), models.SyncJobUpdate) {
		t.Error("no UPDATE job after the service update")
	}

	if removed, err := members.Delete(ctx, writer, "SVC-001"); err != nil || !removed {
		t.Fatalf("delete: removed %v, err %v", removed, err)
	}
	if !contains(jobTypes(env.jobs(deviceKey)), models.SyncJobDelete) {
		t.Error("no DELETE job after the service delete")
	}
	if removed, err := members.Delete(ctx, writer, "SVC-001"); err != nil || removed {
		t.Errorf("deleting twice = removed %v, err %v; want an idempotent no-op", removed, err)
	}

	// The legacy wrapper is unchanged: a repeated DELETE there is still a
	// silent no-op.
	if err := database.DeleteMember(writer.CompanyID(), "SVC-001"); err != nil {
		t.Errorf("legacy DeleteMember of a deleted member = %v, want nil", err)
	}
}

func TestServiceMemberCreateMapsStoreRefusalsToRegistryCodes(t *testing.T) {
	env := newTestEnv(t)
	env.createMember(env.siteAKey, "DUP-001", "Already Here")
	writer, _ := tenantFor(t, env, "one", "svc-dup@example.com",
		`{"name":"writer","scopes":["members:write"]}`)
	members := service.NewMemberService(cursorSigner(t))
	ctx := context.Background()

	_, err := members.Create(ctx, writer, service.MemberInput{MemberID: "DUP-001", FullName: "Again", MembershipType: "BASIC"})
	if codeOf(t, err) != models.CodeMemberIDExists {
		t.Errorf("duplicate id = %v, want member_id_already_exists", err)
	}
	_, err = members.Create(ctx, writer, service.MemberInput{MemberID: "has space", FullName: "Bad", MembershipType: "BASIC"})
	if codeOf(t, err) != models.CodeMemberIDUnusable {
		t.Errorf("unusable id = %v, want member_id_unusable", err)
	}
	_, err = members.Create(ctx, writer, service.MemberInput{FullName: "No id", MembershipType: "BASIC"})
	if svc, _ := service.As(err); svc == nil || svc.Code() != models.CodeMissingField || svc.Param() != "member_id" {
		t.Errorf("missing id = %v, want missing_field on member_id", err)
	}
}

// ---------------------------------------------------------------------------
// Pagination is bound to the tenant
// ---------------------------------------------------------------------------

func TestMemberListPagesByKeysetAndCursorsAreTenantBound(t *testing.T) {
	env := newTestEnv(t)
	for i := 1; i <= 5; i++ {
		env.createMember(env.siteAKey, fmt.Sprintf("PAGE-%03d", i), "Person")
	}
	env.createMember(env.siteCKey, "OTHER-001", "Elsewhere")
	one, _ := tenantFor(t, env, "one", "svc-page@example.com", `{"name":"pager","scopes":["members:read"]}`)
	two, _ := tenantFor(t, env, "two", "svc-page2@example.com", `{"name":"pager","scopes":["members:read"]}`)
	members := service.NewMemberService(cursorSigner(t))
	ctx := context.Background()

	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := members.List(ctx, one, service.PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, m := range page.Items {
			seen = append(seen, m.MemberID)
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Error("no more pages but a cursor was issued")
			}
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	// Newest first (API_SPEC.md section 18), three pages of two.
	if strings.Join(seen, ",") != "PAGE-005,PAGE-004,PAGE-003,PAGE-002,PAGE-001" || pages != 3 {
		t.Errorf("paged %v in %d pages", seen, pages)
	}

	// An out-of-range limit is refused, not clamped.
	for _, limit := range []int{-1, 201} {
		if _, err := members.List(ctx, one, service.PageRequest{Limit: limit}); codeOf(t, err) != models.CodeInvalidField {
			t.Errorf("limit %d = %v, want invalid_field", limit, err)
		}
	}

	// A cursor minted for company one is refused for company two.
	first, _ := members.List(ctx, one, service.PageRequest{Limit: 2})
	if _, err := members.List(ctx, two, service.PageRequest{Cursor: first.NextCursor}); codeOf(t, err) != models.CodeCursorInvalid {
		t.Errorf("another tenant's cursor = %v, want cursor_invalid", err)
	}
	if _, err := members.List(ctx, one, service.PageRequest{Cursor: "garbage.garbage"}); codeOf(t, err) != models.CodeCursorInvalid {
		t.Errorf("forged cursor = %v, want cursor_invalid", err)
	}
	// A service built without a signer cannot serve a cursor at all.
	if _, err := service.NewMemberService(nil).List(ctx, one, service.PageRequest{Cursor: first.NextCursor}); codeOf(t, err) != models.CodeInternalError {
		t.Errorf("unsigned list = %v, want internal_error", err)
	}
}

// ---------------------------------------------------------------------------
// Error mapping at the handler edge
// ---------------------------------------------------------------------------

func TestRespondServiceErrorServesRegistryShapesAndHidesCauses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	serve := func(err error) (int, map[string]any) {
		r := gin.New()
		r.Use(middleware.RequestIDMiddleware())
		r.GET("/x", func(c *gin.Context) { handlers.RespondServiceError(c, "test op", err) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return w.Code, body["error"].(map[string]any)
	}

	cases := []struct {
		err    error
		status int
		code   string
		typ    string
	}{
		{service.ErrNotFound(), 404, models.CodeResourceNotFound, "not_found_error"},
		{service.ErrInsufficientScope("members:write"), 403, models.CodeInsufficientScope, "permission_error"},
		{service.ErrMissingField("full_name"), 400, models.CodeMissingField, "invalid_request_error"},
		{service.ErrCursorExpired(), 410, models.CodeCursorExpired, "gone_error"},
		{service.ErrInternal(errors.New("pq: relation people does not exist")), 500, models.CodeInternalError, "api_error"},
		{service.ErrUnavailable(errors.New("dial tcp: refused")), 503, models.CodeServiceUnavailable, "api_error"},
		{errors.New("raw error that escaped"), 500, models.CodeInternalError, "api_error"},
	}
	for _, c := range cases {
		status, detail := serve(c.err)
		if status != c.status || detail["code"] != c.code || detail["type"] != c.typ {
			t.Errorf("%v -> %d %v, want %d %s/%s", c.err, status, detail, c.status, c.typ, c.code)
		}
		if detail["request_id"] == nil || detail["request_id"] == "" {
			t.Errorf("%v: no request_id in the body", c.err)
		}
		msg, _ := detail["message"].(string)
		for _, leak := range []string{"pq:", "relation", "dial tcp", "raw error"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%v: internal text %q reached the public message %q", c.err, leak, msg)
			}
		}
	}

	// A param travels; a specific message replaces the default.
	_, detail := serve(service.ErrMissingField("full_name"))
	if detail["param"] != "full_name" || !strings.Contains(detail["message"].(string), "full_name") {
		t.Errorf("param/message not carried: %v", detail)
	}
}

// ---------------------------------------------------------------------------
// The middleware: request -> credential -> tenant, and nothing else
// ---------------------------------------------------------------------------

// publicProbe mounts the (otherwise unmounted) credential middleware on a
// throwaway engine with a handler that reports what the request resolved to.
func publicProbe(t *testing.T, scope string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.RequestIDMiddleware())
	group := r.Group("/probe", middleware.APICredentialAuthMiddleware(models.APIEnvironmentLive))
	if scope != "" {
		group.Use(middleware.RequireScope(scope))
	}
	group.POST("", func(c *gin.Context) {
		tc := middleware.Tenant(c)
		c.JSON(http.StatusOK, gin.H{
			"tenant":            tc.CompanyID(),
			"legacy_company_id": c.GetInt64("company_id"),
			"prefix":            tc.KeyPrefix(),
		})
	})
	return r
}

func probe(r *gin.Engine, authorization, body string) (int, map[string]any) {
	req := httptest.NewRequest("POST", "/probe?company_id=999999", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestCredentialMiddlewareResolvesTenantFromTheCredentialAndIgnoresTheRequest(t *testing.T) {
	env := newTestEnv(t)
	one, secret := tenantFor(t, env, "one", "svc-mw@example.com", `{"name":"mw","scopes":["members:read"]}`)
	r := publicProbe(t, "")

	// A body and a query string both naming another company change nothing.
	code, out := probe(r, "Bearer "+secret, `{"company_id":999999,"tenant":"two"}`)
	if code != http.StatusOK {
		t.Fatalf("authenticated probe = %d %v", code, out)
	}
	if int64(out["tenant"].(float64)) != one.CompanyID() {
		t.Errorf("tenant = %v, want %d from the credential", out["tenant"], one.CompanyID())
	}
	if out["legacy_company_id"].(float64) != 0 {
		t.Error("the middleware set the console/site-key company_id key; the two worlds must not share one")
	}

	for name, header := range map[string]string{
		"missing":     "",
		"basic":       "Basic abc",
		"malformed":   "Bearer nope",
		"unknown":     "Bearer atp_live_" + strings.Repeat("0", 64),
		"environment": "Bearer atp_test_" + strings.Repeat("0", 64),
	} {
		code, out := probe(r, header, "{}")
		if code != http.StatusUnauthorized {
			t.Errorf("%s: %d %v, want 401", name, code, out)
			continue
		}
		detail := out["error"].(map[string]any)
		want := models.CodeCredentialInvalid
		switch name {
		case "missing", "basic":
			want = models.CodeCredentialMissing
		case "environment":
			want = models.CodeCredentialWrongEnvironment
		}
		if detail["code"] != want {
			t.Errorf("%s: code %v, want %s", name, detail["code"], want)
		}
	}

	// Scope gate at mount time.
	code, out = probe(publicProbe(t, models.ScopeMembersWrite), "Bearer "+secret, "{}")
	if code != http.StatusForbidden || out["error"].(map[string]any)["code"] != models.CodeInsufficientScope {
		t.Errorf("RequireScope(members:write) on a read-only key = %d %v", code, out)
	}
}

// The throwaway probe engine above is never part of the real router.
func TestTheProbeRouteIsNotOnTheRealRouter(t *testing.T) {
	env := newTestEnv(t)
	for _, route := range env.router.Routes() {
		if strings.Contains(route.Path, "/probe") {
			t.Errorf("a test-only route is mounted: %s %s", route.Method, route.Path)
		}
	}
}
