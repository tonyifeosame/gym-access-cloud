package handlers

import (
	"context"
	"net/http"

	"access-terminal-cloud-api/service"

	"github.com/gin-gonic/gin"
)

// Integration-managed access on the public API (API_SPEC.md section 18,
// "Integration-managed access"). Scope access:write. The member must be one
// this integration created; the rule is derived from the credential, never
// from the request, which has no body.

// PublicGrantMemberAccess handles PUT /api/public/v1/members/{member_id}/access.
func PublicGrantMemberAccess(c *gin.Context) {
	changeMemberAccess(c, auditPermissionCreated, publicAPI().access.Grant)
}

// PublicRevokeMemberAccess handles DELETE /api/public/v1/members/{member_id}/access.
func PublicRevokeMemberAccess(c *gin.Context) {
	changeMemberAccess(c, auditPermissionDeleted, publicAPI().access.Revoke)
}

func changeMemberAccess(c *gin.Context, action string, change func(context.Context, *service.TenantContext, string) (*service.MemberAccess, int, error)) {
	tc := tenantOrRefuse(c)
	if tc == nil || !checkQuery(c) {
		return
	}
	access, changed, err := change(c.Request.Context(), tc, c.Param("member_id"))
	if err != nil {
		RespondServiceError(c, "public member access", err)
		return
	}
	if changed > 0 {
		recordIntegrationAudit(c, tc, action, auditTargetPerson, access.PersonPublicID, access.MemberID, gin.H{
			"rules": changed, "managed_access": true,
		})
	}
	c.JSON(http.StatusOK, access)
}
