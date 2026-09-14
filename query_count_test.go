package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Query counts that must not grow with the result.
//
// An N+1 answers exactly the same JSON as a join. Nothing in a response, and
// nothing in an ordinary test, can tell twenty-one round trips from two --
// so this file counts them, using the statement counter in
// database/statements.go, and holds each list endpoint to a count that is
// the same for a large result as for a small one.
//
// THE HARNESS. Each case seeds the company at a small size and a larger one,
// serves the endpoint through NewRouter, and reads how many statements the
// pool sent. The assertion is on the DIFFERENCE, not on an absolute: an
// endpoint that makes four statements for three rows and four for thirty is
// fine; one that makes four and thirty-one has a query per row and is what
// this file exists to catch. The absolute is logged so a reviewer can see it.
//
// The sizes are small on purpose. Thirty rows are as good as three thousand
// for detecting a per-row query, and the suite already takes minutes.

const (
	smallResult = 3
	largeResult = 30
)

// countStatements runs fn and reports how many statements it sent.
func countStatements(fn func()) int64 {
	before := database.StatementCount()
	fn()
	return database.StatementCount() - before
}

// serve performs one request through the router and fails the test unless it
// answers 200.
func serveOK(t *testing.T, router *gin.Engine, method, path string, headers map[string]string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
	}
}

func sessionHeaders(token string) map[string]string {
	name, _ := middleware.SessionCookieConfig()
	return map[string]string{"Cookie": name + "=" + token}
}

// queryCountFixture is one company with an OWNER session and the identifiers
// the seeders need.
type queryCountFixture struct {
	env       *testEnv
	companyID int64
	siteID    int64
	sitePub   string
	token     string
	csrf      string
	deviceKey string
	serial    string
}

func newQueryCountFixture(t *testing.T) *queryCountFixture {
	t.Helper()
	cheapBcrypt(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	_, token, csrf := consoleOperatorSession(t, env.router, companyID,
		"owner@example.com", models.RoleOwner)
	f := &queryCountFixture{
		env: env, companyID: companyID, token: token, csrf: csrf,
		siteID: siteIDByKey(t, env.siteAKey), serial: "AT-QC-0001",
	}
	f.sitePub = queryString(t, `SELECT public_id::text FROM sites WHERE id = $1`, f.siteID)
	f.deviceKey = env.registerDevice(env.siteAKey, f.serial)
	return f
}

// queryCountCase is one endpoint and how to grow its result.
type queryCountCase struct {
	name string
	// seed adds n more rows of whatever the endpoint lists.
	seed func(t *testing.T, f *queryCountFixture, n int)
	// request serves the endpoint once.
	request func(t *testing.T, f *queryCountFixture)
	// allowance is how many extra statements the large result may cost over
	// the small one. Zero for every endpoint that pages or joins; the one
	// non-zero entry is documented where it is set.
	allowance int64
}

// seedAdoptedAnnouncement writes an ADOPTED announcement for a serial straight
// into the table, as a terminal that has announced and been adopted would leave
// it. DIRECT ON PURPOSE: the adopt route is rate-limited per caller, and a test
// that needs thirty of them in a row would be refused by the limiter rather
// than measured. The hashes only have to be unique, so they are derived from
// the serial.
func seedAdoptedAnnouncement(t *testing.T, companyID int64, serial string) {
	t.Helper()
	mustExec(t, `
		INSERT INTO terminal_announcements
		       (serial_number, pairing_code_hash, pairing_code_prefix,
		        announce_token_hash, announce_token_prefix,
		        state, company_id, adopted_at, expires_at)
		VALUES ($1::text,
		        md5($1::text || ':code') || md5($1::text || ':code:2'), left($1::text, 8),
		        md5($1::text || ':token') || md5($1::text || ':token:2'), left($1::text, 12),
		        'ADOPTED', $2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + interval '1 hour')`,
		serial, companyID)
}

var queryCountCases = []queryCountCase{
	{
		name: "GET /console/operators",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				user := mustCreateOperator(t, f.companyID,
					fmt.Sprintf("op-%d-%d@example.com", n, i), models.RoleManager)
				if err := database.ReplaceSiteGrants(f.companyID, user.ID, []string{f.sitePub}); err != nil {
					t.Fatalf("granting a site: %v", err)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/operators", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/people",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				mustExec(t, `INSERT INTO people (company_id, external_id, full_name, membership_type, active)
				             VALUES ($1, $2, $3, 'STANDARD', TRUE)`,
					f.companyID, fmt.Sprintf("P-%d-%03d", n, i), fmt.Sprintf("Person %d", i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/people?limit=100", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/terminals",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				f.env.registerDevice(f.env.siteAKey, fmt.Sprintf("AT-QC-%d-%03d", n, i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/terminals?limit=100", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/sites",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				seedSite(t, f.companyID, fmt.Sprintf("Site %d-%03d", n, i), fmt.Sprintf("qc-site-key-%d-%d", n, i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/sites", sessionHeaders(f.token))
		},
	},
	{
		/*
		  THE ONE PER-ROW READ THE FIRST AUDIT MISSED. The list read the
		  announcements and then looked each serial's ownership up one
		  statement at a time to decide its verdict -- 5 statements for 3
		  rows, 35 for 33 -- on the endpoint the console polls every ten
		  seconds. The ownership is joined in now. Half the seeded serials
		  already own a device row so the join has something to meet.
		*/
		name: "GET /console/terminal-announcements",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				serial := fmt.Sprintf("AT-AN-%d-%03d", n, i)
				if i%2 == 0 {
					f.env.registerDevice(f.env.siteAKey, serial)
				}
				seedAdoptedAnnouncement(t, f.companyID, serial)
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/terminal-announcements", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/schedules",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				if _, err := database.CreateSchedule(f.companyID, models.ScheduleRequest{
					Name: fmt.Sprintf("Schedule %d-%03d", n, i),
					Windows: []models.ScheduleWindow{
						{DaysOfWeek: models.DayEveryDay, StartTime: "08:00", EndTime: "12:00"},
						{DaysOfWeek: models.DayEveryDay, StartTime: "13:00", EndTime: "18:00"},
					},
				}); err != nil {
					t.Fatalf("creating a schedule: %v", err)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/schedules", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/events",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				if _, err := database.RecordAccessEvent(database.AccessEvent{
					CompanyID: f.companyID, SiteID: f.siteID,
					SubjectExternalID: fmt.Sprintf("P-%d", i),
					EventType:         models.EventAccessDenied, Decision: models.DecisionDenied,
					ReasonCode: "NO_PERMISSION",
				}); err != nil {
					t.Fatalf("recording an event: %v", err)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/events?limit=100", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/audit",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				database.WriteAuditEvent(database.AuditEntry{
					CompanyID: f.companyID, ActorEmail: "owner@example.com", ActorRole: models.RoleOwner,
					Action: "PERSON_CREATED", TargetType: "person", TargetLabel: fmt.Sprintf("P-%d", i),
				})
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/audit?limit=100", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/api-credentials",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			// A company may hold twenty live credentials; the large seed stays
			// under that. Fifteen rows detect a per-row query as well as thirty.
			if n > 15 {
				n = 15
			}
			for i := 0; i < n; i++ {
				code, body := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/api-credentials",
					fmt.Sprintf(`{"name":"Integration %d-%03d","scopes":["members:read"],"site_ids":[%q]}`, n, i, f.sitePub),
					f.token, f.csrf)
				if code != http.StatusCreated {
					t.Fatalf("issuing an api credential = %d (%v)", code, body)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/api-credentials", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/people/:id/permissions",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			if n == smallResult {
				f.env.createMember(f.env.siteAKey, "P-PERM", "Permitted Person")
			}
			for i := 0; i < n; i++ {
				serial := fmt.Sprintf("AT-PERM-%d-%03d", n, i)
				f.env.registerDevice(f.env.siteAKey, serial)
				if _, err := database.GrantPermission(f.companyID, "P-PERM", models.PermissionRequest{
					ScopeType: models.ScopeTerminal, DeviceSerial: serial,
				}); err != nil {
					t.Fatalf("granting a permission: %v", err)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/people/P-PERM/permissions", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /console/firmware",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				code, body := consoleCall(t, f.env.router, http.MethodPost, "/api/v1/console/firmware",
					fmt.Sprintf(`{"version":"9.%d.%d","device_type":"TERMINAL","download_url":"https://example.com/fw-%d-%d.bin","checksum_sha256":"%064x","size_bytes":1024}`,
						n, i, n, i, i+n*100), f.token, f.csrf)
				if code != http.StatusCreated {
					t.Fatalf("publishing firmware = %d (%v)", code, body)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/console/firmware", sessionHeaders(f.token))
		},
	},
	{
		name: "GET /members (site key)",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				f.env.createMember(f.env.siteAKey, fmt.Sprintf("M-%d-%03d", n, i), fmt.Sprintf("Member %d", i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/members", siteAuth(f.env.siteAKey))
		},
	},
	{
		name: "GET /members?limit= (site key, paged)",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				f.env.createMember(f.env.siteAKey, fmt.Sprintf("MP-%d-%03d", n, i), fmt.Sprintf("Member %d", i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/members?limit=100&offset=0", siteAuth(f.env.siteAKey))
		},
	},
	{
		name: "GET /members/changes (site key)",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			for i := 0; i < n; i++ {
				f.env.createMember(f.env.siteAKey, fmt.Sprintf("MC-%d-%03d", n, i), fmt.Sprintf("Member %d", i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet,
				"/api/v1/members/changes?since=2000-01-01T00:00:00Z&limit=100", siteAuth(f.env.siteAKey))
		},
	},
	{
		name: "GET /access/logs (site key)",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			if n == smallResult {
				f.env.createMember(f.env.siteAKey, "M-LOG", "Logged Member")
			}
			for i := 0; i < n; i++ {
				res := f.env.do(http.MethodPost, "/api/v1/access/log", map[string]any{
					"member_id": "M-LOG", "granted": true, "source": "FINGERPRINT",
				}, siteAuth(f.env.siteAKey))
				if res.Code != http.StatusCreated && res.Code != http.StatusOK {
					t.Fatalf("logging access = %d (%s)", res.Code, res.Raw)
				}
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/access/logs?limit=100", siteAuth(f.env.siteAKey))
		},
	},
	{
		name: "GET /devices/jobs (device key)",
		seed: func(t *testing.T, f *queryCountFixture, n int) {
			// Every member created queues a CREATE job for every terminal.
			for i := 0; i < n; i++ {
				f.env.createMember(f.env.siteAKey, fmt.Sprintf("J-%d-%03d", n, i), fmt.Sprintf("Job Member %d", i))
			}
		},
		request: func(t *testing.T, f *queryCountFixture) {
			serveOK(t, f.env.router, http.MethodGet, "/api/v1/devices/jobs?limit=100", deviceAuth(f.deviceKey))
		},
	},
}

func TestListEndpointsDoNotQueryPerRow(t *testing.T) {
	for _, tc := range queryCountCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueryCountFixture(t)

			tc.seed(t, f, smallResult)
			// Warm once: the first request may pay for things a list does not
			// (session touch, a lazily built cache) and those are not per row.
			tc.request(t, f)
			small := countStatements(func() { tc.request(t, f) })

			tc.seed(t, f, largeResult)
			tc.request(t, f)
			large := countStatements(func() { tc.request(t, f) })

			t.Logf("%-40s %3d rows: %3d statements   %3d rows: %3d statements",
				tc.name, smallResult, small, smallResult+largeResult, large)

			if large-small > tc.allowance {
				t.Errorf("%s sends %d statements for %d rows but %d for %d rows: "+
					"the count grows with the result (a query per row)",
					tc.name, small, smallResult, large, smallResult+largeResult)
			}
		})
	}
}

// TestStatementCounterCountsWhatThePoolSends pins the instrument itself: a
// counter that missed a path would make every assertion above vacuous.
func TestStatementCounterCountsWhatThePoolSends(t *testing.T) {
	newTestEnv(t)

	n := countStatements(func() {
		var one int
		mustScan(t, `SELECT 1`, &one)
		mustExec(t, `SELECT 2`)
		rows, err := database.DB.Query(`SELECT 3`)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	})
	if n != 3 {
		t.Errorf("three statements counted as %d", n)
	}

	// A transaction's own statements count; its BEGIN and COMMIT do not.
	n = countStatements(func() {
		tx, err := database.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`SELECT 4`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	})
	if n != 1 {
		t.Errorf("one statement inside a transaction counted as %d", n)
	}

	// Prepared statements count per execution.
	n = countStatements(func() {
		stmt, err := database.DB.Prepare(`SELECT $1::int`)
		if err != nil {
			t.Fatal(err)
		}
		defer stmt.Close()
		for i := 0; i < 2; i++ {
			var v int
			if err := stmt.QueryRow(i).Scan(&v); err != nil {
				t.Fatal(err)
			}
		}
	})
	if n != 2 {
		t.Errorf("two executions of a prepared statement counted as %d", n)
	}
}
