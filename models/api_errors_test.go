package models

import (
	"net/http"
	"strings"
	"testing"
)

// The public error registry.
//
// The codes are a published contract, so what these tests protect is that the
// contract cannot drift by accident: that a code always arrives with the same
// status and type, that an unregistered code cannot be served, and that no
// message leaks something internal.

func TestEveryRegisteredErrorIsWellFormed(t *testing.T) {
	validTypes := map[APIErrorType]bool{
		ErrorTypeInvalidRequest: true,
		ErrorTypeAuthentication: true,
		ErrorTypePermission:     true,
		ErrorTypeNotFound:       true,
		ErrorTypeConflict:       true,
		ErrorTypeGone:           true,
		ErrorTypeRateLimit:      true,
		ErrorTypeAPI:            true,
	}

	for code, spec := range APIErrors {
		if spec.Code != code {
			t.Errorf("error %q is registered under key %q", spec.Code, code)
		}
		if !validTypes[spec.Type] {
			t.Errorf("code %q has unknown type %q", code, spec.Type)
		}
		if spec.Status < 400 || spec.Status > 599 {
			t.Errorf("code %q has status %d, which is not an error status", code, spec.Status)
		}
		if spec.Message == "" {
			t.Errorf("code %q has no message", code)
		}

		// snake_case, because the code appears in an integrator's switch
		// statement and a mixed convention is a source of typos forever.
		if code != strings.ToLower(code) || strings.ContainsAny(code, " -") {
			t.Errorf("code %q is not lower snake_case", code)
		}
	}
}

// One code, one status. A code that could arrive as either a 400 or a 409 is a
// code an integrator cannot write a handler for.
func TestErrorTypeAndStatusAgree(t *testing.T) {
	expected := map[APIErrorType]int{
		ErrorTypeAuthentication: http.StatusUnauthorized,
		ErrorTypePermission:     http.StatusForbidden,
		ErrorTypeInvalidRequest: http.StatusBadRequest,
		ErrorTypeNotFound:       http.StatusNotFound,
		ErrorTypeConflict:       http.StatusConflict,
		ErrorTypeGone:           http.StatusGone,
		ErrorTypeRateLimit:      http.StatusTooManyRequests,
	}

	for code, spec := range APIErrors {
		want, checked := expected[spec.Type]
		if !checked {
			continue // api_error covers both 500 and 503 deliberately
		}
		if spec.Status != want {
			t.Errorf("code %q is type %q but status %d, want %d",
				code, spec.Type, spec.Status, want)
		}
	}
}

// An unregistered code must not be servable. A typo would otherwise publish a
// code that is not in the documentation, and the caller would have no way to
// tell that from one that is.
func TestAnUnknownCodeBecomesAnInternalError(t *testing.T) {
	status, body := NewAPIError("not_a_real_code", "req-1")

	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if body.Error.Code != CodeInternalError {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeInternalError)
	}
	if body.Error.Type != ErrorTypeAPI {
		t.Errorf("type = %q, want %q", body.Error.Type, ErrorTypeAPI)
	}
}

func TestKnownAPIErrorCode(t *testing.T) {
	if !KnownAPIErrorCode(CodeRateLimitExceeded) {
		t.Error("a registered code was reported as unknown")
	}
	if KnownAPIErrorCode("invented") {
		t.Error("an unregistered code was reported as known")
	}
}

func TestErrorBodyCarriesTheRequestID(t *testing.T) {
	_, body := NewAPIError(CodeResourceNotFound, "7bd8490b23dbdbcc")

	if body.Error.RequestID != "7bd8490b23dbdbcc" {
		t.Errorf("request_id = %q", body.Error.RequestID)
	}
	// Repeated in the body precisely so an integrator quoting a failure does not
	// have to know to look at a header.
	if !strings.HasPrefix(body.Error.DocURL, APIErrorDocBase) {
		t.Errorf("doc_url = %q, want a link under %q", body.Error.DocURL, APIErrorDocBase)
	}
}

// A handler may make the sentence more specific. It may not change the code, the
// type or the status -- those are the contract.
func TestASpecificMessageDoesNotChangeTheContract(t *testing.T) {
	status, body := NewAPIErrorWithMessage(CodeInvalidField,
		"valid_from must be earlier than valid_until", "valid_from", "req-2")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if body.Error.Code != CodeInvalidField {
		t.Errorf("code = %q, want %q", body.Error.Code, CodeInvalidField)
	}
	if body.Error.Param != "valid_from" {
		t.Errorf("param = %q", body.Error.Param)
	}
	if !strings.Contains(body.Error.Message, "valid_from must be earlier") {
		t.Errorf("message was not replaced: %q", body.Error.Message)
	}
}

// No registered message may carry an internal detail. The existing API returns
// raw validator strings naming Go structs and field paths; the public tree
// translates rather than forwarding, and this is the check that it stays that
// way as codes are added.
func TestNoRegisteredMessageLeaksInternals(t *testing.T) {
	forbidden := []string{
		"Key: '",           // the go-playground validator's own prefix
		"Field validation", // ditto
		"sql:",             // database/sql
		"pq:",              // lib/pq
		"goroutine",
		"database/",
		"models.",
		"handlers.",
	}

	for code, spec := range APIErrors {
		for _, needle := range forbidden {
			if strings.Contains(spec.Message, needle) {
				t.Errorf("message for %q contains %q, which is an internal detail",
					code, needle)
			}
		}
	}
}

func TestAllAPIErrorCodesIsSortedAndComplete(t *testing.T) {
	codes := AllAPIErrorCodes()

	if len(codes) != len(APIErrors) {
		t.Fatalf("AllAPIErrorCodes returned %d of %d codes", len(codes), len(APIErrors))
	}
	for i := 1; i < len(codes); i++ {
		if codes[i-1] >= codes[i] {
			t.Fatalf("codes are not sorted: %v", codes)
		}
	}
	// Sorted order is what the OpenAPI enum will be generated from, so it has to
	// be stable across runs rather than map order.
	for _, code := range codes {
		if !KnownAPIErrorCode(code) {
			t.Errorf("AllAPIErrorCodes returned %q, which is not registered", code)
		}
	}
}
