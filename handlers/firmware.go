package handlers

import (
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Firmware catalogue reads and site-scoped fleet inventory, on the SITE KEY.
//
// Inventory and reporting only. Marking a build current changes what "outdated"
// means; it does not push anything to any device. OTA is not implemented.
//
// EVERYTHING HERE IS NOW SCOPED TO THE AUTHENTICATED SITE, and the writes have
// moved to an operator session. See the note at the bottom of this file.

// ListSiteDevices handles GET /devices, on the site-key credential.
//
// NARROWED TO THE AUTHENTICATED SITE. This previously reported the whole
// company on the reasoning that "the holder of a site key is trusted with the
// company's inventory by construction". That reasoning does not survive
// contact with what a site key physically is: a secret installed on hardware
// bolted to a wall at one location, handled by whoever installs terminals.
//
// A key at the smallest site enumerating every terminal at every other one is
// disclosure with no corresponding need -- a terminal does not read this, and an
// operator wanting a company-wide view has /console/terminals, which has an
// identity and a grant model behind it.
//
// The scope is taken from the authenticated context, never from a parameter, so
// there is no argument through which a caller could widen it.
func ListSiteDevices(c *gin.Context) {
	outdatedOnly := c.Query("outdated") == "true"

	siteScope := []int64{c.GetInt64("site_id")}

	devices, err := database.ListDevices(c.GetInt64("company_id"), outdatedOnly, siteScope)
	if err != nil {
		logError(c, "list devices", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve devices"})
		return
	}
	if devices == nil {
		devices = []models.DeviceInventory{}
	}

	c.JSON(http.StatusOK, gin.H{"count": len(devices), "devices": devices})
}

// GetSiteFleetSummary handles GET /devices/summary, on the site-key credential.
//
// Narrowed to the authenticated site for the same reason the list is. A rollup
// an operator cannot drill into, describing hardware at locations this
// credential has nothing to do with, is not a summary of anything actionable --
// it is a count of somebody else's estate.
func GetSiteFleetSummary(c *gin.Context) {
	siteScope := []int64{c.GetInt64("site_id")}

	summary, err := database.GetFleetSummary(c.GetInt64("company_id"), siteScope)
	if err != nil {
		logError(c, "fleet summary", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve fleet summary"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ListFirmwareVersions handles GET /firmware
func ListFirmwareVersions(c *gin.Context) {
	versions, err := database.ListFirmwareVersions(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "list firmware versions", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve firmware versions"})
		return
	}
	if versions == nil {
		versions = []models.FirmwareVersion{}
	}
	c.JSON(http.StatusOK, gin.H{"count": len(versions), "firmware_versions": versions})
}

// The firmware WRITES used to live here, on the site-key credential, and have
// moved to /api/v1/console/firmware under an ADMIN operator session --
// handlers/console_firmware.go.
//
// Kept as a note rather than deleted silently: `POST /firmware` and
// `PUT /firmware/{id}/current` were reachable with any site provisioning key,
// so a secret installed on hardware at one location could add a build to the
// company catalogue and move the `is_current` target. That target is what every
// "is this terminal outdated" report is measured against, and once OTA exists it
// is the row a terminal would be pointed at.
//
// Anything still calling the old paths receives 404 and should move to the
// console routes. The read above is unchanged and stays on this credential,
// because a terminal checking what it should be running is a legitimate use of
// a device-adjacent secret.
