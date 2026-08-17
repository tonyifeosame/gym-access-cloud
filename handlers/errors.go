package handlers

import (
	"errors"
	"log"
	"net/http"

	"access-terminal-cloud-api/database"
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

// respondIfOverCapacity answers a roster-capacity refusal and reports whether it
// did (FW-01).
//
// 409 CONFLICT, not 500 and not 400. Nothing failed and the request was not
// malformed: the platform is refusing to promise something it cannot deliver,
// because the terminal named cannot hold the people its permissions cover.
//
// THE NUMBERS ARE IN THE RESPONSE. "Over capacity" tells an operator to raise a
// support ticket; "holds 256, needs 312" tells them what to do. The error
// message from the database layer already reads as a sentence, so it is passed
// through rather than replaced with a generic one -- this is one of the few
// failures where the internal detail IS the actionable part and discloses
// nothing a caller with a terminal grant does not already know.
func respondIfOverCapacity(c *gin.Context, err error) bool {
	var overflow *database.RosterCapacityError
	if !errors.As(err, &overflow) {
		return false
	}

	c.JSON(http.StatusConflict, gin.H{
		"error":         overflow.Error(),
		"code":          "ROSTER_EXCEEDS_TERMINAL_CAPACITY",
		"serial_number": overflow.Serial,
		"roster_size":   overflow.RosterSize,
		"capacity":      overflow.Capacity,
	})
	return true
}
