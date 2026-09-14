# Integrating an external application with AccessLink fingerprint authentication

This guide is for a team that runs its own membership, HR, student or visitor
system and wants AccessLink to do the fingerprint part: enrol people at a
terminal, recognise them at the door, and tell you what happened. Your
application stays the system of record for who those people are. AccessLink
holds only what a door needs.

Everything below describes the API **as it is implemented today**. Every
endpoint, field and status code was checked against the running code and
against [`API_SPEC.md`](../API_SPEC.md), which remains the authoritative
reference for the details this guide leaves out.

> **Read this first.** AccessLink does not currently expose a public API for
> uploading a fingerprint and asking AccessLink to match it, and no API call
> from your application makes a terminal capture a finger. Matching and capture
> happen on the AccessLink terminal. Your integration works through member
> synchronisation, terminal enrolment, access state and access logs — see
> [section 11](#11-limitation-no-remote-fingerprint-matching).

---

## 1. Overview

AccessLink provides:

- **Fingerprint enrolment** — a person places a finger on an AccessLink
  terminal; the terminal captures and stores the biometric template and reports
  to AccessLink that the person is enrolled.
- **Terminal synchronisation** — every person you create, update or delete is
  pushed to every terminal in your company through AccessLink's sync jobs, so a
  terminal knows who a fingerprint belongs to and whether they are active.
- **Access status** — an authorisation decision for a person at a specific
  terminal, evaluated by the same engine the terminal's own decisions use.
- **Access logging** — a record of every access attempt, granted or denied,
  whether it was recorded by a terminal or by your application.

Your application provides:

- the people (customers, members, staff) and their identifiers;
- the decisions about who should have access (you create, activate, deactivate
  and remove people in AccessLink to reflect them);
- whatever your own business does with the outcome (attendance, billing,
  reporting).

AccessLink never becomes the system of record for your people. It holds a
short identifier, a display name, a category, an active flag and — after
enrolment — a fingerprint credential that lives on the terminal.

---

## 2. Integration model

Four things need to be understood together.

| Concept | Owned by | What it is |
|---|---|---|
| **Your person / customer ID** | your application | The primary key in your own database. AccessLink never sees it unless you choose to use it as the `member_id`. |
| **AccessLink `member_id`** | you choose it; AccessLink stores it | The identifier AccessLink and every terminal use for a person. It is the value in every URL and every log line below. |
| **Fingerprint template** | the AccessLink terminal | The biometric material captured at enrolment. It is stored on the terminal's sensor and replicated between terminals by AccessLink. **It is never returned to you and you never send one.** |
| **Terminal / device** | AccessLink | The hardware at a door. It recognises fingerprints, decides admission from the roster AccessLink has synchronised to it, and reports the result. |

### Recommendation: use your own stable identifier as `member_id`

`member_id` is the join key between your system and AccessLink, so the simplest
robust integration is to make it **your own stable identifier** for the person
— a member number, an employee number, a student number. Then nothing has to be
mapped or stored on your side.

That works only if the value satisfies the constraint the terminal imposes.
`member_id` must be:

- **at most 31 characters**, and
- **printable ASCII with no spaces** (bytes `0x21`–`0x7E`).

This is a hardware limit, not a style rule: a terminal refuses an identifier it
cannot store rather than truncating it, because a truncated identifier is a
different person. The API enforces the same rule when a member is created and
answers `400` naming the problem, so a person you could never enrol is never
created. **A UUID does not fit** (36 characters). If your primary key is a UUID
or contains spaces, use a shorter, stable, unique value you already hold (a
membership number, a badge number) rather than a fresh mapping table.

`member_id` cannot be changed after creation. If your identifier can change,
choose a different one.

### Do not manage fingerprint templates yourself

The member endpoints accept an optional `fingerprint_template` field for legacy
reasons. **Do not send it.** It is a credential locator the terminal writes
through the enrolment flow; setting it from outside does not enrol anyone and
can leave AccessLink's record disagreeing with the hardware. Read
`biometric_enrolled` (a boolean) to know whether a person is enrolled, and let
the terminal do the rest.

---

## 3. Authentication

Every endpoint in this guide authenticates with the **site API key**, sent as a
header on every request:

```
X-API-Key: <SITE_API_KEY>
```

A site key identifies one **site** and, through it, your **company**
(tenant). Every query the API runs is scoped to that company, so a key issued
to one tenant cannot read or change another tenant's data. A resource that
belongs to another tenant is reported as `404`, not `403` — the API does not
confirm that an identifier exists in someone else's account.

Format: `ats_` followed by 64 hexadecimal characters (256 bits from a
cryptographic random source).

**How you obtain one.** Keys are issued and rotated from the AccessLink
operator console (`POST /api/v1/console/sites` when a site is created, and
`POST /api/v1/console/sites/{site_id}/api-key` to rotate). The key is shown
**once** in that response. AccessLink stores only a SHA-256 hash of it and has
no endpoint that reads it back. Lose it and you rotate it; rotation invalidates
the previous key immediately, with no overlap window.

**Treat the site key as a provisioning secret.** Besides the endpoints in this
guide, the same key authorises registering terminals at the site. Anyone
holding it can enrol hardware at that site. Keep it in a secrets manager, send
it only over HTTPS, and never embed it in a client that end users run.

**Development seed credentials are not production credentials.** The AccessLink
repository ships a development seed (`seeds/dev_seed.sql`) whose key appears in
examples in `API_SPEC.md`. It is public. It is not created by the production
migrations and must never be used against a production deployment.

Authentication failures:

| Condition | Status | Body |
|---|---|---|
| Header absent | `401` | `{"error":"API key required"}` |
| Key unknown, or its site has been deactivated | `401` | `{"error":"Invalid API key"}` |
| Database unreachable during authentication | `500` | `{"error":"Authentication unavailable"}` |

An outage is reported as `500`, never as `401` — do not discard a key because
one request answered `500`.

---

## 4. Member lifecycle

A **member** is a person AccessLink and its terminals know about. Four
operations cover the lifecycle. All of them take `Content-Type:
application/json` where a body is sent, and all are scoped to your company.

### The member object

Every read and write returns this shape:

```json
{
  "id": 1,
  "public_id": "de49b725-2b19-4e2a-bdc1-10be7402fdca",
  "member_id": "MEM001",
  "full_name": "Ada Lovelace",
  "membership_type": "ANNUAL",
  "active": true,
  "biometric_enrolled": false,
  "created_at": "2026-08-07T19:20:27.655424Z",
  "updated_at": "2026-08-07T19:20:27.655424Z"
}
```

| Field | Meaning |
|---|---|
| `member_id` | Your identifier for the person (section 2). Immutable. |
| `full_name` | Display name; shown on the terminal during enrolment. |
| `membership_type` | Free text; a category your organisation uses. Required, so send something meaningful to you (`STANDARD`, `STAFF`, `VISITOR`, …). |
| `active` | Whether terminals should admit this person. `false` is how you suspend somebody without removing them. |
| `biometric_enrolled` | `true` once a fingerprint has been captured for this person. Read-only; **this is the entire biometric surface of the object** — no template, no sensor detail is ever returned. |
| `id`, `public_id` | AccessLink's internal identifiers. You do not need them. |

### `GET /api/v1/members` — list

Every member in your company, newest first, as a JSON array (empty: `[]`).

Without parameters the list is **unpaginated** — it returns everybody. For a
large roster pass `?limit={n}` (1–1000) and `?offset={n}` to read it in
windows of the same newest-first order; the response is still a bare array, so
page until a call returns fewer rows than you asked for.

`GET /api/v1/members/{MEMBER_ID}` returns one member, or `404` `{"error":"Member
not found"}`.

### `POST /api/v1/members` — create

```json
{"member_id": "MEM001", "full_name": "Ada Lovelace", "membership_type": "ANNUAL", "active": true}
```

| Field | Required | Notes |
|---|---|---|
| `member_id` | yes | ≤ 31 chars, printable ASCII, no spaces (section 2) |
| `full_name` | yes | |
| `membership_type` | yes | |
| `active` | no | **Defaults to `false`.** Send `true` if the person should be admitted once enrolled. |

→ `201` with the member object (`biometric_enrolled` is `false`).

**Side effect:** a `CREATE` sync job is queued for every terminal in your
company, in the same transaction as the insert. The terminals learn the person
on their next poll.

| Error | Status |
|---|---|
| Missing required field | `400` (validator message) |
| `member_id` violates the constraint | `400` `{"error": "...", "field": "member_id"}` |
| `member_id` already exists in this company | `409` `{"error":"Member ID already exists"}` |

### `PUT /api/v1/members/{MEMBER_ID}` — update

```json
{"full_name": "Ada B. Lovelace", "membership_type": "MONTHLY", "active": true}
```

`member_id` comes from the URL and is not in the body. → `200` with the
updated member object; `404` if the member does not exist.

**This is a full replacement, and two consequences follow.**

1. `active` defaults to `false` when omitted. **Always send it**, or an update
   that only meant to fix a name will suspend the person.
2. `fingerprint_template` is replaced too, so **a `PUT` that omits it clears
   the person's enrolment**: `biometric_enrolled` becomes `false` and the
   terminals are told the person no longer has a credential. Because the
   template is never returned to you, there is no value you can send back to
   preserve it. Consequently:
   - prefer to set `full_name`, `membership_type` and `active` correctly at
     creation, before enrolment;
   - if you must change these fields after enrolment through this endpoint,
     plan to re-enrol the person;
   - if you hold integration credentials for AccessLink's public API
     (`/api/public/v1`, documented in `API_SPEC.md` §18), its `PATCH
     /api/public/v1/members/{member_id}` is a partial update that leaves the
     credential alone. That API is separate from the site key and is outside
     this guide.

**Side effect:** an `UPDATE` sync job is queued for every terminal.

### `DELETE /api/v1/members/{MEMBER_ID}` — remove

→ `200` `{"message":"Member deleted successfully"}`.

A soft delete: the record is retained for audit and the `member_id` becomes
available for reuse. **Side effect:** a `DELETE` sync job is queued for every
terminal — this is the only way a terminal learns to stop recognising the
person, so removing somebody from your system should always be mirrored here.

Deleting a member that does not exist (or was already deleted) also answers
`200` and queues nothing; repeated deletes are safe.

### How changes reach terminals

Create, update and delete each queue a **sync job** per terminal. Terminals
poll AccessLink for their jobs and acknowledge each one; a terminal that is
offline receives its backlog when it reconnects. There is no endpoint that
tells you when a particular terminal has applied a particular change, and no
callback. If you need to confirm, read the member back
(`GET /api/v1/members/{MEMBER_ID}`) for AccessLink's own state, and use the
operator console for per-terminal sync health.

---

## 5. Fingerprint enrolment

Enrolment is where the person, a terminal and AccessLink meet. Read this
section carefully, because the endpoint named "start" does less than its name
suggests:

> **`POST /api/v1/enrollment/start` creates AccessLink's enrolment request
> record. It does not start fingerprint capture at any terminal.** Capture
> happens only when an AccessLink operator, using the AccessLink console,
> directs one specific terminal to enrol the person while the person is
> standing at it. There is no site-key API call that does that.

### What your application can do

**`POST /api/v1/enrollment/start`** — record that an enrolment is wanted.

```json
{"member_id": "MEM001"}
```

→ `201`:

```json
{
  "message": "Enrollment request created",
  "member": { "...the member object..." },
  "request": {
    "id": 1,
    "public_id": "4d293fbf-cf79-452c-91d2-4ebc29134b57",
    "member_id": "MEM001",
    "status": "PENDING",
    "created_at": "2026-08-07T19:20:43.999771Z"
  }
}
```

| Error | Status |
|---|---|
| `member_id` missing | `400` |
| No such member in your company | `404` `{"error":"Member not found"}` |

Exactly what this does: it writes an enrolment request for the person with
status `PENDING`, addressed to **no terminal**. A person has at most one live
request; calling `start` again supersedes the earlier one rather than failing.
Nothing is sent to any terminal as a result of this call, and no terminal will
prompt for a finger because of it. Its practical use is bookkeeping: the
request is visible in `GET /api/v1/enrollment/pending` (site key) until an
enrolment for that person completes, when it is closed as `COMPLETED`.

> The `member` object in this response is AccessLink's internal member record
> and, for a person already enrolled, may carry a `fingerprint_template`
> value. It is a credential locator, not usable biometric data; treat it as
> opaque — do not store, log or forward it. Use `biometric_enrolled` instead.

**`GET /api/v1/enrollment/pending`** — the company's requests still in
`PENDING`, oldest first, as an array of the `request` shape above (empty:
`[]`).

### What has to happen for a terminal to capture the finger

This is the sequence the current AccessLink implementation runs. Steps 2–4
are performed by AccessLink and the operator, not by your application.

1. **Your application** creates the member (section 4). Optionally it calls
   `POST /api/v1/enrollment/start` to record the intent.
2. **An AccessLink operator**, with the person physically at a terminal, opens
   the AccessLink console and starts an enrolment for that person **at that
   terminal**. The console is an operator-session interface and is not part
   of the site-key API; the operator chooses the door because only they know
   which one the person is standing at.
3. **AccessLink** queues an `ENROLL_FINGERPRINT` sync job addressed to that
   one terminal. The terminal receives it on its next poll of its job queue
   (the same channel that delivers member create/update/delete), verifies the
   job is addressed to itself, and enters enrolment mode showing the person's
   name. The job carries a time window (default five minutes); if nobody
   places a finger, it expires and nothing is recorded.
4. **The terminal** captures the finger, stores the template on its own
   sensor, and reports the result to AccessLink with its own device
   credential: `POST /api/v1/devices/enrollment/result`. AccessLink then, in
   one transaction, marks the person `biometric_enrolled: true`, closes the
   person's open enrolment request(s) as `COMPLETED`, and queues an `UPDATE`
   sync job so the credential is replicated to the other terminals. It does
   **not** change `active` — a suspended person can be enrolled and stays
   suspended until you set `active: true`.

Two device-credential endpoints appear in that sequence,
`GET /api/v1/devices/enrollment/pending` and
`POST /api/v1/devices/enrollment/result`. They authenticate with the
terminal's `X-Device-Key`, which an integration does not hold, and they are
listed here only so you know what the hardware is doing. The terminal firmware
currently in service does not act on the pending list; it acts on the
terminal-addressed job from step 3.

### How your application knows it worked

**Success means `biometric_enrolled` is `true`** on the member. Read it with
`GET /api/v1/members/{MEMBER_ID}`. Until then it is `false`, and the request
(if you created one) is still listed by `GET /api/v1/enrollment/pending`.

AccessLink sends no notification and specifies no polling interval. Read the
member when your own workflow needs the answer — for example when the person
leaves the desk, or the next time your UI shows their status.

An enrolment that does not complete leaves the person exactly as they were:
present, `active` as you set it, `biometric_enrolled: false`. A failed or
abandoned capture changes nothing on the member record.

---

## 6. Access status and access logging

### What recognises the finger

**The terminal does.** When a person presents a finger, the AccessLink terminal
matches it against the templates it holds, decides admission from the roster
and rules AccessLink has synchronised to it, opens or does not open the door,
and reports the event to AccessLink under its own device credential. Your
application is not consulted in that decision, cannot take part in it, and has
no endpoint through which to submit a fingerprint for matching.

### `GET /api/v1/access/{MEMBER_ID}?terminal={SERIAL}` — access status (deprecated)

Answers the question *"would this person be admitted at this terminal right
now?"* from AccessLink's authorisation engine — the same evaluator the terminal
path uses. It evaluates the person's current state in AccessLink (existence,
`active`, credential state, permissions, schedules, validity windows, the
terminal's state and application mode, and whether the site and company are
in service). **It does not perform fingerprint recognition** and it does not
know whether the person is physically present.

`terminal` is **required**: an authorisation decision is about a person at a
specific door. The serial is resolved inside the authenticated site.

→ `200`:

```json
{
  "granted": false,
  "message": "Access Denied: no permission",
  "status": "NO_PERMISSION",
  "reason": "NO_PERMISSION",
  "member_id": "MEM001",
  "terminal": "AT-000123",
  "evaluated_at": "2026-08-16T09:14:00Z",
  "decided_by": "authorization_engine",
  "deprecated": true,
  "deprecated_by": "/api/v1/console/terminals/{serial}/evaluate"
}
```

Read `granted` and `reason`. `reason` is one of the engine's codes
(`ALLOWED`, `NO_PERMISSION`, `EXPLICIT_DENY`, `OUTSIDE_SCHEDULE`,
`PERMISSION_EXPIRED`, `PERMISSION_NOT_YET_VALID`, `PERSON_INACTIVE`,
`PERSON_UNKNOWN`, `CREDENTIAL_UNKNOWN`, `CREDENTIAL_REVOKED`,
`CREDENTIAL_SUSPENDED`, `CREDENTIAL_EXPIRED`, `CREDENTIAL_NOT_YET_VALID`,
`APPLICATION_NOT_ENABLED`, `TERMINAL_DISABLED`, `SITE_INACTIVE`,
`COMPANY_INACTIVE`, `OFFLINE_POLICY`). `message` is for humans and its wording
is not stable.

| Error | Status |
|---|---|
| `terminal` omitted | `400` `{"error": "...", "code": "TERMINAL_REQUIRED"}` |
| Serial not registered at the authenticated site | `404` `{"error":"Terminal not registered for this site"}` |

The endpoint is **deprecated**: responses carry `Deprecation: true` and a
`Link` header naming the successor, which takes an operator session rather
than a site key. It continues to work; build new integrations to tolerate its
removal (for example, by relying on the access log rather than on pre-checks).

### `POST /api/v1/access/log` — record an access attempt

Records an attempt your application observed or decided. Use it when your
system is the one that opened something — a gate you control from a
fingerprint result the terminal displayed, a manual override at a desk — so
that AccessLink's log is complete.

```json
{"member_id": "MEM001", "granted": true, "source": "fingerprint", "message": "front desk"}
```

| Field | Required | Notes |
|---|---|---|
| `source` | yes | Free text naming the credential or channel. Use `"fingerprint"` for a fingerprint recognition. |
| `granted` | no | Defaults to `false`. **A denial is valid and meaningful** — log those too. |
| `member_id` | no | Omit for an unrecognised credential; stored as null and matches nobody. |
| `message` | no | Free text. |
| `site_name` | no | Ignored; derived from the API key. |

→ `201` with the stored log entry:

```json
{
  "id": 1,
  "public_id": "dce8e5a5-fe0f-4115-a54f-9b6267c99dc3",
  "member_id": "MEM001",
  "granted": true,
  "source": "fingerprint",
  "site_name": "Main Site",
  "message": "front desk",
  "created_at": "2026-08-07T19:20:28.783609Z"
}
```

`400` if `source` is missing. Note that logging an attempt does not grant
anything and does not check the member — it records what you tell it.

### Reading the log

- `GET /api/v1/access/logs?limit={n}` — company-wide, newest first; `limit`
  defaults to 100 and is capped at 1000.
- `GET /api/v1/access/logs/{MEMBER_ID}?limit={n}` — the same, for one person.

Both return an array of the log entry shape above (empty: `[]`). Entries
recorded by terminals under their device credential appear here alongside the
ones your application writes.

---

## 7. End-to-end example

A gym's membership platform ("MemberBase") integrates with AccessLink. Which
side performs each step is stated on every line.

**1. MemberBase creates a customer.** Its own database row gets the membership
number `GYM-000482`. That number is 10 printable ASCII characters — it fits the
`member_id` constraint, so MemberBase uses it directly.

**2. MemberBase creates the AccessLink member** (your application → AccessLink):

```bash
curl -X POST "$BASE_URL/api/v1/members" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"member_id":"GYM-000482","full_name":"Ada Lovelace","membership_type":"ANNUAL","active":true}'
```

`201`. AccessLink queues a `CREATE` job to every terminal; within their next
poll the terminals know `GYM-000482` exists and is active, but nobody can be
recognised yet — there is no fingerprint.

**3. MemberBase records that enrolment is wanted** (your application →
AccessLink; optional, and it captures nothing):

```bash
curl -X POST "$BASE_URL/api/v1/enrollment/start" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"member_id":"GYM-000482"}'
```

`201`, `request.status` is `PENDING`. No terminal has been told anything yet.

**4. The finger is captured at a terminal** (AccessLink operator + terminal —
**not** MemberBase). At the front desk, an AccessLink operator uses the
AccessLink console to direct the terminal Ada is standing beside to enrol
`GYM-000482`. AccessLink queues the instruction to that terminal alone; the
terminal picks it up on its next poll, shows her name, she places her finger,
and the terminal captures the template and reports the result to AccessLink
with its own device credential. There is no API call MemberBase could make to
do this step.

**5. AccessLink records and synchronises the enrolment** (AccessLink). The
member becomes `biometric_enrolled: true`, the pending request is closed, and
an `UPDATE` job replicates the credential to the other terminals.

**6. MemberBase confirms** (your application → AccessLink):

```bash
curl "$BASE_URL/api/v1/members/GYM-000482" -H "X-API-Key: $SITE_API_KEY"
```

`200` with `"biometric_enrolled": true`. MemberBase marks the customer as
"fingerprint enrolled" in its own database.

**7. Ada arrives the next morning** (terminal). She presents her finger at the
door terminal. The terminal recognises it, checks her against the roster and
rules AccessLink has given it, admits her, and records the event with
AccessLink under its device credential. MemberBase is not consulted.

**8. MemberBase consumes the outcome** (your application → AccessLink). Its
attendance job reads recent entries:

```bash
curl "$BASE_URL/api/v1/access/logs/GYM-000482?limit=50" -H "X-API-Key: $SITE_API_KEY"
```

`200` with an array of entries; the newest shows `granted: true`, `source`
`FINGERPRINT` (as the terminal reported it) and the timestamp. MemberBase
records a visit.

**9. Ada's membership lapses** (your application → AccessLink). MemberBase
suspends her:

```bash
curl -X PUT "$BASE_URL/api/v1/members/GYM-000482" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"full_name":"Ada Lovelace","membership_type":"ANNUAL","active":false}'
```

Because `PUT` is a full replacement and no `fingerprint_template` is sent,
**this call also clears her enrolment** (section 4). If MemberBase wants
suspension to be reversible without a new capture at a terminal, it should not
suspend through `PUT`: keep her active in AccessLink and enforce the lapse on
its own side, or use one of the credential-preserving paths named in section
4. Otherwise, renewal means a `PUT` back to `active: true` **and** a fresh
enrolment at a terminal.

**10. Ada leaves for good** (your application → AccessLink):

```bash
curl -X DELETE "$BASE_URL/api/v1/members/GYM-000482" -H "X-API-Key: $SITE_API_KEY"
```

`200`. A `DELETE` job tells every terminal to forget her.

---

## 8. Data and security guidance

- **Never handle raw fingerprint data.** Do not build a path where a customer's
  fingerprint scan is sent to your application, and never ask AccessLink for a
  template. Enrolment and recognition happen on the terminal; your application
  only ever sees `biometric_enrolled: true/false` and log entries.
- **Do not send `fingerprint_template`** on create or update, and do not store
  or log it if it appears in a response (section 5). It is a locator the
  terminal owns, not data you can use.
- **Treat the site API key as a secret.** It also provisions hardware. Keep it
  server-side in a secrets store, rotate it from the console if it is ever
  exposed, and remember rotation cuts over immediately.
- **Use HTTPS in production.** The key travels in a header on every request.
- **Branch on HTTP status codes, never on error strings.** Every error is
  `{"error": "<message>"}`; the messages are for humans and are not stable.
  Where a machine-readable `code` is present (for example `TERMINAL_REQUIRED`)
  it is stable and may be used.
- **Tenant isolation is enforced by the credential.** Every request is scoped
  to the company the site key belongs to; you cannot reach, and will not be
  told about, anyone else's data. Do not send site or company identifiers to
  "select" a tenant — there is no such field and it would be ignored.
- **Log the request id.** Every response carries `X-Request-ID`. Keep it with
  your own logs; it is what AccessLink support will ask for.

---

## 9. Errors

| Status | Meaning for this integration |
|---|---|
| `400` | Malformed JSON, a missing required field, or a `member_id` the terminal cannot store. The body says which; some carry a stable `code`. Fix the request — do not retry it unchanged. |
| `401` | No `X-API-Key`, or the key is not recognised (including a key whose site has been deactivated). Check the credential; a key that answers `401` will keep answering `401`. |
| `403` | Not produced by the endpoints in this guide with a site key. Elsewhere in AccessLink it means a valid credential that is not allowed the action (an inactive device credential, or an operator without the role or CSRF token). If you see it, you are calling an endpoint outside this guide. |
| `404` | The member (or the terminal named in `?terminal=`) does not exist **in your company**. Anything belonging to another tenant is also `404`. |
| `409` | The `member_id` already exists in your company (`POST /api/v1/members`). Treat it as "already created", not as a retryable failure. |
| `500` | AccessLink or its database could not complete the request. Retry with backoff. During authentication this is reported as `500`, never `401`, so do not discard your key. |

`201` is returned by creates (`POST /api/v1/members`,
`POST /api/v1/enrollment/start`, `POST /api/v1/access/log`); `200` by
everything else, including `DELETE`.

---

## 10. API examples

All examples use three placeholders; substitute your own values and never
commit real credentials.

```bash
BASE_URL="https://api.your-accesslink-deployment.example"
SITE_API_KEY="ats_..."      # from the AccessLink console; keep it secret
MEMBER_ID="GYM-000482"      # your own stable identifier, <= 31 printable ASCII chars, no spaces
```

List members (unpaginated):
```bash
curl "$BASE_URL/api/v1/members" -H "X-API-Key: $SITE_API_KEY"
```

List members in pages of 200:
```bash
curl "$BASE_URL/api/v1/members?limit=200&offset=0" -H "X-API-Key: $SITE_API_KEY"
```

Read one member (and its `biometric_enrolled` flag):
```bash
curl "$BASE_URL/api/v1/members/$MEMBER_ID" -H "X-API-Key: $SITE_API_KEY"
```

Create a member:
```bash
curl -X POST "$BASE_URL/api/v1/members" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d "{\"member_id\":\"$MEMBER_ID\",\"full_name\":\"Ada Lovelace\",\"membership_type\":\"ANNUAL\",\"active\":true}"
```

Update a member (full replacement — always send `active`; see section 4 about
the enrolment):
```bash
curl -X PUT "$BASE_URL/api/v1/members/$MEMBER_ID" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"full_name":"Ada B. Lovelace","membership_type":"ANNUAL","active":true}'
```

Remove a member:
```bash
curl -X DELETE "$BASE_URL/api/v1/members/$MEMBER_ID" -H "X-API-Key: $SITE_API_KEY"
```

Record that an enrolment is wanted:
```bash
curl -X POST "$BASE_URL/api/v1/enrollment/start" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d "{\"member_id\":\"$MEMBER_ID\"}"
```

See which enrolments are still pending:
```bash
curl "$BASE_URL/api/v1/enrollment/pending" -H "X-API-Key: $SITE_API_KEY"
```

Ask whether a person would be admitted at a terminal right now (deprecated
endpoint; `terminal` is required):
```bash
curl "$BASE_URL/api/v1/access/$MEMBER_ID?terminal=AT-000123" -H "X-API-Key: $SITE_API_KEY"
```

Record an access attempt observed by your application:
```bash
curl -X POST "$BASE_URL/api/v1/access/log" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d "{\"member_id\":\"$MEMBER_ID\",\"granted\":true,\"source\":\"fingerprint\"}"
```

Read a person's recent access log:
```bash
curl "$BASE_URL/api/v1/access/logs/$MEMBER_ID?limit=50" -H "X-API-Key: $SITE_API_KEY"
```

---

## 11. Limitation: no remote fingerprint matching

> **AccessLink does not currently expose a public API where an external
> application uploads a fingerprint and asks AccessLink to perform 1:N
> fingerprint matching. Fingerprint matching occurs on the AccessLink terminal.
> The external integration works through member synchronisation, terminal
> enrolment, access state, and access logs.**

Concretely, this means:

- no endpoint accepts an image or template and returns "this is member X";
- no endpoint returns a person's template for you to match elsewhere;
- no endpoint makes a terminal capture a finger — `POST /api/v1/enrollment/start`
  records a request; the capture is directed by an operator from the console
  (section 5);
- `GET /api/v1/access/{member_id}` evaluates AccessLink's rules for a person
  you have already identified — it is not a recognition step;
- "who just presented a finger" is answered by the access log the terminal
  writes, after the fact, not by a call you make at the moment of presentation.

If your product needs a fingerprint reader that talks to your own application,
AccessLink terminals are not that reader. If it needs a door that recognises
your members and tells you who came in, the integration in this guide is the
one AccessLink supports.
