# Sprint 7 Post-Merge Verification Report

Read-only verification of the actual repository state after Sprint 7
(`312d61a chore(prod): production-readiness pass -- security, sync fixes, test suite`).

No source code was modified in producing this report. Every claim below was checked
against the files as they stand, and the database claims were executed against a real
PostgreSQL 18 instance: throwaway databases were built from `migrations/`, inspected,
and dropped.

## Verified clean

No defects found in: the `002` firmware INSERT removal (the only `INSERT` anywhere in
`migrations/` is `INSERT INTO companies`); fresh-database contents (exactly one row,
`companies`, therefore zero sites and zero credentials); `007` on both empty and
populated databases including idempotent re-run; `middleware/auth.go` (a single CORS
middleware, no duplicate or conflicting headers); `main.go` / `router.go` (one
`gin.New()`, one `NewRouter()` call, every path+method registered exactly once);
`database/firmware.go` (all eight statements `PREPARE` against the live schema, all
company-scoped); enrollment (no `members` table reference remains in any Go file, all
live queries company-scoped). `gofmt -l .` clean; `go vet ./...` clean; the full suite
passes against a real PostgreSQL.

## Findings index

| ID | Severity | Area | Migration | API change |
|---|---|---|---|---|
| #1 | Medium | `migrations/006_device_firmware_state.sql` | No | No |
| #2 | Low | `migrations/007_firmware_tenancy.sql` | Yes — new `008` | No |
| #3 | Low | `database/queries.go` | No | No |
| #4 | Low | `handlers/firmware.go` | No | Yes |
| #5 | High | `main_test.go` / `database/db.go` | No | No |

---

## FINDING #1 — Migration 006 still marks the removed 1.0.0 placeholder as the current firmware target

**ID:** SPRINT7-001

**Severity:** Medium — conditional trigger, fleet-wide impact when it fires

**File:** `migrations/006_device_firmware_state.sql`

**Function/line:** Lines 93–104, the statement under the comment
*"Seed the 1.0.0 dev build as the current STABLE target if nothing is set"*.
No Go function is involved; the affected consumers are `database.ListDevices` and
`database.GetFleetSummary` in `database/firmware.go`.

**Problem**

The block still present in the final file:

```sql
-- Seed the 1.0.0 dev build as the current STABLE target if nothing is set
UPDATE firmware_versions
   SET is_current = TRUE
 WHERE version = '1.0.0'
   AND device_type = 'TERMINAL'
   AND release_channel = 'STABLE'
   AND deleted_at IS NULL
   AND NOT EXISTS (
        SELECT 1 FROM firmware_versions
         WHERE device_type = 'TERMINAL' AND release_channel = 'STABLE'
           AND is_current AND deleted_at IS NULL
   );
```

Sprint 7 moved the 1.0.0 firmware row out of `002_core_schema.sql` into
`seeds/dev_seed.sql`, where it is inserted **deliberately not current**
(`seeds/dev_seed.sql:55-56`: *"A build for the fleet to be measured against. Not marked
current: leaving it current would report every terminal as outdated against a
placeholder."*). Migration 006 still contains the statement that flips exactly that row
to `is_current = TRUE`, overriding the seed's stated intent.

Two secondary defects in the same block:

* Its `NOT EXISTS` guard is installation-wide rather than `company_id`-scoped, so it no
  longer matches the tenancy model migration 007 established.
* It hardcodes a version string that no migration produces any more.

**Reproduction**

Executed against a real PostgreSQL 18 instance:

```
1) fresh DB, apply migrations 001-007
   -> SELECT count(*) FROM firmware_versions = 0 rows      (block is a no-op)

2) psql -v SEED_ALLOW_INSECURE=1 -f seeds/dev_seed.sql
   -> 1.0.0 | TERMINAL | STABLE | is_current = f

3) re-apply migrations/006_device_firmware_state.sql
   -> 1.0.0 | TERMINAL | STABLE | is_current = t           <- flipped

4) insert 3 terminals reporting firmware 1.4.2,
   run the ListDevices outdated predicate
   -> reported_outdated = 3, total_devices = 3
```

**Why it matters**

Step 3 reproduces precisely the regression that `002_core_schema.sql:520-523` says was
being eliminated: *"A demo firmware row used to be inserted here, and migration 006
marks the newest build per channel as current. On a production database that combined
to report every real terminal as 'firmware outdated' against a placeholder 1.0.0 that
was never a real build."* Sprint 7 removed one half of that pair and left the other half
in place.

When it fires, `GET /api/v1/devices?outdated=true`, `GET /api/v1/devices/summary`, and
the fleet dashboard header all report the entire fleet as behind. Once OTA exists,
`is_current` is the row a terminal would be pointed at.

Trigger requires migrations re-applied over a seeded database.
`docker-entrypoint-initdb.d` runs only on first container init, so the bundled compose
path will not hit this; any manual re-run, restore-then-migrate, or CI reset that seeds
before migrating will.

**Recommended fix**

Delete lines 93–104 from `006`. This is safe without a new migration, and the argument
is verifiable rather than a judgement call: `firmware_versions` holds **0 rows** at the
point 006 executes in any migrations-only build (measured in step 1 above), so the block
cannot affect the outcome of any correctly ordered run. Removing it changes nothing
except the re-run hazard.

If the "never edit applied history" convention stated at `002:7-9` is to be honoured
strictly, replace the block with a comment explaining why it was removed and neutralise
it in a new `008` instead.

**API / schema / production-behavior impact**

* API: none.
* Schema: none — no DDL, and no data change on any correctly ordered run.
* Production behavior: changes only on migration re-application over a seeded database,
  where it restores correct outdated-reporting instead of flagging every device.

---

## FINDING #2 — Firmware tenant foreign key is `ON DELETE CASCADE` while every peer is `RESTRICT`

**ID:** SPRINT7-002

**Severity:** Low — latent data loss, currently masked

**File:** `migrations/007_firmware_tenancy.sql`

**Function/line:** Lines 42–44, constraint `firmware_versions_company_id_fkey`.

**Problem**

As written in the final file:

```sql
ALTER TABLE firmware_versions DROP CONSTRAINT IF EXISTS firmware_versions_company_id_fkey;
ALTER TABLE firmware_versions ADD CONSTRAINT firmware_versions_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE CASCADE;
```

The final schema has four tenant foreign keys and this one disagrees with the other
three:

| Constraint | Defined in | On delete |
|---|---|---|
| `sites_company_id_fkey` | `002:102-103` | `RESTRICT` |
| `people_company_id_fkey` | `002:195-196` | `RESTRICT` |
| `access_logs_company_id_fkey` | `002:294-295` | `RESTRICT` |
| `firmware_versions_company_id_fkey` | `007:43-44` | **`CASCADE`** |

**Reproduction**

Read the four constraint definitions above; all four were confirmed present in the
schema built from `migrations/`. Operationally: a company that owns firmware rows but
no sites, people, or access logs — a tenant mid-onboarding, or one whose sites were hard
deleted — is deletable, and `DELETE FROM companies WHERE id = N` silently destroys its
entire firmware catalog rather than being refused.

**Why it matters**

The catalog carries download URLs, SHA-256 checksums, and the `is_current` deployment
target. By 007's own reasoning (`007:20-21`): *"once OTA exists, that same row is what a
terminal would be pointed at."* Everywhere else in this schema the rule is that a tenant
cannot be deleted out from under its data; here the data is deleted instead.

Currently unreachable in most states because the three `RESTRICT` peers block the delete
first whenever the tenant owns any site, person, or log. This is a latent inconsistency,
not a live bug.

**Recommended fix**

New migration `migrations/008_firmware_fk_restrict.sql`:

```sql
-- ---------------------------------------------------------------------------
-- 008: align the firmware tenant foreign key with the rest of the schema
-- ---------------------------------------------------------------------------
--
-- 007 attached firmware_versions to a company with ON DELETE CASCADE, while the
-- three tenant foreign keys that predate it -- sites, people, access_logs --
-- are all ON DELETE RESTRICT.
--
-- The inconsistency matters because of what the catalog holds: download URLs,
-- checksums, and the is_current deployment target that defines "outdated" for a
-- fleet and that OTA would later point terminals at. Deleting a company that
-- owns firmware but has no sites, people or logs yet would take the catalog
-- with it, silently. RESTRICT makes that a refused delete, which is the
-- behaviour every other tenant-owned table already has.
--
-- No rows are read or written; this only replaces the referential action.

BEGIN;

ALTER TABLE firmware_versions DROP CONSTRAINT IF EXISTS firmware_versions_company_id_fkey;
ALTER TABLE firmware_versions ADD CONSTRAINT firmware_versions_company_id_fkey
    FOREIGN KEY (company_id) REFERENCES companies(id) ON DELETE RESTRICT;

COMMIT;
```

It is idempotent (`DROP CONSTRAINT IF EXISTS` then re-add), touches no rows, and takes
only a brief `ACCESS EXCLUSIVE` lock on `firmware_versions`. If 007 has not yet been
deployed anywhere, editing `007:44` in place is equivalent and cheaper.

**API / schema / production-behavior impact**

* API: none.
* Schema: **yes — requires a migration.** Referential action on one existing constraint;
  no columns, indexes, or rows change.
* Production behavior: `DELETE FROM companies` against a tenant owning firmware is
  refused with a foreign-key violation instead of cascading. No application code path
  performs company deletion today, so no handler behavior changes.

---

## FINDING #3 — `UpdateEnrollmentRequestStatus` is unreachable and not company-scoped

**ID:** SPRINT7-003

**Severity:** Low — latent; not currently exploitable

**File:** `database/queries.go`

**Function/line:** Lines 245–255, `func UpdateEnrollmentRequestStatus(id int64, status string) error`.

**Problem**

The function as it stands:

```go
func UpdateEnrollmentRequestStatus(id int64, status string) error {
	var completedAt sql.NullTime
	if status == "COMPLETED" || status == "FAILED" {
		now := time.Now()
		completedAt = sql.NullTime{Time: now, Valid: true}
	}

	query := `UPDATE enrollment_requests SET status = $1, completed_at = $2 WHERE id = $3`
	_, err := DB.Exec(query, status, completedAt, id)
	return err
}
```

No `companyID` parameter, and the `WHERE` clause filters on the raw internal id alone.
Every sibling in the same file is scoped:

* `CreateEnrollmentRequest:199` — `p.company_id = $2`
* `GetPendingEnrollmentRequests:217` — `p.company_id = $1`
* `CompleteEnrollment:259` — `company_id = $3` on both statements

All three handlers in `handlers/enrollment.go` pass `c.GetInt64("company_id")`. This is
the only exported write in the enrollment path without a tenant filter.

**Reproduction**

There is **no runtime reproduction, and that is stated deliberately** — the defect is
structural, not live. `grep -rn "UpdateEnrollmentRequestStatus"` across the repository
returns exactly two hits: the declaration at `queries.go:245` and its doc comment at
`:244`. No handler, no test, and no other package references it. Nothing reaches it, so
nothing currently crosses a tenant boundary through it.

The reproduction is the read: `WHERE id = $3` will update any tenant's enrollment
request the moment a caller passes an id it did not itself derive under a company filter.

**Why it matters**

`database/queries.go:14-17` states the package invariant as absolute: *"Reads and writes
are scoped by company_id, which the auth middleware derives from the API key's site.
Nothing crosses a tenant boundary."* This function is the single exception to a rule the
file claims is universal, and it sits unused beside four correct implementations of the
same operation.

The realistic failure is not today; it is the next engineer adding an admin or callback
route who reaches for the obviously-named helper and inherits an unscoped write.
`enrollment_requests` also has no `company_id` column of its own — scoping requires the
`person_id -> people.company_id` join — so the correct form is not something a caller can
add at the call site.

**Recommended fix**

Delete the function. Nothing calls it, and `CompleteEnrollment:282-286` already closes
requests out inline with a properly scoped subquery:

```go
_, err = tx.Exec(`UPDATE enrollment_requests SET status = 'COMPLETED', completed_at = $1
                  WHERE status = 'PENDING' AND person_id = (
                      SELECT id FROM people
                      WHERE external_id = $2 AND company_id = $3 AND deleted_at IS NULL
                  )`, now, memberID, companyID)
```

If it is wanted for a future admin path, add `companyID int64` as the first parameter and
scope it through that same join rather than on `enrollment_requests.id`.

**API / schema / production-behavior impact**

* API: none.
* Schema: none.
* Production behavior: none — the function is dead code, so deleting it is observably a
  no-op. The build and `go vet` stay clean either way.

---

## FINDING #4 — Internal `BIGSERIAL` id exposed in the firmware route

**ID:** SPRINT7-004

**Severity:** Low

**File:** `handlers/firmware.go` (with `router.go` and `database/firmware.go`)

**Function/line:** `handlers/firmware.go:94-99`, `func SetCurrentFirmware(c *gin.Context)`;
route registered at `router.go:95`; backing query at `database/firmware.go:194`,
`func SetCurrentFirmware(companyID, firmwareID int64)`.

**Problem**

The handler parses the path segment as the raw sequence primary key:

```go
func SetCurrentFirmware(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid firmware id"})
		return
	}
	...
	version, err := database.SetCurrentFirmware(c.GetInt64("company_id"), id)
```

`migrations/002_core_schema.sql:13-14` states the convention this violates:

```
--   * id          BIGSERIAL, internal only. Never exposed over the API.
--   * public_id   UUID, the stable public identifier used in URLs/payloads.
```

Every other route honours it — members and access use `external_id`, devices use
`serial_number`. `PUT /api/v1/firmware/:id/current` is the only endpoint whose URL
carries a sequence primary key.

**Reproduction**

`router.go:95` registers `firmware.PUT("/:id/current", handlers.SetCurrentFirmware)`;
`handlers/firmware.go:95` parses it with `strconv.ParseInt`. The repository's own test
drives it that way — `security_test.go:186-197`:

```go
created := env.do(http.MethodPost, "/api/v1/firmware", map[string]any{
    "version": "9.9.9", "device_type": "TERMINAL", "release_channel": "STABLE",
}, siteAuth(env.siteCKey))
id, _ := created.Body["id"].(float64)
...
res := env.do(http.MethodPut, "/api/v1/firmware/"+itoa(int64(id))+"/current", nil, siteAuth(env.siteAKey))
```

The integer is read straight out of the create response's `id` field and used as the URL
segment.

**Why it matters**

This is **not** a tenancy hole, and the boundary is worth stating exactly.
`database/firmware.go:202-205` filters `id = $1 AND company_id = $2`, so another tenant's
id returns `sql.ErrNoRows`, and `handlers/firmware.go:104-107` maps that to 404 —
indistinguishable from a nonexistent id. `security_test.go:183-202`
(`TestTenantCannotRetargetAnotherTenantsFleet`) asserts exactly this and passes.

What it actually costs is the ordinary price of an exposed monotonic sequence: catalog
size and creation rate are inferable, and ids are enumerable. The more durable cost is
that one endpoint now contradicts a convention the schema documents as absolute, which is
how conventions quietly stop being enforced.

**Recommended fix**

Accept `public_id` (UUID). `firmware_versions_public_id_key` already exists (`002:405`),
so the lookup stays a single index probe: change `WHERE id = $1` to
`WHERE public_id = $1` in both statements of `database.SetCurrentFirmware` and widen the
parameter to `string`.

To avoid breaking existing clients, parse the path segment as a UUID first and fall back
to the integer form, keeping both working during a deprecation window.

**API / schema / production-behavior impact**

* API: **yes — the only finding of the five that touches the wire contract**, and it is
  confined to one request path parameter.

  Response bodies need no change. `models/models.go:258-260` already serializes both
  identifiers on `FirmwareVersion`:

  ```go
  type FirmwareVersion struct {
      ID             int64      `json:"id"`
      PublicID       string     `json:"public_id"`
  ```

  so `GET /api/v1/firmware` and `POST /api/v1/firmware` already hand clients the
  `public_id` they would need.

  | | Before | After |
  |---|---|---|
  | Route | `PUT /api/v1/firmware/:id/current` | unchanged path shape |
  | `:id` accepts | `firmware_versions.id`, integer | `public_id`, UUID (integer still accepted during deprecation) |
  | Request body | none | none |
  | Response body | `FirmwareVersion` | unchanged |
  | Unknown / foreign id | 404 | 404, unchanged |
  | Unparseable segment | 400 `"Invalid firmware id"` | 400, unchanged |

  It is breaking only for a client that stored the integer from an earlier
  `POST /firmware` response and replays it after the integer fallback is removed — which
  is why dual-parse is recommended over a straight swap.

* Schema: none — `public_id` and its unique index already exist.
* Production behavior: none beyond identifier parsing. Tenancy enforcement, 404
  semantics, and the demote/promote transaction are untouched.

**Related observation, not a separate finding:** `models.DeviceInventory`
(`models/models.go:223-226`) also serializes `id` and `site_id` alongside `public_id`, so
`GET /api/v1/devices` exposes internal sequence ids in its response body as well. No
route consumes them, so nothing breaks today, but that is the second place to look if the
`002:13` convention is to be enforced rather than merely stated.

---

## FINDING #5 — `go test ./...` reports green without ever reaching a database

**ID:** SPRINT7-005

**Severity:** High

**File:** `main_test.go`, `database/db.go`, `main.go`, `README.md`

**Function/line:** `main_test.go:49` `func TestMain(m *testing.M)` -> `main_test.go:68`
`func runSuite(m *testing.M) (int, error)` -> `main_test.go:69`
`database.GetConfigFromEnv()`; `database/db.go:87-100` `func GetConfigFromEnv() Config`
(default at `:91`); `main.go:22` `godotenv.Load()`; `README.md:309`.

**Problem**

Three defects compound into a false pass.

*Why the cache lies.* Go's test cache keys a result on the test binary, its command-line
arguments, the files it opens, and the environment variables it reads. It has **no
visibility into a TCP connection to PostgreSQL**. This suite's entire result depends on
that connection — `main_test.go:23-31` says so directly: *"These tests run against a real
PostgreSQL database rather than a mock."* So a pass recorded under an earlier machine
state, one where `at_admin@localhost:5432` was reachable with `DB_*` unset, is replayed
verbatim as a cache hit. Go also never caches failures, so the stale `ok` entry survives
a failing run and continues to be served.

*Why `.env` is not loaded.* `grep -rn "godotenv|\.env" --include=*.go .` returns exactly
three lines, all in `main.go` (17, 22, 23). `godotenv.Load()` is called inside
`func main()`, which a test binary never executes. `TestMain` goes directly to
`database.GetConfigFromEnv()` with no `.env` load anywhere in the path. The server reads
`.env`; the tests do not.

*Where `at_admin` comes from.* `database/db.go:91`, inside `GetConfigFromEnv()`:

```go
User: getEnv("DB_USER", "at_admin"),
```

`getEnv` (`db.go:102-106`) returns the default whenever the variable is empty. The value
is inconsistent across the repo: `.env:4` says `postgres`, while `.env.example:4` and
`docker-compose.yml:8` say `at_admin`. Because nothing loads `.env`, the tests get
`at_admin`.

**Reproduction**

Reproduced live, same shell, same environment, in this order:

```
$ go test ./...
ok  	access-terminal-cloud-api	(cached)

$ go test -count=1 ./...
integration tests could not start: error connecting to database:
pq: password authentication failed for user "at_admin" (28P01)
FAIL	access-terminal-cloud-api	1.287s

$ go test ./...
ok  	access-terminal-cloud-api	(cached)        <- still green
```

Environment variables *are* part of the cache key — the
`DB_USER=postgres DB_PASSWORD=12345` run re-executed for 11.42s rather than hitting cache
— which is precisely why the bare command keeps landing on the stale entry: it
reproduces the same all-unset environment the cached run had.

*Failing, not skipped, and not the wrong database.* Each verified separately:

* Not skipped — `TEST_DB_SKIP` is empty, so the guard at `main_test.go:52` does not
  short-circuit.
* Not the wrong host — `localhost:5432` has a live listener (one listener, PID 12032).
* Wrong user, and only that — `pq: password authentication failed for user "at_admin"`.
  `SELECT rolname FROM pg_roles` on that server returns only `postgres`; no `at_admin`
  role exists on it.
* Database targeting is correct and safe — `runSuite` connects to admin database
  `postgres` (`TEST_ADMIN_DB`, `main_test.go:74`), then drops and creates
  `access_terminal_test` (`TEST_DB_NAME`, `main_test.go:38`). It never touches
  `access_terminal`. The failure occurs at the admin connection, before any database is
  created.

**Why it matters**

This suite is the only thing guarding the tenancy SQL — the cross-tenant assertions in
`security_test.go:136-202`, the partial unique indexes, the acknowledgement constraint,
`SKIP LOCKED` delivery. `main_test.go:24-28` makes the case itself: *"The behaviour worth
protecting here lives in SQL... The sync engine bugs this suite covers were all in the
SQL."*

A green `go test ./...` on a machine with no database is byte-identical to a real pass, so
a tenancy regression can be committed, reviewed, and merged as "tests pass." That is not
hypothetical: it produced a false green during this verification, and the first evidence
of Sprint 7's test state was that stale entry.

Compounding it, `README.md:309` documents the command **without** `-count=1`:

```
DB_HOST=localhost DB_USER=postgres DB_PASSWORD=secret go test ./...
```

so the documented invocation carries the same trap.

**Correct command for a genuinely fresh full integration run**

```bash
go clean -testcache
DB_USER=postgres DB_PASSWORD=12345 go test -count=1 ./...
```

`-count=1` is the supported cache bypass; `go clean -testcache` additionally clears the
already-poisoned entry so a later bare invocation cannot resurrect it. Requirements: a
PostgreSQL reachable at `localhost:5432` (override with `DB_HOST` / `DB_PORT`), and an
account with `CREATEDB` — the suite drops and recreates `access_terminal_test` from
`migrations/` on every run, which is also what makes it the standing check that a fresh
database builds from zero (`main_test.go:30-31`). Confirmed result:

```
ok  	access-terminal-cloud-api	11.420s
ok  	access-terminal-cloud-api/models	0.945s
```

**Recommended fix**

1. Call `godotenv.Load()` from `runSuite` before `GetConfigFromEnv()`, ignoring a missing
   file, so tests and server read one configuration source.
2. Fail loudly instead of silently defaulting: in `TestMain`, when no `DB_*` variables are
   set and `TEST_DB_SKIP` is unset, print the required command rather than dialing
   `at_admin` and reporting an auth error that reads like a credentials problem.
3. Update `README.md:309` to include `-count=1`, with a note that the test cache cannot
   observe the database and that a bare `go test ./...` can report a stale pass.
4. Reconcile the `at_admin` / `postgres` split across `database/db.go:91`, `.env`,
   `.env.example`, and `docker-compose.yml` so one value is authoritative.

**API / schema / production-behavior impact**

* API: none.
* Schema: none.
* Production behavior: none — `main.go` and every handler are untouched. Changes are
  confined to the test harness, `README.md`, and configuration defaults. The visible
  change is that `go test` without a database fails with actionable instructions instead
  of a stale `ok`.

---

## Status

No fixes have been applied. This report is the only change in the working tree.
