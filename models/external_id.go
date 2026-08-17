package models

import (
	"errors"
	"fmt"
)

// The external id constraint (FW-09).
//
// ---------------------------------------------------------------------------
// THE FINDING
// ---------------------------------------------------------------------------
//
// `people.external_id` is VARCHAR(50) and nothing validated what went into it.
// The terminal's row holds 31 usable characters, and its parser REFUSES an id
// that does not fit rather than truncating it -- correctly, because a truncated
// identifier is not a shorter name for the same person, it is a different
// person, and every decision downstream is keyed on it.
//
// The two halves compose into the failure the audit recorded: an operator
// creates somebody the console accepts, the platform fans them out, and every
// terminal refuses the job. The person exists, appears on the roster, is shown
// as having access, and cannot open a door. Ten retries later the job parks
// FAILED and the only evidence is a counter.
//
// So the check belongs HERE, at the moment somebody is named, where the answer
// can be a sentence an operator can act on.
//
// ---------------------------------------------------------------------------
// WHY THESE EXACT BOUNDS
// ---------------------------------------------------------------------------
//
// They are the firmware's, read off the firmware, not invented here:
//
//	include/member_store.h    kMemberExternalIdSize = 32, so 31 usable bytes
//	src/member_store.cpp      externalIdIsWellFormed: every byte in 0x21..0x7E
//
// The character rule is not stylistic. The value is composed into a URL path
// and a JSON body by the terminal's sync client, so a control byte in it is an
// injection rather than a typo -- which is why the device refuses one, and why
// refusing it here is the same decision made earlier.
//
// A SPACE IS EXCLUDED because 0x20 is below 0x21. That is the firmware's rule
// and this must not be more permissive than the thing it is protecting: an id
// this layer accepted and the terminal rejected would put us back exactly where
// the finding started.
//
// ---------------------------------------------------------------------------
// WHY THE SERVER COLUMN IS NOT NARROWED TO MATCH
// ---------------------------------------------------------------------------
//
// VARCHAR(50) stays. Deployed installations may already hold ids this rule
// refuses, and a migration that added a CHECK constraint would fail to apply
// against a real customer database -- turning a data-quality problem into an
// upgrade that cannot run. The constraint is enforced on the way IN, so nothing
// new can violate it, and what is already stored keeps being served.
//
// Finding the rows an existing installation already holds is a query, and it is
// written out in docs/market-readiness.md under FW-09 so an operator can run it
// before blaming a terminal for people it was never able to accept.

// MaxExternalIDLength is the longest id a terminal can store.
//
// 31, because the on-device field is 32 bytes and one of them is the
// terminator. Named rather than written as a literal, since the number appears
// in an error message a human reads and in the validation beside it.
const MaxExternalIDLength = 31

// ErrExternalIDRequired is returned when no id was supplied at all.
var ErrExternalIDRequired = errors.New("external_id is required")

// ErrExternalIDUnusable is the shape of every other refusal.
//
// A sentinel to test against with errors.Is, wrapped by ValidateExternalID with
// the specific reason so the caller can return one message that both names the
// constraint and says which part of it was missed.
var ErrExternalIDUnusable = errors.New("external_id cannot be stored by a terminal")

// ValidateExternalID reports whether an id can reach a terminal.
//
// The error text is written to be shown to whoever typed the value: it says
// what the limit is and why the limit exists, because "invalid external_id" in
// a console dialog tells an operator nothing they can act on.
func ValidateExternalID(id string) error {
	if id == "" {
		return ErrExternalIDRequired
	}

	// Counted in BYTES, not runes, and that is deliberate: the device field is
	// 32 bytes, so a multi-byte character costs more than one of them. Counting
	// runes here would accept a 20-character id that needs 40 bytes on the wire
	// -- which the range check below refuses anyway, but only because every
	// non-ASCII byte is out of range. The two checks agree on purpose.
	if len(id) > MaxExternalIDLength {
		return fmt.Errorf("%w: %d characters is longer than the %d a terminal can store",
			ErrExternalIDUnusable, len(id), MaxExternalIDLength)
	}

	for i := 0; i < len(id); i++ {
		if b := id[i]; b < 0x21 || b > 0x7E {
			return fmt.Errorf(
				"%w: it must be printable ASCII with no spaces (character %d is not)",
				ErrExternalIDUnusable, i+1)
		}
	}

	return nil
}

// ExternalIDIsStorable is ValidateExternalID as a predicate, for the places
// that are filtering a list rather than answering a request.
func ExternalIDIsStorable(id string) bool { return ValidateExternalID(id) == nil }
