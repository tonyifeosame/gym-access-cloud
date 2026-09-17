package assistant

import (
	"net/http"
	"time"

	"access-terminal-cloud-api/models"
)

// Fingerprint enrolment tools.
//
// THE CAPTURE HAPPENS IN THE CONSOLE'S OWN SCREEN, NOT HERE. What the
// assistant can do is ask one terminal to enter enrolment mode -- after the
// operator has approved which terminal, with the person standing at it --
// and then follow the state, or stop it. Every result carries a hand-off to
// the person's page with the enrolment dialog open (?enrol=1), where the
// existing EnrollmentWorkflow shows the live states, cancels, and retries.
// Nothing about the hardware interaction is reproduced in the agent.
//
// Routes: POST /console/terminals/:serial/enrollments (ConsoleStartEnrollment),
// GET /console/people/:id/enrollment (ConsoleGetPersonEnrollment),
// DELETE /console/people/:id/enrollment (ConsoleCancelEnrollment).

func registerEnrollmentTools(r *Registry) {
	r.Register(&Tool{
		Name: "start_enrollment",
		Description: "Ask ONE terminal to capture a person's fingerprint. Only call this after the person " +
			"exists and you know which terminal they are standing at (use list_terminals to find it). " +
			"The operator must approve it before the terminal enters enrolment mode; the capture itself " +
			"then happens in the console's enrolment screen, which opens for them.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "serial", Type: "string", Description: "The serial number of the terminal the person is standing at.", Required: true, MaxLen: 64, Identifier: true},
		},
		Destructive: false, Idempotent: false,
		MinRole: models.RoleManager,
		Domains: []string{DomainPeople, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			person, _, fail := get(t, consolePeople+"/"+Segment(a.String("external_id")))
			if fail != nil {
				return nil, failError(fail)
			}
			term, _, fail := get(t, consoleTerminals+"/"+Segment(a.String("serial")))
			if fail != nil {
				return nil, failError(fail)
			}
			return &ConfirmationPlan{
				Consequence: enrollmentConsequence(personLabel(person), terminalLabel(term), str(term, "site_name")),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := post(t, http.MethodPost,
				consoleTerminals+"/"+Segment(a.String("serial"))+"/enrollments",
				object{"external_id": a.String("external_id")})
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"enrollment": pick(m, enrolFields...),
				"note": "The terminal has been asked to capture the fingerprint. The operator can follow it " +
					"in the enrolment screen; use wait_for_enrollment to check the outcome.",
			})
			out.Handoff = handoffEnrolment(a.String("external_id"), personLabel(object{"full_name": str(m, "full_name"), "external_id": a.String("external_id")}))
			return out
		},
	})

	r.Register(&Tool{
		Name: "wait_for_enrollment",
		Description: "Wait up to timeout_s seconds (default 30, max 60) for a person's enrolment to reach a " +
			"final state -- completed, failed, expired or cancelled -- and return it. Returns the current " +
			"state at the timeout if it is still in progress.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "timeout_s", Type: "integer", Description: "Seconds to wait (1-60).", Min: intPtr(1), Max: intPtr(maxWaitSeconds)},
		},
		ReadOnly: true, Idempotent: true,
		MinRole:     models.RoleViewer,
		MaxDuration: (maxWaitSeconds + 5) * time.Second,
		Run: func(t *Turn, a Args) Outcome {
			timeout := a.Int("timeout_s")
			if timeout == 0 {
				timeout = 30
			}
			id := a.String("external_id")
			out := pollUntil(t, time.Duration(timeout)*time.Second, 2*time.Second, func(p *Turn) (Outcome, bool) {
				o := enrollmentState(p, id)
				if o.IsError {
					return o, true
				}
				result, _ := o.Result.(object)
				e, _ := result["enrollment"].(object)
				switch str(e, "status") {
				case "COMPLETED", "FAILED", "EXPIRED", "CANCELLED":
					return o, true
				case "":
					return o, true
				}
				return o, false
			})
			if !out.IsError {
				out.Handoff = handoffEnrolment(id, id)
			}
			return out
		},
	})

	r.Register(&Tool{
		Name: "cancel_enrollment",
		Description: "Stop a person's in-progress fingerprint enrolment, whichever terminal it was sent to. " +
			"Fails if none is in progress.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
		},
		Idempotent: true,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainPeople, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			id := a.String("external_id")
			m, resp, fail := post(t, http.MethodDelete, consolePeople+"/"+Segment(id)+"/enrollment", nil)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"enrollment": pick(m, enrolFields...),
				"note":       "Cancelled. The terminal leaves enrolment mode on its next poll.",
			})
			out.Handoff = handoffPerson(id, id)
			return out
		},
	})
}
