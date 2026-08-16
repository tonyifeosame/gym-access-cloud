# Backend requirements from the console

What the operator console needs and cannot build, recorded rather than invented.

Every entry here is a screen that was **not written**, or was written with a
stated gap, because the API to serve it does not exist. Nothing in the console
fakes one of these: no endpoint is called that is not in `router.go`, no value is
derived from an unrelated one to stand in for a missing field, and every screen
that has a hole says so in words an operator can read.

That last rule is the reason this file exists. The cheap alternative — deriving
attendance from access logs, showing an empty audit view where door events would
go, marking a capability "enabled" and letting an owner infer the rest — produces
a console that looks finished and lies. Recording the gap costs a paragraph and
keeps the product honest about what it does.

**Status of this document.** Written by the console against the API as it stood
at the end of this pass, including the platform, credential-handover,
terminal-lifecycle, authorization-engine and event work that landed during it.
Each item names the audit finding it belongs to, so it can be closed against the
same register.

**One correction to the register, offered rather than applied.** `CON-03` is
marked `FIXED`, but `models.ConsoleSite` still has no `api_key_prefix` and
`console_sites_test.go` asserts the prefix only on the CREATE response and in a
raw SQL read — not on `GET /console/sites` or `GET /console/sites/{id}`, which is
what the finding describes. The console types the field optional and populates it
from create/rotate responses only, so nothing here is broken; the status is
simply ahead of the projection. Left for AI #1 to reconcile, since the register
is theirs.

---

## Ordering

These are not independent, and building them in the wrong order means building
some of them twice.

```
   (events, landed)  ──────────────→  AT-01  attendance & check-in
   CR-01  credential lifecycle  ────→  (registration becomes operational)
   PP-01  person model  ───────────→  PP-02  person filters
   SET-01 settings schema  ────────→  guided settings for sites AND applications
   TM-01  terminal paging + health   (independent, small)
   CO-01  small gaps                 (independent, one line each)
```

`AT-01` is now the largest remaining item, and it is unblocked: the typed event
model it needs exists. `CR-01` is the one that turns a capability from "partly
built" into operational, so it is the highest-value of the rest.

---

## CLOSED DURING THIS PASS

Two of the largest items on this list were built by AI #1 while the console work
was in flight, and both now have screens rather than entries here.

| Was | Landed as | Console |
|---|---|---|
| EV-01 — door and terminal event history | `GET /console/events`, VIEWER, grant-scoped, with `occurred_at` and `recorded_at` kept distinct and `occurred_at_trusted` beside them | The **Events** page. Explains refusals by reason and remedy, shows presentations that matched nobody, and surfaces both timestamps when they diverge. |
| AC-01 — permissions, schedules, validity | `GET/POST /console/people/{id}/permissions`, `DELETE /console/permissions/{id}`, schedule CRUD, and `POST /console/terminals/{serial}/evaluate` | Access rules on a person's record, a **Schedules** page, and "would this person get in here, right now, and why" on the terminal page. |

The API shapes were richer than this document had asked for in two ways that
changed the screens for the better, and are worth recording because the next
endpoint should copy them:

- **`evaluate` writes no event.** The engine's authorize step is separate from
  its record step precisely so a preview cannot put a presentation that never
  happened into the trail an attendance report is built on. That single decision
  is what makes the preview safe to run twenty times while working out a rule,
  and the console says so on screen.
- **The decision carries its reason on a GRANT as well as a refusal.** A trail
  that records why somebody was turned away but not why somebody was admitted
  answers half of what a security review asks.

### Backend changes on 2026-08-16 the console has to absorb

Three of these are refusals where something used to be accepted silently. None
of them is a new screen; all of them change what an existing form must do.

| Change | What the console must do |
|---|---|
| **`external_id` is validated** (FW-09) — at most 31 characters, printable ASCII, no spaces | Validate in the person form with the same rule, so the constraint is visible while it can still be changed. **A UUID does not fit**, and it is what an integrator reaches for first. The `400` carries `field` and a message written to be shown verbatim |
| **`offline_policy` / `offline_grace_minutes` refused in free-form settings** | See SET-01. Reject in the raw editor rather than sending and displaying the error |
| **`GET /access/{member_id}` requires `?terminal=`** | The console should not call it at all — `POST /console/terminals/{serial}/evaluate` is the supported route and always was. Recorded in case anything still reaches for the older one |
| **Site projections carry `offline_policy` and `offline_grace_minutes`** | The site list can now show which locations keep opening during an outage, without a per-site fetch |

---

## AT-01 — Attendance, check-in and worked time

**Audit finding** APP-04 · **Depends on** EV-01

**Endpoints**

```
GET /api/v1/console/attendance?person=&site_id=&from=&to=
GET /api/v1/console/attendance/summary?from=&to=&group_by=person|site|day
```

**Response** Sessions with `person`, `site`, `first_seen`, `last_seen`,
`duration_seconds`, `source_event_ids`, and an explicit `complete` flag for a
session whose closing event has not arrived.

**Authorization** `VIEWER`, site-scoped.

**Reason** And this is the one where the shortcut is genuinely tempting: an
attendance report *could* be derived in the browser from access events. It must
not be. A door event says somebody presented a credential at a reader; it does
not say they arrived for work, that they stayed, or that they left — a person who
props a door and walks out generates one event and no departure. Deriving
business records from unrelated technical ones produces numbers that look
authoritative and are wrong in ways nobody can audit afterwards. `ATTENDANCE`,
`CHECK_IN` and `TIME_TRACKING` are therefore all marked **not built** in the
console, each with the sentence "nothing records attendance / a check-in /
worked time", and no screen derives anything.

---

## CR-01 — Credential lifecycle from an operator session

**Audit finding** PPL-01, FW-03, HW-03

**Endpoints**

```
GET    /api/v1/console/people/{external_id}/credentials
POST   /api/v1/console/people/{external_id}/credentials/enrolment
DELETE /api/v1/console/credentials/{id}
```

**Request** (start an enrolment)

```json
{ "terminal_serial": "AT-0001", "type": "FINGERPRINT" }
```

**Response** An enrolment with `state`
(`PENDING` / `IN_PROGRESS` / `COMPLETE` / `FAILED` / `EXPIRED`), the terminal it
is to happen at, an expiry, and — on the credential itself — `type`, `state`,
`enrolled_at`, `enrolled_at_terminal`, `usable_at_terminal_count`.

**NEVER** a template, a locator, a slot number, a sensor identifier or a vendor
detail. The console models biometrics as an abstraction the backend owns; that is
what lets the storage change without a frontend release, and it is asserted by a
test that scans the source for those very words.

**Authorization** `MANAGER`, site-scoped by the terminal the enrolment is
directed at.

**Reason** Enrolment endpoints are device- and site-key-authenticated, so an
operator cannot start one, watch it, cancel it, or revoke a credential
afterwards. The console shows `biometric_enrolled` as a boolean and has nothing
to offer beyond it. `usable_at_terminal_count` is the field that makes the
current limitation legible: an enrolment binds to the terminal that took it, so
"enrolled" today means "recognised at one door", and an operator has no way to
discover that.

---

## PP-01 — The person model a general-purpose platform needs

**Audit finding** PPL-03, PPL-04, GP-02

**Changes to** `GET/POST/PUT /api/v1/console/people`

| Field | Direction | Note |
|---|---|---|
| `email`, `phone` | both | Optional. Present on the row, absent from the API. |
| `valid_from`, `valid_until` | both | Contractors and visitors are the ordinary case, not the exception. Enforced by the permission engine, not by the console. |
| `category` | write | Must be **clearable**. It is a plain string today, so "" is indistinguishable from "leave alone" — a pointer, as `active` already is. |
| `categories` | read | The company's own vocabulary, so the console can offer a list instead of free text without inventing one. |

**Reason** `person_type` was a fixed four-value taxonomy from the single-purpose
product; `category` replaced it with free text, which is honest but gives the
console nothing to offer. A school's categories are not a factory's, and neither
is the platform's business — but the *company's* own set is, and a per-company
vocabulary is the difference between a free-text box and a usable control. The
console shows `category` as free text today and says nothing about validity
because there is nothing to say.

---

## PP-02 — Server-side people filters

**Audit finding** PPL-05 · **Depends on** PP-01 for `category`

**Changes to** `GET /api/v1/console/people`

Add `active`, `category`, `enrolled`, `site_id` alongside the existing `q`,
`limit` and `offset`.

**Reason** The list searches on the server, correctly — but "show me everybody
without a credential" is the question an operator actually asks before a rollout,
and the console cannot answer it. Filtering a fetched page in the browser would
filter the page rather than the roster: silently wrong past the first page, and
silently right in every test with three fixtures.

---

## TM-01 — Terminal paging, search, and the health projection

**Audit finding** CON-02, SYN-04

**Changes to** `GET /api/v1/console/terminals`

Add `limit`, `offset`, `q` (serial, name, site) and return the same
`{count,total,limit,offset,has_more,…}` envelope people already uses. The route
currently returns every terminal in the company with no bound.

**And serve the health projection that already exists.** `models.ConsoleTerminalHealth`
is defined — `pending_jobs`, `failed_jobs`, `last_apply_error`,
`last_apply_error_at`, `credential_active`, `offline_policy`,
`offline_grace_minutes` — and nothing populates or returns it. Adding it to
`GET /console/terminals/{serial}` needs no new type. **STILL OPEN.** The type is
still unrouted; `database.GetTerminalHealth` populates it and is called only
from a test.

**Capacity landed ahead of it (2026-08-16), and the console can render it now:**

| Field | Where | Meaning |
|---|---|---|
| `member_capacity` | terminal list and detail | People the terminal can hold, **as reported by it**. ABSENT means it has never said — render "unknown", never unlimited and never zero |
| `roster_size` | detail only | People its permissions currently cover |
| `over_capacity` | detail only | True only when a REPORTED capacity is exceeded |
| `roster_overflow_at` / `roster_overflow_count` | list and detail | When the roster last did not fit, and how big it was |

A `409` with `code: "ROSTER_EXCEEDS_TERMINAL_CAPACITY"` now comes back from
resync and from relocation, carrying `roster_size` and `capacity`. The dialog
should show both numbers: "over capacity" sends an operator to support, "holds
256, needs 312" sends them to a purchase order.

**Do not render a capacity for a terminal that has not reported one.** No
shipped firmware reports it yet, so today that is every terminal, and inventing
a figure would recreate the exact class of confident-but-wrong display this
document exists to prevent.

**Authorization** Unchanged: `VIEWER`, narrowed by grant.

**Reason** A fleet list with no upper bound is a page that degrades quietly as a
customer grows, and there is no way to find one terminal among two hundred.
`last_apply_error` is the more valuable half: SYN-01 records that a terminal
whose person table is full fails silently for ever, and the console currently
shows `last_sync_at` and nothing else — an operator cannot tell that people are
missing from a door. The terminal page has the space for it and no data.

---

## SET-01 — A declared schema for settings

**Audit finding** GP-04, GP-06, FW-08

**Endpoint**

```
GET /api/v1/console/settings-schema?scope=SITE|APPLICATION&code=
```

**Response** Per key: `key`, `type`, `label`, `description`, `default`,
constraints (`min`, `max`, `enum`, `pattern`), `required`, and — this is the one
that matters — **`implemented`**, whether the firmware or the platform actually
reads it.

**Authorization** `VIEWER`.

**Reason** Two open JSON objects with no schema: a site's device settings and a
capability's configuration. The console offers a guided editor over a
hard-coded list of keys it recognises plus a raw editor over everything, and
never discards a key it does not understand — which is correct and is the best
that can be done without a schema. `implemented` is the honest part: FW-08
records that two of the four exposed settings are inert, so the console is
currently offering controls that change nothing. A flag would let it say so
instead of the operator discovering it at a door.

**STILL OPEN, with two keys carved out (2026-08-16).** `offline_policy` and
`offline_grace_minutes` are now **refused** in the free-form object with a `400`
carrying `code: "RESERVED_SETTINGS_KEY"`, and every site read — the list, the
detail, and `GET /sites/settings` — states the values actually in force.

They were carved out ahead of the general mechanism because for those two, "the
console accepted it and nothing happened" was a *safety* failure: an operator
set a grace window, got a `200`, saw the number read back, and no door was ever
told. The other keys are merely unvalidated.

**For the raw editor:** the two keys must be rejected client-side with a pointer
to the site's policy control, not sent and refused, so the operator never sees
an error for a field the form let them type.

---

## APP-01 — Implementation status in the capability catalogue

**Audit finding** APP-01, GP-03, GP-05

**Changes to** `GET /api/v1/console/applications`

For each entry in `available`, add:

```json
{
  "code": "ATTENDANCE",
  "implementation": "NOT_IMPLEMENTED",   // NOT_IMPLEMENTED | PARTIAL | IMPLEMENTED
  "gap": "Nothing records attendance.",
  "requires": [],                        // GP-05: capability prerequisites
  "conflicts": []
}
```

**Reason** The console distinguishes four states — available, enabled,
configured, **operational** — and only the last one matters to a customer. It is
the only one not derivable from configuration, so this build carries the
implementation table itself (`src/applications/readiness.ts`), which is correct
today: it is a property of what code exists, not of a company's settings. It is
still the wrong long-term home, because a platform that gains a capability then
needs a frontend release to stop calling it unbuilt. `requires`/`conflicts` are
GP-05, currently answered on the detail page with "the platform does not record
relationships between capabilities" — which is better than an empty
"Dependencies:" heading implying the answer is none.

---

## CO-01 — Small gaps with no home elsewhere

| # | Finding | Endpoint | Change | Reason |
|---|---|---|---|---|
| CO-01a | CON-03 | `GET /console/sites`, `GET /console/sites/{id}` | Add `api_key_prefix` to the projection. | Non-secret, already read by `database.SiteKeyPrefix`, and it is the only way an operator can tell WHICH key a site is on. Still absent from `ConsoleSite` despite the register marking the finding fixed — see the note at the top. The console types the field optional and populates it from create/rotate responses only, so this is a one-line backend change with nothing to alter here. |
| CO-01b | GP-01 | `PUT /platform/companies/{id}` | Allow retention to be returned to **indefinite**. | The fields are `*int`, so absent means "leave alone" and there is no way to express null. The console's edit form says plainly that it cannot clear a retention period once set, which is an odd thing to have to tell somebody. |
| CO-01c | SEC-10 | `POST /auth/forgot-password` | Deliver the link. | The token reaches the operational log and nothing else, so a self-service reset completes only if somebody with log access finishes it by hand. The console says so on the confirmation screen rather than showing "check your inbox" for a message that is never coming. This is a deployment capability, not only code. |

---

## Firmware dependencies

Not backend requirements — these are firmware behaviours the console is
currently describing accurately and would describe differently if they changed.
Recorded so the console is updated in the same change rather than left stale.

| Finding | Firmware behaviour | What the console says today |
|---|---|---|
| FW-04 | Terminals never heartbeat, so `MarkDevicesOffline` never matches and every registered terminal reads `ONLINE` for ever | The terminal page shows last heartbeat and last seen as separate facts and treats "never" as its own state rather than as an error. It cannot detect that the status field is meaningless. **A status derived from a heartbeat that does not exist is the single most misleading value in the console**, and closing FW-04 is what makes it true. |
| FW-02 | No OTA | The firmware section says "AccessLink does not push firmware over the air — updating is a separate, deliberate operation", and the catalogue's `is_current` is described as what a fleet is MEASURED AGAINST rather than pushed to. |
| FW-03 | An enrolment binds to the terminal that took it | See CR-01. `REGISTRATION` is marked partly built with that as its named gap. |
| FW-05 | ~~Terminals cannot self-register~~ **Superseded: the claim flow is shipped on both halves.** | The terminal page's wording is now WRONG and should be updated by AI #3: first provisioning is an operator issuing a claim code in the console, and the installer typing it into the unit. The site's provisioning key never reaches the hardware, and as of 2026-08-16 presenting it with a serial no longer authenticates as a terminal at all (SEC-05). |
| FW-01 | A terminal's member table has a fixed ceiling, and the firmware does not report it | The console can show `member_capacity` and `roster_size` — see TM-01 — but **no shipped firmware sends the capacity**, so every terminal reads "unknown" today. When AI #2 lands the heartbeat field, the terminal page becomes able to say how much headroom a door has. |
| FW-07 | Revocation has no firmware-side effect until the terminal next reaches the server | The revoke dialog says the credential stops resolving; it does **not** claim the door stops opening at that instant, because an offline terminal holds a local roster. If FW-07 lands with a defined degraded policy, that dialog should say what it is. |

---

## What the console does NOT need

Recorded because they were considered and rejected, and somebody will propose
them again.

- **A platform route that reads inside a tenant.** Not "not yet" — not ever.
  Platform administration returns counts and nothing else, and the safest way not
  to leak a customer's biometric roster is to have no query that could load it.
- **An endpoint returning the site API key.** It exists in one response shape,
  from creation and rotation, and the database holds only a hash. A recovery
  route would undo that.
- **Server-rendered permission checks for the UI.** The console mirrors the
  server's role rules to avoid offering buttons that 403. It is not an
  authorization boundary and must not become one — if the two ever disagree, the
  server wins and the symptom is a button that errors, which is the correct
  failure.
