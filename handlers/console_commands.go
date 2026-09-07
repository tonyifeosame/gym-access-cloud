package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The console's half of the remote command plane (028).
//
// FOUR ROUTES AND ONE SHAPE. Issue, read one, list, withdraw -- plus a
// capabilities read that tells the app which controls to draw. All of them
// return models.ConsoleCommand or a list of it, so the "just issued" screen and
// the "still waiting" screen cannot disagree about what they are showing.
//
// TENANCY IS NOT WRITTEN HERE. Every route goes through RequireTerminalGrant,
// which resolves the serial inside the caller's company and applies the
// operator's site grant -- another tenant is a 404 and an ungranted site is a
// 403. The store then resolves the serial again through resolveTerminal, which
// is the single place that join is written. A handler that resolved a device id
// by hand would be the tenancy rule written a third time.
//
// ROLE IS CHECKED TWICE, AND THE SECOND CHECK IS THE REAL ONE. The router
// mounts these behind the lowest role any command needs, because a route can
// only carry one gate; the per-command MinRole is enforced below against the
// registry. Without that, mounting DIAGNOSTIC_SNAPSHOT at MANAGER would have
// silently let a manager issue every command that ever joins the registry.

// ConsoleIssueCommand handles POST /console/terminals/:serial/commands
func ConsoleIssueCommand(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	serial := c.Param("serial")

	var req models.CommandIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	spec, known := models.CommandSpecFor(req.Type)
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Unknown command type",
			"code":  models.CommandRefusedUnknownType,
			// The list is served rather than merely refused: an integrator who
			// mistyped a command should not have to read the source to find the
			// spelling, and the list is already public through /capabilities.
			"supported_types": models.IssuableCommandTypes(),
		})
		return
	}
	if !spec.Issuable {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "This command has its own endpoint and is not issued here",
			"code":  models.CommandRefusedNotIssuable,
		})
		return
	}

	// THE PER-COMMAND ROLE GATE.
	//
	// Enforced here rather than at the router because the router's gate is per
	// ROUTE and this is per COMMAND TYPE. The two are only the same while every
	// registered command needs the same role, which is true today and is
	// exactly the kind of thing that stops being true quietly -- the plan puts
	// UNLOCK at ADMIN with a step-up and FACTORY_RESET at OWNER, on this route.
	identity := middleware.Operator(c)
	if identity == nil {
		// Unreachable behind OperatorAuthMiddleware, and checked anyway: the
		// alternative is a nil dereference deciding whether a command is
		// authorized, which fails open on the one path that must not.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	if !middleware.RoleAtLeast(identity.Role, spec.MinRole) {
		c.JSON(http.StatusForbidden, gin.H{
			"error":     "Your role cannot issue this command",
			"required":  spec.MinRole,
			"command":   spec.Type,
			"read_only": spec.ReadOnly,
		})
		return
	}

	params := req.Params
	if spec.ValidateParams != nil {
		validated, err := spec.ValidateParams(req.Params)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
				"code":  models.CommandRefusedBadParams,
			})
			return
		}
		params = validated
	} else if len(req.Params) > 0 {
		// REFUSED RATHER THAN DROPPED. A parameter the platform silently
		// ignores is one an operator believes took effect, and for a command
		// that is the difference between "I asked for the short pulse" and a
		// door that used the default.
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "This command takes no parameters",
			"code":  models.CommandRefusedBadParams,
		})
		return
	}

	if len(req.Reason) > models.MaxCommandReasonLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "reason is too long",
			"code":  models.CommandRefusedBadParams,
		})
		return
	}

	// A malformed retry token is a 400 rather than a constraint violation the
	// caller cannot tell from an outage. Validated here because this is where a
	// well-formed refusal can still be written.
	if req.IdempotencyKey != "" && !isUUID(req.IdempotencyKey) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "idempotency_key must be a UUID",
			"code":  models.CommandRefusedBadParams,
		})
		return
	}

	result, err := database.IssueCommand(database.CommandIssueInput{
		CompanyID:      companyID,
		Serial:         serial,
		Type:           spec.Type,
		Params:         params,
		Reason:         req.Reason,
		OperatorID:     identity.UserID,
		OperatorEmail:  identity.Email,
		IdempotencyKey: req.IdempotencyKey,
	})

	var refused *database.CommandRefusedError
	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.As(err, &refused):
		// 409 RATHER THAN 400 WHEN THE SLOT IS TAKEN. The request is
		// well-formed -- there is nothing for the caller to correct -- and what
		// refused it is the terminal's current state, which is what a conflict
		// means. The console needs the distinction: a 400 is a bug in the
		// integration to be fixed, a 409 is a command to withdraw first.
		status := http.StatusBadRequest
		if refused.Code == models.CommandRefusedAlreadyPending {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{
			"error": refused.Detail,
			"code":  refused.Code,
		})
		return
	case respondIfTerminalUnreachable(c, err):
		// NOTHING WAS QUEUED. The refusal codes are 024's, and the console
		// branches on them to choose which recovery it explains.
		return
	case err != nil:
		logError(c, "issue terminal command", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue the command"})
		return
	}

	// THE RECORD IS OF THE REQUEST, NOT OF THE OUTCOME, and it says so by
	// carrying the state the command was in when it was written. Whether the
	// terminal ever collects it is in sync_jobs; who asked for it is here.
	//
	// `already_pending` is recorded rather than suppressed, on exactly the
	// reasoning ConsoleRequestWifiRecovery records `already_queued`: an operator
	// pressing a button three times is a fact worth having when somebody later
	// asks why a door did something.
	//
	// The parameters are recorded in full. Nothing on this path is secret --
	// the registry's validators accept a closed set of section names and one
	// test target, and there is deliberately nowhere in either shape to put a
	// credential.
	recordAudit(c, auditTerminalCommandIssued, auditTargetTerminal, result.ID, serial, gin.H{
		"command_id":      result.ID,
		"type":            result.Type,
		"state":           result.State,
		"params":          result.Params,
		"reason":          req.Reason,
		"already_pending": result.AlreadyPending,
	})

	// 202 rather than 201. The row exists, but the thing the operator asked for
	// has not happened and may never happen -- the terminal has to collect it
	// first. A 201 would say the command was carried out.
	c.JSON(http.StatusAccepted, result)
}

// ConsoleGetCommand handles GET /console/terminals/:serial/commands/:id
//
// A PURE READ. It queues nothing, cancels nothing and retires nothing, so an
// operator leaving a dialog open cannot change what the terminal will be sent.
// A lapsed command reads as EXPIRED here and is retired by the next issue of
// its type, which is the only path that needs the index slot back.
func ConsoleGetCommand(c *gin.Context) {
	result, err := database.CommandStatus(
		c.GetInt64("company_id"), c.Param("serial"), c.Param("id"))

	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, models.ErrCommandNotFound):
		// Scoped to this terminal, so a public id belonging to another door is
		// not found rather than forbidden -- the answer cannot confirm that it
		// exists somewhere else.
		c.JSON(http.StatusNotFound, gin.H{"error": "Command not found for this terminal"})
		return
	case err != nil:
		logError(c, "read terminal command", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the command"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// ConsoleListCommands handles GET /console/terminals/:serial/commands
//
// `status=open` narrows to commands that can still change, which is what a
// console polling a dialog asks for. Unfiltered is the audit-shaped question --
// what has been asked of this door -- which is what somebody investigating a
// complaint reads.
func ConsoleListCommands(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	result, err := database.ListCommands(
		c.GetInt64("company_id"), c.Param("serial"),
		c.Query("status") == "open", limit)

	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case err != nil:
		logError(c, "list terminal commands", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list commands"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// ConsoleWithdrawCommand handles DELETE /console/terminals/:serial/commands/:id
//
// WITHDRAWS ONLY WHAT HAS NOT BEEN COLLECTED. A delivered command cannot be
// recalled, and this refuses rather than pretending: the terminal already has
// it and will act on it or not, and reporting "cancelled" for something a door
// is executing would be exactly the lie the DELIVERED state exists to prevent.
func ConsoleWithdrawCommand(c *gin.Context) {
	serial := c.Param("serial")
	commandID := c.Param("id")

	result, err := database.WithdrawCommand(c.GetInt64("company_id"), serial, commandID)

	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, models.ErrCommandNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Command not found for this terminal"})
		return
	case errors.Is(err, models.ErrCommandNotWithdrawable):
		c.JSON(http.StatusConflict, gin.H{
			"error": "This command has already been collected by the terminal " +
				"or has already finished, so it cannot be withdrawn.",
			"code": "COMMAND_NOT_WITHDRAWABLE",
		})
		return
	case err != nil:
		logError(c, "withdraw terminal command", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to withdraw the command"})
		return
	}

	recordAudit(c, auditTerminalCommandWithdrawn, auditTargetTerminal, commandID, serial, gin.H{
		"command_id": commandID,
		"type":       result.Type,
	})

	c.JSON(http.StatusOK, result)
}

// ConsoleTerminalCapabilities handles GET /console/terminals/:serial/capabilities
//
// WHAT THE APP DRAWS ITS CONTROLS FROM. A console that rendered a button for
// every command this server knows would offer work the terminal will refuse,
// and the operator would learn that from a 409 after pressing it. The answer
// here is the intersection: what the registry can issue, marked with whether
// this particular terminal reported it can do it.
//
// VIEWER, unlike the write routes. Which controls exist is not a privileged
// fact -- it describes the hardware, not the roster -- and a viewer who can see
// a terminal should be able to see why a control is greyed out.
func ConsoleTerminalCapabilities(c *gin.Context) {
	result, err := database.TerminalCapabilities(c.GetInt64("company_id"), c.Param("serial"))

	switch {
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case err != nil:
		logError(c, "read terminal capabilities", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read capabilities"})
		return
	}

	c.JSON(http.StatusOK, result)
}

// isUUID reports whether a string is a canonical 8-4-4-4-12 UUID.
//
// Hand-rolled rather than pulling in a parser: the only question asked is
// whether Postgres will accept it as a uuid, and the shape is the whole of
// that. Case-insensitive, because Postgres accepts either.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') ||
				(r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
