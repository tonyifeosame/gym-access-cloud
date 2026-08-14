package database

import (
	"strings"
	"testing"
)

// Configuration tests.
//
// These need no PostgreSQL: they cover the code that decides WHAT to connect
// to, which runs before any connection is attempted. That matters here because
// the integration suite in the repository root is skipped whenever a database
// is unavailable, and the rule this file protects -- that a remote database is
// never reached in plaintext -- must not be part of what gets skipped.

func TestNormalizeDatabaseURLDefaultsToRequiredTLS(t *testing.T) {
	// Render hands out a URL with no sslmode. libpq's default without one is
	// `prefer`, which downgrades to plaintext without reporting it.
	got, err := normalizeDatabaseURL("postgres://u:p@dpg-abc.oregon-postgres.render.com/appdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "postgres://u:p@dpg-abc.oregon-postgres.render.com/appdb?sslmode=require&timezone=UTC"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeDatabaseURLKeepsStrongerModes(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		in := "postgres://u:p@db.example.com:5432/appdb?sslmode=" + mode
		got, err := normalizeDatabaseURL(in)
		if err != nil {
			t.Errorf("sslmode=%s: unexpected error: %v", mode, err)
			continue
		}
		// Unchanged apart from the pinned time zone, which is added to every
		// URL -- see TestConnStringPinsUTC.
		want := in + "&timezone=UTC"
		if got != want {
			t.Errorf("sslmode=%s: got %q, want %q", mode, got, want)
		}
	}
}

// TestConnStringPinsUTC covers the second half of the timestamp fix.
//
// TIMESTAMPTZ makes a stored value mean one instant; this decides how it is
// spelled coming back out. lib/pq returns a time.Time located in the session's
// zone and encoding/json renders that location verbatim, so an unpinned
// connection to a server in Africa/Lagos would emit "+01:00" offsets where this
// API has always emitted "Z". The pin keeps the wire format independent of where
// the database happens to sit.
func TestConnStringPinsUTC(t *testing.T) {
	// Both DSN shapes. The key/value form is used by the DB_* variables; the URL
	// form by every managed provider.
	for _, cfg := range []Config{
		{Host: "localhost", Port: "5432", User: "at_admin", DBName: "db", SSLMode: "disable"},
		{URL: "postgres://u:p@db.example.com/appdb?sslmode=require"},
	} {
		got, err := cfg.connString()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(got, "timezone=UTC") {
			t.Errorf("connection string does not pin the session time zone: %q", got)
		}
	}

	// A URL that names its own zone is OVERRIDDEN, not honoured. The API's
	// documented output format depends on this, so it is not a deployment's
	// choice to make.
	got, err := normalizeDatabaseURL("postgres://u:p@db.example.com/appdb?sslmode=require&timezone=Africa%2FLagos")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "Lagos") {
		t.Errorf("a time zone in DATABASE_URL was honoured; it must be overridden: %q", got)
	}
	if !strings.Contains(got, "timezone=UTC") {
		t.Errorf("got %q, want the time zone forced to UTC", got)
	}
}

func TestNormalizeDatabaseURLRejectsRemoteDowngrade(t *testing.T) {
	for _, mode := range []string{"disable", "allow", "prefer"} {
		_, err := normalizeDatabaseURL("postgres://u:p@db.example.com/appdb?sslmode=" + mode)
		if err == nil {
			t.Errorf("sslmode=%s against a remote host was accepted; it permits "+
				"an unencrypted connection and must be refused", mode)
		}
	}
}

func TestNormalizeDatabaseURLAllowsLoopbackDowngrade(t *testing.T) {
	// A local development database usually has TLS switched off, and there is no
	// network between the two processes to intercept.
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if _, err := normalizeDatabaseURL("postgres://u:p@" + host + ":5432/appdb?sslmode=disable"); err != nil {
			t.Errorf("host %s: unexpected error: %v", host, err)
		}
	}
}

func TestNormalizeDatabaseURLRejectsNonPostgres(t *testing.T) {
	for _, in := range []string{
		"mysql://u:p@db.example.com/appdb",
		"http://db.example.com/appdb",
		"postgres:///appdb", // no host
	} {
		if _, err := normalizeDatabaseURL(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestConnStringPrefersURL(t *testing.T) {
	cfg := Config{
		URL:      "postgres://u:p@db.example.com/appdb",
		Host:     "localhost",
		User:     "at_admin",
		DBName:   "access_terminal",
		SSLMode:  "disable",
		Port:     "5432",
		Password: "secret",
	}
	got, err := cfg.connString()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "postgres://u:p@db.example.com/appdb?sslmode=require&timezone=UTC" {
		t.Errorf("URL was not preferred over the DB_* fields: %q", got)
	}
}

func TestConnStringFallsBackToDiscreteFields(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: "5432", User: "at_admin",
		Password: "secret", DBName: "access_terminal", SSLMode: "disable",
	}
	got, err := cfg.connString()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "host=localhost port=5432 user=at_admin password=secret dbname=access_terminal sslmode=disable timezone=UTC"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWithDatabaseRewritesURLPath(t *testing.T) {
	// The guard that keeps the integration suite from running its destructive
	// fixtures against whatever DATABASE_URL points at.
	cfg := Config{URL: "postgres://u:p@db.example.com/appdb?sslmode=require"}
	got := cfg.WithDatabase("access_terminal_test")

	want := "postgres://u:p@db.example.com/access_terminal_test?sslmode=require"
	if got.URL != want {
		t.Errorf("URL: got %q, want %q", got.URL, want)
	}
	if got.DBName != "access_terminal_test" {
		t.Errorf("DBName: got %q", got.DBName)
	}
	if cfg.URL != "postgres://u:p@db.example.com/appdb?sslmode=require" {
		t.Errorf("the receiver was mutated: %q", cfg.URL)
	}
}

func TestTargetOmitsPassword(t *testing.T) {
	// This string goes to the log, where it is kept.
	cfg := Config{URL: "postgres://u:hunter2@db.example.com:5432/appdb?sslmode=require"}
	got := cfg.Target()
	if got != "postgres://u@db.example.com:5432/appdb?sslmode=require" {
		t.Errorf("got %q", got)
	}

	cfg = Config{Host: "localhost", Port: "5432", User: "at_admin",
		Password: "hunter2", DBName: "access_terminal", SSLMode: "disable"}
	if got := cfg.Target(); got != "postgres://at_admin@localhost:5432/access_terminal (sslmode=disable)" {
		t.Errorf("got %q", got)
	}
}
