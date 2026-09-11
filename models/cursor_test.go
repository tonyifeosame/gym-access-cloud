package models

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// Encrypted, authenticated pagination cursors (v2).
//
// The cursor is NOT the tenancy boundary -- every query carries its own company
// filter regardless -- so what these tests protect is that a cursor cannot be
// edited into a different position, cannot be carried between accounts, cannot
// be replayed against a different set of filters, and cannot be READ: the
// internal ids it positions on never leave the server in the clear.

func testSigner(t *testing.T) *CursorSigner {
	t.Helper()
	signer, err := NewCursorSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("building signer: %v", err)
	}
	return signer
}

func sampleCursor() Cursor {
	return Cursor{
		CompanyID: 42,
		SortKey:   time.Date(2026, 9, 9, 17, 0, 0, 0, time.UTC),
		ID:        1042,
		Filter:    "abc123",
		IssuedAt:  time.Now().UTC(),
	}
}

func mustEncode(t *testing.T, s *CursorSigner, c Cursor) string {
	t.Helper()
	encoded, err := s.Encode(c)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return encoded
}

func TestCursorRoundTrips(t *testing.T) {
	signer := testSigner(t)
	original := sampleCursor()

	decoded, err := signer.Decode(mustEncode(t, signer, original), original.CompanyID, original.Filter, 0)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if decoded.ID != original.ID {
		t.Errorf("id = %d, want %d", decoded.ID, original.ID)
	}
	if !decoded.SortKey.Equal(original.SortKey) {
		t.Errorf("sort key = %v, want %v", decoded.SortKey, original.SortKey)
	}
	// The tenant and filter a caller reads back are the ones IT supplied.
	if decoded.CompanyID != original.CompanyID || decoded.Filter != original.Filter {
		t.Errorf("company/filter = %d/%q, want %d/%q", decoded.CompanyID, decoded.Filter,
			original.CompanyID, original.Filter)
	}
}

// base64url without padding, so it survives a query string untouched. A client
// that has to escape a cursor will eventually fail to.
func TestCursorIsURLSafe(t *testing.T) {
	encoded := mustEncode(t, testSigner(t), sampleCursor())
	if strings.ContainsAny(encoded, "+/=.") {
		t.Errorf("cursor contains characters a query string would mangle: %q", encoded)
	}
}

// THE POINT OF v2. The token must not reveal the internal company id, the
// internal row id, or even that it is JSON. A client that base64-decodes it
// learns nothing but a version byte and noise.
func TestACursorIsOpaque(t *testing.T) {
	c := sampleCursor()
	c.ID = 1042
	c.CompanyID = 42
	raw, err := base64.RawURLEncoding.DecodeString(mustEncode(t, testSigner(t), c))
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if raw[0] != cursorVersion {
		t.Errorf("first byte = %#x, want the version byte %#x", raw[0], cursorVersion)
	}
	// Multi-byte markers only: a one- or two-byte pattern would eventually
	// appear by chance in random ciphertext and make this test flaky.
	for _, leak := range []string{`"i":`, `"c":`, `"k":`, `"t":`, "1042", "2026-09"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("decoded token contains %q -- the payload is readable", leak)
		}
	}
	// And the id is not present as raw big-endian bytes either.
	if bytes.Contains(raw[1:], []byte{0, 0, 0, 0, 0, 0, 0x04, 0x12}) {
		t.Error("decoded token contains the row id as bytes")
	}
}

// A random nonce per Encode: the same position never produces the same token,
// so a client cannot compare cursors to learn that two listings stood at the
// same row.
func TestTwoCursorsForTheSamePositionDiffer(t *testing.T) {
	signer := testSigner(t)
	c := sampleCursor()
	a, b := mustEncode(t, signer, c), mustEncode(t, signer, c)
	if a == b {
		t.Fatal("two encodings of the same cursor are identical; the nonce is not random")
	}
	for _, encoded := range []string{a, b} {
		if _, err := signer.Decode(encoded, 42, "abc123", 0); err != nil {
			t.Errorf("decode = %v, want success", err)
		}
	}
}

func TestATamperedCursorIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded := mustEncode(t, signer, sampleCursor())
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}

	// Flip one bit in every region of the token: nonce, ciphertext, tag. Each
	// must be refused as invalid, never as expired and never as success.
	for _, at := range []int{1, 1 + 12, len(raw) - 20, len(raw) - 1} {
		forged := append([]byte(nil), raw...)
		forged[at] ^= 0x01
		_, err := signer.Decode(base64.RawURLEncoding.EncodeToString(forged), 42, "abc123", 0)
		if !errors.Is(err, ErrCursorInvalid) {
			t.Errorf("bit flipped at %d: decode = %v, want ErrCursorInvalid", at, err)
		}
	}

	// Truncation.
	if _, err := signer.Decode(base64.RawURLEncoding.EncodeToString(raw[:len(raw)-1]), 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("truncated: decode = %v, want ErrCursorInvalid", err)
	}
	// Version byte changed.
	forged := append([]byte(nil), raw...)
	forged[0] = 0x01
	if _, err := signer.Decode(base64.RawURLEncoding.EncodeToString(forged), 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("version changed: decode = %v, want ErrCursorInvalid", err)
	}
}

func TestACursorFromAnotherSignerIsRefused(t *testing.T) {
	mine := testSigner(t)
	theirs, err := NewCursorSigner([]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("building second signer: %v", err)
	}
	if _, err := mine.Decode(mustEncode(t, theirs, sampleCursor()), 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("a foreign key decoded with err = %v, want ErrCursorInvalid", err)
	}
}

// A cursor minted for one account, presented by another. The query would filter
// it out anyway; refusing it makes the failure legible rather than an empty page
// somebody debugs as missing data. In v2 the refusal is an authentication
// failure -- indistinguishable from a forgery, which is the point: nothing
// confirms the cursor was real.
func TestACursorFromAnotherCompanyIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded := mustEncode(t, signer, sampleCursor()) // company 42
	_, err := signer.Decode(encoded, 43, "abc123", 0)
	if !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("cross-company decode = %v, want ErrCursorInvalid", err)
	}
	// The retained v1 sentinel still matches the v2 answer for old callers.
	if !errors.Is(ErrCursorWrongTenant, ErrCursorInvalid) {
		t.Error("ErrCursorWrongTenant must be a kind of ErrCursorInvalid")
	}
}

func TestACursorFromADifferentQueryIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded := mustEncode(t, signer, sampleCursor()) // filter "abc123"
	if _, err := signer.Decode(encoded, 42, "different", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("decode with changed filters = %v, want ErrCursorInvalid", err)
	}
	// Associated data is unambiguous: company 4 with filter "2abc123" is not
	// company 42 with filter "abc123".
	if _, err := signer.Decode(encoded, 4, "2abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("shifted company/filter boundary decoded = %v, want ErrCursorInvalid", err)
	}
}

// Expiry is only ever decided on a cursor that authenticated: a stale forgery is
// invalid, not expired.
func TestAnExpiredCursorIsRefused(t *testing.T) {
	signer := testSigner(t)
	stale := sampleCursor()
	stale.IssuedAt = time.Now().Add(-2 * time.Hour).UTC()
	encoded := mustEncode(t, signer, stale)

	if _, err := signer.Decode(encoded, 42, "abc123", time.Hour); !errors.Is(err, ErrCursorExpired) {
		t.Errorf("an old cursor decoded with err = %v, want ErrCursorExpired", err)
	}
	// maxAge of zero disables expiry, for a resource with no retention window.
	if _, err := signer.Decode(encoded, 42, "abc123", 0); err != nil {
		t.Errorf("with expiry disabled, decode = %v, want success", err)
	}
	// Stale AND presented by another company: invalid, not expired.
	if _, err := signer.Decode(encoded, 43, "abc123", time.Hour); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("stale foreign cursor = %v, want ErrCursorInvalid (authentication first)", err)
	}
}

func TestMalformedCursorsAreRefused(t *testing.T) {
	signer := testSigner(t)
	for _, in := range []string{
		"",
		"x",
		"!!!not base64!!!",
		base64.RawURLEncoding.EncodeToString([]byte{cursorVersion}),
		base64.RawURLEncoding.EncodeToString(make([]byte, 1+24)),    // version + nonce, no tag
		base64.RawURLEncoding.EncodeToString(make([]byte, 1+24+16)), // right length, wrong everything
		base64.RawURLEncoding.EncodeToString(make([]byte, 200)),     // zeros
		"eyJjIjo0MiwiaSI6MTA0Mn0.AAAA",                              // a v1-shaped token
	} {
		if _, err := signer.Decode(in, 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
			t.Errorf("Decode(%q) = %v, want ErrCursorInvalid", in, err)
		}
	}
}

func TestAShortSigningKeyIsRefused(t *testing.T) {
	if _, err := NewCursorSigner([]byte("too short")); !errors.Is(err, ErrCursorKeyTooShort) {
		t.Errorf("NewCursorSigner with a short key = %v, want ErrCursorKeyTooShort", err)
	}
}

// The signer must not alias the caller's key buffer: a caller that reuses or
// zeroes its slice afterwards would otherwise silently change every token.
func TestSignerCopiesItsKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := NewCursorSigner(key)
	if err != nil {
		t.Fatalf("building signer: %v", err)
	}
	encoded := mustEncode(t, signer, sampleCursor())
	for i := range key {
		key[i] = 0
	}
	if _, err := signer.Decode(encoded, 42, "abc123", 0); err != nil {
		t.Errorf("after the caller zeroed its key buffer, decode = %v", err)
	}
}

// Two signers over the same configured key agree -- which is what lets two
// instances sharing CURSOR_SIGNING_KEY honour each other's cursors.
func TestTwoSignersOverOneKeyAgree(t *testing.T) {
	a := testSigner(t)
	b := testSigner(t)
	if _, err := b.Decode(mustEncode(t, a, sampleCursor()), 42, "abc123", 0); err != nil {
		t.Errorf("a sibling signer refused the cursor: %v", err)
	}
}

func TestFilterFingerprintIsOrderIndependent(t *testing.T) {
	first := FilterFingerprint(map[string]string{"active": "true", "site_id": "abc"})
	second := FilterFingerprint(map[string]string{"site_id": "abc", "active": "true"})
	if first != second {
		t.Errorf("reordering the same filters changed the fingerprint: %s vs %s", first, second)
	}
	if FilterFingerprint(map[string]string{"active": "false", "site_id": "abc"}) == first {
		t.Error("a different filter value produced the same fingerprint")
	}
}

// An empty value is an absent filter. ?active= and no parameter at all are the
// same query, and treating them as different would refuse a cursor for no reason
// a caller could understand.
func TestFilterFingerprintIgnoresEmptyValues(t *testing.T) {
	none := FilterFingerprint(nil)
	if FilterFingerprint(map[string]string{"active": ""}) != none {
		t.Error("an empty filter value changed the fingerprint")
	}
	if FilterFingerprint(map[string]string{}) != none {
		t.Error("an empty filter map differs from a nil one")
	}
}
