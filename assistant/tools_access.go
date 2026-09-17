package assistant

import (
	"net/http"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// Access questions: would this person get in, and why were they refused.
//
// Routes: POST /console/terminals/:serial/evaluate (ConsoleEvaluateAccess),
// GET /console/events (ConsoleListEvents), GET /console/people/:id/permissions
// and /credentials, GET /console/onboarding (ConsoleOnboardingState).
//
// THE EVALUATION IS A READ THAT LIVES ON A WRITE ROUTE. It takes a body and
// so is a POST, and the router mounts it at MANAGER with CSRF; it records no
// event by design. The tools here are MANAGER for that reason alone, and the
// router is not loosened for them.

func registerAccessTools(r *Registry) {
	r.Register(&Tool{
		Name: "evaluate_access",
		Description: "Would this person be let in at this terminal, now or at a given moment, and why. " +
			"A preview: it records no event.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "at", Type: "string", Description: "Optional instant to evaluate at (RFC 3339). Blank means now.", MaxLen: 40},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleManager,
		Run: func(t *Turn, a Args) Outcome {
			at, err := optionalInstant(a.String("at"))
			if err != nil {
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
			}
			decision, resp, fail := evaluate(t, a.String("serial"), a.String("external_id"), at)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, decision)
		},
	})

	r.Register(&Tool{
		Name: "explain_denial",
		Description: "Why a person was refused: finds their latest refusal (or the one at a given terminal), " +
			"their rules, where their fingerprint is enrolled, and what the decision would be at that " +
			"moment. One call in place of several.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "serial", Type: "string", Description: "Optional: only refusals at this terminal.", MaxLen: 64, Identifier: true},
		},
		ReadOnly: true, Idempotent: true,
		MinRole: models.RoleManager,
		Run: func(t *Turn, a Args) Outcome {
			// AT MOST SIX REQUESTS, in a fixed order: person, refusal, rules,
			// credentials, evaluation. No loop, no paging.
			id := a.String("external_id")
			person, _, fail := get(t, consolePeople+"/"+Segment(id))
			if fail != nil {
				return *fail
			}
			events, _, fail := get(t, Query(consoleEvents, map[string]string{
				"external_id": id, "serial": a.String("serial"), "decision": models.DecisionDenied, "limit": "1",
			}))
			if fail != nil {
				return *fail
			}
			refusals := pickList(events, "events", eventFields...)
			rules, _, fail := get(t, consolePeople+"/"+Segment(id)+"/permissions")
			if fail != nil {
				return *fail
			}
			creds := personCredentials(t, id)
			if creds.IsError {
				return creds
			}
			// The last successful read, for the record when no evaluation runs.
			lastRead := Response{Status: creds.HTTPStatus, Route: creds.Route, RequestID: creds.RequestID}

			out := object{
				"person":       pick(person, personFields...),
				"access_rules": pickList(rules, "permissions", ruleFields...),
				"credentials":  creds.Result.(object)["credentials"],
			}
			findings := []string{}
			if !boolOf(person, "active") {
				findings = append(findings, "The person is inactive: every terminal refuses them until they are activated.")
			}
			if len(refusals) == 0 {
				out["refusal"] = nil
				findings = append(findings, "No refusal is recorded for this person"+atTerminal(a.String("serial"))+".")
				out["findings"] = findings
				return succeeded(lastRead, out)
			}
			refusal := refusals[0]
			out["refusal"] = refusal
			serial := str(refusal, "device_serial")
			at, _ := optionalInstant(str(refusal, "occurred_at"))
			decision, resp, fail := evaluate(t, serial, id, at)
			if fail != nil {
				// The terminal may be gone or outside the operator's sites;
				// the rules and credentials still answer most of the question.
				out["decision"] = nil
				findings = append(findings, "The decision could not be re-evaluated: "+failText(fail))
			} else {
				out["decision"] = decision
				findings = append(findings, explainReason(str(decision, "reason"), str(refusal, "device_name"))...)
			}
			if str(refusal, "reason") != "" && (fail != nil || str(decision, "reason") != str(refusal, "reason")) {
				findings = append(findings, "The terminal's own reason at the time was "+str(refusal, "reason")+".")
			}
			findings = append(findings, credentialFinding(creds.Result.(object), serial)...)
			out["findings"] = findings
			if fail != nil {
				return succeeded(lastRead, out)
			}
			return succeeded(resp, out)
		},
	})

	r.Register(&Tool{
		Name:        "list_people_without_access",
		Description: "How many active people have no access rule at all, and so can get in nowhere.",
		ReadOnly:    true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/onboarding")
			if fail != nil {
				return *fail
			}
			n := num(m, "people_without_access")
			note := "Everyone active has at least one rule."
			if n > 0 {
				note = "Find them with search_people and check each with get_person; a person with no rule is refused everywhere."
			}
			return succeeded(resp, object{"people_without_access": n, "note": note})
		},
	})
}

const consoleEvents = "/api/v1/console/events"

// evaluate runs the preview and projects the decision.
func evaluate(t *Turn, serial, externalID string, at *time.Time) (object, Response, *Outcome) {
	body := object{"external_id": externalID}
	if at != nil {
		body["at"] = at.UTC().Format(time.RFC3339)
	}
	m, resp, fail := post(t, http.MethodPost, consoleTerminals+"/"+Segment(serial)+"/evaluate", body)
	if fail != nil {
		return nil, resp, fail
	}
	return pick(m, decisionFields...), resp, nil
}

func optionalInstant(s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil, errText("at must be an RFC 3339 instant, e.g. 2026-09-15T09:14:00Z")
	}
	return &ts, nil
}

func atTerminal(serial string) string {
	if serial == "" {
		return ""
	}
	return " at " + serial
}

func failText(o *Outcome) string {
	if s, ok := o.Result.(string); ok {
		return s
	}
	return "that could not be looked up"
}

// explainReason puts a decision reason into words the operator can act on.
func explainReason(reason, terminal string) []string {
	where := "that terminal"
	if terminal != "" {
		where = terminal
	}
	switch reason {
	case models.ReasonAllowed:
		return []string{"By the rules as they stand, they would be let in at " + where + " at that moment; the refusal came from the terminal (a fingerprint not enrolled there, or a terminal that had not yet synced the rule)."}
	case models.ReasonNoPermission:
		return []string{"No rule lets them in at " + where + ". Add one with grant_access."}
	case models.ReasonExplicitDeny:
		return []string{"A Keep out rule covers " + where + ", and Keep out always wins."}
	case models.ReasonOutsideSchedule:
		return []string{"Their rule for " + where + " is limited to a schedule, and that moment was outside it."}
	case models.ReasonPermissionExpired:
		return []string{"Their rule for " + where + " had ended by then."}
	case models.ReasonPermissionNotYet:
		return []string{"Their rule for " + where + " had not started yet."}
	case models.ReasonPersonInactive:
		return []string{"They were inactive at the time."}
	case models.ReasonTerminalDisabled:
		return []string{where + " was disabled."}
	case models.ReasonSiteInactive:
		return []string{"The site was inactive."}
	case models.ReasonApplicationOff:
		return []string{"The feature the terminal runs is not enabled for the company."}
	case models.ReasonCredentialUnknown, models.ReasonCredentialRevoked, models.ReasonCredentialSuspended,
		models.ReasonCredentialExpired, models.ReasonCredentialNotYet:
		return []string{"Their credential was not usable: " + reason + "."}
	case "":
		return nil
	default:
		return []string{"The decision reason was " + reason + "."}
	}
}

// credentialFinding says whether a fingerprint is enrolled at the terminal
// in question, which is the usual answer to "why not recognised".
func credentialFinding(creds object, serial string) []string {
	list, _ := creds["credentials"].([]object)
	if len(list) == 0 {
		return []string{"No fingerprint is enrolled for them anywhere. Start one with start_enrollment at the terminal they will use."}
	}
	if serial == "" {
		return nil
	}
	for _, c := range list {
		term, _ := c["terminal"].(object)
		if str(term, "serial_number") == serial && str(c, "state") == models.CredentialActive {
			return nil
		}
	}
	return []string{"Their fingerprint is not enrolled at " + serial + ". A fingerprint works only at the terminal that captured it; enrol them there."}
}
