package database

import (
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

var DB *sql.DB

// Config holds database configuration
type Config struct {
	// URL is a libpq connection URI (postgres://user:pass@host:port/dbname).
	// When set it wins over every discrete field below.
	//
	// Managed PostgreSQL hands out one string and nothing else -- Render, RDS,
	// Neon all do -- and splitting it back into host/port/user/password loses
	// the query parameters, which is where sslmode lives. A password containing
	// a character the key/value DSN format treats specially is the other half of
	// the problem: it has to be quoted there and does not in a URI.
	URL string

	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	// SSLMode is passed straight to the driver. Kept configurable because a
	// deployment where the database is across a network needs `require` or
	// stronger, and one where it is a local socket cannot use it at all.
	SSLMode string
	// MaxOpenConns bounds connections to the database. Unbounded is the
	// dangerous default: a fleet of terminals all polling at once will open a
	// connection per in-flight request until PostgreSQL refuses new ones, and
	// then every request fails rather than queueing behind a busy pool.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open establishes an INDEPENDENT pool against cfg and returns it.
//
// Same connection rules as Connect -- TLS defaulting, the pinned session time
// zone, the pool bounds -- but the caller owns the handle and must Close it.
// Connect is the process-wide one; this is for anything that needs a second
// connection without disturbing it, such as a migration or maintenance task
// operating on a different database on the same server.
//
// The connection string is deliberately not returned. It carries the password,
// and Target() exists for anything that needs to name what was connected to.
func Open(cfg Config) (*sql.DB, error) {
	connStr, err := cfg.connString()
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	// Recycling connections keeps a long-lived process from holding one that a
	// restarted database or a failed-over replica no longer considers valid.
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("error connecting to database: %w", err)
	}
	return db, nil
}

// Connect establishes the process-wide connection to PostgreSQL.
func Connect(cfg Config) error {
	db, err := Open(cfg)
	if err != nil {
		return err
	}
	DB = db

	log.Printf("Connected to PostgreSQL %s (max_open_conns=%d, timezone=%s)",
		cfg.Target(), cfg.MaxOpenConns, sessionTimeZone)
	return nil
}

// sessionTimeZone is pinned on every connection this pool opens.
//
// The columns are TIMESTAMPTZ as of migration 010, so they hold instants and the
// session zone cannot change what a stored value MEANS. It changes two things
// that still matter:
//
//   - What lib/pq attaches to a time.Time on the way out. It hands back a value
//     located in the session's zone, and encoding/json renders that location
//     verbatim -- so an unpinned connection to a server in Africa/Lagos emits
//     "2026-08-14T18:00:00+01:00" where a UTC one emits "2026-08-14T17:00:00Z".
//     Both name the same instant and both are valid RFC3339, but the API's
//     JSON should not change shape because somebody moved the database. Pinning
//     UTC keeps the wire format exactly what it has always been.
//   - How a naive timestamp STRING is read when it is compared against one of
//     these columns -- `?since=2026-08-14T09:00:00` on the member-changes
//     endpoint, say. Postgres resolves an unqualified literal in the session
//     zone, so pinning UTC makes "no offset" mean UTC rather than meaning
//     whatever the database host happens to be set to.
//
// Forced rather than defaulted. A deployment does not get to opt out of this,
// because the API's documented output format depends on it.
//
// WHY THIS IS NOT A SUBSTITUTE FOR MIGRATION 010. Pinning UTC over the old naive
// columns would have made reads and writes through THIS POOL correct, which is
// tempting and was measured to be true. It is not enough:
//
//   - it does nothing for rows already written, which stay wrong by the old
//     offset forever;
//   - it protects only this pool. psql, a backup being restored, a reporting or
//     BI connection, deploy/migrate.sh -- none of them are pinned, and against a
//     naive column each of them silently reads and writes a different instant;
//   - it leaves the value itself ambiguous, so being right depends permanently
//     on every future caller remembering to pin. One that forgets is not an
//     error anywhere; it is just quietly an hour out.
//
// The column type is what makes the value mean one thing. This pin only settles
// how it is spelled on the way out.
const sessionTimeZone = "UTC"

// connString renders the configuration into something sql.Open understands.
func (c Config) connString() (string, error) {
	if c.URL == "" {
		return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s timezone=%s",
			c.Host, c.Port, c.User, c.Password, c.DBName, c.SSLMode, sessionTimeZone), nil
	}
	return normalizeDatabaseURL(c.URL)
}

// Target describes what is being connected to, WITHOUT the password. Used in
// the startup log line and by the test suite to name the server it reached.
func (c Config) Target() string {
	if c.URL == "" {
		return fmt.Sprintf("postgres://%s@%s:%s/%s (sslmode=%s)",
			c.User, c.Host, c.Port, c.DBName, c.SSLMode)
	}

	u, err := url.Parse(c.URL)
	if err != nil {
		return "DATABASE_URL (unparseable)"
	}
	// Drop the password rather than masking it: a mask still reveals the length,
	// and this string goes to the log where it is kept.
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	return u.Redacted()
}

// WithDatabase points the configuration at a different database on the same
// server.
//
// The test suite switches between the maintenance database and the throwaway
// one it builds, and it did that by assigning to DBName. In URL mode that field
// is not read at all -- so without this, a developer with DATABASE_URL set
// would have the suite create its scratch database and then run every
// destructive fixture against whatever DATABASE_URL actually points at.
func (c Config) WithDatabase(name string) Config {
	c.DBName = name
	if c.URL == "" {
		return c
	}

	u, err := url.Parse(c.URL)
	if err != nil {
		// Left as-is on purpose: Connect is about to fail on the same URL with a
		// message that says what is wrong with it. Reporting it twice, from a
		// function that cannot return an error, would only obscure that.
		return c
	}
	u.Path = "/" + name
	c.URL = u.String()
	return c
}

// sslModes that permit an unencrypted connection. `prefer` is the libpq default
// and is the dangerous one: it tries TLS, silently accepts plaintext when the
// server declines, and reports success either way.
var insecureSSLModes = map[string]bool{
	"disable": true,
	"allow":   true,
	"prefer":  true,
}

// normalizeDatabaseURL validates a connection URI and defaults its TLS mode.
//
// Two things happen here, both about the same failure. A managed database is
// reached over a network the deployment does not control, and the credential
// crossing it is the one that reads every member record and every device key
// hash. So:
//
//   - a URI with no sslmode gets `require`. Render's connection strings carry
//     no sslmode, and libpq's default without one is `prefer`, which downgrades
//     to plaintext without saying so.
//   - a URI that asks for a downgrade is REJECTED, not honoured with a warning,
//     unless the host is loopback -- where there is no network to intercept and
//     a local development database usually has TLS switched off.
func normalizeDatabaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("DATABASE_URL is not a valid URL: %w", err)
	}

	switch u.Scheme {
	case "postgres", "postgresql":
	default:
		return "", fmt.Errorf("DATABASE_URL must be a postgres:// URL, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("DATABASE_URL has no host")
	}

	q := u.Query()
	switch mode := q.Get("sslmode"); {
	case mode == "":
		q.Set("sslmode", "require")
	case insecureSSLModes[mode] && !isLoopback(u.Hostname()):
		return "", fmt.Errorf(
			"DATABASE_URL sets sslmode=%s for host %q, which allows an unencrypted "+
				"connection to a remote database; use sslmode=require or stronger",
			mode, u.Hostname())
	}

	// Overwritten, not defaulted -- see sessionTimeZone. A managed provider's
	// connection string never carries this, and one that did would be choosing
	// the API's JSON output format on its behalf.
	q.Set("timezone", sessionTimeZone)

	u.RawQuery = q.Encode()

	return u.String(), nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

// Close closes the database connection
func Close() {
	if DB != nil {
		DB.Close()
	}
}

// Pool defaults. Sized for a fleet of terminals polling on an interval rather
// than for sustained concurrent load: requests are short, so a modest pool with
// queueing behind it behaves better than a large one that lets every caller
// open its own connection.
const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 5
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
)

// GetConfigFromEnv reads database configuration from environment variables.
//
// DATABASE_URL, when present, supersedes the DB_* variables entirely. That is
// the shape every managed provider hands out, and Render's blueprint wires it
// in automatically from the database it provisions.
//
// DB_SSLMODE defaults to `disable` to preserve the behaviour every existing
// deployment and the bundled docker-compose already rely on. It is called out
// in the README because a database reached over a network needs `require`.
// Note that the DATABASE_URL path does NOT inherit that default -- see
// normalizeDatabaseURL, which requires TLS for anything that is not loopback.
func GetConfigFromEnv() Config {
	return Config{
		URL:             strings.TrimSpace(os.Getenv("DATABASE_URL")),
		Host:            getEnv("DB_HOST", "localhost"),
		Port:            getEnv("DB_PORT", "5432"),
		User:            getEnv("DB_USER", "at_admin"),
		Password:        getEnv("DB_PASSWORD", ""),
		DBName:          getEnv("DB_NAME", "access_terminal"),
		SSLMode:         getEnv("DB_SSLMODE", "disable"),
		MaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", defaultMaxOpenConns),
		MaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", defaultMaxIdleConns),
		ConnMaxLifetime: getEnvSeconds("DB_CONN_MAX_LIFETIME_SECONDS", defaultConnMaxLifetime),
		ConnMaxIdleTime: getEnvSeconds("DB_CONN_MAX_IDLE_SECONDS", defaultConnMaxIdleTime),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return v
		}
	}
	return defaultValue
}

func getEnvSeconds(key string, defaultValue time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return defaultValue
}
