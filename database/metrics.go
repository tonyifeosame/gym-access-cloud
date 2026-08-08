package database

import (
	"context"
	"time"
)

// Operational metrics and maintenance queries.
//
// Gauges are computed at scrape time rather than kept as in-process counters.
// Counters drift: they reset on restart, and with more than one instance running
// each would report only what it happened to observe. The database is the single
// source of truth for how many devices are offline or how many jobs are pending,
// so every instance reports the same numbers.
//
// These are deliberately fleet-wide, not company-scoped. /metrics is an operator
// endpoint for monitoring the deployment, not a tenant-facing view.

// SystemMetrics is a point-in-time snapshot of the system
type SystemMetrics struct {
	DevicesByStatus         map[string]int
	DevicesTotal            int
	DevicesFirmwareOutdated int
	SyncJobsByStatus        map[string]int
	OldestPendingJobAge     float64 // seconds
	SyncJobRetries          int     // pending jobs that have failed at least once
	PeopleTotal             int
	SitesTotal              int
	CompaniesTotal          int
}

// CollectMetrics gathers a metrics snapshot.
func CollectMetrics(ctx context.Context) (*SystemMetrics, error) {
	m := &SystemMetrics{
		DevicesByStatus:  map[string]int{},
		SyncJobsByStatus: map[string]int{},
	}

	// Every state is reported even at zero. A gauge that vanishes when it hits
	// zero looks identical to a scrape failure on a dashboard.
	for _, status := range []string{"PROVISIONING", "ONLINE", "OFFLINE", "UPDATING", "ERROR", "DISABLED"} {
		m.DevicesByStatus[status] = 0
	}
	for _, status := range []string{"PENDING", "IN_PROGRESS", "COMPLETED", "FAILED", "CANCELLED"} {
		m.SyncJobsByStatus[status] = 0
	}

	rows, err := DB.QueryContext(ctx,
		`SELECT status, count(*) FROM devices WHERE deleted_at IS NULL GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		m.DevicesByStatus[status] = count
		m.DevicesTotal += count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = DB.QueryContext(ctx, `SELECT status, count(*) FROM sync_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		m.SyncJobsByStatus[status] = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The age of the oldest undelivered job is the signal that actually matters:
	// a large pending count that is draining is healthy, a small one that is not
	// draining is not.
	err = DB.QueryRowContext(ctx, `
		SELECT COALESCE(EXTRACT(EPOCH FROM (CURRENT_TIMESTAMP - min(created_at))), 0)
		  FROM sync_jobs WHERE status = 'PENDING'`).Scan(&m.OldestPendingJobAge)
	if err != nil {
		return nil, err
	}

	err = DB.QueryRowContext(ctx,
		`SELECT count(*) FROM sync_jobs WHERE status = 'PENDING' AND attempts > 0`).
		Scan(&m.SyncJobRetries)
	if err != nil {
		return nil, err
	}

	err = DB.QueryRowContext(ctx, `
		SELECT count(*) FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  LEFT JOIN firmware_versions fv
		         ON fv.company_id = s.company_id
		        AND fv.device_type = d.device_type
		        AND fv.release_channel = d.release_channel
		        AND fv.is_current AND fv.deleted_at IS NULL
		 WHERE d.deleted_at IS NULL AND s.deleted_at IS NULL
		   AND fv.version IS NOT NULL
		   AND (d.firmware_version IS NULL OR d.firmware_version <> fv.version)`).
		Scan(&m.DevicesFirmwareOutdated)
	if err != nil {
		return nil, err
	}

	err = DB.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM people WHERE deleted_at IS NULL),
		       (SELECT count(*) FROM sites WHERE deleted_at IS NULL),
		       (SELECT count(*) FROM companies WHERE deleted_at IS NULL)`).
		Scan(&m.PeopleTotal, &m.SitesTotal, &m.CompaniesTotal)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// Ping checks that the database is reachable, for readiness probes.
func Ping(ctx context.Context) error {
	return DB.PingContext(ctx)
}

// PruneSyncJobs deletes delivered and superseded jobs older than the retention
// window, and reports how many rows went.
//
// Only COMPLETED and CANCELLED rows are eligible. PENDING work is still owed to
// a device, and FAILED rows are a dead-letter queue worth investigating, so
// neither is ever pruned regardless of age.
func PruneSyncJobs(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}

	result, err := DB.ExecContext(ctx, `
		DELETE FROM sync_jobs
		 WHERE status IN ('COMPLETED', 'CANCELLED')
		   AND created_at < CURRENT_TIMESTAMP - ($1 || ' days')::interval`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// MarkDevicesOfflineContext flags devices that have missed heartbeats for longer
// than the given window.
//
// A device that has never sent a heartbeat is left alone: it has not gone
// quiet, it has never spoken, and that is what PROVISIONING already says.
func MarkDevicesOfflineContext(ctx context.Context, staleAfter time.Duration) (int64, error) {
	result, err := DB.ExecContext(ctx, `
		UPDATE devices
		   SET status = 'OFFLINE'
		 WHERE status IN ('ONLINE', 'UPDATING')
		   AND deleted_at IS NULL
		   AND last_heartbeat_at IS NOT NULL
		   AND last_heartbeat_at < CURRENT_TIMESTAMP - ($1 || ' seconds')::interval`,
		int(staleAfter.Seconds()))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
