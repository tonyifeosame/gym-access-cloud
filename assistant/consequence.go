package assistant

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

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

// newRequestID mints the id an internal request carries -- the same shape
// the logging middleware mints, so audit rows look alike whoever caused them.
func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "assistant"
	}
	return hex.EncodeToString(raw)
}
