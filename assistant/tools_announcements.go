package assistant

import (
	"net/http"

	"access-terminal-cloud-api/models"
)

// Deciding about a terminal that has announced itself.
//
// Routes: GET /console/terminal-announcements/:id (ConsoleGetPendingTerminal,
// MANAGER), POST /console/terminal-announcements/:id/approve
// (ConsoleApproveAnnouncement, ADMIN), POST
// /console/terminal-announcements/:id/reject (ConsoleRejectAnnouncement,
// ADMIN).
//
// TWO DECISIONS, AND NOTHING ELSE OF THE LIFECYCLE. Approving says which site
// a waiting unit belongs to and what it is called; rejecting turns one away,
// and is also the undo for an approval. Both are decisions an administrator
// makes about hardware somebody has already unpacked, and both are reversible
// through each other while the unit has not collected.
//
// ADOPTION IS NOT HERE AND MUST NOT BE. Adopting a unit means typing the
// pairing code shown on its screen, which is a shared secret the model must
// never see, hold or guess -- the route has its own session-keyed rate
// limiter for exactly that reason. The assistant can say a unit is waiting
// and hand the console the screen; the code is typed by a person.
//
// NOR IS ANYTHING THAT MOVES OR ENDS A TERMINAL. Claim codes, credential
// revocation, retirement, release to another company, relocation, Wi-Fi
// recovery and firmware all stay in the console.
//
// THE CREDENTIAL IS NEVER IN PLAY. Approving authorises; the device key is
// generated when the unit comes to collect it, so there is no moment at which
// a secret exists for a projection to leak.

const consoleAnnouncements = "/api/v1/console/terminal-announcements"

// maxRejectReasonBytes is what RejectAnnouncement stores: anything longer is
// cut, by bytes, in database/announcements.go. The assistant sends no more
// than this so that the cut never happens there.
const maxRejectReasonBytes = 200

func registerAnnouncementTools(r *Registry) {
	r.Register(&Tool{
		Name: "approve_pending_terminal",
		Description: "Set up a terminal that is waiting: choose which site it belongs to and what it is " +
			"called. It joins the fleet when it next checks in. The operator must approve.",
		Params: []Param{
			{Name: "pending_id", Type: "string", Description: "The waiting terminal's id, from list_pending_terminals.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "site_id", Type: "string", Description: "The site it belongs to, from list_sites.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "device_name", Type: "string", Description: "Optional: what to call it, e.g. Reception. Blank uses its serial number.", MaxLen: 100},
		},
		// NOT Destructive: approving creates authorisation rather than
		// destroying anything, and reject_pending_terminal undoes it while
		// the unit has not collected. It asks because it decides what a piece
		// of hardware on a door belongs to.
		Destructive: false, Idempotent: false,
		MinRole: models.RoleAdmin,
		// The site's terminal count moves, so the sites cache goes too --
		// the same three the console's own approve mutation invalidates.
		Domains: []string{DomainPendingTerminals, DomainTerminals, DomainSites, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			pending, err := pendingTerminal(t, a.String("pending_id"))
			if err != nil {
				return nil, err
			}
			if state := str(pending, "state"); state != "ADOPTED" {
				return nil, errText(unapprovableText(str(pending, "serial_number"), state))
			}
			site, _, fail := get(t, "/api/v1/console/sites/"+Segment(a.String("site_id")))
			if fail != nil {
				return nil, failError(fail)
			}
			replaces := ""
			if existing, ok := pending["existing_terminal"].(object); ok {
				replaces = terminalLabel(existing)
			}
			return &ConfirmationPlan{
				Consequence: approveTerminalConsequence(str(pending, "serial_number"), str(site, "name"),
					a.String("device_name"), str(pending, "verdict"), replaces),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			body := object{"site_id": a.String("site_id")}
			if name := a.String("device_name"); name != "" {
				body["device_name"] = name
			}
			m, resp, fail := post(t, http.MethodPost,
				consoleAnnouncements+"/"+Segment(a.String("pending_id"))+"/approve", body)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"pending_terminal": pick(m, pendingDecisionFields...),
				"note": "Approved. It becomes a terminal when it next checks in and collects its " +
					"credential; until then it is still on the waiting list.",
			})
			out.Handoff = handoffPendingTerminals()
			return out
		},
	})

	r.Register(&Tool{
		Name: "reject_pending_terminal",
		Description: "Turn away a terminal that is waiting to be set up, or undo an approval that has not " +
			"been collected yet. The serial is released, so the unit can be set up again later. The " +
			"operator must approve.",
		Params: []Param{
			{Name: "pending_id", Type: "string", Description: "The waiting terminal's id, from list_pending_terminals.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "reason", Type: "string", Description: "Optional: the operator's own words on why.", MaxLen: 200},
		},
		Destructive: true, Idempotent: false,
		MinRole: models.RoleAdmin,
		Domains: []string{DomainPendingTerminals, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			pending, err := pendingTerminal(t, a.String("pending_id"))
			if err != nil {
				return nil, err
			}
			state := str(pending, "state")
			if state != "ADOPTED" && state != "APPROVED" {
				return nil, errText(unapprovableText(str(pending, "serial_number"), state))
			}
			return &ConfirmationPlan{
				Consequence: rejectTerminalConsequence(str(pending, "serial_number"), state),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			body := object{}
			if reason := a.String("reason"); reason != "" {
				// BOUNDED HERE, because the route truncates rather than
				// refusing and does it with a byte slice. See
				// assistantReasonWithin.
				body["reason"] = assistantReasonWithin(reason, maxRejectReasonBytes)
			}
			m, resp, fail := post(t, http.MethodPost,
				consoleAnnouncements+"/"+Segment(a.String("pending_id"))+"/reject", body)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"pending_terminal": pick(m, pendingDecisionFields...),
				"note": "Rejected. The serial is released: if the unit is switched on again it " +
					"announces itself afresh with a new pairing code, which somebody types in the console.",
			})
			out.Handoff = handoffPendingTerminals()
			return out
		},
	})
}

// pendingTerminal reads one waiting terminal, projected, for a confirmation
// to describe. The projection is the same allow-list the list tool uses plus
// the existing terminal a re-provision would replace -- no pairing code, no
// address, no adopter, no capabilities.
func pendingTerminal(t *Turn, id string) (object, error) {
	m, _, fail := get(t, consoleAnnouncements+"/"+Segment(id))
	if fail != nil {
		return nil, failError(fail)
	}
	out := pick(m, pendingDecisionFields...)
	if existing, ok := m["existing_terminal"].(object); ok {
		out["existing_terminal"] = pick(existing, "serial_number", "device_name", "site_name", "status")
	}
	return out, nil
}

// unapprovableText says why a decision cannot be made, in the state's own
// words rather than the route's status code.
func unapprovableText(serial, state string) string {
	switch state {
	case "PENDING":
		return serial + " is waiting, but nobody has adopted it yet: somebody must type the pairing " +
			"code shown on its screen, in the console. That is not something the assistant can do."
	case "SUPERSEDED":
		return serial + " announced itself again, so this entry is stale. Look at the waiting list " +
			"for the current one."
	case "APPROVED":
		return serial + " has already been approved. Use reject_pending_terminal to undo that if it was wrong."
	case "REJECTED":
		return serial + " has already been rejected. If the unit is switched on again it announces itself afresh."
	case "EXPIRED":
		return serial + " is no longer waiting: its pairing code has lapsed. Check the terminal's screen for a new code."
	case "COLLECTED":
		return serial + " has already been set up and is a terminal now."
	default:
		return serial + " is not waiting to be set up (it is " + state + ")."
	}
}
