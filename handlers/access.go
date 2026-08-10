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

	logs, err := database.GetAccessLogs(c.GetInt64("company_id"), limit, offset)
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

	logs, err := database.GetAccessLogsByMember(c.GetInt64("company_id"), memberID, limit)
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

	created, err := database.CreateDeviceAccessLog(
		c.GetInt64("company_id"),
		c.GetInt64("site_id"),
		c.GetInt64("device_id"),
		req.EventID, memberID, req.Granted, req.Source, name, req.Message,
		occurredAt)
	if err != nil {
		logError(c, "create device access log", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log access"})
		return
	}

	// 200 either way, and `duplicate` says which. A replay MUST NOT be an error:
	// the terminal retries precisely because it did not hear the first answer,
	// and telling it "failed" would make it retry for ever.
	c.JSON(http.StatusOK, gin.H{
		"event_id":  req.EventID,
		"recorded":  created,
		"duplicate": !created,
	})
}
