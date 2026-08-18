package handlers

import (
	"errors"
	"log"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
)

// Announce and approve (migrations/022, database/announcements.go).
//
// Three audiences in one file, which is unusual and is the point: they are three
// halves of one exchange and the security argument only reads correctly when
// they are next to each other.
//
//	TERMINAL   POST /api/v1/devices/announce   unauthenticated -- it has no
//	           GET  /api/v1/devices/announce   credential, and obtaining one is
//	                                           what this is for. The GET carries
//	                                           the announce token, so only the
//	                                           caller that created an
//	                                           announcement can collect against
//	                                           it.
//
//	OPERATOR   /api/v1/console/terminal-announcements/*   session + CSRF, ADMIN
//	           to adopt/approve/reject, MANAGER to read. A signed-in human typing
//	           a code displayed on a physical unit is the ONLY thing that turns
//	           an announcement into a credential.
//
//	PLATFORM   POST /api/v1/platform/terminals/:serial/release   the only route
//	           that detaches a serial from a company, and it is not a transfer.
//
// WHAT THIS ADDS TO CLAIM CODES RATHER THAN REPLACING. A claim code is minted
// for a serial an operator already knows, which is right for pre-authorised and
// staged installs and impossible for a customer holding a box -- the serial is
// derived from the factory MAC and readable only over a USB cable. Both paths
// stay; neither weakens the other; and no open, any-hardware-can-redeem code
// exists in either.

// ---------------------------------------------------------------------------
// The terminal
// ---------------------------------------------------------------------------

// AnnounceTerminalHeader is where a terminal presents its announce token.
const AnnounceTerminalHeader = "X-Announce-Token"

// AnnounceTerminal handles POST /api/v1/devices/announce
//
// UNAUTHENTICATED BY NECESSITY, and safe because WHAT IT CREATES GRANTS NOTHING.
// The row it writes has no company, is visible to no operator anywhere, and can
// only ever become a credential by a route that requires an authenticated
// administrator to type a code that is displayed on the terminal's own panel.
//
// NOT AUDITED HERE, and that is a schema fact rather than an oversight:
// audit_events.company_id is NOT NULL, and an announcement has no company by
// construction. Writing one against a guessed tenant would be worse than not
// writing it. The event is recorded in the operational log, and the announcement's
// own facts -- serial, when it announced, where from, how many times -- are
// carried into the TERMINAL_ADOPTED audit record, which is the first moment a
// company is entitled to any of it.
func AnnounceTerminal(c *gin.Context) {
	// ShouldBindBodyWith rather than ShouldBindJSON, and the difference matters
	// on this route alone: AnnounceRateLimiter reads the serial out of the body
	// to key its per-terminal bucket, which drains the reader. Binding through
	// the cached copy is what lets both read the same request.
	//
	// It is not a dependency on the middleware having run -- with no cached copy
	// this reads the body itself, exactly as ShouldBindJSON would.
	var req models.AnnounceRequest
	if err := c.ShouldBindBodyWith(&req, binding.JSON); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "serial_number is required"})
		return
	}

	result, err := database.Announce(database.AnnounceRequest{
		SerialNumber:     req.SerialNumber,
		FirmwareVersion:  req.FirmwareVersion,
		HardwareRevision: req.HardwareRevision,
		Capabilities:     req.Capabilities,
		IPAddress:        c.ClientIP(),
		PresentedToken:   c.GetHeader(AnnounceTerminalHeader),
	})
	switch {
	case errors.Is(err, database.ErrAnnouncementSerialRequired),
		errors.Is(err, models.ErrDeviceSerialTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "announce terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to announce"})
		return
	}

	// THE PAIRING CODE IS NOT LOGGED. Only the serial and the outcome, so a
	// support engineer reading logs can see that a unit announced without being
	// able to adopt it themselves.
	logAnnounce(c, result)

	status := http.StatusCreated
	if result.Existing {
		// 200 rather than 201: nothing was created. A terminal re-announcing on a
		// cycle, or after a reboot, lands here.
		status = http.StatusOK
	}

	c.JSON(status, models.AnnounceResponse{
		AnnouncementID:   result.PublicID,
		State:            result.State,
		SerialNumber:     result.SerialNumber,
		PairingCode:      result.PairingCode,
		AnnounceToken:    result.AnnounceToken,
		ExpiresAt:        result.ExpiresAt,
		PollAfterSeconds: database.AnnouncePollSeconds,
	})
}

// AnnouncementStatus handles GET /api/v1/devices/announce
//
// AUTHENTICATED BY THE ANNOUNCE TOKEN. This is the endpoint that hands over a
// device credential, so it is not left unauthenticated: the token proves the
// caller is the unit that created the announcement, which is what stops a
// credential being collected by something that merely learned a serial.
//
// ONE-SHOT. The credential is minted, the announcement is marked COLLECTED, and
// the same token never yields another. A terminal that loses what it was given
// re-announces and is approved again -- that is a deliberate trade, because the
// alternative is an endpoint that will re-issue a working key to anything
// replaying a token.
func AnnouncementStatus(c *gin.Context) {
	token := c.GetHeader(AnnounceTerminalHeader)

	result, err := database.AnnouncementStatus(token, c.ClientIP())
	switch {
	case errors.Is(err, database.ErrAnnouncementUnknownToken):
		// ONE ANSWER for an absent, malformed or unknown token. A caller with no
		// token is not entitled to learn whether some other token would have
		// worked.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unknown announce token"})
		return
	case err != nil:
		logError(c, "announcement status", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read announcement status"})
		return
	}

	body := models.AnnounceStatusResponse{
		State:            result.State,
		SerialNumber:     result.SerialNumber,
		PollAfterSeconds: database.AnnouncePollSeconds,
	}
	if !result.ExpiresAt.IsZero() {
		expires := result.ExpiresAt
		body.ExpiresAt = &expires
	}

	if result.State == database.AnnounceStateApproved {
		body.APIKey = result.APIKey
		body.CompanyName = result.CompanyName
		body.SiteName = result.SiteName
		body.DeviceName = result.DeviceName

		// AUDITED WITH NO OPERATOR ON THE REQUEST, exactly as claim redemption is.
		//
		// The operator named is the one who AUTHORISED this -- they approved it --
		// not one who performed it: there is no human on this call at all. The
		// same choice the DEVICE_CLAIMED record makes, and the alternative
		// (an actor-less row) reads in the trail as though nobody decided.
		//
		// THE KEY IS NOT HERE and must never be.
		database.WriteAuditEvent(database.AuditEntry{
			CompanyID:   result.CompanyID,
			ActorEmail:  result.ApprovedByEmail,
			IPAddress:   c.ClientIP(),
			UserAgent:   c.Request.UserAgent(),
			RequestID:   middleware.RequestID(c),
			Action:      auditTerminalCollected,
			TargetType:  auditTargetTerminal,
			TargetLabel: result.SerialNumber,
			Changes: gin.H{
				"site":           result.SiteName,
				"device_name":    result.DeviceName,
				"bootstrap_jobs": result.BootstrapJobs,
				"approved_by":    result.ApprovedByEmail,
			},
		})
	}

	c.JSON(http.StatusOK, body)
}

// logAnnounce records an announcement operationally.
//
// The SERIAL and the OUTCOME, tagged with the request id so it joins the request
// line. NEVER the pairing code and NEVER the token: a log that carried either
// would be a place to read a credential-in-waiting, and logs are the one store
// nobody treats as sensitive.
//
// This is also the only trace an announcement leaves until somebody adopts it,
// because audit_events requires a company and an un-adopted announcement has
// none. See the note on AnnounceTerminal.
func logAnnounce(c *gin.Context, result *database.AnnouncedTerminal) {
	outcome := "announced"
	if result.Existing {
		outcome = "re-announced state=" + result.State
	}
	log.Printf("request_id=%s terminal %s serial=%s",
		middleware.RequestID(c), outcome, result.SerialNumber)
}

// ---------------------------------------------------------------------------
// The console
// ---------------------------------------------------------------------------

// ConsoleAdoptAnnouncement handles POST /console/terminal-announcements/adopt
//
// THE ONE FIELD THE CUSTOMER FILLS IN. An eight-character code displayed on the
// terminal, and this is the moment an announcement stops being invisible and
// becomes this company's problem.
//
// ADMIN, matching claim-code issue and site-key rotation, and for the same
// reason: adopting is the first half of authorising hardware to join and be
// handed a credential.
//
// THREE REFUSALS, and only the first is uniform:
//
//	unknown / expired / already-adopted code -> 404, one message. Telling them
//	    apart would let somebody in a console distinguish "no such code" from
//	    "real but spent", which is what makes guessing worth doing.
//	serial owned by another company           -> 409, naming no company, no site
//	    and no operator. The caller learns that they may not have it and nothing
//	    whatever about who does.
//	serial disabled inside THIS company       -> 409 with the remedy, because it
//	    is their own hardware and the fix is one click on their own screen.
func ConsoleAdoptAnnouncement(c *gin.Context) {
	var req models.AdoptAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pairing_code is required"})
		return
	}

	actor := middleware.Operator(c)
	actorEmail := ""
	if actor != nil {
		actorEmail = actor.Email
	}

	pending, err := database.AdoptAnnouncement(c.GetInt64("company_id"),
		actorUserID(actor), actorEmail, req.PairingCode)
	switch {
	case errors.Is(err, database.ErrAnnouncementRefused):
		c.JSON(http.StatusNotFound, gin.H{
			"error": "That code was not recognised. Codes expire after 15 minutes — " +
				"check the terminal's screen for the current one.",
			"code": "PAIRING_CODE_REFUSED",
		})
		return

	case errors.Is(err, database.ErrTerminalOwnedElsewhere):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "TERMINAL_OWNED_ELSEWHERE",
		})
		return

	case errors.Is(err, database.ErrTerminalDisabledLocally):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "TERMINAL_DISABLED",
		})
		return

	case err != nil:
		logError(c, "adopt announcement", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add the terminal"})
		return
	}

	// The announcement's own facts land here, at the first moment a company is
	// entitled to them. See AnnounceTerminal on why there is no separate
	// TERMINAL_ANNOUNCED record.
	recordAudit(c, auditTerminalAdopted, auditTargetTerminal, pending.PublicID,
		pending.SerialNumber, gin.H{
			"verdict":       pending.Verdict,
			"announced_at":  pending.AnnouncedAt,
			"first_seen_ip": pending.FirstSeenIP,
			"firmware":      pending.FirmwareVersion,
		})

	c.JSON(http.StatusOK, projectPendingTerminal(*pending))
}

// ConsoleListPendingTerminals handles GET /console/terminal-announcements
//
// MANAGER: seeing that a terminal is waiting is operational information, and the
// person who unpacked the box is often not an administrator. Acting on it is
// ADMIN.
//
// SCOPED TO THE COMPANY, and there is no route at all that lists announcements
// belonging to nobody -- which is what keeps one customer's unclaimed hardware
// invisible to every other.
func ConsoleListPendingTerminals(c *gin.Context) {
	pending, err := database.ListAnnouncements(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "list pending terminals", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve terminals waiting to be set up"})
		return
	}

	items := make([]models.PendingTerminal, 0, len(pending))
	for _, item := range pending {
		items = append(items, projectPendingTerminal(item))
	}

	c.JSON(http.StatusOK, models.PendingTerminalsResponse{
		Count:   len(items),
		Pending: items,
	})
}

// ConsoleGetPendingTerminal handles GET /console/terminal-announcements/:id
func ConsoleGetPendingTerminal(c *gin.Context) {
	pending, err := database.GetAnnouncement(c.GetInt64("company_id"), c.Param("id"))
	if errors.Is(err, database.ErrAnnouncementNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "get pending terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve the terminal"})
		return
	}
	c.JSON(http.StatusOK, projectPendingTerminal(*pending))
}

// ConsoleApproveAnnouncement handles
// POST /console/terminal-announcements/:id/approve
//
// AUTHORISES; MINTS NOTHING. What this writes is the decision -- which site, what
// the unit is called -- and the credential is generated when the terminal comes
// to collect it. A key minted here would have to be STORED in plaintext until the
// device arrived, which is the one thing every other secret in this schema is
// careful never to do.
//
// THE SITE IS NAMED IN THE BODY, so this route cannot go behind
// RequireSiteGrant. That is the same position ConsoleMoveTerminal is in and it is
// resolved the same way: the store looks the site up INSIDE the caller's company,
// so another tenant's site is a 404, and the route is ADMIN -- a role that is
// never site-scoped under the grant rule, so there is no scoped operator whose
// grants this could bypass.
func ConsoleApproveAnnouncement(c *gin.Context) {
	var req models.ApproveAnnouncementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "site_id is required"})
		return
	}

	actor := middleware.Operator(c)
	actorEmail := ""
	if actor != nil {
		actorEmail = actor.Email
	}

	pending, err := database.ApproveAnnouncement(c.GetInt64("company_id"),
		actorUserID(actor), actorEmail, c.Param("id"), req.SiteID, req.DeviceName)
	switch {
	case errors.Is(err, database.ErrAnnouncementNotFound):
		// Also the answer for an announcement that expired while the operator was
		// on the screen: it can no longer be approved, and the terminal is already
		// displaying a new code.
		c.JSON(http.StatusNotFound, gin.H{
			"error": "That terminal is no longer waiting to be set up. " +
				"Check its screen for a new code and add it again.",
			"code": "ANNOUNCEMENT_NOT_PENDING",
		})
		return

	case errors.Is(err, models.ErrSiteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return

	case errors.Is(err, database.ErrTerminalOwnedElsewhere):
		// RE-CHECKED HERE rather than trusted from adoption. Minutes pass between
		// the two, and in them the serial can acquire a device row somewhere else.
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "TERMINAL_OWNED_ELSEWHERE",
		})
		return

	case errors.Is(err, database.ErrTerminalDisabledLocally):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "TERMINAL_DISABLED",
		})
		return

	case err != nil:
		logError(c, "approve announcement", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve the terminal"})
		return
	}

	recordAudit(c, auditTerminalApproved, auditTargetTerminal, pending.PublicID,
		pending.SerialNumber, gin.H{
			"site":        pending.SiteName,
			"device_name": pending.DeviceName,
			"verdict":     pending.Verdict,
		})

	c.JSON(http.StatusOK, projectPendingTerminal(*pending))
}

// ConsoleRejectAnnouncement handles
// POST /console/terminal-announcements/:id/reject
//
// Refuses a terminal, and is also the UNDO for an approval. The second is the
// operationally important one: it releases the serial, so a unit that was
// approved and then factory-reset -- losing the announce token it needed to
// collect with -- can announce again instead of being deadlocked against its own
// stale approval.
func ConsoleRejectAnnouncement(c *gin.Context) {
	var req models.RejectAnnouncementRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	actor := middleware.Operator(c)
	actorEmail := ""
	if actor != nil {
		actorEmail = actor.Email
	}

	pending, err := database.RejectAnnouncement(c.GetInt64("company_id"),
		actorEmail, c.Param("id"), req.Reason)
	if errors.Is(err, database.ErrAnnouncementNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "reject announcement", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject the terminal"})
		return
	}

	recordAudit(c, auditTerminalRejected, auditTargetTerminal, pending.PublicID,
		pending.SerialNumber, gin.H{"reason": req.Reason})

	c.JSON(http.StatusOK, projectPendingTerminal(*pending))
}

// projectPendingTerminal maps the store's projection onto the wire shape.
//
// A FUNCTION RATHER THAN JSON TAGS ON THE STORE TYPE, so that adding a field to
// the store -- which is where the announce token and the code hashes live -- can
// never serialise one by accident.
func projectPendingTerminal(item database.Announcement) models.PendingTerminal {
	out := models.PendingTerminal{
		ID:               item.PublicID,
		SerialNumber:     item.SerialNumber,
		State:            item.State,
		Verdict:          item.Verdict,
		FirmwareVersion:  item.FirmwareVersion,
		HardwareRevision: item.HardwareRevision,
		Capabilities:     item.Capabilities,
		FirstSeenIP:      item.FirstSeenIP,
		LastSeenIP:       item.LastSeenIP,
		LastSeenAt:       item.LastSeenAt,
		AnnouncedAt:      item.AnnouncedAt,
		AdoptedBy:        item.AdoptedByEmail,
		AdoptedAt:        item.AdoptedAt,
		SiteID:           item.SitePublicID,
		SiteName:         item.SiteName,
		DeviceName:       item.DeviceName,
		ApprovedBy:       item.ApprovedByEmail,
		ApprovedAt:       item.ApprovedAt,
		ExpiresAt:        item.ExpiresAt,
	}
	if item.ExistingTerminal != nil {
		out.ExistingTerminal = &models.ExistingTerminalSummary{
			SerialNumber: item.ExistingTerminal.SerialNumber,
			DeviceName:   item.ExistingTerminal.DeviceName,
			SiteName:     item.ExistingTerminal.SiteName,
			Status:       item.ExistingTerminal.Status,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The platform administrator
// ---------------------------------------------------------------------------

// PlatformReleaseTerminal handles POST /api/v1/platform/terminals/:serial/release
//
// THE ONLY ROUTE ON THE PLATFORM THAT MOVES HARDWARE BETWEEN TENANTS, and it is
// deliberately only half of a move: it releases the serial, and whoever gets it
// next adopts it through the ordinary flow with their own administrator's
// approval. There is no single call that takes a terminal from company A and
// gives it to company B, because that call would be a credential capable of
// reassigning any door on the platform, and because the two halves genuinely
// need two different people to agree.
//
// PLATFORM IDENTITY ONLY. A tenant operator cannot reach it from either side:
// the company losing the unit cannot be made to give it up by the company that
// wants it, and the company that wants it cannot help itself.
//
// AUDITED INTO THE COMPANY THAT LOSES THE TERMINAL, because that is the tenant
// whose door stopped working and whose trail has to explain why. The gaining
// company's side of the story is its own TERMINAL_ADOPTED record, written when
// they adopt -- two records rather than one, because they are two decisions
// taken by two people at two times.
func PlatformReleaseTerminal(c *gin.Context) {
	var req models.ReleaseTerminalRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	serial := c.Param("serial")

	released, err := database.ReleaseTerminalSerial(serial, req.Reason)
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "platform release terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to release the terminal"})
		return
	}

	// recordPlatformAudit, not recordAudit: the trail entry belongs to the
	// company that LOST the terminal, and the actor is a platform administrator
	// who is not a row in `users` at all. That helper is exactly the one that
	// writes an action into a company's trail on behalf of an identity from
	// outside it.
	recordPlatformAudit(c, released.CompanyID, auditTerminalReleased, "",
		released.SerialNumber, gin.H{
			"site":                   released.SiteName,
			"device_name":            released.DeviceName,
			"reason":                 req.Reason,
			"pending_jobs_cancelled": released.PendingJobsCancelled,
			"announcements_voided":   released.AnnouncementsVoided,
		})

	c.JSON(http.StatusOK, models.ReleaseTerminalResponse{
		SerialNumber:         released.SerialNumber,
		Released:             true,
		PreviousCompany:      released.CompanyName,
		PreviousSite:         released.SiteName,
		PendingJobsCancelled: released.PendingJobsCancelled,
		AnnouncementsVoided:  released.AnnouncementsVoided,
	})
}
