package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Timestamp correctness (migration 010).
//
// Every timestamp column was TIMESTAMP WITHOUT TIME ZONE, which stores a
// wall-clock reading and no record of which clock took it. Three writers reached
// those columns from three different clocks -- CURRENT_TIMESTAMP from the
// database server, a Go time.Time from the API process, a device's RFC3339 `Z`
// from UTC -- and PostgreSQL discarded the offset on the way in, so they
// disagreed with each other. lib/pq then labelled everything UTC on the way out,
// so the API confidently reported an instant that was wrong by the database
// server's offset.
//
// These tests are the guard on all of that. They are deliberately mostly about
// AGREEMENT rather than about any particular value: the failure being prevented
// is two paths meaning different things by the same column, which no single
// assertion about one row would catch.

// tolerance for "these should be the same instant". Generous enough for a slow
// round trip, far tighter than any real time zone offset -- the smallest of
// which is 15 minutes.
const clockTolerance = 90 * time.Second

// ---------------------------------------------------------------------------
// The schema itself
// ---------------------------------------------------------------------------

// TestSchemaHasNoNaiveTimestamps is the regression guard.
//
// It is not really about migration 010, which is already proven by every other
// test here. It is about migration 014, or 020: a future ALTER TABLE that adds
// `last_reviewed_at TIMESTAMP` reintroduces the entire defect for that column
// only, silently, and nothing else in the suite would notice. This fails the
// moment such a column exists.
func TestSchemaHasNoNaiveTimestamps(t *testing.T) {
	rows, err := database.DB.Query(`
		SELECT c.table_name, c.column_name
		  FROM information_schema.columns c
		  JOIN information_schema.tables t
		    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = 'public'
		   AND t.table_type   = 'BASE TABLE'
		   AND c.data_type    = 'timestamp without time zone'
		 ORDER BY c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("querying the catalog: %v", err)
	}
	defer rows.Close()

	var naive []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		naive = append(naive, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating: %v", err)
	}

	if len(naive) > 0 {
		t.Errorf("these columns are TIMESTAMP WITHOUT TIME ZONE and must be TIMESTAMPTZ:\n  %s\n"+
			"A naive column stores a wall-clock reading with no record of which clock took it. "+
			"See migrations/010_timestamptz.sql.", strings.Join(naive, "\n  "))
	}

	// And prove the conversion actually happened rather than the table simply
	// being empty of timestamp columns altogether.
	var aware int
	if err := database.DB.QueryRow(`
		SELECT count(*) FROM information_schema.columns c
		  JOIN information_schema.tables t
		    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		   AND c.data_type = 'timestamp with time zone'`).Scan(&aware); err != nil {
		t.Fatalf("counting timestamptz columns: %v", err)
	}
	if aware < 50 {
		t.Errorf("only %d timestamptz columns; the schema should have every one of its "+
			"timestamps converted", aware)
	}
}

// TestPoolPinsUTC covers the other half of the fix.
//
// timestamptz stores an instant, so the session zone cannot change what a value
// MEANS -- but it decides what lib/pq attaches to the time.Time it returns, and
// encoding/json renders that location verbatim. Unpinned, an API on a server in
// Africa/Lagos would emit "+01:00" offsets where it has always emitted "Z".
func TestPoolPinsUTC(t *testing.T) {
	var zone string
	if err := database.DB.QueryRow(`SELECT current_setting('TimeZone')`).Scan(&zone); err != nil {
		t.Fatalf("reading session TimeZone: %v", err)
	}
	if zone != "UTC" {
		t.Errorf("pool session TimeZone = %q, want UTC", zone)
	}

	// The consequence that actually matters: a value read back is UTC-located,
	// so it serialises with a Z.
	var got time.Time
	if err := database.DB.QueryRow(`SELECT CURRENT_TIMESTAMP`).Scan(&got); err != nil {
		t.Fatalf("reading CURRENT_TIMESTAMP: %v", err)
	}
	if _, offset := got.Zone(); offset != 0 {
		t.Errorf("CURRENT_TIMESTAMP came back at offset %ds, want UTC", offset)
	}
	if formatted := got.Format(time.RFC3339); !strings.HasSuffix(formatted, "Z") {
		t.Errorf("serialised as %s, want a Z-suffixed instant", formatted)
	}
}

// ---------------------------------------------------------------------------
// The three writers must now agree
// ---------------------------------------------------------------------------

// TestWritePathsAgreeOnTheSameInstant is the core of this fix.
//
// Before 010 these three landed in the same column meaning three different
// things, and the gap between them was exactly the servers' UTC offsets -- an
// hour on the deployment this was found on. access_logs is the column where it
// mattered most: occurred_at is filled by the device when it has a clock and by
// the server when it does not, so a single site's audit trail was internally
// inconsistent and could order events wrongly.
func TestWritePathsAgreeOnTheSameInstant(t *testing.T) {
	env := newTestEnv(t)
	_ = env

	now := time.Now()

	var fromServer, fromGo, fromDeviceUTC time.Time
	err := database.DB.QueryRow(`
		SELECT CURRENT_TIMESTAMP, $1::timestamptz, $2::timestamptz`,
		now, now.UTC().Format(time.RFC3339Nano)).
		Scan(&fromServer, &fromGo, &fromDeviceUTC)
	if err != nil {
		t.Fatalf("comparing write paths: %v", err)
	}

	truth := now.UTC()
	for _, probe := range []struct {
		name string
		got  time.Time
	}{
		{"CURRENT_TIMESTAMP (database server clock)", fromServer},
		{"a Go time.Time parameter", fromGo},
		{"a device's RFC3339 UTC string", fromDeviceUTC},
	} {
		if delta := probe.got.Sub(truth); delta > clockTolerance || delta < -clockTolerance {
			t.Errorf("%s resolved to %s, which is %s away from the real instant %s",
				probe.name, probe.got.UTC().Format(time.RFC3339Nano), delta,
				truth.Format(time.RFC3339Nano))
		}
	}
}

// TestStoredTimestampsMatchTheInstantTheyClaim writes through the real code
// path and reads back through the real query, which is what a console screen
// will do.
//
// Before 010 this failed by the server's offset in the direction that matters
// most: created_at read back AHEAD of the wall clock, so a person created now
// appeared to have been created in the future.
func TestStoredTimestampsMatchTheInstantTheyClaim(t *testing.T) {
	env := newTestEnv(t)
	_ = env

	before := time.Now().UTC().Add(-clockTolerance)

	member := models.Member{
		MemberID:       "TS-1",
		FullName:       "Timestamp Probe",
		MembershipType: "STANDARD",
		Active:         true,
	}
	if err := database.CreateMember(operatorCompanyID(t, "one"), &member); err != nil {
		t.Fatalf("creating a person: %v", err)
	}

	stored, err := database.GetMemberByID(operatorCompanyID(t, "one"), "TS-1")
	if err != nil {
		t.Fatalf("reading the person back: %v", err)
	}

	after := time.Now().UTC().Add(clockTolerance)
	created := stored.CreatedAt.UTC()

	if created.Before(before) || created.After(after) {
		t.Errorf("created_at = %s, which is outside [%s, %s] -- the stored value does not "+
			"name the instant the row was written",
			created.Format(time.RFC3339Nano),
			before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}

	// The specific old symptom: a brand new row dated in the future.
	if created.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("created_at %s is in the future", created.Format(time.RFC3339Nano))
	}
}

// TestChangesSinceHonoursAnOffset covers the one query that compares a
// CLIENT-SUPPLIED string against one of these columns.
//
// Before 010 the offset in "…T09:00:00Z" was silently discarded, so a terminal
// asking a correctly-formed question got an answer computed against a different
// instant than the one it named. The two spellings of the same instant must
// select the same rows.
func TestChangesSinceHonoursAnOffset(t *testing.T) {
	newTestEnv(t)
	one := operatorCompanyID(t, "one")

	member := models.Member{
		MemberID: "TS-SINCE", FullName: "Since Probe",
		MembershipType: "STANDARD", Active: true,
	}
	if err := database.CreateMember(one, &member); err != nil {
		t.Fatalf("creating a person: %v", err)
	}

	cutoff := time.Now().UTC().Add(-time.Hour)

	// The same instant, spelled three ways a client might send it.
	spellings := map[string]string{
		"RFC3339 in UTC":         cutoff.Format(time.RFC3339),
		"RFC3339 at +01:00":      cutoff.In(time.FixedZone("test", 3600)).Format(time.RFC3339),
		"naive (implicitly UTC)": cutoff.Format("2006-01-02T15:04:05"),
	}
	for name, since := range spellings {
		changed, err := database.GetMembersChangedSince(one, since)
		if err != nil {
			t.Fatalf("%s (%q): %v", name, since, err)
		}
		if len(changed) != 1 {
			t.Errorf("%s (%q) returned %d people, want 1", name, since, len(changed))
		}
	}

	// And an instant AFTER the row was written must exclude it, in every
	// spelling -- otherwise the test above would pass on a query that ignores
	// the parameter entirely.
	future := time.Now().UTC().Add(time.Hour)
	for name, since := range map[string]string{
		"future RFC3339 in UTC":    future.Format(time.RFC3339),
		"future RFC3339 at -05:00": future.In(time.FixedZone("test", -5*3600)).Format(time.RFC3339),
	} {
		changed, err := database.GetMembersChangedSince(one, since)
		if err != nil {
			t.Fatalf("%s (%q): %v", name, since, err)
		}
		if len(changed) != 0 {
			t.Errorf("%s (%q) returned %d people, want none", name, since, len(changed))
		}
	}
}

// TestConsoleTimestampsSerialiseAsUTCInstants is the frontend-facing assertion:
// what a browser actually receives.
func TestConsoleTimestampsSerialiseAsUTCInstants(t *testing.T) {
	cheapBcrypt(t)
	env := newTestEnv(t)
	one := operatorCompanyID(t, "one")

	_, token, csrf := consoleOperatorSession(t, env.router, one, "ts@example.com", models.RoleOwner)

	code, body := consoleCall(t, env.router, "POST", "/api/v1/console/people",
		`{"external_id":"TS-API","full_name":"Api Probe"}`, token, csrf)
	if code != http.StatusCreated {
		t.Fatalf("creating a person = %d (%v)", code, body)
	}

	raw, ok := body["created_at"].(string)
	if !ok {
		t.Fatalf("created_at is not a string: %v", body["created_at"])
	}
	if !strings.HasSuffix(raw, "Z") {
		t.Errorf("created_at = %q, want a Z-suffixed UTC instant -- the wire format must not "+
			"change with the database server's zone", raw)
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("created_at %q is not RFC3339: %v", raw, err)
	}
	if delta := time.Since(parsed); delta > clockTolerance || delta < -clockTolerance {
		t.Errorf("created_at %q is %s away from now; the browser would render the wrong time",
			raw, delta)
	}
}

// ---------------------------------------------------------------------------
// Converting a database that already holds data
// ---------------------------------------------------------------------------

// TestMigrationConvertsExistingData is the one test that does not run against
// the suite's database.
//
// Every other test here proves 010 is right on a FRESH database, which is what
// the suite builds. That says nothing about the case the migration exists for:
// an existing deployment whose tables are full of wall-clock readings that have
// to be reinterpreted without being corrupted. So this builds a database at the
// pre-010 schema, writes values whose intended meaning is known exactly, runs
// the conversion, and checks the arithmetic.
//
// Both branches are covered, because they behave differently and the difference
// is a trap: the migration's default reads the connection's own TimeZone, and
// the API pins its connections to UTC -- so applied down that path the default
// is a no-op. The explicit override exists for exactly that, and the migration
// warns when it is converting real data without one.
func TestMigrationConvertsExistingData(t *testing.T) {
	cfg := database.GetConfigFromEnv()
	scratch := envOr("TEST_DB_NAME", defaultTestDB) + "_conv"

	admin, err := database.Open(cfg.WithDatabase(envOr("TEST_ADMIN_DB", "postgres")))
	if err != nil {
		t.Fatalf("connecting to the maintenance database: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch)); err != nil {
		t.Fatalf("dropping the scratch database: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + quoteIdent(scratch)); err != nil {
		t.Fatalf("creating the scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch))
	})

	db, err := database.Open(cfg.WithDatabase(scratch))
	if err != nil {
		t.Fatalf("connecting to the scratch database: %v", err)
	}
	defer db.Close()

	// The schema as it was BEFORE this fix: everything up to but excluding 010.
	if err := applyMigrationsTo(db, "009"); err != nil {
		t.Fatalf("building the pre-conversion schema: %v", err)
	}

	var naiveBefore int
	if err := db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema='public' AND data_type='timestamp without time zone'`).
		Scan(&naiveBefore); err != nil {
		t.Fatalf("counting naive columns: %v", err)
	}
	if naiveBefore == 0 {
		t.Fatal("the pre-010 schema has no naive timestamp columns; this test is not testing anything")
	}

	// A row whose stored wall-clock reading is known exactly. 09:30 was read off
	// a clock in Africa/Lagos, so it denotes 08:30 UTC.
	const legacyZone = "Africa/Lagos"
	const wallClock = "2026-01-15 09:30:00"
	const wantUTC = "2026-01-15T08:30:00Z"

	if _, err := db.Exec(`INSERT INTO companies (name, slug) VALUES ('Conv', 'conv')`); err != nil {
		t.Fatalf("seeding a company: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO people (company_id, external_id, full_name, membership_type, created_at, updated_at)
		SELECT id, 'P-CONV', 'Conv Person', 'STANDARD', $1::timestamp, $1::timestamp
		  FROM companies WHERE slug = 'conv'`, wallClock); err != nil {
		t.Fatalf("seeding a person: %v", err)
	}

	// Apply the conversion, naming the zone those readings were taken in. This
	// connection is UTC-pinned like every pool this package opens, which is
	// precisely why the override has to be given.
	migration, err := os.ReadFile(filepath.Join("migrations", "010_timestamptz.sql"))
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("SET accesslink.legacy_timezone = %s;\n%s",
		quoteLiteral(legacyZone), migration)); err != nil {
		t.Fatalf("applying 010: %v", err)
	}

	// Every column converted.
	var naiveAfter int
	if err := db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema='public' AND data_type='timestamp without time zone'`).
		Scan(&naiveAfter); err != nil {
		t.Fatalf("counting naive columns after: %v", err)
	}
	if naiveAfter != 0 {
		t.Errorf("%d naive column(s) survived the conversion", naiveAfter)
	}

	// The value now names the instant it always meant.
	var converted time.Time
	if err := db.QueryRow(
		`SELECT created_at FROM people WHERE external_id = 'P-CONV'`).Scan(&converted); err != nil {
		t.Fatalf("reading the converted value: %v", err)
	}
	if got := converted.UTC().Format(time.RFC3339); got != wantUTC {
		t.Errorf("converted created_at = %s, want %s (%s read in %s)",
			got, wantUTC, wallClock, legacyZone)
	}

	// Defaults survived. A conversion that silently dropped DEFAULT
	// CURRENT_TIMESTAMP would leave the next INSERT writing a NULL, which is a
	// NOT NULL violation on most of these columns.
	var def sql.NullString
	if err := db.QueryRow(`
		SELECT column_default FROM information_schema.columns
		 WHERE table_name = 'people' AND column_name = 'created_at'`).Scan(&def); err != nil {
		t.Fatalf("reading the column default: %v", err)
	}
	if !def.Valid || !strings.Contains(strings.ToUpper(def.String), "CURRENT_TIMESTAMP") {
		t.Errorf("created_at default = %v, want CURRENT_TIMESTAMP preserved", def)
	}

	// And the converted schema still works: a fresh insert lands at the right
	// instant, proving the re-applied default is functional and not just present.
	if _, err := db.Exec(`
		INSERT INTO people (company_id, external_id, full_name, membership_type)
		SELECT id, 'P-AFTER', 'After Person', 'STANDARD' FROM companies WHERE slug = 'conv'`); err != nil {
		t.Fatalf("inserting after the conversion: %v", err)
	}
	var fresh time.Time
	if err := db.QueryRow(
		`SELECT created_at FROM people WHERE external_id = 'P-AFTER'`).Scan(&fresh); err != nil {
		t.Fatalf("reading the fresh row: %v", err)
	}
	if delta := time.Since(fresh); delta > clockTolerance || delta < -clockTolerance {
		t.Errorf("a row inserted after the conversion is dated %s, %s away from now",
			fresh.UTC().Format(time.RFC3339Nano), delta)
	}
}

// TestMigrationDefaultsToTheConnectionTimeZone documents the trap rather than
// merely avoiding it.
//
// The migration's default is the applying connection's own zone. Down a
// UTC-pinned connection that means "assume the values were already UTC", which
// leaves them uncorrected -- correct if they were, wrong if they were not. The
// deploy path (psql, unpinned) reports the server's real zone and is right by
// default; this proves the other path behaves as documented, so nobody has to
// rediscover it against real data.
func TestMigrationDefaultsToTheConnectionTimeZone(t *testing.T) {
	cfg := database.GetConfigFromEnv()
	scratch := envOr("TEST_DB_NAME", defaultTestDB) + "_convdef"

	admin, err := database.Open(cfg.WithDatabase(envOr("TEST_ADMIN_DB", "postgres")))
	if err != nil {
		t.Fatalf("connecting to the maintenance database: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch)); err != nil {
		t.Fatalf("dropping the scratch database: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + quoteIdent(scratch)); err != nil {
		t.Fatalf("creating the scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(scratch)) })

	db, err := database.Open(cfg.WithDatabase(scratch))
	if err != nil {
		t.Fatalf("connecting to the scratch database: %v", err)
	}
	defer db.Close()

	if err := applyMigrationsTo(db, "009"); err != nil {
		t.Fatalf("building the pre-conversion schema: %v", err)
	}

	const wallClock = "2026-01-15 09:30:00"
	if _, err := db.Exec(`INSERT INTO companies (name, slug) VALUES ('Conv', 'conv')`); err != nil {
		t.Fatalf("seeding a company: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO people (company_id, external_id, full_name, membership_type, created_at, updated_at)
		SELECT id, 'P-DEF', 'Default Person', 'STANDARD', $1::timestamp, $1::timestamp
		  FROM companies WHERE slug = 'conv'`, wallClock); err != nil {
		t.Fatalf("seeding a person: %v", err)
	}

	migration, err := os.ReadFile(filepath.Join("migrations", "010_timestamptz.sql"))
	if err != nil {
		t.Fatalf("reading the migration: %v", err)
	}
	// No override: the migration falls back to this connection's zone, UTC.
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("applying 010: %v", err)
	}

	var converted time.Time
	if err := db.QueryRow(
		`SELECT created_at FROM people WHERE external_id = 'P-DEF'`).Scan(&converted); err != nil {
		t.Fatalf("reading the converted value: %v", err)
	}
	if got, want := converted.UTC().Format(time.RFC3339), "2026-01-15T09:30:00Z"; got != want {
		t.Errorf("with no override over a UTC-pinned connection, created_at = %s, want %s "+
			"(the reading taken as already-UTC)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// applyMigrationsTo runs every migration up to and including the given version
// prefix, against a caller-supplied handle.
//
// applyMigrations() in main_test.go runs all of them against the package global.
// This one exists so a test can build the schema as it stood BEFORE a particular
// migration, which is the only way to test what that migration does to data.
func applyMigrationsTo(db *sql.DB, lastVersion string) error {
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations found")
	}
	sort.Strings(files)

	applied := 0
	for _, file := range files {
		version := strings.SplitN(filepath.Base(file), "_", 2)[0]
		if version > lastVersion {
			break
		}
		sqlText, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := db.Exec(string(sqlText)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
		applied++
	}
	if applied == 0 {
		return fmt.Errorf("no migrations at or below %s", lastVersion)
	}
	return nil
}

// quoteLiteral renders a string as a SQL literal. Only ever called with
// constants from this file, but doubling the quote costs nothing and means the
// helper is not a hazard if it is reused.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
