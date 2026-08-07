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

The effective configuration is logged at startup, so an operator can see what the
process will actually do without reading the environment back.

## Shutdown

On `SIGTERM`/`SIGINT` the process drains in dependency order:

1. stop accepting new requests and drain in-flight ones (15s),
2. stop background tasks,
3. close the database.

Draining first matters because a device may be mid-acknowledgement. Dropping that
connection would leave it retrying a job it had already applied — harmless,
since applies are idempotent, but pointless work.
