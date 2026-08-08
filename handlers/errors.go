package handlers

import (
	"log"

	"access-terminal-cloud-api/middleware"

	"github.com/gin-gonic/gin"
)

// Server-side error reporting.
//
// Every handler answers a failure with a fixed, generic message -- "Failed to
// retrieve members" and so on. That is the right thing to send: the caller is a
// terminal or a dashboard, and a database error string tells it nothing it can
// act on while potentially disclosing schema details.
//
// The problem was that the underlying error was then dropped entirely, so a 500
// left no trace anywhere. An operator seeing a device stuck in a retry loop had
// the status code and nothing else. logError keeps the response generic and puts
// the cause in the log, tagged with the same request id the request line
// carries, so the two can be joined.
func logError(c *gin.Context, operation string, err error) {
	if err == nil {
		return
	}

	who := ""
	if serial := c.GetString("device_serial"); serial != "" {
		who = " device=" + serial
	}

	log.Printf("request_id=%s error op=%q%s: %v",
		middleware.RequestID(c), operation, who, err)
}
