package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Quarantine: a terminal excluded from roster-driven change without being taken
// out of service.
//
// THE INCIDENT THESE PREVENT COMING BACK. A bench terminal was holding two
// fingerprint templates that were the only evidence of a live defect. Bringing
// the API up so the terminal could drain its queued access events also woke the
// 15-minute roster reconciler, which queued DELETE jobs for both of those
// people within fifteen minutes. A DELETE is applied at the terminal by
// removing the member row AND erasing that person's template from the sensor,
// so delivering those jobs would have destroyed the evidence.
//
// The reconciler was doing exactly what it is for. There was no way to say
// "leave this one alone", and the only lever available -- disabling the device
// -- would have shut the door. Quarantine is that missing lever.

func seedQuarantineDevice(t *testing.T, siteID int64, serial string) int64 {
	t.Helper()
	var id int64

	// NOT mustScan: its variadic is scan DESTINATIONS, and it runs
	// QueryRow(query) with no arguments at all -- so $1 and $2 were never
	// bound and every caller failed with "there is no parameter $1". There is
	// no helper that both binds arguments and scans a result, so the row is
	// inserted directly here.
	// api_key_hash must be a REAL 64-hex value, not 'hash-' || serial: migration
	// 005 constrains it to NULL or ^[0-9a-f]{64}$ and indexes it UNIQUE. It also
	// must not be NULL -- deviceIsSyncable requires api_key_hash IS NOT NULL, so
	// a credential-less device is never syncable and every reconciliation
	// assertion here would pass or fail for the wrong reason.
	sum := sha256.Sum256([]byte(serial))
	apiKeyHash := hex.EncodeToString(sum[:])

	const q = `INSERT INTO devices (site_id, serial_number, device_name, status,
	                                api_key_hash, active)
	           VALUES ($1, $2, $2, 'ONLINE', $3, TRUE)
	           RETURNING id`
	if err := database.DB.QueryRow(q, siteID, serial, apiKeyHash).Scan(&id); err != nil {
		t.Fatalf("seeding quarantine device %q: %v", serial, err)
	}
	return id
}

func allowPersonEverywhere(t *testing.T, companyID, personID int64) {
	t.Helper()
	mustExec(t, `INSERT INTO permissions (company_id, person_id, scope_type, effect, active)
	             VALUES ($1, $2, 'COMPANY', 'ALLOW', TRUE)`, companyID, personID)
}

func pendingJobCount(t *testing.T, deviceID int64) int {
	t.Helper()
	var n int
	// Same reason as seedQuarantineDevice: mustScan's variadic is scan
	// destinations, so deviceID was never bound to $1.
	const q = `SELECT count(*) FROM sync_jobs
	            WHERE device_id = $1 AND status = 'PENDING'`
	if err := database.DB.QueryRow(q, deviceID).Scan(&n); err != nil {
		t.Fatalf("counting pending jobs for device %d: %v", deviceID, err)
	}
	return n
}

// A PAUSED TERMINAL RECEIVES NO ROSTER-DRIVEN WORK. This is the property the
// whole feature exists for: the reconciler runs, finds the terminal, and leaves
// it alone -- including when a person it holds has just been deleted, which is
// precisely the case that generates the destructive job.
func TestQuarantinedDeviceGetsNoReconciliationJobs(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	deviceID := seedQuarantineDevice(t, siteID, "AT-QUARANTINE-1")
	personID := seedPerson(t, companyID, "test-002", "Bench Finger B")
	allowPersonEverywhere(t, companyID, personID)

	// Converge once so the roster is settled, then clear the jobs that produced.
	if _, _, err := database.ReconcileDeviceRoster(deviceID); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	mustExec(t, `DELETE FROM sync_jobs WHERE device_id = $1`, deviceID)

	if err := database.PauseDeviceSync(deviceID, "investigator@example.com",
		"template mutation under investigation"); err != nil {
		t.Fatalf("pausing: %v", err)
	}

	// The event that would queue a DELETE -- and erase a template.
	mustExec(t, `UPDATE people SET deleted_at = NOW() WHERE id = $1`, personID)

	added, removed, err := database.ReconcileDeviceRoster(deviceID)
	if err != nil {
		t.Fatalf("reconcile while paused: %v", err)
	}
	if added != 0 || removed != 0 {
		t.Errorf("a paused terminal was reconciled: added=%d removed=%d", added, removed)
	}
	if n := pendingJobCount(t, deviceID); n != 0 {
		t.Errorf("a paused terminal was queued %d job(s); want 0", n)
	}
}

// The fleet-wide pass must skip it too. ReconcileStaleRosters selects its own
// device list, so the predicate has to hold there as well as in the per-device
// path -- a guard that covered only one of them would leave the scheduled
// reconciler, which is the one that actually fired during the incident.
func TestTheScheduledPassSkipsAQuarantinedDevice(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	deviceID := seedQuarantineDevice(t, siteID, "AT-QUARANTINE-2")
	personID := seedPerson(t, companyID, "test-003", "Someone")
	allowPersonEverywhere(t, companyID, personID)

	if err := database.PauseDeviceSync(deviceID, "op@example.com", "held"); err != nil {
		t.Fatalf("pausing: %v", err)
	}

	if _, _, _, err := database.ReconcileStaleRosters(context.Background()); err != nil {
		t.Fatalf("scheduled pass: %v", err)
	}
	if n := pendingJobCount(t, deviceID); n != 0 {
		t.Errorf("the scheduled pass queued %d job(s) for a paused terminal", n)
	}
}

// Resuming restores ordinary behaviour, and the next pass converges whatever
// changed while the terminal was paused. Nothing is lost by pausing, only
// deferred -- which is what makes it safe to reach for.
func TestResumingRestoresReconciliation(t *testing.T) {
	newTestEnv(t)
	companyID := companyIDBySlug(t, "one")
	siteID := siteIDByKey(t, "test-site-a-key")

	deviceID := seedQuarantineDevice(t, siteID, "AT-QUARANTINE-3")
	personID := seedPerson(t, companyID, "test-100", "Resumed Person")
	allowPersonEverywhere(t, companyID, personID)

	if err := database.PauseDeviceSync(deviceID, "op@example.com", "held"); err != nil {
		t.Fatalf("pausing: %v", err)
	}
	if _, _, err := database.ReconcileDeviceRoster(deviceID); err != nil {
		t.Fatalf("reconcile while paused: %v", err)
	}
	if n := pendingJobCount(t, deviceID); n != 0 {
		t.Fatalf("paused terminal was queued %d job(s)", n)
	}

	if err := database.ResumeDeviceSync(deviceID); err != nil {
		t.Fatalf("resuming: %v", err)
	}

	added, _, err := database.ReconcileDeviceRoster(deviceID)
	if err != nil {
		t.Fatalf("reconcile after resume: %v", err)
	}
	if added == 0 {
		t.Error("a resumed terminal was not converged")
	}
}

// A quarantine reason is required, because a terminal found paused weeks later
// is a puzzle unless the pause says what it was for -- and the person who can
// answer that is the one running the command now.
func TestPauseRequiresAReason(t *testing.T) {
	newTestEnv(t)
	deviceID := seedQuarantineDevice(t, siteIDByKey(t, "test-site-a-key"), "AT-QUARANTINE-4")

	if err := database.PauseDeviceSync(deviceID, "op@example.com", "   "); err == nil {
		t.Error("a quarantine with no reason was accepted")
	}

	state, err := database.DeviceSyncPaused(deviceID)
	if err != nil {
		t.Fatalf("reading quarantine state: %v", err)
	}
	if state.Paused {
		t.Error("a rejected pause still paused the terminal")
	}
}

// Pausing twice must not move the original timestamp: "how long has this been
// paused" must not reset every time somebody re-runs the command.
func TestPauseIsIdempotentAndKeepsTheOriginalTimestamp(t *testing.T) {
	newTestEnv(t)
	deviceID := seedQuarantineDevice(t, siteIDByKey(t, "test-site-a-key"), "AT-QUARANTINE-5")

	if err := database.PauseDeviceSync(deviceID, "first@example.com", "first"); err != nil {
		t.Fatalf("first pause: %v", err)
	}
	first, err := database.DeviceSyncPaused(deviceID)
	if err != nil {
		t.Fatalf("reading state: %v", err)
	}
	if !first.Paused {
		t.Fatal("the terminal did not report as paused")
	}

	if err := database.PauseDeviceSync(deviceID, "second@example.com", "second"); err != nil {
		t.Fatalf("second pause: %v", err)
	}
	second, err := database.DeviceSyncPaused(deviceID)
	if err != nil {
		t.Fatalf("reading state: %v", err)
	}

	if !second.Since.Time.Equal(first.Since.Time) {
		t.Errorf("re-pausing moved the timestamp: %v -> %v",
			first.Since.Time, second.Since.Time)
	}
	if second.Reason.String != "second" {
		t.Errorf("re-pausing did not refresh the reason: %q", second.Reason.String)
	}
}

func TestPausingAnUnknownDeviceIsReported(t *testing.T) {
	newTestEnv(t)
	if err := database.PauseDeviceSync(999999, "op@example.com", "why"); !errors.Is(err, models.ErrDeviceNotFound) {
		t.Errorf("pausing an unknown device = %v, want ErrDeviceNotFound", err)
	}
}
