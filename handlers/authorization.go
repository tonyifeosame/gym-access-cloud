package handlers

import (
	"errors"
	"net/http"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Authorization: permissions, schedules and the decision preview.
//
// APP-02. The engine's evaluator lives in database/authorization.go; these are
// the routes that let an operator write the rules it evaluates, and ask it what
// it would decide without standing at a door to find out.
//
// ROLE. Permissions and schedules are MANAGER, not ADMIN. Deciding who may come
// in on Tuesday is day-to-day work at every customer that has more than a
// handful of people, and putting it behind the same gate as minting a site
// provisioning credential would mean every rota change needs an administrator.
// The destructive operations -- deleting a schedule that rules depend on --
// still refuse rather than cascade.
//
// SCOPE. Every route resolves its person, site and terminal INSIDE the caller's
// company, in the store. A rule naming another tenant's terminal is a 404.

// ConsoleListPersonPermissions handles GET /console/people/:external_id/permissions
func ConsoleListPersonPermissions(c *gin.Context) {
	permissions, err := database.ListPersonPermissions(
		c.GetInt64("company_id"), c.Param("external_id"))
	if err != nil {
		logError(c, "console list permissions", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve permissions"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"count": len(permissions), "permissions": permissions})
}

// ConsoleGrantPermission handles POST /console/people/:external_id/permissions
//
// Idempotent by construction: the store upserts onto the partial unique index
// for the scope, so a console that retries a create does not get a duplicate or
// a conflict.
func ConsoleGrantPermission(c *gin.Context) {
	var req models.PermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	companyID := c.GetInt64("company_id")
	externalID := c.Param("external_id")

	permission, err := database.GrantPermission(companyID, externalID, req)
	switch {
	case errors.Is(err, models.ErrPersonNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	case errors.Is(err, models.ErrSiteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, models.ErrScheduleNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Schedule not found"})
		return
	case errors.Is(err, models.ErrInvalidScope),
		errors.Is(err, models.ErrInvalidEffect),
		errors.Is(err, models.ErrScopeTargetRequired),
		errors.Is(err, models.ErrScopeTargetRefused):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		if database.IsConstraintViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logError(c, "console grant permission", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to grant permission"})
		return
	}

	recordAudit(c, auditPermissionCreated, auditTargetPermission, permission.ID, externalID,
		gin.H{
			"scope":       permission.ScopeType,
			"effect":      permission.Effect,
			"site":        permission.SiteName,
			"terminal":    permission.DeviceSerial,
			"application": permission.Application,
			"schedule":    permission.ScheduleName,
		})

	c.JSON(http.StatusCreated, permission)
}

// ConsoleRevokePermission handles DELETE /console/permissions/:permission_id
func ConsoleRevokePermission(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	permissionID := c.Param("permission_id")

	// Read before deleting so the audit record can say what was removed. A
	// trail that records "a permission was deleted" without saying which access
	// it carried answers nothing a security review asks.
	permission, err := database.GetPermission(companyID, permissionID)
	if errors.Is(err, models.ErrPermissionNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Permission not found"})
		return
	}
	if err != nil {
		logError(c, "console read permission", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke permission"})
		return
	}

	if err := database.RevokePermission(companyID, permissionID); err != nil {
		if errors.Is(err, models.ErrPermissionNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Permission not found"})
			return
		}
		logError(c, "console revoke permission", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke permission"})
		return
	}

	recordAudit(c, auditPermissionDeleted, auditTargetPermission, permission.ID, permission.PersonID,
		gin.H{
			"scope":    permission.ScopeType,
			"effect":   permission.Effect,
			"site":     permission.SiteName,
			"terminal": permission.DeviceSerial,
		})

	c.JSON(http.StatusOK, gin.H{"revoked": true, "permission_id": permission.ID})
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

// ConsoleListSchedules handles GET /console/schedules
func ConsoleListSchedules(c *gin.Context) {
	schedules, err := database.ListSchedules(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console list schedules", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve schedules"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"count": len(schedules), "schedules": schedules})
}

// ConsoleCreateSchedule handles POST /console/schedules
func ConsoleCreateSchedule(c *gin.Context) {
	var req models.ScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	schedule, err := database.CreateSchedule(c.GetInt64("company_id"), req)
	switch {
	case errors.Is(err, models.ErrScheduleNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrScheduleNoWindows), errors.Is(err, models.ErrInvalidWindow):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		if database.IsConstraintViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logError(c, "console create schedule", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create schedule"})
		return
	}

	recordAudit(c, auditScheduleSet, auditTargetSchedule, schedule.ID, schedule.Name,
		gin.H{"windows": len(schedule.Windows), "timezone": schedule.Timezone})

	c.JSON(http.StatusCreated, schedule)
}

// ConsoleUpdateSchedule handles PUT /console/schedules/:schedule_id
//
// Windows are REPLACED when the body carries them. A schedule is a set, and the
// caller sends the set it wants.
func ConsoleUpdateSchedule(c *gin.Context) {
	var req models.ScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	schedule, err := database.UpdateSchedule(c.GetInt64("company_id"), c.Param("schedule_id"), req)
	switch {
	case errors.Is(err, models.ErrScheduleNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Schedule not found"})
		return
	case errors.Is(err, models.ErrScheduleNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrScheduleNoWindows), errors.Is(err, models.ErrInvalidWindow):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		if database.IsConstraintViolation(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logError(c, "console update schedule", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update schedule"})
		return
	}

	recordAudit(c, auditScheduleSet, auditTargetSchedule, schedule.ID, schedule.Name,
		gin.H{"windows": len(schedule.Windows), "active": schedule.Active})

	c.JSON(http.StatusOK, schedule)
}

// ConsoleDeleteSchedule handles DELETE /console/schedules/:schedule_id
//
// Refused while permissions reference it. The foreign key is ON DELETE SET NULL,
// which on a soft delete would silently widen every rule that used it -- see the
// store, where the reasoning is written out.
func ConsoleDeleteSchedule(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	scheduleID := c.Param("schedule_id")

	err := database.DeleteSchedule(companyID, scheduleID)
	switch {
	case errors.Is(err, models.ErrScheduleNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Schedule not found"})
		return
	case errors.Is(err, models.ErrScheduleInUse):
		// 409 rather than 400: the request is well formed and would be valid
		// once the rules using it are changed.
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console delete schedule", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete schedule"})
		return
	}

	recordAudit(c, auditScheduleSet, auditTargetSchedule, scheduleID, "", gin.H{"deleted": true})

	c.JSON(http.StatusOK, gin.H{"deleted": true, "schedule_id": scheduleID})
}

// ---------------------------------------------------------------------------
// The decision preview
// ---------------------------------------------------------------------------

// ConsoleEvaluateAccess handles POST /console/terminals/:serial/evaluate
//
// "WOULD THIS PERSON GET IN AT THIS TERMINAL, RIGHT NOW, AND WHY." The question
// an operator actually asks after configuring a rule, and the one that
// previously could only be answered by sending somebody to the door.
//
// WRITES NO EVENT. Authorize is deliberately separate from RecordAccessDecision
// for exactly this caller: a preview that wrote a door event would put a
// presentation that never happened into the audit trail, and an attendance
// report built on that trail would count it.
//
// The `at` parameter makes a schedule testable without waiting for Tuesday.
func ConsoleEvaluateAccess(c *gin.Context) {
	var req models.AccessEvaluationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "external_id is required"})
		return
	}

	// RequireTerminalGrant has already resolved the serial inside the caller's
	// company and authorized the site, and left the device id in the context.
	deviceID := c.GetInt64("device_id")
	if deviceID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}

	at := time.Now().UTC()
	if req.At != nil {
		at = *req.At
	}

	decision, err := database.Authorize(models.AccessRequest{
		ExternalID:   req.ExternalID,
		CredentialID: req.CredentialID,
		DeviceID:     deviceID,
		Application:  req.Application,
		At:           at,
	})
	if err != nil {
		// The decision beside the error is still DENIED and still correct; the
		// error says the evaluation could not be completed, which an operator
		// running a preview needs to know rather than read as a refusal.
		logError(c, "console evaluate access", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":    "Failed to evaluate access",
			"decision": decision,
		})
		return
	}

	c.JSON(http.StatusOK, decision)
}
