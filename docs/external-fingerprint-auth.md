# Using AccessLink fingerprint authentication from your own application

This guide is for a developer who already has an application with its own
people in it — a gym membership system, an HR system, a school or visitor
system — and wants AccessLink to handle the fingerprint side. Your application
stays in charge of who your people are. AccessLink handles enrolling their
fingerprints at a terminal, recognising them at the door, and telling you what
happened.

Everything here describes the API as it works today. Each endpoint, field and
status code was checked against the running code. [`API_SPEC.md`](../API_SPEC.md)
is the full reference for anything this guide leaves out.

> **Two things to know before you start.**
>
> 1. **You cannot send AccessLink a fingerprint and ask "who is this?".** There
>    is no public API for uploading a fingerprint for matching. Matching happens
>    on the AccessLink terminal itself.
> 2. **`POST /api/v1/enrollment/start` does not scan anyone's finger.** It only
>    records that an enrolment is wanted. The finger is captured when an
>    AccessLink operator, using the AccessLink console, tells one terminal to
>    enrol the person standing at it.
>
> Details are in [How enrolment really works](#5-fingerprint-enrolment) and
> [What the API does not do](#11-what-the-api-does-not-do).

---

## 1. How it works — the short version

1. **You create the person in AccessLink** with `POST /api/v1/members`, using
   your own member number as the `member_id`.
2. **AccessLink sends the person to every terminal** in your company. This
   happens by itself, in the background.
3. **The person's fingerprint is captured at a terminal.** An AccessLink
   operator, using the AccessLink console, tells the terminal the person is
   standing at to enrol them. The person places their finger. The terminal
   stores the fingerprint and tells AccessLink.
4. **You check that it worked** by reading the member with
   `GET /api/v1/members/{MEMBER_ID}`. When `biometric_enrolled` is `true`,
   enrolment is done. AccessLink copies the fingerprint to the other terminals.
5. **At the door, the terminal recognises the finger** and decides whether to
   let the person in. Your application is not involved in that moment.
6. **You read what happened** from the access log with
   `GET /api/v1/access/logs/{MEMBER_ID}`, and you can add your own entries
   with `POST /api/v1/access/log`.
7. **When someone leaves or is suspended**, you update or delete the member
   and every terminal is told.

The rest of this guide explains each step, the exact requests, and the things
that can catch you out.

---

## 2. Who owns what

| Thing | Who owns it | What it is |
|---|---|---|
| **Your customer / member record** | your application | Your own database row. AccessLink never sees it unless you use its number as the `member_id`. |
| **`member_id`** | you choose it, AccessLink stores it | The one identifier AccessLink and the terminals use for a person. It appears in every URL and every log entry. |
| **The fingerprint** | the AccessLink terminal | Captured and stored on the terminal's sensor, and copied between terminals by AccessLink. **It is never sent to you and you never send it.** |
| **The terminal** | AccessLink | The device at the door. It recognises fingerprints, decides who gets in using the list AccessLink has sent it, and reports each attempt. |

### Use your own member number as `member_id`

The easiest and most reliable setup is to make `member_id` the same stable
number you already use for the person — a membership number, an employee
number, a student number. Then there is nothing to map or look up.

The terminal puts limits on what `member_id` can be:

- **at most 31 characters**, and
- **only printable ASCII characters, with no spaces** (bytes `0x21`–`0x7E`).

These are hardware limits. A terminal refuses an identifier it cannot store,
because a shortened identifier would be a different person. AccessLink checks
the same rule when you create a member and answers `400` if it fails, so you
never create a person a terminal could not accept. **A UUID is too long**
(36 characters). If your primary key is a UUID or has spaces, use another
stable, unique value you already hold, such as a membership or badge number.

`member_id` cannot be changed after the member is created. If your number can
change, pick one that cannot.

### Leave fingerprint data alone

The member endpoints accept an optional `fingerprint_template` field for
historical reasons. **Do not send it.** It is an internal value the terminal
writes during enrolment. Setting it yourself does not enrol anyone and can put
AccessLink's records out of step with the hardware. To know whether a person is
enrolled, read the `biometric_enrolled` flag (`true` or `false`) and let the
terminal do the rest.

---

## 3. Authentication: the site API key

Every request in this guide carries your **site API key** in a header:

```
X-API-Key: <SITE_API_KEY>
```

A site API key belongs to one **site** and, through it, to your **company**.
Every request is limited to your company's data. A key from one company cannot
see or change another company's data. If you ask for something that belongs
to a different company, you get `404` (not found), not `403`, so the API never
confirms that someone else's record exists.

A key looks like `ats_` followed by 64 hexadecimal characters.

**Getting a key.** Keys are created and rotated in the AccessLink operator
console: creating a site (`POST /api/v1/console/sites`) returns one, and
`POST /api/v1/console/sites/{site_id}/api-key` issues a replacement. The key
is shown **once**. AccessLink stores only a hash of it and has no way to show
it again. If you lose it, rotate it. Rotation cancels the old key immediately,
with no overlap period.

**Keep it secret.** The same key is also used to set up terminals at the site,
so anyone holding it can add hardware to your site. Store it in a secrets
manager, send it only over HTTPS, and never put it in an app that end users
run.

**Do not use the development key.** The AccessLink source code ships with a
development seed (`seeds/dev_seed.sql`) whose key appears in examples in
`API_SPEC.md`. It is public. It is not created in production and will not
work there.

When authentication fails:

| What happened | Status | Body |
|---|---|---|
| No `X-API-Key` header | `401` | `{"error":"API key required"}` |
| Key unknown, or its site has been deactivated | `401` | `{"error":"Invalid API key"}` |
| AccessLink's database was unreachable | `500` | `{"error":"Authentication unavailable"}` |

A `500` here means an outage, not a bad key. Do not discard your key because
of one `500`.

---

## 4. Members: create, read, update, delete

A **member** is a person that AccessLink and its terminals know about. Send
`Content-Type: application/json` with any request that has a body.

### What a member looks like

Every read and write returns this:

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
| `member_id` | Your identifier for the person (section 2). Cannot be changed. |
| `full_name` | The person's name. The terminal shows it during enrolment. |
| `membership_type` | Any text that means something to you (`STANDARD`, `STAFF`, `VISITOR`, …). Required. |
| `active` | Whether terminals should let this person in. Set it to `false` to suspend someone without deleting them. |
| `biometric_enrolled` | `true` once a fingerprint has been captured for this person. Read-only. This is the only fingerprint-related information you ever receive. |
| `id`, `public_id` | AccessLink's internal identifiers. You do not need them. |

### List members — `GET /api/v1/members`

Returns every member in your company, newest first, as a JSON array (`[]` if
there are none).

With no parameters it returns **everyone**. For a large list, add
`?limit={n}` (1 to 1000) and `?offset={n}` to read it in pages. The response
is still a plain array, so keep paging until a call returns fewer members than
you asked for.

`GET /api/v1/members/{MEMBER_ID}` returns one member, or
`404 {"error":"Member not found"}`.

### Create a member — `POST /api/v1/members`

```json
{"member_id": "MEM001", "full_name": "Ada Lovelace", "membership_type": "ANNUAL", "active": true}
```

| Field | Required | Notes |
|---|---|---|
| `member_id` | yes | 31 characters or fewer, printable ASCII, no spaces (section 2) |
| `full_name` | yes | |
| `membership_type` | yes | |
| `active` | no | **Defaults to `false`.** Send `true` if the person should be let in once enrolled. |

Returns `201` with the member (`biometric_enrolled` is `false`).

What happens next: AccessLink queues a `CREATE` sync job for every terminal in
your company. Each terminal picks it up the next time it checks in.

| Problem | Status |
|---|---|
| A required field is missing | `400` |
| `member_id` breaks the rules above | `400 {"error": "...", "field": "member_id"}` |
| `member_id` already exists in your company | `409 {"error":"Member ID already exists"}` |

### Update a member — `PUT /api/v1/members/{MEMBER_ID}`

```json
{"full_name": "Ada B. Lovelace", "membership_type": "MONTHLY", "active": true}
```

`member_id` is in the URL, not the body. Returns `200` with the updated
member, or `404` if the member does not exist. AccessLink queues an `UPDATE`
sync job for every terminal.

> **Warning: `PUT` replaces the whole member, including the fingerprint.**
>
> - If you leave out `active`, it becomes `false` and the person is suspended.
>   **Always send `active`.**
> - If you leave out `fingerprint_template` — which you should never send —
>   **the person's enrolment is cleared.** `biometric_enrolled` becomes `false`
>   and the terminals are told the person no longer has a fingerprint. Because
>   AccessLink never gives you the fingerprint value, there is nothing you can
>   send back to keep it.
>
> So: get `full_name`, `membership_type` and `active` right when you create the
> person, before enrolment. If you must change them later through this
> endpoint, plan to enrol the person again. Two other ways to change a member
> keep the fingerprint: the AccessLink operator console, and the separate
> public API's `PATCH /api/public/v1/members/{member_id}` (see `API_SPEC.md`
> section 18), which uses its own API credentials and is outside this guide.

### Delete a member — `DELETE /api/v1/members/{MEMBER_ID}`

Returns `200 {"message":"Member deleted successfully"}`.

This is a soft delete: AccessLink keeps the record for its audit history, and
the `member_id` can be used again. AccessLink queues a `DELETE` sync job for
every terminal. **This is the only way a terminal learns to stop recognising
someone**, so when you remove a person from your system, delete them here too.

Deleting a member that does not exist, or was already deleted, also returns
`200` and does nothing. Repeating a delete is safe.

### How changes reach the terminals

Every create, update and delete queues a sync job for each terminal. Terminals
regularly check in with AccessLink, collect their jobs, apply them, and confirm
each one. A terminal that is offline gets its backlog when it reconnects.

There is no endpoint or callback that tells you when a particular terminal has
applied a particular change. To confirm AccessLink's own record, read the
member back with `GET /api/v1/members/{MEMBER_ID}`. Per-terminal sync status
is visible in the AccessLink operator console.

---

## 5. Fingerprint enrolment

This is where people most often expect the API to do more than it does, so
here is the plain statement first:

> **`POST /api/v1/enrollment/start` records that an enrolment is wanted. It
> does not scan a finger and it does not make any terminal start scanning.**
> The finger is captured only when an AccessLink operator, using the AccessLink
> console, tells one specific terminal to enrol the person standing at it.
> There is no site-API-key call that does that.

### What your application can do

**Record that an enrolment is wanted — `POST /api/v1/enrollment/start`**

```json
{"member_id": "MEM001"}
```

Returns `201`:

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

| Problem | Status |
|---|---|
| `member_id` missing | `400` |
| No such member in your company | `404 {"error":"Member not found"}` |

What this actually does: it saves an enrolment request for the person with
status `PENDING`. The request is not tied to any terminal, and no terminal is
told about it. A person has at most one open request; calling `start` again
replaces the earlier one instead of failing. The request stays open until an
enrolment for that person completes, when it is marked `COMPLETED`. Its
practical use is record-keeping.

The `member` object in this response is AccessLink's internal member record.
For a person who is already enrolled it may include a `fingerprint_template`
value. Treat it as an internal value: do not store it, log it, or pass it on.
Use `biometric_enrolled` instead.

**See open requests — `GET /api/v1/enrollment/pending`**

Returns the company's requests still in `PENDING`, oldest first, as an array of
the `request` objects shown above (`[]` if there are none).

### What has to happen for the terminal to capture the finger

This is the sequence AccessLink runs today. Only step 1 is yours.

1. **Your application** creates the member (section 4). Optionally, it calls
   `POST /api/v1/enrollment/start` to record the intent.
2. **An AccessLink operator**, with the person standing at a terminal, opens
   the AccessLink console and starts an enrolment for that person **at that
   terminal**. The console is a separate, operator-login interface, not part
   of the site-API-key API. The operator chooses the terminal because only
   they know which door the person is standing at.
3. **AccessLink** queues an `ENROLL_FINGERPRINT` sync job for that one
   terminal. The terminal collects it on its next check-in (the same channel
   that delivers member changes), checks that the job is meant for it, and
   switches to enrolment mode showing the person's name. The job has a time
   limit (five minutes by default). If nobody places a finger in time, it
   expires and nothing is recorded.
4. **The terminal** captures the finger, stores the fingerprint on its own
   sensor, and reports the result to AccessLink using the terminal's own device
   key (`POST /api/v1/devices/enrollment/result`). AccessLink then, in one
   step, marks the person `biometric_enrolled: true`, marks the person's open
   enrolment request `COMPLETED`, and queues an `UPDATE` sync job so the
   fingerprint is copied to the other terminals. It does **not** change
   `active`. A suspended person can be enrolled and stays suspended until you
   set `active: true`.

Two terminal-only endpoints appear in that sequence:
`GET /api/v1/devices/enrollment/pending` and
`POST /api/v1/devices/enrollment/result`. They use the terminal's own device
key (`X-Device-Key`), which your application does not have. They are listed
here only so you know what the hardware is doing. The terminal software
currently in use does not act on the pending list; it acts on the job from
step 3.

### How you know it worked

**Enrolment has succeeded when `biometric_enrolled` is `true`** on the member.
Read it with `GET /api/v1/members/{MEMBER_ID}`. Until then it is `false`, and
your request (if you made one) is still listed by
`GET /api/v1/enrollment/pending`.

AccessLink does not send notifications and does not define how often to check.
Read the member when your own workflow needs the answer — for example when the
person leaves the front desk, or the next time your screen shows their status.

If an enrolment does not complete, nothing changes on the member: they are
still there, `active` is whatever you set, and `biometric_enrolled` stays
`false`.

---

## 6. Access: who checks the finger, and what you can read

### The terminal recognises the finger, not the API

When someone presents a finger, the AccessLink terminal matches it against the
fingerprints it holds, checks the person against the list and rules AccessLink
has sent it, opens the door or not, and reports the event to AccessLink using
its own device key. Your application is not asked, cannot take part, and has no
endpoint to submit a fingerprint for matching.

### Would this person be let in? — `GET /api/v1/access/{MEMBER_ID}?terminal={SERIAL}` (deprecated)

Asks AccessLink's own decision engine — the same one the terminal's decisions
use — whether the person would be let in at that terminal right now. It looks
at AccessLink's current records for the person: whether they exist, are
`active`, are enrolled, their permissions, schedules and validity dates, the
terminal's state and mode, and whether the site and company are in service.

**It does not recognise a fingerprint** and it does not know whether the
person is physically there.

`terminal` is **required**. The decision depends on which door it is. The
serial must belong to the site your key is for.

Returns `200`:

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

Use `granted` and `reason`. `reason` is one of: `ALLOWED`, `NO_PERMISSION`,
`EXPLICIT_DENY`, `OUTSIDE_SCHEDULE`, `PERMISSION_EXPIRED`,
`PERMISSION_NOT_YET_VALID`, `PERSON_INACTIVE`, `PERSON_UNKNOWN`,
`CREDENTIAL_UNKNOWN`, `CREDENTIAL_REVOKED`, `CREDENTIAL_SUSPENDED`,
`CREDENTIAL_EXPIRED`, `CREDENTIAL_NOT_YET_VALID`, `APPLICATION_NOT_ENABLED`,
`TERMINAL_DISABLED`, `SITE_INACTIVE`, `COMPANY_INACTIVE`, `OFFLINE_POLICY`.
`message` is for people to read and its wording may change.

| Problem | Status |
|---|---|
| `terminal` left out | `400 {"error": "...", "code": "TERMINAL_REQUIRED"}` |
| That serial is not registered at your site | `404 {"error":"Terminal not registered for this site"}` |

This endpoint is **deprecated**. Responses include a `Deprecation: true`
header and a `Link` header pointing to its replacement, which needs an
operator login rather than a site API key. It still works today. For new
integrations, prefer reading the access log over checking in advance.

### Record an access attempt — `POST /api/v1/access/log`

Use this when your application is the one that let someone through or turned
them away — for example a gate your system controls, or a manual override at
the front desk — so AccessLink's log is complete.

```json
{"member_id": "MEM001", "granted": true, "source": "fingerprint", "message": "front desk"}
```

| Field | Required | Notes |
|---|---|---|
| `source` | yes | Free text naming how the person was identified. Use `"fingerprint"` for a fingerprint. |
| `granted` | no | Defaults to `false`. **Logging a refusal is valid and useful.** |
| `member_id` | no | Leave it out for an unrecognised person; it is stored as empty and matches nobody. |
| `message` | no | Free text. |
| `site_name` | no | Ignored. AccessLink fills it in from your API key. |

Returns `201` with the saved entry:

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

`400` if `source` is missing. Logging an attempt only records it. It does not
let anyone in and does not check the member.

### Read the log

- `GET /api/v1/access/logs?limit={n}` — your whole company, newest first.
  `limit` defaults to 100 and is capped at 1000.
- `GET /api/v1/access/logs/{MEMBER_ID}?limit={n}` — the same, for one person.

Both return an array of entries in the shape above (`[]` if none). Entries
recorded by terminals appear here alongside the ones your application writes.

---

## 7. A complete example

A gym's membership system, "MemberBase", uses AccessLink for the doors. Each
step says who does it.

**1. MemberBase creates a customer.** Its own database gives her membership
number `GYM-000482`. That is 10 printable characters with no spaces, so it can
be the `member_id` as it is.

**2. MemberBase creates the AccessLink member** (MemberBase → AccessLink):

```bash
curl -X POST "$BASE_URL/api/v1/members" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"member_id":"GYM-000482","full_name":"Ada Lovelace","membership_type":"ANNUAL","active":true}'
```

`201`. AccessLink queues a `CREATE` job for every terminal. After their next
check-in the terminals know `GYM-000482` exists and is active, but they cannot
recognise her yet because there is no fingerprint.

**3. MemberBase records that an enrolment is wanted** (MemberBase →
AccessLink; optional, and it does not scan anything):

```bash
curl -X POST "$BASE_URL/api/v1/enrollment/start" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"member_id":"GYM-000482"}'
```

`201`, with `request.status` `PENDING`. No terminal has been told anything.

**4. Ada's finger is captured at a terminal** (AccessLink operator and the
terminal — **not** MemberBase). At the front desk, an AccessLink operator uses
the AccessLink console to tell the terminal Ada is standing at to enrol
`GYM-000482`. AccessLink sends the instruction to that terminal only. The
terminal picks it up on its next check-in, shows her name, she places her
finger, and the terminal captures the fingerprint and reports back to
AccessLink with its own device key. There is no API call MemberBase could make
to do this step.

**5. AccessLink records and copies the enrolment** (AccessLink). Ada becomes
`biometric_enrolled: true`, her pending request is marked `COMPLETED`, and an
`UPDATE` job copies the fingerprint to the other terminals.

**6. MemberBase confirms** (MemberBase → AccessLink):

```bash
curl "$BASE_URL/api/v1/members/GYM-000482" -H "X-API-Key: $SITE_API_KEY"
```

`200` with `"biometric_enrolled": true`. MemberBase marks her as "fingerprint
enrolled" in its own database.

**7. Ada arrives the next morning** (the terminal). She places her finger on
the door terminal. The terminal recognises it, checks her against the list and
rules AccessLink gave it, lets her in, and records the event with AccessLink
using its device key. MemberBase is not involved.

**8. MemberBase reads what happened** (MemberBase → AccessLink). Its
attendance job reads her recent entries:

```bash
curl "$BASE_URL/api/v1/access/logs/GYM-000482?limit=50" -H "X-API-Key: $SITE_API_KEY"
```

`200` with an array. The newest entry shows `granted: true`, a `source` of
`FINGERPRINT` (as the terminal reported it) and the time. MemberBase records a
visit.

**9. Ada's membership lapses** (MemberBase → AccessLink). MemberBase suspends
her:

```bash
curl -X PUT "$BASE_URL/api/v1/members/GYM-000482" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"full_name":"Ada Lovelace","membership_type":"ANNUAL","active":false}'
```

Because `PUT` replaces the whole member and no `fingerprint_template` is sent,
**this also clears her enrolment** (section 4). If MemberBase wants to be able
to reinstate her without a new fingerprint capture, it should not suspend
through `PUT`: it can keep her active in AccessLink and enforce the lapse on
its own side, or use one of the fingerprint-preserving routes named in
section 4. Otherwise, reinstating her means a `PUT` back to `active: true`
**and** a fresh enrolment at a terminal.

**10. Ada leaves for good** (MemberBase → AccessLink):

```bash
curl -X DELETE "$BASE_URL/api/v1/members/GYM-000482" -H "X-API-Key: $SITE_API_KEY"
```

`200`. A `DELETE` job tells every terminal to forget her.

---

## 8. Security and data handling

- **Never handle fingerprint data yourself.** Do not build anything where a
  customer's fingerprint scan is sent to your application, and never ask
  AccessLink for a fingerprint. Enrolment and recognition happen on the
  terminal. All you ever see is `biometric_enrolled: true` or `false` and log
  entries.
- **Do not send `fingerprint_template`**, and do not store or log it if it
  appears in a response (section 5). It is an internal value the terminal
  owns.
- **Treat the site API key as a secret.** It also lets someone add terminals
  to your site. Keep it server-side in a secrets store. Rotate it from the
  console if it is ever exposed; rotation takes effect at once.
- **Use HTTPS in production.** The key is sent in a header on every request.
- **Branch on HTTP status codes, not on error text.** Every error is
  `{"error": "<message>"}`. The messages are for people and may change. Where
  a `code` field is present (for example `TERMINAL_REQUIRED`) it is stable and
  safe to use.
- **Your data is separated from other companies' by your key.** Every request
  is limited to your company. There is no field to pick a company or site; if
  you send one, it is ignored.
- **Keep the request ID.** Every response carries an `X-Request-ID` header.
  Save it with your own logs; AccessLink support will ask for it.

---

## 9. Status codes

| Status | What it means for you |
|---|---|
| `400` | The request was malformed, a required field was missing, or the `member_id` breaks the terminal's rules. The body says which, and some carry a stable `code`. Fix the request; do not retry it as it is. |
| `401` | No `X-API-Key`, or the key is not recognised (including a key whose site was deactivated). A key that gets `401` will keep getting `401`. |
| `403` | Not returned by the endpoints in this guide when used with a site API key. Elsewhere in AccessLink it means a valid login that is not allowed to do something. If you see it, you are calling an endpoint outside this guide. |
| `404` | The member (or the terminal in `?terminal=`) does not exist **in your company**. Records belonging to other companies also return `404`. |
| `409` | The `member_id` already exists in your company (`POST /api/v1/members`). Treat it as "already created", not as something to retry. |
| `500` | AccessLink or its database could not complete the request. Retry later with a delay. During authentication a `500` is an outage, not a bad key. |

`201` comes back from the three creates (`POST /api/v1/members`,
`POST /api/v1/enrollment/start`, `POST /api/v1/access/log`). Everything else,
including `DELETE`, returns `200`.

---

## 10. Copy-and-paste examples

Set these three values first. Never commit a real key.

```bash
BASE_URL="https://api.your-accesslink-deployment.example"
SITE_API_KEY="ats_..."      # from the AccessLink console; keep it secret
MEMBER_ID="GYM-000482"      # your own stable number: 31 characters or fewer, no spaces
```

List all members:
```bash
curl "$BASE_URL/api/v1/members" -H "X-API-Key: $SITE_API_KEY"
```

List members 200 at a time:
```bash
curl "$BASE_URL/api/v1/members?limit=200&offset=0" -H "X-API-Key: $SITE_API_KEY"
```

Read one member (and see whether they are enrolled):
```bash
curl "$BASE_URL/api/v1/members/$MEMBER_ID" -H "X-API-Key: $SITE_API_KEY"
```

Create a member:
```bash
curl -X POST "$BASE_URL/api/v1/members" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d "{\"member_id\":\"$MEMBER_ID\",\"full_name\":\"Ada Lovelace\",\"membership_type\":\"ANNUAL\",\"active\":true}"
```

Update a member (replaces everything — always send `active`; see the warning
in section 4 about the fingerprint):
```bash
curl -X PUT "$BASE_URL/api/v1/members/$MEMBER_ID" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d '{"full_name":"Ada B. Lovelace","membership_type":"ANNUAL","active":true}'
```

Delete a member:
```bash
curl -X DELETE "$BASE_URL/api/v1/members/$MEMBER_ID" -H "X-API-Key: $SITE_API_KEY"
```

Record that an enrolment is wanted (does not scan anything):
```bash
curl -X POST "$BASE_URL/api/v1/enrollment/start" \
  -H "X-API-Key: $SITE_API_KEY" -H "Content-Type: application/json" \
  -d "{\"member_id\":\"$MEMBER_ID\"}"
```

See which enrolment requests are still open:
```bash
curl "$BASE_URL/api/v1/enrollment/pending" -H "X-API-Key: $SITE_API_KEY"
```

Ask whether a person would be let in at a terminal right now (deprecated;
`terminal` is required):
```bash
curl "$BASE_URL/api/v1/access/$MEMBER_ID?terminal=AT-000123" -H "X-API-Key: $SITE_API_KEY"
```

Record an access attempt your application handled:
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

## 11. What the API does not do

> **AccessLink does not currently expose a public API where an external
> application uploads a fingerprint and asks AccessLink to perform 1:N
> fingerprint matching. Fingerprint matching occurs on the AccessLink terminal.
> The external integration works through member synchronisation, terminal
> enrolment, access state, and access logs.**

In practice:

- no endpoint takes a fingerprint image or template and answers "this is
  member X";
- no endpoint gives you a person's fingerprint to match somewhere else;
- no endpoint makes a terminal capture a finger — `POST /api/v1/enrollment/start`
  only records a request; the capture is started by an operator from the
  console (section 5);
- `GET /api/v1/access/{member_id}` checks AccessLink's rules for a person you
  have already identified; it is not fingerprint recognition;
- "who just presented a finger" is answered by the access log the terminal
  writes, after the event, not by a call you make at that moment.

If your product needs a fingerprint reader that talks directly to your own
application, AccessLink terminals are not that reader. If it needs a door that
recognises your members and tells you who came in, this guide describes the
integration AccessLink supports.

---

## 12. Implementation details worth knowing

These are the finer points behind the sections above, collected in one place.

- **Sync jobs.** Creating, updating or deleting a member queues one job per
  terminal in the same database transaction as the change itself, so a saved
  change is always on its way to the terminals. Terminals collect jobs when
  they check in and confirm each one; a terminal that is offline receives its
  backlog later.
- **One open enrolment request per person.** `POST /api/v1/enrollment/start`
  replaces any earlier open request for the same person rather than failing.
- **Enrolment never changes `active`.** A completed enrolment sets
  `biometric_enrolled` only. Suspended people can be enrolled and stay
  suspended.
- **Enrolment failures leave no trace on the member.** A capture that fails,
  expires or is cancelled changes nothing on the member record.
- **Terminal software and the pending list.** The terminal software currently
  in use collects enrolment instructions as terminal-specific jobs created from
  the operator console. It does not act on the company-wide list returned by
  `GET /api/v1/devices/enrollment/pending`.
- **The `member` object in the `enrollment/start` response** is AccessLink's
  internal record and may include `fingerprint_template` for an enrolled
  person. It is an internal locator, not biometric data, but do not keep it.
- **Soft deletes.** A deleted member is kept in AccessLink's records for audit
  purposes; the `member_id` becomes free to reuse.
- **`GET /api/v1/access/{member_id}` is deprecated** and its replacement
  requires an operator login. Build new integrations so they still work if it
  is removed — reading the access log is the durable approach.
