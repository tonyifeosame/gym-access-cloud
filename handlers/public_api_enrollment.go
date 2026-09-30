package handlers

import (
	"net/http"

	"access-terminal-cloud-api/service"

	"github.com/gin-gonic/gin"
)

// Terminal listing and remote fingerprint enrolment on the public API
// (API_SPEC.md section 18, "Terminals" and "Enrolment").
//
// The same enrolment store the console drives: one state machine, one
// ENROLL_FINGERPRINT job, the terminal and its protocol untouched. The
// difference is only the caller -- an integration credential, bounded by its
// scopes and its site restriction -- and the audit line, which names the
// credential instead of an operator.

type enrollmentStartBody struct {
	MemberID         string `json:"member_id"`
	ExpiresInSeconds *int   `json:"expires_in_seconds"`
}

// PublicListTerminals handles GET /api/public/v1/terminals. Scope terminals:read.
func PublicListTerminals(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil || !checkQuery(c) {
		return
	}
	terminals, err := publicAPI().enrollments.Terminals(c.Request.Context(), tc)
	if err != nil {
		RespondServiceError(c, "public list terminals", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": terminals})
}

// PublicStartEnrollment handles POST /api/public/v1/terminals/{serial}/enrollments.
// Scope enrollments:write. The terminal is in the path because it is the thing
// being commanded, exactly as in the console route.
func PublicStartEnrollment(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil || !checkQuery(c) {
		return
	}
	var body enrollmentStartBody
	if !decodePublicBody(c, &body) {
		return
	}
	enrollment, err := publicAPI().enrollments.Start(c.Request.Context(), tc, c.Param("serial"), service.EnrollmentInput{
		MemberID:         body.MemberID,
		ExpiresInSeconds: body.ExpiresInSeconds,
	})
	if err != nil {
		RespondServiceError(c, "public start enrollment", err)
		return
	}
	recordIntegrationAudit(c, tc, auditEnrollmentStarted, auditTargetPerson, enrollment.Member, enrollment.MemberID, gin.H{
		"terminal":   enrollment.TerminalSerial,
		"expires_at": enrollment.ExpiresAt,
	})
	c.JSON(http.StatusCreated, enrollment)
}

// PublicGetMemberEnrollment handles GET /api/public/v1/members/{member_id}/enrollment.
// Scope members:read.
func PublicGetMemberEnrollment(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil || !checkQuery(c) {
		return
	}
	enrollment, err := publicAPI().enrollments.Get(c.Request.Context(), tc, c.Param("member_id"))
	if err != nil {
		RespondServiceError(c, "public get enrollment", err)
		return
	}
	c.JSON(http.StatusOK, enrollment)
}

// PublicCancelMemberEnrollment handles DELETE /api/public/v1/members/{member_id}/enrollment.
// Scope enrollments:write. Answers with the cancelled enrolment.
func PublicCancelMemberEnrollment(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil || !checkQuery(c) {
		return
	}
	enrollment, err := publicAPI().enrollments.Cancel(c.Request.Context(), tc, c.Param("member_id"))
	if err != nil {
		RespondServiceError(c, "public cancel enrollment", err)
		return
	}
	recordIntegrationAudit(c, tc, auditEnrollmentCancelled, auditTargetPerson, enrollment.Member, enrollment.MemberID, gin.H{
		"terminal": enrollment.TerminalSerial,
	})
	c.JSON(http.StatusOK, enrollment)
}
