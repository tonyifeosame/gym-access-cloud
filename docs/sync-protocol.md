# Device Synchronization Protocol v1

The contract between an Access Terminal (ESP32) and the cloud API. Firmware and
server are built by different people in different repos, so this document — not
either codebase — is the source of truth for the seam between them.

**Current version: 1** (`models.SyncProtocolVersion`)

---

## Why jobs and not a changes feed

`GET /members/changes?since=...` can only describe rows that still exist. When a
person is deleted their row stops appearing in the feed, so a terminal that
cached their fingerprint never learns to remove it and keeps opening the door —
indefinitely, and silently.

Deletions are therefore pushed as durable, individually acknowledged jobs. A
change is not "done" when the server writes it; it is done when the device says
it applied it.

**The changes feed must never be used to communicate deletions.**

## Guarantees

| Property | How it is achieved |
|---|---|
| No lost changes | Jobs are enqueued in the same transaction as the write. A person cannot be created without its jobs. |
| At-least-once delivery | Fetching takes a 60s lease and leaves the job `PENDING`. Only an acknowledgement retires it. |
| Idempotent apply | `CREATE` and `UPDATE` are both **upserts** on the device. Redelivery is harmless. |
| Idempotent ack | Acknowledging an already-acknowledged job returns `200`, so a lost ack response can be safely retried. |
| Eventual convergence | Jobs accumulate while a device is offline and are delivered in `id` order when it returns. |
| Ordering | Jobs are delivered oldest-first by `id`. |

Delivery is **at-least-once, not exactly-once**. The device must tolerate seeing
the same job twice. That is why every operation is an upsert or a delete-if-present.

## Authentication

A device authenticates with **its own credential**, issued at registration:

| Header | Meaning |
|---|---|
| `X-Device-Key` | The device's credential. Preferred. |
| `X-Protocol-Version` | Protocol the firmware speaks. Omitted means v1. |

The credential is stored server-side as a SHA-256 hash and is returned in
plaintext exactly once, at registration. It cannot be recovered — a device that
loses it re-registers and is issued a new one.

### Deprecated fallback

`X-API-Key` (site key) plus `X-Device-Serial` is still accepted so firmware built
against the Sprint 4 protocol keeps working during a rollout. It is weaker by
construction: the site key is shared by every terminal at the site, so it cannot
distinguish one device from another beyond the serial the caller claims. **Move
to `X-Device-Key` and expect this path to be removed.**

## Response framing

**A client must not assume `Content-Length` is present, and must decode
`Transfer-Encoding: chunked`.**

This is not a hypothetical. Every response from the Render deployment is
chunked, including bodies of under a hundred bytes:

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
Transfer-Encoding: chunked

5d
{"commit":"5f8eb3e","service":"Access Terminal Cloud API","status":"healthy","version":"dev"}
0

```

The API does not choose this. Go sets `Content-Length` on a small response, and
the Nginx deployment in `deploy/` passes it through — but the managed edge in
front of the Render service re-frames responses on its way out, and the origin's
length header does not survive. **Framing is a property of the deployment, not
of the contract**, so firmware must handle both and must not be tuned to
whichever one a given environment happens to use.

The failure mode when it is not handled is quiet and total. A client reading the
socket directly receives the chunk-size line as though it were payload, so the
body begins `5d\r\n{` — which is not JSON. On the ESP32 that surfaced as
`Sync: response did not parse -- InvalidInput` on every sync cycle, while
heartbeat, enrolment upload, job acknowledgement and access-log upload all
appeared to work, because those calls only inspect the status code and never
parse the body. `GET /devices/jobs` is the only device call whose body is read,
which is what made a total transport fault look like a sync-specific one.

Two consequences for anyone writing a client:

- **Use an HTTP client that de-chunks**, rather than reading the socket. On the
  ESP32 that means `HTTPClient::writeToStream()`; `getStreamPtr()` is the raw
  stream and returns the framing with the payload.
- **Do not use `Content-Length` as the completeness check.** Over a chunked
  transfer there is nothing to compare against, so a length-based check silently
  passes rather than failing closed — and a partially delivered `FULL_SYNC`
  roster that is treated as complete is read as a list of deletions. Take
  completeness from whether the transfer itself finished.

Nothing about the JSON bodies documented below changes with the framing, and
this is not a protocol version concern: `SyncProtocolVersion` describes the
envelope, not the transport that carries it.

## `POST /api/v1/devices/register`

Authenticated with the **site** API key, which acts as the provisioning secret.

```json
{ "serial_number": "AT-0001", "device_name": "Front Door", "firmware_version": "1.0.0" }
```

```json
{
  "protocol_version": 1,
  "device_id": "0ff07b95-…",
  "serial_number": "AT-0001",
  "api_key": "atd_e6653a26…",
  "bootstrap_jobs": 2,
  "warning": "Store this api_key now. It cannot be retrieved again."
}
```

Registration is idempotent by serial: registering an existing serial **rotates**
its credential rather than failing, so a factory-reset terminal can recover. The
previous credential stops working immediately.

Registration also seeds the device with the current member list as `CREATE`
jobs (`bootstrap_jobs` reports how many). Re-registration re-seeds, on the
assumption that a device re-registering has lost its local state. Because those
are ordinary upserts queued after anything already pending, a re-seed is
harmless to a device that was actually fine.

A serial already registered to a **different** site returns `409` and changes
nothing — reassignment is a provisioning decision, not something registration
does silently.

A device an operator has taken out of service — `DISABLED`, or `active = false`
— returns `403` and changes nothing. Disabling is the only revocation this API
offers, so registration must not be a route around it: a re-registration of a
disabled serial neither re-enables the device nor issues a credential, and the
key already on the row is left exactly as it was.

## `POST /api/v1/devices/heartbeat`

```json
{ "firmware_version": "1.0.1", "hardware_revision": "rev-C",
  "build_number": "2481", "boot_count": 42,
  "status": "ONLINE", "error": "" }
```

```json
{ "protocol_version": 1, "device_id": "AT-0001",
  "server_time": "2026-08-07T17:40:31Z", "pending_jobs": 4 }
```

Records liveness and inventory. `pending_jobs` lets a device skip polling when
there is nothing waiting.

### Device states

| State | Meaning | Set by |
|---|---|---|
| `PROVISIONING` | Row exists, never registered | server |
| `ONLINE` | Heartbeating normally | device |
| `OFFLINE` | Missed its heartbeat window | server (sweep) |
| `UPDATING` | Applying firmware | device |
| `ERROR` | Reported a fault; see `last_error` | device |
| `DISABLED` | Administratively out of service | operator |

A device may only report `ONLINE`, `UPDATING` or `ERROR` for itself. `OFFLINE`
is inferred by the server, and `DISABLED` is an administrative decision — a
heartbeat from a disabled device records liveness without returning it to
service. Anything else a device sends is treated as `ONLINE`.

A device in `ERROR` still receives sync jobs; only `DISABLED` devices are
excluded from fan-out. An errored terminal is expected to converge once it
recovers.

## `GET /api/v1/devices/settings`

The device's effective settings, inherited from its site. Devices normally
receive settings as `SETTINGS` jobs; this is the pull equivalent, for confirming
configuration after a restart.

If the device declares a version **higher** than the server supports, the server
returns `400` rather than sending a payload the firmware would misparse:

```json
{ "error": "Unsupported protocol version",
  "server_protocol_version": 1, "device_protocol_version": 2 }
```

This is what lets a newer server keep serving older firmware mid-rollout.

## `GET /api/v1/devices/jobs?limit=50`

Returns due work, oldest first, and takes a delivery lease on each job returned.

```json
{
  "protocol_version": 1,
  "device_id": "AT-0001",
  "server_time": "2026-08-07T18:27:36Z",
  "count": 2,
  "jobs": [
    {
      "id": 7,
      "public_id": "…uuid…",
      "protocol_version": 1,
      "job_type": "DELETE",
      "entity_type": "PERSON",
      "entity_external_id": "MEM001",
      "payload": {
        "member_id": "MEM001",
        "full_name": "Ada L. Byron",
        "membership_type": "ANNUAL",
        "active": true,
        "deleted": true,
        "updated_at": "2026-08-07T18:27:37Z"
      },
      "attempts": 0,
      "created_at": "2026-08-07T18:27:37Z"
    }
  ]
}
```

`limit` defaults to 50, capped at 200.

### Job types

| `job_type` | Device action |
|---|---|
| `CREATE` | Upsert the person from `payload` |
| `UPDATE` | Upsert the person from `payload` (identical handling to `CREATE`) |
| `DELETE` | Remove the person identified by `payload.member_id`. Succeed if already absent. |
| `SETTINGS` | Apply `payload` as device settings |
| `FULL_SYNC` | Reconcile the local roster against `payload.member_ids` — delete any local member not in the list |

### FULL_SYNC and backlog compaction

A device that has been offline a long time accumulates one job per change.
Rather than making it replay history, the server collapses its queue into a
snapshot once the backlog passes `SYNC_COMPACTION_THRESHOLD` (default 500).
The response sets `"snapshot_taken": true` and the queue becomes:

1. one `FULL_SYNC` carrying the authoritative roster,
2. one `CREATE` per live member (with templates),
3. one `SETTINGS`.

```json
{ "job_type": "FULL_SYNC",
  "payload": { "snapshot": true, "count": 5,
               "member_ids": ["MEM001","MEM002","MEM003","MEM004","MEM005"] } }
```

`FULL_SYNC` is a **set reconciliation, not a wipe**: delete local members absent
from `member_ids`, and leave the rest alone. That distinction is load-bearing.
A wipe-then-repopulate design loses data — if the wipe's acknowledgement were
lost after the following `CREATE`s had already been applied and acked,
redelivery would clear the device with nothing left to repopulate it. Framed as
a set difference, redelivery is a no-op once converged.

It carries IDs only, no templates, so a thousand members is roughly ten
kilobytes. The `CREATE` jobs that follow supply anything the device is missing.

An operator can force this with `POST /api/v1/devices/{serial}/resync`.

`payload.deleted` is `true` only on `DELETE`. A terminal that trusts `job_type`
alone is correct; `deleted` is a redundant safety check.

### SETTINGS payload

Settings live at the site; every device at that site receives the same push.

```json
{
  "job_type": "SETTINGS",
  "entity_type": "SETTINGS",
  "payload": {
    "settings_version": 2,
    "settings": {
      "unlock_duration_seconds": 8,
      "sync_interval_seconds": 30,
      "offline_grace_minutes": 120,
      "tamper_alarm": true
    }
  }
}
```

`settings_version` increases by one on every change. **A device must ignore a
SETTINGS job whose `settings_version` is lower than the one it already holds**,
which is what keeps settings idempotent under redelivery and reordering.

`settings` is an opaque JSON object to the transport — adding a key does not
change the protocol version. Firmware must ignore keys it does not recognise.

Settings are managed by an operator through:

- `GET /api/v1/sites/settings` — read current settings and version
- `PUT /api/v1/sites/settings` — replace settings; bumps the version and fans
  a SETTINGS job out to every device at the site in the same transaction

## `POST /api/v1/devices/jobs/{id}/complete`

```json
{ "status": "COMPLETED" }
```

or, on failure:

```json
{ "status": "FAILED", "error": "fingerprint sensor busy" }
```

An empty body means `COMPLETED`. Response:

```json
{ "protocol_version": 1, "job_id": 7, "status": "COMPLETED", "pending_jobs": 3 }
```

`pending_jobs` is the device's remaining unacknowledged backlog — useful as a
"keep polling" signal.

| Outcome | Result |
|---|---|
| `COMPLETED` | Job retired. Re-acking returns `200`. |
| `FAILED` | `attempts++`, job stays `PENDING`, retried with exponential backoff (1s → 15m cap). After `max_attempts` (default 10) it is parked in `FAILED`. |
| Another device's job | `404` |

## Expected device loop

1. Poll `GET /devices/jobs`.
2. Apply each job **in the order received**.
3. Acknowledge each job individually as it is applied.
4. If `pending_jobs > 0`, poll again immediately; otherwise back off.
5. On restart, just poll — anything unacknowledged is still queued.

A device must **not** consider a job done because it received it. If it applies a
job and then loses power before acknowledging, it will receive that job again.
That is correct and safe, because applies are idempotent.

## Not yet in v1

These are known gaps, not oversights:

- **OTA.** Firmware is inventory only. Marking a build current changes what
  "outdated" means; nothing downloads, schedules, or applies firmware, and no
  `FIRMWARE_UPDATE` job is ever dispatched.
- **Site API keys are still stored in plaintext.** Device credentials are
  hashed, but `sites.api_key` predates that and was left alone deliberately —
  hashing it needs its own migration and a rotation window.
- **The offline sweep is not scheduled.** `MarkDevicesOffline` exists but nothing
  calls it periodically yet, so `status` only advances to `OFFLINE` when
  something invokes the sweep.
- **Permission jobs.** Only `PERSON` and `SETTINGS` entities sync today. Doors,
  schedules, and permissions arrive with the permission engine (Sprint 6).

## Changing this protocol

Additive changes (a new optional payload field, a new `job_type` older firmware
can ignore) do **not** require a version bump.

Bump `SyncProtocolVersion` for anything that would make old firmware misbehave:
renamed or removed fields, changed semantics of an existing `job_type`, or a
different envelope shape. Old firmware keeps declaring the old version, and the
server is expected to keep serving it until the fleet is upgraded.
