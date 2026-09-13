package handlers

import (
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Terminal release, the console half (migrations/032, database/release.go).
//
// Four routes, ADMIN, all on the live row in the caller's own company:
//
//	POST   /console/terminals/:serial/release         order
//	GET    /console/terminals/:serial/release         read
//	DELETE /console/terminals/:serial/release         cancel (ORDERED only)
//	POST   /console/terminals/:serial/release/force   finalize without the terminal
//
// WHAT IS DELIBERATELY NOT HERE: a route that gives the terminal to another
// company. Release is half of a transfer; the other half is the next owner's
// own adoption, with their own administrator's approval. The platform route
// in announcements.go is the same shape for the same reason.

// ConsoleOrderTerminalRelease handles POST /console/terminals/:serial/release
//
// Idempotent: a second order while one is outstanding answers 200 with the
// existing order and writes nothing, so a retried request cannot mint a
// second order and orphan the one a terminal is already executing.
func ConsoleOrderTerminalRelease(c *gin.Context) {
	var req models.ConsoleTerminalReleaseRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")
	actor := middleware.Operator(c)
	actorEmail := ""
	if actor != nil {
		actorEmail = actor.Email
	}

	release, created, err := database.OrderTerminalRelease(companyID, serial,
		req.Reason, actorUserID(actor), actorEmail)
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console order terminal release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to order the release"})
		return
	}

	if created {
		recordAudit(c, auditTerminalReleaseOrdered, auditTargetTerminal, "", serial, gin.H{
			"reason":           req.Reason,
			"release_id":       release.ReleaseID,
			"terminal_capable": release.TerminalCapable,
			"order_verifiable": release.OrderVerifiable,
		})
	}

	c.JSON(http.StatusOK, projectTerminalRelease(*release))
}

// ConsoleGetTerminalRelease handles GET /console/terminals/:serial/release
func ConsoleGetTerminalRelease(c *gin.Context) {
	release, err := database.GetTerminalRelease(c.GetInt64("company_id"), c.Param("serial"))
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console get terminal release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the release"})
		return
	}
	c.JSON(http.StatusOK, projectTerminalRelease(*release))
}

// ConsoleCancelTerminalRelease handles DELETE /console/terminals/:serial/release
//
// 409 when nothing is ordered: a cancel that "succeeds" against nothing would
// let a console believe it withdrew something.
func ConsoleCancelTerminalRelease(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	release, cancelledReleaseID, err := database.CancelTerminalRelease(companyID, serial)
	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, database.ErrReleaseNotOrdered):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "RELEASE_NOT_ORDERED"})
		return
	case err != nil:
		logError(c, "console cancel terminal release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel the release"})
		return
	}

	// NAMES THE ORDER IT WITHDREW. Without the id a CANCELLED line cannot be
	// tied to anything -- a serial that has been through several release cycles
	// collects indistinguishable cancellations -- and it was the one action of
	// the four that did not record it. The id comes back from the cancel itself,
	// read under its FOR UPDATE, so it is the order that was actually cleared.
	recordAudit(c, auditTerminalReleaseCancelled, auditTargetTerminal, "", serial, gin.H{
		"release_id": cancelledReleaseID,
	})

	c.JSON(http.StatusOK, projectTerminalRelease(*release))
}

// ConsoleForceTerminalRelease handles POST /console/terminals/:serial/release/force
//
// THE ESCALATION, and the only way a release completes without the terminal
// proving it wiped. Requires an outstanding order (an operator cannot force
// what they never ordered) and an explicit attestation in the body, which the
// console only sets after a typed confirmation. The body names the order the
// attestation was typed against; a force naming any other order is refused
// with RELEASE_MISMATCH, so a stale page cannot finalize an order its operator
// never read. The consequence -- the unit
// keeps admitting this company's members under this company's offline policy
// until it reconnects or is wiped at the unit -- is the operator's to accept,
// and it is recorded with the action.
func ConsoleForceTerminalRelease(c *gin.Context) {
	var req models.ConsoleTerminalForceReleaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.Attest {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "attest must be true: forcing a release means the terminal will " +
				"keep working for your members until it reconnects or is wiped at the unit",
			"code": "ATTESTATION_REQUIRED",
		})
		return
	}

	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	// The order's facts, for the audit line, read before they are gone.
	before, err := database.GetTerminalRelease(companyID, serial)
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console force terminal release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to release the terminal"})
		return
	}

	fin, err := database.ForceTerminalRelease(companyID, serial, req.Reason, req.ReleaseID)
	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, database.ErrReleaseNotOrdered):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "RELEASE_NOT_ORDERED"})
		return
	case errors.Is(err, database.ErrReleaseMismatch):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "RELEASE_MISMATCH"})
		return
	case err != nil:
		logError(c, "console force terminal release", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to release the terminal"})
		return
	}

	recordAudit(c, auditTerminalReleaseForced, auditTargetTerminal, "", serial, gin.H{
		"reason":                 req.Reason,
		"release_id":             fin.ReleaseID,
		"attested":               true,
		"terminal_capable":       before.TerminalCapable,
		"order_verifiable":       before.OrderVerifiable,
		"last_seen_at":           before.LastSeenAt,
		"pending_jobs_cancelled": fin.PendingJobsCancelled,
		"announcements_voided":   fin.AnnouncementsVoided,
	})

	c.JSON(http.StatusOK, models.ConsoleTerminalReleasedResponse{
		SerialNumber:         fin.SerialNumber,
		ReleaseID:            fin.ReleaseID,
		Released:             true,
		ConfirmedBy:          fin.ConfirmedBy,
		PendingJobsCancelled: fin.PendingJobsCancelled,
		AnnouncementsVoided:  fin.AnnouncementsVoided,
	})
}

func projectTerminalRelease(r database.TerminalRelease) models.ConsoleTerminalRelease {
	return models.ConsoleTerminalRelease{
		SerialNumber:    r.SerialNumber,
		State:           r.State,
		ReleaseID:       r.ReleaseID,
		OrderedAt:       r.OrderedAt,
		OrderedByEmail:  r.OrderedByEmail,
		Reason:          r.Reason,
		TerminalCapable: r.TerminalCapable,
		OrderVerifiable: r.OrderVerifiable,
		LastSeenAt:      r.LastSeenAt,
	}
}
