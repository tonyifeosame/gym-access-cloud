package middleware

import (
	"log"
	"net/http"
	"os"
	"sync"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Platform administrator authentication.
//
// THE THIRD CREDENTIAL CLASS, and the separation is enforced here rather than
// assumed. This file reads its OWN cookie, resolves its OWN session table, and
// sets a context key nothing else reads. The consequences that matter:
//
//   * A platform session presented to a console route is nothing. The console
//     middleware looks for an operator session and finds none, so the request is
//     unauthenticated -- it does not fall through to a weaker check.
//
//   * An operator session presented to a platform route is likewise nothing.
//     Both cookies are Path=/, so a browser genuinely sends each to the other's
//     routes; that is a real case rather than a hypothetical, and both
//     directions are covered by tests.
//
//   * A platform administrator never acquires a company_id. Every console
//     handler and every database function is scoped by one, so an identity with
//     no company cannot reach a tenant's data even if it somehow reached a
//     console handler -- the query would filter on zero and match nothing.
//
// A SEPARATE COOKIE NAME, not a separate value in the same cookie. One cookie
// carrying either kind would mean the check that distinguishes them lives on the
// hot authentication path in Go, and that is exactly the check that gets
// forgotten once.

// PlatformCookieName is the cookie a platform administrator authenticates with.
//
// The __Host- prefix is enforced by the browser: it refuses the cookie unless it
// is Secure, Path=/, and carries no Domain. Host-only and un-settable by any
// sibling subdomain, which matters more here than anywhere else -- this
// credential creates tenants.
const PlatformCookieName = "__Host-al_platform"

// DevPlatformCookieName is used ONLY when SESSION_COOKIE_INSECURE is set.
//
// The name changes with the flag for the same reason the operator cookie's does:
// a __Host- cookie without Secure is not a weaker cookie, it is one the browser
// rejects outright, so "keep the name, drop Secure" is not an option that
// exists.
const DevPlatformCookieName = "al_platform"

var platformInsecureWarning sync.Once

// PlatformCookieConfig reports the cookie name and whether Secure is set.
func PlatformCookieConfig() (name string, secure bool) {
	if os.Getenv("SESSION_COOKIE_INSECURE") != "" {
		platformInsecureWarning.Do(func() {
			log.Println("WARNING: SESSION_COOKIE_INSECURE is set -- platform " +
				"administrator sessions use a non-Secure cookie without the " +
				"__Host- prefix. Local development only.")
		})
		return DevPlatformCookieName, false
	}
	return PlatformCookieName, true
}

// Context keys. DELIBERATELY DISJOINT from the operator ones, and there is no
// company_id among them: a platform administrator is not a tenant, and a handler
// that read one would be reaching for something that does not exist.
const (
	ContextPlatformAdmin = "platform_admin"
	// ActorPlatform distinguishes a platform administrator from an operator and
	// a terminal in the request log.
	ActorPlatform = "platform"
)

// PlatformAdmin returns the authenticated administrator, or nil.
func PlatformAdmin(c *gin.Context) *models.PlatformIdentity {
	value, exists := c.Get(ContextPlatformAdmin)
	if !exists {
		return nil
	}
	identity, _ := value.(*models.PlatformIdentity)
	return identity
}

// PlatformAuthMiddleware authenticates a platform administration request.
//
// Every refusal answers the same way, for the same reason the operator
// middleware does: unknown token, revoked session, either expiry lapsed,
// disabled account. The caller cannot act differently on any of them.
//
// A database failure is a 500, never a 401.
func PlatformAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cookieName, _ := PlatformCookieConfig()
		token, err := c.Cookie(cookieName)
		if err != nil || token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			c.Abort()
			return
		}

		identity, err := database.AuthenticatePlatformSession(token)
		if err != nil {
			log.Printf("request_id=%s error op=\"authenticate platform admin\": %v",
				RequestID(c), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
			c.Abort()
			return
		}
		if identity == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired session"})
			c.Abort()
			return
		}

		c.Set(ContextPlatformAdmin, identity)
		c.Set(ContextAuthActor, ActorPlatform)

		c.Next()
	}
}

// RequirePlatformCSRF rejects an unsafe platform request without the session's
// synchronizer token.
//
// A separate function from RequireCSRF rather than a shared one taking an
// interface: the two read different identities out of the context, and a shared
// helper that accepted either would be one refactor away from checking an
// operator's token against a platform session.
func RequirePlatformCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		identity := PlatformAdmin(c)
		if identity == nil {
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
