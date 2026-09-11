package service

import (
	"errors"
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
	// MaxPageSize bounds what a caller may ask for. Larger requests are
	// clamped, not refused: an integrator asking for 1,000 wants "a lot", and
	// the answer is a page and a cursor, not an error.
	MaxPageSize = 200
)

// PageRequest is what a list operation is asked for.
type PageRequest struct {
	// Limit is the page size wanted; 0 means DefaultPageSize.
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

// pageSize normalises and clamps a requested limit.
func pageSize(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
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
