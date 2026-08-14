# Production deployment

Everything here is prepared and **nothing has been deployed**. The hostname is
settled — `api.accesslink.store`, written into the nginx site and every command
below — but no DNS record has been created and no certificate has been issued,
so the two things that depend on those are still outstanding. Start at
[Domain configuration](#domain-configuration) — nothing else can proceed until
the name resolves.

Files:

| File | What it is |
|---|---|
| `Dockerfile` | Production image — non-root, pinned tags, healthcheck, stamped with commit |
| `docker-compose.prod.yml` | API + PostgreSQL; database not published, API on loopback only |
| `migrate.sh` | Applies migrations once each, against a `schema_migrations` ledger |
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

## Domain configuration

`api.accesslink.store` is written out in full in the Nginx site, in the
certificate paths, and in the terminal's `set url`. This is what has to exist
before any of it works.

### 1. Create the DNS record

At the registrar holding `accesslink.store`, add an address record for the `api`
label pointing at the host's **public** address:

| Type | Name | Value | TTL |
|---|---|---|---|
| `A` | `api` | *public IPv4 of the host* | 300 |
| `AAAA` | `api` | *public IPv6, if the host has one* | 300 |

Keep the TTL low (300s) until the deployment has settled. A 24-hour TTL on a
record that turns out to be wrong is a day of waiting, and the fleet is pinned
to this name.

Add the `AAAA` record **only if the host actually serves on IPv6**. The Nginx
site listens on `[::]:80` and `[::]:443`, so it will — but if the host has no
working IPv6 route, a published `AAAA` makes clients that prefer v6 try it first
and stall. Publish one or fix routing; do not publish one and hope.

### 2. Verify it resolves, before requesting a certificate

Certbot's webroot challenge proves control by being *reached over the name*, so
a record that has not propagated produces a confusing failure that looks like a
certbot problem.

```bash
dig +short A    api.accesslink.store
dig +short AAAA api.accesslink.store

# Ask an authoritative server directly -- this skips every resolver cache
# between here and the registrar, which is what makes propagation confusing.
dig +short NS accesslink.store
dig +short api.accesslink.store @$(dig +short NS accesslink.store | head -1)
```

Both must return the host's own addresses. Then confirm the host answers on that
name over plain HTTP, which is the exact path the challenge takes:

```bash
curl -sI http://api.accesslink.store/.well-known/acme-challenge/probe
```

A 404 from Nginx is success — it means the name resolved here and the location
block is serving. A timeout means DNS or the firewall; a connection refused
means Nginx is not listening.

### 3. Then continue to the certificate

Only once the above returns cleanly, go to **First deploy → step 5**.

### Using a different domain

The hostname appears in several places and they must move together. There is
deliberately no variable for it: an Nginx `server_name` cannot be templated, and
a half-substituted config that still starts is worse than one that does not.

```bash
OLD=api.accesslink.store
NEW=api.otherdomain.tld

sed -i "s/${OLD//./\\.}/$NEW/g" deploy/nginx/access-terminal-api.conf
sed -i "s/${OLD//./\\.}/$NEW/g" deploy/README.md

# Confirm nothing was missed -- expect no output.
grep -rn "$OLD" deploy/ || echo "clean"
```

Then re-issue the certificate for the new name, and re-flash the terminals with
`set url https://$NEW`. The certificate paths in the Nginx site are
`/etc/letsencrypt/live/<name>/`, which certbot creates from the name you request,
so those are covered by the same substitution.

Changing the domain after terminals are deployed means visiting each one — a
terminal holds its URL in NVS and there is no remote path to change it.

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
sh deploy/migrate.sh --dry-run     # what would run
sh deploy/migrate.sh               # run it
```

`migrate.sh` keeps a `schema_migrations` ledger and applies each file once,
inside a single transaction that also records the ledger row — so a migration
and the record of it either both land or neither does.

> **Do not go back to looping `psql` over `migrations/*.sql`.** These migrations
> are not merely non-idempotent — 001 is *incompatible with the schema the later
> ones produce*. Replayed against a fully migrated database it fails at
> `001_init_schema.sql:52`:
>
> ```
> ERROR: column "member_id" does not exist
> ```
>
> because that line indexes `enrollment_requests(member_id)`, and 002 replaced
> that column with `person_id`. The `IF NOT EXISTS` on the index does not help:
> the object it guards is gone and the column it names no longer exists. Bare
> `CREATE TRIGGER` and `ALTER TABLE ... ADD CONSTRAINT` further on would fail
> too, if execution ever reached them.
>
> And 001 opens no transaction of its own — 002–007 do, so they roll back
> cleanly, but whatever 001 executes before the failing line stays. A deploy
> that half-applies schema and then reports an error is the worst outcome
> available here.
>
> This ran correctly on a fresh database, which is why it survived review: the
> loop only misbehaves on the *second* deploy.

Re-running is safe and is the normal upgrade step — it applies whatever is new:

```bash
sh deploy/migrate.sh --status      # applied vs pending, changes nothing
```

If the schema was already applied by hand before this script existed, record it
without re-executing anything, then confirm it reads as clean:

```bash
sh deploy/migrate.sh --baseline
sh deploy/migrate.sh --status      # expect: 0 pending
```

#### Migration 011 on an installation with live sites

`011_site_credentials.sql` replaces the plaintext `sites.api_key` with a SHA-256
hash and **drops the plaintext column**.

**No terminal or tool has to change anything.** The wire contract is identical —
the same `X-API-Key: <same string>` — and the migration computes each existing
key's hash from the plaintext it is about to remove, so every already-provisioned
site keeps authenticating with the key it already holds. It refuses to run if any
live site would be left without a hash, rather than silently locking out a fleet.

What is lost, deliberately: **the key can no longer be read back out of the
database.** `SELECT api_key FROM sites` was a working recovery path and is now
gone. Anyone relying on it should rotate instead. Take a backup before applying,
as with any migration that drops a column.

#### Migration 010 on a database that already holds data

`010_timestamptz.sql` converts every timestamp column from a wall-clock reading
to a true instant. To do that it has to know **which clock took the existing
readings**, and it defaults to the time zone of the connection applying it.

Run through `migrate.sh` as documented above, that default is correct: psql pins
no time zone, so it reports the database server's own — which is the clock
`CURRENT_TIMESTAMP` was reading. Nothing extra is needed, and on an empty
database the choice cannot affect any value at all.

It is wrong in one case: **the database has moved or been reconfigured since the
data was written.** Name the zone the old rows were written in:

```bash
psql "$DATABASE_URL" -c "SET accesslink.legacy_timezone = 'Africa/Lagos';" \
                     -f migrations/010_timestamptz.sql
```

The migration raises a `WARNING` naming the zone it used whenever it converts a
populated database without an explicit override — check the deploy output for it
rather than discovering the assumption later in an access log that reads an hour
out. Getting this wrong shifts historical timestamps and is not automatically
reversible, so verify before the ledger records it as applied.

Note also that each `ALTER ... TYPE` rewrites its table under an `ACCESS
EXCLUSIVE` lock and rebuilds the indexes over it. On a large `access_logs` this
is not instant — apply it in a maintenance window.

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

**Through the console API, not with psql.** Since migration 011 there is no
plaintext `api_key` column to insert into — the database stores a SHA-256 hash —
so the `INSERT` this step used to describe no longer works. The API generates the
key, stores its hash, and returns the key once.

Sign in as the operator the bootstrap created (see step 4), then:

```bash
# 1. Sign in. Keep the cookie jar and the CSRF token from the response body.
curl -sc jar.txt -X POST https://api.example.com/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@your-company.com","password":"…"}'

# 2. Create the site. Requires ADMIN or OWNER.
curl -sb jar.txt -X POST https://api.example.com/api/v1/console/sites \
  -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" \
  -d '{"name":"Main Site","timezone":"Africa/Lagos"}'
```

```json
{
  "site": {"id":"…","name":"Main Site","timezone":"Africa/Lagos","…":"…"},
  "credential": {"api_key":"ats_…","api_key_prefix":"ats_…","shown_once":true}
}
```

**Store `api_key` in a password manager now.** It is the provisioning secret:
whoever holds it can enrol terminals, read the roster and pull access logs. It is
stored only as a hash, so this response is the last time it exists anywhere the
server can produce. There is no endpoint that returns it again.

Lost it, or need to revoke it? Rotate:

```bash
curl -sb jar.txt -X POST \
  https://api.example.com/api/v1/console/sites/<site_id>/api-key \
  -H "X-CSRF-Token: $CSRF"
```

The old key stops working immediately — there is no overlap window. The response
reports `legacy_terminals`: how many terminals at that site have never been
issued a device credential of their own and therefore still depend on the site
key. **Those need re-provisioning; terminals holding their own `X-Device-Key` are
unaffected.**

#### Retiring a site

`DELETE /api/v1/console/sites/{site_id}` retires a site **and soft-deletes every
terminal at it**, reporting the count. Every one of those terminals stops opening
a door immediately. If you only mean to suspend a location, use
`PUT /api/v1/console/sites/{site_id}` with `{"active": false}` instead — that is
reversible and destroys nothing.

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

# Which build is actually running. `"version":"dev"` here means the binary was
# built without the ldflags stamp -- the deployed commit is then unknowable, so
# treat it as a failed deploy rather than a cosmetic issue.
curl -s https://api.accesslink.store/health

# Schema state. Expect "0 pending"; anything else means the API is running
# against a database it does not match.
sh deploy/migrate.sh --status

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
mkdir -p deploy/backups
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  exec -T postgres pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc \
  > deploy/backups/at-$(date +%F-%H%M).dump
```

`deploy/backups/` and `*.dump` are gitignored — that path, specifically. Writing
to a `backups/` at the repository root instead puts a file holding the entire
member roster and every device credential hash into `git status` as untracked,
one `git add -A` away from being committed.

Put it on a timer, keep it off this host, and restore one before you need to.

---

## Moving the database off-host

`DB_SSLMODE=disable` is correct only while PostgreSQL is on this host or on the
private bridge the compose stack creates. The moment it is reachable over a
network the setting has to change — and the one to change it to is
**`verify-full`**, not `require`.

`require` encrypts but does not check who answered, so it defeats a passive
listener and not an active one; against a server presenting any certificate at
all it connects happily. `verify-full` checks the chain and the hostname.

```bash
# on the API host
sudo install -D -m 0644 ca.crt /etc/access-terminal/db-ca.crt
```

```ini
DB_HOST=db.internal.example
DB_SSLMODE=verify-full
PGSSLROOTCERT=/etc/access-terminal/db-ca.crt
```

`lib/pq` reads `PGSSLROOTCERT` from the environment, so it needs no code change —
but it is read at connection time, which means the file must be readable by the
service user and mounted into the container if the API runs in one.

Confirm the connection is actually verified rather than merely encrypted:

```bash
docker compose --env-file deploy/.env.production -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -U "$DB_USER" -d "$DB_NAME" \
  -c "SELECT ssl, version, cipher FROM pg_stat_ssl
      JOIN pg_stat_activity USING (pid) WHERE usename = current_user;"
```

---

## Known gaps this deployment does not close

Carried from the device-registration review; none is introduced here, and each
is a deliberate not-yet rather than an oversight.

| Gap | Effect in production |
|---|---|
| No revoke/rotate endpoint | Revoking a stolen terminal needs `psql`. Disabling it works and registration can no longer undo that, but there is no API for it. |
| Site keys stored in plaintext, compared with `=` | The provisioning secret is readable by anyone with database access. |
| ~~`SetTrustedProxies` not called~~ | **Closed.** `TRUSTED_PROXIES` (router.go); Nginx also overwrites the header rather than appending. Set it to match your topology — the value differs between the compose and systemd deployments. |
| No application-level rate limiting | Handled at Nginx here. An attacker reaching port 8080 directly would bypass it, which is why the API binds to loopback only. |
| Access logs are site-key authenticated | A terminal cannot upload door events without the provisioning secret. Blocks the ESP32 sync client; needs a device-authenticated endpoint. |
| Fingerprint templates are cloud-stored | `people.fingerprint_template` is pushed to devices in `PERSON` sync payloads, which contradicts "templates stay device-local". Decide before the fleet grows. |
