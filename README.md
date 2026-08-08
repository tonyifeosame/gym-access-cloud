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

3. **Run migrations**, in filename order:
```bash
for f in migrations/*.sql; do
  psql -U at_admin -d access_terminal -v ON_ERROR_STOP=1 -f "$f" || break
done
```

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

- `GET /api/v1/access/:member_id` - Check member access
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
once, at registration. `X-API-Key` plus `X-Device-Serial` is still accepted as a
deprecated fallback while firmware migrates. See
[docs/sync-protocol.md](docs/sync-protocol.md) for the full contract.

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
with their own credential and keep working. It does invalidate any firmware
still using the deprecated `X-API-Key` + `X-Device-Serial` path, and any
dashboard or tooling configured with the old key.

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
curl -X GET http://localhost:8080/api/v1/access/MEM001 \
  -H "X-API-Key: main-site-api-key-123"
```

Response:
```json
{
  "granted": true,
  "message": "Access Granted",
  "status": "ACTIVE"
}
```

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
go test ./...
```

The suite is mostly **integration tests against a real PostgreSQL instance**.
The behaviour worth protecting — tenant filters, the partial unique indexes, the
acknowledgement constraint, `SKIP LOCKED` delivery — lives in SQL, and a mocked
database layer would only assert that the Go code calls queries, not that the
queries are right.

The tests create and drop their own database (`access_terminal_test`, override
with `TEST_DB_NAME`) from `migrations/` on every run, so they never touch a real
one and double as the check that a fresh database can be built from zero.
Connection settings come from the same `DB_*` variables the server uses:

```bash
DB_HOST=localhost DB_USER=postgres DB_PASSWORD=secret go test ./...
```

Set `TEST_DB_SKIP=1` to skip them where no PostgreSQL is available.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `DB_HOST` / `DB_PORT` | `localhost` / `5432` | Database address |
| `DB_USER` / `DB_PASSWORD` / `DB_NAME` | `at_admin` / — / `access_terminal` | Credentials |
| `DB_SSLMODE` | `disable` | Set to `require` or stronger when the database is across a network |
| `DB_MAX_OPEN_CONNS` | `25` | Pool ceiling. Unbounded lets a polling fleet exhaust PostgreSQL's connection slots |
| `DB_MAX_IDLE_CONNS` | `5` | Idle connections retained |
| `DB_CONN_MAX_LIFETIME_SECONDS` | `1800` | Recycle connections so a failover does not leave stale ones |
| `DB_CONN_MAX_IDLE_SECONDS` | `300` | Idle connection timeout |
| `SERVER_PORT` | `8080` | Listen port |
| `GIN_MODE` | `release` | `debug` prints the routing table at startup |
| `CORS_ALLOWED_ORIGINS` | unset | Comma-separated origin allowlist. Unset means any origin, never with credentials |
| `METRICS_TOKEN` | unset | Requires a bearer token on `/metrics`. Unset leaves it open |
| `SYNC_COMPACTION_THRESHOLD` | `500` | Backlog at which a device's queue is replaced by a snapshot |
| `MAINTENANCE_ENABLED` | `true` | Background sweeps |
| `DEVICE_OFFLINE_AFTER_SECONDS` | `300` | Silence before a device is marked `OFFLINE` |
| `SYNC_JOB_RETENTION_DAYS` | `90` | Delivered job retention; `0` disables pruning |

## Deployment

For production deployment:
1. Use environment variables for all configuration
2. Terminate TLS in front of the API (it serves plain HTTP)
3. Set `DB_SSLMODE=require` unless the database is on the same host
4. Use a reverse proxy (nginx, Caddy)
5. Set up database backups
6. Monitor logs and metrics

## Security Notes

- **Rotate any seeded API keys** — see [Credentials](#credentials)
- Use strong database passwords
- Enable PostgreSQL SSL (`DB_SSLMODE=require`)
- Keep `/metrics` off the public network, or set `METRICS_TOKEN`: it reports
  fleet-wide counts across all tenants
- Implement rate limiting — there is none, including on authentication
- Use HTTPS in production
- Set `CORS_ALLOWED_ORIGINS` before a browser dashboard sends credentials

Known gaps a deployment must design around are listed under **Known limitations**
in [API_SPEC.md](API_SPEC.md).
