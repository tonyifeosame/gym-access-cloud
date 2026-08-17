# Access Terminal Cloud API

Go-based REST API for the Access Terminal system. This is the cloud backend that serves as the source of truth for member data, enrollment requests, and access logs.

## Architecture

```
Admin Portal → Go API → PostgreSQL → Terminal (C++) → Hardware
```

## Features

- **Member Management**: CRUD operations for members
- **Access Control**: Real-time access verification
- **Enrollment System**: Fingerprint enrollment workflow
- **Sync Engine**: Incremental member synchronization
- **Access Logging**: Complete audit trail
- **Multi-Site Support**: Multiple site locations with API key authentication
- **Offline-Ready**: Designed to work with terminal's SQLite cache

## Prerequisites

- Go 1.21 or higher
- PostgreSQL 12 or higher

## Setup

1. **Install dependencies**:
```bash
cd access-terminal-cloud-api
go mod download
```

2. **Configure environment**:
```bash
cp .env.example .env
# Edit .env with your database credentials
```

3. **Run migrations**:
```bash
DB_HOST=localhost MIGRATE_VIA_COMPOSE=0 sh deploy/migrate.sh
```

`deploy/migrate.sh` applies each file once against a `schema_migrations` ledger,
inside a transaction that also records the ledger row. `--status` lists what is
applied and what is pending; `--baseline` records an already-migrated database
without re-executing anything.

**Do not loop `psql` over `migrations/*.sql`.** It works on an empty database
and fails on every later run: 001 indexes `enrollment_requests(member_id)`, a
column 002 replaces with `person_id`, so replaying 001 against a migrated
database dies at line 52 — and 001 has no transaction of its own, so whatever
ran before that point stays. See `deploy/README.md`.

Migrations are versioned and additive: later migrations never edit or replace
earlier ones. Applying all of them to an empty database produces the current
schema, which the integration test suite verifies on every run.

**A database built from `migrations/` alone contains no sites and therefore no
usable API keys.** That is deliberate — see [Credentials](#credentials).

4. **Seed development data** (optional, dev only):
```bash
psql -U at_admin -d access_terminal -v SEED_ALLOW_INSECURE=1 -f seeds/dev_seed.sql
```

This creates the three sites the local stack and the ESP32 firmware expect. The
keys it inserts are committed to this repository and therefore public; the
script refuses to run without the flag.

5. **Run the server**:
```bash
go run .
```

The API will start on port 8080 (configurable via `SERVER_PORT` env var).

## API Endpoints

**[API_SPEC.md](API_SPEC.md) is the authoritative contract** for the ESP32
firmware and the React dashboard — every endpoint with request/response bodies,
error cases, and worked examples. The summary below is a quick index.

Most endpoints require an `X-API-Key` header; device endpoints authenticate with
`X-Device-Key`.

### Members

- `GET /api/v1/members` - Get all members
- `GET /api/v1/members/:id` - Get member by ID
- `POST /api/v1/members` - Create new member
- `PUT /api/v1/members/:id` - Update member
- `DELETE /api/v1/members/:id` - Delete member
- `GET /api/v1/members/changes?since=timestamp` - Get changed members for sync

### Access

- `GET /api/v1/access/:member_id?terminal=SERIAL` - **Deprecated.** Evaluates the
  real authorization decision at a named terminal. The terminal is required —
  see `API_SPEC.md` §4. Use `POST /console/terminals/:serial/evaluate` instead
- `POST /api/v1/access/log` - Log access attempt
- `GET /api/v1/access/logs` - Get access logs
- `GET /api/v1/access/logs/:member_id` - Get member's access logs

### Enrollment

- `POST /api/v1/enrollment/start` - Start enrollment request
- `GET /api/v1/enrollment/pending` - Get pending enrollments
- `POST /api/v1/enrollment/result` - Submit fingerprint template

### Site Settings

- `GET /api/v1/sites/settings` - Current settings and version
- `PUT /api/v1/sites/settings` - Replace settings; queues a SETTINGS job for
  every device at the site

### Devices and Firmware

- `POST /api/v1/devices/register` - Provision a device and issue its credential
  (authenticated with the **site** API key)
- `GET /api/v1/devices` - Fleet inventory; `?outdated=true` filters to devices
  behind the current build for their release channel
- `GET /api/v1/devices/summary` - Device counts by state, plus outdated count
- `POST /api/v1/devices/:serial/resync` - Force a full-sync snapshot for a device
- `GET /api/v1/firmware` - Firmware catalog
- `POST /api/v1/firmware` - Publish a build to the catalog
- `PUT /api/v1/firmware/:id/current` - Make a build the deployment target

Firmware handling is **inventory only** — marking a build current changes what
"outdated" means and pushes nothing. OTA is not implemented.

The following authenticate as the device itself, with `X-Device-Key`:

- `POST /api/v1/devices/heartbeat` - Report liveness and firmware
- `GET /api/v1/devices/settings` - Effective settings for this device
- `GET /api/v1/devices/jobs` - Fetch this device's pending sync jobs
- `POST /api/v1/devices/jobs/:id/complete` - Acknowledge a job as applied or failed

Device credentials are stored as SHA-256 hashes and returned in plaintext only
once, at registration or when a claim code is redeemed.

`X-API-Key` plus `X-Device-Serial` is **no longer accepted** (SEC-05): the site
key is the provisioning secret, so a caller holding it could authenticate as any
terminal at the site. A fleet still running firmware that predates per-device
credentials can re-open the path with `LEGACY_DEVICE_AUTH=1`, which the server
announces at startup. See [docs/sync-protocol.md](docs/sync-protocol.md) for the
full contract.

Deletions are delivered **only** as `DELETE` sync jobs. `GET /members/changes`
cannot express a deletion — a removed row simply stops appearing in it — so a
terminal relying on that feed would keep a revoked credential forever.

### Health and Monitoring

No auth required. See [docs/operations.md](docs/operations.md) for details.

- `GET /health` - Health check
- `GET /health/live` - Liveness: is the process able to serve?
- `GET /health/ready` - Readiness: `503` when the database is unreachable
- `GET /health/maintenance` - Background task run history
- `GET /metrics` - Prometheus metrics (set `METRICS_TOKEN` to require a credential)

Liveness deliberately does not check the database: a dependency outage must fail
readiness so traffic is routed away, without triggering a restart loop across
every instance.

Background maintenance marks unresponsive devices `OFFLINE` and prunes delivered
sync jobs. Without the sweep, a terminal that loses power would stay `ONLINE`
forever.

## Authentication

Every request must include:
```
X-API-Key: your-site-api-key
```

API keys are configured in the `sites` table in PostgreSQL.

## Credentials

### Device keys

Generated server-side from 256 bits of `crypto/rand`, returned in plaintext
exactly once at registration, and stored only as a SHA-256 hash. There is no
recovery path: a terminal that loses its key re-registers and is issued a new
one, which invalidates the old one immediately.

### Site keys

Stored in plaintext and compared with a plain SQL equality. This is the weakest
part of the credential story and is **not** fixed yet, because fixing it needs a
way to re-issue a site key and no endpoint offers one — hashing them today would
make a lost key unrecoverable with no way to mint a replacement. Treat a site key
as a provisioning secret: it can enrol terminals.

### Rotating seeded credentials

Databases created before this change were seeded by the migrations with three
sites whose keys are in this repository's git history
(`main-site-api-key-123` and friends). Any such database should be rotated:

```sql
-- Inspect what is present
SELECT id, site_name, api_key FROM sites WHERE deleted_at IS NULL;

-- Replace a known-public key with a fresh one
UPDATE sites
   SET api_key = encode(gen_random_bytes(32), 'hex')
 WHERE api_key IN ('main-site-api-key-123',
                   'main-gym-api-key-123',
                   'lekki-branch-api-key-456',
                   'abuja-branch-api-key-789')
RETURNING id, site_name, api_key;
```

Rotating a site key does **not** invalidate device keys — terminals authenticate
with their own credential and keep working. It invalidates any dashboard or
tooling configured with the old key, and any firmware on the deprecated
`X-API-Key` + `X-Device-Serial` path in a deployment that has re-enabled it.

## Database Schema

### Tables

- **companies**: Tenant that owns sites and people
- **sites**: Site locations and API keys
- **doors**: Physical openings at a site
- **devices**: Terminals/readers installed at a door
- **people**: Person records and fingerprint templates
- **permissions**: Which people may open which door or site, and when
- **access_logs**: Access attempt history
- **sync_jobs**: Queued/completed work pushed out to devices
- **firmware_versions**: Available firmware builds per device type
- **enrollment_requests**: Fingerprint enrollment workflow

```
companies ─┬─ sites ─┬─ doors ── devices ── firmware_versions
           │         └─ sync_jobs
           └─ people ─┬─ permissions
                      ├─ enrollment_requests
                      └─ access_logs
```

Permission schedules use a day-of-week bitmask: `Mon=1, Tue=2, Wed=4, Thu=8,
Fri=16, Sat=32, Sun=64`, so `127` means every day.

### Conventions

- `id` is an internal `BIGSERIAL` and is never used as a public identifier.
- `public_id` is a UUID and is the stable identifier for API/URL use.
- Every table carries `created_at` and `updated_at` (trigger-maintained).
- Entity tables carry `deleted_at` for soft deletes; all reads filter
  `deleted_at IS NULL`, and unique keys are partial indexes scoped the same way
  so a deleted name/serial/API key becomes reusable.
- `access_logs` and `sync_jobs` intentionally have no `deleted_at`: an audit
  trail must not be erasable, and jobs have their own terminal states.
- Every query is scoped by `company_id`, resolved from the API key's site.

See `migrations/001_init_schema.sql` and `migrations/002_core_schema.sql` for
the complete schema.

## Example Requests

### Check Access
```bash
curl -X GET "http://localhost:8080/api/v1/access/MEM001?terminal=AT-0001" \
  -H "X-API-Key: main-site-api-key-123"
```

Response:
```json
{
  "granted": true,
  "message": "Access Granted",
  "status": "ALLOWED",
  "reason": "ALLOWED",
  "terminal": "AT-0001",
  "decided_by": "authorization_engine",
  "deprecated": true
}
```

The terminal is **required**. Permissions are scoped to companies, sites and
terminals, schedules run in the site's timezone, and the terminal's application
mode decides which capability the question is about — so an answer without a
door would not mean anything. Omitting it is a `400`.

### Get Member Changes (for sync)
```bash
curl -X GET "http://localhost:8080/api/v1/members/changes?since=2024-01-01T00:00:00Z" \
  -H "X-API-Key: main-site-api-key-123"
```

### Log Access
```bash
curl -X POST http://localhost:8080/api/v1/access/log \
  -H "X-API-Key: main-site-api-key-123" \
  -H "Content-Type: application/json" \
  -d '{
    "member_id": "MEM001",
    "granted": true,
    "source": "fingerprint",
    "site_name": "Main Site",
    "message": "Access granted via fingerprint"
  }'
```

## Development

### Run in debug mode:
```bash
GIN_MODE=debug go run main.go
```

### Run tests:
```bash
go test -count=1 -timeout 30m ./...
```

`-timeout` is not decoration. Every test builds a database from `migrations/`
and talks to a real PostgreSQL, and hundreds of deliberately slow bcrypt
comparisons are part of what is being tested — the run had grown to within a
minute of `go test`'s 10-minute default, so a slower machine reports a **panic**
rather than a result. A timeout is meant to catch a deadlock, not a busy laptop.

The suite is mostly **integration tests against a real PostgreSQL instance**.
The behaviour worth protecting — tenant filters, the partial unique indexes, the
acknowledgement constraint, `SKIP LOCKED` delivery — lives in SQL, and a mocked
database layer would only assert that the Go code calls queries, not that the
queries are right.

The tests create and drop their own database (`access_terminal_test`, override
with `TEST_DB_NAME`) from `migrations/` on every run, so they never touch a real
one and double as the check that a fresh database can be built from zero. The
account therefore needs `CREATEDB`.

Connection settings come from the same `DB_*` variables the server uses, and a
`.env` in the repo root is loaded automatically — the tests read it themselves
rather than relying on `main()`, which a test binary never executes. Anything
set on the command line still wins:

```bash
DB_HOST=localhost DB_USER=postgres DB_PASSWORD=secret go test -count=1 ./...
```

A run that reached a server prints the server it reached before the tests start:

```
integration tests: loaded .env from the repo root
integration tests: connected to PostgreSQL 18.4 ... as "postgres", database "access_terminal_test"
```

**`-count=1` is not optional here.** `go test` caches a passing package result
and replays it whenever the cache key matches. That key covers the test binary,
the command line, and the files and environment variables the tests consult —
it does not, and cannot, cover whether PostgreSQL is reachable. Worse, the `DB_*`
variables are not part of it either: the go command re-reads recorded variables
from its own environment, and the harness resolves them before `m.Run()` starts.

So a bare `go test ./...` can report `ok (cached)` for a run that never opened a
connection — a pass banked while the database was up, replayed after it went
away. That is not hypothetical; it is how this suite once reported green against
a server refusing every connection. Always run:

```bash
go test -count=1 -timeout 30m ./...

# and if a pass may already be banked against a database that has since gone:
go clean -testcache && go test -count=1 -timeout 30m ./...
```

The `Makefile` wraps these as `make test` and `make test-fresh` where `make` is
available; the commands above are the source of truth.

If PostgreSQL is unavailable the suite **fails** and names the target it tried
(`postgres://user@host:port/db`); it does not fall back to a mock and it does not
skip. `TEST_DB_SKIP=1` (`make test-skip-db`) skips it deliberately and says
loudly that the tenancy and sync SQL went uncovered.

After any real run, `.integration-run` records which server was reached and
when — worth checking in CI, since a passing `go test` prints nothing.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DATABASE_URL` | unset | Full connection URI. Supersedes every `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME`/`DB_SSLMODE` below when set — this is the shape managed providers hand out. TLS is enforced on it: no `sslmode` means `require`, and `disable`/`allow`/`prefer` against a non-loopback host is refused at startup |
| `DB_HOST` / `DB_PORT` | `localhost` / `5432` | Database address |
| `DB_USER` / `DB_PASSWORD` / `DB_NAME` | `at_admin` / — / `access_terminal` | Credentials |
| `DB_SSLMODE` | `disable` | Use `verify-full` (with a CA) when the database is across a network. `require` encrypts but authenticates nothing, and fails outright against the stock postgres image, which has SSL off |
| `DB_MAX_OPEN_CONNS` | `25` | Pool ceiling. Unbounded lets a polling fleet exhaust PostgreSQL's connection slots |
| `DB_MAX_IDLE_CONNS` | `5` | Idle connections retained |
| `DB_CONN_MAX_LIFETIME_SECONDS` | `1800` | Recycle connections so a failover does not leave stale ones |
| `DB_CONN_MAX_IDLE_SECONDS` | `300` | Idle connection timeout |
| `PORT` | unset | Listen port, checked **before** `SERVER_PORT`. Container platforms inject it and route to whatever the process bound there |
| `SERVER_PORT` | `8080` | Listen port when `PORT` is unset |
| `BIND_ADDRESS` | `0.0.0.0` | Interface to bind. `0.0.0.0` is required on a container platform, where the proxy and health check arrive over the container's interface. Set `127.0.0.1` where Nginx terminates TLS on the same host |
| `GIN_MODE` | `release` | `debug` prints the routing table at startup |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Whose `X-Forwarded-For` to believe, as IPs/CIDRs. `none` ignores the header entirely. A malformed value is fatal at startup rather than a silent fall back to trusting everything |
| `CORS_ALLOWED_ORIGINS` | unset | Comma-separated origin allowlist. Unset means any origin, never with credentials |
| `METRICS_TOKEN` | unset | Requires a bearer token on `/metrics`. Unset leaves it open |
| `SYNC_COMPACTION_THRESHOLD` | `500` | Backlog at which a device's queue is replaced by a snapshot |
| `MAINTENANCE_ENABLED` | `true` | Background sweeps |
| `DEVICE_OFFLINE_AFTER_SECONDS` | `300` | Silence before a device is marked `OFFLINE`. Must exceed the terminals' poll interval comfortably or a healthy fleet flaps |
| `OFFLINE_SWEEP_INTERVAL_SECONDS` | `60` | How often that sweep runs |
| `SYNC_JOB_RETENTION_DAYS` | `90` | Delivered job retention; `0` disables pruning |
| `SYNC_JOB_PRUNE_INTERVAL_SECONDS` | `21600` | How often the prune runs |
| `MAINTENANCE_SHUTDOWN_TIMEOUT_SECONDS` | `10` | Grace for an in-flight maintenance task at shutdown |

## Deployment

`deploy/` holds the production stack, the Nginx site, the migration runner and
the runbook. In outline:

1. Use environment variables for all configuration
2. Terminate TLS in front of the API (it serves plain HTTP)
3. Use a reverse proxy (nginx, Caddy)
4. Set `TRUSTED_PROXIES` to match that proxy — the API records the caller's
   address on the device row, so the wrong value makes it either spoofable or
   uniformly wrong
5. Set `DB_SSLMODE=verify-full` once the database is off-host, and give it a CA.
   Not `require`: that encrypts without checking who answered. Leave it
   `disable` while PostgreSQL is on the same host or on a private bridge — the
   stock postgres image has SSL off and generates no certificate, so `require`
   against it simply fails to connect
6. Set up database backups
7. Monitor logs and metrics

See [`deploy/README.md`](deploy/README.md) for the full runbook, including
domain and DNS setup.

### Render (free tier)

The current development/staging target is Render's free tier, described by
[`render.yaml`](render.yaml) and
[`docs/render-free-deployment.md`](docs/render-free-deployment.md). Nothing is
deployed and no DNS record exists; the blueprint sets `autoDeploy: false`.

The free **web service** is suitable for current development and testing. The
free **PostgreSQL is disposable** — Render deletes it 30 days after creation, so
everything in it must be reproducible from `migrations/` and `seeds/`. A
**persistent production database and service means paid resources**, which are
out of scope for now.

It differs from the VPS deployment in ways that matter: the service sleeps after
15 minutes idle (so the first request after that times out against the
terminals' 10-second budget), the free database is deleted 30 days after
creation, migrations run from a workstation against the external URL because a
free service has no shell, and Render's edge replaces Nginx — including losing
its rate limits. All of it is in that document. `deploy/` is unchanged and
remains the production path.

## Security Notes

- **Rotate any seeded API keys** — see [Credentials](#credentials)
- Use strong database passwords
- Enable PostgreSQL SSL with `DB_SSLMODE=verify-full` and a CA once the database
  is reachable over a network — `require` encrypts but authenticates nothing
- Set `TRUSTED_PROXIES` to the proxy in front of the API, or `none` if there
  isn't one; gin's default trusts every `X-Forwarded-For` it is given
- Keep `/metrics` off the public network, or set `METRICS_TOKEN`: it reports
  fleet-wide counts across all tenants
- Implement rate limiting — there is none, including on authentication
- Use HTTPS in production
- Set `CORS_ALLOWED_ORIGINS` before a browser dashboard sends credentials

Known gaps a deployment must design around are listed under **Known limitations**
in [API_SPEC.md](API_SPEC.md).
