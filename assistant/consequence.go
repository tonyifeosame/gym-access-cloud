package assistant

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"access-terminal-cloud-api/models"
)

// The wording an operator reads before approving a consequential action.
//
// ONE SOURCE. These sentences are what the assistant's confirmation card
// shows; the card holds no wording of its own. They say what the equivalent
// console dialogs say (PersonAccessPanel's Grant access and Remove rule
// dialogs; the Add-a-person enrolment step), because an operator who meets
// the same action in two places must read the same consequence in both.
// Change them here, and only here.

// Sentences shared with the console's access dialogs.
const (
	keepOutWinsSentence = "Keep out always wins, even if another rule lets them in."

	companyWideWarningTitle = "This covers terminals that do not exist yet"
	companyWideWarningBody  = "A rule for everywhere applies to every terminal you have and every one installed later. " +
		"That is often what somebody wants for staff, and rarely what they want for a visitor."

	revokeAllowBody = "This person loses access to %s. If no other rule covers that place, they will be refused."
	revokeDenyBody  = "The rule keeping this person out of %s is removed. If another rule allows them there, " +
		"they will be admitted from the next sync."
	syncLagSentence = "Terminals learn about this on their next sync rather than instantly, and a terminal that is " +
		"offline keeps its cached answer until it reconnects — bounded by its site's offline policy."
)

// grantConsequence is the confirmation for grant_access.
func grantConsequence(personLabel, effect, scopeLabel, whenLabel, windowLabel string) models.AssistantConsequence {
	var c models.AssistantConsequence
	if effect == "DENY" {
		c.Title = fmt.Sprintf("Keep %s out of %s?", personLabel, scopeLabel)
		c.Body = fmt.Sprintf("Adds a rule that keeps %s out of %s, %s%s. %s", personLabel, scopeLabel,
			whenLabel, windowLabel, keepOutWinsSentence)
	} else {
		c.Title = fmt.Sprintf("Let %s in at %s?", personLabel, scopeLabel)
		c.Body = fmt.Sprintf("Adds a rule that lets %s in at %s, %s%s. %s", personLabel, scopeLabel,
			whenLabel, windowLabel, "A Keep out rule elsewhere still wins over it.")
	}
	return c
}

// revokeConsequence is the confirmation for revoke_access.
func revokeConsequence(personLabel, effect, scopeLabel string) models.AssistantConsequence {
	if effect == "DENY" {
		return models.AssistantConsequence{
			Title:    fmt.Sprintf("Remove the keep-out rule for %s at %s?", personLabel, scopeLabel),
			Body:     fmt.Sprintf(revokeDenyBody, scopeLabel),
			Warnings: []string{syncLagSentence},
		}
	}
	return models.AssistantConsequence{
		Title:    fmt.Sprintf("Remove %s's access to %s?", personLabel, scopeLabel),
		Body:     fmt.Sprintf(revokeAllowBody, scopeLabel),
		Warnings: []string{syncLagSentence},
	}
}

// enrollmentConsequence is the confirmation for start_enrollment.
//
// STARTING IS NEVER AUTOMATIC. Asking to "add and enrol" somebody adds them;
// it does not put a terminal into enrolment mode. That happens only after the
// operator has read which terminal, confirmed the person is standing at it,
// and approved. The capture itself then runs in the console's enrolment
// screen, which the hand-off opens.
func enrollmentConsequence(personLabel, terminalLabel, siteLabel string) models.AssistantConsequence {
	where := terminalLabel
	if siteLabel != "" {
		where += " (" + siteLabel + ")"
	}
	return models.AssistantConsequence{
		Title: fmt.Sprintf("Ready to start fingerprint enrollment at %s?", where),
		Body: fmt.Sprintf("%s must be standing at %s. Only that terminal will enter enrolment mode; "+
			"no other terminal is affected. Their fingerprint is stored on that terminal only — "+
			"AccessLink never keeps a copy. The enrolment screen opens next so you can follow the capture.",
			personLabel, where),
	}
}

// deactivateConsequence is the confirmation for set_person_active(false).
// The words are PersonDetailPage's Deactivate dialog.
func deactivateConsequence(personLabel string) models.AssistantConsequence {
	return models.AssistantConsequence{
		Title: fmt.Sprintf("Deactivate %s?", personLabel),
		Body: "Every terminal in your company will be told to stop admitting them. " +
			"The record and any enrolled credential are kept.",
		Warnings: []string{"Reversible — you can activate them again from this same screen. " +
			"Terminals apply the change on their next sync, so it is not instant on hardware that is currently offline."},
	}
}

// scheduleChangeConsequence is the confirmation for update_schedule when
// rules depend on the schedule. The words are SchedulesPage's warning.
func scheduleChangeConsequence(name string, dependents int, summary string) models.AssistantConsequence {
	return models.AssistantConsequence{
		Title: fmt.Sprintf("Change %s?", name),
		Body:  summary,
		Warnings: []string{fmt.Sprintf("This changes every rule that uses it: %s refer to this schedule. "+
			"Widening a window widens all of them at once, everywhere.", fmtCount(dependents, "access rule"))},
	}
}

// assistantReason marks a reason the assistant sends on the operator's
// behalf. The audit trail already records the User-Agent
// (AccessLink-Assistant/1 ...); the reason field is the human-readable
// mark, and it keeps the operator's own words when they gave any.
func assistantReason(operatorWords string) string {
	words := strings.TrimSpace(operatorWords)
	if words == "" {
		return "assistant"
	}
	return "assistant: " + words
}

// newRequestID mints the id an internal request carries -- the same shape
// the logging middleware mints, so audit rows look alike whoever caused them.
func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "assistant"
	}
	return hex.EncodeToString(raw)
}

// ---------------------------------------------------------------------------
// Phase 2b
// ---------------------------------------------------------------------------

// deviceTestConsequence is the confirmation for run_device_test.
//
// THE POINT OF THE CARD IS THE ROOM, not the terminal. A buzzer sounding at
// an access point with people at it is perceived by them, and the operator
// approving it should be told where "there" is before they do -- which is why
// the place is in the title rather than the body.
func deviceTestConsequence(terminalLabel, siteName, target, status string) models.AssistantConsequence {
	where := terminalLabel
	if siteName != "" {
		where += " (" + siteName + ")"
	}
	var what string
	switch target {
	case models.DeviceTestBuzzer:
		what = "sound its buzzer"
	case models.DeviceTestDisplay:
		what = "light its display"
	default:
		what = "run a self-test of its buzzer and display"
	}
	c := models.AssistantConsequence{
		Title: fmt.Sprintf("Have %s %s?", where, what),
		Body: fmt.Sprintf("%s will %s when it next checks in, which is usually within a minute. "+
			"Anybody standing at it will notice. It admits nobody, refuses nobody and opens nothing; "+
			"the test lapses unrun if the terminal has not collected it within five minutes.",
			terminalLabel, what),
	}
	if status != "" && status != "ONLINE" {
		c.Warnings = append(c.Warnings, fmt.Sprintf("This terminal is %s rather than online, so it may "+
			"not collect the test before it lapses.", strings.ToLower(status)))
	}
	return c
}

// deleteScheduleConsequence is the confirmation for delete_schedule.
//
// ONLY EVER SHOWN FOR A SCHEDULE NOTHING USES. The platform refuses to delete
// one a rule still references -- the foreign key is ON DELETE SET NULL, which
// on a soft delete would silently widen every rule that used it -- so
// delete_schedule reads the dependent count first and refuses in its own
// words rather than asking the operator to approve something that will be
// turned down. The count is stated on the card regardless, because "nothing
// uses it" is the fact the approval rests on.
func deleteScheduleConsequence(name string, dependents int) models.AssistantConsequence {
	return models.AssistantConsequence{
		Title: fmt.Sprintf("Delete %s?", name),
		Body: fmt.Sprintf("Nothing uses it: %s refer to this schedule, so no one's access changes. "+
			"The schedule itself is gone from the list and a rule cannot be limited to it again "+
			"unless it is recreated.", fmtCount(dependents, "access rule")),
		Warnings: []string{"If an access rule starts using it between now and your approval, the " +
			"deletion is refused rather than quietly widening that rule."},
	}
}

// approveTerminalConsequence is the confirmation for approve_pending_terminal.
//
// The words follow the console's own Add-a-terminal panel: what approving
// does (authorises; mints nothing here), when the unit becomes a terminal,
// and -- for RE_PROVISION -- which existing terminal it replaces, because
// that is the case where approving affects hardware already on a door.
func approveTerminalConsequence(serial, siteName, deviceName, verdict, replaces string) models.AssistantConsequence {
	called := serial
	if deviceName != "" {
		called = deviceName
	}
	c := models.AssistantConsequence{
		Title: fmt.Sprintf("Set %s up at %s?", serial, siteName),
		Body: fmt.Sprintf("This authorises the unit to join your company as %s at %s. It collects its "+
			"credential the next time it checks in, usually within a minute, and from then on it "+
			"admits people by the rules that apply at that site. Nothing is issued now and no "+
			"credential is shown here.", called, siteName),
	}
	if verdict == "RE_PROVISION" {
		replaced := replaces
		if replaced == "" {
			replaced = serial
		}
		// The platform's own words for RE_PROVISION (database/announcements.go):
		// collection ROTATES the credential, and the key that terminal is
		// using right now stops working.
		c.Warnings = append(c.Warnings, fmt.Sprintf("This serial already has a terminal in your company (%s). "+
			"Approving re-provisions it: when the unit collects, its credential is rotated and the key "+
			"that terminal is using right now stops working.", replaced))
	}
	c.Warnings = append(c.Warnings, "Undoing this is reject_pending_terminal, which releases the serial "+
		"so the unit can be set up again.")
	return c
}

// rejectTerminalConsequence is the confirmation for reject_pending_terminal.
//
// TWO ACTIONS IN ONE ROUTE, and the card must not describe them as the same
// thing. Rejecting a unit that is merely waiting turns away hardware nobody
// has committed to; rejecting one that has been approved is the UNDO of that
// approval, and it releases the serial.
func rejectTerminalConsequence(serial, state string) models.AssistantConsequence {
	if state == "APPROVED" {
		return models.AssistantConsequence{
			Title: fmt.Sprintf("Undo the approval of %s?", serial),
			Body: fmt.Sprintf("%s was approved and has not collected its credential yet. This withdraws "+
				"that approval and releases the serial, so the unit can be set up again -- at another "+
				"site, or by somebody else.", serial),
			Warnings: []string{"If the unit is already installed on a door, it will not come into service " +
				"until somebody approves it again."},
		}
	}
	return models.AssistantConsequence{
		Title: fmt.Sprintf("Refuse %s?", serial),
		Body: fmt.Sprintf("%s stops waiting to be set up and disappears from the list. It has no "+
			"credential and no site, so nothing it could do is taken away. If it is switched on again "+
			"it announces itself afresh with a new pairing code.", serial),
	}
}
