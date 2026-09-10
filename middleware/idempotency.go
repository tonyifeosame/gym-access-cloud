package middleware

import (
	"bytes"
	"io"
	"log"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Idempotency-Key handling for the public API.
//
// ---------------------------------------------------------------------------
// NOT MOUNTED ON ANYTHING
// ---------------------------------------------------------------------------
//
// This middleware is complete and tested and is registered on no route, because
// the routes it is for -- the public write endpoints -- do not exist in this
// build. It reads two context keys that only the public authentication
// middleware will set, so mounting it on a console route today would be a no-op
// rather than a hazard; it is still not mounted, because a primitive that is
// quietly live somewhere nobody expects is worse than one that is obviously
// dormant.
//
// ---------------------------------------------------------------------------
// WHAT IT DOES
// ---------------------------------------------------------------------------
//
//	GET/HEAD/OPTIONS   passes straight through. A safe method has nothing to
//	                   replay, and demanding a key on one would be noise.
//	no key             passes through. The key is required by the ROUTE, not by
//	                   this middleware -- a handler that must have one says so,
//	                   which keeps "required on POST, optional on PATCH" a
//	                   property of the endpoint rather than a global rule that
//	                   would have to be excepted.
//	key present        claim, then either short-circuit or capture the response.
//
// THE RESPONSE IS CAPTURED, NOT RE-RENDERED. What is stored is exactly the bytes
// the handler produced, so a replay is indistinguishable from the original --
// including a field the handler computed from something that has since changed.
// Re-running the handler to produce a "fresh" answer would be a different
// response to the same request, which is the thing idempotency exists to
// prevent.

// Context keys the public authentication middleware will set. Named here so the
// dependency is one-directional: this file knows what it needs, and the
// middleware that will provide it does not have to know about this one.
const (
	ContextAPICredentialID = "api_credential_id"
	ContextAPICompanyID    = "company_id"
)

// IdempotencyHeader carries the key.
const IdempotencyHeader = "Idempotency-Key"

// IdempotentReplayHeader marks a response that was replayed rather than
// produced. Not part of the decision -- a client must behave the same either way
// -- but it turns "did my retry do anything" from a guess into an observation.
const IdempotentReplayHeader = "Idempotent-Replay"

// maxIdempotentBodyBytes bounds what will be buffered to fingerprint a request.
//
// The global body limit is smaller than this, so in practice this is a second
// belt: it exists so that this middleware's own buffering cannot be the thing
// that makes a request unbounded if the body limit is ever mounted after it.
const maxIdempotentBodyBytes = 1 << 20

// IdempotencyMiddleware claims a key, replays a completed response, or captures
// a new one.
func IdempotencyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}

		key := c.GetHeader(IdempotencyHeader)
		if key == "" {
			c.Next()
			return
		}

		if len(key) > database.MaxIdempotencyKeyLength {
			writeAPIError(c, models.CodeIdempotencyKeyBad)
			return
		}

		companyID := c.GetInt64(ContextAPICompanyID)
		credentialID := c.GetInt64(ContextAPICredentialID)
		if companyID == 0 || credentialID == 0 {
			// Reached only if this is mounted without the public authentication
			// middleware in front of it. There is no tenant to scope the record
			// to, and a record scoped to nothing would be reachable by everyone.
			c.Next()
			return
		}

		body, err := readAndRestoreBody(c)
		if err != nil {
			writeAPIError(c, models.CodeBodyTooLarge)
			return
		}

		fingerprint := database.FingerprintRequest(c.Request.Method, c.Request.URL.Path, body)

		result, err := database.BeginIdempotent(c.Request.Context(),
			companyID, credentialID, key, fingerprint, database.DefaultIdempotencyTTL)
		if err != nil {
			log.Printf("request_id=%s error op=\"idempotency claim\": %v", RequestID(c), err)
			writeAPIError(c, models.CodeServiceUnavailable)
			return
		}

		switch result.Outcome {
		case database.IdempotencyReplay:
			c.Header(IdempotentReplayHeader, "true")
			c.Data(result.ResponseStatus, "application/json; charset=utf-8", result.ResponseBody)
			c.Abort()
			return
		case database.IdempotencyReuse:
			writeAPIError(c, models.CodeIdempotencyKeyReuse)
			return
		case database.IdempotencyInProgress:
			writeAPIError(c, models.CodeIdempotencyInProgres)
			return
		}

		// Claimed. Capture whatever the handler writes so it can be replayed.
		capture := &capturingWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = capture

		c.Next()

		status := capture.Status()
		if !capture.wrote {
			// The handler produced nothing at all. Releasing the claim is the
			// only safe move: storing an empty response would replay silence
			// forever, and leaving it IN_PROGRESS would tell every retry that a
			// request which is not running still is.
			if err := database.ReleaseIdempotent(c.Request.Context(),
				companyID, credentialID, key); err != nil {
				log.Printf("request_id=%s error op=\"idempotency release\": %v",
					RequestID(c), err)
			}
			return
		}

		if err := database.CompleteIdempotent(c.Request.Context(),
			companyID, credentialID, key, status, capture.body.Bytes()); err != nil {
			// Logged, not fatal. The response has already been sent; failing now
			// would turn a successful mutation into an error the caller would
			// retry, which is precisely the outcome this middleware exists to
			// avoid.
			log.Printf("request_id=%s error op=\"idempotency complete\": %v",
				RequestID(c), err)
		}
	}
}

// readAndRestoreBody buffers the body for fingerprinting and puts it back.
//
// The handler downstream still has to be able to read it, so the buffer is
// restored as a fresh reader rather than the body being consumed.
func readAndRestoreBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}

	limited := io.LimitReader(c.Request.Body, maxIdempotentBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxIdempotentBodyBytes {
		return nil, io.ErrShortBuffer
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	// Gin's own cache, so a handler using ShouldBindBodyWith downstream reads
	// the same bytes rather than re-reading a reader this function replaced.
	c.Set(gin.BodyBytesKey, body)
	return body, nil
}

// capturingWriter records what a handler wrote while still writing it through.
//
// The response goes to the client as it always would; the copy is what gets
// stored. Buffering and replaying afterwards would delay every write endpoint by
// the length of its own response for the benefit of the retry that usually does
// not happen.
type capturingWriter struct {
	gin.ResponseWriter
	body  *bytes.Buffer
	wrote bool
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.wrote = true
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *capturingWriter) WriteString(s string) (int, error) {
	w.wrote = true
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func (w *capturingWriter) WriteHeader(status int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

// writeAPIError answers with the public error envelope.
//
// Lives here rather than in handlers because middleware cannot import handlers,
// and both need to produce the same shape. The registry it draws from is in
// models, which both may import.
func writeAPIError(c *gin.Context, code string) {
	status, body := models.NewAPIError(code, RequestID(c))
	if code == models.CodeServiceUnavailable {
		c.Header("Retry-After", "5")
	}
	c.JSON(status, body)
	c.Abort()
}
