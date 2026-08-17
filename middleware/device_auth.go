package middleware

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Device authentication (SEC-05).
//
// ---------------------------------------------------------------------------
// THE FINDING, AND WHY IT IS NOW CLOSABLE
// ---------------------------------------------------------------------------
//
// Two credentials used to be accepted, unconditionally:
//
//	X-Device-Key                    the terminal's own credential
//	X-API-Key + X-Device-Serial     the SITE key plus a serial the caller types
//
// The second is not a terminal authenticating. It is anybody holding the site's
// provisioning secret ASSERTING which terminal they are, and the server taking
// their word for it. The site key registers devices and rotates their
// credentials, so it is handled by installers and lives on laptops -- and with
// this path open, holding it was equivalent to holding every device key at that
// site. A terminal could be impersonated well enough to read its work list, its
// people, and to write door events in its name.
//
// It was kept because removing it would strand firmware that predated per-device
// keys. That argument has now expired on the firmware side: `POST /devices/claim`
// is shipped on BOTH halves (docs/firmware-protocol-requirements.md §7), so a
// terminal can obtain its own credential from a single-use, serial-bound code
// without the site key ever reaching it. Factory-reset units re-provision the
// same way, because registration is idempotent by serial.
//
// ---------------------------------------------------------------------------
// SO IT IS OFF, AND IT IS AN EXPLICIT OPT-IN TO TURN IT BACK ON
// ---------------------------------------------------------------------------
//
// Not deleted. An installation with genuinely old hardware still in a wall needs
// a way to keep those doors working while it is upgraded, and the alternative to
// a documented flag is somebody reverting this commit in a hurry.
//
// The default is CLOSED, which is the part that matters: a deployment that does
// nothing gets the safe behaviour, and the unsafe one has to be asked for by
// name and is announced at startup. That is the shape the audit's remediation
// column asks for -- "gate behind an explicit opt-in until then".
//
// WHAT DOES NOT CHANGE: revocation. Both paths refuse an inactive or DISABLED
// device, and RevokeTerminalCredential sets both, so a revoked terminal is
// refused however it presents itself. That is asserted by a test rather than
// left to be read off this comment.

// LegacyDeviceAuthEnv is the variable that re-opens the deprecated path.
const LegacyDeviceAuthEnv = "LEGACY_DEVICE_AUTH"

// LegacyDeviceAuthEnabled reports whether site-key-plus-serial authentication is
// accepted.
//
// Read on every call rather than cached, so a test can set it around a router it
// has already built and so an operator's answer to "is this on right now" comes
// from the process rather than from when it started. It is a string comparison
// on a request path that already makes two database round trips.
func LegacyDeviceAuthEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(LegacyDeviceAuthEnv))
	if raw == "" {
		return false
	}
	// Anything unparseable is treated as OFF. A typo in the one variable that
	// re-opens a known weakness must not be what enables it.
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

// DeviceAuthMiddleware authenticates a terminal.
//
// X-Device-Key is the credential. The site-key-plus-serial pair is accepted only
// when LEGACY_DEVICE_AUTH says so, and is refused with a message naming the
// header a terminal should be sending.
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

		apiKey := c.GetHeader("X-API-Key")
		serial := c.GetHeader("X-Device-Serial")

		// THE REFUSAL IS THE SAME whether the caller sent nothing at all or sent
		// a perfectly valid site key and serial. A terminal has one credential
		// and this is the message that says so, including where to get one.
		//
		// It is deliberately not a 403: the caller has not been forbidden a
		// resource, it has failed to present the credential this endpoint takes.
		if !LegacyDeviceAuthEnabled() {
			if apiKey != "" && serial != "" {
				// Logged, because this is a deployed terminal that has just
				// stopped working and the operator needs to know which one and
				// why. Silence here would look like a network fault.
				log.Printf("device auth refused: %s presented a site key instead of a "+
					"device key. Issue it a claim code (POST /console/sites/:id/claim-codes) "+
					"or set %s=1 to re-open the deprecated path while the fleet is upgraded.",
					serial, LegacyDeviceAuthEnv)
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "X-Device-Key required. A terminal authenticates with its own " +
					"credential, obtained by redeeming a claim code at POST /devices/claim.",
			})
			c.Abort()
			return
		}

		// ------------------------------------------------------------------
		// Deprecated path, explicitly enabled
		// ------------------------------------------------------------------
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

		// REVOCATION STILL APPLIES on this path. RevokeTerminalCredential clears
		// the key hash AND sets DISABLED/inactive; only the second half is
		// visible here, which is exactly why it sets both.
		if !device.Active || device.Status == models.DeviceDisabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "Device is inactive"})
			c.Abort()
			return
		}

		log.Printf("DEPRECATED device auth: %s authenticated with the site provisioning "+
			"key. This terminal has no credential of its own; claim it.", serial)

		c.Set("device_id", device.ID)
		c.Set("device_serial", device.SerialNumber)
		c.Set("site_id", site.ID)
		c.Set("company_id", site.CompanyID)
		c.Set("device_auth", "site_key_legacy")
		c.Next()
	}
}
