package models

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// Signed pagination cursors.
//
// The signature is NOT the tenancy boundary -- every query carries its own
// company filter regardless -- so what these tests protect is that a cursor
// cannot be edited into a different position, cannot be carried between
// accounts, and cannot be replayed against a different set of filters.

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

func TestCursorRoundTrips(t *testing.T) {
	signer := testSigner(t)
	original := sampleCursor()

	encoded, err := signer.Encode(original)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	decoded, err := signer.Decode(encoded, original.CompanyID, original.Filter, 0)
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("id = %d, want %d", decoded.ID, original.ID)
	}
	if !decoded.SortKey.Equal(original.SortKey) {
		t.Errorf("sort key = %v, want %v", decoded.SortKey, original.SortKey)
	}
	if decoded.CompanyID != original.CompanyID {
		t.Errorf("company = %d, want %d", decoded.CompanyID, original.CompanyID)
	}
}

// base64url without padding, so it survives a query string untouched. A client
// that has to escape a cursor will eventually fail to.
func TestCursorIsURLSafe(t *testing.T) {
	signer := testSigner(t)
	encoded, err := signer.Encode(sampleCursor())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("cursor %q contains characters that need escaping in a URL", encoded)
	}
	if !strings.Contains(encoded, ".") {
		t.Errorf("cursor %q has no signature separator", encoded)
	}
}

func TestATamperedCursorIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded, err := signer.Encode(sampleCursor())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	body, signature, _ := strings.Cut(encoded, ".")

	// Re-encode the payload with a different position and keep the signature.
	// This is the attack the signature exists for: choosing your own bounds.
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	forged := base64.RawURLEncoding.EncodeToString(
		[]byte(strings.Replace(string(raw), `"i":1042`, `"i":9999`, 1)))

	if _, err := signer.Decode(forged+"."+signature, 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("an edited payload decoded with err = %v, want ErrCursorInvalid", err)
	}

	// A mutated signature over an untouched payload.
	if _, err := signer.Decode(body+".AAAA", 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("a mutated signature decoded with err = %v, want ErrCursorInvalid", err)
	}
}

func TestACursorFromAnotherSignerIsRefused(t *testing.T) {
	mine := testSigner(t)
	theirs, err := NewCursorSigner([]byte("ffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("building second signer: %v", err)
	}

	encoded, err := theirs.Encode(sampleCursor())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if _, err := mine.Decode(encoded, 42, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("a foreign signature decoded with err = %v, want ErrCursorInvalid", err)
	}
}

// A cursor minted for one account, presented by another. The query would filter
// it out anyway; refusing it makes the failure legible rather than an empty page
// somebody debugs as missing data.
func TestACursorFromAnotherCompanyIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded, err := signer.Encode(sampleCursor()) // company 42
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	_, err = signer.Decode(encoded, 43, "abc123", 0)
	if !errors.Is(err, ErrCursorWrongTenant) {
		t.Fatalf("cross-company decode = %v, want ErrCursorWrongTenant", err)
	}
}

// The check must come AFTER the signature, so an unsigned probe cannot use the
// difference between "wrong tenant" and "invalid" to learn that a cursor was
// real.
func TestTenantCheckIsNotReachableWithoutAValidSignature(t *testing.T) {
	signer := testSigner(t)
	encoded, err := signer.Encode(sampleCursor())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	body, _, _ := strings.Cut(encoded, ".")

	// A valid payload for company 42 with a junk signature, presented as
	// company 43. If the tenant check ran first this would report the tenant
	// mismatch and confirm the payload parsed.
	if _, err := signer.Decode(body+".AAAA", 43, "abc123", 0); !errors.Is(err, ErrCursorInvalid) {
		t.Errorf("decode = %v, want ErrCursorInvalid (the signature must be checked first)", err)
	}
}

func TestACursorFromADifferentQueryIsRefused(t *testing.T) {
	signer := testSigner(t)
	encoded, err := signer.Encode(sampleCursor()) // filter "abc123"
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	_, err = signer.Decode(encoded, 42, "different", 0)
	if !errors.Is(err, ErrCursorFilterChanged) {
		t.Fatalf("decode with changed filters = %v, want ErrCursorFilterChanged", err)
	}
}

func TestAnExpiredCursorIsRefused(t *testing.T) {
	signer := testSigner(t)

	stale := sampleCursor()
	stale.IssuedAt = time.Now().Add(-2 * time.Hour).UTC()

	encoded, err := signer.Encode(stale)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	if _, err := signer.Decode(encoded, 42, "abc123", time.Hour); !errors.Is(err, ErrCursorExpired) {
		t.Errorf("an old cursor decoded with err = %v, want ErrCursorExpired", err)
	}

	// maxAge of zero disables expiry, for a resource with no retention window.
	if _, err := signer.Decode(encoded, 42, "abc123", 0); err != nil {
		t.Errorf("with expiry disabled, decode = %v, want success", err)
	}
}

func TestMalformedCursorsAreRefused(t *testing.T) {
	signer := testSigner(t)

	for _, in := range []string{
		"",
		"nodot",
		".onlysignature",
		"onlybody.",
		"!!!not base64!!!.AAAA",
	} {
		if _, err := signer.Decode(in, 42, "abc123", 0); err == nil {
			t.Errorf("Decode(%q) succeeded, want a refusal", in)
		}
	}
}

func TestAShortSigningKeyIsRefused(t *testing.T) {
	if _, err := NewCursorSigner([]byte("too short")); !errors.Is(err, ErrCursorKeyTooShort) {
		t.Errorf("NewCursorSigner with a short key = %v, want ErrCursorKeyTooShort", err)
	}
}

// The signer must not alias the caller's key buffer: a caller that reuses or
// zeroes its slice afterwards would otherwise silently change every signature.
func TestSignerCopiesItsKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := NewCursorSigner(key)
	if err != nil {
		t.Fatalf("building signer: %v", err)
	}

	encoded, err := signer.Encode(sampleCursor())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	for i := range key {
		key[i] = 0
	}

	if _, err := signer.Decode(encoded, 42, "abc123", 0); err != nil {
		t.Errorf("after the caller zeroed its key buffer, decode = %v", err)
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
