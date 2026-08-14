package handlers

import (
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Terminal lifecycle, /api/v1/console/terminals/*.
//
// The audit's SEC-01: there was no operator route that disabled a terminal,
// deleted one, moved one, or invalidated one credential. The device state
// machine had DISABLED from Sprint 6 and exactly one writer -- site retirement.
// A stolen unit could only be dealt with by retiring its whole site.
//
// AUTHORIZATION IS UPSTREAM AND IS NOT RE-DERIVED HERE. Every route below sits
// behind RequireTerminalGrant, which resolves the serial inside the caller's
// company and applies the grant rule to the site it stands at -- so a terminal
// in another tenant is a 404 and one at an ungranted site is a 403. The store
// functions repeat the company filter anyway; a handler that would read across
// tenants if it were ever mounted without its gate is a handler waiting to be
// mounted wrongly.
//
// ROLE. These are ADMIN, matching site lifecycle rather than the MANAGER gate on
// day-to-day writes. Revoking a credential stops a door working and retiring a
// terminal is one-way; neither is day-to-day work.

// ConsoleSetTerminalState handles PUT /console/terminals/:serial/state
//
// Disable or re-enable. REVERSIBLE and deliberately does not touch the
// credential -- a faulty terminal that is repaired should come back without a
// site visit to re-provision it.
func ConsoleSetTerminalState(c *gin.Context) {
	var req models.ConsoleTerminalStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Disabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "disabled is required"})
		return
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")
	actor := middleware.Operator(c)

	result, err := database.SetTerminalDisabled(companyID, serial, *req.Disabled,
		req.Reason, actorUserID(actor))
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console set terminal state", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update terminal"})
		return
	}

	action := auditTerminalEnabled
	if *req.Disabled {
		action = auditTerminalDisabled
	}
	recordAudit(c, action, auditTargetTerminal, "", serial, gin.H{"reason": req.Reason})

	respondTerminalDetail(c, companyID, serial, gin.H{
		"status":             result.Status,
		"active":             result.Active,
		"credential_cleared": result.CredentialCleared,
	})
}

// ConsoleRevokeTerminalCredential handles POST /console/terminals/:serial/revoke
//
// THE ANSWER TO A STOLEN TERMINAL. The device key hash is cleared, so the
// credential stops resolving at the index probe rather than at a check above it
// -- a status the authentication lookup does not consult authorizes nothing, and
// a revocation that depends on a check somewhere else is one a refactor can
// lose.
//
// Irreversible for that credential. The terminal must re-register to work again,
// which needs the site provisioning key, which is the authority to enrol
// hardware at that site and always was.
func ConsoleRevokeTerminalCredential(c *gin.Context) {
	var req models.ConsoleTerminalRevokeRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")
	actor := middleware.Operator(c)

	result, err := database.RevokeTerminalCredential(companyID, serial, req.Reason,
		actorUserID(actor))
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console revoke terminal credential", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke the terminal credential"})
		return
	}

	recordAudit(c, auditTerminalRevoked, auditTargetTerminal, "", serial, gin.H{
		"reason":                 req.Reason,
		"pending_jobs_cancelled": result.PendingJobsCancelled,
	})

	respondTerminalDetail(c, companyID, serial, gin.H{
		"status":             result.Status,
		"active":             result.Active,
		"credential_cleared": true,
		// Stated rather than left for an operator to discover: a revoked
		// terminal will never apply what was queued for it.
		"pending_jobs_cancelled": result.PendingJobsCancelled,
		"recovery": "This terminal must re-register with the site provisioning " +
			"key before it can authenticate again.",
	})
}

// ConsoleRetireTerminal handles DELETE /console/terminals/:serial
//
// The unit is gone: decommissioned, destroyed, returned. Soft delete, revoking
// the credential on the way out -- a retired row that still authenticates is
// exactly the orphaned hardware site retirement's comment warns about, invisible
// to every console query while its key still works.
//
// One-way through the API. Disabling is the reversible alternative.
func ConsoleRetireTerminal(c *gin.Context) {
	var req models.ConsoleTerminalRetireRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")
	actor := middleware.Operator(c)

	result, err := database.RetireTerminal(companyID, serial, req.Reason, actorUserID(actor))
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console retire terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retire the terminal"})
		return
	}

	recordAudit(c, auditTerminalRetired, auditTargetTerminal, "", serial, gin.H{
		"reason": req.Reason,
	})

	// 200 with a body rather than 204: the cancelled-work count is the fact an
	// operator needs, and an empty response would discard it.
	c.JSON(http.StatusOK, models.ConsoleTerminalRetiredResponse{
		SerialNumber:         result.SerialNumber,
		Retired:              true,
		CredentialCleared:    true,
		PendingJobsCancelled: result.PendingJobsCancelled,
	})
}

// ConsoleMoveTerminal handles PUT /console/terminals/:serial/site
//
// Reassigns a terminal to another site in the same company. Cross-company moves
// do not exist: a device reaches its company only through its site, so moving
// one across that boundary would hand whatever roster it holds to a different
// tenant.
//
// THE ROSTER IS REBUILT, NOT CARRIED. Queued work for the old site is cancelled,
// placements are marked for removal so the terminal is told to erase templates
// it may no longer hold, and the device re-converges against the new site. A
// move that kept the old roster would leave a terminal opening for the previous
// location's people.
func ConsoleMoveTerminal(c *gin.Context) {
	var req models.ConsoleTerminalMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SiteID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "site_id is required"})
		return
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	result, err := database.MoveTerminal(companyID, serial, req.SiteID)
	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, models.ErrSiteNotFound):
		// 404 rather than 400: naming a site in another tenant must not be
		// distinguishable from naming one that does not exist.
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	case err != nil:
		logError(c, "console move terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to move the terminal"})
		return
	}

	recordAudit(c, auditTerminalMoved, auditTargetTerminal, "", serial, gin.H{
		"site_id": req.SiteID,
	})

	respondTerminalDetail(c, companyID, serial, gin.H{
		"moved":                  true,
		"pending_jobs_cancelled": result.PendingJobsCancelled,
	})
}

// ConsoleResyncTerminal handles POST /console/terminals/:serial/resync
//
// Replaces a terminal's queue with a snapshot of current state, for a unit
// believed to have drifted.
//
// The same operation the site-key route offers, reachable by the identity that
// actually needs it. That route authenticates with the provisioning secret,
// which a browser must never hold, so an operator who suspected drift had no way
// to act on it.
func ConsoleResyncTerminal(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	superseded, pending, err := database.ResyncTerminal(companyID, serial)
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console resync terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue a full sync"})
		return
	}

	recordAudit(c, auditTerminalResynced, auditTargetTerminal, "", serial, nil)

	c.JSON(http.StatusOK, gin.H{
		"serial_number":   serial,
		"superseded_jobs": superseded,
		"pending_jobs":    pending,
	})
}

// respondTerminalDetail reloads the terminal and merges the operation's own
// facts into the response.
//
// The detail shape is returned from every mutation so a client can replace what
// it holds rather than refetching, which is the convention the application-mode
// route already set.
func respondTerminalDetail(c *gin.Context, companyID int64, serial string, extra gin.H) {
	detail, err := loadTerminalDetail(companyID, serial)
	if err != nil {
		// The operation succeeded; only the read-back failed. Report what is
		// known rather than a 500 that would invite a retry of a completed
		// mutation.
		logError(c, "console reload terminal", err)
		c.JSON(http.StatusOK, extra)
		return
	}

	body := gin.H{"terminal": detail}
	for k, v := range extra {
		body[k] = v
	}
	c.JSON(http.StatusOK, body)
}

// actorUserID returns the operator's internal id, or 0 when there is none.
//
// 0 rather than an error: these columns are attribution, and a mutation must not
// fail because attribution was unavailable.
func actorUserID(identity *models.OperatorIdentity) int64 {
	if identity == nil {
		return 0
	}
	return identity.UserID
}
