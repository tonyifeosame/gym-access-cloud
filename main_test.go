package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// Integration test harness.
//
// These tests run against a real PostgreSQL database rather than a mock. The
// behaviour worth protecting here lives in SQL -- the tenant filters, the
// partial unique indexes, the ack constraint, SKIP LOCKED delivery -- and a
// mocked database layer would assert that the Go code calls queries, not that
// the queries are correct. The sync engine bugs this suite covers were all in
// the SQL.
//
// The database is created from migrations/ on every run, so the suite doubles
// as the check that a fresh database can be built from zero.
//
// Configuration comes from the same DB_* variables the server uses, except
// DB_NAME: the tests create and drop their own database (TEST_DB_NAME, default
// access_terminal_test) so they can never touch a real one. A .env in the repo
// root is loaded automatically, so `go test` and `go run` agree on which server
// they are talking to. Set TEST_DB_SKIP=1 to skip when no PostgreSQL is
// available -- which leaves the tenancy and sync SQL entirely uncovered.

const defaultTestDB = "access_terminal_test"

// runRecordFile records which server the suite last connected to, and when. It
// is the durable proof that a run really reached PostgreSQL: `go test` hides a
// passing package's stdout, so the banner alone is not visible without -v.
const runRecordFile = ".integration-run"

// runRecord is the content written to runRecordFile by this process
var runRecord string

// testEnv is the fixture shared by every test in the suite
type testEnv struct {
	t        *testing.T
	router   *gin.Engine
	siteAKey string // Site A, company 1
	siteBKey string // Site B, company 1 -- different site, same tenant
	siteCKey string // Site C, company 2 -- different tenant
}

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)

	// The suite reads its database configuration the same way the server does,
	// but it must not depend on main() having loaded .env: main() never runs in
	// a test binary, so the godotenv.Load() there is invisible from here. That
	// gap is what silently pointed the tests at the DB_USER default while the
	// server used the value in .env. Loading it explicitly closes it.
	//
	// Load never overwrites a variable that is already set, so an explicit
	// DB_USER=... on the command line still wins, and production is unaffected:
	// nothing about main()'s own configuration path changes.
	if err := godotenv.Load(); err == nil {
		fmt.Println("integration tests: loaded .env from the repo root")
	}

	if os.Getenv("TEST_DB_SKIP") != "" {
		fmt.Println("======================================================================")
		fmt.Println("TEST_DB_SKIP is set: the integration tests were SKIPPED, not run.")
		fmt.Println("The tenancy, sync and migration SQL has NO coverage in this run.")
		fmt.Println("======================================================================")
		os.Exit(0)
	}

	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nintegration tests could not start: %v\n\n"+
			"These are integration tests against a real PostgreSQL instance; there is\n"+
			"no mock to fall back to, so this is a failure and not a skip.\n\n"+
			"Point DB_HOST/DB_PORT/DB_USER/DB_PASSWORD at a server (a .env in the repo\n"+
			"root is picked up automatically) and re-run:\n\n"+
			"    go test -count=1 ./...\n\n"+
			"The account needs CREATEDB: the suite builds its own database from\n"+
			"migrations/ on every run and drops it afterwards.\n\n"+
			"TEST_DB_SKIP=1 skips these deliberately, leaving the tenancy and sync SQL\n"+
			"uncovered.\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// TestSuiteConnectedToPostgres records, on disk, which server this run actually
// reached -- and fails if it reached none.
//
// It exists because a passing `go test` swallows package stdout, so the banner
// runSuite prints is invisible without -v. After any run, .integration-run says
// what was really talked to, which is the difference between "the suite passed"
// and "the suite passed against something".
//
// It is NOT a defence against the test cache. `go test` caches a passing package
// result and replays it whenever its key matches, and nothing in that key
// describes the database -- the DB_* variables are not even part of it, because
// the go command re-reads recorded variables from its own environment and
// runSuite resolves them before m.Run() starts. A pass banked while PostgreSQL
// was reachable is therefore replayed unchanged after it goes away. Defeating
// that from inside the binary was tried and abandoned: the techniques that work
// in a synthetic module did not hold here, and a guard that works by accident is
// worse than none. Use -count=1, or `make test`, which is what the README and
// the startup failure message both point at.
func TestSuiteConnectedToPostgres(t *testing.T) {
	if runRecord == "" {
		t.Fatal("run record is empty, so runSuite established no connection")
	}
	if err := os.WriteFile(runRecordFile, []byte(runRecord), 0o644); err != nil {
		t.Fatalf("writing %s: %v", runRecordFile, err)
	}
}

// runSuite builds a database from the migrations, runs the tests, and drops it
func runSuite(m *testing.M) (int, error) {
	cfg := database.GetConfigFromEnv()
	testDB := envOr("TEST_DB_NAME", defaultTestDB)

	// Connect to the maintenance database to create the test one.
	//
	// WithDatabase, not an assignment to DBName: DBName is ignored when the
	// configuration came from DATABASE_URL, and a suite that quietly stayed on
	// the URL's database would run its destructive fixtures against it.
	adminCfg := cfg.WithDatabase(envOr("TEST_ADMIN_DB", "postgres"))
	if err := database.Connect(adminCfg); err != nil {
		// Naming the target matters: the failure that hid behind this line was a
		// user mismatch, and "authentication failed" alone reads like a password
		// problem rather than a configuration one.
		return 0, fmt.Errorf("connecting to %s: %w", describeTarget(adminCfg), err)
	}

	// A leftover database from an interrupted run must not silently become the
	// fixture for this one.
	if _, err := database.DB.Exec("DROP DATABASE IF EXISTS " + quoteIdent(testDB)); err != nil {
		return 0, fmt.Errorf("dropping old test database: %w", err)
	}
	if _, err := database.DB.Exec("CREATE DATABASE " + quoteIdent(testDB)); err != nil {
		return 0, fmt.Errorf("creating test database: %w", err)
	}
	database.Close()

	cfg = cfg.WithDatabase(testDB)
	if err := database.Connect(cfg); err != nil {
		return 0, fmt.Errorf("connecting to %s: %w", describeTarget(cfg), err)
	}

	// Report what the suite is actually talking to. A run that prints this line
	// reached a real server; a run that does not, did not.
	var serverVersion, whoami, dbName string
	if err := database.DB.QueryRow(
		`SELECT version(), current_user, current_database()`).
		Scan(&serverVersion, &whoami, &dbName); err != nil {
		return 0, fmt.Errorf("verifying connection to %s: %w", describeTarget(cfg), err)
	}
	fmt.Printf("integration tests: connected to %s as %q, database %q\n",
		truncate(serverVersion, 60), whoami, dbName)

	// Describe the connection that actually happened.
	// TestSuiteConnectedToPostgres writes this to disk.
	runRecord = fmt.Sprintf("%d %s\n", time.Now().UnixNano(), describeTarget(cfg))

	if err := applyMigrations(); err != nil {
		return 0, fmt.Errorf("applying migrations: %w", err)
	}

	code := m.Run()

	database.Close()
	if err := database.Connect(adminCfg); err == nil {
		_, _ = database.DB.Exec("DROP DATABASE IF EXISTS " + quoteIdent(testDB))
		database.Close()
	}
	return code, nil
}

// applyMigrations runs every migration in filename order, which is the same
// order docker-entrypoint-initdb.d uses.
func applyMigrations() error {
	files, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations found")
	}
	sort.Strings(files)

	for _, file := range files {
		sqlText, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := database.DB.Exec(string(sqlText)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
	}
	return nil
}

// describeTarget renders a connection target without its password, so a failure
// says which server, user and database were tried.
func describeTarget(c database.Config) string {
	return c.Target()
}

// truncate keeps the connection banner to one readable line
func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// newTestEnv gives each test its own tenants, sites and clean tables.
//
// Truncating between tests rather than sharing fixtures keeps one test's
// devices and sync jobs from changing another's backlog counts.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	_, err := database.DB.Exec(`
		TRUNCATE sync_jobs, access_logs, enrollment_requests, people,
		         devices, doors, firmware_versions, sites, companies
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("resetting tables: %v", err)
	}

	env := &testEnv{
		t:        t,
		router:   NewRouter(),
		siteAKey: "test-site-a-key",
		siteBKey: "test-site-b-key",
		siteCKey: "test-site-c-key",
	}

	var companyOne, companyTwo int64
	mustScan(t, `INSERT INTO companies (name, slug) VALUES ('Company One', 'one') RETURNING id`, &companyOne)
	mustScan(t, `INSERT INTO companies (name, slug) VALUES ('Company Two', 'two') RETURNING id`, &companyTwo)

	// Site keys are stored HASHED, exactly as a real one is since
	// 011_site_credentials.sql. The fixture keeps the plaintext in env.site*Key
	// because the tests present it in X-API-Key -- which is the point: the wire
	// contract did not change when the storage did, and a fixture that wrote
	// plaintext would be testing a database shape the server no longer has.
	seedSite(t, companyOne, "Site A", env.siteAKey)
	seedSite(t, companyOne, "Site B", env.siteBKey)
	seedSite(t, companyTwo, "Site C", env.siteCKey)

	return env
}

// seedSite inserts a site whose provisioning key is `key`, stored the way the
// server stores one.
func seedSite(t *testing.T, companyID int64, name, key string) {
	t.Helper()
	mustExec(t, `INSERT INTO sites (company_id, site_name, api_key_hash, api_key_prefix, active)
	             VALUES ($1, $2, $3, $4, TRUE)`,
		companyID, name, database.HashSiteKey(key), truncateKeyPrefix(key))
}

func truncateKeyPrefix(key string) string {
	if len(key) < 12 {
		return key
	}
	return key[:12]
}

// siteIDByKey resolves a site from the plaintext key a test holds, hashing it
// the same way authentication does.
func siteIDByKey(t *testing.T, key string) int64 {
	t.Helper()
	var id int64
	if err := database.DB.QueryRow(
		`SELECT id FROM sites WHERE api_key_hash = $1`, database.HashSiteKey(key)).Scan(&id); err != nil {
		t.Fatalf("resolving site for key: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

// response is a decoded API reply
type response struct {
	Code int
	Body map[string]any
	Raw  string
}

// list decodes a response whose body is a JSON array
func (e *testEnv) list(method, path string, headers map[string]string) (int, []map[string]any) {
	e.t.Helper()
	rec := e.raw(method, path, nil, headers)

	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("%s %s: decoding array: %v (body %s)", method, path, err, rec.Body.String())
	}
	return rec.Code, out
}

// do issues a request and decodes a JSON object response
func (e *testEnv) do(method, path string, body any, headers map[string]string) response {
	e.t.Helper()
	rec := e.raw(method, path, body, headers)

	out := response{Code: rec.Code, Raw: rec.Body.String()}
	if rec.Body.Len() > 0 {
		// A body that is not an object (an array, say) leaves Body nil rather
		// than failing: some callers only care about the status.
		_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
	}
	return out
}

func (e *testEnv) raw(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("encoding request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// siteAuth is the header set for a site API key
func siteAuth(key string) map[string]string { return map[string]string{"X-API-Key": key} }

// deviceAuth is the header set for a per-device credential
func deviceAuth(key string) map[string]string { return map[string]string{"X-Device-Key": key} }

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// registerDevice enrols a terminal and returns its credential
func (e *testEnv) registerDevice(siteKey, serial string) string {
	e.t.Helper()

	res := e.do(http.MethodPost, "/api/v1/devices/register",
		map[string]any{"serial_number": serial}, siteAuth(siteKey))
	if res.Code != http.StatusCreated {
		e.t.Fatalf("registering %s: got %d, want 201 (body %s)", serial, res.Code, res.Raw)
	}

	key, _ := res.Body["api_key"].(string)
	if key == "" {
		e.t.Fatalf("registering %s: response carried no api_key (body %s)", serial, res.Raw)
	}
	return key
}

// createMember adds a person to the tenant behind siteKey
func (e *testEnv) createMember(siteKey, memberID, name string) {
	e.t.Helper()

	res := e.do(http.MethodPost, "/api/v1/members", map[string]any{
		"member_id": memberID, "full_name": name,
		"membership_type": "PREMIUM", "active": true,
	}, siteAuth(siteKey))
	if res.Code != http.StatusCreated {
		e.t.Fatalf("creating member %s: got %d, want 201 (body %s)", memberID, res.Code, res.Raw)
	}
}

// jobs fetches a device's due work
func (e *testEnv) jobs(deviceKey string) []map[string]any {
	e.t.Helper()

	res := e.do(http.MethodGet, "/api/v1/devices/jobs?limit=200", nil, deviceAuth(deviceKey))
	if res.Code != http.StatusOK {
		e.t.Fatalf("fetching jobs: got %d, want 200 (body %s)", res.Code, res.Raw)
	}

	raw, _ := res.Body["jobs"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if job, ok := item.(map[string]any); ok {
			out = append(out, job)
		}
	}
	return out
}

// jobTypes summarises a batch for assertions
func jobTypes(jobs []map[string]any) []string {
	types := make([]string, 0, len(jobs))
	for _, job := range jobs {
		t, _ := job["job_type"].(string)
		types = append(types, t)
	}
	return types
}

func jobID(t *testing.T, job map[string]any) int64 {
	t.Helper()
	id, ok := job["id"].(float64)
	if !ok {
		t.Fatalf("job carried no numeric id: %v", job)
	}
	return int64(id)
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// jobStatus reads a job's stored status directly, for assertions about state
// the API deliberately does not expose.
func jobStatus(t *testing.T, id int64) string {
	t.Helper()
	var status string
	if err := database.DB.QueryRow(`SELECT status FROM sync_jobs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("reading status of job %d: %v", id, err)
	}
	return status
}

// jobRetryState reports a job's attempt count and whether it is currently
// backed off, which together describe how it will be redelivered.
func jobRetryState(t *testing.T, id int64) (attempts int, backedOff bool) {
	t.Helper()
	err := database.DB.QueryRow(
		`SELECT attempts, next_attempt_at > CURRENT_TIMESTAMP FROM sync_jobs WHERE id = $1`,
		id).Scan(&attempts, &backedOff)
	if err != nil {
		t.Fatalf("reading retry state of job %d: %v", id, err)
	}
	return attempts, backedOff
}

// deviceStatus reads a device's stored state
func deviceStatus(t *testing.T, serial string) string {
	t.Helper()
	var status string
	if err := database.DB.QueryRow(
		`SELECT status FROM devices WHERE serial_number = $1`, serial).Scan(&status); err != nil {
		t.Fatalf("reading status of device %s: %v", serial, err)
	}
	return status
}

// makeJobDue clears a job's delivery lease so the next poll offers it again,
// rather than making the test wait out the lease.
func makeJobDue(t *testing.T, id int64) {
	t.Helper()
	mustExec(t, `UPDATE sync_jobs SET next_attempt_at = CURRENT_TIMESTAMP - interval '1 hour' WHERE id = $1`, id)
}

// itoa formats a job id for a URL
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// queryInt runs a single-value integer query
func queryInt(t *testing.T, query string, args ...any) int {
	t.Helper()
	var out int
	if err := database.DB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// queryBool runs a single-value boolean query
func queryBool(t *testing.T, query string, args ...any) bool {
	t.Helper()
	var out bool
	if err := database.DB.QueryRow(query, args...).Scan(&out); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Small utilities
// ---------------------------------------------------------------------------

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// quoteIdent quotes a database name for DDL, which cannot be parameterised
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func mustExec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := database.DB.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustScan(t *testing.T, query string, dest ...any) {
	t.Helper()
	if err := database.DB.QueryRow(query).Scan(dest...); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
}
