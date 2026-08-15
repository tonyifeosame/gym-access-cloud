# Access Terminal Cloud API — Specification

The contract between the cloud backend, the ESP32 terminal firmware, and the
React dashboard. Every example below was captured from a running server, not
written by hand.

- **Base URL:** `http://<host>:8080`
- **Sync protocol version:** `1`
- **Content type:** `application/json` for all request and response bodies
  (except `GET /metrics`, which is Prometheus text)

**Related documents**

- [docs/sync-protocol.md](docs/sync-protocol.md) — device sync semantics, job
  types, idempotency rules
- [docs/operations.md](docs/operations.md) — monitoring, maintenance, env config

---

## Table of contents

1. [Authentication](#1-authentication)
2. [Conventions](#2-conventions)
3. [Members](#3-members)
4. [Access](#4-access)
5. [Enrollment](#5-enrollment)
6. [Site settings](#6-site-settings)
7. [Devices — operator](#7-devices--operator)
8. [Firmware](#8-firmware)
9. [Devices — device-authenticated](#9-devices--device-authenticated)
10. [Health and monitoring](#10-health-and-monitoring)
11. [Endpoint index](#11-endpoint-index)
12. [Operator authentication](#12-operator-authentication)
13. [Operator console](#13-operator-console)
14. [Applications and modules](#14-applications-and-modules)
15. [Authorization — permissions and schedules](#15-authorization--permissions-and-schedules)
16. [Events — the activity trail](#16-events--the-activity-trail)
17. [Device protocol additions](#17-device-protocol-additions)

---

## 1. Authentication

There are four authentication modes. Which one applies is stated on every
endpoint below.

| Mode | Credential | Who uses it |
|---|---|---|
| Site API key | `X-API-Key` | provisioning and server-to-server tooling |
| Device key | `X-Device-Key` | one terminal |
| Site key + serial | `X-API-Key` + `X-Device-Serial` | deprecated terminal fallback |
| **Operator session** | `__Host-al_session` cookie | the browser dashboard |

**A browser must use an operator session, never a site API key.** The site key
is the *provisioning secret*: whoever holds it can register a terminal and rotate
any device credential at that site. It is never returned by any operator-session
endpoint, and it authenticates nothing under `/api/v1/auth` or
`/api/v1/console`. See [section 12](#12-operator-authentication).

### Site API key

```
X-API-Key: main-site-api-key-123
```

Identifies a **site**, and through it a company. Used for provisioning and by
server-to-server tooling — not by a browser. Every query is scoped to that site's
company — a key issued to one tenant cannot reach another's data.

The site key is also the **provisioning secret**: it is what authorises
`POST /devices/register`, which mints device credentials. Treat it accordingly —
anyone holding it can enrol a terminal at that site.

**Stored as a SHA-256 hash and not recoverable.** Since migration 011 the
database holds no plaintext, so a key exists exactly twice: in the response that
created or rotated it, and wherever the operator put it. There is no endpoint and
no query that reads one back. Lost it? Rotate:
`POST /api/v1/console/sites/{site_id}/api-key`.

The format is `ats_` followed by 64 hex characters — 256 bits from `crypto/rand`.
The `ats_` prefix distinguishes it at a glance from a device key (`atd_`), which
matters when one is pasted where the other belongs.

> The key shown above is the development seed from `seeds/dev_seed.sql`. It is
> committed to the repository and therefore public. It is **not** created by the
> migrations: a database built from `migrations/` alone has no sites and no
> credentials until an operator creates one.

Failure responses:

| Condition | Status | Body |
|---|---|---|
| Header absent | `401` | `{"error":"API key required"}` |
| Key unknown | `401` | `{"error":"Invalid API key"}` |
| Site marked inactive | `401` | `{"error":"Invalid API key"}` |
| Database unreachable | `500` | `{"error":"Authentication unavailable"}` |

An outage is reported as `500`, never as `401`. A client told its credential is
invalid may discard a key that was fine.

### Device key

```
X-Device-Key: atd_d4fadeddd3b3be3e82fc4019fc594d32ed92145d5ab35731ca4f61c6692a6168
```

Identifies a single **device**. Issued once by `POST /devices/register` and
stored server-side only as a SHA-256 hash — it cannot be recovered. A device
that loses it re-registers and receives a new one.

| Condition | Status | Body |
|---|---|---|
| No credential | `401` | `{"error":"X-Device-Key required (or X-API-Key with X-Device-Serial)"}` |
| Key unknown | `401` | `{"error":"Invalid device key"}` |
| Device inactive or `DISABLED` | `403` | `{"error":"Device is inactive"}` |

### Site key + serial (deprecated)

```
X-API-Key: main-site-api-key-123
X-Device-Serial: AT-0001
```

Accepted on device endpoints so firmware built against the Sprint 4 protocol
keeps working during a rollout. **Weaker by construction** — the site key is
shared by every terminal at the site, so it cannot distinguish one device from
another beyond the serial the caller claims. Migrate to `X-Device-Key`; this
path will be removed.

### Protocol version header

Optional on all device endpoints:

```
X-Protocol-Version: 1
```

Omitted means version 1. Declaring a version **higher** than the server supports
is refused rather than mis-served:

```json
{ "error": "Unsupported protocol version",
  "server_protocol_version": 1, "device_protocol_version": 2 }
```
→ `400`

---

## 2. Conventions

### Identifiers

Every record carries two:

- `id` — internal `BIGSERIAL`. Stable, but an implementation detail.
- `public_id` — UUID. **Use this** in dashboard URLs and any external reference.

Members are additionally addressed by `member_id`, the badge/member number the
terminal reads. It is unique **per company**, so two companies may both use
`MEM001`. Path parameters for member endpoints use `member_id`, not `id`.

### Timestamps

RFC 3339 / ISO 8601, UTC: `2026-08-07T19:20:27.655424Z`.

Every timestamp the API returns is a **true instant in UTC**, and the `Z` is
accurate. Storage is `TIMESTAMPTZ` and the API pins its database sessions to
UTC, so the wire format does not change with the database server's location.

> **Before migration 010 this was not true**, and any client that cached
> timestamps from an earlier build holds values that are wrong by the database
> server's UTC offset. The columns were `TIMESTAMP WITHOUT TIME ZONE`: they
> stored a wall-clock reading, the driver labelled it `Z` on the way out, and
> three different writers (the database's clock, the API process's clock, and a
> device's own UTC) disagreed about what went in. Re-fetch rather than reconcile.

**Timestamps you send** are accepted in either form. A value carrying an offset
or a `Z` names an instant and is honoured exactly; a value carrying neither is
read as UTC. This applies to `?since=` on the member-changes feed and to
`occurred_at` on a device access log.

### Empty collections

List endpoints return `[]`, never `null`. Clients may iterate unconditionally.

### Error shape

All errors are `{"error": "<message>"}`. Validation failures surface the
underlying validator message, e.g.:

```json
{"error":"Key: 'DeviceRegistrationRequest.SerialNumber' Error:Field validation for 'SerialNumber' failed on the 'required' tag"}
```

These strings are for humans and logs — **do not parse them**. Branch on the
HTTP status code.

### Status codes

| Code | Meaning |
|---|---|
| `200` | Success |
| `201` | Created |
| `400` | Malformed body, missing required field, or unsupported protocol version |
| `401` | Missing or invalid credential |
| `403` | Valid credential, but the site or device is inactive |
| `404` | Resource not found, or not owned by the caller |
| `409` | Conflict — serial registered to another site, duplicate `member_id`, duplicate firmware version |
| `500` | Server or database error |
| `503` | Dependency unavailable (readiness / metrics only) |

A resource owned by another tenant is reported as `404`, not `403`, so the API
does not confirm that an id exists in someone else's account.

### Request correlation

Every response carries `X-Request-ID`. Send your own to have it echoed back:

```
X-Request-ID: 9f2c1ab4de77c051
```

The same id appears on the server's request line and on any error logged while
serving it, so a device that reports a failing request id gives an operator an
exact place to look. Anything longer than 64 characters is replaced.

### Response envelopes

Not uniform, for backward-compatibility reasons. Members, access logs, and
enrollment requests return **bare arrays**; devices and firmware return
`{"count": n, "<name>": [...]}`. Noted per endpoint below.

---

## 3. Members

Backed by the `people` table. **Auth: site API key.**

### `GET /api/v1/members`

All members for the authenticated site's company, newest first.

```bash
curl http://localhost:8080/api/v1/members \
  -H "X-API-Key: main-site-api-key-123"
```

```json
[
  {
    "id": 1,
    "public_id": "de49b725-2b19-4e2a-bdc1-10be7402fdca",
    "member_id": "MEM001",
    "full_name": "Ada Lovelace",
    "membership_type": "ANNUAL",
    "active": true,
    "created_at": "2026-08-07T19:20:27.655424Z",
    "updated_at": "2026-08-07T19:20:27.655424Z"
  }
]
```
→ `200`. Empty: `[]`.

`fingerprint_template` is omitted when empty and included as a string when set.

> **Not paginated.** Returns every member in the company. For a large tenant the
> dashboard should prefer `GET /members/changes`.

### `GET /api/v1/members/{member_id}`

```bash
curl http://localhost:8080/api/v1/members/MEM001 -H "X-API-Key: ..."
```

Returns a single member object as above → `200`.

| Error | Status | Body |
|---|---|---|
| Not found | `404` | `{"error":"Member not found"}` |

| Error | Status | Body |
|---|---|---|
| No such member in this company | `404` | `{"error":"Member not found"}` |
| Database failure | `500` | `{"error":"Failed to retrieve member"}` |

### `POST /api/v1/members`

| Field | Type | Required |
|---|---|---|
| `member_id` | string | yes |
| `full_name` | string | yes |
| `membership_type` | string | yes |
| `active` | bool | no (default `false`) |
| `fingerprint_template` | string | no |

```bash
curl -X POST http://localhost:8080/api/v1/members \
  -H "X-API-Key: main-site-api-key-123" -H 'Content-Type: application/json' \
  -d '{"member_id":"MEM001","full_name":"Ada Lovelace","membership_type":"ANNUAL","active":true}'
```

```json
{
  "id": 1,
  "public_id": "de49b725-2b19-4e2a-bdc1-10be7402fdca",
  "member_id": "MEM001",
  "full_name": "Ada Lovelace",
  "membership_type": "ANNUAL",
  "active": true,
  "created_at": "2026-08-07T19:20:27.655424Z",
  "updated_at": "2026-08-07T19:20:27.655424Z"
}
```
→ `201`

**Side effect:** queues a `CREATE` sync job for every device in the company, in
the same transaction as the insert.

| Error | Status | Body |
|---|---|---|
| Missing required field | `400` | validator message |
| `member_id` already exists in this company | `409` | `{"error":"Member ID already exists"}` |

### `PUT /api/v1/members/{member_id}`

Full replacement. The member id comes from the URL and must **not** be sent in
the body.

| Field | Type | Required |
|---|---|---|
| `full_name` | string | yes |
| `membership_type` | string | yes |
| `active` | bool | no (defaults to `false` — send it explicitly) |
| `fingerprint_template` | string | no |

```bash
curl -X PUT http://localhost:8080/api/v1/members/MEM001 \
  -H "X-API-Key: ..." -H 'Content-Type: application/json' \
  -d '{"full_name":"Ada B","membership_type":"MONTHLY","active":true}'
```

Returns the updated member → `200`.

> Omitting `active` sets it to `false`, deactivating the member. Always send it.

**Side effect:** queues an `UPDATE` sync job for every device.

| Error | Status | Body |
|---|---|---|
| Missing `full_name`/`membership_type` | `400` | validator message |
| Member not found | `404` | `{"error":"Member not found"}` |

### `DELETE /api/v1/members/{member_id}`

**Soft delete.** The row is retained for audit and its `member_id` becomes
available for reuse.

```json
{"message":"Member deleted successfully"}
```
→ `200`

**Side effect:** queues a `DELETE` sync job for every device. This is the *only*
way a terminal learns of a removal.

> Deleting an already-deleted or non-existent member also returns `200` and
> queues nothing — repeated deletes are idempotent.

### `GET /api/v1/members/changes?since={timestamp}`

Members whose `updated_at` is later than `since`, oldest first.

```bash
curl "http://localhost:8080/api/v1/members/changes?since=2000-01-01T00:00:00Z" \
  -H "X-API-Key: ..."
```

Returns an array of member objects → `200`. Empty: `[]`.

`since` is compared as an instant. An offset or `Z` is honoured; no offset means
UTC. Before migration 010 the offset was **discarded**, so a terminal sending a
correctly-formed `…Z` was asking about a different moment than the one it named —
by the database server's offset, in the direction that silently skipped changes.

| Error | Status | Body |
|---|---|---|
| `since` absent | `400` | `{"error":"since parameter required"}` |

> **This feed cannot express deletions.** A deleted member simply stops
> appearing. Terminals must use `DELETE` sync jobs — see
> [docs/sync-protocol.md](docs/sync-protocol.md).

---

## 4. Access

**Auth: site API key.**

### `GET /api/v1/access/{member_id}`

```json
{"granted":true,"message":"Access Granted","status":"ACTIVE"}
```

Always `200`, including for a denial — the *decision* is in the body.

| `status` | `granted` | Meaning |
|---|---|---|
| `ACTIVE` | `true` | Member exists and is active |
| `INACTIVE` | `false` | Member exists, marked inactive |
| `NOT_FOUND` | `false` | No such member in this company |

> **Current behaviour checks membership status only.** It does not consult the
> `permissions` table — doors, schedules, and validity windows are not yet
> evaluated. The permission engine is a future sprint; treat this as
> "is this person a valid member", not "may they open this door".

### `POST /api/v1/access/log`

Records an access attempt.

| Field | Type | Required |
|---|---|---|
| `granted` | bool | no — **`false` is valid and meaningful** |
| `source` | string | yes (e.g. `fingerprint`, `pin`, `card`) |
| `member_id` | string | no — omit for an unrecognised credential |
| `message` | string | no |
| `site_name` | string | no — ignored; derived from the API key |

```bash
curl -X POST http://localhost:8080/api/v1/access/log \
  -H "X-API-Key: ..." -H 'Content-Type: application/json' \
  -d '{"member_id":"MEM001","granted":true,"source":"fingerprint","message":"ok"}'
```

```json
{
  "id": 1,
  "public_id": "dce8e5a5-fe0f-4115-a54f-9b6267c99dc3",
  "member_id": "MEM001",
  "granted": true,
  "source": "fingerprint",
  "site_name": "Main Site",
  "message": "ok",
  "created_at": "2026-08-07T19:20:28.783609Z"
}
```
→ `201`

**Denied attempt by an unknown credential** — omit `member_id`:

```bash
curl -X POST http://localhost:8080/api/v1/access/log \
  -H "X-API-Key: ..." -H 'Content-Type: application/json' \
  -d '{"granted":false,"source":"fingerprint","message":"unknown finger"}'
```

```json
{
  "id": 2,
  "public_id": "0ae9e778-e2d2-47a6-841b-95de13920787",
  "granted": false,
  "source": "fingerprint",
  "site_name": "Main Site",
  "message": "unknown finger",
  "created_at": "2026-08-07T19:20:28.883914Z"
}
```
→ `201`. `member_id` is absent from the response and stored as `NULL`.

| Error | Status |
|---|---|
| `source` missing | `400` |

### `GET /api/v1/access/logs?limit={n}&offset={n}`

Company-wide log, newest first. `limit` defaults to `100`, **capped at `1000`**
(an over-large value is clamped, not rejected). `offset` is accepted but is
currently always `0`.

Returns an array of log objects → `200`. Empty: `[]`.

### `GET /api/v1/access/logs/{member_id}?limit={n}`

Same shape, filtered to one member. Same limit rules.

---

## 5. Enrollment

**Auth: site API key.** `pending` and `result` are *also* mounted under
`/api/v1/devices/` behind the device key — same handlers, same shapes, listed in
the [endpoint index](#11-endpoint-index). A terminal is where enrolment
physically happens, and it must be able to report one without carrying the
site's provisioning secret.

### `POST /api/v1/enrollment/start`

```json
{"member_id": "MEM001"}
```

```json
{
  "message": "Enrollment request created",
  "member": { "...full member object..." },
  "request": {
    "id": 1,
    "public_id": "4d293fbf-cf79-452c-91d2-4ebc29134b57",
    "member_id": "MEM001",
    "status": "PENDING",
    "created_at": "2026-08-07T19:20:43.999771Z"
  }
}
```
→ `201`

| Error | Status | Body |
|---|---|---|
| Member does not exist | `404` | `{"error":"Member not found"}` |

### `GET /api/v1/enrollment/pending`

Array of pending requests, oldest first → `200`. Empty: `[]`.

`status` is one of `PENDING`, `IN_PROGRESS`, `COMPLETED`, `FAILED`.
`completed_at` appears only once set.

### `POST /api/v1/enrollment/result`

| Field | Type | Required |
|---|---|---|
| `member_id` | string | yes |
| `fingerprint_template` | string | yes |

```json
{"message":"Enrollment completed successfully","member_id":"MEM001"}
```
→ `200`

Stores the template, sets the member active, closes the pending request, and
queues an `UPDATE` sync job to every device — all in one transaction.

| Error | Status | Body |
|---|---|---|
| Missing field | `400` | validator message |
| No such member in this company | `404` | `{"error":"Member not found"}` |

---

## 6. Site settings

**Auth: site API key.** Settings apply to the site behind the key; every device
at that site inherits them.

### `GET /api/v1/sites/settings`

```json
{
  "settings": {
    "tamper_alarm": true,
    "offline_grace_minutes": 60,
    "sync_interval_seconds": 60,
    "unlock_duration_seconds": 5
  },
  "settings_version": 1
}
```
→ `200`

### `PUT /api/v1/sites/settings`

Body is the settings object itself — **a full replacement**, not a merge. Keys
omitted are removed.

```bash
curl -X PUT http://localhost:8080/api/v1/sites/settings \
  -H "X-API-Key: ..." -H 'Content-Type: application/json' \
  -d '{"unlock_duration_seconds":8,"sync_interval_seconds":30}'
```

```json
{"settings":{"sync_interval_seconds":30,"unlock_duration_seconds":8},"settings_version":2}
```
→ `200`

`settings_version` increments on every change. **Side effect:** queues a
`SETTINGS` job to every device at the site.

| Error | Status | Body |
|---|---|---|
| Body is not a JSON object | `400` | `{"error":"Settings must be a JSON object"}` |

---

## 7. Devices — operator

**Auth: site API key.**

### `POST /api/v1/devices/register`

Provisions a device and issues its credential.

| Field | Type | Required |
|---|---|---|
| `serial_number` | string | yes |
| `device_name` | string | no (defaults to the serial) |
| `device_type` | string | no — `TERMINAL` (default), `READER`, `CONTROLLER` |
| `firmware_version` | string | no |
| `hardware_revision` | string | no |
| `build_number` | string | no |
| `release_channel` | string | no — `STABLE` (default), `BETA`, `CANARY` |
| `ip_address` | string | no (defaults to the caller's IP) |

```bash
curl -X POST http://localhost:8080/api/v1/devices/register \
  -H "X-API-Key: main-site-api-key-123" -H 'Content-Type: application/json' \
  -d '{"serial_number":"AT-0001","device_name":"Front Door","firmware_version":"1.0.0","hardware_revision":"rev-C","build_number":"2481"}'
```

```json
{
  "protocol_version": 1,
  "device_id": "185e129c-071b-4429-a2ee-8f364adc9b38",
  "serial_number": "AT-0001",
  "api_key": "atd_d4fadeddd3b3be3e82fc4019fc594d32ed92145d5ab35731ca4f61c6692a6168",
  "bootstrap_jobs": 1,
  "warning": "Store this api_key now. It cannot be retrieved again."
}
```
→ `201`

**`api_key` is shown exactly once.** It is stored only as a hash.

Registering an existing serial **rotates** the credential rather than failing —
this is how a factory-reset terminal recovers. The previous key stops working
immediately. Registration also seeds the device with the current member list as
`CREATE` jobs (`bootstrap_jobs` reports how many), **in the same transaction**
that issues the credential — so there is no state in which a key is committed
but never returned to the caller that has to store it.

A device that is `DISABLED`, or whose `active` is false, is **refused**.
Disabling is currently the only way to revoke a terminal, so registration is not
allowed to undo it: nothing is rotated, nothing is re-enabled, and no credential
is issued. Re-enable the device first.

| Error | Status | Body |
|---|---|---|
| `serial_number` missing | `400` | validator message |
| `device_type` not `TERMINAL`/`READER`/`CONTROLLER` | `400` | validator message |
| `release_channel` not `STABLE`/`BETA`/`CANARY` | `400` | validator message |
| Device is `DISABLED` or inactive | `403` | `{"error":"Device is disabled; re-enable it before registering"}` |
| Serial belongs to another site | `409` | `{"error":"Serial number is registered to another site"}` |

### `GET /api/v1/devices?outdated=true`

Fleet inventory for the company. `outdated=true` narrows to devices not running
the current build for their release channel.

```json
{
  "count": 1,
  "devices": [
    {
      "id": 1,
      "public_id": "185e129c-071b-4429-a2ee-8f364adc9b38",
      "site_id": 1,
      "site_public_id": "6adb7321-5581-4287-96b4-dbe1dc922685",
      "site_name": "Main Site",
      "serial_number": "AT-0001",
      "device_name": "Front Door",
      "device_type": "TERMINAL",
      "status": "ONLINE",
      "active": true,
      "release_channel": "STABLE",
      "firmware_version": "1.0.0",
      "hardware_revision": "rev-C",
      "build_number": "2481",
      "current_firmware_version": "1.1.0",
      "firmware_outdated": true
    }
  ]
}
```
→ `200`

`boot_count`, `last_seen_at`, `last_sync_at`, `last_heartbeat_at` appear once
reported/known. `firmware_outdated` is `false` when no current build is marked
for that type and channel.

**Device states:** `PROVISIONING`, `ONLINE`, `OFFLINE`, `UPDATING`, `ERROR`,
`DISABLED`. See [docs/sync-protocol.md](docs/sync-protocol.md#device-states).

### `GET /api/v1/devices/summary`

```json
{"total":1,"online":1,"offline":0,"updating":0,"error":0,"disabled":0,"provisioning":0,"firmware_outdated":0}
```
→ `200`

### `POST /api/v1/devices/{serial}/resync`

Forces the device's queue to be replaced with a snapshot of current state. Use
when a terminal is believed to have drifted.

```json
{"serial_number":"AT-0001","superseded_jobs":1,"pending_jobs":3}
```
→ `200`

| Error | Status | Body |
|---|---|---|
| Serial not at this site | `404` | `{"error":"Device not registered for this site"}` |

---

## 8. Firmware

**Auth: site API key.** Catalog and inventory only — **nothing here downloads,
schedules, or applies firmware.** OTA is not implemented.

The catalog is **scoped to the caller's company**. A tenant sees, publishes and
promotes only its own builds; another tenant's firmware id reads as `404`.

### `GET /api/v1/firmware`

```json
{
  "count": 1,
  "firmware_versions": [
    {
      "id": 1,
      "public_id": "9e0a3ecc-ad8b-4888-95c0-e4277a4ab9ed",
      "version": "1.0.0",
      "device_type": "TERMINAL",
      "release_channel": "STABLE",
      "is_mandatory": false,
      "is_current": true,
      "published_at": "2026-08-07T19:20:04.108678Z",
      "created_at": "2026-08-07T19:20:04.108678Z"
    }
  ]
}
```
→ `200`

### `POST /api/v1/firmware`

| Field | Type | Required |
|---|---|---|
| `version` | string | yes |
| `device_type` | string | no (default `TERMINAL`) |
| `release_channel` | string | no (default `STABLE`) |
| `download_url` | string | no |
| `checksum_sha256` | string | no — 64 hex characters |
| `size_bytes` | integer | no |
| `release_notes` | string | no |
| `is_mandatory` | bool | no |

Returns the created record with `is_current: false` → `201`. A new build does
**not** become the target until explicitly promoted.

| Error | Status | Body |
|---|---|---|
| Version already published for that device type | `409` | `{"error":"That version already exists for this device type"}` |

### `PUT /api/v1/firmware/{id}/current`

Makes a build the deployment target for its
`(company, device_type, release_channel)`, demoting whatever held that slot.
Exactly one current build per triple.

Returns the record with `is_current: true` → `200`.

> This only changes what "outdated" means. **No device is contacted.**

| Error | Status | Body |
|---|---|---|
| Unknown id, or owned by another company | `404` | `{"error":"Firmware version not found"}` |

---

## 9. Devices — device-authenticated

**Auth: device key** (or the deprecated site key + serial). These are the
endpoints the ESP32 calls.

### `POST /api/v1/devices/heartbeat`

All fields optional; body may be omitted entirely.

| Field | Type | Notes |
|---|---|---|
| `firmware_version` | string | |
| `hardware_revision` | string | |
| `build_number` | string | |
| `boot_count` | integer | |
| `status` | string | `ONLINE`, `UPDATING`, or `ERROR` only |
| `error` | string | recorded when `status` is `ERROR` |
| `ip_address` | string | defaults to the caller's IP |

```bash
curl -X POST http://localhost:8080/api/v1/devices/heartbeat \
  -H "X-Device-Key: atd_d4fa..." -H 'Content-Type: application/json' \
  -d '{"firmware_version":"1.0.0","hardware_revision":"rev-C","build_number":"2481","boot_count":42,"status":"ONLINE"}'
```

```json
{"protocol_version":1,"device_id":"AT-0001","server_time":"2026-08-07T18:21:02.3261061Z","pending_jobs":4}
```
→ `200`

`pending_jobs` lets a device skip polling when nothing is waiting. `server_time`
is authoritative for clock alignment.

> A device may only claim `ONLINE`, `UPDATING`, or `ERROR`. Anything else is
> treated as `ONLINE`. `OFFLINE` is inferred by the server; `DISABLED` is
> administrative and a heartbeat will not clear it.

### `GET /api/v1/devices/settings`

```json
{
  "protocol_version": 1,
  "device_id": "AT-0001",
  "settings_version": 2,
  "settings": {"sync_interval_seconds": 30, "unlock_duration_seconds": 8}
}
```
→ `200`

### `GET /api/v1/devices/jobs?limit={n}`

Due work, oldest first. `limit` defaults to `50`, capped at `200`.

```json
{
  "protocol_version": 1,
  "device_id": "AT-0001",
  "server_time": "2026-08-07T18:21:02.5603296Z",
  "count": 3,
  "snapshot_taken": false,
  "jobs": [
    {
      "id": 2,
      "public_id": "13797620-7a6b-495f-a549-fd419e8e57a8",
      "protocol_version": 1,
      "job_type": "FULL_SYNC",
      "entity_type": "ROSTER",
      "payload": {"count": 1, "snapshot": true, "member_ids": ["MEM001"]},
      "attempts": 0,
      "created_at": "2026-08-07T19:20:45.594194Z"
    },
    {
      "id": 3,
      "public_id": "ac4a3c3f-f1c3-4a09-bd5b-702782eff4b8",
      "protocol_version": 1,
      "job_type": "CREATE",
      "entity_type": "PERSON",
      "entity_external_id": "MEM001",
      "payload": {
        "member_id": "MEM001",
        "full_name": "Ada B",
        "membership_type": "MONTHLY",
        "active": true,
        "deleted": false,
        "fingerprint_template": "BASE64TEMPLATE",
        "updated_at": "2026-08-07T19:20:44Z"
      },
      "attempts": 0,
      "created_at": "2026-08-07T19:20:45.594194Z"
    },
    {
      "id": 4,
      "public_id": "4fa4703a-5ded-4293-9c2e-ee78f18b2fed",
      "protocol_version": 1,
      "job_type": "SETTINGS",
      "entity_type": "SETTINGS",
      "payload": {"settings_version": 2, "settings": {"sync_interval_seconds": 30, "unlock_duration_seconds": 8}},
      "attempts": 0,
      "created_at": "2026-08-07T19:20:45.594194Z"
    }
  ]
}
```
→ `200`. No work: `count: 0`, `jobs: []`.

| `job_type` | Device action |
|---|---|
| `CREATE` | Upsert the person from `payload` |
| `UPDATE` | Upsert — identical handling to `CREATE` |
| `DELETE` | Remove `payload.member_id`; succeed if already absent |
| `SETTINGS` | Apply `payload.settings`; **ignore if `settings_version` is older than held** |
| `FULL_SYNC` | Reconcile: delete any local member **not** in `payload.member_ids` |

`snapshot_taken: true` means the backlog was collapsed and the queue replaced.

**Fetching does not complete a job.** Each fetch takes a 60-second lease and the
job stays pending; only an acknowledgement retires it. Delivery is
**at-least-once** — apply jobs in the order received and expect duplicates.

### `POST /api/v1/devices/jobs/{id}/complete`

```json
{"status": "COMPLETED"}
```

or

```json
{"status": "FAILED", "error": "sensor busy"}
```

An empty body — or `{}` — means `COMPLETED`.

```json
{"protocol_version":1,"job_id":2,"status":"COMPLETED","pending_jobs":3}
```
→ `200`

**Acknowledging an already-completed job returns `200`**, so an ack whose
response was lost can be safely retried.

`FAILED` increments `attempts`, keeps the job pending, and schedules a retry
with exponential backoff (1s → 15min cap). After `max_attempts` (default 10) the
job is parked in `FAILED`.

| Error | Status | Body |
|---|---|---|
| Non-numeric id | `400` | `{"error":"Invalid job id"}` |
| `status` not `COMPLETED`/`FAILED` | `400` | `{"error":"status must be COMPLETED or FAILED"}` |
| Job belongs to another device, or does not exist | `404` | `{"error":"Job not found for this device"}` |

**Acknowledging a job that is already retired is a no-op that returns `200`.**
A device may safely retransmit an acknowledgement whose response it never
received. Specifically:

| Job's current state | `COMPLETED` ack | `FAILED` ack |
|---|---|---|
| `PENDING` / `FAILED` | retired as completed | attempt recorded, retry scheduled |
| `COMPLETED` | no change, `200` | **no change**, `200` |
| `CANCELLED` (superseded by a snapshot) | no change, `200` | **no change**, `200` |

The two bold cases matter for a terminal that fetched a batch and was then
resynced, or whose acknowledgement was lost: a late failure report must not
reopen work that a snapshot already replaced, or undo an acknowledgement the
server has already accepted.

### `POST /api/v1/devices/access/log`

Records a door event under the **device's own** credential.

Not the same endpoint as [`POST /api/v1/access/log`](#post-apiv1accesslog), and
not the same request shape. That one takes the site API key — which is the
provisioning secret, able to register devices and rotate their credentials — so
requiring it on every terminal to write an audit line would make the audit trail
the weakest thing in the system. This one takes `X-Device-Key`.

**There is no site, company or device field.** All three are read from the
authenticated credential, so a terminal cannot write a log against another site
however the body is constructed. Anything of the sort is ignored, because there
is nowhere for it to land.

| Field | Type | Required | Notes |
|---|---|---|---|
| `event_id` | string | **yes** | **Must be a UUID.** Generated by the device, once per door event, and reused for every retry of that event |
| `source` | string | **yes** | e.g. `FINGERPRINT`, `pin`, `card` |
| `granted` | bool | no | Defaults `false`. **A denial is the more interesting half of an audit trail** |
| `member_id` | string | no | Omit for an unrecognised credential; stored as NULL and matches no person |
| `message` | string | no | |
| `occurred_at` | string | no | RFC3339. Parsed leniently — an unparseable or absent value falls back to server arrival time, so a terminal whose clock has never synced cannot be prevented from reporting |

```bash
curl -X POST https://api.example.com/api/v1/devices/access/log \
  -H "X-Device-Key: atd_d4fa..." -H 'Content-Type: application/json' \
  -d '{"event_id":"183aa074-42ea-4dcd-b0ff-9936e3e8eb8b","member_id":"MEM001","granted":true,"source":"FINGERPRINT","occurred_at":"2026-08-11T10:04:12Z"}'
```

```json
{"event_id":"183aa074-42ea-4dcd-b0ff-9936e3e8eb8b","recorded":true,"duplicate":false}
```
→ **`200`** — note, *not* `201`. The response reports what happened to the event,
and a replay is not a creation.

**`event_id` is the idempotency key, and replay is not an error.** Re-sending an
event the server already holds returns `200` with:

```json
{"event_id":"183aa074-42ea-4dcd-b0ff-9936e3e8eb8b","recorded":false,"duplicate":true}
```

No second audit line is written. This matters because the terminal retries
*precisely when it did not hear the first answer* — including when the server
committed the row and the response was lost on the way back. Answering "failed"
to a replay would make a terminal retry for ever, or drop a real event.

| Field | Meaning |
|---|---|
| `recorded` | `true` when this call created the audit line |
| `duplicate` | `true` when this `event_id` was already held; always the inverse of `recorded` |
| `event_id` | echoed back, so a device can match the answer to the event |

| Condition | Status | Body |
|---|---|---|
| Success, first time | `200` | `{"recorded":true,"duplicate":false,...}` |
| Success, replay | `200` | `{"recorded":false,"duplicate":true,...}` |
| `event_id` missing | `400` | validator message, `failed on the 'required' tag` |
| `event_id` not a UUID | `400` | validator message, `failed on the 'uuid' tag` |
| `source` missing | `400` | validator message |
| No/unknown device key | `401` | see [Device key](#device-key) |
| Device inactive or `DISABLED` | `403` | `{"error":"Device is inactive"}` |
| Storage failure | `500` | `{"error":"Failed to log access"}` |

> **A client must not treat every `4xx` as permanent.** `400` is the server
> refusing this body for ever and the event should be retired; `401`, `403`,
> `408` and `429` are conditions that clear — a rotated credential, a device
> re-enabled by an operator, a transient limit — and an event discarded on one
> of those is gone for good, because nothing on the server can re-drive it.
> Unlike a sync job, an unsent door event exists only on the terminal.

### Recommended device loop

1. `POST /devices/heartbeat` → read `pending_jobs`.
2. If non-zero, `GET /devices/jobs`.
3. Apply each job **in order**, acknowledging each individually.
4. Repeat while `pending_jobs > 0`; otherwise back off.
5. On restart, just heartbeat — anything unacknowledged is still queued.

Never treat a job as done merely because it was received. A device that applies
a job then loses power before acknowledging will receive it again; that is
correct and safe, because applies are idempotent.

---

## 10. Health and monitoring

**No authentication**, except `/metrics` when `METRICS_TOKEN` is set.

### `GET /health`

```json
{"status":"healthy","service":"Access Terminal Cloud API"}
```
→ `200`

### `GET /health/live`

```json
{"status":"alive","service":"Access Terminal Cloud API","uptime_seconds":50}
```
→ `200`. Touches no dependency.

### `GET /health/ready`

```json
{"status":"ready","uptime_seconds":50,"checks":{"database":{"status":"up"}}}
```
→ `200`, or `503` with `"status":"not_ready"` and the database check `down`.

Route traffic on this, not on `/health/live`.

### `GET /health/maintenance`

```json
{
  "enabled": true,
  "count": 2,
  "tasks": [
    {"name":"offline_sweep","interval":"1m0s","runs":16,"failures":0,
     "last_run_at":"2026-08-07T18:09:15.873Z","last_duration":"1.52ms"}
  ]
}
```
→ `200`. With maintenance disabled: `{"enabled":false,"tasks":[]}`.

### `GET /metrics`

Prometheus text exposition format. Optional auth:

```
Authorization: Bearer <METRICS_TOKEN>
X-Metrics-Token: <METRICS_TOKEN>
```

```
access_terminal_devices{status="ONLINE"} 1
access_terminal_sync_jobs{status="PENDING"} 6
access_terminal_sync_jobs_oldest_pending_age_seconds 0.939
```

→ `200`, `401` on a bad token, or `503` with `access_terminal_up 0` if the
scrape fails. Full metric list in [docs/operations.md](docs/operations.md).

---

## 11. Endpoint index

| Method | Path | Auth |
|---|---|---|
| `GET` | `/health` | none |
| `GET` | `/health/live` | none |
| `GET` | `/health/ready` | none |
| `GET` | `/health/maintenance` | none |
| `GET` | `/metrics` | optional token |
| `GET` | `/api/v1/members` | site key |
| `POST` | `/api/v1/members` | site key |
| `GET` | `/api/v1/members/{member_id}` | site key |
| `PUT` | `/api/v1/members/{member_id}` | site key |
| `DELETE` | `/api/v1/members/{member_id}` | site key |
| `GET` | `/api/v1/members/changes` | site key |
| `GET` | `/api/v1/access/{member_id}` | site key |
| `POST` | `/api/v1/access/log` | site key |
| `GET` | `/api/v1/access/logs` | site key |
| `GET` | `/api/v1/access/logs/{member_id}` | site key |
| `POST` | `/api/v1/enrollment/start` | site key |
| `GET` | `/api/v1/enrollment/pending` | site key |
| `POST` | `/api/v1/enrollment/result` | site key |
| `GET` | `/api/v1/sites/settings` | site key |
| `PUT` | `/api/v1/sites/settings` | site key |
| `POST` | `/api/v1/devices/register` | site key |
| `GET` | `/api/v1/devices` | site key |
| `GET` | `/api/v1/devices/summary` | site key |
| `POST` | `/api/v1/devices/{serial}/resync` | site key |
| `GET` | `/api/v1/firmware` | site key |
| `POST` | `/api/v1/firmware` | site key |
| `PUT` | `/api/v1/firmware/{id}/current` | site key |
| `POST` | `/api/v1/devices/heartbeat` | device key |
| `GET` | `/api/v1/devices/settings` | device key |
| `GET` | `/api/v1/devices/jobs` | device key |
| `POST` | `/api/v1/devices/jobs/{id}/complete` | device key |
| `POST` | `/api/v1/devices/access/log` | device key |
| `GET` | `/api/v1/devices/enrollment/pending` | device key |
| `POST` | `/api/v1/devices/enrollment/result` | device key |

34 endpoints.

The last three are the same handlers as their site-key counterparts, mounted a
second time behind device authentication — see `router.go`. They are what let a
terminal close the enrolment loop and write its own audit line without carrying
the site's provisioning secret. `POST /api/v1/devices/access/log` is documented
in [section 9](#post-apiv1devicesaccesslog); the two enrolment routes behave
exactly as documented in [section 5](#5-enrollment), differing only in which
credential opens them and in reading their tenant from the device rather than
from the site key.

### Operator session routes

The dashboard's surface. None of these accept a site API key, and none returns
one. CSRF is required on every unsafe method.

| Method | Path | Auth | Min role |
|---|---|---|---|
| `POST` | `/api/v1/auth/login` | none | — |
| `GET` | `/api/v1/auth/me` | session | any |
| `POST` | `/api/v1/auth/logout` | session + CSRF | any |
| `POST` | `/api/v1/auth/password` | session + CSRF | any |
| `GET` | `/api/v1/console/company` | session | VIEWER |
| `GET` | `/api/v1/console/sites` | session | VIEWER |
| `GET` | `/api/v1/console/sites/{site_id}` | session | VIEWER |
| `GET` | `/api/v1/console/sites/{site_id}/settings` | session | VIEWER |
| `PUT` | `/api/v1/console/sites/{site_id}/settings` | session + CSRF | MANAGER |
| `GET` | `/api/v1/console/applications` | session | VIEWER |
| `PUT` | `/api/v1/console/applications/{code}` | session + CSRF | OWNER |
| `GET` | `/api/v1/console/terminals` | session | VIEWER |
| `GET` | `/api/v1/console/terminals/summary` | session | VIEWER |
| `GET` | `/api/v1/console/terminals/{serial}` | session | VIEWER |
| `PUT` | `/api/v1/console/terminals/{serial}/application-mode` | session + CSRF | MANAGER |
| `GET` | `/api/v1/console/people` | session | VIEWER |
| `GET` | `/api/v1/console/people/{external_id}` | session | VIEWER |
| `POST` | `/api/v1/console/people` | session + CSRF | MANAGER |
| `PUT` | `/api/v1/console/people/{external_id}` | session + CSRF | MANAGER |
| `DELETE` | `/api/v1/console/people/{external_id}` | session + CSRF | MANAGER |
| `GET` | `/api/v1/console/operators` | session | ADMIN |
| `POST` | `/api/v1/console/operators` | session + CSRF | ADMIN |
| `GET` | `/api/v1/console/operators/{operator_id}` | session | ADMIN |
| `PUT` | `/api/v1/console/operators/{operator_id}` | session + CSRF | ADMIN |
| `DELETE` | `/api/v1/console/operators/{operator_id}` | session + CSRF | ADMIN |
| `GET` | `/api/v1/console/operators/{operator_id}/sites` | session | ADMIN |
| `PUT` | `/api/v1/console/operators/{operator_id}/sites` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/operators/{operator_id}/invite` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/operators/{operator_id}/reset` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/sites` | session + CSRF | ADMIN |
| `PUT` | `/api/v1/console/sites/{site_id}` | session + CSRF | ADMIN |
| `DELETE` | `/api/v1/console/sites/{site_id}` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/sites/{site_id}/api-key` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/terminals/{serial}/resync` | session + CSRF | MANAGER |
| `PUT` | `/api/v1/console/terminals/{serial}/state` | session + CSRF | ADMIN |
| `POST` | `/api/v1/console/terminals/{serial}/revoke` | session + CSRF | ADMIN |
| `DELETE` | `/api/v1/console/terminals/{serial}` | session + CSRF | ADMIN |
| `PUT` | `/api/v1/console/terminals/{serial}/site` | session + CSRF | ADMIN |
| `GET` | `/api/v1/console/firmware` | session | ADMIN |
| `POST` | `/api/v1/console/firmware` | session + CSRF | ADMIN |
| `PUT` | `/api/v1/console/firmware/{id}/current` | session + CSRF | ADMIN |
| `GET` | `/api/v1/console/audit` | session | ADMIN |
| `GET` | `/api/v1/console/events` | session | VIEWER |
| `GET` | `/api/v1/console/people/{external_id}/permissions` | session | VIEWER |
| `POST` | `/api/v1/console/people/{external_id}/permissions` | session + CSRF | MANAGER |
| `DELETE` | `/api/v1/console/permissions/{permission_id}` | session + CSRF | MANAGER |
| `GET` | `/api/v1/console/schedules` | session | VIEWER |
| `POST` | `/api/v1/console/schedules` | session + CSRF | MANAGER |
| `PUT` | `/api/v1/console/schedules/{schedule_id}` | session + CSRF | MANAGER |
| `DELETE` | `/api/v1/console/schedules/{schedule_id}` | session + CSRF | MANAGER |
| `POST` | `/api/v1/console/terminals/{serial}/evaluate` | session + CSRF | MANAGER |
| `POST` | `/api/v1/console/sites/{site_id}/claim-codes` | session + CSRF | ADMIN |

### Credential handover routes

Unauthenticated by necessity: somebody who has forgotten their password cannot
authenticate to ask for a new one, and somebody redeeming an invitation has
never had one. Both share the login rate limiter.

| Method | Path | Auth |
|---|---|---|
| `POST` | `/api/v1/auth/forgot-password` | none — always 202, whether or not the address exists |
| `POST` | `/api/v1/auth/redeem` | the single-use token is the whole authorisation |

### Platform administration routes

A **separate credential class** with its own table, session and cookie. It
reaches `companies` and operator bootstrap, and deliberately nothing inside a
tenant — there is no platform route that loads a person, a credential, an event,
a terminal or a site key.

| Method | Path | Auth |
|---|---|---|
| `POST` | `/api/v1/platform/login` | none |
| `GET` | `/api/v1/platform/me` | platform session |
| `POST` | `/api/v1/platform/logout` | platform session + CSRF |
| `GET` | `/api/v1/platform/companies` | platform session |
| `GET` | `/api/v1/platform/companies/{company_id}` | platform session |
| `POST` | `/api/v1/platform/companies` | platform session + CSRF |
| `PUT` | `/api/v1/platform/companies/{company_id}` | platform session + CSRF |
| `POST` | `/api/v1/platform/companies/{company_id}/operators` | platform session + CSRF |

Issuing a first operator is refused into a company that already has one, by a
query predicate rather than a check: a platform identity that could add accounts
to a running tenant at any time would be a standing back door into every
customer.

---

## 12. Operator authentication

`/api/v1/auth/*` — how a human signs in to the dashboard. A separate credential
class from everything above: **no route in this section or the next reads
`X-API-Key`, and none returns one.**

### The session cookie

```
Set-Cookie: __Host-al_session=ats_<64 hex>; Path=/; HttpOnly; Secure; SameSite=Lax
```

Fixed, and not configurable. The `__Host-` prefix is enforced by the browser: it
refuses the cookie unless `Secure` and `Path=/` are set and `Domain` is absent,
which makes it host-only and un-settable by any sibling subdomain of the API.
There is no `Max-Age` — the session's real lifetime is the server-side row.

The token is opaque, 256 bits from `crypto/rand`, and stored only as a SHA-256
hash. **It never appears in a response body.**

Two expiries. The idle window (12h by default) slides forward as the session is
used; the absolute cap (7d) never moves. Both are enforced server-side on every
request, so logout, disabling an account, changing a role and changing a password
all take effect on the *next* request.

**Deployment constraint.** `SameSite=Lax` sends the cookie only on *same-site*
requests. `app.accesslink.store` → `api.accesslink.store` works because both
share the registrable domain `accesslink.store`. A dashboard on an unrelated
domain would never receive the cookie. `CORS_ALLOWED_ORIGINS` must additionally
name the dashboard's exact origin, or the browser will not send credentials
cross-origin — both halves are required and neither substitutes for the other.

For local development without TLS, `SESSION_COOKIE_INSECURE=1` drops `Secure`
**and renames the cookie to `al_session`** — a `__Host-` cookie without `Secure`
is rejected outright, so the prefix has to go with it. Exactly one name is
accepted at a time.

### CSRF

Every unsafe method (anything but `GET`, `HEAD`, `OPTIONS`) under
`/api/v1/auth` and `/api/v1/console` requires:

```
X-CSRF-Token: <csrf_token from login or /me>
```

The token is per-session, returned in the **body** of `login` and `/me` — not as
a second cookie, because a dashboard on another origin cannot read a cookie
scoped to the API host. Hold it in memory and re-fetch it from `/me` after a page
reload. Comparison is by hash, in constant time. Missing → `403 CSRF token
required`; wrong → `403 Invalid CSRF token`.

### `POST /api/v1/auth/login`

Unauthenticated. Requires `Content-Type: application/json` — an HTML form cannot
send JSON, so a cross-site form post cannot reach this endpoint.

```json
{"email": "ops@example.com", "password": "..."}
```

`200` sets the cookie and returns the session body ([below](#the-session-body)).

| Code | When |
|---|---|
| `400` | missing `email` or `password` |
| `401` | **any** credential failure — unknown address, wrong password, disabled account, disabled company. One message for all of them, and an unknown address still costs a bcrypt comparison so timing does not answer what the message will not |
| `415` | body was not `application/json` |
| `429` | too many attempts (per address) **or** the account is temporarily locked (5 failures → 1 min, doubling to a 15 min cap). Carries `Retry-After` |
| `500` | database unavailable — never reported as a credential failure |

### `GET /api/v1/auth/me`

Session required. Returns the same body as `login`, so a dashboard can restore
its whole state after a reload with one request. `401` when the session is
missing, unknown, revoked, expired, or belongs to a disabled account or company.

### The session body

Returned identically by `login` and `/me`:

```json
{
  "operator": {
    "id": "1f0c…", "email": "ops@example.com",
    "full_name": "Ops Person", "role": "OWNER"
  },
  "company": { "id": "9b2a…", "name": "Acme", "slug": "acme" },
  "role": "OWNER",
  "sites": [ { "site_id": "5120…", "site_name": "Site A" } ],
  "all_sites": true,
  "applications": [
    { "code": "ATTENDANCE", "settings": { "grace_minutes": 5 } }
  ],
  "csrf_token": "…",
  "session_expires_at": "2026-08-21T09:12:33Z",
  "session_expires_in_seconds": 604800
}
```

- `sites` — the operator's explicit site grants, possibly empty.
- **`all_sites`** — an empty `sites` array means *not scoped to particular
  sites*, which is **every site in the company**, not none. This flag says which.
  It is true for OWNER and ADMIN always, and for anyone holding no grants.
- `applications` — the capabilities this company has **enabled**, in a stable
  order, and what the dashboard should build its navigation from. An empty array
  is a legitimate, common state; see [section 14](#14-applications-and-modules).
- `session_expires_in_seconds` — a duration, resolved server-side. Prefer it over
  the absolute timestamp when scheduling anything.

An object, deliberately: new fields are added without breaking clients.

### `POST /api/v1/auth/logout`

Session + CSRF. Revokes the row **and** clears the cookie, then `204`. Idempotent
in effect; a copy of the cookie is worthless afterwards.

### `POST /api/v1/auth/password`

Session + CSRF, and rate-limited on the same allowance as `login`.

```json
{"current_password": "...", "new_password": "..."}
```

`204` on success. The current password is required even though the caller is
signed in — a session proves possession of the browser, not knowledge of the
secret. **Every other session for that operator is revoked** in the same
transaction; the calling session survives.

| Code | When |
|---|---|
| `400` | new password fails the policy (minimum 12 characters, maximum 72 bytes) |
| `403` | current password is wrong, or CSRF failed |
| `415` | body was not `application/json` |

---

## 13. Operator console

`/api/v1/console/*` — the dashboard's API. Operator session on every route, CSRF
on every unsafe method, a minimum role per route, and a site-grant check wherever
the path names a site.

**Roles are ordered:** `OWNER > ADMIN > MANAGER > VIEWER`. A route names the
lowest role that may reach it and everyone above inherits. An insufficient role is
`403`; no session at all is `401`.

**Site grants.** An operator may hold grants to specific sites. OWNER and ADMIN
are never scoped, and an operator with *no* grants is not scoped either — absence
means every site in the company. On a site-scoped route:

- a site in another company → **`404`** (never `403`; the API does not confirm an
  id exists in someone else's account)
- a site in your company you are not granted → **`403`**

Lists are narrowed by the same rule, so the console never shows a site the detail
route would then refuse.

**Terminals are governed by the grant on the site they stand at.** A route that
names a `{serial}` rather than a `{site_id}` resolves the terminal's own site and
applies exactly the rule above — another company's serial is `404`, an ungranted
site's serial is `403`. A serial is printed on the hardware and is not a secret,
so knowing one has never been a substitute for a grant.

**Tenancy.** Every query is scoped to the caller's company. A resource in another
tenant is `404`.

### Company and sites

| Method | Path | Role | Returns |
|---|---|---|---|
| `GET` | `/console/company` | VIEWER | `{id, name, slug, contact_email, active, created_at}` |
| `GET` | `/console/sites` | VIEWER | `{count, sites: [...]}` |
| `GET` | `/console/sites/{site_id}` | VIEWER | one site |
| `GET` | `/console/sites/{site_id}/settings` | VIEWER | `{settings, settings_version}` |
| `PUT` | `/console/sites/{site_id}/settings` | MANAGER | updated settings |
| `POST` | `/console/sites` | **ADMIN** | new site **+ its key, once** |
| `PUT` | `/console/sites/{site_id}` | **ADMIN** | updated site |
| `DELETE` | `/console/sites/{site_id}` | **ADMIN** | `{retired, terminals_retired}` |
| `POST` | `/console/sites/{site_id}/api-key` | **ADMIN** | **new key, once** |

A site:

```json
{
  "id": "5120…", "name": "Site A", "address": "…", "timezone": "UTC",
  "active": true, "terminal_count": 3, "created_at": "…"
}
```

**There is no `api_key` field.** The column is never selected for these
endpoints. `{site_id}` is a site's `public_id` (a UUID); a malformed one is
`404`, not a `500`.

Writing settings replaces the object wholesale and enqueues a `SETTINGS` sync job
for every terminal at that site — the same handler and the same behaviour as
[section 6](#6-site-settings).

Site lifecycle is **ADMIN**, above the MANAGER gate on day-to-day writes:
creating a site mints a provisioning credential and retiring one stops doors
opening. ADMIN and OWNER are never site-scoped, so an operator scoped to one site
cannot create or modify another; the `{site_id}` routes still resolve inside the
caller's company, so another tenant's site is `404`.

#### `POST /console/sites`

```json
{"name": "Lagos Depot", "address": "14 Marina Road", "timezone": "Africa/Lagos"}
```

`name` is required and unique per company (`409` on a clash; a *retired* site's
name is free for reuse). `timezone` defaults to `UTC` — it describes where the
hardware stands, which is a different question from the zone an operator reads
timestamps in.

```json
{
  "site": { "id": "…", "name": "Lagos Depot", "…": "…" },
  "credential": {
    "api_key": "ats_9f1c…",
    "api_key_prefix": "ats_9f1c2a",
    "shown_once": true
  }
}
```
→ `201`

> ### The key is shown once and cannot be recovered
>
> `api_key` is the **provisioning secret**: whoever holds it can register a
> terminal at that site and rotate any device credential there. The server stores
> only its SHA-256 hash, so this response is the only time it exists outside
> whatever the caller does with it. **No `GET` ever returns it**, and there is no
> endpoint that can. An operator who loses it must rotate.
>
> `api_key_prefix` is the first 12 characters. It is **not** secret, it *does*
> appear in later reads, and it exists so a key can be identified in a log or a
> support conversation without being reconstructible.

#### `PUT /console/sites/{site_id}`

```json
{"name": "…", "address": "…", "timezone": "…", "active": false}
```

Metadata only; every field optional, and only what is supplied is applied.

`active: false` is **deactivation, not retirement**. The site key and every
terminal at the site stop authenticating immediately, and **nothing is
destroyed** — setting it back to `true` restores service. This is what "we are
closing this depot for a month" needs.

#### `DELETE /console/sites/{site_id}`

Retires the site **and soft-deletes every terminal at it, in one transaction**.

```json
{"retired": true, "terminals_retired": 3}
```
→ `200`

> **This stops doors opening.** A terminal whose site is retired fails
> authentication immediately — on its own device key as well as on the site key,
> because both middlewares refuse a device whose site is gone. `terminals_retired`
> is how many; a client that does not show that number is not describing what
> happened. One-way through the API; use `PUT active:false` if you want it back.

Rows are soft-deleted and retained for audit. The site's *name* becomes reusable,
because the per-company unique index ignores retired rows.

#### `POST /console/sites/{site_id}/api-key`

Issues a replacement key and **invalidates the previous one immediately**. There
is no overlap window — a window is a period in which a credential believed to be
revoked still provisions hardware.

```json
{
  "credential": {"api_key": "ats_…", "api_key_prefix": "ats_…", "shown_once": true},
  "legacy_terminals": 2
}
```
→ `200`

`legacy_terminals` counts terminals at this site that have **never been issued a
device credential of their own** and therefore certainly still authenticate with
the site key — the ones this rotation just locked out. **Terminals holding their
own `X-Device-Key` are unaffected**: that is a different secret and rotation does
not touch it.

### Terminals

| Method | Path | Role | Returns |
|---|---|---|---|
| `GET` | `/console/terminals` | VIEWER | `{count, terminals: [...]}` |
| `GET` | `/console/terminals/summary` | VIEWER | fleet counts |
| `GET` | `/console/terminals/{serial}` | VIEWER | inventory row + application configuration |
| `PUT` | `/console/terminals/{serial}/application-mode` | MANAGER | inventory row + application configuration |

`terminals` entries are the inventory objects from
[section 7](#get-apiv1devicesoutdatedtrue). They carry **no credential material**
— not the device key, not its hash, not the site key. `?outdated=true` filters as
it does there.

**All four are narrowed by site grants**, including the summary: the counts sit
above a list that is itself narrowed, so a company-wide rollup would both misread
to a scoped operator and disclose how much hardware stands at sites they were
deliberately not given. The two `{serial}` routes are gated on the grant to that
terminal's own site — `403` for an ungranted site in your company, `404` for
another tenant's serial or one that does not exist. The gate runs **before** the
handler, so a malformed body against a terminal you may not reach is still `403`
rather than a `400` that would confirm the serial exists.

`GET /console/terminals/{serial}` returns the **same inventory row as the list**,
plus the application assignment:

```json
{
  "public_id": "…", "serial_number": "TERM-1", "device_name": "Front Desk",
  "site_id": 4, "site_public_id": "6adb7321-…", "site_name": "Lagos Depot",
  "device_type": "TERMINAL",
  "status": "ONLINE", "active": true, "release_channel": "STABLE",
  "firmware_version": "1.2.0", "current_firmware_version": "1.3.0",
  "firmware_outdated": true, "hardware_revision": "rev-c", "build_number": "456",
  "boot_count": 12, "last_heartbeat_at": "…", "last_seen_at": "…",

  "application_mode": "CHECK_IN",
  "effective_applications": ["CHECK_IN"]
}
```

`application_mode` is what the terminal is **assigned**;
`effective_applications` is what that **resolves to now**, and goes empty when
the company disables the capability — the assignment is retained, not rewritten.
`PUT …/application-mode` returns this same shape, so a client can use the
response instead of refetching.

```json
PUT {"application_mode": "CHECK_IN"}
```

| Code | When |
|---|---|
| `400` | not a known mode (the response lists the accepted values) |
| `404` | no such terminal in this company |
| `409` | that capability is not enabled for the company |

**Registering a terminal is not a console operation.** It stays on
`POST /api/v1/devices/register` behind the site API key, which is the only place a
device credential is ever issued, exactly once, at registration.

### People

| Method | Path | Role |
|---|---|---|
| `GET` | `/console/people?limit=&offset=&q=` | VIEWER |
| `GET` | `/console/people/{external_id}` | VIEWER |
| `POST` | `/console/people` | MANAGER |
| `PUT` | `/console/people/{external_id}` | MANAGER |
| `DELETE` | `/console/people/{external_id}` | MANAGER |

The list is **paginated and searchable**:

| Parameter | Default | Bounds | Meaning |
|---|---|---|---|
| `limit` | 50 | 1–200 | page size |
| `offset` | 0 | ≥ 0 | rows to skip |
| `q` | — | ≤ 100 chars | matches `external_id` **or** `full_name`, anywhere, case-insensitively |

Out-of-range and unparseable values are **clamped, not rejected** — a `limit` of
5000 is a caller asking for as much as it can have, and `limit=abc` falls back to
the default rather than failing the request.

`%`, `_` and `\` in `q` are matched **literally**. Without that, `%` would select
the whole roster, `_` would match any single character, and a trailing `\` would
produce a malformed pattern; a search box can pass any of them safely. `q` is
trimmed and truncated to 100 characters — a term longer than any stored value
cannot match anything.

```json
{
  "count": 50, "total": 1284, "limit": 50, "offset": 0, "has_more": true,
  "people": [ … ]
}
```

`count` is this page; `total` is the size of the whole match, so `total` reflects
the search rather than the roster. Ordering is newest first with a stable
tiebreak, so paging visits every row exactly once.

**`GET /api/v1/members` (site key) is deliberately unchanged** — it still returns
a bare array of everyone. Terminals and existing tooling speak that contract, and
bounding it would silently truncate a roster somebody depends on being complete.

```json
{
  "id": "7ac1…", "external_id": "P-100", "full_name": "Sam Taylor",
  "category": "STANDARD", "active": true,
  "biometric_enrolled": true,
  "created_at": "…", "updated_at": "…"
}
```

- `external_id` is the identifier a terminal reads. Unique per company. Required
  on create, taken from the path on update.
- `category` is **optional** and free text, defaulting to `STANDARD`. It maps to a
  legacy column; the platform has no opinion about what class of person a company
  records.
- **`biometric_enrolled` is the entire biometric surface.** No template, locator
  or credential detail is ever returned. Biometrics are an abstraction the backend
  owns — do not model a person as *having a fingerprint*, model them as having
  zero or more credentials whose details the API will describe when that resource
  exists.
- An update **never** alters a person's biometric enrolment. Enrolment happens at
  a terminal, through the enrolment flow.

`POST` → `201`. `409` if the `external_id` is taken. `PUT`/`DELETE` → `200`/`204`,
and deleting is idempotent. Person writes enqueue the `CREATE`/`UPDATE`/`DELETE`
sync jobs that keep terminals in step.

People are company-wide in this schema, so **site grants do not narrow the people
list** — see the known limitations.

### Operators

All ADMIN. `{operator_id}` is an operator's `public_id`.

| Method | Path |
|---|---|
| `GET` | `/console/operators` |
| `POST` | `/console/operators` |
| `GET` | `/console/operators/{operator_id}` |
| `PUT` | `/console/operators/{operator_id}` |
| `DELETE` | `/console/operators/{operator_id}` |
| `GET` | `/console/operators/{operator_id}/sites` |
| `PUT` | `/console/operators/{operator_id}/sites` |

```json
POST {"email": "…", "full_name": "…", "password": "…", "role": "MANAGER",
      "site_ids": ["5120…"]}

PUT  {"role": "ADMIN", "active": false, "password": "…"}   // all optional
PUT  /sites {"site_ids": ["5120…", "80e5…"]}               // replaces wholesale
```

Guards, each a `403`:

- an **ADMIN cannot create, promote to, or modify an OWNER** — otherwise ADMIN is
  a synonym for OWNER one request later;
- nobody may change **their own** role, disable themselves, or delete themselves —
  a sole OWNER demoting themselves would leave nobody able to manage operators.

`409` on a duplicate address, `400` on a password below the policy. An
administrative password reset revokes **every** session the target holds. Site
grants are resolved inside the caller's company: an unknown or foreign site fails
the whole call with `400` and changes nothing.

### Applications

| Method | Path | Role |
|---|---|---|
| `GET` | `/console/applications` | VIEWER |
| `PUT` | `/console/applications/{code}` | OWNER |

See [section 14](#14-applications-and-modules).

---

## 14. Applications and modules

AccessLink is a **general-purpose biometric terminal platform**. The same
hardware and the same API serve a company running door access, one recording
attendance, and one doing nothing but identity verification. What a deployment is
*for* is configuration, not a property of the product.

An **application** is a capability the platform offers:

| Code | Capability |
|---|---|
| `ACCESS_CONTROL` | decide whether to release a door, barrier or lock |
| `ATTENDANCE` | record presence against a schedule |
| `REGISTRATION` | enrol people and their credentials |
| `CHECK_IN` | record arrival at an event or appointment |
| `VERIFICATION` | confirm a person is who they claim, and report it |
| `TIME_TRACKING` | accumulate worked time from arrivals and departures |
| `VISITOR_MANAGEMENT` | admit and record people who are not on the roster |

**Nothing is enabled by default.** A company with no capabilities enabled is a
legitimate, fully-working state, and it is the state every company starts in. A
client must render that rather than fall back to a default set — assuming a
workflow is precisely what this model exists to prevent. Read the catalog from
`available` rather than hard-coding it, so a capability added to the platform
appears without a client change.

### `GET /api/v1/console/applications`

```json
{
  "configured": [
    { "id": "…", "code": "ATTENDANCE", "enabled": true,
      "settings": {"grace_minutes": 5},
      "created_at": "…", "updated_at": "…" }
  ],
  "enabled": ["ATTENDANCE"],
  "available": ["ACCESS_CONTROL", "ATTENDANCE", "REGISTRATION", "CHECK_IN",
                "VERIFICATION", "TIME_TRACKING", "VISITOR_MANAGEMENT"]
}
```

`configured` holds every row the company has, enabled or not; `enabled` is the
subset currently on. **A disabled capability is never reported as enabled** — but
its row and its settings are retained, so turning it back on restores what was
configured rather than starting over.

### `PUT /api/v1/console/applications/{code}`

OWNER + CSRF.

```json
{"enabled": true, "settings": {"grace_minutes": 5}}
```

Both fields optional: omitting `enabled` enables, and omitting `settings` leaves
the existing configuration alone, so a toggle does not discard it. `settings` must
be a **JSON object**. `400` for an unknown code, a lowercase code, a non-object
`settings`, or `MULTI_PURPOSE`.

### Terminal application mode

Separate from company capabilities, and per **terminal** — one site routinely
mixes purposes, such as a door terminal at the entrance and a registration desk
in the office.

`application_mode` is one capability code **or** `MULTI_PURPOSE`:

- **`MULTI_PURPOSE`** is the default for every terminal, and means the terminal
  serves whatever its company has enabled. It is a **device mode, never a company
  capability** — `PUT /console/applications/MULTI_PURPOSE` is a `400`.
- A specific mode may only be assigned while the company has that capability
  enabled (`409` otherwise).
- If the company later disables it, the assignment is **retained** and
  `effective_applications` goes empty. Re-enabling restores it; the terminal's
  configuration is never silently rewritten.

`effective_applications` is what the terminal actually serves right now, and is
empty for a company that has enabled nothing.

**The device protocol is unaffected.** No device-facing endpoint reports an
application mode, and no terminal is told anything new — a terminal in the field
cannot tell whether any of this is configured.

---

## 15. Authorization — permissions and schedules

The engine that decides whether somebody may present a credential at a terminal.
Before this existed, `permissions` was a table nothing read: every active person
with a bound credential opened every terminal in their company, permanently.

**The rule, stated once:**

> Absence of permission is not permission.

A decision is DENIED unless a live, matching, in-window `ALLOW` says otherwise,
and any matching `DENY` beats every `ALLOW` at any scope.

### How a decision is reached

1. The terminal, its site and its company must all be in service.
2. The capability the terminal is assigned to must be enabled for the company.
3. The person must resolve inside the terminal's company, be active, and be
   inside their own validity window.
4. The credential, where one is named, must belong to that person, be `ACTIVE`,
   and be inside its validity window.
5. Every live permission whose scope covers the terminal and whose application
   matches is collected. Any `DENY` wins outright. Otherwise any `ALLOW` grants.
6. Otherwise denied, because absence of permission is not permission.

### Reason codes

Returned on **every** outcome including grants. A trail that records why somebody
was refused but not why they were admitted answers half the question a security
review asks.

| Reason | Meaning |
|---|---|
| `ALLOWED` | A matching rule admitted them. |
| `NO_PERMISSION` | Nothing granted access here. The deny-by-default answer. |
| `EXPLICIT_DENY` | A `DENY` rule matched. |
| `OUTSIDE_SCHEDULE` | A rule matched but its schedule could not be evaluated. |
| `PERMISSION_EXPIRED` / `PERMISSION_NOT_YET_VALID` | Validity window closed or not yet open. |
| `PERSON_INACTIVE` / `PERSON_UNKNOWN` | The subject. |
| `CREDENTIAL_UNKNOWN` / `_REVOKED` / `_SUSPENDED` / `_EXPIRED` / `_NOT_YET_VALID` | The credential. |
| `APPLICATION_NOT_ENABLED` | The capability is off for this company. |
| `TERMINAL_DISABLED` / `SITE_INACTIVE` / `COMPANY_INACTIVE` | Context out of service. |

### Scopes

A permission names exactly one:

| Scope | Reaches |
|---|---|
| `COMPANY` | every terminal the company has, including ones installed later |
| `SITE` | every terminal at one site (`site_id` required) |
| `TERMINAL` | exactly one terminal (`device_serial` required) |

### `GET /api/v1/console/people/{external_id}/permissions`

Role **VIEWER**. Returns `{count, permissions[]}`.

```json
{
  "id": "9f1c…",
  "person_id": "3ab2…",
  "scope_type": "SITE",
  "site_id": "7c4d…",
  "site_name": "North Gate",
  "application": "ACCESS_CONTROL",
  "effect": "ALLOW",
  "schedule_id": "1f0a…",
  "schedule_name": "Office hours",
  "starts_at": null,
  "ends_at": "2026-12-31T00:00:00Z",
  "active": true
}
```

`application` empty means the rule applies to whatever the terminal is doing.
`schedule_id` empty means no time restriction.

### `POST /api/v1/console/people/{external_id}/permissions`

Role **MANAGER**. CSRF required. **Idempotent** — it upserts onto the unique
index for the scope, so a retried create is not a conflict.

```json
{
  "scope_type": "SITE",
  "site_id": "7c4d…",
  "application": "ACCESS_CONTROL",
  "effect": "ALLOW",
  "schedule_id": "1f0a…",
  "starts_at": null,
  "ends_at": "2026-12-31T00:00:00Z",
  "active": true
}
```

`effect` defaults to `ALLOW`. A `COMPANY` scope must not name a site or terminal;
a `SITE` or `TERMINAL` scope must name one. `201` with the stored permission.
`404` if the person, site, terminal or schedule is not in the caller's company.

### `DELETE /api/v1/console/permissions/{permission_id}`

Role **MANAGER**. CSRF required. Soft-deletes the rule.

### Schedules

A schedule is a **named, reusable set of windows**, referenced by many
permissions — so "office hours" is edited in one place rather than retyped on
every rule.

| Method | Path | Role |
|---|---|---|
| `GET` | `/console/schedules` | VIEWER |
| `POST` | `/console/schedules` | MANAGER |
| `PUT` | `/console/schedules/{schedule_id}` | MANAGER |
| `DELETE` | `/console/schedules/{schedule_id}` | MANAGER |

```json
{
  "name": "Office hours",
  "timezone": "Europe/London",
  "active": true,
  "windows": [
    {"days_of_week": 31, "start_time": "09:00", "end_time": "17:00"},
    {"days_of_week": 32, "start_time": "09:00", "end_time": "13:00"}
  ]
}
```

`days_of_week` is a bitmask: Mon=1, Tue=2, Wed=4, Thu=8, Fri=16, Sat=32, Sun=64,
every day=127.

**Windows may cross midnight.** `end_time <= start_time` means the window runs
into the following day, and `days_of_week` names the day it **starts** on — so a
22:00–06:00 Friday window admits Friday 23:00 and Saturday 02:00, and refuses
Friday 05:00.

`timezone` omitted means the window is evaluated in the timezone of the site the
terminal stands at. Set it explicitly for a company running one shift pattern
across several countries.

**On update, `windows` REPLACES the whole set.** A schedule is a set, and the
caller sends the set it wants.

`DELETE` answers **409** while permissions still reference the schedule. The
foreign key is `ON DELETE SET NULL`, which on a soft delete would silently widen
every rule that used it — a permission restricted to office hours would become
one with no time restriction at all.

### `POST /api/v1/console/terminals/{serial}/evaluate`

Role **MANAGER**. CSRF required. *"Would this person get in here, right now, and
why."*

```json
{"external_id": "P-1042", "credential_id": "…", "application": "ACCESS_CONTROL",
 "at": "2026-08-18T09:30:00Z"}
```

`at` makes a schedule testable without waiting for Tuesday. Returns an
`AccessDecision` — `granted`, `reason`, `person_id`, `person_name`,
`external_id`, `credential_id`, `application`, `matched_permission`,
`decided_at`.

**This writes no event.** A preview that recorded a door event would put a
presentation that never happened into the trail an attendance report is built
from.

### What a new person is allowed

`companies.default_person_access` decides what permission is written when
somebody is created:

| Value | Effect |
|---|---|
| `COMPANY_ALLOW` | a company-scoped `ALLOW` is written at creation |
| `NONE` | nothing is written; they can open nothing until a rule says otherwise |

Companies that existed before the engine were migrated to `COMPANY_ALLOW`, which
reproduces their previous behaviour exactly. Companies created through the
platform API start at `NONE`. The grant is a real permission row an operator can
see and remove, not a special case in the evaluator.

---

## 16. Events — the activity trail

What actually happened in the field. Distinct from `/console/audit`, which
records what **operators changed**.

`access_logs` is unchanged and still carries the device wire contract. `events`
is the model going forward: an open `event_type`, an `application`, four
decisions, a `direction`, a machine-readable `reason_code` and an opaque
`payload` — enough to carry attendance, check-in, verification and visitor
records without another migration.

### `GET /api/v1/console/events`

Role **VIEWER**. **Grant-scoped**: an operator scoped to one site sees that
site's events only.

| Parameter | Meaning |
|---|---|
| `limit` / `offset` | default 50, max 500 |
| `site_id`, `serial`, `person_id`, `external_id` | narrow to one subject |
| `event_type`, `application`, `decision`, `direction` | narrow by kind |
| `from`, `to` | RFC3339, on `occurred_at` |
| `q` | free text over person name and the id the terminal read |

`decision` is one of `GRANTED`, `DENIED`, `RECORDED`, `ERROR`. `RECORDED` is the
no-outcome case: nothing was released, the event **is** the outcome.

**A malformed `from`/`to` is a 400, not an ignored filter.** Silently dropping
`from=lastweek` and answering with the whole trail looks like an answer to the
question that was asked. The same now applies to `since`/`until` on
`/console/audit`, which previously ignored them.

```json
{
  "count": 2, "total": 2, "limit": 50, "offset": 0, "has_more": false,
  "events": [{
    "id": "8c2f…",
    "event_type": "ACCESS_DENIED",
    "application": "ACCESS_CONTROL",
    "decision": "DENIED",
    "reason": "NO_PERMISSION",
    "site_name": "North Gate",
    "device_serial": "ESP32-0007",
    "person_id": "3ab2…",
    "person_name": "A Person",
    "subject_external_id": "P-1042",
    "credential_id": "…",
    "credential_type": "FINGERPRINT",
    "occurred_at": "2026-08-15T09:30:00Z",
    "recorded_at": "2026-08-15T09:30:02Z",
    "occurred_at_trusted": true
  }]
}
```

**Two times, and the difference matters.** `occurred_at` is when it happened at
the terminal; `recorded_at` is when the server heard. An event queued through an
outage and uploaded hours later has an `occurred_at` hours before its
`recorded_at`. Filters and ordering use `occurred_at`, because an operator asking
what happened on Tuesday means at the door. `occurred_at_trusted` is false when
the terminal's clock was not believable and the server stamped arrival instead.

### Divergence

When a terminal reports a decision the platform would not have made, the event
records the **terminal's** decision — that is the one that released the lock —
and carries the platform's verdict beside it:

```json
"payload": {"source": "FINGERPRINT", "diverged": true,
            "server_decision": {"granted": false, "reason": "NO_PERMISSION"}}
```

That is a terminal running on a cache it synced before a permission was revoked.
It is what the site's offline policy is a trade against, and it is now visible
and searchable.

---

## 17. Device protocol additions

Everything in this section implements
`docs/firmware-protocol-requirements.md` in the **firmware** repository, which
is the contract of record between the two sides. The field names, enum
spellings and shapes below were taken from the firmware source that parses them
(`sync_job.cpp`, `heartbeat.cpp`, `provisioning.cpp`, `credential_ref.h`), not
from prose.

**`SyncProtocolVersion` is unchanged and stays at 1.** Every addition is either
a new optional key inside an existing object or a new route, which is the
extension path the compatibility policy already relies on.

### 17.1 The site's offline policy reaches the terminal

Two keys inside the existing `settings` object, on **both**
`GET /api/v1/devices/settings` and every `SETTINGS` job payload:

```json
{
  "settings_version": 7,
  "settings": {
    "unlock_duration_seconds": 5,
    "sync_interval_seconds": 60,
    "offline_policy": "CACHED_GRACE",
    "offline_grace_minutes": 720
  }
}
```

| Key | Type | Values |
|---|---|---|
| `offline_policy` | string | `DENY_ALL`, `CACHED_GRACE`, `CACHED_INDEFINITE` |
| `offline_grace_minutes` | integer | `0`–`43200` |

Both come from `sites.offline_policy` and `sites.offline_grace_minutes`.

**The columns are layered OVER the stored settings blob.** `sites.settings` is a
free-form JSON object an operator can write anything into; the policy is a
validated column. If the two disagree, the column wins — otherwise an operator
could bypass a safety control by writing raw JSON.

**Setting it:** `PUT /api/v1/console/sites/{site_id}` now accepts
`offline_policy` and `offline_grace_minutes`. **ADMIN**, matching site key
rotation, because it decides what a door does during an outage.

Changing either **increments `settings_version` and queues a `SETTINGS` job to
every terminal at the site**. This is required, not incidental: the terminal
gates a push behind a strictly-greater version check, so a policy change written
without the bump is discarded as a replay — a silent no-op the console would
report as applied.

Renaming a site does **not** bump the version or push a job.

**Backward compatibility.** Purely additive. Firmware that does not know the keys
ignores them; firmware that does treats absent keys as "leave the stored policy
alone", so old-server/new-terminal and new-server/old-terminal both behave
exactly as before.

### 17.2 Firmware updates on the heartbeat response

`POST /api/v1/devices/heartbeat` gains one optional object:

```json
{
  "protocol_version": 1,
  "device_id": "AT-A1B2C3",
  "server_time": "2026-08-15T09:30:00Z",
  "pending_jobs": 0,
  "firmware_update": {
    "version": "1.2.0",
    "download_url": "https://updates.example.com/at-1.2.0.bin",
    "checksum_sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "size_bytes": 1003664,
    "is_mandatory": false
  }
}
```

**Absent when there is nothing to offer**, so a fleet that is up to date sees
exactly the response it saw before this field existed.

The offer is the `is_current` build for the device's own **company, device type
and release channel**, and is withheld when the device already runs that
version.

**Only one transport is implemented — this one.** The requirements document
offered a `FIRMWARE` sync job as an alternative; it is deliberately not built,
because the firmware's `syncJobTypeFromName` has no `FIRMWARE` case, so such
jobs would be enqueued per terminal per poll only to be acknowledged and
discarded. Adding it later is additive and needs no version bump.

**The server withholds an offer that breaks any of the four hard rules**, and
logs the catalogue row and the reason:

1. `checksum_sha256` present — the device refuses an offer without one.
2. **Lower-case** 64-hex — the device refuses upper case rather than folding it.
3. `size_bytes` present and positive — it sizes the flash write and is trusted
   over `Content-Length`.
4. `download_url` is `https`.

Two further limits come from the device's fixed buffers: a `download_url` over
127 characters or a `version` over 23 is withheld, because the terminal would
truncate it and fetch the wrong image.

`server_time` is RFC3339 **UTC with a `Z`**. The terminal adopts it as a clock
when it cannot reach NTP and **refuses a numeric offset** rather than
mis-parsing it, so this must never become a local time.

### 17.3 Credential placements, device-facing

Two device-authenticated routes. **No biometric material crosses either, in
either direction.** A `credential_id` is a handle naming which credential a
report is about; the template stays on the sensor that captured it, which is all
the fitted hardware permits.

#### `GET /api/v1/devices/credentials/pending`

What this terminal is expected to hold and does not — the work list that makes
"person A is recognised at terminals A, B and C" real without moving a template.

`?limit=` defaults to 25, capped at 100.

```json
{
  "protocol_version": 1,
  "device_id": "AT-A1B2C3",
  "count": 1,
  "credentials": [{
    "credential_id": "8c2f…",
    "placement_id": "1a9b…",
    "member_id": "MEM001",
    "full_name": "A Person",
    "credential_type": "FINGERPRINT",
    "template_format": "SENSOR_LOCAL",
    "vendor": "ZFM",
    "state": "PENDING",
    "attempts": 0,
    "last_error": "",
    "generation": 1
  }]
}
```

Scoped to the **authenticated device** — there is no parameter naming a
terminal — and to people that terminal's **permissions would admit**, so the
enrolment surface is exactly as narrow as the access surface. Credentials that
are `REVOKED`/`SUSPENDED`, or already `PLACED`/`REMOVING`/`REMOVED` here, are
excluded.

#### `POST /api/v1/devices/credentials/placement`

```json
{"credential_id": "8c2f…", "member_id": "MEM001",
 "credential_type": "FINGERPRINT", "template_format": "SENSOR_LOCAL",
 "vendor": "ZFM", "slot": 5, "state": "PLACED", "error": ""}
```

`state` is `PLACED`, `FAILED` or `REMOVED`. `PENDING` and `REMOVING` are the
**platform's** intentions and are refused with 400 — a terminal claiming either
would be a device deciding what the platform wants.

`slot` is 1-based and required for `PLACED`. `error` carries the device's own
words ("sensor full") so an operator sees why.

Idempotent on `(credential, device)`. A `PLACED` report promotes a `PENDING`
credential to `ACTIVE`. A credential named in the body that does not belong to
`member_id`, or to this tenant, is a **404**.

#### `generation`

`credential_placements.generation` (migration 019) records the sensor era a
placement belongs to, copied from `devices.placement_generation`. The sensor
hands out the lowest free slot, so a slot freed by a deletion names a different
finger afterwards; the counter removes the ambiguity outright.

#### Enrolment results

`POST /api/v1/devices/enrollment/result` now **binds** the `credential` object
the firmware already sends, writing a real credential and placement:

```json
{"member_id": "MEM001",
 "fingerprint_template": "terminal:AT-0001:slot:5",
 "credential": {"credential_type": "FINGERPRINT", "template_format": "SENSOR_LOCAL",
                "vendor": "ZFM", "terminal": "AT-0001", "slot": 5}}
```

`fingerprint_template` is unchanged, still required, and still written — it is a
**locator, not material**, and deployed firmware plus the 012 back-fill depend on
it. The `credential` object is optional; older firmware omits it and the
enrolment completes exactly as before. Placement writing is best-effort: it
never fails an enrolment that has already committed. `terminal` is recorded but
**not trusted** — the placement is written against the authenticated device.

### 17.4 `POST /api/v1/devices/claim`

**Unauthenticated by necessity**: the caller is a terminal that has no
credential, and obtaining one is what it is for. This removes the need to put the
**site API key** — which registers devices and rotates their credentials — on an
installer's laptop.

```
POST /api/v1/devices/claim
{"claim_code": "K7M2-P4QX", "serial_number": "AT-A1B2C3"}

200 {"api_key": "atd_…", "serial_number": "AT-A1B2C3"}
```

The response is deliberately tiny: the firmware refuses a body over 512 bytes.
`api_key` is `atd_` + 64 lower-case hex, which is what
`DeviceCredential::keyLooksIssued` requires.

| Status | Meaning to the firmware |
|---|---|
| `200` | claimed |
| `400` | malformed body — the endpoint exists |
| `401` | the code was refused |
| `404` | **this server does not support claim codes**; use `set key` |
| `429` | rate limited |
| `5xx` | server error, retry |

`404` is reserved for "unsupported" and is never used for a bad code — it would
send an installer to look at the wrong thing.

**Every failure returns the same 401 with the same body.** Wrong code, expired,
superseded, or the right code with the wrong serial — all identical.
Distinguishing them would let an unauthenticated caller learn that a code is real
but the serial is wrong, which is exactly what turns an intercepted code back
into something worth having.

Security properties:

- **Single use** — redemption marks it, and issuing a new code for a serial
  supersedes any outstanding one.
- **Serial-bound** — the device sends the serial it derives from its factory MAC;
  a code redeemed by other hardware is refused, and the legitimate code survives
  the attempt.
- **Short-lived** — 2 hours by default, capped at 24.
- **Hashed at rest** — SHA-256; the plaintext exists once, in the response that
  mints it.
- **Rate limited** — its own limiter instance, so an installer retrying a
  mistyped code cannot exhaust the allowance an operator needs to sign in.
- **Audited** — `DEVICE_CLAIM_CODE_ISSUED` (who, which serial, code prefix) and
  `DEVICE_CLAIMED` (which unit, from which address). The code itself is never
  recorded.
- **Cannot revive a disabled terminal** — it goes through the same registration
  lifecycle as the site-key path, which refuses a `DISABLED` unit.

#### `POST /api/v1/console/sites/{site_id}/claim-codes`

**ADMIN.** Mints a code for one serial.

```json
{"serial_number": "AT-A1B2C3", "expires_in_minutes": 120}

201 {"claim_code": "K7M2-P4QX", "code_prefix": "K7M2",
     "serial_number": "AT-A1B2C3", "site_name": "North Gate",
     "expires_at": "…", "shown_once": true, "superseded_codes": 0}
```

The code is returned **once** and no read endpoint gives it back.
`serial_number` is limited to 15 characters — what the firmware can store —
refused when minted rather than discovered at a door. `superseded_codes` lets the
console warn that an installer's earlier printout has just stopped working.

### 17.5 What is still outstanding for the firmware side

Recorded because the requirements document lists them as done on the device and
they are not consumed today:

- **The heartbeat response's `firmware_update` object is not parsed.**
  `parseHeartbeatResponse` reads `protocol_version`, `pending_jobs` and
  `server_time` only. The OTA *execution* path is complete and reachable from the
  serial console; the network transport that feeds it is not wired.
- **`GET /devices/credentials/pending` has no client.** The firmware document
  describes it as what the terminal *would* use.
- **A `REMOVING` placement has no sync job to carry it.** Listed in the document
  as the third route; withdrawal is currently visible only through the pending
  list ceasing to offer the credential.

---

## Known limitations

Behaviour a client must design around today. This list is maintained against the
code, not against intent — an item is removed only when a test that would catch
its return exists and passes.

1. **The legacy `GET /access/{member_id}` still ignores permissions.** It tests
   membership status only. It is retained for deployed tooling and is
   **deprecated**: the authorization engine is reached through
   `POST /console/terminals/{serial}/evaluate` (operator) and is applied
   automatically to every event a terminal reports. Do not build anything new on
   the legacy route.
2. **`GET /members` is unpaginated.** `GET /console/people` is paginated and
   searchable; use it.
3. **Rate limiting covers the credential endpoints only.** `POST /auth/login`,
   `POST /auth/password`, `POST /auth/forgot-password`, `POST /auth/redeem` and
   the platform login share per-address allowances and per-account lockout. The
   limiter is **in-process**, so with more than one instance the effective rate
   multiplies by the instance count (SEC-09, open). Nothing else is limited — a
   leaked site key can still be brute-forced against `/devices/register`.
4. **The deprecated site-key + serial device auth is still accepted.** It cannot
   distinguish one terminal at a site from another beyond the serial the caller
   claims. It cannot be removed until firmware self-registration exists (FW-05).
5. **A conditional `DENY` is not enforced at an offline terminal.** A terminal
   caches a flat roster and does not evaluate permissions, so a `DENY` narrowed
   to one application or one schedule cannot be applied by removing the person
   from the roster without also refusing them when they *are* allowed. Those
   people stay on the roster and the server decides at the door. An
   **unconditional** `DENY` does remove them, so exclusion survives an outage.
   The site's offline policy bounds the exposure; `DENY_ALL` removes it.
6. **Permission validity windows take effect at an offline terminal on a
   reconciliation cycle, not instantly.** Roster membership changes with the
   clock, and the reconciler runs every 15 minutes by default
   (`ROSTER_RECONCILE_INTERVAL_SECONDS`). An online terminal is decided by the
   server at the door and is exact.
7. **Response envelopes are inconsistent** — bare arrays for members, access
   logs and enrollment; `{count, …}` objects for devices, firmware and every
   console list.
8. **Error message strings are not stable.** Branch on status codes.
9. **Terminals are not told their application mode.** The device protocol is
   unchanged, so a mode is operator-facing configuration and a server-side input
   to the decision, but the terminal does not know it (APP-03, open). Delivering
   it will be an additive field in the settings payload.
10. **Only `ACCESS_CONTROL` has behaviour.** `ATTENDANCE`, `CHECK_IN`,
    `VERIFICATION`, `TIME_TRACKING` and `VISITOR_MANAGEMENT` are configuration
    and an event model that can carry them; no capability logic is implemented
    on top. Enabling one changes what the dashboard should offer and what the
    authorization engine will refuse, not what the platform does with the event
    afterwards.
11. **Biometric templates remain terminal-local, and this is a hardware
    limit rather than a schema one.** The fitted sensor's driver implements
    template export but **not import**, so a template captured at one door
    cannot be installed at another by this firmware at all. What the platform
    now does instead is tell each terminal which people it is expected to
    recognise and does not (section 17.3) — which turns "enrolled at one door,
    silently does nothing at the others" into a work list. No template ever
    crosses the device API in either direction.
12. **Site API keys are hashed** (SHA-256) and rotatable through
    `POST /console/sites/{site_id}/api-key`. Device keys are hashed and never
    stored in the clear. Neither is ever returned by a read endpoint — a key is
    shown once, at the moment it is minted.
13. **OTA offers are delivered, not yet consumed.** The heartbeat response
    carries a `firmware_update` object when a newer current build exists for the
    device's company, type and channel, and the server withholds one that breaks
    any of the four hard rules. The firmware's heartbeat parser does not read the
    field yet, so no fleet updates itself today — see section 17.5.
14. **A `REMOVING` placement has no delivery mechanism.** A credential withdrawn
    from a terminal stops appearing in that terminal's pending list, but nothing
    instructs it to erase the template it already holds. That is the third route
    in the firmware requirements and is not built.
15. **Claim codes remove the credential exposure, not the serial cable.** The
    code still has to be typed into the unit over a serial console, because the
    fitted keypad cannot reach the admin menu on this hardware revision.
16. **The 64-person on-device ceiling is a firmware constant.** The server no
    longer fans a whole company at every terminal (SEC-04), which removes most
    of the pressure, but a single site with more than 64 permitted people will
    still exhaust a terminal's table. The server-side capacity model and the
    firmware limit are both open (FW-01).
