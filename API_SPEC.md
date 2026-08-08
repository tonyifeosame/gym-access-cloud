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

---

## 1. Authentication

There are three authentication modes. Which one applies is stated on every
endpoint below.

### Site API key

```
X-API-Key: main-site-api-key-123
```

Identifies a **site**, and through it a company. Used by the dashboard and by
operator tooling. Every query is scoped to that site's company — a key issued to
one tenant cannot reach another's data.

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

**Auth: site API key.**

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
`CREATE` jobs (`bootstrap_jobs` reports how many).

| Error | Status | Body |
|---|---|---|
| `serial_number` missing | `400` | validator message |
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

31 endpoints.

---

## Known limitations

Behaviour a client must design around today:

1. **Access checks ignore permissions.** `GET /access/{member_id}` tests
   membership status only — no door scope, schedule, or validity window.
2. **`GET /members` is unpaginated.**
3. **No rate limiting** on any endpoint, including authentication. A leaked site
   key can be brute-forced against `/devices/register` without resistance, and
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
   logs, and enrollment; `{count, ...}` objects for devices and firmware.
9. **Error message strings are not stable.** Branch on status codes.
