package models

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
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
// WHY IT IS ENCRYPTED AND AUTHENTICATED (v2), NOT MERELY SIGNED (v1)
// ---------------------------------------------------------------------------
//
// The tenant filter is applied to every query regardless, so a forged cursor
// cannot reach another company's rows -- the cursor is not the isolation
// boundary and must never be described as one. What authentication buys is
// that a class of "what happens if I edit this" probing becomes a clean, cheap
// refusal instead of a query with attacker-chosen bounds, and that a cursor
// minted for one set of filters cannot be replayed against another, where it
// would silently mean something different.
//
// v1 was an HMAC over a base64 JSON payload, which anyone could DECODE: the
// payload carried the internal company id and the internal id of the last row
// served, and API_SPEC.md section 18 promises the internal BIGSERIAL is never
// exposed under any name. v2 closes that: the position is ENCRYPTED with
// XChaCha20-Poly1305, and the tenant and the filter fingerprint are bound as
// ASSOCIATED DATA rather than carried in the payload at all. A cursor presented
// by another company, or against other filters, fails the authentication tag
// before a single field is read -- integrity is the Poly1305 tag, and the
// encryption is opacity on top of it, never a substitute for it.
//
// THE KEY IS AN ARGUMENT, NOT AN ENVIRONMENT READ. A signer is constructed with
// its key and passed to whatever needs one. A package that read the environment
// itself would be a package a test cannot exercise twice with two keys, which is
// exactly what the tamper tests need to do. The configured key is never used as
// the cipher key directly: it is stretched through HKDF-SHA256 with a
// version-specific label, so a future format can derive a different key from the
// same configuration.
//
// ROTATING THE KEY INVALIDATES EVERY OUTSTANDING CURSOR, by design and without a
// grace window: a cursor is short-lived listing state, and a client answers
// cursor_invalid by starting its listing again.

// cursorVersion is the leading byte of every v2 token. A token that does not
// start with it -- including every v1 token -- is refused before any
// cryptography runs.
const cursorVersion = 0x02

// cursorKeyInfo is the HKDF label. Changing the format changes the label, so a
// new format never shares a cipher key with an old one.
const cursorKeyInfo = "accesslink cursor v2 xchacha20poly1305"

// Cursor is the decoded position.
//
// CompanyID and Filter are NOT serialised into the token. On Encode they are
// bound as associated data; on Decode they are the caller's CURRENT values and
// are filled in from them, so a caller reading them back sees what it asked
// for, never what a token claimed.
type Cursor struct {
	// CompanyID binds the cursor to the tenant it was issued for. Bound as
	// associated data: a cursor from another company fails authentication
	// rather than merely yielding nothing, so the failure is legible instead
	// of looking like an empty page.
	CompanyID int64 `json:"-"`

	// SortKey is the value of the ordering column on the last row of the
	// previous page -- occurred_at for events, created_at for members.
	SortKey time.Time `json:"k"`

	// ID is the row's internal sequence position, used to break ties when two
	// rows share a SortKey. Without it a page boundary that falls inside a
	// group of identical timestamps repeats or skips exactly the rows the
	// keyset was adopted to protect. ENCRYPTED: it is the reason v2 exists.
	ID int64 `json:"i"`

	// Filter fingerprints the query this cursor belongs to. Bound as associated
	// data: a cursor carried across a change of filters describes a position in
	// a different result set, and refusing it is the difference between a
	// client getting a confusing page and a client getting an answer to a
	// question it did not ask.
	Filter string `json:"-"`

	// IssuedAt supports expiry. A cursor older than the caller's retention
	// window points at rows that may have been purged.
	IssuedAt time.Time `json:"t"`
}

// Cursor failures. The handler maps every one of these except ErrCursorExpired
// to CodeCursorInvalid: a caller is not entitled to learn whether a cursor was
// forged, was issued to another company, or simply belongs to a different
// filter set, and none of those distinctions is actionable for them.
//
// In v2 the tenant and filter mismatches are not even distinguishable
// INTERNALLY: both are an authentication-tag failure, exactly like a forgery.
// The sentinels are kept so that ErrCursorWrongTenant and
// ErrCursorFilterChanged still match errors.Is(err, ErrCursorInvalid) for any
// caller written against v1.
var (
	ErrCursorInvalid = errors.New("cursor is not valid")
	// ErrCursorWrongTenant is retained for callers; in v2 it is never returned
	// on its own -- see the note above.
	ErrCursorWrongTenant = fmt.Errorf("%w: issued for another account", ErrCursorInvalid)
	// ErrCursorFilterChanged is retained for callers; in v2 it is never returned
	// on its own.
	ErrCursorFilterChanged = fmt.Errorf("%w: belongs to a different query", ErrCursorInvalid)
	ErrCursorExpired       = errors.New("cursor has expired")
	// ErrCursorKeyTooShort refuses a signing key that is not worth having.
	ErrCursorKeyTooShort = errors.New("a cursor signing key must be at least 32 bytes")
)

// CursorSigner mints and verifies cursors. The name is kept from v1; what it
// holds now is an AEAD.
type CursorSigner struct {
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		NonceSize() int
		Overhead() int
	}
}

// NewCursorSigner builds a signer over a configured key.
//
// THE KEY IS PASSED IN. Wiring it from configuration is the caller's job, which
// keeps this package testable with two keys in one process and keeps the
// decision about where the key lives at the edge where it belongs.
//
// Thirty-two bytes minimum, then HKDF-SHA256 to a 32-byte cipher key. The
// minimum is what makes the derived key worth having: a short configured value
// would be stretched to the right length and still be guessable.
func NewCursorSigner(key []byte) (*CursorSigner, error) {
	if len(key) < 32 {
		return nil, ErrCursorKeyTooShort
	}
	derived := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(hkdf.New(sha256.New, key, nil, []byte(cursorKeyInfo)), derived); err != nil {
		return nil, fmt.Errorf("deriving cursor key: %w", err)
	}
	aead, err := chacha20poly1305.NewX(derived)
	if err != nil {
		return nil, fmt.Errorf("building cursor cipher: %w", err)
	}
	return &CursorSigner{aead: aead}, nil
}

// Encode renders an encrypted, authenticated, opaque cursor.
//
// Wire form: base64url( version ‖ nonce ‖ ciphertext ‖ tag ), no padding, no
// separator. The nonce is 24 random bytes per call, so two cursors for the same
// position are different strings and nothing about the token repeats.
func (s *CursorSigner) Encode(c Cursor) (string, error) {
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	c.SortKey = c.SortKey.UTC()
	c.IssuedAt = c.IssuedAt.UTC()

	plaintext, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encoding cursor: %w", err)
	}

	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("cursor nonce: %w", err)
	}

	token := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	token = append(token, cursorVersion)
	token = append(token, nonce...)
	token = s.aead.Seal(token, nonce, plaintext, cursorAAD(c.CompanyID, c.Filter))
	return base64.RawURLEncoding.EncodeToString(token), nil
}

// Decode authenticates a cursor against the CURRENT request and returns its
// position.
//
// companyID and filter are what the request resolves to, not what a token
// claims -- a token claims nothing, because neither is in it. They are bound as
// associated data, so a cursor that was issued for another company or another
// query fails authentication exactly as a forged one does. Only a token that
// authenticates is ever parsed; only a parsed token can be too old.
//
// maxAge of zero disables expiry, which is right for a resource with no
// retention window; a caller with one passes it and gets ErrCursorExpired rather
// than a silently empty page.
func (s *CursorSigner) Decode(encoded string, companyID int64, filter string,
	maxAge time.Duration) (Cursor, error) {

	token, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, ErrCursorInvalid
	}
	nonceSize := s.aead.NonceSize()
	if len(token) < 1+nonceSize+s.aead.Overhead() || token[0] != cursorVersion {
		return Cursor{}, ErrCursorInvalid
	}
	nonce := token[1 : 1+nonceSize]
	ciphertext := token[1+nonceSize:]

	plaintext, err := s.aead.Open(nil, nonce, ciphertext, cursorAAD(companyID, filter))
	if err != nil {
		// Tampered, forged, another key, another company, another filter: one
		// answer, and deliberately so.
		return Cursor{}, ErrCursorInvalid
	}

	var c Cursor
	if err := json.Unmarshal(plaintext, &c); err != nil {
		return Cursor{}, ErrCursorInvalid
	}
	c.CompanyID = companyID
	c.Filter = filter

	if maxAge > 0 && time.Since(c.IssuedAt) > maxAge {
		return Cursor{}, ErrCursorExpired
	}
	return c, nil
}

// cursorAAD is the associated data a token is bound to: the format version, the
// tenant and the filter fingerprint, with an unambiguous separator so that
// (company 1, filter "2x") and (company 12, filter "x") cannot collide.
func cursorAAD(companyID int64, filter string) []byte {
	aad := make([]byte, 0, 1+8+1+len(filter))
	aad = append(aad, cursorVersion)
	aad = binary.BigEndian.AppendUint64(aad, uint64(companyID))
	aad = append(aad, 0x1f)
	aad = append(aad, filter...)
	return aad
}

// FilterFingerprint reduces a query's filters to a stable short string.
//
// ORDER-INDEPENDENT, because ?active=true&site_id=x and ?site_id=x&active=true
// are the same query and a client that reorders its parameters between pages has
// not changed anything. Sorting before hashing is what makes that true.
//
// The fingerprint is not a secret and does not need to be: it exists so that a
// cursor cannot be silently reused across a change of filters, and it is bound
// into the token's authentication.
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
