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

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
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
// access_terminal_test) so they can never touch a real one. Set TEST_DB_SKIP=1
// to skip when no PostgreSQL is available.

const defaultTestDB = "access_terminal_test"

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

	if os.Getenv("TEST_DB_SKIP") != "" {
		fmt.Println("TEST_DB_SKIP set, skipping integration tests")
		os.Exit(0)
	}

	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nintegration tests could not start: %v\n\n"+
			"These tests need a PostgreSQL instance. Point DB_HOST/DB_PORT/DB_USER/\n"+
			"DB_PASSWORD at one, or set TEST_DB_SKIP=1 to skip them.\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// runSuite builds a database from the migrations, runs the tests, and drops it
func runSuite(m *testing.M) (int, error) {
	cfg := database.GetConfigFromEnv()
	testDB := envOr("TEST_DB_NAME", defaultTestDB)

	// Connect to the maintenance database to create the test one.
	adminCfg := cfg
	adminCfg.DBName = envOr("TEST_ADMIN_DB", "postgres")
	if err := database.Connect(adminCfg); err != nil {
		return 0, err
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

	cfg.DBName = testDB
	if err := database.Connect(cfg); err != nil {
		return 0, err
	}

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

	mustExec(t, `INSERT INTO sites (company_id, site_name, api_key, active) VALUES ($1, 'Site A', $2, TRUE)`,
		companyOne, env.siteAKey)
	mustExec(t, `INSERT INTO sites (company_id, site_name, api_key, active) VALUES ($1, 'Site B', $2, TRUE)`,
		companyOne, env.siteBKey)
	mustExec(t, `INSERT INTO sites (company_id, site_name, api_key, active) VALUES ($1, 'Site C', $2, TRUE)`,
		companyTwo, env.siteCKey)

	return env
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
