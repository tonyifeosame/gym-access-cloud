package handlers

import (
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Change Wi-Fi, /api/v1/console/terminals/:serial/wifi-recovery.
//
// WHAT THIS ENDPOINT IS FOR. A customer changes their Wi-Fi password, or the
// router is replaced, and every terminal in the building goes dark. There is no
// keypad and no screen to type a new password into, so before firmware 15caf88
// the only way back was a laptop and a serial cable. This is the remote half of
// that fix: it hands ONE terminal back to the same setup portal a brand-new unit
// uses, and somebody standing next to it connects a phone and joins the new
// network.
//
// NO CREDENTIAL CROSSES THIS BOUNDARY, IN EITHER DIRECTION. The request body is
// empty by design -- there is no SSID field and no passphrase field, here or in
// the job, or in the database. The platform does not learn the customer's Wi-Fi
// password and cannot leak one it never had. That is a property of the shape of
// this endpoint rather than of a redaction step somebody has to remember.
//
// AND IT DESTROYS NOTHING ELSE. The firmware's recovery path clears the SSID and
// the pre-shared key and holds no reference to anything else -- the device
// credential, the terminal's identity and serial, its company, its site, its
// name, the server URL, the offline policy, the member table and every
// fingerprint binding are all in stores that function cannot reach. Nothing on
// the cloud side deletes anything at all: this handler inserts one row.
//
// ROLE. ADMIN, so OWNER and ADMIN may send it and MANAGER may not. It sits in
// the same route group as revoke, retire and move rather than beside resync,
// and the reason is what happens if it is sent to the wrong terminal: that door
// stops working until somebody physically visits it with a phone. A resync is
// invisible to everybody; this is a site visit.
//
// AUTHORIZATION IS UPSTREAM AND IS NOT RE-DERIVED HERE, exactly as in
// console_terminals.go. RequireTerminalGrant resolves the serial inside the
// caller's company and applies the grant rule to the site the terminal stands
// at, so another tenant's terminal is a 404 and an ungranted site is a 403. The
// store repeats the company join anyway.

// ConsoleRequestWifiRecovery handles POST /console/terminals/:serial/wifi-recovery
//
// IDEMPOTENT. Pressing the button twice, or a browser retrying a request whose
// response it never saw, returns the command already waiting rather than
// queueing a second one -- see database/wifi_recovery.go for why a duplicate is
// not merely wasteful but destructive.
//
// 202 rather than 200, and the distinction is the whole point of the screen this
// serves: the command has been ACCEPTED FOR DELIVERY. Nothing has happened at
// the terminal yet, and the console must not say otherwise until the device
// acknowledges the job.
func ConsoleRequestWifiRecovery(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	result, err := database.RequestWifiRecovery(companyID, serial)
	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case respondIfTerminalUnreachable(c, err):
		return
	case err != nil:
		logError(c, "console request wifi recovery", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to queue the Wi-Fi change"})
		return
	}

	// THE RECORD IS OF THE REQUEST, NOT OF THE OUTCOME, and it says so by
	// carrying the state the command was in when it was written. Whether the
	// terminal ever collects it is in sync_jobs; who asked for it is here.
	//
	// `already_queued` is recorded rather than suppressed: an operator pressing
	// the button three times is a fact worth having when somebody later asks why
	// a door was in setup mode. Nothing else is recorded, because nothing else
	// exists -- there is no reason field, no network name and no password on
	// this path to redact.
	recordAudit(c, auditTerminalWifiRecovery, auditTargetTerminal, "", serial, gin.H{
		"request_id":     result.RequestID,
		"state":          result.State,
		"already_queued": result.AlreadyQueued,
	})

	c.JSON(http.StatusAccepted, result)
}

// ConsoleWifiRecoveryStatus handles GET /console/terminals/:serial/wifi-recovery
//
// What the console polls while it says "Waiting for terminal…". A PURE READ:
// it queues nothing, cancels nothing and writes nothing, so an operator leaving
// the dialog open cannot change what the terminal will be sent.
//
// The state it reports is the honest one. ACCEPTED means the DEVICE
// acknowledged the job; nothing weaker is ever reported as done.
func ConsoleWifiRecoveryStatus(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	result, err := database.WifiRecoveryStatus(companyID, serial)
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console wifi recovery status", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to read the Wi-Fi change status"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// respondIfTerminalUnreachable answers a command aimed at a terminal that cannot
// collect it, and reports whether it did.
//
// 409 rather than 400 or 404: the request is well formed and the terminal is
// real and the caller is entitled to it -- the CONFLICT is with the terminal's
// current state, which is exactly what 409 means and is the shape
// respondIfOverCapacity already established.
//
// `code` is the stable half of the answer. The message beside it is for humans
// and will change; the console branches on the code to decide which recovery to
// explain, and for TERMINAL_OFFLINE that recovery is the terminal's own local
// procedure rather than anything this console can do.
func respondIfTerminalUnreachable(c *gin.Context, err error) bool {
	var unreachable *database.TerminalUnreachableError
	if !errors.As(err, &unreachable) {
		return false
	}

	message := terminalUnreachableMessage(unreachable.Code)
	if unreachable.Detail != "" {
		// The stable half is the code; this appends the part that narrows WHY,
		// for the operator rather than for the client's branching.
		message = message + " " + unreachable.Detail
	}

	c.JSON(http.StatusConflict, gin.H{
		"error":           message,
		"code":            unreachable.Code,
		"serial_number":   unreachable.Serial,
		"terminal_status": unreachable.Status,
	})
	return true
}

func terminalUnreachableMessage(code string) string {
	switch code {
	case models.WifiRecoveryTerminalDisabled:
		return "This terminal is disabled, so it will not collect commands. Re-enable it first."
	case models.WifiRecoveryTerminalNoCredential:
		return "This terminal holds no credential, so it cannot collect commands. Provision it first."
	case models.WifiRecoveryTerminalIncapable:
		// NOTHING WAS QUEUED, and that is the point of this refusal rather than
		// an aside. Serving the command anyway would have produced an
		// acknowledgement from a build that did not understand it, a console
		// reporting the terminal had confirmed the request, and a customer
		// waiting at a door for a setup network that was never going to appear.
		return "This terminal has not reported that it can change Wi-Fi remotely, so nothing was sent."
	default:
		return "This terminal is offline and cannot be sent a command."
	}
}
