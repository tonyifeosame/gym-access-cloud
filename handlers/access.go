package handlers

import (
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// CheckAccess handles GET /access/:member_id
func CheckAccess(c *gin.Context) {
	memberID := c.Param("member_id")

	response, err := database.CheckMemberAccess(c.GetInt64("company_id"), memberID)
	if err != nil {
		logError(c, "check access", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check access"})
		return
	}

	c.JSON(http.StatusOK, response)
}

// LogAccess handles POST /access/log
func LogAccess(c *gin.Context) {
	var req models.AccessLogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get site_name from context (set by auth middleware)
	siteName, exists := c.Get("site_name")
	if !exists {
		siteName = req.SiteName
	}

	// An unrecognised credential is logged with no member reference rather than
	// an empty string, so it is stored as NULL and never matches a real person.
	var memberID *string
	if req.MemberID != "" {
		memberID = &req.MemberID
	}

	log := models.AccessLog{
		MemberID: memberID,
		Granted:  req.Granted,
		Source:   req.Source,
		SiteName: siteName.(string),
		Message:  req.Message,
	}

	if err := database.CreateAccessLog(c.GetInt64("company_id"), c.GetInt64("site_id"), &log); err != nil {
		logError(c, "create access log", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log access"})
		return
	}

	c.JSON(http.StatusCreated, log)
}

// Access logs grow with every door scan, so an unbounded `limit` would let one
// request pull the entire audit history into memory. Mirrors the cap the device
// job endpoint already applies.
const (
	defaultLogLimit = 100
	maxLogLimit     = 1000
)

// clampLogLimit parses a limit query parameter, falling back to the default and
// capping it rather than rejecting an over-large value.
func clampLogLimit(raw string) int {
	limit := defaultLogLimit
	if raw != "" {
		if l, err := strconv.Atoi(raw); err == nil && l > 0 {
			limit = l
		}
	}
	if limit > maxLogLimit {
		limit = maxLogLimit
	}
	return limit
}

// GetAccessLogs handles GET /access/logs
func GetAccessLogs(c *gin.Context) {
	offset := 0
	limit := clampLogLimit(c.Query("limit"))

	if offsetStr := c.Query("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// NARROWED TO THE AUTHENTICATED SITE. This returned the whole company's
	// audit trail to any site key -- a secret installed on hardware at one
	// location reading who entered every other location. The console's
	// grant-scoped event history is where a company-wide view belongs.
	//
	// The scope comes from the authenticated context, so there is no parameter
	// through which a caller could widen it.
	logs, err := database.GetSiteAccessLogs(c.GetInt64("company_id"), c.GetInt64("site_id"),
		limit, offset)
	if err != nil {
		logError(c, "list access logs", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve access logs"})
		return
	}
	// Empty must be `[]`, not `null`, so a client can iterate unconditionally
	if logs == nil {
		logs = []models.AccessLog{}
	}

	c.JSON(http.StatusOK, logs)
}

// GetMemberAccessLogs handles GET /access/logs/:member_id
func GetMemberAccessLogs(c *gin.Context) {
	memberID := c.Param("member_id")
	limit := clampLogLimit(c.Query("limit"))

	// Site-scoped, like the list above. Reconstructing one person's movements
	// across every location a company operates is exactly what a credential
	// installed at one of them must not be able to do.
	logs, err := database.GetSiteAccessLogsByMember(c.GetInt64("company_id"),
		c.GetInt64("site_id"), memberID, limit)
	if err != nil {
		logError(c, "list member access logs", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve access logs"})
		return
	}
	if logs == nil {
		logs = []models.AccessLog{}
	}

	c.JSON(http.StatusOK, logs)
}

// LogDeviceAccess handles POST /api/v1/devices/access/log
//
// The terminal's own endpoint for reporting door events. Everything that
// identifies WHERE the event happened comes from the device credential; the
// body carries only what happened.
//
// This exists because the operator endpoint takes the site API key, and that
// key is the provisioning secret -- it can register devices and rotate their
// credentials. Requiring it on every terminal so a door could write an audit
// line would have made the audit trail the weakest thing in the system.
func LogDeviceAccess(c *gin.Context) {
	var req models.DeviceAccessLogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// An unrecognised finger is logged with no member reference rather than an
	// empty string, so it stores as NULL and matches no real person.
	var memberID *string
	if req.MemberID != "" {
		memberID = &req.MemberID
	}

	// Parsed leniently: a terminal whose clock has never synced sends nothing,
	// and one with a broken clock should not be able to stop its own events
	// being recorded. Either way the server falls back to its own time.
	var occurredAt time.Time
	if req.OccurredAt != "" {
		if parsed, err := time.Parse(time.RFC3339, req.OccurredAt); err == nil {
			occurredAt = parsed
		}
	}

	// site_name is descriptive; the authoritative site is site_id below. Taken
	// from the authenticated context so a device cannot label its events with
	// another site's name.
	siteName, _ := c.Get("site_name")
	name, _ := siteName.(string)

	companyID := c.GetInt64("company_id")
	siteID := c.GetInt64("site_id")
	deviceID := c.GetInt64("device_id")

	created, err := database.CreateDeviceAccessLog(
		companyID, siteID, deviceID,
		req.EventID, memberID, req.Granted, req.Source, name, req.Message,
		occurredAt)
	if err != nil {
		logError(c, "create device access log", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log access"})
		return
	}

	// The typed event trail (APP-04), written BESIDE access_logs rather than
	// instead of it.
	//
	// access_logs is a frozen wire contract -- deployed firmware uploads to it
	// and the compatibility policy says so -- but its shape cannot carry an
	// attendance record, a check-in or a reason code, so it cannot be the model
	// going forward. Both are written from this one handler so the trail and the
	// log can never disagree about what a terminal reported.
	//
	// NEVER FAILS THE REQUEST. The person is already through the door; the
	// terminal is reporting history. Refusing the upload because the event row
	// could not be written would make the terminal retry for ever and lose the
	// access_log that DID commit.
	recordDoorEvent(c, companyID, siteID, deviceID, req, occurredAt)

	// 200 either way, and `duplicate` says which. A replay MUST NOT be an error:
	// the terminal retries precisely because it did not hear the first answer,
	// and telling it "failed" would make it retry for ever.
	c.JSON(http.StatusOK, gin.H{
		"event_id":  req.EventID,
		"recorded":  created,
		"duplicate": !created,
	})
}

// recordDoorEvent writes the typed event for one reported door presentation, and
// checks the terminal's decision against the platform's own.
//
// WHAT THE EVENT RECORDS IS WHAT HAPPENED, NOT WHAT SHOULD HAVE. The decision
// stored is the terminal's, because that is the one that actually released a
// lock or did not. Substituting the server's re-evaluation would produce a trail
// that disagrees with reality -- an operator reading "DENIED" about somebody who
// demonstrably walked in.
//
// The server's own evaluation is recorded BESIDE it, in the payload, when the
// two differ. That divergence is the actionable signal this platform previously
// had no way to produce: a terminal running on a stale cache admitting somebody
// whose permission was revoked while it was offline. It is exactly what the
// site's offline policy exists to bound, and now it is visible.
func recordDoorEvent(c *gin.Context, companyID, siteID, deviceID int64,
	req models.DeviceAccessLogRequest, occurredAt time.Time) {

	eventType := models.EventAccessDenied
	decision := models.DecisionDenied
	if req.Granted {
		eventType = models.EventAccessGranted
		decision = models.DecisionGranted
	}

	payload := map[string]any{"source": req.Source}
	if req.Message != "" {
		payload["message"] = req.Message
	}

	// Re-evaluate against the platform's current rules. Best effort: an
	// evaluation that cannot be completed must not stop the event being
	// recorded, so the divergence fields are simply absent.
	reason := ""
	if verdict, err := database.Authorize(models.AccessRequest{
		ExternalID: req.MemberID,
		DeviceID:   deviceID,
		At:         occurredAt,
	}); err == nil && verdict != nil {
		reason = verdict.Reason
		if verdict.Granted != req.Granted {
			payload["server_decision"] = map[string]any{
				"granted": verdict.Granted,
				"reason":  verdict.Reason,
			}
			// Named so it can be alarmed on and searched for. A terminal
			// admitting somebody the platform would refuse is the failure mode
			// an offline grace window is a trade against, not an accident.
			payload["diverged"] = true
		}
	}

	personID, _ := database.ResolvePersonRowID(companyID, req.MemberID)

	if _, err := database.RecordAccessEvent(database.AccessEvent{
		// The device's event id is the idempotency key for BOTH tables, so a
		// retried upload produces neither a second log nor a second event.
		PublicID:          req.EventID,
		CompanyID:         companyID,
		SiteID:            siteID,
		DeviceID:          deviceID,
		PersonID:          personID,
		SubjectExternalID: req.MemberID,
		EventType:         eventType,
		Decision:          decision,
		ReasonCode:        reason,
		Payload:           payload,
		OccurredAt:        occurredAt,
		OccurredAtTrusted: !occurredAt.IsZero(),
	}); err != nil {
		database.LogEventFailure(companyID, deviceID, eventType, err)
	}
}
