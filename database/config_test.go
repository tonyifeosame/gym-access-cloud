package database

import "testing"

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
	want := "postgres://u:p@dpg-abc.oregon-postgres.render.com/appdb?sslmode=require"
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
		if got != in {
			t.Errorf("sslmode=%s: got %q, want it unchanged", mode, got)
		}
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
	if got != "postgres://u:p@db.example.com/appdb?sslmode=require" {
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
	want := "host=localhost port=5432 user=at_admin password=secret dbname=access_terminal sslmode=disable"
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
