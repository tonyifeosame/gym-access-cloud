#!/usr/bin/env sh
#
# Apply pending migrations, exactly once each.
#
# ---------------------------------------------------------------------------
# WHY THIS EXISTS
# ---------------------------------------------------------------------------
#
# deploy/README.md used to say: loop over migrations/*.sql and psql each one.
# That works precisely once. The migrations in this repository are NOT
# idempotent -- 001 has bare CREATE TRIGGER, 002 through 007 have bare
# ALTER TABLE ... ADD CONSTRAINT, and 002 seeds a company row. PostgreSQL has
# no CREATE TRIGGER IF NOT EXISTS and no ADD CONSTRAINT IF NOT EXISTS, so the
# second run of that loop fails partway through with "trigger already exists".
#
# That failure is not the dangerous part. The dangerous part is WHERE it fails:
# 002 through 007 wrap themselves in BEGIN/COMMIT, so a re-run rolls those back
# cleanly -- but 001 does not, so a re-run of 001 leaves whatever it managed to
# execute before the first duplicate trigger. A deploy that half-applies schema
# and reports an error is the worst of the available outcomes.
#
# So: a ledger. schema_migrations records what has been applied; each file runs
# once, inside one transaction with its own ledger row, and either both land or
# neither does.
#
# ---------------------------------------------------------------------------
# USAGE
# ---------------------------------------------------------------------------
#
#   sh deploy/migrate.sh              apply everything pending
#   sh deploy/migrate.sh --status     list applied/pending, change nothing
#   sh deploy/migrate.sh --dry-run    show what would run, change nothing
#   sh deploy/migrate.sh --baseline   record all files as applied WITHOUT
#                                     running them; for a database that was
#                                     already migrated by hand (see below)
#
# Connection comes from the environment, the same variables the API reads:
#
#   set -a; . deploy/.env.production; set +a
#   DB_HOST=localhost sh deploy/migrate.sh
#
# DATABASE_URL, if set, supersedes all of them and psql runs directly against
# it. That is the path for a managed database -- Render, RDS, Neon -- which
# hands out one connection string and no host port to reach any other way:
#
#   DATABASE_URL='postgres://user:pass@host/db' sh deploy/migrate.sh
#
# It matters on Render's free tier specifically: the service has no shell and
# cannot run one-off jobs, so migrations are applied from a workstation against
# the database's EXTERNAL URL. That connection crosses the public internet, so
# this script requires TLS on it -- sslmode defaults to `require` and a URL
# asking for a downgrade against a remote host is refused, matching what the API
# itself does with the same variable.
#
# In the compose deployment PostgreSQL publishes no host port, so psql has to
# run inside the container. That is the default when docker compose is present
# and DB_HOST is unset or "postgres"; force either way with:
#
#   MIGRATE_VIA_COMPOSE=1   run psql inside the compose postgres service
#   MIGRATE_VIA_COMPOSE=0   run psql on this host against DB_HOST:DB_PORT

set -eu

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MIGRATIONS_DIR="$REPO_ROOT/migrations"
COMPOSE_FILE="$REPO_ROOT/deploy/docker-compose.prod.yml"
ENV_FILE="${ENV_FILE:-$REPO_ROOT/deploy/.env.production}"

DB_USER="${DB_USER:-at_admin}"
DB_NAME="${DB_NAME:-access_terminal}"
DB_HOST="${DB_HOST:-}"
DB_PORT="${DB_PORT:-5432}"
DB_SSLMODE="${DB_SSLMODE:-disable}"
DATABASE_URL="${DATABASE_URL:-}"

MODE=apply
for arg in "$@"; do
  case "$arg" in
    --status)   MODE=status ;;
    --dry-run)  MODE=dryrun ;;
    --baseline) MODE=baseline ;;
    -h|--help)  sed -n '2,62p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

# --- how we reach psql -------------------------------------------------------

if [ -n "$DATABASE_URL" ]; then
  # A connection string names its own server. Nothing about compose applies.
  MIGRATE_VIA_COMPOSE=0

  # Refuse a downgrade to plaintext against a remote database, and default to
  # TLS when the URL says nothing -- psql's own default is `prefer`, which tries
  # TLS, silently accepts plaintext when the server declines, and reports
  # success either way. The API applies the same rule to the same variable; the
  # migration path carries the same credentials over the same network and has no
  # business being weaker than the thing it is preparing.
  url_host=$(printf '%s' "$DATABASE_URL" \
    | sed -e 's|^[a-zA-Z][a-zA-Z0-9+.-]*://||' -e 's|^[^@/]*@||' \
          -e 's|[/?].*$||' -e 's|:[0-9]*$||')

  case "$url_host" in
    localhost|127.*|::1|'[::1]') url_is_local=1 ;;
    *)                           url_is_local=0 ;;
  esac

  case "$DATABASE_URL" in
    *sslmode=disable*|*sslmode=allow*|*sslmode=prefer*)
      if [ "$url_is_local" = 0 ]; then
        echo "  ! DATABASE_URL permits an unencrypted connection to $url_host." >&2
        echo "    Use sslmode=require or stronger." >&2
        exit 1
      fi
      ;;
    *sslmode=*) : ;;  # require/verify-ca/verify-full -- left alone
    *)
      # No sslmode in the URL. PGSSLMODE supplies the default without editing
      # the string, and an explicit sslmode in a future URL still wins over it.
      if [ "$url_is_local" = 0 ]; then
        export PGSSLMODE="${PGSSLMODE:-require}"
      fi
      ;;
  esac
elif [ -z "${MIGRATE_VIA_COMPOSE:-}" ]; then
  if [ -z "$DB_HOST" ] || [ "$DB_HOST" = "postgres" ]; then
    MIGRATE_VIA_COMPOSE=1
  else
    MIGRATE_VIA_COMPOSE=0
  fi
fi

# psql reads PGPASSWORD, which keeps the password off the command line and out
# of the process table.
export PGPASSWORD="${DB_PASSWORD:-}"

# -v ON_ERROR_STOP=1 is what makes a failed statement abort the script instead
# of psql shrugging and carrying on to the next one with a broken transaction.
psql_run() {
  if [ -n "$DATABASE_URL" ]; then
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 "$@"
  elif [ "$MIGRATE_VIA_COMPOSE" = "1" ]; then
    docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" exec -T \
      -e PGPASSWORD="$PGPASSWORD" postgres \
      psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 "$@"
  else
    psql "host=$DB_HOST port=$DB_PORT user=$DB_USER dbname=$DB_NAME sslmode=$DB_SSLMODE" \
      -v ON_ERROR_STOP=1 "$@"
  fi
}

# Quiet, unaligned, tuples-only: output that can be read by the shell.
psql_value() { psql_run -qtAX -c "$1"; }

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# --- the ledger --------------------------------------------------------------
#
# checksum is stored so an edit to an already-applied migration is caught. That
# is the quiet failure mode of every home-grown migration runner: someone fixes
# a typo in 003, it is already recorded as applied, the fix never runs, and the
# database silently disagrees with the file that claims to describe it.

ensure_ledger() {
  # Suppressed only here: this runs on every invocation and its "relation
  # already exists, skipping" is noise that trains the reader to ignore
  # NOTICEs. Migrations themselves are left at the default so theirs show.
  psql_run -qX <<'SQL'
SET client_min_messages = warning;
CREATE TABLE IF NOT EXISTS schema_migrations (
    version     TEXT PRIMARY KEY,
    checksum    TEXT NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
COMMENT ON TABLE schema_migrations IS
    'Applied migrations. Maintained by deploy/migrate.sh; do not edit by hand.';
SQL
}

is_applied() {
  [ "$(psql_value "SELECT 1 FROM schema_migrations WHERE version = '$1'")" = "1" ]
}

recorded_checksum() {
  psql_value "SELECT checksum FROM schema_migrations WHERE version = '$1'"
}

# --- applying one file -------------------------------------------------------
#
# 001 has no transaction control of its own; 002-007 open with BEGIN; and end
# with COMMIT;. Both must end up as ONE transaction that also carries the ledger
# row, so the file's own BEGIN/COMMIT lines are stripped and replaced by ours.
# Without that, the file's COMMIT would close the transaction and the ledger
# INSERT would land in a separate one -- leaving the door open to schema applied
# but not recorded, which is the state that breaks the very next deploy.
#
# Safe here because every migration was checked for this: exactly one BEGIN; and
# one COMMIT; at line start, no SAVEPOINT, no ROLLBACK, and nothing that refuses
# to run inside a transaction (no CREATE INDEX CONCURRENTLY).

apply_one() {
  file="$1"; version="$2"; sum="$3"

  # Reject anything the strip-and-wrap above would mishandle, rather than
  # discovering it against a production database.
  if grep -qiE '^[[:space:]]*(SAVEPOINT|ROLLBACK)' "$file"; then
    echo "  ! $version contains SAVEPOINT/ROLLBACK; apply it by hand" >&2
    exit 1
  fi
  if grep -qiE 'CONCURRENTLY' "$file"; then
    echo "  ! $version uses CONCURRENTLY and cannot run in a transaction" >&2
    exit 1
  fi

  {
    echo "BEGIN;"
    grep -vE '^[[:space:]]*(BEGIN|COMMIT);[[:space:]]*$' "$file"
    echo "INSERT INTO schema_migrations (version, checksum) VALUES ('$version', '$sum');"
    echo "COMMIT;"
  } | psql_run -qX
}

# --- main --------------------------------------------------------------------

ensure_ledger

pending=0
applied=0

for file in "$MIGRATIONS_DIR"/*.sql; do
  version=$(basename "$file" .sql)
  sum=$(checksum "$file")

  if is_applied "$version"; then
    have=$(recorded_checksum "$version")
    if [ "$have" != "$sum" ]; then
      echo "  ! $version was applied but the file has changed since" >&2
      echo "      recorded $have" >&2
      echo "      on disk  $sum" >&2
      echo "    The database does not match this file. Write a NEW migration" >&2
      echo "    with the correction -- editing an applied one cannot fix it." >&2
      exit 1
    fi
    [ "$MODE" = status ] && echo "  applied  $version"
    continue
  fi

  pending=$((pending + 1))

  case "$MODE" in
    status|dryrun)
      echo "  PENDING  $version"
      ;;
    baseline)
      psql_run -qX -c \
        "INSERT INTO schema_migrations (version, checksum) VALUES ('$version', '$sum')"
      echo "  baselined $version (not executed)"
      applied=$((applied + 1))
      ;;
    apply)
      echo "  applying $version"
      apply_one "$file" "$version" "$sum"
      applied=$((applied + 1))
      ;;
  esac
done

case "$MODE" in
  status)   echo "$pending pending" ;;
  dryrun)   echo "$pending would be applied" ;;
  baseline) echo "$applied recorded as already applied; nothing was executed" ;;
  apply)
    if [ "$applied" -eq 0 ]; then
      echo "schema is up to date"
    else
      echo "$applied migration(s) applied"
    fi
    ;;
esac
