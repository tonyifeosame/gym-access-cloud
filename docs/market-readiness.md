# AccessLink market readiness

The tracked remediation register for the full-product audit of 2026-08-14.

**This document does not certify anything.** It records what was found, why it
was wrong, what was done about it, and what proves the fix. A finding is only
`FIXED` when a test that would have caught it exists and passes. Everything else
carries the status it actually has.

AccessLink is a **general-purpose identity, biometric, terminal and workflow
platform**. It is sold to different companies for different purposes. Nothing in
this document, and nothing in the remediation it tracks, may assume a gym, a
school, an office, a factory, a hotel, a warehouse or a residential building is
the customer.

---

## Status vocabulary

| Status | Meaning |
|---|---|
| `FIXED` | Implemented, and a test that would have caught the original defect passes. |
| `PARTIAL` | Materially improved, with a named remainder. The remainder is a finding in its own right. |
| `ACCEPTED` | Not fixed, deliberately, with the risk stated and owned. |
| `BLOCKED` | Cannot be fixed here — needs hardware, a legal decision, or a commercial decision. |
| `OPEN` | Not yet started. |

## Severity vocabulary

| Severity | Meaning |
|---|---|
| `BLOCKER` | Cannot be sold with this open. |
| `CRITICAL` | Ships broken or unsafe in a way customers will hit. |
| `MAJOR` | Real customer-visible defect or risk. Should not launch with it. |
| `MINOR` | Worth fixing; tolerable at launch if stated. |
| `NICE-TO-HAVE` | Improvement, not a gap. |

---

## Subsystem grouping

The 57 findings, grouped by the subsystem that owns the fix. A finding appears
once, under the subsystem where the work actually lands.

| Subsystem | Findings |
|---|---|
| Tenancy / company | GP-01, CON-01 |
| Authentication / authorization | SEC-09, SEC-10, SEC-13, SEC-14 |
| Operators | PPL-02, PPL-06 |
| Sites | CON-03 |
| Terminals | SEC-01, SEC-05, SEC-06, CON-02, SYN-04 |
| People | GP-02, PPL-03, PPL-04, PPL-05 |
| Credentials / biometrics | FW-03, HW-03, PPL-01 |
| Applications | APP-01, APP-03, GP-03, GP-05, GP-06 |
| Access control | APP-02 |
| Attendance / workflows | APP-04 |
| Synchronization | SEC-04, SYN-01, SYN-02, SYN-03, SYN-05, SYN-06, SYN-07 |
| Firmware | FW-01, FW-02, FW-04, FW-05, FW-06, FW-07, FW-09, DOC-04 |
| Hardware integration | SEC-03, HW-01, HW-02 |
| Settings | FW-08, GP-04 |
| Audit / activity | SEC-07, SEC-08, OPS-06 |
| Deployment | OPS-02, OPS-04, OPS-05, SEC-15 |
| CI/CD | OPS-03 |
| Security | SEC-02, SEC-11, SEC-12, CON-04 |
| Documentation | DOC-01, DOC-02, DOC-03, CON-05, LEG-01 |

---

## Dependency order

Findings are not independent. This is the order the work has to happen in,
derived from what each fix needs to already exist.

```
                    ┌─────────────────────────────────────────┐
   FOUNDATION       │  Platform primitives schema             │
   (nothing else    │  credentials · events · permissions     │
    can land first) │  schedules · audit · person taxonomy    │
                    └────────────────┬────────────────────────┘
                                     │
        ┌────────────────┬───────────┼────────────┬──────────────────┐
        ▼                ▼           ▼            ▼                  ▼
   ┌─────────┐   ┌──────────────┐ ┌────────┐ ┌──────────┐   ┌──────────────┐
   │ Company │   │  Terminal    │ │Identity│ │ Settings │   │  Operator    │
   │lifecycle│   │  lifecycle   │ │  and   │ │  schema  │   │  security    │
   │ GP-01   │   │  SEC-01/02   │ │creds   │ │  GP-04   │   │  PPL-02      │
   │ CON-01  │   │  SEC-05/06   │ │FW-03   │ │  FW-08   │   │  SEC-10      │
   └─────────┘   └──────┬───────┘ │HW-03   │ └──────────┘   └──────────────┘
                        │         │PPL-01  │
                        │         └───┬────┘
                        │             │
                        ▼             ▼
                 ┌──────────────────────────┐
                 │  Sync capacity & scoping │
                 │  SEC-04 · FW-01 · SYN-*  │
                 └────────────┬─────────────┘
                              │
                              ▼
                 ┌──────────────────────────┐
                 │  Application engine      │
                 │  APP-01 · APP-03 · GP-03 │
                 └────────────┬─────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
      ┌──────────────┐ ┌────────────┐ ┌──────────────┐
      │Access control│ │  Events /  │ │  Firmware    │
      │   APP-02     │ │   audit    │ │  FW-02/04/05 │
      │              │ │ SEC-07/08  │ │  FW-07/09    │
      └──────────────┘ └────────────┘ └──────────────┘
                              │
                              ▼
                 ┌──────────────────────────┐
                 │  CI · deployment · docs  │
                 │  OPS-* · DOC-* · LEG-01  │
                 └──────────────────────────┘
```

**The hard sequencing constraints, stated explicitly:**

1. **The event model precedes every application.** `APP-04` is a schema change
   that `APP-01`, `APP-02`, `SEC-08` and all five unbuilt capabilities depend on.
   Building an application first means building it twice.
2. **The permission engine precedes access control.** `APP-02` supplies scope,
   schedule and validity. `PPL-03` (validity windows) is the same work.
3. **The credentials table precedes the biometric fix.** `HW-03` must land before
   `FW-03`, or template distribution attaches to a column instead of an entity.
4. **Roster scoping precedes the capacity fix.** `SEC-04` removes most of the
   pressure that makes `FW-01` bite, and changes what capacity has to mean.
5. **Terminal lifecycle precedes firmware revocation.** `SEC-01` has to define
   the revoked state before `FW-07` can enforce it at the door.
6. **Self-registration precedes removing legacy auth.** `FW-05` must land before
   `SEC-05` can be removed without stranding terminals.
7. **CI precedes nothing and protects everything.** `OPS-03` lands early so the
   rest of the work is guarded as it happens.

---

## Compatibility policy

Deployed terminals speak a contract. This remediation does not break it.

**Frozen — no change permitted without a firmware migration path:**

- `GET /api/v1/devices/jobs` envelope and job payload shapes
- `POST /api/v1/devices/jobs/{id}/complete`
- `POST /api/v1/devices/access/log`
- `POST /api/v1/devices/enrollment/result`
- `X-Device-Key` header authentication
- `X-Protocol-Version: 1`
- The `member_id` / `external_id` field name on the wire

**How new capability is added without breaking the contract:**

Every device-facing addition is either an **additive optional field** inside an
existing payload, or a **new job type** that older firmware reports as
`kUnknown` and acknowledges — which the firmware already does correctly
(`sync_engine.cpp`, `SyncJobType::kUnknown` → `kIgnored` → acked). That path is
what makes the protocol extensible without a version bump, and it is why
`SyncProtocolVersion` stays at 1 through this work.

**Where the wire name and the internal name now differ**, the internal model uses
the general-purpose name and the wire keeps the legacy one, with the mapping in
one place rather than scattered. Renaming a JSON field that deployed firmware
parses is not a cosmetic change and is not done.

---

## Findings

Ordered by severity, then by subsystem. Each carries its root cause rather than
only its symptom, because several findings share one.

---

### BLOCKER

#### GP-01 — No API creates a company
**Subsystem** Tenancy · **Status** OPEN

**Root cause.** Companies were introduced by migration 002 as the tenant every
pre-existing row was adopted by. Nothing has ever needed to create a second one,
so no code path does. `users.company_id` is `NOT NULL` and every console route is
scoped by the authenticated operator's company, which means there is also no
identity that could legitimately administer the installation rather than a tenant
within it.

**Remediation.** A platform-administration surface separate from the tenant
console: company create, read, update, activate/deactivate, and the ability to
issue a first operator into a new company. This needs an identity that is not a
tenant operator — the existing `users` table cannot express it, because every row
belongs to exactly one company by construction.

**Migration/compatibility.** Additive. No existing route changes meaning.

---

#### SEC-01 — No terminal revocation at any credential class
**Subsystem** Terminals · **Status** OPEN

**Root cause.** `DISABLED` is written in exactly one place in the entire codebase
— `RetireSite` (`database/sites.go:249`). The device state machine has the state;
nothing operator-facing reaches it. Site key rotation deliberately does not touch
device credentials, which is correct in isolation and means there is no lever at
all for one terminal.

**Remediation.** Terminal lifecycle at the console: disable, re-enable, revoke
credential, retire, reassign site. Revocation must clear `api_key_hash` so the
credential stops resolving, not merely flip a status a future code path might
ignore.

**Migration/compatibility.** Additive routes. The device auth path already
refuses `DISABLED`, so enforcement exists and only the control is missing.

---

#### SEC-02 — The site provisioning key is a company-wide data credential
**Subsystem** Security · **Status** OPEN

**Root cause.** `AuthMiddleware` sets `company_id` from the authenticated site,
and the handlers behind it were written before an operator identity existed — so
they scope by company because that was the only scope available. The result is
that a key installed at a door reads the whole company's roster (including the
credential column), its audit trail and its fleet, and writes its firmware
catalogue.

**Remediation.** Narrow the site key to provisioning. Roster, event and fleet
reads move to operator sessions; the firmware catalogue moves to `ADMIN` console
routes. The legacy `/api/v1/members` surface stays for deployed tooling but is
scoped and marked deprecated.

**Migration/compatibility.** This one has real breakage risk for anything using
the site key as an integration credential. Handled by keeping the legacy routes
working, narrowed to the authenticated site rather than the whole company, and
documenting the replacement.

---

#### SEC-03 — No secure boot, no flash encryption
**Subsystem** Hardware · **Status** OPEN

**Root cause.** Never configured. `platformio.ini` sets neither, so the device
credential and the local member table sit in readable NVS on a part that can be
dumped over its own UART.

**Remediation.** Build configuration for flash encryption and secure boot v2, and
a manufacturing procedure for the fuse burn. Verification requires hardware.

**Migration/compatibility.** One-way per device. Cannot be applied to already-
deployed units without reflashing them.

---

#### FW-01 — 64-person ceiling per terminal, failing silently
**Subsystem** Firmware · **Status** OPEN

**Root cause.** Two independent decisions that compound. `kMaxMembers = 64` sizes
the on-device table, and the server fans the entire **company** roster to every
terminal regardless of site (SEC-04). Person 65 fails `applyPersonUpsert` as
retryable, exhausts ten attempts and parks `FAILED`, and nothing surfaces it.

**Remediation.** Both halves. Scope the roster to what a terminal actually needs
(SEC-04), then establish a real capacity model bounded by the sensor, NVS and RAM
rather than a constant, with the limit reported to operators and an alarm when it
is approached.

**Migration/compatibility.** The on-device record layout is versioned already
(`kMemberSchemaVersion`, with a v1 migration path), so growing the table follows
the established pattern.

---

#### FW-02 — No OTA
**Subsystem** Firmware · **Status** OPEN

**Root cause.** Never built. The firmware catalogue was explicitly scoped as
inventory only in Sprint 6 and nothing has changed since.

**Remediation.** Signed OTA over the existing TLS transport, dual-partition with
rollback, staged by release channel. The catalogue and its `is_current` target
already exist to drive it.

**Migration/compatibility.** Deployed units cannot receive an OTA that teaches
them OTA. The first fleet needs one physical flash; everything after is remote.
This has to be stated in the sales and installation documentation.

---

#### FW-03 — Fingerprints are terminal-local, non-portable, unbacked
**Subsystem** Credentials · **Status** OPEN

**Root cause.** Templates live in the sensor. The firmware uploads a locator
(`terminal:<id>:slot:<n>`) and explicitly ignores `fingerprint_template` on the
way down. A person enrolled at one terminal is a member row with no finger bound
at every other terminal.

**Remediation.** Server-mediated, **server-opaque** template distribution:
sealed on the enrolling terminal, stored as ciphertext the server cannot read,
distributed only to terminals where the person has permission. Raw templates
never reach the cloud in readable form.

**Migration/compatibility.** New credential entity and a new job type. Older
firmware acknowledges the unknown job type and continues on the existing
single-terminal behaviour, so the fleet is not stranded.

---

#### APP-01 — No application business logic
**Subsystem** Applications · **Status** OPEN

**Root cause.** The capability model was built in advance of the capabilities, on
the reasoning that configuration should precede behaviour. It did — by a whole
product. `company_applications` and `devices.application_mode` are read only by
the console.

**Remediation.** An application framework over real platform primitives, then one
capability implemented end to end. Every capability not implemented stays
honestly marked, and is not offered for sale.

---

#### APP-02 — No permission engine, schedules or event model
**Subsystem** Access control · **Status** OPEN

**Root cause.** `permissions` and `doors` were created by migration 002 with the
note that the engine "lands in a later sprint". It did not. Zero lines of Go read
either table.

**Remediation.** Build the engine as a platform primitive: scope, level, day
mask, time window, validity period — evaluated on the server and enforced on the
device.

---

#### LEG-01 — No biometric data-protection posture
**Subsystem** Documentation / compliance · **Status** OPEN

**Root cause.** Never scoped. The platform processes special-category personal
data with no consent record, no retention policy, no erasure path (soft delete
retains the row indefinitely, `access_logs` is explicitly immutable), and no
documentation of where template data lives.

**Remediation.** The technical primitives — consent capture, retention windows
with a purge task, a real erasure path, and a written data-flow statement. The
legal instruments themselves are outside what can be built here.

---

### CRITICAL

#### SEC-04 — Person changes fan out company-wide
**Subsystem** Synchronization · **Status** OPEN

**Root cause.** `enqueuePersonChangeTx` and `enqueueBootstrapJobs` join
`people` to `devices` through `sites.company_id`. Written before sites mattered
for people, and never revisited.

**Remediation.** Scope the fan-out to the terminal's site, then to the permission
set once APP-02 exists.

---

#### FW-04 — Firmware never heartbeats
**Subsystem** Firmware · **Status** OPEN

**Root cause.** The network task calls four endpoints; heartbeat is not one of
them. `MarkDevicesOffline` requires `last_heartbeat_at IS NOT NULL`, so it never
matches, and every registered terminal reads `ONLINE` permanently.

**Remediation.** Heartbeat from the network task's probe cycle. The server side is
complete and tested.

---

#### FW-05 — Firmware never self-registers
**Subsystem** Firmware · **Status** OPEN

**Root cause.** Provisioning was designed as an operator action and never moved
into the device. The firmware has no `X-API-Key` path at all.

**Remediation.** Firmware-side registration holding the site key transiently, or a
claim-code flow that never puts the site key on a terminal. The second is
preferable and is what the design should aim at.

---

#### SYN-01 — Table-full is a silent permanent failure
**Subsystem** Synchronization · **Status** OPEN

**Root cause.** `kRetryable` is the correct classification — a delete may free a
row — but nothing distinguishes "retry, this may clear" from "this will never
clear", and no failure count reaches an operator.

**Remediation.** Surface per-terminal apply failures and backlog depth, alarm on
them, and provide an operator-reachable requeue.

---

#### OPS-02 — The console has no deployment configuration
**Subsystem** Deployment · **Status** OPEN

**Root cause.** `render.yaml` was written for the API before the console existed.

**Remediation.** Static site in the blueprint, an nginx site for the VPS target,
a strict CSP on both, and per-environment `VITE_API_BASE_URL`.

---

### MAJOR

| ID | Finding | Root cause | Remediation | Status |
|---|---|---|---|---|
| SEC-05 | Legacy site-key + serial device auth still accepted | Kept for firmware predating per-device keys | Remove once FW-05 lands; gate behind an explicit opt-in until then | OPEN |
| SEC-06 | Registration rotates credentials without limit | Rate limiting lives only in the VPS nginx config | Application-level limiter plus repeat-registration alerting | OPEN |
| SEC-07 | No operator action audit log | Never built; sessions are logged, mutations are not | Append-only `audit_events`, written in the mutation's transaction | OPEN |
| SEC-08 | Access logs unreachable from an operator session | The only log route predates the console | Grant-scoped `GET /console/events` over the typed event model | OPEN |
| SEC-09 | Rate limiting in-process and credential-only | Written for a single-instance deployment | Shared store; extend to enumeration endpoints | OPEN |
| SEC-10 | No operator password reset | Requires transactional email the platform lacks | Single-use short-lived reset tokens | OPEN |
| PPL-01 | No operator API for biometric enrolment | Enrolment endpoints are device/site-key authenticated | Console enrolment lifecycle over the credentials entity | OPEN |
| PPL-02 | No invitation flow, no forced first change | `password_changed_at` exists and is unread | Single-use invitations, `must_change_password` policy | OPEN |
| GP-02 | `person_type` is a fixed four-value taxonomy | Legacy of the single-purpose product | De-taxonomise; per-company vocabulary | OPEN |
| GP-03 | Capability codes are a closed SQL CHECK | Modelled as an enum before it was configuration | Promote capabilities to rows | OPEN |
| APP-03 | Terminals are never told their application mode | Device protocol deliberately untouched in Sprint 9 | Additive optional field in the settings payload | OPEN |
| APP-04 | No generic event model | `access_logs` predates every non-door capability | Typed `events` table: type, direction, subject, terminal, application | OPEN |
| FW-06 | Pinned root CA does not match documented production target | Bundle generated for Render; nginx documents Let's Encrypt | Pin both root sets; the bundle format already supports it | OPEN |
| FW-07 | Revocation has no firmware-side effect | 401/403 correctly treated as retryable for uploads, but no degraded state exists | Parse `offline_grace_minutes`; defined degraded policy on sustained 403 | OPEN |
| FW-08 | Two of four exposed settings are inert | Console schema written ahead of firmware support | Implement both, or remove them from the console | OPEN |
| FW-09 | Field truncation silently excludes people | `copyExact` correctly refuses rather than truncating, but nothing validates upstream | Server-side length validation with a clear error; surface apply failures | OPEN |
| SYN-02 | Access-log queue is lossy | RAM-only ring, drops oldest on overflow | Persist to flash; upload a gap marker when entries are dropped | OPEN |
| SYN-03 | Delivery failures consume the attempt budget | `AckJobFailed` cannot distinguish apply-failure from delivery-failure | Separate the two counters | OPEN |
| SYN-04 | No sync visibility in the console | Terminal projection predates the sync engine's operational needs | Backlog and failure counts; console-authenticated resync | OPEN |
| FE-01 | No accessibility, browser or device verification | All 349 tests are jsdom | axe pass, manual screen-reader pass, device matrix, hardware pass | OPEN |
| OPS-03 | No CI of any kind | Never set up | Pipeline gating all three suites plus security scanning | OPEN |
| DOC-01 | Documentation drift | README predates the rename and the console; three limitation lists disagree with the code | Rewrite and reconcile | OPEN |
| DOC-02 | No customer-facing documentation | Never written | Install guide, operator manual, onboarding runbook | OPEN |

---

### MINOR

| ID | Finding | Remediation | Status |
|---|---|---|---|
| SEC-11 | Metrics token compared with `==` | `subtle.ConstantTimeCompare` | OPEN |
| SEC-12 | `/metrics` open when `METRICS_TOKEN` unset | Fail closed | OPEN |
| SEC-13 | CSRF token derived, stable for session life | Accept as designed; revisit if bodies are ever logged | OPEN |
| SEC-14 | `SameSite=Lax` assumes a shared registrable domain | Document as a deployment constraint | OPEN |
| SEC-15 | Render database `ipAllowList` is `0.0.0.0/0` | Narrow; automate rather than rely on a comment | OPEN |
| OPS-04 | Render free tier sleeps and expires | Paid plan or VPS before customer traffic | OPEN |
| OPS-05 | Backups are a documented manual command | Scheduled, off-host, restore rehearsed | OPEN |
| OPS-06 | `access_logs` has no retention policy | Configurable retention with a purge task | OPEN |
| GP-04 | Two unvalidated open JSON settings objects | One declared-schema mechanism serving both | OPEN |
| GP-05 | No capability dependency model | Model prerequisites when two capabilities first relate | OPEN |
| GP-06 | Per-application settings unvalidated | Folds into GP-04 | OPEN |
| PPL-03 | People API omits email, phone, validity window | Expose all four; enforce validity in the permission engine | OPEN |
| PPL-04 | `category` can be replaced but never cleared | Pointer field, as `active` already uses | OPEN |
| PPL-05 | People list has no filters beyond free text | Server-side filters | OPEN |
| PPL-06 | Site grants persist through promotion | Clear on promotion, or surface explicitly | OPEN |
| CON-01 | No API to update company details | Folds into GP-01 | OPEN |
| CON-02 | Terminal list unpaginated | `limit`/`offset`/`q`, matching people | OPEN |
| CON-03 | Site reads omit `api_key_prefix` | One line in the projection | OPEN |
| CON-04 | No error boundary, no CSP | Route-level boundary; CSP with OPS-02 | OPEN |
| SYN-05 | Bootstrap convergence takes ~13 poll cycles | Poll faster while a backlog exists | OPEN |
| SYN-06 | New device seeded with people but not settings | Enqueue the settings snapshot in the registration transaction | OPEN |
| HW-01 | No tamper detection | Tamper input plus an event | OPEN |
| HW-02 | No REX, door sensor, forced/held-open, fire interlock | Scope against the target market | OPEN |
| HW-03 | Single credential factor, modelled as a column | Credentials table | OPEN |
| DOC-04 | Root CA bundle is gitignored | Commit it, or record its fingerprint | OPEN |

---

### NICE-TO-HAVE

| ID | Finding | Remediation | Status |
|---|---|---|---|
| CON-05 | No internationalisation | Extract strings behind a lookup | OPEN |
| SYN-07 | `EnqueueSettingsJob` is dead code | Remove, or use it for SYN-06 | OPEN |
| DOC-03 | `docs/sync-protocol.md` has uncommitted changes | Commit it | OPEN |

---

## Application status

Restated here because it is the question a buyer actually asks, and because the
answer must not drift from the code.

| Application | Status | May be sold? |
|---|---|---|
| ACCESS_CONTROL | PARTIAL | No — not until APP-02 governs the decision |
| REGISTRATION | PARTIAL | No — not until PPL-01 and FW-03 |
| ATTENDANCE | CONFIGURATION ONLY | No |
| CHECK_IN | CONFIGURATION ONLY | No |
| VERIFICATION | CONFIGURATION ONLY | No |
| TIME_TRACKING | CONFIGURATION ONLY | No |
| VISITOR_MANAGEMENT | CONFIGURATION ONLY | No |

A capability is not marked `IMPLEMENTED` because its configuration exists, its
terminal assignment works, or its navigation entry renders. It is marked
implemented when a person can be affected by it end to end and an operator can
see that they were.

---

## Verification baseline

Measured at the start of remediation, so any regression is visible.

| Suite | Count | Result |
|---|---|---|
| Go integration (real PostgreSQL) | 144 | Pass, 248s |
| Frontend (vitest/jsdom + MSW) | 349 | Pass, 20s |
| Firmware native (22 suites) | 550 | Pass, 137s |
| **Total** | **1043** | **Pass** |

No CI existed at the baseline, so this is the first run in which all three were
executed together.

---

## What has not been verified, and will be claimed only when it is

- **Real hardware.** Nothing in this remediation has been flashed to or exercised
  on a physical ESP32.
- **Real browser.** Every frontend test is jsdom.
- **Production deployment.** Nothing has been deployed.
- **Penetration testing.** No external security assessment has been performed.
- **Compliance.** No legal review of the biometric processing posture.

These are release gates, not caveats. They are listed in full at the end of the
audit report.
