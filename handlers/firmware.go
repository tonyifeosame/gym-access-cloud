package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Firmware catalog and fleet inventory endpoints.
//
// Inventory and reporting only. Marking a build current changes what "outdated"
// means; it does not push anything to any device. OTA is not implemented.

// ListDevices handles GET /devices
//
// The fleet inventory. `?outdated=true` narrows it to devices not running the
// current build for their release channel.
func ListDevices(c *gin.Context) {
	outdatedOnly := c.Query("outdated") == "true"

	devices, err := database.ListDevices(c.GetInt64("company_id"), outdatedOnly)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve devices"})
		return
	}
	if devices == nil {
		devices = []models.DeviceInventory{}
	}

	c.JSON(http.StatusOK, gin.H{"count": len(devices), "devices": devices})
}

// GetFleetSummary handles GET /devices/summary
func GetFleetSummary(c *gin.Context) {
	summary, err := database.GetFleetSummary(c.GetInt64("company_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve fleet summary"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ListFirmwareVersions handles GET /firmware
func ListFirmwareVersions(c *gin.Context) {
	versions, err := database.ListFirmwareVersions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve firmware versions"})
		return
	}
	if versions == nil {
		versions = []models.FirmwareVersion{}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(versions), "firmware_versions": versions})
}

// CreateFirmwareVersion handles POST /firmware
//
// Adds a build to the catalog. It does not become the deployment target until
// it is explicitly marked current.
func CreateFirmwareVersion(c *gin.Context) {
	var req models.CreateFirmwareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	version, err := database.CreateFirmwareVersion(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create firmware version"})
		return
	}
	c.JSON(http.StatusCreated, version)
}

// SetCurrentFirmware handles PUT /firmware/:id/current
//
// Makes a build the deployment target for its device type and release channel.
// Devices running anything else are then reported as outdated. Nothing is
// pushed -- this only moves the target.
func SetCurrentFirmware(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid firmware id"})
		return
	}

	version, err := database.SetCurrentFirmware(id)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "Firmware version not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set current firmware"})
		return
	}

	c.JSON(http.StatusOK, version)
}
