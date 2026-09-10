package models

import (
	"net/http"
	"sort"
)

// The public API error contract.
//
// ---------------------------------------------------------------------------
// WHY THIS IS NOT THE SHAPE THE REST OF THE API USES
// ---------------------------------------------------------------------------
//
// Everything in /api/v1 answers `{"error": "<message>"}`, and API_SPEC.md says
// in bold that those strings are for humans and must not be parsed. That is a
// workable contract for a console written by the same people as the server and
// an unworkable one for a third party: an integrator has to branch on
// something, and if the only stable thing is the status code then every 400
// looks alike.
//
// So the public API answers with a TYPE, a CODE, a message and the request id:
//
//	{"error": {"type": "invalid_request_error",
//	           "code": "member_id_unusable",
//	           "message": "…",
//	           "param": "member_id",
//	           "request_id": "7bd8490b23dbdbcc"}}
//
// `type` is a coarse class a client may branch on generically. `code` is the
// stable identifier and is part of the contract -- it may be added to, never
// renamed or repurposed. `message` is for a human and is explicitly not stable.
//
// ---------------------------------------------------------------------------
// WHAT MUST NEVER REACH A PUBLIC MESSAGE
// ---------------------------------------------------------------------------
//
// A raw validator string. The existing API returns things like
// "Key: 'DeviceRegistrationRequest.SerialNumber' Error:Field validation for
// 'SerialNumber' failed on the 'required' tag", which names a Go struct and its
// field path. That is an internal detail on the way out of the building, and the
// public tree must translate rather than forward it -- which is why every code
// below carries its own message and none of them is built from an error string.
//
// NOTHING SERVES THESE YET. There is no public route in this build; the registry
// and the envelope exist so that the codes can be enumerated, tested and
// published before the first endpoint depends on them.

// APIErrorType is the coarse class.
type APIErrorType string

const (
	ErrorTypeInvalidRequest APIErrorType = "invalid_request_error"
	ErrorTypeAuthentication APIErrorType = "authentication_error"
	ErrorTypePermission     APIErrorType = "permission_error"
	ErrorTypeNotFound       APIErrorType = "not_found_error"
	ErrorTypeConflict       APIErrorType = "conflict_error"
	ErrorTypeGone           APIErrorType = "gone_error"
	ErrorTypeRateLimit      APIErrorType = "rate_limit_error"
	ErrorTypeAPI            APIErrorType = "api_error"
)

// The stable code set. Additive only: a code may be added, and may never be
// renamed, removed or given a different meaning, because an integrator's error
// handling branches on it.
const (
	// authentication_error -- 401
	CodeCredentialMissing          = "api_credential_missing"
	CodeCredentialInvalid          = "api_credential_invalid"
	CodeCredentialExpired          = "api_credential_expired"
	CodeCredentialRevoked          = "api_credential_revoked"
	CodeCredentialWrongEnvironment = "api_credential_wrong_environment"

	// permission_error -- 403
	CodeInsufficientScope = "insufficient_scope"
	CodeSiteNotPermitted  = "site_not_permitted"
	CodeCompanyInactive   = "company_inactive"

	// invalid_request_error -- 400
	CodeUnknownParameter  = "unknown_parameter"
	CodeUnknownField      = "unknown_field"
	CodeMissingField      = "missing_field"
	CodeInvalidField      = "invalid_field"
	CodeInvalidTimestamp  = "invalid_timestamp"
	CodeCursorInvalid     = "cursor_invalid"
	CodeSortNotSupported  = "sort_not_supported"
	CodeTenantIdentity    = "tenant_identity_not_permitted"
	CodeMemberIDUnusable  = "member_id_unusable"
	CodeIdempotencyKeyBad = "idempotency_key_invalid"
	CodeBodyTooLarge      = "body_too_large"

	// not_found_error -- 404
	CodeResourceNotFound = "resource_not_found"

	// conflict_error -- 409
	CodeMemberIDExists       = "member_id_already_exists"
	CodeIdempotencyKeyReuse  = "idempotency_key_reuse"
	CodeIdempotencyInProgres = "idempotency_in_progress"
	CodeRosterOverCapacity   = "roster_exceeds_terminal_capacity"
	CodeWebhookLimitReached  = "webhook_limit_reached"

	// gone_error -- 410
	CodeCursorExpired = "cursor_expired"

	// rate_limit_error -- 429
	CodeRateLimitExceeded = "rate_limit_exceeded"

	// api_error -- 500 / 503
	CodeInternalError      = "internal_error"
	CodeServiceUnavailable = "service_unavailable"
)

// APIErrorSpec is one registered code.
type APIErrorSpec struct {
	Code string
	Type APIErrorType
	// Status is the HTTP status this code is always served with. One code, one
	// status: a code that could arrive as either a 400 or a 409 is a code an
	// integrator cannot write a handler for.
	Status int
	// Message is the default human-readable text. A handler may replace it with
	// something more specific; it may never replace it with an internal error
	// string.
	Message string
}

// APIErrors is the registry. A code absent from this map does not exist, and
// NewAPIError refuses to build one -- so a typo in a code becomes a loud
// internal_error rather than a quiet contract change.
var APIErrors = map[string]APIErrorSpec{
	CodeCredentialMissing: {CodeCredentialMissing, ErrorTypeAuthentication, http.StatusUnauthorized,
		"No credential was presented. Send Authorization: Bearer <your key>."},
	CodeCredentialInvalid: {CodeCredentialInvalid, ErrorTypeAuthentication, http.StatusUnauthorized,
		"The credential presented is not valid."},
	CodeCredentialExpired: {CodeCredentialExpired, ErrorTypeAuthentication, http.StatusUnauthorized,
		"This credential has expired. Issue a new one from the console."},
	CodeCredentialRevoked: {CodeCredentialRevoked, ErrorTypeAuthentication, http.StatusUnauthorized,
		"This credential has been revoked."},
	CodeCredentialWrongEnvironment: {CodeCredentialWrongEnvironment, ErrorTypeAuthentication, http.StatusUnauthorized,
		"This credential belongs to a different environment."},

	CodeInsufficientScope: {CodeInsufficientScope, ErrorTypePermission, http.StatusForbidden,
		"This credential does not carry the scope this endpoint requires."},
	CodeSiteNotPermitted: {CodeSiteNotPermitted, ErrorTypePermission, http.StatusForbidden,
		"This credential is not scoped to that site."},
	CodeCompanyInactive: {CodeCompanyInactive, ErrorTypePermission, http.StatusForbidden,
		"This account is not active."},

	CodeUnknownParameter: {CodeUnknownParameter, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"An unrecognised query parameter was supplied."},
	CodeUnknownField: {CodeUnknownField, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"An unrecognised field was supplied in the request body."},
	CodeMissingField: {CodeMissingField, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"A required field is missing."},
	CodeInvalidField: {CodeInvalidField, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"A field was supplied with a value this endpoint cannot accept."},
	CodeInvalidTimestamp: {CodeInvalidTimestamp, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"A timestamp could not be read. Use RFC 3339, for example 2026-09-09T17:00:00Z."},
	CodeCursorInvalid: {CodeCursorInvalid, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"The pagination cursor is not valid for this request."},
	CodeSortNotSupported: {CodeSortNotSupported, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"That sort order is not supported for this resource."},
	CodeTenantIdentity: {CodeTenantIdentity, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"The account is determined by your credential and cannot be supplied in a request."},
	CodeMemberIDUnusable: {CodeMemberIDUnusable, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"That member id cannot be stored by a terminal."},
	CodeIdempotencyKeyBad: {CodeIdempotencyKeyBad, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"Idempotency-Key must be between 1 and 255 characters."},
	CodeBodyTooLarge: {CodeBodyTooLarge, ErrorTypeInvalidRequest, http.StatusBadRequest,
		"The request body is larger than this API accepts."},

	CodeResourceNotFound: {CodeResourceNotFound, ErrorTypeNotFound, http.StatusNotFound,
		"No such resource."},

	CodeMemberIDExists: {CodeMemberIDExists, ErrorTypeConflict, http.StatusConflict,
		"A member with that id already exists."},
	CodeIdempotencyKeyReuse: {CodeIdempotencyKeyReuse, ErrorTypeConflict, http.StatusConflict,
		"That Idempotency-Key was already used for a different request."},
	CodeIdempotencyInProgres: {CodeIdempotencyInProgres, ErrorTypeConflict, http.StatusConflict,
		"A request with that Idempotency-Key is still in progress."},
	CodeRosterOverCapacity: {CodeRosterOverCapacity, ErrorTypeConflict, http.StatusConflict,
		"A terminal cannot hold the people this change would give it."},
	CodeWebhookLimitReached: {CodeWebhookLimitReached, ErrorTypeConflict, http.StatusConflict,
		"This account already has the maximum number of webhook endpoints."},

	CodeCursorExpired: {CodeCursorExpired, ErrorTypeGone, http.StatusGone,
		"That cursor points past the data retained for this account."},

	CodeRateLimitExceeded: {CodeRateLimitExceeded, ErrorTypeRateLimit, http.StatusTooManyRequests,
		"Rate limit exceeded."},

	CodeInternalError: {CodeInternalError, ErrorTypeAPI, http.StatusInternalServerError,
		"Something went wrong on our side. The request id identifies this failure in our logs."},
	CodeServiceUnavailable: {CodeServiceUnavailable, ErrorTypeAPI, http.StatusServiceUnavailable,
		"A dependency is temporarily unavailable. Please retry."},
}

// APIErrorBody is the wire shape.
type APIErrorBody struct {
	Error APIErrorDetail `json:"error"`
}

// APIErrorDetail is the object inside it.
type APIErrorDetail struct {
	Type    APIErrorType `json:"type"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	// Param names the field or query parameter at fault, when there is one.
	Param string `json:"param,omitempty"`
	// RequestID is the same correlation id the X-Request-ID header carries. It
	// is repeated in the body because an integrator quoting a failure in a
	// support ticket should not have to know to look at a header.
	RequestID string `json:"request_id,omitempty"`
	// DocURL points at the published description of this code.
	DocURL string `json:"doc_url,omitempty"`
}

// APIErrorDocBase is the prefix for doc_url. Not yet a live site; the codes are
// stable regardless, and a dead link is better than a code nobody can look up.
const APIErrorDocBase = "https://docs.accesslink.store/errors/"

// NewAPIError builds a response body for a registered code.
//
// AN UNREGISTERED CODE IS AN INTERNAL ERROR, not a passthrough. A typo in a code
// name would otherwise publish a contract nobody agreed to, and it would do so
// silently -- the caller would receive a code that is not in the documentation
// and would have no way to tell that from a code that is.
//
// The returned status is the registry's, so a handler cannot serve a code with a
// status the documentation does not describe.
func NewAPIError(code, requestID string) (int, APIErrorBody) {
	spec, known := APIErrors[code]
	if !known {
		spec = APIErrors[CodeInternalError]
	}
	return spec.Status, APIErrorBody{Error: APIErrorDetail{
		Type:      spec.Type,
		Code:      spec.Code,
		Message:   spec.Message,
		RequestID: requestID,
		DocURL:    APIErrorDocBase + spec.Code,
	}}
}

// NewAPIErrorWithMessage is NewAPIError with a more specific human message.
//
// The code, the type and the status still come from the registry. Only the
// sentence changes, and it must be written for the integrator -- never built
// from an internal error string.
func NewAPIErrorWithMessage(code, message, param, requestID string) (int, APIErrorBody) {
	status, body := NewAPIError(code, requestID)
	if message != "" {
		body.Error.Message = message
	}
	body.Error.Param = param
	return status, body
}

// KnownAPIErrorCode reports whether a code is registered.
func KnownAPIErrorCode(code string) bool {
	_, ok := APIErrors[code]
	return ok
}

// AllAPIErrorCodes returns every registered code, sorted.
//
// This is the list the OpenAPI ErrorCode enum is generated from and the list the
// parity test compares against, so it has to be stable in order as well as in
// content.
func AllAPIErrorCodes() []string {
	out := make([]string, 0, len(APIErrors))
	for code := range APIErrors {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}
