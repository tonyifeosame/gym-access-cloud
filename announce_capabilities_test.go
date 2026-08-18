package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Capabilities on the ANNOUNCE, and the rate limiting that lets a whole site
// announce at once.
//
// ---------------------------------------------------------------------------
// WHY THE ANNOUNCE CARRIES THEM AT ALL
// ---------------------------------------------------------------------------
//
// The heartbeat already reports capabilities and the Change Wi-Fi gate already
// reads them (025). But the heartbeat needs a credential, and the announce
// happens BEFORE there is one -- so at the moment an administrator is deciding
// whether to approve a piece of hardware and bolt it to a door, the platform
// knew nothing about what it could do.
//
// That is the wrong moment to be missing it. "This terminal cannot be recovered
// over the network" costs nothing to learn before the unit is mounted and costs
// a site visit to learn afterwards.
//
// SHOWN, NEVER TRUSTED. The gate still reads devices.capabilities, which only an
// authenticated heartbeat writes. This is corroboration for a human, on the same
// footing as the firmware version and the IP the unit called from.

// announceWith posts an announcement carrying whatever body the caller wants,
// so a test can send capabilities, omit them, or send an empty list.
func announceWith(t *testing.T, env *testEnv, body map[string]any) response {
	t.Helper()
	return env.do(http.MethodPost, "/api/v1/devices/announce", body, nil)
}

// storedAnnouncementCapabilities reads the column straight out of the row.
//
// Returns nil for "never reported", which is NOT the same as an empty list --
// the whole point of the column is that those two are different, so the helper
// preserves the difference rather than flattening it.
func storedAnnouncementCapabilities(t *testing.T, serial string) []string {
	t.Helper()

	var raw []byte
	err := database.DB.QueryRow(`
		SELECT capabilities FROM terminal_announcements
		 WHERE serial_number = $1 ORDER BY id DESC LIMIT 1`, serial).Scan(&raw)
	if err != nil {
		t.Fatalf("reading announcement capabilities for %s: %v", serial, err)
	}
	if len(raw) == 0 {
		return nil
	}

	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding announcement capabilities: %v", err)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func TestAnAnnouncedCapabilityListIsPersisted(t *testing.T) {
	f := newAnnounceFixture(t)

	res := announceWith(t, f.env, map[string]any{
		"serial_number":    "AT-ANN-CAP-1",
		"firmware_version": "1.2.0",
		"capabilities": []string{
			models.CapabilityWifiProvisioning,
			models.CapabilityWifiRecovery,
			models.CapabilityTerminalAnnounce,
		},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("announce = %d, want 201 (%s)", res.Code, res.Raw)
	}

	// NOT SILENTLY DISCARDED, which is what this endpoint used to do: the
	// firmware sent the field, the request model had no place for it, and it
	// went nowhere.
	got := storedAnnouncementCapabilities(t, "AT-ANN-CAP-1")
	if !database.DeviceHasCapability(got, models.CapabilityWifiRecovery) {
		t.Fatalf("stored announcement capabilities = %v, want wifi_recovery among them", got)
	}
	if len(got) != 3 {
		t.Fatalf("stored announcement capabilities = %v, want three", got)
	}
}

func TestAnAnnounceWithoutCapabilitiesStoresNullNotEmpty(t *testing.T) {
	f := newAnnounceFixture(t)
	f.announce(t, "AT-ANN-CAP-2")

	// NIL, not []. Every unit built before capability reporting announces like
	// this, and the console has to be able to say "we have not been told" rather
	// than "this terminal can do nothing" -- they send an operator to different
	// places.
	if got := storedAnnouncementCapabilities(t, "AT-ANN-CAP-2"); got != nil {
		t.Fatalf("an announce with no capability field stored %v, want NULL", got)
	}
}

func TestATerminalCanAnnounceThatItHasNoCapabilities(t *testing.T) {
	f := newAnnounceFixture(t)

	res := announceWith(t, f.env, map[string]any{
		"serial_number": "AT-ANN-CAP-3",
		"capabilities":  []string{},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("announce = %d, want 201 (%s)", res.Code, res.Raw)
	}

	// AN EMPTY LIST IS A REAL ANSWER: "I report my capabilities, and I have none
	// of them". Stored as such, and distinguishable from silence.
	got := storedAnnouncementCapabilities(t, "AT-ANN-CAP-3")
	if got == nil {
		t.Fatal("an explicit empty list stored as NULL, losing the distinction")
	}
	if len(got) != 0 {
		t.Fatalf("an explicit empty list stored as %v", got)
	}
}

func TestAReannounceWithoutCapabilitiesDoesNotEraseThem(t *testing.T) {
	f := newAnnounceFixture(t)

	res := announceWith(t, f.env, map[string]any{
		"serial_number": "AT-ANN-CAP-4",
		"capabilities":  []string{models.CapabilityWifiRecovery},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("announce = %d, want 201 (%s)", res.Code, res.Raw)
	}
	token, _ := res.Body["announce_token"].(string)

	// The SAME unit re-announcing with its token -- a reboot, or the poll loop
	// starting over. It names no capabilities this time.
	again := f.env.do(http.MethodPost, "/api/v1/devices/announce",
		map[string]any{"serial_number": "AT-ANN-CAP-4"}, announceHeader(token))
	if again.Code != http.StatusOK {
		t.Fatalf("re-announce = %d, want 200 (%s)", again.Code, again.Raw)
	}

	// MERGED, not replaced. A terminal re-announcing must not blank a list an
	// operator is looking at on the approval screen.
	got := storedAnnouncementCapabilities(t, "AT-ANN-CAP-4")
	if !database.DeviceHasCapability(got, models.CapabilityWifiRecovery) {
		t.Fatalf("a re-announce with no capability field erased them: %v", got)
	}
}

func TestAnAnnouncedCapabilityListIsNotAnArrayIsRefusedSafely(t *testing.T) {
	f := newAnnounceFixture(t)

	// A scalar where an array belongs. The binding refuses it, which is the
	// right answer -- what must NOT happen is a 500 from a CHECK constraint
	// fired deep inside the insert, which is where the column's own guard sits.
	res := announceWith(t, f.env, map[string]any{
		"serial_number": "AT-ANN-CAP-5",
		"capabilities":  "wifi_recovery",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("announce with a scalar capability list = %d, want 400 (%s)",
			res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// What the console reads
// ---------------------------------------------------------------------------

func TestTheApprovalScreenSeesWhatTheUnitSaidItCanDo(t *testing.T) {
	f := newAnnounceFixture(t)

	res := announceWith(t, f.env, map[string]any{
		"serial_number": "AT-ANN-CAP-6",
		"capabilities":  []string{models.CapabilityWifiRecovery},
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("announce = %d, want 201 (%s)", res.Code, res.Raw)
	}
	code, _ := res.Body["pairing_code"].(string)

	status, body := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d, want 200 (%v)", status, body)
	}

	// THE MOMENT IT MATTERS. This is the screen an administrator is looking at
	// while deciding whether to approve a piece of hardware and mount it.
	list, ok := body["capabilities"].([]any)
	if !ok {
		t.Fatalf("the approval screen carries no capability list: %v", body)
	}
	if len(list) != 1 || list[0] != models.CapabilityWifiRecovery {
		t.Fatalf("capabilities = %v, want [wifi_recovery]", list)
	}
}

func TestAnAnnouncementThatSaidNothingOmitsTheListEntirely(t *testing.T) {
	f := newAnnounceFixture(t)
	code, _ := f.announce(t, "AT-ANN-CAP-7")

	status, body := f.adopt(t, code)
	if status != http.StatusOK {
		t.Fatalf("adopt = %d, want 200 (%v)", status, body)
	}

	// OMITTED, not []. A console receiving an empty array would be entitled to
	// tell an operator this terminal can do nothing, which is a claim the
	// platform cannot support about a unit that simply did not say.
	if _, present := body["capabilities"]; present {
		t.Fatalf("an announcement that reported nothing carries a list: %v", body)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------
//
// A SITE IS ONE ADDRESS. Every terminal in a building shares the customer's
// public IP, and a terminal waiting to be approved polls twelve times a minute.
// The limiter was keyed on the address alone at sixty a minute, which is FIVE
// TERMINALS before a legitimate installation starts being refused -- and the
// refusals land on the customer commissioning hardware.
//
// The fix is two buckets, both of which must have a token: the address bounds
// what one network can do in aggregate, and a per-terminal bucket bounds one
// device by its own identity. Neither is sufficient alone, and these tests are
// the three properties that has to keep.
//
// The limits are lowered through the environment so the tests are fast and
// exact. NewRouter builds the limiters when it builds the routes, so the values
// have to be set before the env is created.

func TestOneAbusiveClientIsStillLimited(t *testing.T) {
	t.Setenv("ANNOUNCE_RATE_LIMIT_PER_MINUTE", "8")
	t.Setenv("ANNOUNCE_DEVICE_RATE_LIMIT_PER_MINUTE", "20")

	f := newAnnounceFixture(t)

	// EVERY REQUEST A DIFFERENT SERIAL, which is exactly what an attacker does
	// and exactly what the per-terminal bucket cannot see: each one lands in a
	// fresh bucket with a full allowance. The address limiter is the only thing
	// standing in the way, and it has to be.
	refused := 0
	for i := 0; i < 20; i++ {
		res := announceWith(t, f.env, map[string]any{
			"serial_number": "AT-FLOOD-" + string(rune('A'+i)),
		})
		if res.Code == http.StatusTooManyRequests {
			refused++
		}
	}

	if refused == 0 {
		t.Fatal("a client varying the serial on every request was never refused")
	}

	// And it is refused with something a client can act on rather than a bare
	// status: the firmware reads a 429 as "the announcement is fine, I asked too
	// often" and backs off, which needs a hint of how long.
	rec := f.env.raw(http.MethodPost, "/api/v1/devices/announce",
		map[string]any{"serial_number": "AT-FLOOD-Z"}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the flood was not still limited: %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a rate-limited announce carries no Retry-After")
	}
}

func TestASiteFullOfTerminalsCanAnnounceBehindOneAddress(t *testing.T) {
	// The regression this whole change exists for. Twelve terminals is a
	// perfectly ordinary gym, and at the old sixty-a-minute address limit the
	// sixth one onwards was refused -- with nothing anywhere saying why, on the
	// day a customer was installing them.
	t.Setenv("ANNOUNCE_RATE_LIMIT_PER_MINUTE", "300")
	t.Setenv("ANNOUNCE_DEVICE_RATE_LIMIT_PER_MINUTE", "20")

	f := newAnnounceFixture(t)

	// Every request from the same address, as they would be behind one router.
	// Each is a DIFFERENT terminal announcing once, which is what a bulk
	// installation looks like.
	tokens := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		serial := "AT-SITE-" + string(rune('A'+i))
		res := announceWith(t, f.env, map[string]any{
			"serial_number":    serial,
			"firmware_version": "1.2.0",
			"capabilities":     []string{models.CapabilityTerminalAnnounce},
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("terminal %d of 12 behind one address = %d, want 201 (%s)",
				i+1, res.Code, res.Raw)
		}
		token, _ := res.Body["announce_token"].(string)
		if token == "" {
			t.Fatalf("terminal %d announced without a token: %s", i+1, res.Raw)
		}
		tokens = append(tokens, token)
	}

	// AND THEN THEY ALL POLL, which is the sustained load rather than the burst:
	// twelve terminals waiting to be approved at the five-second cadence is 144
	// requests a minute from one address, well over twice what the old limit
	// allowed. Ten rounds here is 120 polls on top of the 12 announces.
	for round := 0; round < 10; round++ {
		for i, token := range tokens {
			if code := f.poll(t, token).Code; code != http.StatusOK {
				t.Fatalf("round %d, terminal %d poll = %d, want 200",
					round+1, i+1, code)
			}
		}
	}
}

func TestTheSameTerminalCannotHammerAnnounceIndefinitely(t *testing.T) {
	// The other half: raising the address allowance must not let ONE device
	// spend a whole site's budget. The per-terminal bucket is keyed on the
	// serial, so this unit is bounded by its own behaviour however much room the
	// address has.
	t.Setenv("ANNOUNCE_RATE_LIMIT_PER_MINUTE", "1000")
	t.Setenv("ANNOUNCE_DEVICE_RATE_LIMIT_PER_MINUTE", "6")

	f := newAnnounceFixture(t)

	refused := 0
	for i := 0; i < 20; i++ {
		res := announceWith(t, f.env, map[string]any{
			"serial_number": "AT-HAMMER-1",
		})
		if res.Code == http.StatusTooManyRequests {
			refused++
		}
	}

	if refused == 0 {
		t.Fatal("one terminal hammering announce was never refused, " +
			"even with the address allowance wide open")
	}

	// AND ITS NEIGHBOUR IS UNAFFECTED. This is the property the per-terminal
	// bucket exists for: one unit in a loop must not be able to stop the door
	// next to it being commissioned.
	res := announceWith(t, f.env, map[string]any{"serial_number": "AT-HAMMER-2"})
	if res.Code != http.StatusCreated {
		t.Fatalf("a second terminal behind the same address = %d, want 201 (%s)",
			res.Code, res.Raw)
	}
}

func TestPollingIsBoundedPerTokenRatherThanPerAddress(t *testing.T) {
	t.Setenv("ANNOUNCE_RATE_LIMIT_PER_MINUTE", "1000")
	t.Setenv("ANNOUNCE_DEVICE_RATE_LIMIT_PER_MINUTE", "6")

	f := newAnnounceFixture(t)
	_, tokenA := f.announce(t, "AT-POLL-A")
	_, tokenB := f.announce(t, "AT-POLL-B")

	// One terminal polling far too fast is cut off...
	refused := 0
	for i := 0; i < 20; i++ {
		if f.poll(t, tokenA).Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("a terminal polling in a tight loop was never refused")
	}

	// ...and the terminal beside it, on the same address, is not.
	if code := f.poll(t, tokenB).Code; code != http.StatusOK {
		t.Fatalf("a second terminal's poll = %d, want 200", code)
	}
}
