package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// Unit tests that need no database: the tenant context, the error type, the
// bearer parser, and the structural guarantee that no service can be handed a
// tenant by anything but an authenticated credential.

func TestTenantContextIsBuiltOnlyFromACredentialIdentity(t *testing.T) {
	tc := FromCredential(&models.APICredentialIdentity{
		ID: 7, CompanyID: 42, KeyPrefix: "atp_live_deadbeef", Environment: "live",
		Scopes: []string{"members:read", "sites:read"}, SiteIDs: []int64{3, 5},
	}, "req-1")

	if tc.CompanyID() != 42 || tc.CredentialID() != 7 || tc.RequestID() != "req-1" {
		t.Fatalf("context did not carry the identity: %+v", tc)
	}
	if !tc.HasScope("members:read") || tc.HasScope("members:write") {
		t.Error("scope membership is wrong")
	}
	if err := tc.RequireScope("members:write"); !errors.Is(err, ErrInsufficientScope("members:write")) {
		t.Errorf("RequireScope on a missing scope = %v, want insufficient_scope", err)
	}
	if !tc.ReachesSite(3) || tc.ReachesSite(4) {
		t.Error("site restriction is wrong")
	}
	if err := tc.RequireSite(4); !errors.Is(err, ErrSiteNotPermitted()) {
		t.Errorf("RequireSite outside the restriction = %v, want site_not_permitted", err)
	}

	// The copies handed back are copies: mutating them cannot widen the context.
	tc.Scopes()[0] = "members:write"
	tc.SiteIDs()[0] = 4
	if tc.HasScope("members:write") || tc.ReachesSite(4) {
		t.Error("a returned slice aliases the context's own")
	}

	if FromCredential(nil, "x") != nil {
		t.Error("a nil identity must not produce a context")
	}
}

func TestAnUnrestrictedCredentialReachesEverySite(t *testing.T) {
	tc := FromCredential(&models.APICredentialIdentity{CompanyID: 1}, "")
	if tc.RestrictedToSites() || !tc.ReachesSite(999) {
		t.Error("an empty restriction must mean every site in the company")
	}
}

// THE STRUCTURAL GUARANTEE. Every exported method on every service type takes
// (context.Context, *TenantContext) first and never an int64 that could be a
// company id; no exported input struct has a field that names a tenant; and
// TenantContext itself has no exported field, so nothing can assemble one from
// request data. If this test fails, a way to hand a service a tenant from a
// request has been added, and the review has to say why.
func TestNoServiceAcceptsATenantFromItsCaller(t *testing.T) {
	services := []any{&MemberService{}, &SiteService{}}
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	tenantType := reflect.TypeOf(&TenantContext{})

	for _, svc := range services {
		rt := reflect.TypeOf(svc)
		for i := 0; i < rt.NumMethod(); i++ {
			m := rt.Method(i)
			// Method type includes the receiver as In(0).
			if m.Type.NumIn() < 3 || m.Type.In(1) != ctxType || m.Type.In(2) != tenantType {
				t.Errorf("%s.%s must take (context.Context, *TenantContext) first; has %v",
					rt.Elem().Name(), m.Name, m.Type)
			}
			for j := 3; j < m.Type.NumIn(); j++ {
				if k := m.Type.In(j).Kind(); k == reflect.Int64 || k == reflect.Int {
					t.Errorf("%s.%s takes an integer argument %v that could carry a tenant id",
						rt.Elem().Name(), m.Name, m.Type.In(j))
				}
			}
		}
	}

	for _, in := range []any{MemberInput{}, PageRequest{}} {
		rt := reflect.TypeOf(in)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.ToLower(rt.Field(i).Name)
			if strings.Contains(name, "company") || strings.Contains(name, "tenant") {
				t.Errorf("%s.%s names a tenant; inputs may not", rt.Name(), rt.Field(i).Name)
			}
		}
	}

	tt := reflect.TypeOf(TenantContext{})
	for i := 0; i < tt.NumField(); i++ {
		if tt.Field(i).IsExported() {
			t.Errorf("TenantContext.%s is exported; a handler could set it", tt.Field(i).Name)
		}
	}
}

func TestServiceErrorsCarryRegisteredCodesAndTheirStatuses(t *testing.T) {
	cases := []struct {
		err    *Error
		code   string
		status int
	}{
		{ErrNotFound(), models.CodeResourceNotFound, http.StatusNotFound},
		{ErrInsufficientScope("x"), models.CodeInsufficientScope, http.StatusForbidden},
		{ErrSiteNotPermitted(), models.CodeSiteNotPermitted, http.StatusForbidden},
		{ErrTenantIdentity("company_id"), models.CodeTenantIdentity, http.StatusBadRequest},
		{ErrMissingField("full_name"), models.CodeMissingField, http.StatusBadRequest},
		{ErrMemberIDUnusable(), models.CodeMemberIDUnusable, http.StatusBadRequest},
		{ErrMemberIDExists(), models.CodeMemberIDExists, http.StatusConflict},
		{ErrRosterOverCapacity("T1", 300, 256), models.CodeRosterOverCapacity, http.StatusConflict},
		{ErrCursorInvalid(), models.CodeCursorInvalid, http.StatusBadRequest},
		{ErrCursorExpired(), models.CodeCursorExpired, http.StatusGone},
		{ErrCredentialMissing(), models.CodeCredentialMissing, http.StatusUnauthorized},
		{ErrCredentialInvalid(), models.CodeCredentialInvalid, http.StatusUnauthorized},
		{ErrCredentialWrongEnvironment(), models.CodeCredentialWrongEnvironment, http.StatusUnauthorized},
		{ErrInternal(errors.New("boom")), models.CodeInternalError, http.StatusInternalServerError},
		{ErrUnavailable(errors.New("down")), models.CodeServiceUnavailable, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		if c.err.Code() != c.code || c.err.Status() != c.status {
			t.Errorf("%v: code=%s status=%d, want %s/%d", c.err, c.err.Code(), c.err.Status(), c.code, c.status)
		}
		if !models.KnownAPIErrorCode(c.err.Code()) {
			t.Errorf("%s is not in the registry", c.err.Code())
		}
	}

	// The cause is reachable for the log line and absent from the message.
	cause := errors.New("pq: connection refused")
	wrapped := ErrInternal(cause)
	if !errors.Is(wrapped, cause) {
		t.Error("the cause must unwrap")
	}
	if wrapped.Message() != "" {
		t.Error("an internal error must not carry its cause as the public message")
	}
	if got, ok := As(wrapped); !ok || got != wrapped {
		t.Error("As must find a service error at the top of a chain")
	}
	if _, ok := As(cause); ok {
		t.Error("As must not invent a service error from a plain one")
	}
}

func TestAnUnregisteredCodeCannotBeConstructed(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newError accepted a code the registry does not know")
		}
	}()
	newError("made_up_code", "", "", nil)
}

func TestBearerCredentialParsing(t *testing.T) {
	cases := map[string]string{
		"Bearer atp_live_abc": "atp_live_abc",
		"bearer atp_live_abc": "atp_live_abc",
		"  Bearer   atp_x  ":  "atp_x",
		"Basic dXNlcjpwYXNz":  "",
		"atp_live_abc":        "",
		"":                    "",
		"Bearer":              "",
		"Token atp_live_abc":  "",
		"Bearer atp_live_a b": "atp_live_a b", // the caller validates shape; this only strips the scheme
	}
	for header, want := range cases {
		if got := BearerCredential(header); got != want {
			t.Errorf("BearerCredential(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestAuthenticateRefusesBeforeTheDatabaseIsNeeded(t *testing.T) {
	// None of these reach the store, so no database is required.
	if _, err := Authenticate("", "live", "r"); !errors.Is(err, ErrCredentialMissing()) {
		t.Errorf("empty credential = %v, want api_credential_missing", err)
	}
	if _, err := Authenticate("not-a-key", "live", "r"); !errors.Is(err, ErrCredentialInvalid()) {
		t.Errorf("malformed credential = %v, want api_credential_invalid", err)
	}
	testKey := "atp_test_" + strings.Repeat("ab", 32)
	if _, err := Authenticate(testKey, "live", "r"); !errors.Is(err, ErrCredentialWrongEnvironment()) {
		t.Errorf("test key on live = %v, want api_credential_wrong_environment", err)
	}
}

func TestPageSizeIsClampedNotRefused(t *testing.T) {
	for in, want := range map[int]int{0: DefaultPageSize, -5: DefaultPageSize, 1: 1, 200: 200, 5000: MaxPageSize} {
		if got := pageSize(in); got != want {
			t.Errorf("pageSize(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestMemberInputValidation(t *testing.T) {
	if err := validateMemberInput(MemberInput{}, true); !errors.Is(err, ErrMissingField("member_id")) {
		t.Errorf("empty create = %v", err)
	}
	if err := validateMemberInput(MemberInput{MemberID: "M1"}, true); !errors.Is(err, ErrMissingField("full_name")) {
		t.Errorf("no name = %v", err)
	}
	if err := validateMemberInput(MemberInput{}, false); err != nil {
		t.Errorf("an empty update is valid (nothing changes) but got %v", err)
	}
	long := strings.Repeat("n", MaxMemberNameLength+1)
	if err := validateMemberInput(MemberInput{FullName: long}, false); !errors.Is(err, ErrInvalidField("full_name", "")) {
		t.Errorf("overlong name = %v", err)
	}
}
