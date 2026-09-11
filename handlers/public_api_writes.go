package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/service"
)

// The public API's write routes, its access-standing read and its event trail
// (API_SPEC.md section 18). Mounted in router.go behind PublicAPILimiter,
// IdempotencyMiddleware and RequireScope, in that order.
//
// ---------------------------------------------------------------------------
// BODIES ARE STRICT
// ---------------------------------------------------------------------------
//
// The legacy site-key routes bind with gin and ignore fields they do not know,
// which is how a client can send `fingerprint_template` to a route that must
// not accept one and never learn it was dropped. Section 18 promises the
// opposite -- an unknown field is 400 unknown_field, named -- so these handlers
// decode with DisallowUnknownFields and map the decoder's own errors onto the
// registry: an unknown field names the field, a wrong type names the field,
// and anything that is not a JSON object at all is invalid_field on `body`.
//
// ---------------------------------------------------------------------------
// AUDITED AS AN INTEGRATION, NOT AS AN OPERATOR
// ---------------------------------------------------------------------------
//
// recordAudit is shaped around an operator session. A public write has none;
// what it has is a credential. So the actor is written as role INTEGRATION
// with the credential's non-secret key prefix where an email would go, and
// the credential's public id and name in `changes`, which is what an
// administrator reviewing the trail needs to find the key. The Activity page
// renders an unknown role as its raw label, so nothing there needs to change.

// maxPublicBodyBytes bounds a write body. A member is a few hundred bytes; a
// megabyte is not a member.
const maxPublicBodyBytes = 64 << 10

// The audit vocabulary for public writes reuses the person actions, so a
// person's history reads as one sequence whoever changed them.
const actorRoleIntegration = "INTEGRATION"

// memberCreateBody is POST /members. Pointers where absence matters.
type memberCreateBody struct {
	MemberID       string `json:"member_id"`
	FullName       string `json:"full_name"`
	MembershipType string `json:"membership_type"`
	Active         *bool  `json:"active"`
}

// memberPatchBody is PATCH /members/{member_id}. member_id is a pointer only so
// its PRESENCE can be refused: the id is the path parameter and cannot change.
type memberPatchBody struct {
	MemberID       *string `json:"member_id"`
	FullName       *string `json:"full_name"`
	MembershipType *string `json:"membership_type"`
	Active         *bool   `json:"active"`
}

// decodePublicBody reads a JSON object into dst with unknown fields refused.
// Reports false having already written the refusal.
func decodePublicBody(c *gin.Context, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxPublicBodyBytes+1))
	if err != nil {
		RespondServiceError(c, "public body", service.ErrInvalidField("body", "The request body could not be read."))
		return false
	}
	if len(raw) > maxPublicBodyBytes {
		RespondServiceError(c, "public body", service.ErrInvalidField("body", "The request body is larger than this endpoint accepts."))
		return false
	}
	if strings.TrimSpace(string(raw)) == "" {
		RespondServiceError(c, "public body", service.ErrInvalidField("body", "A JSON object body is required."))
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		RespondServiceError(c, "public body", bodyError(err))
		return false
	}
	// Exactly one JSON value. Trailing content is a malformed request, not a
	// second request.
	if decoder.More() {
		RespondServiceError(c, "public body", service.ErrInvalidField("body", "The request body must be a single JSON object."))
		return false
	}
	return true
}

// bodyError maps encoding/json's failures onto the registry.
func bodyError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			return service.ErrInvalidField("body", "The request body must be a JSON object.")
		}
		return service.ErrInvalidField(field, field+" has the wrong type.")
	}
	// The decoder reports an unknown field as a plain error whose text is the
	// only carrier of the name: `json: unknown field "x"`.
	const unknown = `json: unknown field "`
	if msg := err.Error(); strings.HasPrefix(msg, unknown) {
		return service.ErrUnknownField(strings.TrimSuffix(strings.TrimPrefix(msg, unknown), `"`))
	}
	return service.ErrInvalidField("body", "The request body is not valid JSON.")
}

// recordIntegrationAudit writes an audit row for a public write.
func recordIntegrationAudit(c *gin.Context, tc *service.TenantContext, action, targetType,
	targetPublicID, label string, changes gin.H) {

	entry := database.AuditEntry{
		CompanyID:      tc.CompanyID(),
		ActorEmail:     tc.KeyPrefix(),
		ActorRole:      actorRoleIntegration,
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         action,
		TargetType:     targetType,
		TargetPublicID: targetPublicID,
		TargetLabel:    label,
	}
	if changes == nil {
		changes = gin.H{}
	}
	changes["via"] = "public_api"
	changes["credential_key_prefix"] = tc.KeyPrefix()
	entry.Changes = map[string]any(changes)
	database.WriteAuditEvent(entry)
}

// PublicCreateMember handles POST /api/public/v1/members. Scope members:write.
func PublicCreateMember(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	var body memberCreateBody
	if !decodePublicBody(c, &body) {
		return
	}
	member, err := publicAPI().members.Create(c.Request.Context(), tc, service.MemberInput{
		MemberID:       body.MemberID,
		FullName:       body.FullName,
		MembershipType: body.MembershipType,
		Active:         body.Active,
	})
	if err != nil {
		RespondServiceError(c, "public create member", err)
		return
	}
	recordIntegrationAudit(c, tc, auditPersonCreated, auditTargetPerson, member.ID, member.MemberID, gin.H{
		"full_name":       member.FullName,
		"membership_type": member.MembershipType,
		"active":          member.Active,
	})
	c.JSON(http.StatusCreated, member)
}

// PublicUpdateMember handles PATCH /api/public/v1/members/{member_id}. Scope
// members:write. Partial: absent fields keep their value.
func PublicUpdateMember(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	var body memberPatchBody
	if !decodePublicBody(c, &body) {
		return
	}
	if body.MemberID != nil {
		RespondServiceError(c, "public update member",
			service.ErrInvalidField("member_id", "member_id identifies the member and cannot be changed."))
		return
	}
	if body.FullName == nil && body.MembershipType == nil && body.Active == nil {
		RespondServiceError(c, "public update member",
			service.ErrInvalidField("body", "Supply at least one of full_name, membership_type or active."))
		return
	}
	in := service.MemberInput{Active: body.Active}
	changes := gin.H{}
	if body.FullName != nil {
		// An explicit empty name is a refusal, not "keep it": the service
		// treats "" as absent, so the check has to happen here.
		if strings.TrimSpace(*body.FullName) == "" {
			RespondServiceError(c, "public update member",
				service.ErrInvalidField("full_name", "full_name cannot be empty."))
			return
		}
		in.FullName = *body.FullName
		changes["full_name"] = strings.TrimSpace(*body.FullName)
	}
	if body.MembershipType != nil {
		if strings.TrimSpace(*body.MembershipType) == "" {
			RespondServiceError(c, "public update member",
				service.ErrInvalidField("membership_type", "membership_type cannot be empty."))
			return
		}
		in.MembershipType = *body.MembershipType
		changes["membership_type"] = strings.TrimSpace(*body.MembershipType)
	}
	if body.Active != nil {
		changes["active"] = *body.Active
	}
	member, err := publicAPI().members.Update(c.Request.Context(), tc, c.Param("member_id"), in)
	if err != nil {
		RespondServiceError(c, "public update member", err)
		return
	}
	recordIntegrationAudit(c, tc, auditPersonUpdated, auditTargetPerson, member.ID, member.MemberID, changes)
	c.JSON(http.StatusOK, member)
}

// PublicDeleteMember handles DELETE /api/public/v1/members/{member_id}. Scope
// members:write. 204 whether or not anything was removed; audited only when
// something was.
func PublicDeleteMember(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	memberID := c.Param("member_id")
	removed, err := publicAPI().members.Delete(c.Request.Context(), tc, memberID)
	if err != nil {
		RespondServiceError(c, "public delete member", err)
		return
	}
	if removed {
		recordIntegrationAudit(c, tc, auditPersonDeleted, auditTargetPerson, "", memberID, nil)
	}
	c.Status(http.StatusNoContent)
}

// PublicMemberAccess handles GET /api/public/v1/members/{member_id}/access.
// Scope access:read.
func PublicMemberAccess(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	access, err := publicAPI().access.Rules(c.Request.Context(), tc, c.Param("member_id"))
	if err != nil {
		RespondServiceError(c, "public member access", err)
		return
	}
	c.JSON(http.StatusOK, access)
}

// PublicListEvents handles GET /api/public/v1/events. Scope events:read.
func PublicListEvents(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c, "limit", "cursor", "member_id", "site_id", "decision", "from", "to") {
		return
	}
	page, ok := pageRequest(c)
	if !ok {
		return
	}
	filter := service.EventFilter{
		MemberID: c.Query("member_id"),
		SiteID:   c.Query("site_id"),
		Decision: strings.ToUpper(strings.TrimSpace(c.Query("decision"))),
	}
	if filter.Decision != "" && !models.IsEventDecision(filter.Decision) {
		RespondServiceError(c, "public list events",
			service.ErrInvalidField("decision", "decision must be GRANTED, DENIED, RECORDED or ERROR."))
		return
	}
	var err error
	if filter.From, err = publicTimeQuery(c, "from"); err != nil {
		RespondServiceError(c, "public list events", err)
		return
	}
	if filter.To, err = publicTimeQuery(c, "to"); err != nil {
		RespondServiceError(c, "public list events", err)
		return
	}
	if filter.From != nil && filter.To != nil && !filter.To.After(*filter.From) {
		RespondServiceError(c, "public list events",
			service.ErrInvalidField("to", "to must be later than from."))
		return
	}
	result, err := publicAPI().events.List(c.Request.Context(), tc, page, filter)
	if err != nil {
		RespondServiceError(c, "public list events", err)
		return
	}
	c.JSON(http.StatusOK, envelope(result))
}

// publicTimeQuery parses an optional RFC 3339 query parameter.
func publicTimeQuery(c *gin.Context, name string) (*time.Time, error) {
	raw, present := c.GetQuery(name)
	if !present {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return nil, service.ErrInvalidTimestamp(name)
	}
	return &parsed, nil
}
