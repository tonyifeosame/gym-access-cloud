package middleware

import (
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// DeviceAuthMiddleware authenticates a terminal.
//
// Two credentials are accepted while the fleet migrates:
//
//	X-Device-Key                    per-device credential (preferred)
//	X-API-Key + X-Device-Serial     site key plus serial (deprecated)
//
// The legacy path exists only so firmware already deployed against the Sprint 4
// protocol keeps working through the upgrade. It is weaker by construction: the
// site key is shared by every terminal at the site, so it cannot distinguish one
// device from another beyond the serial the caller claims. Remove it once the
// fleet is on per-device credentials.
func DeviceAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if deviceKey := c.GetHeader("X-Device-Key"); deviceKey != "" {
			identity, err := database.AuthenticateDevice(deviceKey)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to authenticate device"})
				c.Abort()
				return
			}
			if identity == nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid device key"})
				c.Abort()
				return
			}
			if !identity.Active || identity.Status == models.DeviceDisabled {
				c.JSON(http.StatusForbidden, gin.H{"error": "Device is inactive"})
				c.Abort()
				return
			}

			c.Set("device_id", identity.ID)
			c.Set("device_serial", identity.SerialNumber)
			c.Set("site_id", identity.SiteID)
			c.Set("company_id", identity.CompanyID)
			c.Set("device_auth", "device_key")
			c.Next()
			return
		}

		// Deprecated fallback: site key plus a claimed serial.
		apiKey := c.GetHeader("X-API-Key")
		serial := c.GetHeader("X-Device-Serial")
		if apiKey == "" || serial == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "X-Device-Key required (or X-API-Key with X-Device-Serial)",
			})
			c.Abort()
			return
		}

		site, err := database.GetSiteByAPIKey(apiKey)
		if err != nil || site == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			c.Abort()
			return
		}

		device, err := database.GetDeviceBySerial(site.ID, serial)
		if err != nil || device == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Device not registered for this site"})
			c.Abort()
			return
		}
		if !device.Active || device.Status == models.DeviceDisabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "Device is inactive"})
			c.Abort()
			return
		}

		c.Set("device_id", device.ID)
		c.Set("device_serial", device.SerialNumber)
		c.Set("site_id", site.ID)
		c.Set("company_id", site.CompanyID)
		c.Set("device_auth", "site_key_legacy")
		c.Next()
	}
}
