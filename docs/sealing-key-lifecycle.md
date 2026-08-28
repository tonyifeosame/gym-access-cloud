# The sealing key: lifecycle and threat model

**Status: IMPLEMENTED in migration 026 and `database/sealing_keys.go`.** This
document was written as the gate before that work and is kept as the reasoning
behind it, so §8 still reads as a list of open decisions -- see "What was
decided" below for which of them the implementation actually took, and which are
still open.

It states exactly what the key is, every state it passes through, who can obtain
it, and what an attacker gets at each position. Read it before changing anything
about `company_sealing_keys`.

## What was decided

| §8 decision | Outcome |
|---|---|
| 1. Key scope | **Per company**, as recommended. One `ACTIVE` key per company, enforced by a partial unique index rather than by the application. |
| 2. Where `SEALING_MASTER_KEY` lives | **Still open**, and it is a deployment choice rather than a code one. The server reads it from the environment and refuses to start if this installation holds sealing keys it cannot unwrap. |
| 3. Accepting T7 | **Accepted for M1.** There is no rotation endpoint: a lost or stolen terminal permanently compromises its company's material, and §3 L5 and L7 are the record of what that means. |
| 4. Correcting the `012` and `models/identity.go` comments | **Partly.** `models/identity.go` states the weaker, true claim of §5. `012` is left exactly as applied -- its checksum is in `schema_migrations`, and editing it stops `deploy/migrate.sh` applying anything. The correction lives in `026` instead. |

§3 L6 asks for the startup refusal to be asserted in
`production_config_test.go`, and it is:
`TestStartupRefusesOnlyWhenSealingKeysAreUnreadable` covers all four
combinations of master key and stored keys. Three of them must NOT refuse --
the dangerous mistake there is not missing the broken state but catching a good
one, because a server that refuses to boot on an installation which has simply
not turned this feature on is a self-inflicted outage of the whole API.

---

## 0. The one paragraph version

A 32-byte per-company key is generated on the server, stored wrapped under a
master key held in the environment, and handed to each terminal exactly once
when it collects its device credential. Terminals seal and unseal templates with
it. **The server never encrypts or decrypts biometric material** — it unwraps
the key only to hand it to a terminal, and the material endpoints are byte pipes
that touch no crypto at all. A database compromise yields nothing. A compromise
of the running server yields everything. A stolen terminal yields everything,
permanently, and M1 has no remedy for it.

---

## 1. What the key is

| | |
|---|---|
| Scope | one per **company** (see §8, decision 1) |
| Material | 32 bytes from `crypto/rand` |
| Algorithm | AES-256-GCM, 12-byte random nonce **prepended**, 16-byte tag appended |
| `key_id` | `ck_` + 12 hex, e.g. `ck_7f3a91c04e2b`. **Not secret.** A label, stored in `credentials.sealed_key_id`, so material can be matched to the key that sealed it |
| Additional authenticated data | `company_id \| member_id \| key_id`, ASCII, pipe-separated |
| Wrapping | AES-256-GCM under `SEALING_MASTER_KEY`, AAD `company_id \| key_id` |

### Why there is AAD on the seal

Without it, anyone who can write to the `credentials` table can move person A's
ciphertext onto person B's row, and the next terminal to import it enrols A's
finger under B's identity — a privilege escalation achieved with no key at all.
Binding the ciphertext to `company_id | member_id | key_id` makes that
transplant fail to unseal on the receiving terminal.

**The server cannot check this binding** — it has no key on the material path.
It is enforced by the importing terminal, which knows which member it is
importing for because the pending list told it. Stated here because it is the
kind of guarantee a reader will otherwise assume the server provides.

### Why there is AAD on the wrap

The same transplant one level up: it stops a wrapped key row being copied from
one company to another.

### Nonce discipline

A random 12-byte nonce per seal. The birthday bound for GCM nonce reuse is
around 2^32 seals under one key; a company would need billions of enrolments to
approach it. Not a practical concern at this scale, recorded so nobody has to
re-derive it.

---

## 2. Where the key is used — and where it is not

This is the most load-bearing property of the design, so it is stated before the
lifecycle:

| Path | Touches the key? |
|---|---|
| `POST /devices/credentials/material` (upload) | **No.** Validates shape, stores opaque bytes |
| `GET /devices/credentials/:id/material` (fetch) | **No.** Reads opaque bytes, checks authorisation |
| Fan-out | **No** |
| Console, audit, metrics, logs | **No.** Never, in any form |
| Terminal collects its credential | **Yes** — unwrapped in memory, put in the response, discarded |
| Terminal seals / unseals a template | **Yes**, on the device |

So `SEALING_MASTER_KEY` is used **once per terminal claim** and never on the
material path. The blast radius of a bug in the material endpoints is bounded to
routing ciphertext incorrectly, which the authorisation rules in
`biometric-replication.md` §6.3 already govern. No material endpoint can leak a
key, because no material endpoint has one.

---

## 3. Lifecycle

### L1 — Creation: lazy, at first collection

A company gets a key the first time a terminal of that company **collects** its
device credential, in the same transaction. Not at company creation: a company
with no terminals needs no key, and a key that exists before anything can use it
is a secret sitting in a database for no reason.

### L2 — Storage at rest: wrapped, never plaintext, never hashed

`company_sealing_keys.wrapped_key`. Unlike a site key or a device credential,
this one **cannot be hashed** — it has to be recoverable to be delivered.

That is a real departure from this schema's discipline, and
`handlers/announcements.go:346` names the discipline explicitly: *"A key minted
here would have to be STORED in plaintext until the device arrived, which is the
one thing every other secret in this schema is careful never to do."* The
sealing key is the first secret in this system that must be stored recoverably.
Wrapping under a key held outside the database is the mitigation, and §8
decision 2 is where that key lives.

### L3 — Delivery: once, with the device credential

In `models.AnnounceStatusResponse`, on the APPROVED → COLLECTED transition,
beside the `api_key` that already rides there
(`handlers/announcements.go:161-166`). Base64.

It inherits every property that response already has: one shot, gone once
collected, and **not written to the audit record** — the existing audit block
carries site, device name and job count, under a comment reading `THE KEY IS NOT
HERE and must never be`. The sealing key is not there either.

**Failure mode worth naming:** if the terminal receives the response but fails to
persist the key — an NVS write failure, which is not hypothetical on a unit with
a damaged partition — it can never obtain it again. It can still open doors; it
cannot seal or unseal. Recovery is a re-claim. The firmware must treat a failed
key write the same way it treats a failed credential write.

### L4 — Use on the device

NVS, beside the device credential, same record discipline. Read on enrol (seal)
and on import (unseal). Never printed to serial, never in a console command,
never in a log line.

### L5 — Rotation: **NOT IN M1**

The schema supports it — `status`, `retired_at`, and a partial unique index
allowing exactly one `ACTIVE` key per company while retired keys stay
unwrappable. **There is no rotation endpoint in M1 and no firmware support for
holding more than one key.**

This is a deliberate scope cut and it has a consequence that must be accepted
rather than discovered: **in M1 there is no way to respond to a suspected key
compromise.** See §8 decision 3.

### L6 — Loss of the master key

Wrapped keys become unrecoverable. Terminals already holding the key keep
working, because their copy is in NVS. What stops is everything that needs the
server's copy: no newly claimed terminal can ever be given it, so replication to
new hardware stops permanently, and stored material becomes dead weight.
Recovery is a new key plus re-enrolling every person.

Because that damage is silent and permanent, **startup refuses to boot when
`SEALING_MASTER_KEY` is absent and any `company_sealing_keys` row exists.** A
loud failure at deploy time is strictly better than a fleet that quietly stops
replicating. Asserted by `TestStartupRefusesOnlyWhenSealingKeysAreUnreadable`
in `production_config_test.go`, over all four combinations of master key and
stored keys -- including the three that must NOT refuse.

### L7 — Terminal decommission, loss or theft

**The key stays on the unit's NVS. There is no remote wipe, and M1 has no
rotation.** A terminal removed from a wall and taken away holds a key that
decrypts every template in the company, for ever.

This is the worst property of per-company keying and the main reason §8 decision
1 exists.

---

## 4. Threat model

"Defeated" below means the attacker obtains **no biometric plaintext**.

| # | Adversary and capability | Outcome | Status |
|---|---|---|---|
| T1 | Database backup, read replica, `SELECT` by a support engineer, SQL injection on any query touching `credentials` | Ciphertext and wrapped keys. No master key, so nothing decrypts | **Defeated** |
| T2 | Console operator, any role including ADMIN | Nothing. `models.Credential` has no ciphertext field at all — it carries `HasMaterial bool`. Defeated by the type, not by a permission check | **Defeated** |
| T3 | Cross-tenant: a terminal or operator of company X reaching company Y's material | Nothing. Every lookup is scoped to the authenticated device's own company, and fan-out reuses `rosterMembershipPredicate` | **Defeated, and tested** |
| T4 | Network attacker between terminal and API | Nothing. TLS; material never appears in a URL, a log line, an audit payload or a metric | **Defeated** |
| T5 | Stolen **device API key** only, without the terminal itself | Ciphertext for credentials that device has a `PENDING`/`FAILED` placement for — its own roster, and only if it also presents a matching `sensor_profile` and the `biometric_import` capability. No company key, so no plaintext | **Defeated** (bounded ciphertext exposure only) |
| T6 | **Physical possession of a terminal** | Company key read from NVS on a part with no flash encryption, plus its API key. Decrypts everything it can fetch, which is its roster | **NOT defeated.** Pre-existing, recorded in `migrations/012`, depends on flash encryption |
| T7 | **A decommissioned or stolen terminal, later** | As T6, permanently, with no revocation available in M1 | **NOT defeated.** §8 decision 3 |
| T8 | Full compromise of the running server (process memory or environment, plus the database) | Master key unwraps every company key; every template decrypts | **NOT defeated.** Inherent to any scheme where the server can hand keys out |
| T9 | Compromised terminal uploading **forged** material for a roster member | Binds attacker-chosen bytes to that person's credential | **NOT defeated — and this is the one genuinely new risk.** See below |
| T10 | Offline confirmation against `material_digest` | An attacker holding a *candidate* template can confirm whether it is the stored one. Cannot recover a template from the digest, and the digest is not portable across template formats | **Accepted**, recorded in `migrations/012` |

### T9 deserves its own paragraph

A compromised terminal can already enrol an arbitrary finger against a roster
member today — it is the enrolment device, and that authority is inherent to
being one. **Replication does not widen that authority. It widens the blast
radius:** a forged enrolment used to open one door, and after this feature it
propagates to every door that person is admitted to.

M1 bounds it — device authentication, the person must be inside that device's
roster, every upload and fetch is audited, and `applied_digest` records what
each receiving terminal says it actually wrote, so a mismatch is visible. It
does not eliminate it. Eliminating it needs operator confirmation of enrolments
before fan-out, which is a product decision, not a cryptographic one, and is not
in M1.

---

## 5. What the server can and cannot do — plainly

**Can:** decrypt any template, given the master key it holds in its environment.

**Cannot:** decrypt anything from the database alone; verify a `material_digest`;
verify the AAD binding; produce material for a terminal that has no placement
for it.

`migrations/012` and `models/identity.go` say the material is sealed *"under a
key the server never holds"*. **That sentence is false under this design.** The
weaker and true statement is: the database alone yields nothing; the database
plus the master key yields everything.

`models/identity.go` was corrected. **`migrations/012` was not, and deliberately
so.** It is applied in production and its checksum is recorded in
`schema_migrations`; `deploy/migrate.sh` hashes the whole file and cannot tell a
comment from a statement, so editing it makes the runner refuse to apply
anything -- including 026. An applied migration is a record of what was run, not
a document to keep current. The correction therefore lives in `026`, which is
the migration that makes the claim false, in `models/identity.go`, and here.

---

## 6. What M1 does not include

* Key rotation, in either the API or the firmware.
* Any remote wipe or revocation of a key on a lost terminal.
* Per-terminal key wrapping (the design that would remove T7 and T8).
* Flash encryption on the ESP32 (separate tracked work; T6 depends on it).
* Operator confirmation of enrolments before fan-out (T9).
* Any claim whatsoever about Module B. The key design is independent of the
  sensor; compatibility is gated by byte-equal `sensor_profile` and remains
  **untested**.

---

## 7. Alternatives, and why they are not the M1 recommendation

**Per-site key.** Blast radius of T6/T7 drops from a company to one site. Costs
cross-site replication: a person admitted at two sites needs two enrolments,
because the two sites cannot read each other's material. Cheap to build —
`site_id` instead of `company_id`.

**Per-terminal wrapping, server as a blind relay.** Each terminal holds an
X25519 keypair and publishes only its public key. Material is sealed under a
random content key; the content key is wrapped once per recipient terminal.
**The server holds no key at all, so T8 collapses to ciphertext and T7 is
bounded to what that one terminal already held.** There is no master key and no
`SEALING_MASTER_KEY` to lose.

The cost is real: a terminal claimed later needs its own wrapped copy, which
requires asking a terminal that already holds the credential to re-wrap it — a
new job type, a new firmware path, and a donor terminal that has to be online.
This is the correct long-term design and it is not a deadline-sized piece of
work. Nothing here forecloses it: `sealed_key_id` already names the key, and
per-placement wrapped keys are an added column, not a migration rewrite.

---

## 8. Decisions needed before implementation

1. **Key scope** — per company (recommended for M1), per site (smaller blast
   radius, costs cross-site replication), or per terminal (removes T7/T8, not
   deadline-sized).
2. **Where `SEALING_MASTER_KEY` lives** — a Render environment variable
   (simplest, visible in the dashboard to anyone with access to it), a Render
   secret file, or an external KMS (strongest, adds a runtime dependency and
   deploy complexity).
3. **Accepting T7** — that in M1 a lost or stolen terminal permanently
   compromises its scope's material, with no rotation available. If this is not
   acceptable, a rotation endpoint has to come into M1 scope, and firmware has
   to be able to hold more than one key.
4. **Correcting the `migrations/012` and `models/identity.go` comments** to the
   true, weaker claim (§5). Only `models/identity.go` could be corrected -- see
   §5 for why 012 must be left exactly as it was applied.
