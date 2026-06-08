# Gym Access Cloud API

Go-based REST API for the Gym Access System. This is the cloud backend that serves as the source of truth for member data, enrollment requests, and access logs.

## Architecture

```
Admin Portal → Go API → PostgreSQL → Terminal (C++) → Hardware
```

## Features

- **Member Management**: CRUD operations for gym members
- **Access Control**: Real-time access verification
- **Enrollment System**: Fingerprint enrollment workflow
- **Sync Engine**: Incremental member synchronization
- **Access Logging**: Complete audit trail
- **Multi-Site Support**: Multiple gym locations with API key authentication
- **Offline-Ready**: Designed to work with terminal's SQLite cache

## Prerequisites

- Go 1.21 or higher
- PostgreSQL 12 or higher

## Setup

1. **Install dependencies**:
```bash
cd cloud-api
go mod download
```

2. **Configure environment**:
```bash
cp .env.example .env
# Edit .env with your database credentials
```

3. **Run migrations**:
```bash
psql -U gym_admin -d gym_access -f migrations/001_init_schema.sql
```

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

- **sites**: Gym locations and API keys
- **members**: Member information and fingerprint templates
- **enrollment_requests**: Fingerprint enrollment workflow
- **access_logs**: Access attempt history

See `migrations/001_init_schema.sql` for complete schema.

## Example Requests

### Check Access
```bash
curl -X GET http://localhost:8080/api/v1/access/MEM001 \
  -H "X-API-Key: main-gym-api-key-123"
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
  -H "X-API-Key: main-gym-api-key-123"
```

### Log Access
```bash
curl -X POST http://localhost:8080/api/v1/access/log \
  -H "X-API-Key: main-gym-api-key-123" \
  -H "Content-Type: application/json" \
  -d '{
    "member_id": "MEM001",
    "granted": true,
    "source": "fingerprint",
    "site_name": "Main Gym",
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
