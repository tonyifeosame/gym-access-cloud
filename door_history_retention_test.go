package main

import (
	"context"
	"testing"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/maintenance"
)

// Door-history retention (038): events and the legacy access_logs, on the
// company's own window or -- when it has none -- the platform default.
//
// Every test here states its windows explicitly rather than relying on the
// shipped default, so a change to that default fails the one test that is about
// it and no other.

// seedDoorEvent records one door event at `at` for a company and site.
func seedDoorEvent(t *testing.T, companyID, siteID int64, at time.Time) {
	t.Helper()
	mustExec(t, `INSERT INTO events (company_id, site_id, event_type, decision, occurred_at)
	             VALUES ($1, $2, 'ACCESS_GRANTED', 'GRANTED', $3)`, companyID, siteID, at)
}

// seedAccessLog records one legacy access-log row at `at`, the shape the
// device upload path writes.
func seedAccessLog(t *testing.T, companyID, siteID int64, at time.Time) {
	t.Helper()
	mustExec(t, `INSERT INTO access_logs
	                 (company_id, site_id, person_external_id, granted, source, site_name, occurred_at)
	             VALUES ($1, $2, 'P-1', TRUE, 'FINGERPRINT', 'Site', $3)`, companyID, siteID, at)
}

func siteIDByName(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	mustScan(t, `SELECT id FROM sites WHERE site_name = '`+name+`'`, &id)
	return id
}

func countDoorHistory(t *testing.T, companyID int64) (events, logs int) {
	t.Helper()
	mustScan(t, `SELECT count(*) FROM events WHERE company_id = `+itoa(companyID), &events)
	mustScan(t, `SELECT count(*) FROM access_logs WHERE company_id = `+itoa(companyID), &logs)
	return events, logs
}

func daysAgo(n int) time.Time { return time.Now().AddDate(0, 0, -n) }

// purgeDoorHistory runs both purges the way the maintenance task does.
func purgeDoorHistory(t *testing.T, defaultDays int) (events, logs int64) {
	t.Helper()
	ctx := context.Background()
	events, err := database.PurgeEvents(ctx, defaultDays)
	if err != nil {
		t.Fatalf("purging events: %v", err)
	}
	logs, err = database.PurgeAccessLogs(ctx, defaultDays)
	if err != nil {
		t.Fatalf("purging access logs: %v", err)
	}
	return events, logs
}

// TestDoorHistoryPastTheDefaultIsPurged: a company that never chose a window
// no longer keeps its door history for ever -- the platform default applies to
// both tables -- and nothing inside the window goes.
func TestDoorHistoryPastTheDefaultIsPurged(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "two")
	site := siteIDByName(t, "Site C")

	seedDoorEvent(t, company, site, daysAgo(400))
	seedDoorEvent(t, company, site, daysAgo(300))
	seedAccessLog(t, company, site, daysAgo(400))
	seedAccessLog(t, company, site, daysAgo(300))

	events, logs := purgeDoorHistory(t, 365)
	if events != 1 || logs != 1 {
		t.Fatalf("purged %d event(s) and %d log(s), want exactly the one of each past 365 days",
			events, logs)
	}

	leftEvents, leftLogs := countDoorHistory(t, company)
	if leftEvents != 1 || leftLogs != 1 {
		t.Errorf("kept %d event(s) and %d log(s), want the one of each inside the window",
			leftEvents, leftLogs)
	}
}

// TestNoDefaultKeepsUnconfiguredHistory: a default of zero is "no default",
// which is exactly pre-038 behaviour -- a company with no window loses nothing.
func TestNoDefaultKeepsUnconfiguredHistory(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "two")
	site := siteIDByName(t, "Site C")

	seedDoorEvent(t, company, site, daysAgo(4000))
	seedAccessLog(t, company, site, daysAgo(4000))

	if events, logs := purgeDoorHistory(t, 0); events != 0 || logs != 0 {
		t.Fatalf("purged %d event(s) and %d log(s) with no default, want none", events, logs)
	}
	if events, logs := countDoorHistory(t, company); events != 1 || logs != 1 {
		t.Errorf("kept %d event(s) and %d log(s), want everything", events, logs)
	}
}

// TestACompanysOwnWindowBeatsTheDefault, in both directions: shorter purges
// sooner than the default would, longer keeps what the default would have
// purged.
func TestACompanysOwnWindowBeatsTheDefault(t *testing.T) {
	newTestEnv(t)
	shorter := companyIDBySlug(t, "one")
	longer := companyIDBySlug(t, "two")
	mustExec(t, `UPDATE companies SET event_retention_days = 30 WHERE id = $1`, shorter)
	mustExec(t, `UPDATE companies SET event_retention_days = 1000 WHERE id = $1`, longer)

	siteA := siteIDByName(t, "Site A")
	siteC := siteIDByName(t, "Site C")

	seedDoorEvent(t, shorter, siteA, daysAgo(90))
	seedAccessLog(t, shorter, siteA, daysAgo(90))
	seedDoorEvent(t, longer, siteC, daysAgo(400))
	seedAccessLog(t, longer, siteC, daysAgo(400))

	purgeDoorHistory(t, 365)

	if events, logs := countDoorHistory(t, shorter); events != 0 || logs != 0 {
		t.Errorf("30-day company kept %d event(s) and %d log(s) aged 90 days, want none",
			events, logs)
	}
	if events, logs := countDoorHistory(t, longer); events != 1 || logs != 1 {
		t.Errorf("1000-day company kept %d event(s) and %d log(s) aged 400 days, want both",
			events, logs)
	}
}

// TestRetentionBoundaryKeepsTheRowExactlyAtTheWindow.
//
// The predicate is strictly older-than. Everything runs in ONE transaction, so
// CURRENT_TIMESTAMP -- which the purge measures from -- is the same instant the
// rows were placed against, and the boundary is exact rather than a race with
// the clock.
func TestRetentionBoundaryKeepsTheRowExactlyAtTheWindow(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "one")
	site := siteIDByName(t, "Site A")
	mustExec(t, `UPDATE companies SET event_retention_days = 30 WHERE id = $1`, company)

	tx, err := database.DB.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	offsets := []string{
		"interval '30 days' + interval '1 second'", // past the window: purged
		"interval '30 days'",                       // exactly at it: kept
		"interval '30 days' - interval '1 second'", // inside it: kept
	}
	for _, offset := range offsets {
		if _, err := tx.Exec(`INSERT INTO events (company_id, site_id, event_type, decision, occurred_at)
		                      VALUES ($1, $2, 'ACCESS_GRANTED', 'GRANTED', CURRENT_TIMESTAMP - `+offset+`)`,
			company, site); err != nil {
			t.Fatalf("seeding event at -(%s): %v", offset, err)
		}
		if _, err := tx.Exec(`INSERT INTO access_logs
		                          (company_id, site_id, person_external_id, granted, source, site_name, occurred_at)
		                      VALUES ($1, $2, 'P-1', TRUE, 'FINGERPRINT', 'Site A', CURRENT_TIMESTAMP - `+offset+`)`,
			company, site); err != nil {
			t.Fatalf("seeding access log at -(%s): %v", offset, err)
		}
	}

	var events, logs int64
	if err := tx.QueryRow(`SELECT purge_events($1, NULL)`, company).Scan(&events); err != nil {
		t.Fatalf("purge_events: %v", err)
	}
	if err := tx.QueryRow(`SELECT purge_access_logs($1, NULL)`, company).Scan(&logs); err != nil {
		t.Fatalf("purge_access_logs: %v", err)
	}
	if events != 1 || logs != 1 {
		t.Fatalf("purged %d event(s) and %d log(s), want only the one a second past the window",
			events, logs)
	}

	var keptEvents, keptLogs int
	if err := tx.QueryRow(`SELECT count(*) FROM events WHERE company_id = $1`, company).
		Scan(&keptEvents); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM access_logs WHERE company_id = $1`, company).
		Scan(&keptLogs); err != nil {
		t.Fatal(err)
	}
	if keptEvents != 2 || keptLogs != 2 {
		t.Errorf("kept %d event(s) and %d log(s), want the two at and inside the window",
			keptEvents, keptLogs)
	}
}

// TestRetentionIsScopedToTheCompany: one company's window never reaches
// another's history, every site in a company follows that company's window,
// and a purge aimed at one company touches no other.
func TestRetentionIsScopedToTheCompany(t *testing.T) {
	newTestEnv(t)
	one := companyIDBySlug(t, "one")
	two := companyIDBySlug(t, "two")
	mustExec(t, `UPDATE companies SET event_retention_days = 30 WHERE id = $1`, one)

	siteA := siteIDByName(t, "Site A")
	siteB := siteIDByName(t, "Site B")
	siteC := siteIDByName(t, "Site C")

	for _, site := range []int64{siteA, siteB} {
		seedDoorEvent(t, one, site, daysAgo(60))
		seedDoorEvent(t, one, site, daysAgo(10))
		seedAccessLog(t, one, site, daysAgo(60))
		seedAccessLog(t, one, site, daysAgo(10))
	}
	seedDoorEvent(t, two, siteC, daysAgo(60))
	seedAccessLog(t, two, siteC, daysAgo(60))

	// Aimed at company one, with a default that WOULD purge company two's rows
	// were the scope ignored.
	var events, logs int64
	mustScan(t, `SELECT purge_events(`+itoa(one)+`, 1)`, &events)
	mustScan(t, `SELECT purge_access_logs(`+itoa(one)+`, 1)`, &logs)
	if events != 2 || logs != 2 {
		t.Fatalf("purged %d event(s) and %d log(s), want the two 60-day rows of each at company one",
			events, logs)
	}

	for _, site := range []int64{siteA, siteB} {
		var e, l int
		mustScan(t, `SELECT count(*) FROM events WHERE site_id = `+itoa(site), &e)
		mustScan(t, `SELECT count(*) FROM access_logs WHERE site_id = `+itoa(site), &l)
		if e != 1 || l != 1 {
			t.Errorf("site %d kept %d event(s) and %d log(s), want its one recent row of each",
				site, e, l)
		}
	}
	if e, l := countDoorHistory(t, two); e != 1 || l != 1 {
		t.Errorf("company two lost history to a purge aimed at company one: %d event(s), %d log(s)", e, l)
	}
}

// TestRetentionPurgeIsIdempotent: a second pass finds nothing and changes
// nothing, so an overlapping or repeated run is harmless.
func TestRetentionPurgeIsIdempotent(t *testing.T) {
	newTestEnv(t)
	company := companyIDBySlug(t, "two")
	site := siteIDByName(t, "Site C")
	seedDoorEvent(t, company, site, daysAgo(400))
	seedDoorEvent(t, company, site, daysAgo(10))
	seedAccessLog(t, company, site, daysAgo(400))
	seedAccessLog(t, company, site, daysAgo(10))

	if events, logs := purgeDoorHistory(t, 365); events != 1 || logs != 1 {
		t.Fatalf("first pass purged %d event(s) and %d log(s), want 1 and 1", events, logs)
	}
	if events, logs := purgeDoorHistory(t, 365); events != 0 || logs != 0 {
		t.Fatalf("second pass purged %d event(s) and %d log(s), want nothing", events, logs)
	}
	if events, logs := countDoorHistory(t, company); events != 1 || logs != 1 {
		t.Errorf("after two passes kept %d event(s) and %d log(s), want 1 and 1", events, logs)
	}
}

// TestAuditRetentionHasItsOwnDefault: door-history retention is not audit
// retention. The audit trail has its own platform default (039) and floor;
// with no default it keeps everything, and a company's own window wins.
func TestAuditRetentionHasItsOwnDefault(t *testing.T) {
	newTestEnv(t)
	unconfigured := companyIDBySlug(t, "two")
	configured := companyIDBySlug(t, "one")
	mustExec(t, `UPDATE companies SET audit_retention_days = 30 WHERE id = $1`, configured)

	for _, company := range []int64{unconfigured, configured} {
		for _, age := range []int{4000, 100, 10} {
			mustExec(t, `INSERT INTO audit_events (company_id, action, actor_email, occurred_at)
			             VALUES ($1, 'SITE_CREATED', 'ops@example.com', $2)`, company, daysAgo(age))
		}
	}
	countAudit := func(company int64) int {
		var n int
		mustScan(t, `SELECT count(*) FROM audit_events WHERE company_id = `+itoa(company), &n)
		return n
	}

	// Purging door history never touches the audit trail.
	purgeDoorHistory(t, 1)
	if countAudit(unconfigured) != 3 {
		t.Fatalf("a door-history purge removed audit records")
	}

	// No audit default: the unconfigured company keeps everything; the
	// configured one loses only what is past its own 30 days.
	if _, err := database.PurgeAuditEvents(context.Background(), 0); err != nil {
		t.Fatalf("purging audit events: %v", err)
	}
	if got := countAudit(unconfigured); got != 3 {
		t.Errorf("with no default the unconfigured company kept %d audit records, want 3", got)
	}
	if got := countAudit(configured); got != 1 {
		t.Errorf("the 30-day company kept %d audit records, want 1", got)
	}

	// The 365-day default: only the 4000-day record goes.
	if _, err := database.PurgeAuditEvents(context.Background(), 365); err != nil {
		t.Fatalf("purging audit events: %v", err)
	}
	if got := countAudit(unconfigured); got != 2 {
		t.Errorf("with a 365-day default the unconfigured company kept %d audit records, want 2", got)
	}

	// Below the 30-day floor a default is ignored, exactly as a company's own
	// setting may not go below it.
	if _, err := database.PurgeAuditEvents(context.Background(), 5); err != nil {
		t.Fatalf("purging audit events: %v", err)
	}
	if got := countAudit(unconfigured); got != 2 {
		t.Errorf("a 5-day default (below the floor) purged audit records: %d left, want 2", got)
	}
}

// TestEventRetentionDefaultIsAYearAndConfigurable pins the shipped default
// and the two ways a deployment changes it.
func TestEventRetentionDefaultIsAYearAndConfigurable(t *testing.T) {
	t.Setenv("EVENT_RETENTION_DEFAULT_DAYS", "")
	if got := maintenance.LoadConfig().EventRetentionDefaultDays; got != 365 {
		t.Errorf("default = %d days, want 365", got)
	}

	t.Setenv("EVENT_RETENTION_DEFAULT_DAYS", "730")
	if got := maintenance.LoadConfig().EventRetentionDefaultDays; got != 730 {
		t.Errorf("EVENT_RETENTION_DEFAULT_DAYS=730 gave %d", got)
	}

	t.Setenv("EVENT_RETENTION_DEFAULT_DAYS", "0")
	if got := maintenance.LoadConfig().EventRetentionDefaultDays; got != 0 {
		t.Errorf("EVENT_RETENTION_DEFAULT_DAYS=0 gave %d, want 0 (no default)", got)
	}

	t.Setenv("AUDIT_RETENTION_DEFAULT_DAYS", "")
	if got := maintenance.LoadConfig().AuditRetentionDefaultDays; got != 365 {
		t.Errorf("audit default = %d days, want 365", got)
	}

	// Garbage falls back to the default rather than to "keep for ever" or to a
	// window nobody chose.
	t.Setenv("EVENT_RETENTION_DEFAULT_DAYS", "-5")
	if got := maintenance.LoadConfig().EventRetentionDefaultDays; got != 365 {
		t.Errorf("EVENT_RETENTION_DEFAULT_DAYS=-5 gave %d, want the 365 default", got)
	}
}
