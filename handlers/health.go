package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
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
// Optionally protected by METRICS_TOKEN. Left open when unset, which is the
// usual arrangement when the endpoint is only reachable inside a cluster.
func Metrics(c *gin.Context) {
	if token := os.Getenv("METRICS_TOKEN"); token != "" && !metricsTokenValid(c, token) {
		c.Header("WWW-Authenticate", "Bearer")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid metrics token"})
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
func metricsTokenValid(c *gin.Context, expected string) bool {
	if header := c.GetHeader("Authorization"); header != "" {
		if strings.TrimPrefix(header, "Bearer ") == expected {
			return true
		}
	}
	return c.GetHeader("X-Metrics-Token") == expected
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
