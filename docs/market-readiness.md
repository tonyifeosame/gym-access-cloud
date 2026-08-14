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

### MR-002 — Timestamps are wall-clock, mislabelled UTC — **Blocker**

*Found: 2026-08-14, while scoping the console's date/time rendering.*

Every timestamp column in the schema is `TIMESTAMP WITHOUT TIME ZONE`, and
`CURRENT_TIMESTAMP` writes the **database server's local wall clock** into it.
`lib/pq` then hands that value back to Go labelled UTC. The stored instant is
therefore wrong by the database server's UTC offset, and nothing in the value
itself says so.

Consequences already worked around rather than fixed:

- `database/users.go` computes account-lock remaining time in SQL, because a
  Go-side comparison against `time.Now()` is wrong by that offset.
- `database/sessions.go` does the same for session expiry.
- `handlers/auth.go` derives `session_expires_at` from a duration rather than
  forwarding a stored timestamp.

Those three are correct in isolation, but they are three local escapes from one
global defect. Every other timestamp the API returns — `created_at`,
`updated_at`, `last_seen_at`, `last_heartbeat_at`, `last_sync_at`,
`last_login_at`, `occurred_at` — is still wrong by the same offset, and all of
them are about to be rendered in a browser that will read the `Z` and believe it.

Classified Blocker because it is not a display bug: access logs and heartbeat
recency are the evidence a customer uses to answer "was the door open at 14:05",
and an hour-shifted answer to that question is worse than no answer.

**Resolution planned as frontend step 2.0b**, before any screen that renders a
date or time is built.

---

## Resolved

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
