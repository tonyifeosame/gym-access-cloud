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

### Terminals

| Method | Path | Role | Returns |
|---|---|---|---|
| `GET` | `/console/terminals` | VIEWER | `{count, terminals: [...]}` |
| `GET` | `/console/terminals/summary` | VIEWER | fleet counts |
| `GET` | `/console/terminals/{serial}` | VIEWER | application configuration |
| `PUT` | `/console/terminals/{serial}/application-mode` | MANAGER | application configuration |

`terminals` entries are the inventory objects from
[section 7](#get-apiv1devicesoutdatedtrue). They carry **no credential material**
— not the device key, not its hash, not the site key. `?outdated=true` filters as
it does there. The list is narrowed by site grants; the summary is company-wide,
because a partial rollup presented as the whole fleet would mislead.

`GET /console/terminals/{serial}` returns the **same inventory row as the list**,
plus the application assignment:

```json
{
  "public_id": "…", "serial_number": "TERM-1", "device_name": "Front Desk",
  "site_id": 4, "site_name": "Lagos Depot", "device_type": "TERMINAL",
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

## Known limitations

Behaviour a client must design around today:

1. **Access checks ignore permissions.** `GET /access/{member_id}` tests
   membership status only — no door scope, schedule, or validity window.
2. **`GET /members` is unpaginated.**
3. **Rate limiting covers the credential endpoints only.** `POST /auth/login`
   and `POST /auth/password` share a per-address allowance and a per-account
   lockout; the limiter is in-process, so with more than one instance the
   effective rate multiplies by the instance count. Nothing else is limited — a
   leaked site key can still be brute-forced against `/devices/register`, and
   nothing bounds how many terminals one key may enrol.
4. **Site API keys are stored in plaintext**, and are compared with a plain SQL
   equality. Device keys are hashed with SHA-256 and never stored in the clear.
   Fixing this needs a way to re-issue a site key, which no endpoint offers yet —
   see the note in README.md.
5. **The deprecated site-key + serial device auth is still accepted.** It cannot
   distinguish one terminal at a site from another beyond the serial the caller
   claims.
6. **People are tenant-wide, not site-scoped.** Every terminal in a company
   receives every person in that company, including terminals at other sites.
   Site settings, by contrast, reach only that site's devices. This is by
   design; door-level scoping belongs to the permission engine.
7. **`/metrics` is fleet-wide and unauthenticated unless `METRICS_TOKEN` is
   set.** It exposes counts across all tenants. Keep it off the public network
   or set the token.
8. **Response envelopes are inconsistent** — bare arrays for members, access
   logs, and enrollment; `{count, ...}` objects for devices, firmware, and every
   console list.
9. **Error message strings are not stable.** Branch on status codes.
10. **Site grants do not narrow `GET /console/people`** — people are company-wide
    (limitation 6), so the console reports the scope the API can actually
    enforce rather than implying one it cannot. The list itself is paginated and
    searchable; `GET /members` (site key) is still unpaginated per limitation 2.
11. **No application business logic exists yet.** The applications model records
    which capabilities a company has enabled and what each terminal is assigned
    to; nothing evaluates attendance, access decisions or check-in on top of it.
    Enabling a capability changes what the dashboard should offer, not what the
    platform does at a door.
12. **Terminals are not told their application mode.** The device protocol is
    unchanged, so a mode is operator-facing configuration until a module needs
    to deliver it — which will be an additive field in the settings payload.
