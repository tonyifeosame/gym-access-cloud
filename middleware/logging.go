package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Request logging.
//
// Two things were missing before and both matter once a fleet is in the field.
//
// The first is correlation. A terminal that cannot apply a job retries it, and
// the interesting question is always "is this the same device failing the same
// job, or many devices failing many jobs". Without an id shared between the
// request line and the error line, those two situations look identical in the
// log.
//
// The second is identity. `POST /api/v1/devices/heartbeat 200` is true of every
// terminal in the fleet, so on its own it cannot tell an operator which one is
// misbehaving. The device serial and site are attached to every line where the
// request authenticated.
//
// What is deliberately never logged: the device key, the site API key, request
// bodies, and fingerprint templates. Credentials must not end up in a log
// aggregator, and templates are biometric data.

// RequestIDHeader carries the correlation id in and out of the service.
const RequestIDHeader = "X-Request-ID"

// contextRequestID is the key the request id is stored under
const contextRequestID = "request_id"

// RequestIDMiddleware attaches a correlation id to every request.
//
// An inbound id is honoured so a proxy or the device itself can tie a retry to
// the attempt it is retrying; otherwise one is generated. The id is echoed in
// the response header so a device can report the exact request that failed.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}

		c.Set(contextRequestID, id)
		c.Writer.Header().Set(RequestIDHeader, id)
		c.Next()
	}
}

// newRequestID returns a short random correlation id. Uniqueness only has to
// hold across the retention window of a log, so 64 bits is plenty.
func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		// A correlation id is not worth failing a request over.
		return "unknown"
	}
	return hex.EncodeToString(raw)
}

// LoggingMiddleware logs one line per request once it has been served.
//
// Health checks are skipped: an orchestrator probes them every few seconds and
// the noise buries everything else. /metrics is skipped for the same reason.
func LoggingMiddleware() gin.HandlerFunc {
	skip := map[string]bool{
		"/health":             true,
		"/health/live":        true,
		"/health/ready":       true,
		"/health/maintenance": true,
		"/metrics":            true,
	}

	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		if skip[path] {
			return
		}

		status := c.Writer.Status()
		latency := time.Since(start)

		// Identity is only present once a request has authenticated, so an
		// unauthenticated 401 simply logs without it.
		who := ""
		if serial := c.GetString("device_serial"); serial != "" {
			who = " device=" + serial
		}
		if siteID := c.GetInt64("site_id"); siteID != 0 {
			who += " site=" + itoa(siteID)
		}

		log.Printf("request_id=%s %s %s status=%d latency=%s ip=%s%s",
			RequestID(c), c.Request.Method, path, status,
			latency.Round(time.Millisecond), c.ClientIP(), who)
	}
}

// RequestID returns the correlation id for a request, or "-" if none was set.
func RequestID(c *gin.Context) string {
	if id := c.GetString(contextRequestID); id != "" {
		return id
	}
	return "-"
}

// itoa avoids pulling strconv in for one call site
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
