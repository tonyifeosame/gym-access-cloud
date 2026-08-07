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

All endpoints require `X-API-Key` header for authentication.

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

### Health

- `GET /health` - Health check (no auth required)

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
