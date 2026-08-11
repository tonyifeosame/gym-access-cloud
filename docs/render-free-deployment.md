# Render free-tier deployment

Staging/development environment for the AccessLink cloud API, on resources
Render provides at no cost. **Nothing here has been deployed, no DNS record has
been created, and no terminal has been flashed.** This document is the procedure
and the list of what it costs us to run on free.

In one line: **the free web service is a fine place to develop and test against,
the free database is scratch space that Render deletes after 30 days, and
anything persistent waits for paid resources.** That split is spelled out in
[What each resource is for](#what-each-resource-is-for) and it governs everything
below.

Production remains undecided between paid Render and the VPS deployment in
[`deploy/`](../deploy/README.md). That material is unchanged and still current —
Docker image, Nginx TLS termination, systemd unit, migration runner. This is the
environment we develop the ESP32 integration against until the system has earned
the spend.

- [What each resource is for](#what-each-resource-is-for)
- [What gets created](#what-gets-created)
- [Free-tier limits that affect this system](#free-tier-limits-that-affect-this-system)
- [Environment variables](#environment-variables)
- [Deployment procedure](#deployment-procedure)
- [Migrations](#migrations)
- [Health checks](#health-checks)
- [Connecting api.accesslink.store](#connecting-apiaccesslinkstore)
- [DNS records to create in Namecheap](#dns-records-to-create-in-namecheap)
- [What the ESP32 needs before it can talk to this](#what-the-esp32-needs-before-it-can-talk-to-this)
- [What could stop this working](#what-could-stop-this-working)

---

## What each resource is for

The two free resources are **not** equally fit for purpose, and treating them as
one tier is the mistake this section exists to prevent.

| | Render Free web service | Render Free PostgreSQL | Paid Render (later) |
|---|---|---|---|
| **Verdict** | **Suitable for current development and testing** | **Disposable development database only** | **The persistent production answer** |
| Lifetime | Indefinite; sleeps when idle | **Deleted 30 days after creation** (+14-day grace) | Persistent |
| Holds state? | No — the API keeps nothing on disk | Yes, and loses it on expiry | Yes, with backups |
| Good for | ESP32 integration work, contract testing, exercising the whole cloud path over real HTTPS | Schema, seed data, throwaway devices and members | Real members, real terminals, real access history |
| Not good for | Anything needing a response within 20 s of an idle period | **Anything we would mind losing** | — |

**The free web service is genuinely fine for what we are doing now.** It runs the
same hardened image as production, over Render's TLS, on a real hostname. Its one
serious limitation is the idle sleep, and the fleet's queues already tolerate a
failed request. Nothing about the free plan makes it the wrong place to develop
and test the ESP32 integration.

**The free database is a different thing and should be treated as scratch
space.** It expires — this is Render's policy, not a fault we can configure
around — so everything in it must be reproducible from `migrations/` and
`seeds/`. Device credentials live in the database, so its deletion also means
re-registering every terminal. Do not let real member data accumulate in it, and
do not build a habit that assumes it persists.

**Paid Render is what makes both persistent**, when we decide the system has
earned the spend: a paid instance type removes the sleep, the 750-hour ceiling,
the single-instance limit and the lack of shell access, and a paid database has
no expiry and supports backups. Moving there is a plan change on the same
`render.yaml`, not a rewrite — the code reads `PORT` and `DATABASE_URL` either
way. The VPS deployment in `deploy/` remains the other option for production; the
choice between them is open and does not have to be made now.

**Out of scope for now:** no paid resources are to be purchased, no service is to
be deployed, no DNS record is to be created, and no terminal is to be flashed.

---

## What gets created

Two resources, both free, both described by [`render.yaml`](../render.yaml):

| Resource | Type | Plan | Notes |
|---|---|---|---|
| `accesslink-api` | Web service, Docker | `free` | Built from `deploy/Dockerfile`. Fit for development and testing |
| `accesslink-db` | Render Postgres | `free` | 1 GB, **expires 30 days after creation**. Disposable development data only |

Both in `oregon`. The region must match or the service cannot reach the database
over the internal URL.

The blueprint sets `autoDeploy: false`. Creating it does not start a deploy loop
on every push; deploys are triggered by hand from the dashboard.

---

## Free-tier limits that affect this system

These are not footnotes. Each one changes how the fleet behaves.

**The service sleeps after 15 minutes with no requests.** The next request wakes
it, and that takes roughly 30–60 seconds. The firmware's transport gives a
request 10 s to connect and 10 s to read
(`kTransportConnectTimeoutMs`/`kTransportReadTimeoutMs` in
`include/cloud_transport.h`), so **the first call after an idle period will time
out** — every time. The queues are what save us: the access-log queue and the
sync backlog both retry, so the events survive and land on a later poll. But a
terminal that has been quiet overnight will fail its first heartbeat, and any
test of "does the door open right now" against a cold service will fail on the
first try and succeed on the second. Do not read that as a bug in the firmware.

**750 instance-hours per month per workspace.** One service running continuously
is ~730 hours, so one always-awake service fits — but sleeping is what actually
keeps us inside it if we ever add a second.

**The database is deleted 30 days after it is created.** Render expires free
Postgres instances; there is a 14-day grace period and then the data is gone.
Everything in this environment is disposable by design: re-create the database,
re-run the migrations, re-seed, re-register the devices. Do not put anything in
it that is not reproducible from `migrations/` and `seeds/`.

**One free database per workspace.** There is no separate staging and test
database on this tier.

**No shell, no one-off jobs, no pre-deploy command.** This is why migrations run
from a workstation against the external URL rather than as part of the deploy —
see [Migrations](#migrations).

**No persistent disk, single instance only.** Neither matters here: the API
keeps no state on disk, and one instance is all the fleet needs.

**Background maintenance stops while the service sleeps.** The in-process
scheduler (offline-device marking, sync-job retention) only runs while the
process is up. On free, expect it to run in bursts around whatever traffic
happens to wake the service, not on the interval it is configured for.

**Cold-start after every deploy.** Nothing is retained between deploys; that is
fine, the process holds no state.

---

## Environment variables

Everything the service needs is in `render.yaml`. Nothing secret is in it.

| Variable | Source | Value |
|---|---|---|
| `DATABASE_URL` | `fromDatabase` | Internal connection string, injected by Render |
| `PORT` | Render | Injected automatically; the app reads it before `SERVER_PORT` |
| `GIN_MODE` | blueprint | `release` |
| `TRUSTED_PROXIES` | blueprint | `none` — see below |
| `METRICS_TOKEN` | `generateValue` | Random, created by Render, readable in the dashboard |
| `DB_MAX_OPEN_CONNS` | blueprint | `10` |
| `DB_MAX_IDLE_CONNS` | blueprint | `2` |
| `MAINTENANCE_ENABLED` | blueprint | `true` |
| `CORS_ALLOWED_ORIGINS` | `sync: false` | Empty until a dashboard exists |

**No secret is committed.** `DATABASE_URL` comes from the database resource,
`METRICS_TOKEN` is generated by Render, and `CORS_ALLOWED_ORIGINS` is prompted
for. The repository's `.gitignore` already covers `.env` and
`deploy/.env.production`.

### `TRUSTED_PROXIES=none`, and what it costs

Render terminates TLS at its own edge and forwards to the container with an
`X-Forwarded-For` header. We do not control or know those addresses, and gin
trusts every proxy by default — which would let any caller choose its own
apparent address. That address is written onto the device row at registration
and heartbeat.

So `none`: the header is ignored and `ClientIP()` reports the peer, which is
Render's proxy. **The recorded device address is therefore Render's, not the
terminal's, in this environment.** That is a real loss and it is the right
trade: a useless address beats an attacker-chosen one. On the VPS the setting
goes back to loopback, where Nginx is on the same host and overwrites the header.

### Database TLS

`DATABASE_URL` from Render carries no `sslmode`, and libpq's default without one
is `prefer` — it attempts TLS, silently accepts plaintext if the server declines,
and reports success either way. `database.normalizeDatabaseURL` appends
`sslmode=require`, and *refuses* a URL asking for `disable`, `allow` or `prefer`
against any host that is not loopback. `deploy/migrate.sh` applies the same rule
to the same variable. There is no flag to turn either off.

---

## Deployment procedure

Nothing below has been done.

1. **Push this branch.** Render reads `render.yaml` from the repository.
2. **Dashboard → New → Blueprint**, point it at `gym-cloud-api`, select the
   branch. Render shows the two resources it will create; confirm they both say
   **Free**.
3. It prompts for `CORS_ALLOWED_ORIGINS` (`sync: false`). Leave it empty.
4. Apply. Render provisions the database, builds `deploy/Dockerfile`, and starts
   the service. First build is a few minutes — no module cache exists yet.
5. The service will come up and **fail its readiness probe**, because the schema
   does not exist yet. Expected. `/health` still answers.
6. **Apply the migrations** ([below](#migrations)).
7. Check `/health` and `/health/ready` on the `*.onrender.com` hostname.
8. Only then, the custom domain ([below](#connecting-apiaccesslinkstore)).

### Verify the deploy

```sh
# Render assigns the hostname when the service is created. Copy it from the
# dashboard -- do not assume it matches the service name.
BASE=https://<the-hostname-render-shows>.onrender.com

curl -s "$BASE/health"          # {"status":"healthy","version":"dev","commit":"<sha>"}
curl -s "$BASE/health/ready"    # {"status":"ready","checks":{"database":{"status":"up"}}}
curl -si "$BASE/health" | head -1
```

`commit` comes from `RENDER_GIT_COMMIT` — Render builds the Dockerfile without
the `VERSION`/`COMMIT` build arguments the Makefile passes, so the binary is not
stamped at link time and `main.resolveCommit` reads the platform's value
instead. `version` will read `dev` here. That is expected on Render and not a
broken build.

---

## Migrations

**Free services have no shell and cannot run one-off jobs or a pre-deploy
command.** Migrations are applied from a workstation, against the database's
**external** URL, with the same runner the VPS uses:

```sh
# Dashboard → accesslink-db → Connect → External Database URL
export DATABASE_URL='postgres://at_admin:...@dpg-xxxx.oregon-postgres.render.com/access_terminal'

sh deploy/migrate.sh --status     # what is applied, what is pending
sh deploy/migrate.sh --dry-run    # what would run
sh deploy/migrate.sh              # apply
```

Requires `psql` locally. The runner keeps its `schema_migrations` ledger with a
checksum per file, so each migration runs exactly once and an edit to an
already-applied file is caught rather than silently skipped.

TLS is enforced on this path: no `sslmode` in the URL means `require`, and a URL
asking for a downgrade against a remote host is refused outright.

The external URL is a credential for the whole database. It is not committed, it
does not go in `render.yaml`, and it should not be pasted anywhere durable.

After the schema is applied, narrow the database's IP allow list in the Render
dashboard (**accesslink-db → Networking**) from `0.0.0.0/0` to the address you
migrate from.

Seed data, if wanted:

```sh
psql "$DATABASE_URL" -f seeds/dev_seed.sql
```

**When the free database expires and is deleted**, the recovery is: create a new
free database, update the blueprint/dashboard link, re-run the migrations,
re-seed, and re-register the terminals — device credentials live in the database
and do not survive it.

---

## Health checks

`healthCheckPath: /health` in the blueprint.

`/health` is deliberate, not `/health/ready`. Readiness reports 503 while the
database is unreachable, and Render restarts a service that fails its health
check — during a database blip that turns one outage into a restart loop. Render
also uses the health check to decide when a new deploy is live, and a deploy
that cannot go live because the database is briefly slow is a self-inflicted
outage. `/health` touches no dependency and still carries `version` and `commit`.

`/health/ready` remains the right endpoint to *look at* by hand, and
`/health/live`, `/health/maintenance` and `/metrics` are unchanged. `/metrics`
needs the generated `METRICS_TOKEN`:

```sh
curl -s -H "Authorization: Bearer $METRICS_TOKEN" "$BASE/metrics"
```

The Docker `HEALTHCHECK` in `deploy/Dockerfile` now probes `${PORT:-8080}`.
Render ignores a Dockerfile healthcheck and uses `healthCheckPath`, but the
image is the same one compose runs, and a hard-coded 8080 would have made it
probe the wrong port anywhere `PORT` is injected.

---

## Connecting api.accesslink.store

**Do not do this yet** — the DNS records below are recorded, not created.

Render serves the service on an `onrender.com` hostname it assigns at creation.
A custom domain is supported on free instance types, including the managed TLS
certificate.

1. Deploy and confirm the service is healthy on the `onrender.com` hostname
   Render assigned it.
2. Dashboard → `accesslink-api` → **Settings → Custom Domains → Add**.
3. Enter `api.accesslink.store`. Render then displays **the exact CNAME target**
   for this service. Copy it from that screen. It is not written down in this
   document and must not be guessed — Render assigns the hostname and appends a
   suffix when the service name is already taken platform-wide.
4. Create the DNS record at Namecheap with that value
   ([below](#dns-records-to-create-in-namecheap)).
5. Back in Render, click **Verify**. Render issues the certificate once the name
   resolves to it. Issuance is usually minutes; it can take longer.
6. Confirm:
   ```sh
   nslookup api.accesslink.store
   curl -sI https://api.accesslink.store/health | head -1
   curl -sI http://api.accesslink.store/health | head -1   # 301 to https
   ```

Render redirects HTTP to HTTPS automatically. There is no plaintext path to the
API and nothing in the firmware would use one if there were.

### What this replaces

The Nginx site in `deploy/nginx/` terminates TLS and applies rate limits for the
VPS deployment. On Render, Render's edge does the TLS. **The rate limits do not
come with it** — `limit_req` is an Nginx feature and there is no equivalent in
front of a Render free service. That is a difference between the two
environments worth remembering before treating staging behaviour as production
behaviour.

---

## DNS records to create in Namecheap

**Not created.** This is the record to add when we get there.

Namecheap → Domain List → `accesslink.store` → **Manage** → **Advanced DNS** →
**Add New Record**:

| Type | Host | Value | TTL |
|---|---|---|---|
| `CNAME Record` | `api` | *the exact `onrender.com` hostname shown in the Render dashboard* | Automatic |

**The Value is not written down here, on purpose.** Render assigns the
`onrender.com` hostname when the service is created, and it is not always the
service name — Render appends a suffix when the name is already taken across the
platform. Take it from **Settings → Custom Domains** after adding
`api.accesslink.store`, which displays the exact CNAME target for this service,
and paste that. A guessed hostname produces a record that resolves to somebody
else's service or to nothing, and the certificate never issues.

Notes that matter:

- **Host is `api`, not `api.accesslink.store`.** Namecheap appends the domain.
  Entering the full name creates `api.accesslink.store.accesslink.store`.
- **CNAME, not A. Never an A record pointing at an `onrender.com` hostname.**
  Render's service addresses are not static, an A record freezes whatever the
  name resolved to on the day it was created, and it will break silently when
  Render moves the service. `onrender.com` is a name to be followed, not an
  address to be copied. (The VPS deployment is the opposite — an `A` record to a
  fixed IP that we own. The two are mutually exclusive: whichever we point the
  name at is the one that gets the certificate.)
- **Remove any existing `A` or `AAAA` record for the `api` host** before adding
  the CNAME. Render's documentation calls out `AAAA` records specifically as a
  cause of unexpected behaviour. Conflicting records for the same host are the
  usual reason verification fails.
- Namecheap's **URL Redirect Record** is not a DNS record and will not work.
- Keep the TTL short until it has settled.
- The apex `accesslink.store` is untouched. Only the `api` label is in scope, and
  the frontend is explicitly out of scope for now.

`deploy/README.md` documents an `A` record for the same hostname pointing at a
VPS. Both cannot be live at once. Nothing has been created for either, so there
is nothing to undo — but only one is our answer, and for now it is this one.

---

## What the ESP32 needs before it can talk to this

**The terminal is not being flashed yet.** This is what will have to be true when
it is, and one item is a genuine change from the VPS plan.

`include/root_ca.h` builds no root certificate in. A build with none refuses
every `https://` request outright — no fallback, no `setInsecure()`, no runtime
override. The root has to be supplied at build time via
`-DCLOUD_ROOT_CA_INCLUDE`.

**Render does not issue only from Let's Encrypt.** Render's managed certificates
come from **Let's Encrypt or Google Trust Services**, and which one a given
domain gets is Render's choice, not ours — it can change on renewal. `root_ca.h`
currently says to pin ISRG Root X1 and X2, which is correct for the Nginx VPS
plan and **not sufficient here**.

So the bundle for a Render deployment must contain the roots from both CAs:

- ISRG Root X1 and ISRG Root X2 (Let's Encrypt, <https://letsencrypt.org/certs/>)
- GTS Root R1–R4 (Google Trust Services, <https://pki.goog/repository/>)

Get them from the CA, not from the server — `openssl s_client -showcerts` prints
leaf and intermediate, never the root, and pinning an intermediate fails on the
next renewal. mbedTLS parses a concatenated bundle and matches whichever root
anchors the presented chain, so listing all six costs only flash.

A terminal flashed with only the ISRG roots will fail every request the day
Render renews from Google, with no way in to fix it. This has to be settled
before any flashing, not after.

Also: point the terminal at `https://api.accesslink.store` only after the
certificate is issued, and expect the first request after an idle period to time
out on the cold start described above.

---

## What could stop this working

Known risks, worst first.

1. **The ESP32 root CA bundle.** As above. Blocks the fleet, not the deploy, and
   costs a reflash if we get it wrong.
2. **Cold starts vs. a 10-second client timeout.** Real, permanent on free, and
   only survivable because the firmware queues and retries. Anything requiring a
   response within 20 seconds of an idle service will not get one.
3. **The 30-day database expiry.** Not a risk to the deploy; a scheduled loss of
   everything in it. Treat this environment as disposable.
4. **Internal `DATABASE_URL` and `sslmode=require`.** The service appends
   `require` to the internal connection string. Render Postgres serves TLS, so
   this is expected to work — but it is the one assumption in this change that
   is not verified locally, and if the internal endpoint ever refuses TLS the
   service will fail to start with a clear database error. The fallback is to set
   `DATABASE_URL` to the *external* URL in the dashboard, which unambiguously
   requires TLS, at the cost of latency. Do not "fix" it by weakening `sslmode`.
5. **A conflicting DNS record for `api`.** The usual cause of a custom domain
   that never verifies. Check for a stale `A`/`AAAA` first.
6. **Blueprint field drift.** `render.yaml` is written against Render's current
   Blueprint spec (`runtime:`, `plan: free`, `databases:`). If Render has renamed
   a field, the dashboard rejects the file with the offending key named — a
   loud, harmless failure to fix in place.
7. **Free-tier availability.** Render can change what "free" includes, and a
   region can be short of free capacity. If the dashboard offers anything other
   than Free for either resource, stop: paid resources are out of scope.
8. **No rate limiting.** The Nginx `limit_req` protection does not exist on this
   path. The API is exposed to the internet with per-device authentication and
   no request throttle in front of it.
