package handlers

import (
	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
	"access-terminal-cloud-api/service"
)

// The one place a service failure becomes a public API response.
//
// A handler on the public tree (none exists yet) calls a service method and,
// on error, calls RespondServiceError and returns. It does not inspect the
// error, choose a status, or write a message: the code on the service.Error
// selects all three from the registry in models/api_errors.go, and anything
// that is NOT a service.Error -- a raw database error that escaped, a panic
// recovered upstream -- is served as internal_error with its text kept for the
// log line and off the wire.
//
// The request id is repeated in the body so an integrator quoting a failure in
// a support ticket does not have to know to look at a header.

// RespondServiceError writes the response for a failed service call.
//
// Every 5xx is logged against the request id with the wrapped cause; 4xx
// answers are the caller's own doing and are not logged here, because the
// access log already records the status.
func RespondServiceError(c *gin.Context, operation string, err error) {
	requestID := middleware.RequestID(c)

	svcErr, ok := service.As(err)
	if !ok {
		logError(c, operation, err)
		status, body := models.NewAPIError(models.CodeInternalError, requestID)
		c.AbortWithStatusJSON(status, body)
		return
	}

	status, body := models.NewAPIErrorWithMessage(
		svcErr.Code(), svcErr.Message(), svcErr.Param(), requestID)
	if status >= 500 {
		logError(c, operation, svcErr)
	}
	if status == 503 {
		c.Header("Retry-After", "5")
	}
	c.AbortWithStatusJSON(status, body)
}
