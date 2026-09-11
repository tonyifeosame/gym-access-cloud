package service

import (
	"errors"
	"fmt"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Keyset pagination at the service boundary.
//
// A list operation takes a PageRequest and answers a Page. The cursor inside
// is the signed, opaque form from models/cursor.go; this file is the seam that
// binds it to the tenant -- a cursor is decoded against the CALLER'S company id
// (from the TenantContext) and the query's filter fingerprint, and one that
// disagrees with either is refused as cursor_invalid. A cursor cannot move a
// caller into another tenant's data even if it was minted for that tenant.

const (
	// DefaultPageSize applies when the caller names no limit.
	DefaultPageSize = 50
	// MaxPageSize bounds what a caller may ask for. Beyond it the request is
	// REFUSED, not clamped (API_SPEC.md section 18): the console's clamping
	// rule exists for a search box, and an integration asking for 5,000 has a
	// bug that should be reported to it rather than quietly served 200.
	MaxPageSize = 200
)

// PageRequest is what a list operation is asked for.
type PageRequest struct {
	// Limit is the page size wanted; 0 means DefaultPageSize. Anything else
	// outside 1..MaxPageSize is refused as invalid_field on `limit`.
	Limit int
	// Cursor continues an earlier page; "" starts from the beginning.
	Cursor string
}

// Page is one page of results plus the cursor for the next, if there is one.
type Page[T any] struct {
	Items      []T
	NextCursor string
	// HasMore is redundant with NextCursor != "" and exists so a caller does
	// not have to know that.
	HasMore bool
}

// pageSize validates a requested limit. Zero is "not supplied".
func pageSize(limit int) (int, error) {
	switch {
	case limit == 0:
		return DefaultPageSize, nil
	case limit < 1 || limit > MaxPageSize:
		return 0, ErrInvalidField("limit",
			fmt.Sprintf("limit must be an integer between 1 and %d.", MaxPageSize))
	default:
		return limit, nil
	}
}

// decodeCursor binds a caller's cursor to the tenant and query it must match.
//
// maxAge zero disables expiry. The cursor's own tenant claim is checked by the
// signer against tc.CompanyID(); a mismatch is cursor_invalid, deliberately the
// same answer as a forged one.
func decodeCursor(signer *models.CursorSigner, tc *TenantContext, encoded, filter string,
	maxAge time.Duration) (*database.KeysetPosition, error) {

	if encoded == "" {
		return nil, nil
	}
	if signer == nil {
		return nil, ErrInternal(errors.New("list called without a cursor signer"))
	}
	c, err := signer.Decode(encoded, tc.CompanyID(), filter, maxAge)
	switch {
	case errors.Is(err, models.ErrCursorExpired):
		return nil, ErrCursorExpired()
	case err != nil:
		return nil, ErrCursorInvalid()
	}
	return &database.KeysetPosition{CreatedAt: c.SortKey, ID: c.ID}, nil
}

// encodeCursor mints the cursor for the row after `last`.
func encodeCursor(signer *models.CursorSigner, tc *TenantContext, filter string,
	last database.KeysetPosition) (string, error) {

	if signer == nil {
		return "", ErrInternal(errors.New("list called without a cursor signer"))
	}
	encoded, err := signer.Encode(models.Cursor{
		CompanyID: tc.CompanyID(),
		SortKey:   last.CreatedAt,
		ID:        last.ID,
		Filter:    filter,
		IssuedAt:  time.Now(),
	})
	if err != nil {
		return "", ErrInternal(err)
	}
	return encoded, nil
}
