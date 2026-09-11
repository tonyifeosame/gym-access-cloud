package service

import (
	"errors"
	"fmt"

	"access-terminal-cloud-api/models"
)

// The error a service returns.
//
// ---------------------------------------------------------------------------
// ONE TYPE, CARRYING A REGISTERED CODE
// ---------------------------------------------------------------------------
//
// A handler on the public tree does one thing with a failure: it maps it to the
// status, type and code the registry in models/api_errors.go publishes. So a
// service failure IS a registry code plus, where it helps, the parameter at
// fault and a sentence for the integrator. The HTTP status is not chosen here
// and cannot be: it is the registry's, looked up from the code, which is what
// keeps "one code, one status" true across every route.
//
// A CODE THAT IS NOT REGISTERED CANNOT BE CONSTRUCTED. newError panics on one,
// which turns a typo into a failing test rather than a published contract
// nobody agreed to -- the same posture models.NewAPIError takes at the edge.
//
// THE CAUSE IS KEPT BUT NEVER SHOWN. An internal failure wraps the underlying
// error so a handler can log it against the request id; the message an
// integrator sees is the registry's, never the wrapped text.
type Error struct {
	code    string
	param   string
	message string
	cause   error
}

// Code is the registered code.
func (e *Error) Code() string { return e.code }

// Param names the field or query parameter at fault, or "".
func (e *Error) Param() string { return e.param }

// Message is the sentence for the integrator, or "" for the registry default.
func (e *Error) Message() string { return e.message }

// Status is the HTTP status the registry binds to this code.
func (e *Error) Status() int { return models.APIErrors[e.code].Status }

// Error renders the failure for logs. Not for a response body.
func (e *Error) Error() string {
	switch {
	case e.cause != nil:
		return e.code + ": " + e.cause.Error()
	case e.message != "":
		return e.code + ": " + e.message
	default:
		return e.code
	}
}

// Unwrap exposes the cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Is lets errors.Is compare two service errors by code alone, so a test can
// write errors.Is(err, service.ErrNotFound()) without caring about the message.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.code == e.code
}

func newError(code, param, message string, cause error) *Error {
	if !models.KnownAPIErrorCode(code) {
		panic(fmt.Sprintf("service: %q is not a registered API error code", code))
	}
	return &Error{code: code, param: param, message: message, cause: cause}
}

// As extracts a service error from a chain, or reports that there is none.
func As(err error) (*Error, bool) {
	var s *Error
	if errors.As(err, &s) {
		return s, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Constructors, one per situation a service can be in
// ---------------------------------------------------------------------------

// ErrNotFound is the answer for a resource that does not exist IN THIS TENANT.
//
// A resource that exists in another company is exactly as absent as one that
// never existed: the same code, the same status, the same message. Answering
// anything else would confirm an identifier to whoever guessed it.
func ErrNotFound() *Error { return newError(models.CodeResourceNotFound, "", "", nil) }

// ErrInsufficientScope names the scope the operation needed.
func ErrInsufficientScope(scope string) *Error {
	return newError(models.CodeInsufficientScope, "",
		"This credential does not carry the "+scope+" scope this operation requires.", nil)
}

// ErrSiteNotPermitted is for a site in the tenant that the credential's site
// restriction excludes. Distinct from not-found on purpose: the site is the
// caller's own, and telling them their key is scoped away from it is the
// helpful answer.
func ErrSiteNotPermitted() *Error { return newError(models.CodeSiteNotPermitted, "", "", nil) }

// ErrTenantIdentity refuses a request that tried to name its own tenant.
func ErrTenantIdentity(param string) *Error {
	return newError(models.CodeTenantIdentity, param, "", nil)
}

// ErrUnknownParameter refuses a query parameter this version does not define.
// Refused rather than ignored: a misspelt parameter that is silently dropped
// looks like an answer to the question that was asked.
func ErrUnknownParameter(param string) *Error {
	return newError(models.CodeUnknownParameter, param,
		"The query parameter "+param+" is not recognised.", nil)
}

// ErrUnknownField refuses a body field this version does not define.
func ErrUnknownField(param string) *Error {
	return newError(models.CodeUnknownField, param,
		"The field "+param+" is not recognised.", nil)
}

// ErrMissingField reports a required field that was not supplied.
func ErrMissingField(param string) *Error {
	return newError(models.CodeMissingField, param, "The "+param+" field is required.", nil)
}

// ErrInvalidField reports a field whose value the operation cannot accept.
func ErrInvalidField(param, message string) *Error {
	return newError(models.CodeInvalidField, param, message, nil)
}

// ErrMemberIDUnusable is FW-09 at the service boundary: an id no terminal can
// store is refused before it reaches the database.
func ErrMemberIDUnusable() *Error {
	return newError(models.CodeMemberIDUnusable, "member_id", "", nil)
}

// ErrMemberIDExists is the unique-violation answer for a duplicate member id.
func ErrMemberIDExists() *Error {
	return newError(models.CodeMemberIDExists, "member_id", "", nil)
}

// ErrRosterOverCapacity carries the terminal detail the console shape already
// reports, in a sentence rather than as extra fields.
func ErrRosterOverCapacity(serial string, rosterSize, capacity int) *Error {
	return newError(models.CodeRosterOverCapacity, "",
		fmt.Sprintf("Terminal %s would hold %d people but has capacity for %d.",
			serial, rosterSize, capacity), nil)
}

// ErrInvalidTimestamp is a query parameter or field that should be RFC 3339
// and is not.
func ErrInvalidTimestamp(param string) *Error {
	return newError(models.CodeInvalidTimestamp, param, "", nil)
}

// ErrCursorInvalid covers a cursor that is malformed, signed by another key,
// issued for another tenant or for a different query.
func ErrCursorInvalid() *Error { return newError(models.CodeCursorInvalid, "cursor", "", nil) }

// ErrCursorExpired is the one cursor failure with its own status (410).
func ErrCursorExpired() *Error { return newError(models.CodeCursorExpired, "cursor", "", nil) }

// Authentication outcomes.
func ErrCredentialMissing() *Error { return newError(models.CodeCredentialMissing, "", "", nil) }
func ErrCredentialInvalid() *Error { return newError(models.CodeCredentialInvalid, "", "", nil) }
func ErrCredentialWrongEnvironment() *Error {
	return newError(models.CodeCredentialWrongEnvironment, "", "", nil)
}

// ErrInternal wraps a failure the caller cannot act on. The cause is for the
// log line; the response carries the registry's sentence and the request id.
func ErrInternal(cause error) *Error {
	return newError(models.CodeInternalError, "", "", cause)
}

// ErrUnavailable is a dependency (the database, in practice) being down.
func ErrUnavailable(cause error) *Error {
	return newError(models.CodeServiceUnavailable, "", "", cause)
}
