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

### MR-005 — No API to create, edit or retire a site — **Major**

*Found: 2026-08-14, while enumerating the console's endpoints against the plan.*

Phase 2.2 calls for site creation and editing. The console API exposes
`GET /console/sites`, `GET /console/sites/{id}` and the settings pair — there is
no `POST`, `PUT` or `DELETE` for a site itself. A site is currently created only
by direct database access, which is how `deploy/README.md` step 6 describes
onboarding a customer.

This is a genuine gap in customer onboarding, not merely a missing screen: a
company cannot add its second location without an operator with database access.

Needs an API decision before 2.2 can deliver site creation. Noted here rather
than fixed inline, because it also raises where the site API key comes from on
creation — and that key must never reach the browser, so "create a site" cannot
simply return one.

---

## Resolved

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
