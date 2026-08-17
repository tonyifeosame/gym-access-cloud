package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"access-terminal-cloud-api/models"
)

// FW-09: an external id a terminal cannot store.
//
// THE DEFECT THESE WOULD HAVE CAUGHT. `people.external_id` is VARCHAR(50) and
// nothing validated what went into it. The terminal's field holds 31 usable
// bytes and its parser REFUSES anything longer rather than truncating -- so a
// person created with a 40-character id was fanned out to every terminal, and
// every terminal refused the job. The person existed, showed as having access,
// and could not open a door. Ten retries later the job parked FAILED.
//
// The tests are written against the API rather than the validator, because the
// validator was never the missing piece -- calling it was.

// TestCreatePersonRefusesUnstorableExternalID covers both creation surfaces.
//
// The legacy site-key API and the console are separate handlers that happen to
// share a store, and the finding is about what an OPERATOR can bring into
// existence, so both are asserted rather than the one they have in common.
func TestCreatePersonRefusesUnstorableExternalID(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	_, token, csrf := consoleOperatorSession(t, env.router,
		operatorCompanyID(t, "one"), "people-admin@example.com", models.RoleManager)

	cases := []struct {
		name       string
		externalID string
		because    string
	}{
		{
			name:       "longer than the device field",
			externalID: strings.Repeat("M", models.MaxExternalIDLength+1),
			because:    "32 bytes does not fit a 32-byte field with a terminator",
		},
		{
			name:       "far longer, but inside the database column",
			externalID: strings.Repeat("M", 50),
			because:    "VARCHAR(50) accepts it, which is exactly why the column is not the check",
		},
		{
			name:       "contains a space",
			externalID: "MEMBER 1",
			because:    "0x20 is below the firmware's 0x21 floor",
		},
		{
			name:       "contains a control character",
			externalID: "M-1\n",
			because:    "the id is composed into a URL path and a JSON body on the device",
		},
		{
			name:       "non-ASCII",
			externalID: "M-Ünïcode",
			because:    "multi-byte characters cost more bytes than they look like",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+" (legacy API)", func(t *testing.T) {
			res := env.do(http.MethodPost, "/api/v1/members", map[string]any{
				"member_id": tc.externalID, "full_name": "Ada",
				"membership_type": "PREMIUM", "active": true,
			}, siteAuth(env.siteAKey))

			if res.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 -- %s (body %s)", res.Code, tc.because, res.Raw)
			}
		})

		t.Run(tc.name+" (console)", func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"external_id": tc.externalID, "full_name": "Ada",
			})
			if err != nil {
				t.Fatalf("encoding request: %v", err)
			}

			code, decoded := consoleCall(t, env.router, http.MethodPost,
				"/api/v1/console/people", string(body), token, csrf)
			if code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400 -- %s (body %v)", code, tc.because, decoded)
			}
		})
	}
}

// TestRefusedExternalIDCreatesNobody is the half that matters operationally.
//
// A 400 that still inserted the row would be worse than no validation at all:
// the operator is told it failed, the person is on the roster, and the terminal
// refuses them for ever.
func TestRefusedExternalIDCreatesNobody(t *testing.T) {
	env := newTestEnv(t)
	tooLong := strings.Repeat("M", 40)

	env.do(http.MethodPost, "/api/v1/members", map[string]any{
		"member_id": tooLong, "full_name": "Ada",
		"membership_type": "PREMIUM", "active": true,
	}, siteAuth(env.siteAKey))

	if n := queryInt(t, `SELECT count(*) FROM people WHERE external_id = $1`, tooLong); n != 0 {
		t.Fatalf("%d people rows exist for a refused id, want 0", n)
	}
	if n := queryInt(t, `SELECT count(*) FROM sync_jobs WHERE entity_external_id = $1`, tooLong); n != 0 {
		t.Fatalf("%d sync jobs were queued for a refused id, want 0", n)
	}
}

// TestExternalIDErrorNamesTheConstraint is about the message, and it is a real
// requirement rather than polish.
//
// The operator who typed the value is the only person who can fix it, and they
// cannot see the device's field width. "Invalid external_id" sends them to
// support; a message carrying the number sends them back to the form.
func TestExternalIDErrorNamesTheConstraint(t *testing.T) {
	env := newTestEnv(t)

	res := env.do(http.MethodPost, "/api/v1/members", map[string]any{
		"member_id": strings.Repeat("M", 40), "full_name": "Ada",
		"membership_type": "PREMIUM", "active": true,
	}, siteAuth(env.siteAKey))

	message, _ := res.Body["error"].(string)
	if !strings.Contains(message, "31") {
		t.Errorf("error %q does not state the limit a terminal can store", message)
	}
	if !strings.Contains(strings.ToLower(message), "terminal") {
		t.Errorf("error %q does not say what the limit belongs to", message)
	}
}

// TestUsableExternalIDsAreStillAccepted is the regression guard on the other
// side. A validator that refused ordinary member numbers would be a worse
// outage than the one it fixed, so the shapes a real customer uses are pinned.
func TestUsableExternalIDsAreStillAccepted(t *testing.T) {
	env := newTestEnv(t)

	accepted := []string{
		"M-1",
		"1",
		strings.Repeat("M", models.MaxExternalIDLength), // exactly at the limit
		"EMP/2026/00417",
		"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", // a UUID is 36 characters
	}

	for _, id := range accepted {
		if len(id) > models.MaxExternalIDLength {
			// The UUID above is deliberately in the list: it is what an
			// integrator reaches for first, it does NOT fit, and a reader of
			// this test should see that stated rather than infer it.
			if err := models.ValidateExternalID(id); err == nil {
				t.Fatalf("%q is %d characters and should not validate", id, len(id))
			}
			continue
		}

		res := env.do(http.MethodPost, "/api/v1/members", map[string]any{
			"member_id": id, "full_name": "Ada",
			"membership_type": "PREMIUM", "active": true,
		}, siteAuth(env.siteAKey))
		if res.Code != http.StatusCreated {
			t.Errorf("%q got %d, want 201 (body %s)", id, res.Code, res.Raw)
		}
	}
}

// TestExternalIDValidationMatchesTheFirmware pins the two numbers to the device
// rather than to this repository's opinion of them.
//
// If the firmware's field grows, this test is where the disagreement should
// surface -- as a deliberate edit with the new constant in it, not as a person
// silently failing to reach a door.
func TestExternalIDValidationMatchesTheFirmware(t *testing.T) {
	// include/member_store.h: kMemberExternalIdSize = 32, one byte of which is
	// the terminator.
	if models.MaxExternalIDLength != 31 {
		t.Errorf("MaxExternalIDLength = %d, but the device field holds 31 usable bytes",
			models.MaxExternalIDLength)
	}

	// src/member_store.cpp externalIdIsWellFormed: every byte must be in
	// 0x21..0x7E. Walked here so the boundaries are asserted rather than
	// described.
	for b := 0; b < 256; b++ {
		id := string([]byte{byte(b)})
		storable := models.ExternalIDIsStorable(id)
		want := b >= 0x21 && b <= 0x7E
		if storable != want {
			t.Errorf("byte 0x%02X: storable = %v, want %v", b, storable, want)
		}
	}
}
