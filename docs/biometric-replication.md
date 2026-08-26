# Single-enrolment biometric replication: implementation design

**Status:** M1 (the server half) is **implemented and tested** — see §11. M2
(firmware) is not started and needs one unit. Cross-module compatibility needs
two units and **has not been tested** — see §9.

**Goal.** A person presents a finger **once**, at one door, and is recognised at
every door their permissions admit them to. This is Option A of
`gym-access-terminal/docs/biometric-architecture.md` §5.

**Explicitly not the goal.** Option B — re-enrolling the same person at each
door, with the platform keeping a work list — is what ships today
(`database/device_credentials.go`, `include/credential_worklist.h`). It stays,
as the fallback for any terminal that cannot replicate (§4). Nothing in this
design removes it, and nothing in it is built around it.

---

## 1. What already exists, and must not be rebuilt

Most of this feature was designed two migrations ago and then left unbuilt
because the driver could not import a template. The parts that are already
right:

| Thing | Where | State |
|---|---|---|
| `credentials` with `sealed_material`, `sealed_key_id`, `sealed_algorithm`, `material_digest` | `migrations/012` | built, unused |
| all-three-or-none sealing constraint | `migrations/012` | built |
| `credential_placements` state machine (`PENDING/PLACED/FAILED/REMOVING/REMOVED`) | `migrations/012` | built and **in use** |
| one-slot-one-credential and one-placement-per-(credential,device) indexes | `migrations/012` | built |
| `generation`, so a placement written before a sensor wipe is distinguishable | `migrations/020` | built and in use |
| `SealedCredentialMaterial`, `SealingAlgorithms = {AES-256-GCM}` | `models/identity.go` | types only, no writer |
| `TemplateFormatVendorTemplate = "VENDOR_TEMPLATE"` | `models/identity.go` | declared, nothing produces it |
| `devices.capabilities` JSONB, merged on heartbeat, NULL is not `[]` | `migrations/025` | built and in use |
| device-authenticated placement report, idempotent on (credential, device) | `database/device_credentials.go` | built and in use |
| roster predicate shared by the access and enrolment surfaces | `database/roster.go` | built and in use |
| `CredentialRef` with a `format` field | `include/credential_ref.h` | built |

**So the schema work is nearly done.** This design adds one migration, and it
adds only things that genuinely do not exist: a key to seal with, a statement of
which sensor a template came off, and the bookkeeping for a transfer.

### The three real gaps

1. **No key.** Nothing anywhere generates, stores or delivers a sealing key.
   `sealed_key_id` names a key that has never existed. §3.
2. **No transfer path in firmware.** The fitted Adafruit driver implements
   neither half properly — `getModel()` sends `UpChar` and never reads the data
   packets back, and `DownChar` (0x09) is not defined at all. §8.
3. **No route for material to travel.** `GET /devices/credentials/pending`
   carries names, deliberately and loudly. §6.

---

## 2. One correction to make first

`migrations/012` says the template is *"encrypted by the enrolling terminal
under a key the server never holds"*, and `models/identity.go` repeats it. **The
design below does not satisfy that sentence, and no design that meets the
deadline will.** A key the server has never held cannot be handed to a terminal
adopted next month without a terminal-to-terminal rendezvous this product has no
mechanism for.

What is achievable now, and what §3 delivers:

> The database alone yields no biometric material. Recovering a template
> requires the database **and** the deployment master key, which lives outside
> it.

That is still exactly the threat the migration set out to defeat — a backup, a
replica, a support engineer with `SELECT`, an injection on a query touching
`credentials`. It is a smaller claim than the one written down, and the comment
must be corrected to it in the same commit that adds the key. This codebase's
comments are load-bearing; leaving a stronger claim standing than the code
delivers is the one outcome worse than the weaker guarantee.

The stronger property (per-terminal public keys, the server as a blind relay,
a re-wrap job when a new terminal joins) is a clean later step —
`sealed_key_id` already names the key, so nothing about it is foreclosed. It is
not MVP.

---

## 3. Key management

**Per-company sealing key. AES-256-GCM, nonce prepended.** This is what
`models/identity.go` already committed to, so no constant changes.

* Generated server-side from `crypto/rand` on first need, 32 bytes.
* Stored **wrapped** under a deployment master key read from the environment
  (`SEALING_MASTER_KEY`, 32 bytes base64) — never in the clear, and never
  hashed, because unlike a site key or a device credential this one has to be
  recoverable.
* `master_key_id` recorded per row, so the master key can rotate without every
  sealed credential becoming unreadable.
* Delivered to a terminal **once**, in the credential-collection response, over
  the same TLS handover that already delivers the device API key
  (`handlers/announcements.go`). The terminal stores it in NVS beside the device
  credential.
* Never returned to the console. Never in an audit payload. Never logged.

Startup refuses to boot if `SEALING_MASTER_KEY` is absent *and* any sealing key
row exists — silently losing the ability to unwrap is worse than not starting.

Two deliberate narrowings of that guard, both in `main.go`:

* **A missing `company_sealing_keys` table is not an error.** Migrations are
  applied separately from deploying the binary, so a build carrying 026 can
  legitimately start against a database still on 025. Without the guard the
  check would ask a question Postgres cannot parse and take the whole API down
  over a deploy-ordering detail, for a feature the installation has not enabled.
* **A query failure is a warning, not a refusal.** This check exists to catch a
  misconfiguration; it is not a database health check, and a diagnostic that can
  itself cause an outage is worse than the fault it looks for.

**Residual risk, stated rather than mitigated:** an attacker with physical
possession of a terminal reads the company key out of NVS on a part with no
flash encryption, and can then decrypt any material they can also fetch. This is
the same exposure `migrations/012` already records, flash encryption is separate
tracked work, and this feature's strength depends on it.

---

## 4. Capability gating — two capabilities, not one

Export and import are separate driver features that fail separately and are
tested separately, so they are two strings (`models/models.go`, beside the
existing three):

```go
CapabilityBiometricExport = "biometric_export" // can read a template off its sensor and seal it
CapabilityBiometricImport = "biometric_import" // can unseal and write a template to its sensor
```

Firmware advertises each **only in a build where that half is compiled and has
passed §9 on the module fitted.** A capability is a statement of tested fact,
which is the whole reason `migrations/025` exists instead of a version check.

The three-valued rule from 025 holds and matters here: `NULL` means *has never
reported*, and **is not** *cannot replicate* — it is a terminal that has not
spoken yet. Fan-out treats `NULL` as "not a target" and re-evaluates on the next
heartbeat. Fail closed.

A terminal advertising neither keeps today's behaviour exactly: it appears in
the pending work list and somebody walks over and enrols the person. **That is
the compatibility story for every unit already in the field, and it needs no
firmware change.**

---

## 5. Sensor compatibility — the Module B question

A ZFM template is a proprietary feature vector. Whether one exported from the
module fitted today imports and *matches* on a second module is **a hardware
fact that has not been established.** It is not established by the protocol
being symmetric, and it is not established by both parts being R307-class.

So compatibility is not asserted anywhere. It is **enforced by equality**:

* Each terminal reports a `sensor_profile` — a short string the firmware
  composes from its build-time vendor constant and what `getParameters()`
  actually answers: `vendor:system_id:capacity`, e.g. `ZFM:0x0009:1000`.
* `credentials.sensor_profile` records the profile of the module the template
  came off.
* **A placement is only created, and material only served, when the target's
  profile is byte-equal to the source's.** No table of "these are probably
  compatible". No inference from the vendor string alone.

Consequences, all of them intended:

* If Module B reports an identical profile, replication to it works the moment
  it is claimed — but that is still not proof it *matches*, which is why HV-4
  in §9 exists and must pass before the pairing is relied on in production.
* If Module B reports a different profile, it receives **no material at all**
  and falls back to the §4 work list. Nothing breaks, nobody is locked out, and
  no untested claim has been made.
* Widening it later is one tested row in an allow-list table, added only after
  HV-4 passes for that specific pair. The MVP does not build that table.

`sensor_profile` NULL — never reported — is never a target. Fail closed.

---

## 6. Wire protocol

### 6.1 The existing pending endpoint stays material-free

`GET /api/v1/devices/credentials/pending` keeps its guarantee. Its SELECT list
is a security boundary a reviewer can check by reading it, and
`device_credentials_test.go` asserts it. **It is extended additively only:**

```json
{ "member_id": "MEM001", "credential_type": "FINGERPRINT",
  "template_format": "VENDOR_TEMPLATE", "material_available": true,
  "attempts": 0, "state": "PENDING" }
```

`material_available` is a boolean. It is not material, and there is still no
field in this response that could hold any.

### 6.2 Upload — `POST /api/v1/devices/credentials/material`

Device-authenticated. Called by the **enrolling** terminal after it has reported
the placement, so the existing placement path is untouched.

```json
{ "member_id": "MEM001", "credential_type": "FINGERPRINT",
  "vendor": "ZFM", "template_format": "VENDOR_TEMPLATE",
  "sensor_profile": "ZFM:0x0009:1000",
  "sealed": { "ciphertext": "<base64>", "key_id": "ck_7f3a...",
              "algorithm": "AES-256-GCM", "digest": "<64 hex>" } }
```

The server refuses unless: the algorithm is in `SealingAlgorithms`; `key_id` is
this company's **ACTIVE** key; the digest is 64 lowercase hex; the ciphertext is
within a hard bound (**2 KiB** — a ZFM template is ~512 B plus nonce and tag, so
this is generous and still refuses anything trying to use the column as
storage); the person resolves inside the **authenticated device's own company**;
and the device advertises `biometric_export`.

On success: writes the four sealing columns and `sensor_profile`, sets
`template_format = VENDOR_TEMPLATE`, and runs fan-out (§7). Idempotent per
credential: re-uploading the same digest is a no-op returning 200, because a
terminal that never heard the response will retry.

### 6.3 Fetch — `GET /api/v1/devices/credentials/:credential_id/material`

Device-authenticated. **This is the only route by which material leaves the
platform**, which is the entire reason it is its own endpoint rather than a
field on the pending list: one route to audit, one route to rate-limit, one
route to test.

Served **only** when all of these hold:

1. a `credential_placements` row exists for (credential, **authenticated
   device**) in `PENDING` or `FAILED` — so a terminal can only fetch what it has
   been *told to hold*, never browse;
2. the device advertises `biometric_import`;
3. `devices.sensor_profile` is byte-equal to `credentials.sensor_profile`;
4. the credential is `ACTIVE` or `PENDING`, not `SUSPENDED` or `REVOKED`;
5. the roster rule admits that person at that device.

Anything else is `404`. A terminal learns nothing about credentials it is not
owed. Every fetch writes an audit row (`migrations/013`) naming device,
credential and outcome.

### 6.4 Apply — the existing placement report, unchanged

The importing terminal reports `PLACED` with its slot through
`POST /api/v1/devices/credentials/placement`, exactly as an enrolling terminal
does today, plus one new optional field `applied_digest`. The server stores it
on the placement and flags a mismatch against `credentials.material_digest`
rather than trusting it silently.

**The server cannot verify that digest itself** — it cannot decrypt. It is a
device-to-device integrity check that the platform records and compares. Said
plainly here because a reader will otherwise assume the server validated it.

---

## 7. Fan-out

On material upload, for every device in the company that is **not** the source:

```
device advertises biometric_import
AND devices.sensor_profile = credentials.sensor_profile   (byte-equal, §5)
AND rosterMembershipPredicate admits this person at this device
AND no placement exists in (PLACED, REMOVING, REMOVED)
  -> INSERT credential_placements (state PENDING,
                                   generation = devices.placement_generation,
                                   source_device_id = the uploading device)
```

The roster predicate is **reused from `database/roster.go`, not restated** — the
same rule the pending list already uses, so the replication surface is exactly
as narrow as the access surface, and a reception-desk terminal is not handed
material for staff who have no business there.

Re-run on the triggers that already change the roster: a device claimed, a
permission grant changed, a person reactivated. A daily reconcile sweep catches
drift. Convergent by construction — the placement table *is* the state, and
re-running fan-out is idempotent.

Revocation reuses what exists: `credentials.status = REVOKED` drives live
placements to `REMOVING`; the terminal erases the slot and reports `REMOVED`.
`database/terminals.go` already writes both transitions.

---

## 8. Firmware work

New, all built on the **public** driver API — `writeStructuredPacket()` and
`getStructuredPacket()` are public in the fitted version, so **the Adafruit
library needs no fork and no patch**:

| File | Contents |
|---|---|
| `include/zfm_transfer.h`, `src/zfm_transfer.cpp` | `exportTemplate(slot)` = `loadModel()` + `getModel()` + a `getStructuredPacket()` loop reassembling ~512 B. `importTemplate(bytes)` = `DownChar` (0x09, defined locally) + a `writeStructuredPacket()` loop + `storeModel(slot)`. ~150 lines, as `biometric-architecture.md` §5 estimated. |
| `include/credential_seal.h` | Seal/unseal, plus SHA-256 of the plaintext. A **seam**, like `TemplateLibrary` and `KeyValueStore`, so the policy is host-testable without a board; the ESP32 implementation is `mbedtls_gcm`. |
| `include/company_key.h` | The company sealing key in NVS, same record discipline as `device_credential.h`. Collected at handover. |
| `include/credential_worklist.h` (edit) | Carry `material_available`; drive the fetch → unseal → verify digest → import → report loop. |

Slot allocation stays where it belongs: the importing terminal picks the lowest
free slot via `TemplateLibrary::slotState()` and reports which one it used.
`kUnknown` is **not** a free slot — a comms fault must never read as "nothing
there", the rule §4 of the architecture doc already established.

**A full sensor is a normal outcome, not an error.** A 64-slot module in a
300-member company fills up; the placement goes `FAILED` with `last_error =
"sensor full"`, which is what an operator reads. Already modelled.

### One thing that must not be repeated

The material fetch **must** hold an in-flight guard from
`include/in_flight_marker.h`, with a timeout release, exactly as the enrolment
upload path does. The access-log path asserted the same invariant in its comment
and implemented no guard, and it turned 2 events into 54 POSTs. This path has
the same shape — a slow request, a loop task that re-offers work every pass —
and it will fail the same way if the guard is left out.

### Cost, at the measured per-request price

One fetch per (credential, terminal), ~700 B base64 on the wire. At the measured
7.4–8.2 s per HTTPS operation with no connection reuse, seeding a 100-member
company onto a third terminal is ~100 requests, i.e. **13–14 minutes of
background sync** — spread across cycles, not blocking the door, since access is
entirely local. Acceptable, but it is why the fetch is one credential per pass
behind an in-flight guard rather than a batch that monopolises the network task.

---

## 9. Hardware validation — **PENDING, NOT STARTED**

**No claim in this document about what the sensor will do has been tested.**
Every statement about `DownChar` comes from the ZFM command set and the driver
source, as §2 of the architecture doc already warned.

| # | Test | Needs | Status |
|---|---|---|---|
| HV-1 | Export a template off a live module; get a plausible ~512 B | 1 unit | **PENDING** |
| HV-2 | Import it back to the **same** module in a different slot; the finger matches there | 1 unit | **PENDING** |
| HV-3 | Export from unit 1, import to unit 2 (**same** module type); the finger matches on unit 2 | 2 units | **PENDING** |
| HV-4 | **Export from Module A, import to Module B; the finger matches on B** | **second module — NOT YET DELIVERED** | **PENDING — BLOCKED ON HARDWARE** |
| HV-5 | Digest of the imported plaintext equals the digest of the exported plaintext | 1 unit | **PENDING** |
| HV-6 | Company key survives reboot and OTA; material fetch still unseals | 1 unit, **clean NVS** | **PENDING** |

**HV-4 is the Module B compatibility item.** Until it passes, the §5 equality
gate means Module B either reports a matching profile (still unproven, so it
must not be relied on in production) or reports a different one and quietly
falls back to the work list. Either way the product does not lie.

**HV-6 must not be run on the bench unit.** `AT-E05A1B38AA38` has a structurally
damaged NVS partition — illegal page state words, orphan entries — so a
key-persistence result measured on it means nothing. It accepts writes, so it is
fine for HV-1 to HV-5.

---

## 10. Migration 026 — the whole schema delta

```sql
BEGIN;

CREATE TABLE IF NOT EXISTS company_sealing_keys (
    id            BIGSERIAL PRIMARY KEY,
    public_id     UUID NOT NULL DEFAULT gen_random_uuid(),
    company_id    BIGINT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
    key_id        VARCHAR(64) NOT NULL,   -- lands in credentials.sealed_key_id
    wrapped_key   BYTEA NOT NULL,         -- AES-256-GCM under the master key
    master_key_id VARCHAR(32) NOT NULL,   -- so the master key can rotate
    algorithm     VARCHAR(32) NOT NULL DEFAULT 'AES-256-GCM',
    status        VARCHAR(20) NOT NULL DEFAULT 'ACTIVE',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    retired_at    TIMESTAMPTZ,
    CONSTRAINT company_sealing_keys_status_check
        CHECK (status IN ('ACTIVE','RETIRED')),
    CONSTRAINT company_sealing_keys_retirement_check
        CHECK ((status = 'RETIRED') = (retired_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS company_sealing_keys_company_key_id_key
    ON company_sealing_keys(company_id, key_id);
-- Exactly one key may be sealed WITH at a time. Retired keys stay readable.
CREATE UNIQUE INDEX IF NOT EXISTS company_sealing_keys_one_active
    ON company_sealing_keys(company_id) WHERE status = 'ACTIVE';

-- Which module a template came off, and which module a terminal has fitted.
-- Equality of these two strings is the ONLY compatibility rule (§5).
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS sensor_profile VARCHAR(64);
ALTER TABLE devices     ADD COLUMN IF NOT EXISTS sensor_profile VARCHAR(64);
ALTER TABLE devices     ADD COLUMN IF NOT EXISTS sensor_profile_reported_at TIMESTAMPTZ;

-- What the receiving terminal says it actually wrote. The server records and
-- compares it; it CANNOT verify it, having no key.
ALTER TABLE credential_placements
    ADD COLUMN IF NOT EXISTS applied_digest CHAR(64);
ALTER TABLE credential_placements
    DROP CONSTRAINT IF EXISTS credential_placements_applied_digest_check;
ALTER TABLE credential_placements ADD CONSTRAINT credential_placements_applied_digest_check
    CHECK (applied_digest IS NULL OR applied_digest ~ '^[0-9a-f]{64}$');

-- Which device's material fed this placement. Audit, and the evidence trail
-- HV-3 and HV-4 are read against.
ALTER TABLE credential_placements
    ADD COLUMN IF NOT EXISTS source_device_id BIGINT REFERENCES devices(id) ON DELETE SET NULL;

-- The fan-out read: "who else should hold this, on a matching module".
CREATE INDEX IF NOT EXISTS idx_devices_sensor_profile
    ON devices(sensor_profile) WHERE deleted_at IS NULL;

COMMIT;
```

`credentials_substance_check` from `migrations/020` needs no change:
a `VENDOR_TEMPLATE` credential carries `sealed_material`, which the check
already accepts.

---

## 11. Sequencing

**M1 — server (no hardware). BUILT.** Migration 026; key generation, wrapping
and delivery at handover; both material endpoints; fan-out; capability and
profile gating; the §2 comment correction. Go tests throughout
(`biometric_replication_test.go`).

Three decisions were taken during implementation that this document did not
specify, all recorded where the code makes them:

* **A second, different template for a credential that already has one is
  refused (409), not overwritten.** Overwriting would leave terminals that
  already placed the first template holding one finger and every terminal placed
  afterwards holding another — silently, and presenting as the exact "works at
  some doors and not others" complaint this feature exists to end. Re-enrolling
  somebody is revoking the credential and creating a new one. A retry of the
  *same* material is idempotent and returns 200.
* **Two capabilities rather than one** — `biometric_export` and
  `biometric_import`. They are separate driver features that fail separately and
  are verified separately, and the fitted driver has neither half working, so a
  build can plausibly ship with one and not the other.
* **Key delivery at collection is best-effort; claiming never fails over it.** A
  deployment with no `SEALING_MASTER_KEY` still puts terminals on walls — they
  collect no sealing key, cannot replicate, and behave exactly as the fleet
  already in the field does. The loud version of that misconfiguration is the
  startup guard, which fires only when this installation *holds* sealing keys and
  cannot read them, and which tolerates the table not existing yet so that a
  binary deployed ahead of its migration does not refuse to boot.

**M2 — firmware (one unit).** `zfm_transfer`, `credential_seal`, `company_key`,
the worklist fetch loop with its in-flight guard, capability reporting,
`sensor_profile` reporting. Host tests against a fake sensor.

**M3 — hardware validation.** HV-1, HV-2, HV-5, HV-6 on one unit. HV-3 on two.
**HV-4 when the second module arrives.**

Ship M1 and M2 with the capabilities advertised only after HV-1 to HV-3 pass. A
fleet running M1 with no M2 firmware behaves exactly as it does today.

---

## 12. Go test plan (M1)

`biometric_replication_test.go`, matching the existing per-feature file style:

* material upload refuses an unknown algorithm, a retired `key_id`, a malformed
  digest, and a ciphertext over 2 KiB;
* upload is idempotent on a re-send of the same digest;
* a device cannot upload material for another company's person;
* fetch returns 404 with no placement, with a `REMOVING` or `REMOVED` placement,
  without `biometric_import`, and on a mismatched `sensor_profile`;
* fetch returns 404 for a `SUSPENDED` and for a `REVOKED` credential;
* fan-out creates placements only on capable, profile-matching, roster-admitted
  devices, and never on the source;
* fan-out with `capabilities IS NULL` creates nothing (fail closed);
* `GET /credentials/pending` still returns no field carrying material — extend
  the existing assertion rather than writing a second one;
* revocation drives live placements to `REMOVING`;
* boot refuses when `SEALING_MASTER_KEY` is absent and sealing keys exist.

---

## 13. What this design does not claim

* **That a template exported from one module imports and matches on another.**
  Untested. HV-3 and HV-4 are the only things that can establish it.
* **That Module B is compatible.** Not tested, not asserted, and not reachable
  in code until HV-4 passes.
* **That the server cannot decrypt.** It can, given the master key. §2.
* **That material is safe against physical possession of a terminal.** It is
  not, until flash encryption ships.
* **That the digest is server-verified.** It is device-computed,
  server-recorded and server-compared. Nothing more.
* **Cross-vendor portability.** Out of scope entirely; a ZFM template is not
  ISO/IEC 19794-2 and never will be. That remains Option C.
