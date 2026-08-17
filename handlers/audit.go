package handlers

import (
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The operator audit trail's handler side.
//
// SEC-07 was that nothing recorded what an operator did. recordAudit is the one
// call every console mutation makes, and it exists as a helper rather than as
// twelve open-coded inserts so that a new mutation has an obvious thing to do
// and so the actor, the address and the request id are captured the same way
// every time.
//
// WHAT IS DELIBERATELY NOT AUTOMATIC. This is not middleware. Middleware that
// audited every unsafe request would record the ones that FAILED validation as
// though they happened, would have no idea what was actually changed, and could
// not name the target -- because the target is resolved inside the handler. An
// audit trail full of attempted actions with no detail is worse than one that a
// developer has to remember, because it looks complete.

// Audit action names. VERB in the past tense, because an audit record is a
// statement about something that already happened.
const (
	auditSiteCreated    = "SITE_CREATED"
	auditSiteUpdated    = "SITE_UPDATED"
	auditSiteRetired    = "SITE_RETIRED"
	auditSiteKeyRotated = "SITE_KEY_ROTATED"

	auditTerminalDisabled = "TERMINAL_DISABLED"
	auditTerminalEnabled  = "TERMINAL_ENABLED"
	auditTerminalRevoked  = "TERMINAL_CREDENTIAL_REVOKED"
	auditTerminalRetired  = "TERMINAL_RETIRED"
	auditTerminalMoved    = "TERMINAL_MOVED"
	auditTerminalResynced = "TERMINAL_RESYNCED"
	auditTerminalModeSet  = "TERMINAL_MODE_SET"

	// Claim codes. Two actions, because they are performed by two different
	// identities: an operator issues, and a terminal with no credential at all
	// redeems. The redemption record carries the address it came from, which is
	// the only identity there is on that path.
	auditDeviceClaimCodeIssued = "DEVICE_CLAIM_CODE_ISSUED"
	auditDeviceClaimed         = "DEVICE_CLAIMED"

	auditPersonCreated = "PERSON_CREATED"
	auditPersonUpdated = "PERSON_UPDATED"
	auditPersonDeleted = "PERSON_DELETED"

	auditCredentialIssued  = "CREDENTIAL_ISSUED"
	auditCredentialRevoked = "CREDENTIAL_REVOKED"

	auditOperatorCreated     = "OPERATOR_CREATED"
	auditOperatorUpdated     = "OPERATOR_UPDATED"
	auditOperatorRoleSet     = "OPERATOR_ROLE_CHANGED"
	auditOperatorDeleted     = "OPERATOR_DELETED"
	auditOperatorSitesSet    = "OPERATOR_SITES_CHANGED"
	auditOperatorPasswordSet = "OPERATOR_PASSWORD_RESET"

	auditApplicationSet = "APPLICATION_CONFIGURED"
	auditSettingsSet    = "SITE_SETTINGS_UPDATED"

	auditPermissionCreated = "PERMISSION_CREATED"
	auditPermissionDeleted = "PERMISSION_DELETED"
	auditScheduleSet       = "SCHEDULE_CONFIGURED"

	auditFirmwarePublished = "FIRMWARE_PUBLISHED"
	auditFirmwareTargetSet = "FIRMWARE_TARGET_SET"
)

// Audit target types.
const (
	auditTargetSite        = "SITE"
	auditTargetTerminal    = "TERMINAL"
	auditTargetPerson      = "PERSON"
	auditTargetCredential  = "CREDENTIAL"
	auditTargetOperator    = "OPERATOR"
	auditTargetApplication = "APPLICATION"
	auditTargetPermission  = "PERMISSION"
	auditTargetSchedule    = "SCHEDULE"
	auditTargetFirmware    = "FIRMWARE"
)

// recordAudit writes one operator action.
//
// targetPublicID may be empty for a target addressed by a natural key rather
// than a UUID -- a terminal is named by its serial, which is what an operator
// reads off the hardware. The label carries it in that case, so the record still
// says what was acted on.
//
// BEST EFFORT, and that is a deliberate trade documented in database/audit.go:
// failing a mutation because its audit row could not be written turns a full
// table into an outage of every console write, and an audit trail that takes the
// product down is one somebody disables.
func recordAudit(c *gin.Context, action, targetType, targetPublicID, label string,
	changes gin.H) {

	identity := middleware.Operator(c)

	entry := database.AuditEntry{
		CompanyID:      c.GetInt64("company_id"),
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         action,
		TargetType:     targetType,
		TargetPublicID: targetPublicID,
		TargetLabel:    label,
	}

	if identity != nil {
		entry.ActorUserID = identity.UserID
		entry.ActorEmail = identity.Email
		entry.ActorRole = identity.Role
	}

	if len(changes) > 0 {
		entry.Changes = map[string]any(changes)
	}

	database.WriteAuditEvent(entry)
}

// ConsoleListAuditEvents handles GET /console/audit
//
// The answer to "who changed this". ADMIN-gated: an audit trail names which
// operators did what, and that is administrative information rather than
// something every viewer needs.
func ConsoleListAuditEvents(c *gin.Context) {
	query := database.AuditQuery{
		Action:     c.Query("action"),
		TargetType: c.Query("target_type"),
		ActorEmail: c.Query("actor"),
		Limit:      boundedQueryInt(c, "limit", 50, 1, 200),
		Offset:     boundedQueryInt(c, "offset", 0, 0, 0),
	}

	since, ok := parseTimeQuery(c, "since")
	if !ok {
		return
	}
	until, ok := parseTimeQuery(c, "until")
	if !ok {
		return
	}
	query.Since = since
	query.Until = until

	page, err := database.ListAuditEvents(c.GetInt64("company_id"), query)
	if err != nil {
		logError(c, "console list audit events", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve the audit trail"})
		return
	}

	c.JSON(http.StatusOK, models.ConsoleAuditPage{
		Count:   len(page.Entries),
		Total:   page.Total,
		Limit:   query.Limit,
		Offset:  query.Offset,
		HasMore: query.Offset+len(page.Entries) < page.Total,
		Entries: page.Entries,
	})
}

// The RFC3339 filter parser both trails use lives in handlers/events.go.
//
// IT REJECTS A MALFORMED VALUE rather than ignoring it, which is a deliberate
// reversal of what this file did before. Silently dropping `since=lastweek` and
// answering with the whole trail looks like an answer to the question that was
// asked: an operator reading a year of audit history believing they are reading
// a week is worse off than one who gets an error and fixes the parameter. The
// same reasoning does NOT apply to `limit`, which is a page size rather than a
// claim about what the results cover, so boundedQueryInt still clamps.
