package models

import (
	"fmt"
	"sort"
	"strings"
)

// The integration scope registry.
//
// ---------------------------------------------------------------------------
// WHY A REGISTRY AND NOT A LIST OF STRINGS AT EACH ROUTE
// ---------------------------------------------------------------------------
//
// The same shape CommandSpecs already uses for the command plane: a scope that
// is not in this map does not exist, cannot be granted, and cannot be named by a
// route. The alternative -- each route naming the roles or scopes it accepts --
// means a new scope has to be added to every list, and the one that gets
// forgotten is a silent authorization hole.
//
// The set is CLOSED in two places on purpose. Here, so Go refuses to grant one
// it does not know; and in migrations/030 as a CHECK, so the database refuses to
// store one. Adding a scope is therefore a code change and a migration, which is
// correct: it is a change to what a credential can do.
//
// ---------------------------------------------------------------------------
// IMPLICATION IS EXPANDED AT ISSUE TIME, NOT AT CHECK TIME
// ---------------------------------------------------------------------------
//
// Granting members:write stores members:read alongside it. Two consequences,
// both wanted:
//
//   - A permission check is a set membership test with no inference in it. An
//     inference evaluated on every request is an inference that can be wrong on
//     every request.
//   - The console shows the customer the actual list. "members:write" implying
//     a read they cannot see is a surprise; storing both is a disclosure.
//
// ---------------------------------------------------------------------------
// ISSUER BOUNDING
// ---------------------------------------------------------------------------
//
// A credential is issued by an operator, and an operator must not be able to
// mint a key that outranks them. MinRole is the lowest operator role that may
// GRANT a scope, which keeps the existing role ladder meaningful once a scope
// exists that ADMIN itself should not hand out.
//
// Today only an ADMIN can reach the issue endpoint at all, so the members:write
// and webhooks:manage bounds are satisfied by everybody who can call it. That is
// not a reason to leave the mechanism out: the check is what makes an
// OWNER-only scope expressible later without revisiting the issue path.

// The V1 scope set. resource:action, lowercase, colon-separated.
const (
	ScopeMembersRead    = "members:read"
	ScopeMembersWrite   = "members:write"
	ScopeSitesRead      = "sites:read"
	ScopeTerminalsRead  = "terminals:read"
	ScopeEventsRead     = "events:read"
	ScopeAccessRead     = "access:read"
	ScopeWebhooksManage = "webhooks:manage"
)

// ScopeSpec describes one scope.
type ScopeSpec struct {
	// Name is the wire form and the stored form.
	Name string

	// Implies are the scopes granted alongside this one. Expanded once, at
	// issue time, by ExpandScopes.
	//
	// Not transitive by declaration -- ExpandScopes closes over the graph -- but
	// the V1 set is deliberately one level deep, because a scope model a
	// customer cannot hold in their head is one they will over-grant.
	Implies []string

	// MinRole is the lowest operator role that may grant this scope.
	MinRole string

	// Write reports whether the scope permits mutation. Read and write are
	// separated so a reporting integration can be given a credential that
	// provably cannot change anything.
	Write bool

	// SiteRestrictable reports whether an api_credential_sites restriction
	// narrows what this scope reaches.
	//
	// FALSE FOR THE MEMBER SCOPES, and that is a documented limitation rather
	// than an oversight: people are company-wide in this schema, exactly as they
	// are for operator site grants. A site-restricted credential still sees the
	// whole roster, and the console has to say so rather than implying a
	// narrowing that does not happen.
	SiteRestrictable bool

	// Description is shown in the console beside the checkbox. Written for the
	// operator deciding whether to tick it, not for the developer using it.
	Description string
}

// Scopes is the registry. A scope absent from this map does not exist.
var Scopes = map[string]ScopeSpec{
	ScopeMembersRead: {
		Name:             ScopeMembersRead,
		MinRole:          RoleViewer,
		Write:            false,
		SiteRestrictable: false,
		Description:      "Read the people on your roster.",
	},
	ScopeMembersWrite: {
		Name:             ScopeMembersWrite,
		Implies:          []string{ScopeMembersRead},
		MinRole:          RoleManager,
		Write:            true,
		SiteRestrictable: false,
		Description:      "Add, change and remove people on your roster.",
	},
	ScopeSitesRead: {
		Name:             ScopeSitesRead,
		MinRole:          RoleViewer,
		Write:            false,
		SiteRestrictable: true,
		Description:      "Read your sites.",
	},
	ScopeTerminalsRead: {
		Name:             ScopeTerminalsRead,
		MinRole:          RoleViewer,
		Write:            false,
		SiteRestrictable: true,
		Description:      "Read your terminals and whether they are online.",
	},
	ScopeEventsRead: {
		Name:             ScopeEventsRead,
		MinRole:          RoleViewer,
		Write:            false,
		SiteRestrictable: true,
		Description:      "Read the record of who was admitted and refused.",
	},
	ScopeAccessRead: {
		Name:             ScopeAccessRead,
		MinRole:          RoleViewer,
		Write:            false,
		SiteRestrictable: true,
		Description:      "Read a person's access standing and where it applies.",
	},
	ScopeWebhooksManage: {
		Name:             ScopeWebhooksManage,
		MinRole:          RoleAdmin,
		Write:            true,
		SiteRestrictable: false,
		Description:      "Register and manage endpoints that receive your events.",
	},
}

// Errors from scope validation. Handlers map these to 400 and 403 respectively;
// anything else is a server fault.
var (
	// ErrUnknownScope names a scope this build does not have. Deliberately not
	// silently dropped: a caller who believes they granted a capability and did
	// not is worse off than one who got an error.
	ErrUnknownScope = fmt.Errorf("unknown scope")

	// ErrNoScopes is an empty grant. A credential that can do nothing is not a
	// credential, and the database refuses one anyway.
	ErrNoScopes = fmt.Errorf("at least one scope is required")

	// ErrScopeAboveIssuer is an operator granting a scope their own role may
	// not.
	ErrScopeAboveIssuer = fmt.Errorf("scope requires a higher role than the issuer holds")
)

// KnownScope reports whether a scope exists in this build.
func KnownScope(name string) bool {
	_, ok := Scopes[name]
	return ok
}

// AllScopes returns every scope name, sorted.
//
// Sorted rather than map order so an error message, a console list and a test
// fixture all read the same way twice in a row.
func AllScopes() []string {
	out := make([]string, 0, len(Scopes))
	for name := range Scopes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ExpandScopes normalises and closes a requested scope set.
//
// Trims, de-duplicates, refuses anything unknown, adds every implied scope, and
// returns the result sorted. Sorting is not cosmetic: the stored array is
// compared and displayed, and an array whose order depends on what the caller
// typed makes two identical grants look different.
//
// The closure is iterative rather than one pass, so a future two-level
// implication is handled without anybody remembering to flatten it by hand.
func ExpandScopes(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, ErrNoScopes
	}

	seen := make(map[string]bool, len(requested)*2)
	queue := make([]string, 0, len(requested))

	for _, raw := range requested {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !KnownScope(name) {
			return nil, fmt.Errorf("%w: %q is not one of %s",
				ErrUnknownScope, name, strings.Join(AllScopes(), ", "))
		}
		if !seen[name] {
			seen[name] = true
			queue = append(queue, name)
		}
	}

	if len(queue) == 0 {
		return nil, ErrNoScopes
	}

	// Breadth-first over the implication graph. queue grows as implied scopes
	// are discovered; seen stops it revisiting one, which also makes a cycle
	// terminate rather than hang.
	for i := 0; i < len(queue); i++ {
		for _, implied := range Scopes[queue[i]].Implies {
			if !KnownScope(implied) {
				// A registry that implies a scope it does not define is a
				// programming error, and the safe reading is to refuse rather
				// than to grant something unnamed.
				return nil, fmt.Errorf("%w: %q is implied by %q but not defined",
					ErrUnknownScope, implied, queue[i])
			}
			if !seen[implied] {
				seen[implied] = true
				queue = append(queue, implied)
			}
		}
	}

	sort.Strings(queue)
	return queue, nil
}

// ScopesWithinIssuerRole reports the first scope in the set that the issuing
// role may not grant.
//
// Returns "" when every scope is within reach. Checked against the EXPANDED set,
// so an implied scope cannot be used to smuggle in something the issuer could
// not have asked for directly.
func ScopesWithinIssuerRole(scopes []string, issuerRole string, atLeast func(role, minimum string) bool) string {
	for _, name := range scopes {
		spec, ok := Scopes[name]
		if !ok {
			// Unreachable through ExpandScopes, which refuses unknown names.
			// Treated as out of reach rather than ignored, because the safe
			// reading of a scope nobody can describe is "nobody may grant it".
			return name
		}
		if !atLeast(issuerRole, spec.MinRole) {
			return name
		}
	}
	return ""
}

// ScopeSetHasWrite reports whether any scope in the set permits mutation.
//
// Used by the console to warn before issuing, and by the audit record, because
// "this integration can change things" is the part of a grant somebody should
// have to look at twice.
func ScopeSetHasWrite(scopes []string) bool {
	for _, name := range scopes {
		if Scopes[name].Write {
			return true
		}
	}
	return false
}
