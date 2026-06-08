package handlers

import (
	"net/http"
	"strconv"

	"gym-access-api/database"
	"gym-access-api/models"

	"github.com/gin-gonic/gin"
)

// CheckAccess handles GET /access/:member_id
func CheckAccess(c *gin.Context) {
	memberID := c.Param("member_id")
	
	response, err := database.CheckMemberAccess(memberID)
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

	log := models.AccessLog{
		MemberID: &req.MemberID,
		Granted:   req.Granted,
		Source:    req.Source,
		SiteName:  siteName.(string),
		Message:   req.Message,
	}

	if err := database.CreateAccessLog(&log); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log access"})
		return
	}

	c.JSON(http.StatusCreated, log)
}

// GetAccessLogs handles GET /access/logs
func GetAccessLogs(c *gin.Context) {
	limit := 100
	offset := 0

	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	if offsetStr := c.Query("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	logs, err := database.GetAccessLogs(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve access logs"})
		return
	}

	c.JSON(http.StatusOK, logs)
}

// GetMemberAccessLogs handles GET /access/logs/:member_id
func GetMemberAccessLogs(c *gin.Context) {
	memberID := c.Param("member_id")
	limit := 100

	if limitStr := c.Query("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	logs, err := database.GetAccessLogsByMember(memberID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve access logs"})
		return
	}

	c.JSON(http.StatusOK, logs)
}
