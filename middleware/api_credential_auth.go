package middleware

import (
	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/service"
)

// Integration-credential authentication for the public API.
//
// MOUNTED ON /api/public/v1 (router.go), and nowhere else. It was written and
// tested in P2 before any route depended on it, so that the whole path from an
// Authorization header to a TenantContext existed ahead of the first
// consumer; TestPublicAPIMountsExactlyTheSpecifiedRoutes pins what sits
// behind it.
//
// WHAT IT SETS, AND WHAT IT DOES NOT. On success the request carries a
// *service.TenantContext under ContextTenant, and that is ALL a public handler
// may read the tenant from. It deliberately does not set the "company_id" key
// the console and site-key paths use: a public handler that reached for
// c.GetInt64("company_id") would get zero, and a service asked for tenant zero
// refuses to open a transaction. The two authentication worlds do not share a
// key, so a handler cannot be wired to the wrong one by accident.
//
// The environment is passed in rather than read from handlers.APIEnvironment,
// because middleware cannot import handlers; main() has both and hands the
// value over at mount time.

// ContextTenant is the gin context key holding the *service.TenantContext.
const ContextTenant = "api_tenant"

// ActorIntegration marks an integration credential in the request log.
const ActorIntegration = "integration"

// APICredentialAuthMiddleware authenticates Authorization: Bearer atp_… .
func APICredentialAuthMiddleware(environment string) gin.HandlerFunc {
	return func(c *gin.Context) {
		presented := service.BearerCredential(c.GetHeader("Authorization"))
		tc, err := service.Authenticate(presented, environment, RequestID(c))
		if err != nil {
			code := service.ErrCredentialInvalid().Code()
			if svcErr, ok := service.As(err); ok {
				code = svcErr.Code()
			}
			if code == service.ErrCredentialMissing().Code() {
				c.Header("WWW-Authenticate", `Bearer realm="accesslink"`)
			}
			writeAPIError(c, code)
			return
		}
		c.Set(ContextTenant, tc)
		c.Set(ContextAuthActor, ActorIntegration)
		// The secret itself is not retained anywhere past this point; the
		// non-secret prefix is on the TenantContext for whoever logs it.
		c.Next()
	}
}

// Tenant returns the authenticated tenant context, or nil when the request did
// not pass APICredentialAuthMiddleware. A handler that receives nil here is
// mounted on the wrong group and must refuse rather than guess.
func Tenant(c *gin.Context) *service.TenantContext {
	value, ok := c.Get(ContextTenant)
	if !ok {
		return nil
	}
	tc, _ := value.(*service.TenantContext)
	return tc
}

// RequireScope refuses a request whose credential lacks a scope. A route
// declares the scope it needs once, at mount time, rather than each handler
// repeating the check -- though the service checks again regardless, so a
// handler mounted without this still cannot act beyond the credential.
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tc := Tenant(c)
		if tc == nil {
			writeAPIError(c, service.ErrCredentialMissing().Code())
			return
		}
		if err := tc.RequireScope(scope); err != nil {
			writeAPIError(c, service.ErrInsufficientScope(scope).Code())
			return
		}
		c.Next()
	}
}
