package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The firmware catalogue, /api/v1/console/firmware.
//
// MOVED HERE FROM THE SITE-KEY TREE, and that move is the fix rather than a
// tidy-up. `POST /api/v1/firmware` and `PUT /api/v1/firmware/{id}/current` were
// authenticated by the site provisioning key -- a secret that lives on hardware
// bolted to a wall. Anyone holding one could add a build to the company's
// catalogue and move `is_current`, which is:
//
//   * the target every "is this terminal outdated" report is measured against,
//     so a fleet could be made to look current when it was not, or the reverse;
//   * once OTA exists, the row a terminal would actually be pointed at, which
//     turns a leaked door credential into a firmware push.
//
// The read stays available on the site key, narrowed, because a terminal
// checking what it should be running is a legitimate use of a device-adjacent
// credential. The writes need an operator.
//
// These handlers are thin. The store functions are unchanged and already
// company-scoped; what changed is who may reach them.

// ConsoleListFirmware handles GET /console/firmware
func ConsoleListFirmware(c *gin.Context) {
	versions, err := database.ListFirmwareVersions(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console list firmware", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve firmware versions"})
		return
	}
	if versions == nil {
		versions = []models.FirmwareVersion{}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(versions), "firmware_versions": versions})
}

// ConsoleCreateFirmware handles POST /console/firmware
//
// Adds a build to the catalogue. It does NOT become the deployment target until
// it is explicitly marked current, which is a separate call and a separate audit
// record -- publishing a build and deciding a fleet should run it are different
// decisions and should not be one request.
func ConsoleCreateFirmware(c *gin.Context) {
	var req models.CreateFirmwareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	version, err := database.CreateFirmwareVersion(c.GetInt64("company_id"), req)
	if database.IsUniqueViolation(err) {
		c.JSON(http.StatusConflict, gin.H{"error": "That version already exists for this device type"})
		return
	}
	if err != nil {
		logError(c, "console create firmware", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create the firmware version"})
		return
	}

	recordAudit(c, auditFirmwarePublished, auditTargetFirmware, version.PublicID,
		version.Version, gin.H{
			"device_type":     version.DeviceType,
			"release_channel": version.ReleaseChannel,
			"is_mandatory":    version.IsMandatory,
		})

	c.JSON(http.StatusCreated, version)
}

// ConsoleSetCurrentFirmware handles PUT /console/firmware/:id/current
//
// Moves the deployment target for a device type and release channel. Nothing is
// pushed -- there is no OTA -- but this is the value the fleet is measured
// against, and once OTA exists it is the value it will be pointed at. Audited
// for that reason.
func ConsoleSetCurrentFirmware(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid firmware id"})
		return
	}

	// A firmware id belonging to another company is reported as not found rather
	// than forbidden, so the endpoint does not confirm that the id exists.
	version, err := database.SetCurrentFirmware(c.GetInt64("company_id"), id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Firmware version not found"})
		return
	}
	if err != nil {
		logError(c, "console set current firmware", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set the current firmware"})
		return
	}

	recordAudit(c, auditFirmwareTargetSet, auditTargetFirmware, version.PublicID,
		version.Version, gin.H{
			"device_type":     version.DeviceType,
			"release_channel": version.ReleaseChannel,
		})

	c.JSON(http.StatusOK, version)
}
