package middleware

import (
	"errors"
	"log"
	"net/http"
	"os"
	"sync"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator (browser) authentication.
//
// This is a SEPARATE credential class from everything in auth.go and
// device_auth.go, and the separation is the point. The site API key those use is
// the provisioning secret: whoever holds it can register a terminal and rotate
// any device credential at that site. A dashboard running on a machine the
// company does not control must never hold it.
//
// So nothing in this file reads X-API-Key, and nothing in auth.go or
// device_auth.go reads a session cookie. A site key presented to a console route
// is simply an unauthenticated request, and a session cookie presented to a
// device route is likewise nothing -- the cookie is Path=/ and therefore IS sent
// to those routes by the browser, so that second direction is a real case and
// not a hypothetical one. Both are covered by tests.
//
// The chain a console route uses:
//
//	OperatorAuthMiddleware()        resolve the session, populate the context
//	RequireCSRF()                   unsafe methods must carry X-CSRF-Token
//	RequireRole(models.RoleAdmin)   minimum role for this route
//	RequireSiteGrant("site_id")     the :site_id in the path must be reachable
//	RequireTerminalGrant("serial")  ...or the site the :serial sits at

// SessionCookieName is the cookie the dashboard authenticates with.
//
// The __Host- prefix is enforced by the browser, not by us: it refuses the
// cookie unless it is Secure, Path=/, and carries no Domain attribute. That
// makes the cookie host-only and un-settable by any sibling subdomain, which is
// worth having on a host that also serves the device provisioning API. The
// attributes are fixed by the prefix, which is why there is nothing to configure
// about them in production.
const SessionCookieName = "__Host-al_session"

// DevSessionCookieName is the name used ONLY when SESSION_COOKIE_INSECURE is
// set, for local development that cannot offer TLS.
//
// The name has to change with the flag. A __Host- cookie without Secure is not a
// weaker cookie -- the browser rejects it outright -- so "keep the name, drop
// Secure" is not an option that exists. Dropping the prefix along with Secure is
// the honest form of the same switch, and it means a development cookie can
// never be mistaken for a production one.
const DevSessionCookieName = "al_session"

var insecureCookieWarning sync.Once

// SessionCookieConfig reports the cookie name and whether Secure is set.
//
// Exactly ONE name is ever accepted. Accepting both would defeat the prefix: a
// sibling subdomain could set a plain al_session cookie and have it honoured on
// a host that was supposed to only accept a host-locked one.
func SessionCookieConfig() (name string, secure bool) {
	if os.Getenv("SESSION_COOKIE_INSECURE") != "" {
		insecureCookieWarning.Do(func() {
			log.Println("WARNING: SESSION_COOKIE_INSECURE is set -- operator " +
				"sessions use a non-Secure cookie without the __Host- prefix. " +
				"Local development only; never set this in a deployment.")
		})
		return DevSessionCookieName, false
	}
	return SessionCookieName, true
}

// CSRFHeader carries the per-session synchronizer token on unsafe requests.
const CSRFHeader = "X-CSRF-Token"

// Context keys. company_id is deliberately the SAME key AuthMiddleware sets
// from a site API key: every database function is already scoped by it, so an
// operator-authenticated caller reuses that tenancy filter unchanged.
//
// site_id and site_name are NOT set here. Under a site key they mean "the site
// whose credential was presented"; an operator has no such thing, and leaving
// them unset makes a handler that needs a site fail loudly rather than act on
// site 0. RequireSiteGrant sets them once a site has been named in the path and
// authorized.
const (
	ContextOperator     = "operator"
	ContextUserID       = "user_id"
	ContextUserPublicID = "user_public_id"
	ContextUserEmail    = "user_email"
	ContextRole         = "role"
	ContextSessionID    = "session_id"
	ContextCompanyID    = "company_id"
	ContextAuthActor    = "auth_actor"

	// ActorOperator distinguishes a human from a terminal in the request log.
	ActorOperator = "operator"
)

// roleRank orders the roles so a route can name a minimum rather than
// enumerating everyone above it: OWNER > ADMIN > MANAGER > VIEWER.
//
// An unknown or empty role ranks 0 and is therefore below every gate. That is
// the safe direction to fail: a role this build does not recognise is refused,
// not waved through.
var roleRank = map[string]int{
	models.RoleViewer:  1,
	models.RoleManager: 2,
	models.RoleAdmin:   3,
	models.RoleOwner:   4,
}

// RoleAtLeast reports whether role meets or exceeds minimum.
func RoleAtLeast(role, minimum string) bool {
	want, known := roleRank[minimum]
	if !known {
		// A gate naming a role that does not exist is a programming error, and
		// the safe reading of it is "nobody".
		return false
	}
	return roleRank[role] >= want
}

// Operator returns the authenticated operator, or nil on a request that did not
// come through OperatorAuthMiddleware.
func Operator(c *gin.Context) *models.OperatorIdentity {
	value, exists := c.Get(ContextOperator)
	if !exists {
		return nil
	}
	identity, _ := value.(*models.OperatorIdentity)
	return identity
}

// OperatorAuthMiddleware authenticates a dashboard request from its session
// cookie.
//
// Every refusal answers with the same message. Unknown token, revoked session,
// either expiry lapsed, account disabled, company disabled -- the caller is not
// entitled to tell those apart, and a browser can do nothing differently for any
// of them beyond sending its operator back to the login form.
//
// A database failure is a 500, never a 401. Reporting an outage as "your session
// is invalid" would have every operator re-entering a password that was never
// the problem, which is the same reasoning AuthMiddleware applies to site keys.
func OperatorAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieName, _ := SessionCookieConfig()
		token, err := c.Cookie(cookieName)
		if err != nil || token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		identity, err := database.AuthenticateSession(token)
		if err != nil {
			log.Printf("request_id=%s error op=\"authenticate operator\": %v", RequestID(c), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
			c.Abort()
			return
		}
		if identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired session"})
			c.Abort()
			return
		}

		c.Set(ContextOperator, identity)
		c.Set(ContextUserID, identity.UserID)
		c.Set(ContextUserPublicID, identity.UserPublicID)
		c.Set(ContextUserEmail, identity.Email)
		c.Set(ContextRole, identity.Role)
		c.Set(ContextSessionID, identity.SessionID)
		c.Set(ContextCompanyID, identity.CompanyID)
		c.Set(ContextAuthActor, ActorOperator)

		c.Next()
	}
}

// RequireCSRF rejects an unsafe request that does not carry the session's own
// synchronizer token.
//
// The token is issued with the session and returned in the login and /me
// response BODIES, never as a second cookie: in a split-origin deployment
// (app.accesslink.store calling api.accesslink.store) the dashboard's JavaScript
// cannot read a cookie scoped to the API host, so a double-submit cookie would
// silently fail there while appearing to work in development.
//
// GET, HEAD and OPTIONS are exempt because they are not supposed to change
// anything; a console route that mutates state on a GET would be the bug, not
// this exemption.
//
// The comparison is by hash and in constant time. Only the SHA-256 of the token
// is stored, and a byte-by-byte compare with an early exit would leak the stored
// value one character at a time to a caller willing to measure.
func RequireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		identity := Operator(c)
		if identity == nil {
			// Reached only if this is mounted without OperatorAuthMiddleware in
			// front of it. There is no session to check a token against, so the
			// request is not authenticated.
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		presented := c.GetHeader(CSRFHeader)
		if presented == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "CSRF token required"})
			c.Abort()
			return
		}

		if !database.CSRFMatches(identity.CSRFTokenHash, presented) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Invalid CSRF token"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireRole gates a route on a minimum role.
//
// Roles are ordered, so a route names the lowest role allowed to reach it and
// everyone above inherits. The alternative -- listing the permitted roles at
// every route -- means a new role has to be added to each list, and the one that
// gets forgotten is a silent authorization hole.
func RequireRole(minimum string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := Operator(c)
		if identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		if !RoleAtLeast(identity.Role, minimum) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireSiteGrant authorizes a route that names a site in its path, and puts
// the resolved site in the context for the handler.
//
// param is the path parameter holding the site's public_id, e.g. "site_id" for
// /api/v1/console/sites/:site_id/settings.
//
// Three outcomes, and the difference between the last two matters:
//
//   - The site is in another company, does not exist, or the id is malformed:
//     404. Never 403 -- answering "forbidden" would confirm that the id exists
//     in somebody else's account, which is why every cross-tenant read in this
//     API is a 404.
//   - The site is in the operator's company but they hold no grant to it: 403.
//     It exists and they may know it exists; they are simply not scoped to it.
//   - Otherwise the site is set in the context and the handler runs.
//
// THE GRANT RULE. OWNER and ADMIN reach every site in their company regardless
// of grant rows. For MANAGER and VIEWER, an EMPTY grant set also means every
// site in the company -- absence is the default rather than a wildcard row, so
// that adding a site to a company does not require touching every account. Once
// an operator has any grant they are scoped to exactly those sites.
//
// Note what is not affected: people are company-wide in this schema, so site
// grants bound the site, device and access-log routes and nothing else. That
// limit is documented rather than papered over.
func RequireSiteGrant(param string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := Operator(c)
		if identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		// Company-scoped resolution. No role skips this: bypassing grants is a
		// policy decision, but reaching another tenant's site is not something
		// any role may do.
		access, err := database.ResolveSiteAccess(identity.CompanyID, identity.UserID, c.Param(param))
		if errors.Is(err, models.ErrSiteNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
			c.Abort()
			return
		}
		if err != nil {
			log.Printf("request_id=%s error op=\"resolve site access\": %v", RequestID(c), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authorization unavailable"})
			c.Abort()
			return
		}

		if !grantAllows(identity, access) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Site access denied"})
			c.Abort()
			return
		}

		c.Set("site_id", access.SiteID)
		c.Set("site_name", access.SiteName)

		c.Next()
	}
}

// grantAllows applies THE GRANT RULE described on RequireSiteGrant to one
// resolved site.
//
// Factored out because it is now applied at two entry points -- a path naming a
// site and a path naming a terminal -- and two copies of an authorization rule
// is two places for it to be relaxed. Every gate that consults grants goes
// through this function.
func grantAllows(identity *models.OperatorIdentity, access *database.SiteAccess) bool {
	return RoleAtLeast(identity.Role, models.RoleAdmin) ||
		access.GrantCount == 0 ||
		access.Granted
}

// RequireTerminalGrant authorizes a route that names a TERMINAL by serial, and
// puts the site that terminal belongs to in the context for the handler.
//
// param is the path parameter holding the serial, i.e. "serial" for
// /api/v1/console/terminals/:serial/application-mode.
//
// WHY THIS EXISTS. The terminal LIST was already narrowed to the caller's
// grants, but the routes that name one terminal were company-scoped only. A
// MANAGER granted Site A could therefore not see Site B's terminals in the list
// and still read one, and change what application it runs, by asking for its
// serial directly -- and a serial is printed on the device rather than being a
// secret. The list and the detail must answer the same question, so the detail
// routes now resolve the terminal's site and apply the same rule.
//
// The outcomes mirror RequireSiteGrant exactly, including the 404/403 split:
//
//   - The terminal is in another company, retired, or does not exist: 404. Never
//     403, which would confirm that a serial is registered to somebody else.
//   - The terminal is at a site in the caller's company that they hold no grant
//     to: 403. They may know it exists; they are not scoped to it.
//   - Otherwise site_id and site_name are set from THE TERMINAL'S OWN SITE and
//     the handler runs.
//
// Setting site_id from the resolved terminal is deliberate: it means a handler
// behind this gate has the same context a handler behind RequireSiteGrant has,
// and cannot be tricked into acting on a site the caller merely named.
func RequireTerminalGrant(param string) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity := Operator(c)
		if identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		access, err := database.ResolveDeviceAccess(identity.CompanyID, identity.UserID, c.Param(param))
		if errors.Is(err, models.ErrDeviceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
			c.Abort()
			return
		}
		if err != nil {
			log.Printf("request_id=%s error op=\"resolve terminal access\": %v", RequestID(c), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authorization unavailable"})
			c.Abort()
			return
		}

		if !grantAllows(identity, access) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Site access denied"})
			c.Abort()
			return
		}

		c.Set("site_id", access.SiteID)
		c.Set("site_name", access.SiteName)

		c.Next()
	}
}
