# Operations

Monitoring endpoints, background maintenance, and the configuration that drives
them.

## Health endpoints

| Endpoint | Purpose | Auth |
|---|---|---|
| `GET /health` | Original health check. Unchanged. | none |
| `GET /health/live` | Liveness — is the process able to serve? | none |
| `GET /health/ready` | Readiness — should traffic be routed here? | none |
| `GET /health/maintenance` | Background task history | none |
| `GET /metrics` | Prometheus exposition format | optional token |

### Liveness vs readiness

These are separate on purpose, and wiring them to the same probe defeats the
point.

**Liveness** touches no dependency. If it responds, the process is running and
should not be restarted.

**Readiness** checks the database and returns `503` when it is unreachable.

A database outage must fail readiness but **not** liveness. If a liveness probe
checked the database, an orchestrator would restart every API instance the
moment Postgres blinked — turning a recoverable dependency failure into a full
outage, and adding a thundering herd of reconnects on top of it.

```
GET /health/ready   ->  200 {"status":"ready","checks":{"database":{"status":"up"}}}
                    ->  503 {"status":"not_ready","checks":{"database":{"status":"down",...}}}
```

## Metrics

Prometheus text format, hand-written — no client library, so no extra
dependency for a handful of gauges.

| Metric | Meaning |
|---|---|
| `access_terminal_up` | 1, or 0 if the scrape itself failed |
| `access_terminal_uptime_seconds` | Seconds since process start |
| `access_terminal_devices{status="…"}` | Devices per state, all six always present |
| `access_terminal_devices_total` | Non-deleted devices |
| `access_terminal_devices_firmware_outdated` | Devices behind their channel's current build |
| `access_terminal_sync_jobs{status="…"}` | Jobs per status, including `PENDING` and `FAILED` |
| `access_terminal_sync_jobs_oldest_pending_age_seconds` | Age of the oldest undelivered job |
| `access_terminal_sync_jobs_retrying` | Pending jobs that have already failed once |
| `access_terminal_people_total` / `_sites_total` / `_companies_total` | Record counts |
| `access_terminal_maintenance_runs_total{task="…"}` | Task executions |
| `access_terminal_maintenance_failures_total{task="…"}` | Task failures |
| `access_terminal_maintenance_last_run_duration_seconds{task="…"}` | Last run duration |

Gauges are computed at scrape time from the database rather than kept as
in-process counters. Counters would reset on restart, and with more than one
instance each would report only what it happened to observe. Every instance now
reports the same numbers.

Every device state and job status is emitted even at zero — a series that
disappears when it hits zero is indistinguishable from a failed scrape.

On scrape failure the endpoint returns `503` with `access_terminal_up 0` rather
than an error page, so the failure is alertable instead of appearing as a gap.

### The alert worth having

`access_terminal_sync_jobs{status="PENDING"}` alone is a weak signal — a large
queue that is draining is healthy. The one that matters is:

```
access_terminal_sync_jobs_oldest_pending_age_seconds
```

Rising steadily means a terminal has stopped acknowledging and is drifting out of
sync — the failure mode that lets a revoked credential keep opening a door.

Pair it with `access_terminal_devices{status="OFFLINE"}` and
`access_terminal_maintenance_failures_total`.

### Protecting the endpoint

Set `METRICS_TOKEN` to require a credential. Accepted as either:

```
Authorization: Bearer <token>
X-Metrics-Token: <token>
```

Unset, the endpoint is open — the usual arrangement when it is only reachable
inside a cluster.

## Background maintenance

| Task | Default interval | What it does |
|---|---|---|
| `offline_sweep` | 60s | Marks devices `OFFLINE` after `DEVICE_OFFLINE_AFTER_SECONDS` without a heartbeat |
| `sync_job_prune` | 6h | Deletes `COMPLETED`/`CANCELLED` jobs older than the retention window |

**The offline sweep is load-bearing.** Nothing else moves a device out of
`ONLINE`. Without it, `status` only ever reflects what a device last claimed, so
a terminal that loses power stays `ONLINE` forever and the dashboard lies.

Pruning only ever removes `COMPLETED` and `CANCELLED` rows. `PENDING` work is
still owed to a device and `FAILED` rows are a dead-letter queue worth
investigating, so neither is deleted regardless of age.

Each task runs once at startup rather than waiting a full interval — after a
crash, devices may already have been unreachable for some time.

### Multi-instance behaviour

Every instance runs these tasks. That is safe because each is an idempotent
set-based `UPDATE`/`DELETE`: two instances sweeping concurrently converge on the
same result. It is wasteful, not wrong. **Any future task that is not idempotent
needs a database advisory lock first.**

This scheduler is deliberately not a durable queue. Its jobs are cheap and safe
to skip or repeat, so a missed tick costs a slightly stale gauge. Anything that
must not be missed belongs in `sync_jobs`.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `MAINTENANCE_ENABLED` | `true` | Master switch for background tasks |
| `DEVICE_OFFLINE_AFTER_SECONDS` | `300` | Heartbeat silence before a device is `OFFLINE` |
| `OFFLINE_SWEEP_INTERVAL_SECONDS` | `60` | How often the sweep runs |
| `SYNC_JOB_PRUNE_INTERVAL_SECONDS` | `21600` | How often pruning runs |
| `SYNC_JOB_RETENTION_DAYS` | `90` | Job history retained; `0` disables pruning |
| `MAINTENANCE_SHUTDOWN_TIMEOUT_SECONDS` | `10` | Grace period for tasks to stop |
| `SYNC_COMPACTION_THRESHOLD` | `500` | Backlog at which a device's queue is snapshotted |
| `METRICS_TOKEN` | unset | If set, required to scrape `/metrics` |
| `API_ENVIRONMENT` | `live` | Which integration credentials this deployment mints and accepts (`live` or `test`) |
| `API_CREDENTIAL_FLUSH_INTERVAL_SECONDS` | `60` | How often buffered credential telemetry is written |
| `API_HOUSEKEEPING_INTERVAL_SECONDS` | `600` | How often rate buckets, idempotency records and usage rows are swept |
| `RATE_BUCKET_IDLE_SECONDS` | `600` | How long an idle per-address rate bucket is kept |
| `API_USAGE_RETENTION_DAYS` | `90` | How long the per-day usage rollup is kept |
| `CURSOR_SIGNING_KEY` | *(ephemeral)* | Signs public API pagination cursors; ≥ 32 bytes. Unset: a per-process key, logged at startup, and cursors do not survive a restart or span instances — development and test only. **Production deployments must set a persistent `CURSOR_SIGNING_KEY` of at least 32 bytes.** |

The effective configuration is logged at startup, so an operator can see what the
process will actually do without reading the environment back.

### Integration credentials and shared rate limiting

`API_ENVIRONMENT` decides which integration credentials (`atp_live_…` /
`atp_test_…`) this deployment will mint and accept. It defaults to `live`, which
is the safe direction — a deployment that configures nothing never accepts a
staging key. **An unrecognised value fails startup**: there is no sensible
fallback, so a typo is a refusal to boot rather than a silent change of
behaviour.

The **login, claim, platform-login and adopt** limiters keep their buckets in
PostgreSQL (`api_rate_buckets`), so the allowance holds across instances. That
closes SEC-09 for the credential endpoints. Nothing needs configuring for the
store itself; the existing `*_RATE_LIMIT_PER_MINUTE` variables still set the
rates.

**The announce limiter deliberately stays in process.** Resolving a terminal's
identity inside a limiter is the work the limiter exists to avoid, and a
twenty-five terminal site polling every five seconds is roughly three hundred
limiter writes a minute before the handler does anything. The cost of the
exception is that with two instances the *announce* allowance doubles — on an
endpoint that grants nothing, whose pairing code is protected by the adopt
limiter, which is shared. If the announce path is ever moved, it should go onto
a store that is not the request database.

Two maintenance tasks come with this:

- `api_credential_flush` drains the in-memory buffer holding `last_used_at` and
  per-class usage counts. The request path writes nothing, so a process stopped
  between flushes loses what it was holding — which is why the interval is
  short.
- `api_housekeeping` prunes idle **address** buckets, expired idempotency
  records and old usage rows. Credential and company buckets are never swept:
  there is one per credential and one per company, so they are bounded by the
  tenant rather than by traffic.

**PRE-PRODUCTION BLOCKER — the public API v1 tree is not rate limited.** The
four read routes under `/api/public/v1` (API_SPEC.md section 18) authenticate
an integration credential and open a tenant-scoped transaction per request, and
nothing bounds how often. The shared store already supports a per-credential
class and `rate_limit_exceeded` is registered; what is missing is a decided
allowance, which this document deliberately does not invent. Do not issue a
customer an integration credential against a production deployment until it
is in place.

**PRE-PRODUCTION BLOCKER — the members cursor exposes internal ids.**
`models/cursor.go` signs the cursor (HMAC) but does not encrypt it: the
base64 payload carries `c` (the internal company id) and `i` (the internal
id of the last row served), which API_SPEC.md section 18 says are never
exposed. Nothing can be done with them — every query is filtered on the
credential's company and a tampered cursor fails its signature — but the
contract is violated as written. Minimum fix, scoped to `Encode`/`Decode`
only: AEAD-encrypt the payload under a key derived from `CURSOR_SIGNING_KEY`
(AES-GCM or XChaCha20-Poly1305), so the wire form is opaque and the keyset
position stays `(created_at, id)`; no query, handler or test outside
`models/cursor_test.go` changes. The alternative — dropping `c` and keying the
tiebreak on `public_id` — touches the ordering SQL and is not the minimum.
Do this with the rate-limit work, before customer exposure.

## Shutdown

On `SIGTERM`/`SIGINT` the process drains in dependency order:

1. stop accepting new requests and drain in-flight ones (15s),
2. stop background tasks,
3. close the database.

Draining first matters because a device may be mid-acknowledgement. Dropping that
connection would leave it retrying a job it had already applied — harmless,
since applies are idempotent, but pointless work.
