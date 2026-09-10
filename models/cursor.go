package models

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Keyset pagination cursors.
//
// ---------------------------------------------------------------------------
// WHY NOT OFFSET
// ---------------------------------------------------------------------------
//
// Every existing list in this API is limit/offset, and API_SPEC.md already
// documents the consequence: with no stable final key, "the same terminal can
// appear on two consecutive pages while another is skipped". For a console that
// is a cosmetic annoyance somebody notices and re-reads. For an integrator
// paging a ten-thousand-member roster into their own database it is silent
// double-processing and silent omission, discovered weeks later as a data
// discrepancy nobody can reproduce.
//
// So the public API pages by KEYSET: the cursor carries the sort value and the
// row id of the last row returned, and the next page asks for rows after that
// pair. Exact, and stable under concurrent writes.
//
// ---------------------------------------------------------------------------
// WHY IT IS SIGNED
// ---------------------------------------------------------------------------
//
// The tenant filter is applied to every query regardless, so a forged cursor
// cannot reach another company's rows -- the signature is not the isolation
// boundary and must never be described as one. What it buys is that a class of
// "what happens if I edit this" probing becomes a clean, cheap refusal instead
// of a query with attacker-chosen bounds, and that a cursor minted for one set
// of filters cannot be replayed against another, where it would silently mean
// something different.
//
// THE KEY IS AN ARGUMENT, NOT AN ENVIRONMENT READ. A signer is constructed with
// its key and passed to whatever needs one. A package that read the environment
// itself would be a package a test cannot exercise twice with two keys, which is
// exactly what the tamper tests need to do.
//
// NOTHING SERVES A CURSOR YET. There is no public list endpoint in this build.

// Cursor is the decoded position.
type Cursor struct {
	// CompanyID binds the cursor to the tenant it was issued for. Checked on
	// decode: a cursor from another company is refused rather than merely
	// yielding nothing, so the failure is legible instead of looking like an
	// empty page.
	CompanyID int64 `json:"c"`

	// SortKey is the value of the ordering column on the last row of the
	// previous page -- occurred_at for events, created_at for members.
	SortKey time.Time `json:"k"`

	// ID is the row's public-facing sequence position, used to break ties when
	// two rows share a SortKey. Without it a page boundary that falls inside a
	// group of identical timestamps repeats or skips exactly the rows the
	// keyset was adopted to protect.
	ID int64 `json:"i"`

	// Filter fingerprints the query this cursor belongs to. A cursor carried
	// across a change of filters describes a position in a different result set;
	// refusing it is the difference between a client getting a confusing page
	// and a client getting an answer to a question it did not ask.
	Filter string `json:"f"`

	// IssuedAt supports expiry. A cursor older than the caller's retention
	// window points at rows that may have been purged.
	IssuedAt time.Time `json:"t"`
}

// Cursor failures. The handler maps every one of these except ErrCursorExpired
// to CodeCursorInvalid: a caller is not entitled to learn whether a cursor was
// forged, was issued to another company, or simply belongs to a different
// filter set, and none of those distinctions is actionable for them.
var (
	ErrCursorInvalid = errors.New("cursor is not valid")
	// ErrCursorWrongTenant is a cursor issued to a different company. Kept
	// separate so a test can prove the check exists; NEVER surfaced as a
	// distinct code, because it would confirm that the cursor was real.
	ErrCursorWrongTenant = errors.New("cursor was issued for another account")
	// ErrCursorFilterChanged is a cursor replayed against different filters.
	ErrCursorFilterChanged = errors.New("cursor belongs to a different query")
	ErrCursorExpired       = errors.New("cursor has expired")

	// ErrCursorKeyTooShort refuses a signing key that is not worth having.
	ErrCursorKeyTooShort = errors.New("a cursor signing key must be at least 32 bytes")
)

// CursorSigner mints and verifies cursors.
type CursorSigner struct {
	key []byte
}

// NewCursorSigner builds a signer over a key.
//
// THE KEY IS PASSED IN. Wiring it from configuration is the caller's job, which
// keeps this package testable with two keys in one process and keeps the
// decision about where the key lives -- and about requiring one once the public
// API is enabled -- at the edge where it belongs.
//
// Thirty-two bytes minimum. A short key here does not weaken confidentiality --
// a cursor holds no secret -- but it weakens the only thing the signature is
// for, which is making forgery not worth attempting.
func NewCursorSigner(key []byte) (*CursorSigner, error) {
	if len(key) < 32 {
		return nil, ErrCursorKeyTooShort
	}
	owned := make([]byte, len(key))
	copy(owned, key)
	return &CursorSigner{key: owned}, nil
}

// Encode renders a signed, opaque cursor.
//
// base64url without padding, so it survives a query string without escaping and
// without a client having to know that. The payload is JSON with one-character
// field names: it is opaque by contract rather than by obscurity, and there is
// no reason to spend bytes on a caller that must not read it.
func (s *CursorSigner) Encode(c Cursor) (string, error) {
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	c.SortKey = c.SortKey.UTC()
	c.IssuedAt = c.IssuedAt.UTC()

	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encoding cursor: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + s.sign(encoded), nil
}

// Decode verifies a cursor and returns its position.
//
// companyID and filter are what the CURRENT request resolves to, not what the
// caller claims. A cursor that disagrees with either is refused.
//
// maxAge of zero disables expiry, which is right for a resource with no
// retention window; a caller with one passes it and gets ErrCursorExpired rather
// than a silently empty page.
func (s *CursorSigner) Decode(encoded string, companyID int64, filter string,
	maxAge time.Duration) (Cursor, error) {

	body, signature, found := strings.Cut(encoded, ".")
	if !found || body == "" || signature == "" {
		return Cursor{}, ErrCursorInvalid
	}

	// CONSTANT TIME. The signature is the only thing standing between a caller
	// and a cursor of their own choosing, and a byte-by-byte compare with an
	// early exit leaks it one character at a time to anybody willing to measure
	// -- the same reasoning CSRFMatches already applies.
	if !hmac.Equal([]byte(s.sign(body)), []byte(signature)) {
		return Cursor{}, ErrCursorInvalid
	}

	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Cursor{}, ErrCursorInvalid
	}

	var c Cursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return Cursor{}, ErrCursorInvalid
	}

	// Checked AFTER the signature, so an unsigned probe cannot use the
	// difference between these errors to learn anything -- it never reaches
	// them.
	if c.CompanyID != companyID {
		return Cursor{}, ErrCursorWrongTenant
	}
	if c.Filter != filter {
		return Cursor{}, ErrCursorFilterChanged
	}
	if maxAge > 0 && time.Since(c.IssuedAt) > maxAge {
		return Cursor{}, ErrCursorExpired
	}

	return c, nil
}

func (s *CursorSigner) sign(body string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// FilterFingerprint reduces a query's filters to a stable short string.
//
// ORDER-INDEPENDENT, because ?active=true&site_id=x and ?site_id=x&active=true
// are the same query and a client that reorders its parameters between pages has
// not changed anything. Sorting before hashing is what makes that true.
//
// The fingerprint is not a secret and does not need to be: it exists so that a
// cursor cannot be silently reused across a change of filters, and it is carried
// inside a signed payload.
func FilterFingerprint(filters map[string]string) string {
	if len(filters) == 0 {
		return "-"
	}

	keys := make([]string, 0, len(filters))
	for k, v := range filters {
		// An empty value is an absent filter. Including it would make
		// ?active= and no parameter at all two different queries, which is a
		// distinction no caller intends.
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "-"
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(filters[k])
		b.WriteByte('\x1f')
	}

	sum := sha256.Sum256([]byte(b.String()))
	// Sixteen hex characters. This is a change detector, not a commitment: it
	// only has to make an accidental collision between two filter sets in one
	// request implausible.
	return hex.EncodeToString(sum[:])[:16]
}
