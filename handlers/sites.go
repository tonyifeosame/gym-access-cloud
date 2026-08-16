package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// GetSiteSettings handles GET /sites/settings
//
// Returns the settings for the site the API key authenticated.
func GetSiteSettings(c *gin.Context) {
	settings, err := database.GetSiteSettings(c.GetInt64("site_id"))
	if err != nil {
		logError(c, "get site settings", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateSiteSettings handles PUT /sites/settings
//
// Replaces the site's settings and queues a SETTINGS sync job for every device
// at that site. The write and the jobs commit together.
func UpdateSiteSettings(c *gin.Context) {
	var settings json.RawMessage
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The column is constrained to a JSON object; reject anything else here so
	// the caller gets a clear message rather than a constraint violation.
	var probe map[string]any
	if err := json.Unmarshal(settings, &probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Settings must be a JSON object"})
		return
	}

	updated, err := database.UpdateSiteSettings(c.GetInt64("site_id"), settings)
	// F3. A caller's mistake, and one with a specific fix, so the message is
	// passed through rather than replaced -- it names the endpoint that does
	// what they were trying to do.
	if errors.Is(err, models.ErrReservedSettingsKey) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
			"code":  "RESERVED_SETTINGS_KEY",
		})
		return
	}
	if err != nil {
		logError(c, "update site settings", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
		return
	}

	// AUDITED ONLY ON THE CONSOLE PATH. This handler is mounted twice -- once
	// behind an operator session and once behind the site key -- and the audit
	// trail is a record of what OPERATORS did. A row with no actor, written
	// whenever a terminal's provisioning key pushed a settings change, would be
	// noise in the one table that has to stay readable.
	//
	// The settings body is not recorded. It is an open JSON object this layer
	// does not know the shape of, and GP-04 is the finding that says so.
	if middleware.Operator(c) != nil {
		recordAudit(c, auditSettingsSet, auditTargetSite, "", c.GetString("site_name"), nil)
	}

	c.JSON(http.StatusOK, updated)
}
