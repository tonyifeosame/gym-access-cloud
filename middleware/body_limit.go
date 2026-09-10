package middleware

import (
	"net/http"

	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Request body limits for the public API.
//
// ---------------------------------------------------------------------------
// WHY THIS IS NOT GLOBAL
// ---------------------------------------------------------------------------
//
// Exactly one path in this API bounds its body today: the announce limiter wraps
// the request in a MaxBytesReader at eight kilobytes, because it reads the body
// itself and an unauthenticated endpoint must not let a caller decide how much
// memory that costs. Everything else is unbounded.
//
// Applying a limit to the whole engine would be the obvious move and is the
// wrong one here. The device sync path uploads sealed biometric material, and
// the ceiling for that is a property of the sensor and the firmware rather than
// a number this file should be choosing on their behalf. Changing what a
// deployed fleet may upload is a device-protocol change, and P1 does not make
// one.
//
// So the limit is mounted on the PUBLIC tree, where the contract is being
// written now and can simply say what it is.
//
// NOT MOUNTED YET. There is no public tree in this build.

// MaxPublicBodyBytes is the ceiling for a public API request.
//
// 256 KiB. A member is a few hundred bytes; the largest plausible public write
// is a batch this API does not offer. Generous by two orders of magnitude and
// still finite, which is the only property that matters -- an unbounded body is
// a caller choosing how much of a single instance's memory to occupy.
const MaxPublicBodyBytes = 256 << 10

// BodyLimitMiddleware refuses a request body over the limit.
//
// http.MaxBytesReader rather than a Content-Length check: a chunked request
// carries no length, and a caller that lies about the one it does carry would
// otherwise be limited by a number it chose. The reader stops at the ceiling
// whatever the header says.
//
// THE REFUSAL COMES FROM THE HANDLER'S BIND, not from here. MaxBytesReader
// surfaces the overrun when the body is actually read, so this middleware sets
// the bound and the bind reports it -- which is why the error mapping below
// exists rather than a check-then-reject in this function.
func BodyLimitMiddleware(limit int64) gin.HandlerFunc {
	if limit <= 0 {
		limit = MaxPublicBodyBytes
	}

	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}

// BodyTooLarge answers a request whose body exceeded the limit.
//
// Exported so a public handler can call it when its bind fails with a
// MaxBytesReader error, and so the answer is the same sentence everywhere rather
// than each handler inventing one.
func BodyTooLarge(c *gin.Context) {
	status, body := models.NewAPIError(models.CodeBodyTooLarge, RequestID(c))
	c.JSON(status, body)
	c.Abort()
}
