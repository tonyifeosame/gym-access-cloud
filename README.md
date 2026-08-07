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
psql -U at_admin -d access_terminal -f migrations/001_init_schema.sql
psql -U at_admin -d access_terminal -f migrations/002_core_schema.sql
```

Migrations are versioned and applied in filename order. They are additive:
later migrations never edit or replace earlier ones.

4. **Run the server**:
```bash
go run main.go
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

## Deployment

For production deployment:
1. Use environment variables for all configuration
2. Enable SSL/TLS
3. Use a reverse proxy (nginx, Caddy)
4. Set up database backups
5. Monitor logs and metrics

## Security Notes

- Change default API keys in production
- Use strong database passwords
- Enable PostgreSQL SSL
- Implement rate limiting
- Use HTTPS in production
