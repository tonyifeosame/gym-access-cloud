package middleware

import (
	"net/http"

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

		// Validate API key against database
		site, err := database.GetSiteByAPIKey(apiKey)
		if err != nil || site == nil {
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

// CORSMiddleware handles CORS headers
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-API-Key")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// LoggingMiddleware logs request information
func LoggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip logging for health checks
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}

		c.Next()

		// Log: method path status client_ip
		// In production, use a proper logging library
		_ = c.Writer.Status()
	}
}
