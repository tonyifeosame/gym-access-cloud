package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"access-terminal-cloud-api/models"
)

// Terminal roster capacity (FW-01, SYN-01).
//
// ---------------------------------------------------------------------------
// WHAT THIS FILE PREVENTS
// ---------------------------------------------------------------------------
//
// A FULL_SYNC job carries the authoritative roster and means "your local set
// should be exactly this". The firmware REFUSES one longer than its member
// table rather than truncating it -- correctly, because a short roster reads as
// a list of deletions. The refusal is the whole job, so the terminal keeps the
// roster it already had, the job burns its ten attempts, parks FAILED, and the
// door goes on admitting a set of people nobody chose.
//
// The server used to generate that snapshot without ever asking whether it
// would fit. It no longer does.
//
// ---------------------------------------------------------------------------
// KNOWN CAPACITY IS ENFORCED. UNKNOWN CAPACITY IS NOT.
// ---------------------------------------------------------------------------
//
// This distinction is the entire safety argument, so it is worth being explicit
// about.
//
//   - The terminal has REPORTED a capacity and the roster exceeds it. This is
//     provable: the snapshot cannot be applied by that terminal. The server
//     declines to queue it, records the overflow where an operator will see it,
//     and leaves the existing queue alone. Declining is strictly better than
//     queueing: the end state is identical -- the terminal keeps its old roster
//     either way -- but this one is visible and does not wedge the queue behind
//     a job that can never succeed.
//
//   - The terminal has reported NOTHING, which is every unit in the field
//     today. The server does not know the ceiling and MUST NOT guess one. A
//     guess of 64 would refuse rosters a 256-row terminal holds happily, and
//     that is a working installation broken by an upgrade. So an unknown
//     capacity is never enforced. It is logged when the roster passes the
//     smallest ceiling any shipped firmware has had, because "this might not
//     fit and we cannot tell" is worth saying once.
//
// Nothing here changes the wire. A terminal that never reports a capacity sees
// exactly the protocol it saw before this file existed.

// AssumedMemberCapacity is the smallest member table any shipped firmware has
// had, and it is used for WARNING ONLY -- never to refuse a snapshot.
//
// 64 was the constant when the audit was written. The table is 256 now that the
// records moved behind a store, and the number is expected to keep moving,
// which is exactly why the server refuses to hold an opinion about it. Treat
// this as "below this, nothing can possibly be wrong", not as a limit.
const AssumedMemberCapacity = 64

// ErrRosterExceedsCapacity is the sentinel behind every capacity refusal, for
// callers that only need to branch on the kind.
var ErrRosterExceedsCapacity = errors.New("roster exceeds terminal capacity")

// RosterCapacityError says which terminal, how many people, and how many it can
// hold. All three are in the message because an operator reading "over
// capacity" learns nothing about how much hardware they need.
type RosterCapacityError struct {
	DeviceID   int64
	Serial     string
	RosterSize int
	Capacity   int
}

func (e *RosterCapacityError) Error() string {
	return fmt.Sprintf(
		"terminal %s can hold %d people and its permissions cover %d",
		e.Serial, e.Capacity, e.RosterSize)
}

func (e *RosterCapacityError) Unwrap() error { return ErrRosterExceedsCapacity }

// TerminalCapacity is what the server knows about one terminal's ceiling and
// how much of it is spoken for.
type TerminalCapacity struct {
	DeviceID int64
	Serial   string

	// Capacity is meaningful only when Known is true. A terminal that has never
	// reported one leaves this zero, which is NOT "holds nobody".
	Capacity int
	Known    bool

	// RosterSize is how many people this terminal's permissions currently
	// cover, evaluated by the same predicate that builds the roster.
	RosterSize int
}

// Exceeded reports a PROVABLE overflow: the terminal said what it can hold, and
// the roster is bigger than that.
func (c TerminalCapacity) Exceeded() bool {
	return c.Known && c.RosterSize > c.Capacity
}

// AtRisk reports the softer case: capacity is unknown and the roster has passed
// the smallest ceiling any firmware has shipped with. It is a reason to log, not
// a reason to refuse.
func (c TerminalCapacity) AtRisk() bool {
	return !c.Known && c.RosterSize > AssumedMemberCapacity
}

// InspectTerminalCapacity measures one terminal against its roster.
//
// READ ONLY, and deliberately so. It runs inside whatever transaction is about
// to write a snapshot, and a check that also wrote would lose its record the
// moment that transaction rolled back -- which is precisely when the record
// matters. Recording is RecordRosterOverflow's job, called by the caller after
// the rollback.
func InspectTerminalCapacity(q rowQuerier, deviceID int64) (TerminalCapacity, error) {
	out := TerminalCapacity{DeviceID: deviceID}

	var capacity sql.NullInt64
	err := q.QueryRow(`
		SELECT d.serial_number, d.member_capacity
		  FROM devices d
		 WHERE d.id = $1 AND d.deleted_at IS NULL`, deviceID).
		Scan(&out.Serial, &capacity)
	if err != nil {
		return out, err
	}
	if capacity.Valid {
		out.Capacity = int(capacity.Int64)
		out.Known = out.Capacity > 0
	}

	// The SAME predicate the snapshot uses, so the count and the roster it is
	// counting cannot drift apart. A second copy of this rule would be a second
	// rule.
	err = q.QueryRow(`
		SELECT count(*)
		  FROM devices d
		  JOIN sites s ON s.id = d.site_id
		  JOIN people p ON p.company_id = s.company_id
		 WHERE d.id = $1
		   AND d.deleted_at IS NULL
		   AND p.deleted_at IS NULL
		   AND `+rosterMembershipPredicate, deviceID).Scan(&out.RosterSize)
	if err != nil {
		return out, err
	}

	return out, nil
}

// guardRosterCapacityTx is the check every snapshot path makes before it queues
// one. Returns a *RosterCapacityError when the overflow is provable, nil
// otherwise -- including when the capacity is unknown, which is logged instead.
func guardRosterCapacityTx(q rowQuerier, deviceID int64) error {
	capacity, err := InspectTerminalCapacity(q, deviceID)
	if err != nil {
		// A capacity check that cannot be completed must not stop a snapshot.
		// Refusing here would turn a transient database problem into a fleet
		// that stops converging, which is a worse failure than the one this
		// guard exists to prevent.
		log.Printf("capacity check skipped for device %d: %v", deviceID, err)
		return nil
	}

	if capacity.Exceeded() {
		return &RosterCapacityError{
			DeviceID:   deviceID,
			Serial:     capacity.Serial,
			RosterSize: capacity.RosterSize,
			Capacity:   capacity.Capacity,
		}
	}

	if capacity.AtRisk() {
		log.Printf(
			"terminal %s (device %d) has %d permitted people and has never reported "+
				"a capacity; if its member table is smaller than that, its FULL_SYNC "+
				"will be refused wholesale",
			capacity.Serial, deviceID, capacity.RosterSize)
	}

	return nil
}

// RecordRosterOverflow makes a declined snapshot visible.
//
// Written OUTSIDE the transaction that was rolled back, on purpose: the whole
// point is that the refusal leaves a trace even though the write it was
// guarding did not happen.
//
// Three surfaces, because they answer different questions:
//
//   - devices.roster_overflow_* is the state, so the console can show a terminal
//     as over capacity rather than merely behind.
//   - last_apply_error is the existing "why is this terminal unhappy" field
//     (SYN-01), so an operator finds this where they already look for the
//     failures it sits beside.
//   - a typed event, so it appears on the trail with a timestamp and can be
//     alarmed on. An overflow is a fleet condition, not a door decision.
func RecordRosterOverflow(deviceID int64, rosterSize, capacity int) error {
	_, err := DB.Exec(`
		UPDATE devices
		   SET roster_overflow_at = CURRENT_TIMESTAMP,
		       roster_overflow_count = $2
		 WHERE id = $1`, deviceID, rosterSize)
	if err != nil {
		return err
	}

	reason := fmt.Sprintf(
		"roster of %d exceeds the terminal's reported capacity of %d; "+
			"snapshot withheld", rosterSize, capacity)
	if err := RecordTerminalApplyFailure(deviceID, reason); err != nil {
		return err
	}

	var companyID, siteID int64
	if err := DB.QueryRow(`
		SELECT s.company_id, d.site_id
		  FROM devices d JOIN sites s ON s.id = d.site_id
		 WHERE d.id = $1`, deviceID).Scan(&companyID, &siteID); err != nil {
		return err
	}

	// Best effort from here. The state and the error field are already written,
	// and failing the caller because the trail could not be appended would turn
	// a visibility improvement into an outage.
	if _, err := RecordAccessEvent(AccessEvent{
		CompanyID: companyID,
		SiteID:    siteID,
		DeviceID:  deviceID,
		EventType: models.EventRosterOverflow,
		Decision:  models.DecisionError,
		Payload: map[string]any{
			"roster_size": rosterSize,
			"capacity":    capacity,
			"shortfall":   rosterSize - capacity,
		},
	}); err != nil {
		LogEventFailure(companyID, deviceID, models.EventRosterOverflow, err)
	}

	return nil
}

// ClearRosterOverflow removes the flag once a terminal fits again.
//
// Called on the successful snapshot path rather than on a schedule, because the
// moment a roster fits is the moment a snapshot is queued for it, and a stale
// "over capacity" badge is its own kind of lie.
func ClearRosterOverflow(deviceID int64) error {
	_, err := DB.Exec(`
		UPDATE devices
		   SET roster_overflow_at = NULL, roster_overflow_count = NULL
		 WHERE id = $1 AND roster_overflow_at IS NOT NULL`, deviceID)
	return err
}

// ReviewTerminalCapacity re-measures one terminal and updates the overflow flag.
//
// WHY THIS EXISTS SEPARATELY FROM THE SNAPSHOT GUARD. The guard fires when
// somebody asks for a snapshot -- a resync, a relocation, or a backlog past the
// compaction threshold. A terminal can sit over capacity for a long time without
// any of those happening, and "visible if you happen to poke it" is not
// visible.
//
// So this runs when a terminal REPORTS A CAPACITY IT HAD NOT REPORTED BEFORE,
// or reports a different one -- which in practice is once per boot. It is
// deliberately NOT run on every heartbeat: counting a roster means evaluating
// the permission predicate, and doing that per terminal per minute is a cost
// with no new information in it, because the answer only changes when
// permissions change or the hardware does.
//
// Best effort by contract. The caller is a heartbeat, and a terminal's liveness
// reporting must not fail because a capacity review could not be completed.
func ReviewTerminalCapacity(deviceID int64) error {
	capacity, err := InspectTerminalCapacity(DB, deviceID)
	if err != nil {
		return err
	}

	if capacity.Exceeded() {
		return RecordRosterOverflow(deviceID, capacity.RosterSize, capacity.Capacity)
	}
	return ClearRosterOverflow(deviceID)
}
