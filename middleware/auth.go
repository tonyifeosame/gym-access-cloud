package middleware

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates API keys from the X-API-Key header
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "API key required"})
			c.Abort()
			return
		}

		// A database failure must not be reported as a bad key. Telling a caller
		// its credential is invalid during an outage sends operators looking for
		// a credential problem that does not exist, and can prompt a device to
		// discard a key that was fine.
		site, err := database.GetSiteByAPIKey(apiKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("request_id=%s error op=\"authenticate site\": %v", RequestID(c), err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
			c.Abort()
			return
		}
		if site == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			c.Abort()
			return
		}

		if !site.Active {
			c.JSON(http.StatusForbidden, gin.H{"error": "Site is inactive"})
			c.Abort()
			return
		}

		// Store site information in context. company_id scopes every query the
		// handlers make, so a key issued to one tenant cannot reach another's data.
		c.Set("site_name", site.SiteName)
		c.Set("site_id", site.ID)
		c.Set("company_id", site.CompanyID)
		c.Next()
	}
}

// corsHeaders is the set a browser client needs to talk to this API
const corsHeaders = "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, " +
	"Authorization, accept, origin, Cache-Control, X-Requested-With, X-API-Key, " +
	"X-Device-Key, X-Device-Serial, X-Protocol-Version, X-Request-ID"

// CORSMiddleware handles CORS headers.
//
// The previous version sent `Allow-Origin: *` together with
// `Allow-Credentials: true`. That pairing is contradictory -- browsers reject it
// outright, so it never actually enabled credentialed requests -- and it
// advertises the API as open to every origin on the web.
//
// CORS_ALLOWED_ORIGINS holds a comma-separated allowlist. When it is set, only a
// listed origin is echoed back and credentials are permitted, which is what an
// operator dashboard on a known host needs. When it is unset the API stays
// readable from any origin but never with credentials, which is the safe
// interpretation of the old behaviour.
//
// None of this affects the terminals: CORS is enforced by browsers, and an ESP32
// sends no Origin header.
func CORSMiddleware() gin.HandlerFunc {
	allowed := parseAllowedOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		switch {
		case len(allowed) == 0:
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && allowed[origin]:
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			// Caches must not serve one origin's response to another.
			c.Writer.Header().Add("Vary", "Origin")
		default:
			c.Writer.Header().Add("Vary", "Origin")
		}

		c.Writer.Header().Set("Access-Control-Allow-Headers", corsHeaders)
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// parseAllowedOrigins builds the origin allowlist from its configured form
func parseAllowedOrigins(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	allowed := map[string]bool{}
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = true
		}
	}
	return allowed
}

// LoggingMiddleware lives in logging.go, alongside request id correlation.
