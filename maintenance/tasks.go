package maintenance

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
)

// The background tasks themselves, and their configuration.
//
// Every duration is environment-tunable so a deployment can trade freshness
// against database load without a rebuild.

// Defaults chosen to be conservative: a device is only called offline after it
// has missed several heartbeats, and job history is kept for a quarter.
const (
	defaultOfflineAfter    = 5 * time.Minute
	defaultSweepInterval   = 1 * time.Minute
	defaultPruneInterval   = 6 * time.Hour
	defaultRetentionDays   = 90
	defaultShutdownTimeout = 10 * time.Second

	// Dead operator sessions are kept for a month before being deleted. They
	// are not credentials by then -- expired and revoked rows authenticate
	// nothing -- but "who signed in, when, and from where" is a question worth
	// being able to answer after an incident.
	defaultSessionRetentionDays = 30
	defaultSessionPurgeInterval = 6 * time.Hour
)

// Config holds the maintenance settings resolved from the environment
type Config struct {
	Enabled              bool
	OfflineAfter         time.Duration
	SweepInterval        time.Duration
	PruneInterval        time.Duration
	RetentionDays        int
	SessionPurgeInterval time.Duration
	SessionRetentionDays int
	ShutdownTimeout      time.Duration
}

// LoadConfig reads maintenance settings from the environment
func LoadConfig() Config {
	return Config{
		Enabled:              envBool("MAINTENANCE_ENABLED", true),
		OfflineAfter:         envDuration("DEVICE_OFFLINE_AFTER_SECONDS", defaultOfflineAfter),
		SweepInterval:        envDuration("OFFLINE_SWEEP_INTERVAL_SECONDS", defaultSweepInterval),
		PruneInterval:        envDuration("SYNC_JOB_PRUNE_INTERVAL_SECONDS", defaultPruneInterval),
		RetentionDays:        envInt("SYNC_JOB_RETENTION_DAYS", defaultRetentionDays),
		SessionPurgeInterval: envDuration("SESSION_PURGE_INTERVAL_SECONDS", defaultSessionPurgeInterval),
		SessionRetentionDays: envInt("SESSION_RETENTION_DAYS", defaultSessionRetentionDays),
		ShutdownTimeout:      envDuration("MAINTENANCE_SHUTDOWN_TIMEOUT_SECONDS", defaultShutdownTimeout),
	}
}

// Tasks builds the task set for a configuration
func (c Config) Tasks() []Task {
	tasks := []Task{
		{
			// Nothing else moves a device out of ONLINE. Without this sweep the
			// status column only ever reflects what a device last claimed, so a
			// terminal that loses power stays ONLINE forever.
			Name:     "offline_sweep",
			Interval: c.SweepInterval,
			Run: func(ctx context.Context) (string, error) {
				n, err := database.MarkDevicesOfflineContext(ctx, c.OfflineAfter)
				if err != nil {
					return "", err
				}
				if n == 0 {
					return "", nil // quiet when there is nothing to report
				}
				return fmt.Sprintf("marked %d device(s) offline", n), nil
			},
		},
	}

	// Retention of 0 disables pruning entirely, for deployments that would
	// rather keep every job row.
	if c.RetentionDays > 0 {
		tasks = append(tasks, Task{
			Name:     "sync_job_prune",
			Interval: c.PruneInterval,
			Run: func(ctx context.Context) (string, error) {
				n, err := database.PruneSyncJobs(ctx, c.RetentionDays)
				if err != nil {
					return "", err
				}
				if n == 0 {
					return "", nil
				}
				return fmt.Sprintf("pruned %d delivered job(s) older than %dd", n, c.RetentionDays), nil
			},
		})
	}

	// Operator sessions. Nothing else deletes these rows: revoking or expiring
	// a session leaves it in place, so without this sweep user_sessions grows
	// by one row per login forever.
	//
	// This CANNOT log anybody out. The delete matches only rows that are
	// already dead -- past their absolute expiry, or revoked -- and then only
	// after the retention window on top of that. A live session is not
	// reachable by the predicate at any retention setting, including 0.
	if c.SessionRetentionDays >= 0 {
		tasks = append(tasks, Task{
			Name:     "session_purge",
			Interval: c.SessionPurgeInterval,
			Run: func(ctx context.Context) (string, error) {
				n, err := database.PurgeExpiredSessionsContext(ctx, c.SessionRetentionDays)
				if err != nil {
					return "", err
				}
				if n == 0 {
					return "", nil
				}
				return fmt.Sprintf("purged %d dead operator session(s) older than %dd",
					n, c.SessionRetentionDays), nil
			},
		})
	}

	return tasks
}

// Describe logs the effective configuration at startup, so an operator can see
// what the process will actually do without reading the environment back.
func (c Config) Describe() {
	if !c.Enabled {
		log.Println("maintenance: disabled (MAINTENANCE_ENABLED=false)")
		return
	}
	log.Printf("maintenance: offline sweep every %s (device offline after %s)",
		c.SweepInterval, c.OfflineAfter)
	if c.RetentionDays > 0 {
		log.Printf("maintenance: sync job prune every %s (retain %d days)",
			c.PruneInterval, c.RetentionDays)
	} else {
		log.Println("maintenance: sync job pruning disabled (SYNC_JOB_RETENTION_DAYS=0)")
	}
	log.Printf("maintenance: operator session purge every %s (retain dead sessions %d days)",
		c.SessionPurgeInterval, c.SessionRetentionDays)
}

func envBool(key string, fallback bool) bool {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			return v
		}
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			return v
		}
	}
	return fallback
}

// envDuration reads a whole number of seconds. A zero or unparseable value
// falls back to the default rather than producing a ticker that spins.
func envDuration(key string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return fallback
}
