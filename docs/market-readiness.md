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
**Subsystem** Tenancy · **Status** FIXED

**Proof.** `platform_admin_test.go` — a company is created, read, updated,
deactivated and issued its first operator through the API; a platform
administrator cannot reach a tenant's people, credentials, events, terminals
or site keys; an operator session cannot reach the platform tree and a
platform session cannot reach the console, in both directions.

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
**Subsystem** Terminals · **Status** FIXED

**Proof.** `terminal_lifecycle_test.go` — a revoked terminal cannot
authenticate, a disabled one keeps its credential and comes back without a
site visit, a retired one is gone, revocation does not touch its neighbours,
and every operation is company-scoped and audited.

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
**Subsystem** Security · **Status** FIXED

**Proof.** `terminal_lifecycle_test.go` — a site key cannot read another
site's terminals or access logs, and cannot write the firmware catalogue at
all. `security_test.go` covers the tenancy boundary on the remaining
site-key routes.

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
**Subsystem** Hardware · **Status** BLOCKED — firmware (AI #2) and a fuse
burn on real hardware. Nothing in the backend can close it.

**Root cause.** Never configured. `platformio.ini` sets neither, so the device
credential and the local member table sit in readable NVS on a part that can be
dumped over its own UART.

**Remediation.** Build configuration for flash encryption and secure boot v2, and
a manufacturing procedure for the fuse burn. Verification requires hardware.

**Migration/compatibility.** One-way per device. Cannot be applied to already-
deployed units without reflashing them.

---

#### FW-01 — 64-person ceiling per terminal, failing silently
**Subsystem** Firmware · **Status** PARTIAL

**Done.** The server half. A terminal is no longer sent the company's whole
roster (SEC-04), which removes most of the pressure, and `devices` now
carries `pending_job_count`, `failed_job_count` and `last_apply_error` so an
exhausted table is visible rather than silent.

**Remainder, and it is a finding in its own right.** `kMaxMembers = 64` is
still a firmware constant, and a single site with more than 64 permitted
people will still exhaust a terminal. The server does not yet refuse or warn
when a roster exceeds a terminal's stated capacity, because the terminal does
not report one. Needs AI #2.

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
**Subsystem** Credentials · **Status** PARTIAL

**Done.** The entities. `credentials` carries sealed, server-opaque material
with a vendor and template format, and `credential_placements` models which
terminal holds which credential in which sensor slot. A credential has its
own lifecycle independent of its owner, and the authorization engine enforces
it — `authorization_engine_test.go` proves a revoked credential is refused
without deactivating the person.

**Remainder.** No job type distributes sealed material to terminals, so a
person enrolled at one terminal still has no finger bound at any other. The
sealing itself is firmware work. Needs AI #2.

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
**Subsystem** Applications · **Status** PARTIAL

**Done.** `ACCESS_CONTROL` has real behaviour for the first time: a decision
is made from the company, site, terminal, person, credential, application,
permission and schedule, and it produces an auditable event carrying the
reason. It is enforced at the door for the ALLOW and unconditional-DENY case,
because a person the rules do not permit is no longer on the terminal's
roster. It is NOT enforced at the door for schedules -- see the application
status table, where that gap is stated in full. The capability is also now a row rather than a SQL `CHECK`, and a
capability a company has switched off authorizes nobody.

**Remainder.** The other five capabilities are configuration and an event
model that can carry them. `ATTENDANCE`, `CHECK_IN`, `VERIFICATION`,
`TIME_TRACKING` and `VISITOR_MANAGEMENT` have no logic on top and are NOT
marked implemented anywhere. See the application status table below, which is
the honest answer and has not been softened.

**Root cause.** The capability model was built in advance of the capabilities, on
the reasoning that configuration should precede behaviour. It did — by a whole
product. `company_applications` and `devices.application_mode` are read only by
the console.

**Remediation.** An application framework over real platform primitives, then one
capability implemented end to end. Every capability not implemented stays
honestly marked, and is not offered for sale.

---

#### APP-02 — No permission engine, schedules or event model
**Subsystem** Access control · **Status** FIXED

**Proof.** `authorization_engine_test.go` — 20+ cases covering
deny-by-default, DENY beating ALLOW at all three scopes, scope containment,
person and credential lifecycle, terminal/site/company state, schedule
bounds, midnight-crossing windows, per-schedule timezones, validity windows,
disabled capabilities and cross-tenant refusal.

The load-bearing case is `TestDenyByDefault`: it is the one the previous
behaviour could not have passed under any configuration.

**A bug this suite caught, recorded because it was invisible.** PostgreSQL
`TIME` columns arrive as `time.Time`, which `database/sql` stringifies as
RFC3339, and the first window parser used `fmt.Sscanf("%d")` — which stops at
the first non-digit and reports no error. `"0000-01-01T09:00:00Z"` parsed as
hour ZERO, every schedule window collapsed to 00:00–00:00, and the
midnight-crossing branch read that as "always". A permission restricted to
office hours admitted at 3am. The columns are cast to text in SQL now and the
parser refuses anything that is not exactly `HH:MM[:SS]`.

**Root cause.** `permissions` and `doors` were created by migration 002 with the
note that the engine "lands in a later sprint". It did not. Zero lines of Go read
either table.

**Remediation.** Build the engine as a platform primitive: scope, level, day
mask, time window, validity period — evaluated on the server and enforced on the
device.

---

#### LEG-01 — No biometric data-protection posture
**Subsystem** Documentation / compliance · **Status** PARTIAL

**Done.** Retention is now enforced rather than merely stored: per-company
event and audit windows exist and a scheduled task applies them hourly
(OPS-06). Biometric material is modelled as sealed and server-opaque, and no
console route loads it.

**Remainder.** No consent record, no erasure path that removes biometric
material on request, and no written data-flow statement. The legal
instruments are outside what can be built here; the consent and erasure
primitives are not, and are open.

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
**Subsystem** Synchronization · **Status** FIXED

**Proof.** `roster_scoping_test.go` — a person with no permission reaches no
terminal; a site-scoped grant reaches only that site; a terminal-scoped grant
reaches only that terminal and not its neighbour at the same site; an
unconditional DENY withdraws somebody; an expired permission is reconciled
off; reconciliation is idempotent; and a deletion still reaches terminals
that were holding the person even after their rule is gone.

**Two limitations this introduced, stated rather than hidden.** A CONDITIONAL
deny (narrowed to one capability or schedule) is not enforced while a
terminal is offline, and a validity window takes effect at an offline
terminal on a reconciliation cycle rather than instantly. Both follow from a
terminal caching a flat roster, both are bounded by the site's offline
policy, and both are in `API_SPEC.md`.

**Root cause.** `enqueuePersonChangeTx` and `enqueueBootstrapJobs` join
`people` to `devices` through `sites.company_id`. Written before sites mattered
for people, and never revisited.

**Remediation.** Scope the fan-out to the terminal's site, then to the permission
set once APP-02 exists.

---

#### FW-04 — Firmware never heartbeats
**Subsystem** Firmware · **Status** OPEN — server side complete and tested;
needs AI #2.

**Root cause.** The network task calls four endpoints; heartbeat is not one of
them. `MarkDevicesOffline` requires `last_heartbeat_at IS NOT NULL`, so it never
matches, and every registered terminal reads `ONLINE` permanently.

**Remediation.** Heartbeat from the network task's probe cycle. The server side is
complete and tested.

---

#### FW-05 — Firmware never self-registers
**Subsystem** Firmware · **Status** OPEN — needs AI #2. Blocks SEC-05.

**Root cause.** Provisioning was designed as an operator action and never moved
into the device. The firmware has no `X-API-Key` path at all.

**Remediation.** Firmware-side registration holding the site key transiently, or a
claim-code flow that never puts the site key on a terminal. The second is
preferable and is what the design should aim at.

---

#### SYN-01 — Table-full is a silent permanent failure
**Subsystem** Synchronization · **Status** PARTIAL

**Done.** Per-terminal `pending_job_count`, `failed_job_count`,
`last_apply_error` and `last_apply_error_at`, maintained by trigger, so a
failing terminal is visible. A roster reconciler runs every 15 minutes and is
the recovery path: a terminal left with a partial roster converges without
anybody noticing and forcing a resync.

**Remainder.** No alarm threshold and no operator-facing notification — the
counters exist and are readable, but nothing shouts. The firmware-side
distinction between "retry, this may clear" and "this will never clear" is
AI #2's.

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
| SEC-07 | No operator action audit log | Never built; sessions are logged, mutations are not | Append-only `audit_events`, written in the mutation's transaction | FIXED |
| SEC-08 | Access logs unreachable from an operator session | The only log route predates the console | Grant-scoped `GET /console/events` over the typed event model | FIXED |
| SEC-09 | Rate limiting in-process and credential-only | Written for a single-instance deployment | Shared store; extend to enumeration endpoints | OPEN |
| SEC-10 | No operator password reset | Requires transactional email the platform lacks | Single-use short-lived reset tokens | FIXED |
| PPL-01 | No operator API for biometric enrolment | Enrolment endpoints are device/site-key authenticated | Console enrolment lifecycle over the credentials entity | PARTIAL |
| PPL-02 | No invitation flow, no forced first change | `password_changed_at` exists and is unread | Single-use invitations, `must_change_password` policy | FIXED |
| GP-02 | `person_type` is a fixed four-value taxonomy | Legacy of the single-purpose product | De-taxonomise; per-company vocabulary | PARTIAL |
| GP-03 | Capability codes are a closed SQL CHECK | Modelled as an enum before it was configuration | Promote capabilities to rows | FIXED |
| APP-03 | Terminals are never told their application mode | Device protocol deliberately untouched in Sprint 9 | Additive optional field in the settings payload | OPEN |
| APP-04 | No generic event model | `access_logs` predates every non-door capability | Typed `events` table: type, direction, subject, terminal, application | FIXED |
| FW-06 | Pinned root CA does not match documented production target | Bundle generated for Render; nginx documents Let's Encrypt | Pin both root sets; the bundle format already supports it | OPEN |
| FW-07 | Revocation has no firmware-side effect | 401/403 correctly treated as retryable for uploads, but no degraded state exists | Parse `offline_grace_minutes`; defined degraded policy on sustained 403 | OPEN |
| FW-08 | Two of four exposed settings are inert | Console schema written ahead of firmware support | Implement both, or remove them from the console | OPEN |
| FW-09 | Field truncation silently excludes people | `copyExact` correctly refuses rather than truncating, but nothing validates upstream | Server-side length validation with a clear error; surface apply failures | OPEN |
| SYN-02 | Access-log queue is lossy | RAM-only ring, drops oldest on overflow | Persist to flash; upload a gap marker when entries are dropped | OPEN |
| SYN-03 | Delivery failures consume the attempt budget | `AckJobFailed` cannot distinguish apply-failure from delivery-failure | Separate the two counters | OPEN |
| SYN-04 | No sync visibility in the console | Terminal projection predates the sync engine's operational needs | Backlog and failure counts; console-authenticated resync | PARTIAL |
| FE-01 | No accessibility, browser or device verification | All 349 tests are jsdom | axe pass, manual screen-reader pass, device matrix, hardware pass | OPEN |
| OPS-03 | No CI of any kind | Never set up | Pipeline gating all three suites plus security scanning | FIXED |
| DOC-01 | Documentation drift | README predates the rename and the console; three limitation lists disagree with the code | Rewrite and reconcile | PARTIAL |
| DOC-02 | No customer-facing documentation | Never written | Install guide, operator manual, onboarding runbook | OPEN |

---

### MINOR

| ID | Finding | Remediation | Status |
|---|---|---|---|
| SEC-11 | Metrics token compared with `==` | `subtle.ConstantTimeCompare` | FIXED |
| SEC-12 | `/metrics` open when `METRICS_TOKEN` unset | Fail closed | FIXED |
| SEC-13 | CSRF token derived, stable for session life | Accept as designed; revisit if bodies are ever logged | ACCEPTED |
| SEC-14 | `SameSite=Lax` assumes a shared registrable domain | Document as a deployment constraint | ACCEPTED |
| SEC-15 | Render database `ipAllowList` is `0.0.0.0/0` | Narrow; automate rather than rely on a comment | OPEN |
| OPS-04 | Render free tier sleeps and expires | Paid plan or VPS before customer traffic | OPEN |
| OPS-05 | Backups are a documented manual command | Scheduled, off-host, restore rehearsed | OPEN |
| OPS-06 | `access_logs` has no retention policy | Configurable retention with a purge task | FIXED |
| GP-04 | Two unvalidated open JSON settings objects | One declared-schema mechanism serving both | OPEN |
| GP-05 | No capability dependency model | Model prerequisites when two capabilities first relate | OPEN |
| GP-06 | Per-application settings unvalidated | Folds into GP-04 | OPEN |
| PPL-03 | People API omits email, phone, validity window | Expose all four; enforce validity in the permission engine | PARTIAL |
| PPL-04 | `category` can be replaced but never cleared | Pointer field, as `active` already uses | OPEN |
| PPL-05 | People list has no filters beyond free text | Server-side filters | OPEN |
| PPL-06 | Site grants persist through promotion | Clear on promotion, or surface explicitly | OPEN |
| CON-01 | No API to update company details | Folds into GP-01 | FIXED |
| CON-02 | Terminal list unpaginated | `limit`/`offset`/`q`, matching people | OPEN |
| CON-03 | Site reads omit `api_key_prefix` | One line in the projection | FIXED |
| CON-04 | No error boundary, no CSP | Route-level boundary; CSP with OPS-02 | OPEN |
| SYN-05 | Bootstrap convergence takes ~13 poll cycles | Poll faster while a backlog exists | OPEN |
| SYN-06 | New device seeded with people but not settings | Enqueue the settings snapshot in the registration transaction | OPEN |
| HW-01 | No tamper detection | Tamper input plus an event | OPEN |
| HW-02 | No REX, door sensor, forced/held-open, fire interlock | Scope against the target market | OPEN |
| HW-03 | Single credential factor, modelled as a column | Credentials table | PARTIAL |
| DOC-04 | Root CA bundle is gitignored | Commit it, or record its fingerprint | OPEN |

---

### NICE-TO-HAVE

| ID | Finding | Remediation | Status |
|---|---|---|---|
| CON-05 | No internationalisation | Extract strings behind a lookup | OPEN |
| SYN-07 | `EnqueueSettingsJob` is dead code | Remove, or use it for SYN-06 | OPEN |
| DOC-03 | `docs/sync-protocol.md` has uncommitted changes | Commit it | OPEN |

---

## Remediation pass 2026-08-15 — backend

What moved, what did not, and what somebody else has to do. Statuses above were
updated against the code in the same pass, not against intent.

### Closed, with the test that would have caught the original defect

| Finding | Proof |
|---|---|
| GP-01 / CON-01 — no API creates a company | `platform_admin_test.go` |
| SEC-01 — no terminal revocation | `terminal_lifecycle_test.go` |
| SEC-02 — site key is a company-wide data credential | `terminal_lifecycle_test.go`, `security_test.go` |
| SEC-04 — person changes fan out company-wide | `roster_scoping_test.go` |
| SEC-07 — no operator action audit log | `terminal_lifecycle_test.go`, `credential_handover_test.go` |
| SEC-08 — access logs unreachable from a session | `events_api_test.go` |
| SEC-10 / PPL-02 — no reset, no invitations | `credential_handover_test.go` |
| SEC-11 / SEC-12 — metrics token and exposure | `metrics_exposure_test.go` |
| APP-02 — no permission engine or schedules | `authorization_engine_test.go` |
| APP-04 — no generic event model | `events_api_test.go`, `platform_primitives_test.go` |
| OPS-06 — no retention policy | scheduled purge task; `platform_primitives_test.go` |
| GP-03 — capabilities are a closed SQL CHECK | `platform_primitives_test.go` |
| OPS-03 — no CI | `.github/workflows/ci.yml` |
| CON-03 — site reads omit `api_key_prefix` | `console_sites_test.go` |

### Deliberately still open, and why

- **The five unbuilt capabilities.** `ATTENDANCE`, `CHECK_IN`, `VERIFICATION`,
  `TIME_TRACKING` and `VISITOR_MANAGEMENT` remain configuration plus an event
  model that can carry them. They are not marked implemented anywhere, and the
  application status table below still says so. Building one of them on the new
  primitives is now a small piece of work; claiming five of them would have been
  the exact failure this register exists to prevent.
- **Everything firmware.** FW-01 (the on-device constant), FW-02, FW-04, FW-05,
  FW-06, FW-07, FW-09, SYN-02, SYN-03, SEC-03, HW-01, HW-02 need AI #2. Where
  the server half exists and is tested, the finding says so.
- **SEC-05.** Legacy site-key + serial device auth cannot be removed until
  firmware self-registration (FW-05) exists, or deployed terminals are stranded.
- **SEC-09.** Rate limiting is still in-process, so with more than one instance
  the effective rate multiplies by the instance count. It is documented in
  `API_SPEC.md` rather than quietly carried.
- **GP-04 / GP-06.** Two unvalidated open JSON settings objects. The audit
  records for settings changes deliberately say *that* settings changed and not
  *what*, because this layer cannot know which fields are sensitive — which is
  the same gap from the other side.

### Two limitations this pass introduced

Both follow from a terminal caching a flat roster rather than evaluating the
permission model, and both are bounded by the site's offline policy. They are in
`API_SPEC.md` as well, because a client has to design around them.

1. **A conditional DENY is not enforced at an offline terminal.** A `DENY`
   narrowed to one capability or one schedule cannot be applied by removing
   somebody from a flat roster without also refusing them when they *are*
   allowed. An unconditional DENY does remove them, so exclusion survives an
   outage.
2. **A permission's validity window takes effect at an offline terminal on a
   reconciliation cycle**, not instantly — 15 minutes by default. An online
   terminal is decided by the server at the door and is exact.

### The compatibility position, restated

Nothing in this pass changed the device wire contract. `SyncProtocolVersion`
stays at 1, `access_logs` is still written by the same handler and to the same
shape, and the device's event id remains the idempotency key. The typed event
trail is written **beside** the access log, from one place, so the two cannot
disagree about what a terminal reported.

The one behavioural change a deployed installation would notice is that a
terminal now receives only the people its permissions cover. Companies that
existed before the authorization engine were migrated to `COMPANY_ALLOW`, which
reproduces their previous roster exactly; companies created after it start at
`NONE`.

---

## Application status

Restated here because it is the question a buyer actually asks, and because the
answer must not drift from the code.

| Application | Status | May be sold? |
|---|---|---|
| ACCESS_CONTROL | PARTIAL | Not yet — see the remainder below |
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

**Why ACCESS_CONTROL is still PARTIAL, stated precisely, because it is the one
most likely to be over-claimed after this pass.**

What is real: the decision is evaluated from company, site, terminal, person,
credential, application, permission, schedule and validity window; it denies by
default; DENY beats ALLOW; it produces an auditable event carrying the reason;
and it is enforced at the door for the ALLOW/unconditional-DENY case, because a
person the rules do not permit is no longer on the terminal's roster at all.

What is not: **the terminal does not enforce schedules.** The device protocol
carries a flat roster and no time rules, so somebody whose permission is
restricted to office hours is admitted by the terminal at any hour. The server
records the divergence afterwards, which makes it visible but does not stop it.
The same applies to a DENY narrowed to one capability.

Closing this needs the terminal to be told its rules — an additive field in the
settings payload (APP-03) plus firmware to evaluate them (AI #2). Until then a
customer whose requirement is "these people, but only on weekday mornings" is
not served by this platform, and must not be told otherwise.

---

## Verification baseline

Measured at the start of remediation, so any regression is visible.

| Suite | At baseline | After this pass | Result |
|---|---|---|---|
| Go integration (real PostgreSQL) | 144 | **252** | Pass, 203s |
| Frontend (vitest/jsdom + MSW) | 349 | not re-measured here (AI #3) | — |
| Firmware native (22 suites) | 550 | not re-measured here (AI #2) | — |

The Go figure is top-level test functions, counted the same way both times. The
frontend and firmware suites are owned by AI #3 and AI #2 and were not run in
this pass, so no claim is made about them.

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
