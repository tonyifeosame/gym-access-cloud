package handlers

import (
	"encoding/json"
	"net/http"

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
)

// GetSiteSettings handles GET /sites/settings
//
// Returns the settings for the site the API key authenticated.
func GetSiteSettings(c *gin.Context) {
	settings, err := database.GetSiteSettings(c.GetInt64("site_id"))
	if err != nil {
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
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
		return
	}

	c.JSON(http.StatusOK, updated)
}
