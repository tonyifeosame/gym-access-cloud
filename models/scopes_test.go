package models

import (
	"errors"
	"reflect"
	"testing"
)

// The scope registry.
//
// What is being protected here is mostly structural: that the set is closed,
// that implication is expanded once rather than inferred, and that a route
// cannot be gated on a scope nobody defined.

// atLeast is the role comparison the middleware supplies. Duplicated here rather
// than imported, because models must not depend on middleware -- and a two-line
// comparison is cheaper to restate than an import cycle is to break.
func atLeast(role, minimum string) bool {
	rank := map[string]int{RoleViewer: 1, RoleManager: 2, RoleAdmin: 3, RoleOwner: 4}
	want, known := rank[minimum]
	if !known {
		return false
	}
	return rank[role] >= want
}

func TestEveryRegisteredScopeIsWellFormed(t *testing.T) {
	if len(Scopes) == 0 {
		t.Fatal("the scope registry is empty")
	}

	for name, spec := range Scopes {
		if spec.Name != name {
			t.Errorf("scope %q is registered under key %q", spec.Name, name)
		}
		if !OperatorRoles[spec.MinRole] {
			t.Errorf("scope %q names MinRole %q, which is not an operator role",
				name, spec.MinRole)
		}
		// A scope that implies something undefined would grant a capability
		// nothing can describe, and ExpandScopes refuses it at issue time -- but
		// the registry should not contain one in the first place.
		for _, implied := range spec.Implies {
			if !KnownScope(implied) {
				t.Errorf("scope %q implies %q, which is not registered", name, implied)
			}
		}
		if spec.Description == "" {
			t.Errorf("scope %q has no description; the console renders one", name)
		}
	}
}

// The registry and the database CHECK in migrations/030 must hold the same set.
// They are two independent closed sets, and the failure if they drift is
// asymmetric and quiet: Go accepts a scope the database then refuses to store,
// which surfaces as a 500 on the issue path.
func TestScopeSetMatchesTheDocumentedV1Set(t *testing.T) {
	want := []string{
		"access:read",
		"events:read",
		"members:read",
		"members:write",
		"sites:read",
		"terminals:read",
		"webhooks:manage",
	}
	if got := AllScopes(); !reflect.DeepEqual(got, want) {
		t.Errorf("scope set = %v, want %v\n"+
			"If this is a deliberate change, migrations/030_api_credentials.sql "+
			"carries the same list as a CHECK and must change with it.", got, want)
	}
}

func TestUnknownScopeIsRefusedRatherThanDropped(t *testing.T) {
	_, err := ExpandScopes([]string{"members:read", "members:delete"})
	if !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("expanding an unknown scope = %v, want ErrUnknownScope", err)
	}

	// Silently dropping it would produce a credential the operator believes can
	// do something it cannot -- which they discover from a customer.
	if _, err := ExpandScopes([]string{"members:delete"}); err == nil {
		t.Error("a set of only unknown scopes was accepted")
	}
}

func TestEmptyScopeSetIsRefused(t *testing.T) {
	for _, in := range [][]string{nil, {}, {""}, {"   "}} {
		if _, err := ExpandScopes(in); !errors.Is(err, ErrNoScopes) {
			t.Errorf("ExpandScopes(%q) = %v, want ErrNoScopes", in, err)
		}
	}
}

func TestImplicationIsExpandedAtIssueTime(t *testing.T) {
	got, err := ExpandScopes([]string{ScopeMembersWrite})
	if err != nil {
		t.Fatalf("expanding members:write: %v", err)
	}

	want := []string{ScopeMembersRead, ScopeMembersWrite}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members:write expands to %v, want %v", got, want)
	}
}

func TestExpansionIsSortedAndDeduplicated(t *testing.T) {
	// The stored array is compared and displayed. Two identical grants that
	// differ only in the order the operator ticked the boxes must not look like
	// different credentials.
	first, err := ExpandScopes([]string{ScopeEventsRead, ScopeMembersWrite, ScopeMembersRead})
	if err != nil {
		t.Fatalf("expanding: %v", err)
	}
	second, err := ExpandScopes([]string{ScopeMembersRead, ScopeEventsRead, ScopeMembersWrite, ScopeMembersWrite})
	if err != nil {
		t.Fatalf("expanding: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Errorf("the same grant expanded two ways gave %v and %v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] >= first[i] {
			t.Errorf("expansion is not sorted: %v", first)
			break
		}
	}
}

func TestIssuerCannotGrantAScopeAboveItsRole(t *testing.T) {
	scopes, err := ExpandScopes([]string{ScopeWebhooksManage})
	if err != nil {
		t.Fatalf("expanding: %v", err)
	}

	// webhooks:manage requires ADMIN.
	if above := ScopesWithinIssuerRole(scopes, RoleManager, atLeast); above != ScopeWebhooksManage {
		t.Errorf("a MANAGER granting webhooks:manage was refused %q, want %q",
			above, ScopeWebhooksManage)
	}
	if above := ScopesWithinIssuerRole(scopes, RoleAdmin, atLeast); above != "" {
		t.Errorf("an ADMIN granting webhooks:manage was refused %q", above)
	}
}

// The bound is applied to the EXPANDED set, so an implied scope cannot be used
// to reach past the issuer's role. members:write implies members:read; a role
// that may grant neither must be refused on the one it actually named.
func TestIssuerBoundIsAppliedToTheExpandedSet(t *testing.T) {
	scopes, err := ExpandScopes([]string{ScopeMembersWrite})
	if err != nil {
		t.Fatalf("expanding: %v", err)
	}

	// members:write requires MANAGER; members:read only VIEWER.
	if above := ScopesWithinIssuerRole(scopes, RoleViewer, atLeast); above != ScopeMembersWrite {
		t.Errorf("a VIEWER granting members:write was refused %q, want %q",
			above, ScopeMembersWrite)
	}
}

func TestUnknownScopeIsNeverWithinReach(t *testing.T) {
	// Unreachable through ExpandScopes, which refuses unknown names -- but the
	// safe reading of a scope nobody can describe is "nobody may grant it", and
	// a future caller that skipped expansion must not get the opposite.
	if above := ScopesWithinIssuerRole([]string{"invented:scope"}, RoleOwner, atLeast); above == "" {
		t.Error("an OWNER was permitted to grant an unregistered scope")
	}
}

func TestScopeSetHasWriteIdentifiesMutatingGrants(t *testing.T) {
	readOnly, _ := ExpandScopes([]string{ScopeMembersRead, ScopeEventsRead})
	if ScopeSetHasWrite(readOnly) {
		t.Error("a read-only grant was reported as granting write")
	}

	writing, _ := ExpandScopes([]string{ScopeMembersWrite})
	if !ScopeSetHasWrite(writing) {
		t.Error("members:write was not reported as granting write")
	}
}

// People are company-wide in this schema, so a site restriction does not narrow
// the roster. That is a documented limitation rather than an oversight, and the
// registry is where it is expressed -- if this flips, the console's wording and
// the specification both have to change with it.
func TestMemberScopesAreNotSiteRestrictable(t *testing.T) {
	for _, scope := range []string{ScopeMembersRead, ScopeMembersWrite} {
		if Scopes[scope].SiteRestrictable {
			t.Errorf("%s is marked site-restrictable, but people are company-wide", scope)
		}
	}
	for _, scope := range []string{ScopeSitesRead, ScopeTerminalsRead, ScopeEventsRead} {
		if !Scopes[scope].SiteRestrictable {
			t.Errorf("%s should be narrowed by a site restriction", scope)
		}
	}
}
