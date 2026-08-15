package handlers

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/maintenance"

	"github.com/gin-gonic/gin"
)

// Health and metrics endpoints.
//
// Liveness and readiness are separate on purpose. Liveness answers "is this
// process wedged, should it be restarted"; readiness answers "should traffic be
// sent here". A database outage must fail readiness but NOT liveness -- an
// orchestrator restarting every API instance because Postgres blinked turns a
// recoverable dependency failure into a full outage.
//
// The pre-existing GET /health is left exactly as it was, since terminals and
// uptime checks already point at it.

const readinessTimeout = 2 * time.Second

var (
	startedAt = time.Now()
	scheduler *maintenance.Scheduler
)

// SetScheduler gives the health handlers visibility of background task state
func SetScheduler(s *maintenance.Scheduler) { scheduler = s }

// HealthLive handles GET /health/live
//
// Process liveness only. It deliberately touches no dependency: if this handler
// responds, the process is running and able to serve.
func HealthLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":         "alive",
		"service":        "Access Terminal Cloud API",
		"uptime_seconds": int(time.Since(startedAt).Seconds()),
		"version":        buildInfo.Version,
		"commit":         buildInfo.Commit,
	})
}

// HealthReady handles GET /health/ready
//
// Reports 503 when a dependency the service cannot work without is unavailable,
// so a load balancer stops sending traffic here.
func HealthReady(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
	defer cancel()

	checks := gin.H{}
	ready := true

	if err := database.Ping(ctx); err != nil {
		checks["database"] = gin.H{"status": "down", "error": err.Error()}
		ready = false
	} else {
		checks["database"] = gin.H{"status": "up"}
	}

	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}

	c.JSON(status, gin.H{
		"status":         state,
		"uptime_seconds": int(time.Since(startedAt).Seconds()),
		"checks":         checks,
	})
}

// HealthMaintenance handles GET /health/maintenance
//
// What the background tasks have actually been doing. A task whose failure count
// is climbing is invisible in the metrics gauges alone.
func HealthMaintenance(c *gin.Context) {
	if scheduler == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "tasks": []any{}})
		return
	}
	stats := scheduler.Stats()
	c.JSON(http.StatusOK, gin.H{
		"enabled": true,
		"count":   len(stats),
		"tasks":   stats,
	})
}

// Metrics handles GET /metrics in Prometheus text exposition format.
//
// Hand-written rather than pulled from a client library: the exposition format
// is a few lines of text, and this avoids a dependency for four gauges.
//
// PROTECTED BY METRICS_TOKEN, AND CLOSED WHEN IT IS UNSET (SEC-12).
//
// This used to be left open when the variable was absent, on the reasoning that
// the endpoint is usually only reachable inside a cluster. That reasoning makes
// the SAFE configuration the one you have to remember: a deployment that forgets
// the variable, or loses it in an environment rename, publishes its fleet size,
// device states, tenant counts and build identity to anyone who asks -- and
// nothing about the response says it was supposed to be protected.
//
// Failing closed inverts that. An installation that genuinely wants an open
// endpoint says so with METRICS_PUBLIC=true, which is a decision somebody made
// on purpose and can be found by grepping the environment.
func Metrics(c *gin.Context) {
	token := os.Getenv("METRICS_TOKEN")
	switch {
	case token != "":
		if !metricsTokenValid(c, token) {
			c.Header("WWW-Authenticate", "Bearer")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid metrics token"})
			return
		}
	case metricsDeliberatelyPublic():
		// Explicitly opted in. Nothing to check.
	default:
		// No token and no opt-in: refuse, and say which of the two is missing so
		// the operator who set this up can fix it without reading the source.
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Metrics are not exposed. Set METRICS_TOKEN, " +
				"or METRICS_PUBLIC=true to serve them unauthenticated."})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
	defer cancel()

	m, err := database.CollectMetrics(ctx)
	if err != nil {
		// Report the scrape failure as a metric rather than an error page, so a
		// dashboard can alert on it instead of seeing a gap it cannot explain.
		var b strings.Builder
		writeGauge(&b, "access_terminal_up", "Whether the last metrics scrape succeeded.", nil, 0)
		c.Data(http.StatusServiceUnavailable, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
		return
	}

	var b strings.Builder

	writeGauge(&b, "access_terminal_up",
		"Whether the last metrics scrape succeeded.", nil, 1)

	writeGauge(&b, "access_terminal_uptime_seconds",
		"Seconds since process start.", nil, time.Since(startedAt).Seconds())

	// The conventional build_info shape: a constant 1 carrying the identity in
	// labels, so a dashboard can group by version and a deploy shows up as the
	// series changing rather than as a gap.
	b.WriteString("# HELP access_terminal_build_info Version and commit of the running binary.\n")
	b.WriteString("# TYPE access_terminal_build_info gauge\n")
	fmt.Fprintf(&b, "access_terminal_build_info{version=%q,commit=%q} 1\n",
		buildInfo.Version, buildInfo.Commit)

	b.WriteString("# HELP access_terminal_devices Devices by status.\n")
	b.WriteString("# TYPE access_terminal_devices gauge\n")
	for _, status := range sortedKeys(m.DevicesByStatus) {
		fmt.Fprintf(&b, "access_terminal_devices{status=%q} %d\n", status, m.DevicesByStatus[status])
	}

	writeGauge(&b, "access_terminal_devices_total",
		"Total non-deleted devices.", nil, float64(m.DevicesTotal))

	writeGauge(&b, "access_terminal_devices_firmware_outdated",
		"Devices not running the current build for their release channel.",
		nil, float64(m.DevicesFirmwareOutdated))

	b.WriteString("# HELP access_terminal_sync_jobs Sync jobs by status.\n")
	b.WriteString("# TYPE access_terminal_sync_jobs gauge\n")
	for _, status := range sortedKeys(m.SyncJobsByStatus) {
		fmt.Fprintf(&b, "access_terminal_sync_jobs{status=%q} %d\n", status, m.SyncJobsByStatus[status])
	}

	writeGauge(&b, "access_terminal_sync_jobs_oldest_pending_age_seconds",
		"Age of the oldest undelivered sync job. Rising means a device is not draining its queue.",
		nil, m.OldestPendingJobAge)

	writeGauge(&b, "access_terminal_sync_jobs_retrying",
		"Pending sync jobs that have already failed at least once.",
		nil, float64(m.SyncJobRetries))

	writeGauge(&b, "access_terminal_people_total",
		"Non-deleted people.", nil, float64(m.PeopleTotal))
	writeGauge(&b, "access_terminal_sites_total",
		"Non-deleted sites.", nil, float64(m.SitesTotal))
	writeGauge(&b, "access_terminal_companies_total",
		"Non-deleted companies.", nil, float64(m.CompaniesTotal))

	if scheduler != nil {
		stats := scheduler.Stats()

		b.WriteString("# HELP access_terminal_maintenance_runs_total Maintenance task executions.\n")
		b.WriteString("# TYPE access_terminal_maintenance_runs_total counter\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "access_terminal_maintenance_runs_total{task=%q} %d\n", s.Name, s.Runs)
		}

		b.WriteString("# HELP access_terminal_maintenance_failures_total Maintenance task failures.\n")
		b.WriteString("# TYPE access_terminal_maintenance_failures_total counter\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "access_terminal_maintenance_failures_total{task=%q} %d\n", s.Name, s.Failures)
		}

		b.WriteString("# HELP access_terminal_maintenance_last_run_duration_seconds Duration of the last run.\n")
		b.WriteString("# TYPE access_terminal_maintenance_last_run_duration_seconds gauge\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "access_terminal_maintenance_last_run_duration_seconds{task=%q} %g\n",
				s.Name, s.LastDurationSeconds())
		}
	}

	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}

// metricsTokenValid accepts the token as a bearer credential or a plain header
// metricsDeliberatelyPublic reports whether an installation has opted into an
// unauthenticated /metrics.
func metricsDeliberatelyPublic() bool {
	public, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("METRICS_PUBLIC")))
	return err == nil && public
}

// metricsTokenValid compares the presented token in CONSTANT TIME (SEC-11).
//
// `==` on strings returns as soon as two bytes differ, so how long a rejection
// takes depends on how many leading bytes were right. That is a usable oracle
// for recovering a secret one byte at a time, and the fix costs nothing --
// there is no argument for the fast comparison on a credential.
//
// Both accepted headers are compared, and BOTH comparisons always run, so which
// header carried the token is not itself observable through timing.
// ConstantTimeCompare already returns 0 for differing lengths without leaking
// where they diverged, so no length check is needed first.
func metricsTokenValid(c *gin.Context, expected string) bool {
	bearer := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")

	viaBearer := subtle.ConstantTimeCompare([]byte(bearer), []byte(expected))
	viaHeader := subtle.ConstantTimeCompare(
		[]byte(c.GetHeader("X-Metrics-Token")), []byte(expected))

	return viaBearer|viaHeader == 1
}

func writeGauge(b *strings.Builder, name, help string, _ map[string]string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
}

// sortedKeys keeps series order stable between scrapes
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
