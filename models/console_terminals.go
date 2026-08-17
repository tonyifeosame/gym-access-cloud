package models

import "time"

// Terminal lifecycle request and response shapes.
//
// Separate from console.go because the operations they describe are a different
// kind of thing from the reads there: each one changes whether a piece of
// hardware can authenticate, and three of them are one-way.

// ConsoleTerminalStateRequest disables or re-enables a terminal.
//
// Disabled is a POINTER so "not supplied" is distinguishable from "false". A
// request that omitted it would otherwise silently mean re-enable, which is the
// opposite of what an operator sending an incomplete body intended.
type ConsoleTerminalStateRequest struct {
	Disabled *bool  `json:"disabled"`
	Reason   string `json:"reason,omitempty"`
}

// ConsoleTerminalRevokeRequest invalidates a terminal's device credential.
//
// The reason is optional but strongly wanted: it lands in the audit trail and in
// disabled_reason, and "why is this terminal dead" is the question the next
// operator asks.
type ConsoleTerminalRevokeRequest struct {
	Reason string `json:"reason,omitempty"`
}

// TerminalRecoveryInstruction is what an operator must actually do to bring a
// revoked terminal back into service, and it is a constant so there is ONE
// answer rather than one per surface that needs to say it.
//
// ---------------------------------------------------------------------------
// WHAT IT USED TO SAY, AND WHY THAT WAS WRONG TWICE
// ---------------------------------------------------------------------------
//
//	"This terminal must re-register with the site provisioning key before it
//	 can authenticate again."
//
// The console appends this VERBATIM to the notification an operator sees the
// moment they revoke, so it is an instruction rather than a description, and it
// failed as one on both counts.
//
// IT OMITTED THE STEP THAT MAKES THE REST WORK. RevokeTerminalCredential sets
// the terminal DISABLED as well as clearing its key hash -- deliberately, so a
// stolen unit cannot quietly re-provision itself the moment somebody runs the
// registration call. Registration and claim redemption both REFUSE a disabled
// terminal (models.ErrDeviceDisabled, and TestClaimRefusesADisabledTerminal
// pins it). So an operator who followed this sentence got a 403 and no
// explanation of it.
//
// IT NAMED THE WRONG CREDENTIAL. Re-provisioning one terminal does not need the
// key that enrols every terminal at that site for ever. A claim code is
// single-use, bound to one serial, short-lived, and never puts the site's
// provisioning secret on an installer's laptop -- which is the entire reason
// claim codes were built. Pointing at the site key first taught the habit they
// exist to break.
//
// The order below is the order the operations must happen in, and neither can be
// skipped.
const TerminalRecoveryInstruction = "Re-enable this terminal, then issue a " +
	"single-use claim code for its serial and redeem it at the unit. " +
	"Re-enabling first is required: provisioning refuses a disabled terminal. " +
	"The site provisioning key is not needed and should not be used."

// ConsoleTerminalRetireRequest soft-deletes a terminal.
type ConsoleTerminalRetireRequest struct {
	Reason string `json:"reason,omitempty"`
}

// ConsoleTerminalMoveRequest reassigns a terminal to another site.
//
// SiteID is the site's PUBLIC id, matching what /console/sites returns. There is
// no company field: a terminal reaches its company through its site, so a move
// across that boundary is not an operation that exists.
type ConsoleTerminalMoveRequest struct {
	SiteID string `json:"site_id"`
}

// ConsoleTerminalRetiredResponse reports what a retirement did.
//
// PendingJobsCancelled is the fact worth returning: a retired terminal will
// never apply what was queued for it, and an operator who is not told would
// believe those changes had been delivered.
type ConsoleTerminalRetiredResponse struct {
	SerialNumber         string `json:"serial_number"`
	Retired              bool   `json:"retired"`
	CredentialCleared    bool   `json:"credential_cleared"`
	PendingJobsCancelled int64  `json:"pending_jobs_cancelled"`
}

// ConsoleTerminalHealth is the operational state the console shows beside the
// inventory row.
//
// SYN-04: the console could show last_sync_at and nothing else -- no backlog
// depth, no failed jobs, no indication a terminal was behind. An operator could
// not tell that people were missing from a door.
type ConsoleTerminalHealth struct {
	PendingJobs int `json:"pending_jobs"`
	FailedJobs  int `json:"failed_jobs"`

	// LastApplyError is the terminal's own words -- "member table full" rather
	// than "failed" -- so the console shows something actionable.
	LastApplyError   string     `json:"last_apply_error,omitempty"`
	LastApplyErrorAt *time.Time `json:"last_apply_error_at,omitempty"`

	// CredentialActive is derived from the key hash being present, which is what
	// authentication actually checks. A console reporting a terminal as
	// credentialed when the lookup would not resolve it is a console that
	// disagrees with the door.
	CredentialActive bool `json:"credential_active"`

	// The site's offline policy, shown on the terminal because that is where an
	// operator is thinking about what happens when the network goes.
	OfflinePolicy       string `json:"offline_policy"`
	OfflineGraceMinutes int    `json:"offline_grace_minutes"`
}
