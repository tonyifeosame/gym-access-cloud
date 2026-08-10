# Production deployment

Everything here is prepared and **nothing has been deployed**. The hostname is
settled — `api.accesslink.store`, written into the nginx site and every command
below — but no DNS record has been created and no certificate has been issued,
so the two things that depend on those are still outstanding. See below.

Files:

| File | What it is |
|---|---|
| `Dockerfile` | Production image — non-root, pinned tags, healthcheck, stamped with commit |
| `docker-compose.prod.yml` | API + PostgreSQL; database not published, API on loopback only |
| `.env.production.example` | Every environment variable the app reads, with the reasoning |
| `nginx/access-terminal-api.conf` | TLS termination, rate limits, proxy headers |
| `systemd/access-terminal-api.service` | Alternative to Docker for the API process |

---

## Blocked on DNS

1. **The certificate.** Nothing can be issued until `api.accesslink.store`
   resolves to the host — certbot's webroot challenge proves control by being
   reached over the name.
2. **The firmware's pinned root CA.** The terminals refuse every `https://`
   request unless a root is compiled in (`include/root_ca.h`), and there is no
   runtime path to disable that. So the fleet cannot talk to production until an
   image is built with the ISRG roots.

Order matters: DNS → certificate → build firmware with the roots → flash → point
terminals at the URL. A terminal flashed before the certificate exists cannot
connect, and cannot be fixed over the network.

Note that step 2 does **not** wait for step 1. The roots come from the CA, not
from the server (step 7), so the firmware image can be built and tested before
the certificate is issued — only the final connection check has to wait.

---

## First deploy

### 1. Prerequisites

Docker with the compose plugin, Nginx, and certbot on the host. Ports 80 and 443
open; **5432 must not be**.

### 2. Secrets

```bash
cp deploy/.env.production.example deploy/.env.production
chmod 600 deploy/.env.production

openssl rand -hex 32   # -> DB_PASSWORD
openssl rand -hex 32   # -> METRICS_TOKEN
```

Confirm it cannot be committed — this is the check, not the assumption:

```bash
git check-ignore -v deploy/.env.production
```

### 3. Bring up the database, then migrate

Migrations are **not** run by the container. `docker-entrypoint-initdb.d` runs
only on first init of an empty volume, so mounting `migrations/` there silently
does nothing on every later deploy while looking like it worked.

```bash
cd /opt/access-terminal
export VERSION=$(git describe --tags --always --dirty)
export COMMIT=$(git rev-parse --short HEAD)

docker compose --env-file deploy/.env.production \
               -f deploy/docker-compose.prod.yml up -d postgres

set -a; . deploy/.env.production; set +a
for f in migrations/*.sql; do
  echo "applying $f"
  docker compose --env-file deploy/.env.production \
                 -f deploy/docker-compose.prod.yml \
                 exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 < "$f"
done
```

**Do not load `seeds/dev_seed.sql`.** It creates sites with known keys.

A database built from migrations alone has no sites, and therefore no usable
credentials, which is intended — create the real one in step 6.

### 4. Start the API

```bash
docker compose --env-file deploy/.env.production \
               -f deploy/docker-compose.prod.yml up -d --build

curl -s localhost:8080/health/ready
```

### 5. Nginx and the certificate

```bash
sudo cp deploy/nginx/access-terminal-api.conf /etc/nginx/sites-available/
sudo ln -s /etc/nginx/sites-available/access-terminal-api.conf /etc/nginx/sites-enabled/
sudo mkdir -p /var/www/certbot

sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/certbot -d api.accesslink.store
sudo nginx -t && sudo systemctl reload nginx
```

The `limit_req_zone` lines are `http{}`-scope. Included from `sites-enabled/`
they are fine; pasted into a `server{}` block they are not, and `nginx -t` says
so clearly.

Renewal is certbot's timer. Verify it, because a silent renewal failure is a
fleet-wide outage ninety days later:

```bash
sudo certbot renew --dry-run
systemctl list-timers | grep certbot
```

### 6. Create the real site

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" <<'SQL'
INSERT INTO companies (name, slug, active)
VALUES ('Your Company', 'your-company', TRUE)
ON CONFLICT DO NOTHING;

INSERT INTO sites (company_id, site_name, api_key, active, timezone)
VALUES ((SELECT id FROM companies WHERE slug = 'your-company'),
        'Main Site', encode(gen_random_bytes(32), 'hex'), TRUE, 'Africa/Lagos')
RETURNING id, site_name, api_key;
SQL
```

Store that key in a password manager. It is the provisioning secret: whoever
holds it can enrol terminals, read the roster, and pull access logs.

### 7. Supply the root CA to the firmware

**Download the roots from the CA. Do not extract them from the server.**

```bash
curl -O https://letsencrypt.org/certs/isrgrootx1.pem
curl -O https://letsencrypt.org/certs/isrg-root-x2.pem
cat isrgrootx1.pem isrg-root-x2.pem > roots.pem
```

Then wrap `roots.pem` as a C string literal in `include/cloud_root_ca.inc` in the
firmware repo (gitignored) and build with
`-DCLOUD_ROOT_CA_INCLUDE='"cloud_root_ca.inc"'`. A concatenated bundle is fine —
mbedTLS parses the whole chain and matches whichever root anchors the presented
certificate. Both are included because Let's Encrypt issues from X1 (RSA) and X2
(ECDSA), and which one you get is not yours to choose.

> **Do NOT take the last certificate from `openssl s_client -showcerts`.**
> A server does not normally send the root — it sends leaf + intermediate, so
> the last certificate is the **intermediate** (R10/R11/E5/E6). Those are
> comparatively short-lived and are rotated by the CA on its own schedule, so a
> fleet pinned to one stops working on a day nobody chose, and it cannot be
> fixed over the connection that broke. This is a real trap: the command
> appears to work and produces a bundle that validates correctly for months.

`s_client` is still the right tool for *verifying* what the server presents:

```bash
openssl s_client -showcerts -connect api.accesslink.store:443 </dev/null 2>/dev/null \
  | grep -E "^(depth|verify|subject|issuer)"
```

Pin the **root, never the leaf**. A pinned leaf stops the entire fleet on
renewal day, roughly ninety days after deployment, all at once.

### 8. Enrol a terminal

```bash
curl -X POST https://api.accesslink.store/api/v1/devices/register \
  -H "X-API-Key: <site key>" -H 'Content-Type: application/json' \
  -d '{"serial_number":"AT-E05A1B38AA38","device_name":"Front Door"}'
```

The `api_key` comes back **once**. On the terminal:

```
set url https://api.accesslink.store
set key atd_...
```

Verify with `n` on the console: the 8-hex fingerprint it prints is the first
four bytes of `sha256(key)`, which is also the first 8 characters of
`devices.api_key_hash`. Matching means the terminal holds the right credential,
and confirms it without either end printing the key.

---

## Verifying the deployment

```bash
curl -sI https://api.accesslink.store/health | head -1
curl -s https://api.accesslink.store/health/ready

# TLS 1.2 must work -- this is the fleet's path
openssl s_client -connect api.accesslink.store:443 -tls1_2 </dev/null 2>&1 | grep -E "Protocol|Cipher"

# Metrics must refuse an unauthenticated caller
curl -s -o /dev/null -w "%{http_code}\n" https://api.accesslink.store/metrics   # want 401

# The database must NOT be reachable
nc -zv api.accesslink.store 5432   # want refused/filtered
```

---

## Backups

The member roster and every device credential hash live in this database. There
is no way to re-derive a device key, but a lost database means re-registering
every terminal by hand.

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  exec -T postgres pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc \
  > backups/at-$(date +%F-%H%M).dump
```

Put it on a timer, keep it off this host, and restore one before you need to.

---

## Known gaps this deployment does not close

Carried from the device-registration review; none is introduced here, and each
is a deliberate not-yet rather than an oversight.

| Gap | Effect in production |
|---|---|
| No revoke/rotate endpoint | Revoking a stolen terminal needs `psql`. Disabling it works and registration can no longer undo that, but there is no API for it. |
| Site keys stored in plaintext, compared with `=` | The provisioning secret is readable by anyone with database access. |
| `SetTrustedProxies` not called | Gin trusts any `X-Forwarded-For`, so the recorded device IP is spoofable. The Nginx config overwrites the header rather than appending, which contains it at the edge — but the application should set its trusted proxy list. |
| No application-level rate limiting | Handled at Nginx here. An attacker reaching port 8080 directly would bypass it, which is why the API binds to loopback only. |
| Access logs are site-key authenticated | A terminal cannot upload door events without the provisioning secret. Blocks the ESP32 sync client; needs a device-authenticated endpoint. |
| Fingerprint templates are cloud-stored | `people.fingerprint_template` is pushed to devices in `PERSON` sync payloads, which contradicts "templates stay device-local". Decide before the fleet grows. |
