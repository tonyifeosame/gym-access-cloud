package handlers

import (
	"net/http"
	"strconv"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// CheckAccess handles GET /access/:member_id
func CheckAccess(c *gin.Context) {
	memberID := c.Param("member_id")

	response, err := database.CheckMemberAccess(c.GetInt64("company_id"), memberID)
	if err != nil {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve access logs"})
		return
	}
	if logs == nil {
		logs = []models.AccessLog{}
	}

	c.JSON(http.StatusOK, logs)
}
