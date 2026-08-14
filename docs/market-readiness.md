# AccessLink market readiness

A running checklist, maintained **during** development rather than assembled at
the end. Items are added the moment they are discovered — including ones found
while building something else — so that nothing depends on remembering it later.

**This document does not certify anything.** A comprehensive review of the whole
product (frontend, API, database, auth, multi-tenancy, applications, sync,
firmware, fingerprint lifecycle, provisioning, offline behaviour, relay safety,
firmware update/recovery, deployment, backup/restore, monitoring, documentation,
onboarding, production hardware) happens once the frontend is complete. Until
that review has been done and passed, AccessLink is **not** market-ready and must
not be described as such.

## Classification

Findings are classified only at the final review. Until then they carry a
severity so the final pass has something to sort by:

| Severity | Meaning |
|---|---|
| **Blocker** | Ships broken or unsafe. Cannot be released with this open. |
| **Major** | Real customer-visible defect or risk; should not launch with it. |
| **Minor** | Worth fixing, tolerable at launch. |

## Status

| Area | State |
|---|---|
| Backend / API | Feature-complete for Phase 2; not reviewed |
| Frontend console | In progress (Phase 2) |
| Firmware | Untouched this phase |
| Everything else | Not yet reviewed |

---

## Open

### MR-011 — No operator API for biometric enrolment — **Major**

*Found: 2026-08-14, auditing the People API before Phase 2.4.*

The console can report **whether** a person has a biometric credential and can
do nothing else with it. `POST /enrollment/start`, `GET /enrollment/pending` and
`POST /enrollment/result` are authenticated by the **site key** or a **device
key** — neither of which a browser may hold — so none is reachable from an
operator session. There is no console route at all to:

- start an enrolment for a person,
- see which enrolments are pending or have failed,
- clear or re-enrol a credential,
- record which terminal a credential lives on.

The practical gap: **an operator cannot revoke one person's biometric credential
while keeping their record.** Removing the person entirely is the only lever, and
that is a different action with different consequences. Re-enrolling someone
whose finger no longer reads reliably is likewise not an operator-facing
workflow.

Recorded rather than invented. Phase 2.4 states plainly on the person detail page
that enrolment happens at a terminal and cannot be managed from the console,
instead of offering a control that would 404.

### MR-012 — `person_type` is hard-coded to a four-value business taxonomy — **Major**

*Found: 2026-08-14, auditing the People schema.*

`people.person_type` exists, is `NOT NULL DEFAULT 'MEMBER'`, is indexed, and
carries a CHECK constraint:

```sql
CHECK (person_type IN ('MEMBER', 'STAFF', 'CONTRACTOR', 'VISITOR'))
```

**That is a fixed business taxonomy in the database**, and it contradicts the
platform being general-purpose. A school cannot record STUDENT; a conference
cannot record ATTENDEE; a hospital cannot record PATIENT. The default value
`MEMBER` is itself a leftover from the single-purpose product this became.

Compounding it, `person_type` is **not exposed by the console API at all**. What
the console shows as "person type" is `category`, which maps to the *other*
column — `membership_type`, free text, also a legacy name. So the schema has two
overlapping classification fields: one constrained and invisible, one
unconstrained and visible.

Phase 2.4 exposes only the free-text one, as free text, which is the behaviour a
general-purpose platform needs. The constrained column remains as a trap for
whoever next tries to use it.

Needs a product decision: drop `person_type`, widen it to free text, or make it a
per-company configurable vocabulary. Not fixable from the frontend.

### MR-013 — People API omits fields the schema already holds — **Minor**

*Found: 2026-08-14, auditing the People API.*

`people` carries `email`, `phone`, `valid_from` and `valid_until`. None is
exposed by any console endpoint, so the console cannot show or set them.

`valid_from`/`valid_until` are the notable pair: a **validity period** is exactly
what a visitor, a contractor on a fixed engagement, or a temporary badge needs,
and the column is already there with a CHECK constraint enforcing ordering. There
is currently no way to grant someone access that expires by itself — an operator
must remember to deactivate them.

Also minor but real: `PUT /console/people/{external_id}` applies `category` only
when non-empty, so **a category cannot be cleared once set**, only replaced.

### MR-014 — People list has no filters beyond free-text search — **Minor**

*Found: 2026-08-14, while building the people list.*

`GET /console/people` accepts `limit`, `offset` and `q` and nothing else. There
is no filter for active/inactive, for enrolled/not-enrolled, or by person type.

Unlike the terminal list (MR-009), this one **is** paginated, so the console
cannot compensate in the browser: filtering the fetched page would filter fifty
people and present it as filtering the roster. Phase 2.4 therefore ships search
and paging only, and adds no client-side filter.

"Who has not enrolled a credential yet" is the query an onboarding operator
actually wants, and it is currently unanswerable.

### MR-008 — No console path for terminal removal or reassignment — **Major**

*Found: 2026-08-14, investigating terminal lifecycle during Phase 2.3.*

The console terminal API is exactly four routes: list, summary, detail, and
set-application-mode. There is **no operator API** to:

- **remove a terminal** — a unit that is lost, stolen, destroyed or replaced
  stays in the inventory and keeps authenticating on its device key;
- **move a terminal to another site** — a unit relocated between locations must
  be re-registered, and until then reports against the wrong site;
- **disable or re-enable a terminal** — `active` and `DISABLED` exist in the
  schema and are honoured by device auth, but nothing operator-facing sets them;
- **force a resync** — `POST /api/v1/devices/{serial}/resync` exists but is
  authenticated by the **site provisioning key**, which a browser must never
  hold, so it is not reachable from the console.

Two consequences worth stating plainly. **A stolen terminal cannot be revoked
from the console** — the only lever is rotating the site key, which is
site-wide and does not touch that unit's own device credential. And this is
what makes site retirement a one-way door: `DELETE /console/sites/{id}` cascades
to terminals precisely because there is no other way to deal with them.

Recorded as a **product requirement rather than invented**. Any of these is a
new authorized endpoint with its own tests; none is something the frontend may
approximate. Phase 2.3 names the gap on the terminal detail page rather than
leaving an operator hunting for a button that was never built.

Needs a decision before commercial release, and the revocation case is the one
I would treat as closest to a blocker.

### MR-009 — The terminal list is unpaginated and unsearchable server-side — **Minor**

*Found: 2026-08-14, while building the terminal inventory.*

`GET /console/terminals` returns the caller's entire scoped fleet in one
response. There is no `limit`, `offset` or `q` — unlike `GET /console/people`,
which was paginated and given SQL search for exactly this reason.

Phase 2.3 therefore filters and searches **in the browser**, which is *correct
today*: the client holds the complete set, so narrowing it narrows everything
rather than one page. That is a property of the current API, not a principle,
and it stops being true the moment the endpoint is paginated — at which point
the client-side filter silently becomes a page filter wearing a search box.

It also does not scale: a customer with a few thousand terminals serialises all
of them on every load. Not urgent at current fleet sizes; would need
`limit`/`offset`/`q` and a matching frontend change before it is.

### MR-010 — Frontend quality gaps carried forward — **Should fix before launch**

*Recorded 2026-08-14, tracked here so they are not rediscovered at review.*

- **No automated accessibility audit.** The primitives are built to explicit
  contracts — focus entry and return, focus trapping, labels bound to controls,
  errors announced, colour never the sole signal — and each is covered by a
  test. None of that is the same as an axe/WCAG pass. **An automated audit is
  still required**, plus a manual pass with an actual screen reader.
- **No real-browser verification.** Everything is jsdom. jsdom does not lay out,
  does not apply media queries, and only approximates focus behaviour — so the
  responsive breakpoints (900px navigation, 680px table-to-card) and the dialog
  focus trap are **proven in tests but unproven in a browser**. Needs a pass on
  real desktop, tablet and phone before launch.
- **No real-device verification.** Deliberately out of scope during the frontend
  phases: nothing has been exercised against actual ESP32 hardware. Site
  settings changes, application-mode assignment and key rotation all have
  effects on terminals that only real hardware can confirm.

### MR-007 — Site reads do not return the key prefix — **Minor**

*Found: 2026-08-14, while building the Sites UI.*

`sites.api_key_prefix` exists, is populated on creation and rotation, and is
explicitly **not secret** — it identifies which key a site is on without being
reconstructible. `database.SiteKeyPrefix` reads it. But `consoleSiteColumns` does
not select it, so `GET /console/sites` and `GET /console/sites/{id}` never carry
it.

The consequence is small but real: after creating a site, an operator has no way
to confirm which key a site is currently on — useful when several have been
rotated, and the ordinary way to check that hardware was re-provisioned with the
right one.

The console is already built for it. `Site.api_key_prefix` is typed optional and
both the list column and the detail card render it when present, degrading to
"—"/"Not shown" when absent. **The fix is one line in the projection plus the
field on `ConsoleSite`**, with nothing to change in the frontend.

Deliberately not done in Phase 2.2, which was scoped to the frontend with the
backend explicitly frozen.

### MR-004 — Site settings have no schema, and the write is a full replacement — **Major**

*Found: 2026-08-14, while building the frontend data layer for the site settings
editor.*

`settings` on a site is an open JSON object. The API validates only that it IS an
object; it does not know the key set, the value types, or the ranges. Two
consequences:

- **A typo is accepted and reaches hardware.** `PUT` replaces the object
  wholesale, so `{"unlock_duration_secnods": 5}` silently drops the real key and
  queues a SETTINGS job carrying the mistake to every terminal at the site.
- **Nothing bounds a value.** `unlock_duration_seconds` of 3600 is accepted, and
  that is a door held open for an hour.

The frontend mitigates this in 2.2 with a guided editor over the keys it
recognises plus a raw editor that never discards an unrecognised key, but a
console cannot be the only validation in front of a door. The authoritative
schema belongs in the API.

Classified Major rather than Blocker because the guided editor removes the
realistic path to the typo; it stays open because the API still accepts one from
any other client.

---

## Resolved

### MR-006 — Site provisioning keys were stored in plaintext — **Blocker**

*Found: 2026-08-14, while inspecting site credential storage before designing
the site-management API. Fixed the same day.*

`sites.api_key` held the provisioning secret in plaintext, matched by exact
comparison. That key registers terminals and rotates their device credentials,
which makes it the highest-value secret on the platform — higher than any single
device key, because it mints them.

The consequence: anything that could read one row of `sites` — a backup, a
replica, a support engineer with `SELECT`, an injection on any query touching the
table — yielded a working provisioning credential for **every site in every
company on the installation**. `deploy/README.md` even documented reading one
back with `SELECT`.

This was known and deferred. `005_device_identity.sql` moved *device* credentials
to SHA-256 and said so explicitly: *"Note the asymmetry with sites.api_key, which
is still plaintext… Hashing site keys belongs in its own migration with a
rotation window."*

**Assessed as not acceptable for a commercial release**, and fixed by
`011_site_credentials.sql`: SHA-256 hash, non-secret 12-character prefix for
display, plaintext column dropped.

**No rotation window was needed**, which is the finding that made this cheap.
Unlike a password, the plaintext was sitting in the column at migration time, so
every existing key's hash could be computed during the migration. The wire
contract did not change by a byte — the same `X-API-Key: <same string>` — so
every provisioned site, and every terminal still on the deprecated
site-key-plus-serial path, keeps authenticating with the key it already holds. No
firmware is aware of it. The migration refuses to run if any live site would be
left without a hash.

What is deliberately lost: the key can no longer be read back out of the
database. Rotation replaces that recovery path.

Covered by `TestSiteKeyMigrationPreservesExistingCredentials` (builds a pre-011
schema, seeds a plaintext key, migrates, proves the same key still resolves and
the column is gone), `TestSiteKeyAuthenticationSurvivesHashedStorage`, and
`TestNoPlaintextSiteKeyRemainsInTheDatabase`.

### MR-005 — No API to create, edit or retire a site — **Major**

*Found: 2026-08-14, while enumerating the console's endpoints against the plan.
Fixed the same day.*

The console API exposed only `GET /console/sites`, `GET /console/sites/{id}` and
the settings pair. A site could be created only by direct database access, which
is how `deploy/README.md` step 6 described onboarding a customer — so a company
could not add its second location without an operator holding database
credentials. A genuine gap in customer onboarding, not merely a missing screen.

Resolved with a full lifecycle at ADMIN: create (returning the generated key
once), update metadata, deactivate reversibly, retire irreversibly, and rotate
the key. Onboarding in `deploy/README.md` now runs through the API.

Two design decisions worth recording for the final review:

- **Retirement cascades to terminals**, in one transaction, and reports the
  count. Refusing to retire a site that still holds hardware sounds safer and is
  a dead end — there is no console route that removes a terminal, so such a site
  could never be retired at all. Every terminal at a retired site stops
  authenticating immediately, on its own device key as well as the site key.
  `PUT active:false` is the reversible alternative.
- **Rotation has no overlap window.** A window is a period during which a
  credential believed to be revoked still provisions door hardware. The response
  reports how many terminals at the site have never been issued a device
  credential and therefore just lost access.

Covered by `console_sites_test.go` — authorized creation, role and CSRF gates,
cross-company refusal, name uniqueness per company, key entropy and format, key
returned only on creation, key absent from every subsequent read, rotation,
deactivation, retirement cascade, and a sweep of the whole site surface for
credential material.

### MR-002 — Timestamps were wall-clock readings mislabelled UTC — **Blocker**

*Found: 2026-08-14, while scoping the console's date/time rendering. Fixed the
same day, as frontend step 2.0b.*

All **61 timestamp columns across 15 tables** were `TIMESTAMP WITHOUT TIME ZONE`,
which stores a wall-clock reading and no record of which clock took it. `lib/pq`
then labelled every value `Z` on the way out, so the API reported a confident,
well-formed instant that was wrong by the database server's UTC offset — one hour
on the deployment this was found on (`Africa/Lagos`).

**It was worse than a uniform offset.** Three writers reached the same columns
from three different clocks, and PostgreSQL discarded the offset on the way into
a naive column:

| Writer | What it actually stored |
|---|---|
| `CURRENT_TIMESTAMP` | the **database** server's wall clock |
| a Go `time.Time` parameter | the **API process's** wall clock |
| a device's RFC3339 `Z` | true UTC |

Measured, not inferred: a device reporting `17:00:00Z` and the server default
firing at the same moment landed **an hour apart** in `access_logs.occurred_at`.
So a single site's audit trail was internally inconsistent, and could order
events wrongly, depending on whether the terminal had a working clock. That is
the evidence a customer uses to answer "was this door released at 14:05".

Three places had already been written around this locally — account lockout,
session expiry, and `session_expires_at` all compute a remaining duration in SQL
rather than compare a stored timestamp to `time.Now()`. Correct in isolation, but
three escapes from one global defect, and every other timestamp still carried it.

**Fixed** by migration `010_timestamptz.sql`, which converts all 61 columns to
`TIMESTAMPTZ`, plus pinning the connection pool's session time zone to UTC so the
wire format stays `Z`-suffixed regardless of where the database sits.

Pinning UTC alone was measured to make reads and writes *through the API pool*
correct, and was deliberately **not** treated as sufficient: it does nothing for
existing rows, protects only that one pool (psql, restores, BI connections and
`migrate.sh` are all unpinned), and leaves the stored value ambiguous forever.

Covered by `TestSchemaHasNoNaiveTimestamps` (a regression guard against any
future migration reintroducing a naive column), `TestPoolPinsUTC`,
`TestWritePathsAgreeOnTheSameInstant`, `TestStoredTimestampsMatchTheInstantTheyClaim`, `TestChangesSinceHonoursAnOffset`,
`TestConsoleTimestampsSerialiseAsUTCInstants`, and two migration tests that build
a pre-010 schema, populate it, convert it and check the arithmetic.

**Carried forward to the final review:** the conversion is not automatically
reversible and assumes the database server's zone has not changed since the data
was written. Documented in `deploy/README.md` with an explicit
`accesslink.legacy_timezone` override, and the migration raises a `WARNING`
naming the zone it assumed whenever it converts a populated database. Worth
re-checking against whatever data the staging deployment actually holds before
that migration is applied to it.

### MR-003 — Terminals could not be joined to their site — **Major**

*Found: 2026-08-14, while building the frontend data layer. Fixed the same day.*

A terminal carried the internal `site_id` (a row id) and `site_name`;
`/console/sites` keys its entries by `public_id`. A browser holding both had
nothing to match them on but the name — which is editable and not unique, so
scoping a terminal list to the selected site, or linking a terminal to the site
it stands at, would have been wrong exactly when two sites were named alike.

Fixed by adding `site_public_id` to the inventory projection. Additive: the
internal `site_id` is part of a contract terminals and existing tooling already
speak, so it stays. Covered by `TestConsoleTerminalsCarryAJoinableSiteID`, which
asserts the list and the detail agree and that the old field survived.

### MR-001 — Terminal detail and configuration ignored site grants — **Blocker**

*Found: 2026-08-14, while preparing to expose terminal operations in the
console. Fixed the same day.*

`GET /console/terminals` was narrowed to the caller's site grants, but
`GET /console/terminals/{serial}` and
`PUT /console/terminals/{serial}/application-mode` were scoped to the company
only. A MANAGER or VIEWER granted one site could not *see* another site's
terminals in the list, and could still read one — and repoint what application it
runs — by naming its serial directly. Serial numbers are printed on the hardware,
so possession of one was never a control.

`GET /console/terminals/summary` additionally reported company-wide counts to a
scoped operator, disclosing the size of a fleet they had deliberately not been
granted, and would have rendered as an obviously-wrong figure above a narrowed
list.

Fixed by resolving the terminal's own site and applying the existing grant rule
to it (`middleware.RequireTerminalGrant`), with the `404`/`403` split matching
`RequireSiteGrant` so the API still never confirms that a serial is registered to
another tenant. The grant rule itself is now a single function both gates call.
The summary and the list are narrowed in SQL by the same scope.

Covered by `TestConsoleTerminalGrantEnforcement`.
