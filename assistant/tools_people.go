package assistant

import (
	"net/http"
	"strconv"

	"access-terminal-cloud-api/models"
)

// People and access tools.
//
// Routes: GET /console/people (ConsoleListPeople), GET /console/people/:id
// (ConsoleGetPerson), GET /console/people/:id/permissions
// (ConsoleListPersonPermissions), POST /console/people (ConsoleCreatePerson),
// POST /console/people/:id/permissions (ConsoleGrantPermission),
// DELETE /console/permissions/:id (ConsoleRevokePermission).

const consolePeople = "/api/v1/console/people"

func registerPeopleTools(r *Registry) {
	r.Register(&Tool{
		Name: "search_people",
		Description: "Find people by name or ID number. Returns a page of matches with whether each is " +
			"active and whether they have a fingerprint enrolled. Use an empty query to list people.",
		Params: []Param{
			{Name: "query", Type: "string", Description: "Part of a name or ID number.", MaxLen: 100},
			{Name: "limit", Type: "integer", Description: "Rows per page (1-50).", Min: intPtr(1), Max: intPtr(50)},
			{Name: "offset", Type: "integer", Description: "Rows to skip, for the next page.", Min: intPtr(0), Max: intPtr(100000)},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			limit := a.Int("limit")
			if limit == 0 {
				limit = 20
			}
			m, resp, fail := get(t, Query(consolePeople, map[string]string{
				"q": a.String("query"), "limit": strconv.Itoa(limit), "offset": strconv.Itoa(a.Int("offset")),
			}))
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"people":   pickList(m, "people", personFields...),
				"total":    num(m, "total"),
				"has_more": boolOf(m, "has_more"),
			})
		},
	})

	r.Register(&Tool{
		Name:        "get_person",
		Description: "One person: their details, fingerprint status, and every access rule they have.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			id := Segment(a.String("external_id"))
			person, resp, fail := get(t, consolePeople+"/"+id)
			if fail != nil {
				return *fail
			}
			rules, resp2, fail := get(t, consolePeople+"/"+id+"/permissions")
			if fail != nil {
				return *fail
			}
			_ = resp
			out := pick(person, personFields...)
			out["access_rules"] = pickList(rules, "permissions", ruleFields...)
			return succeeded(resp2, out)
		},
	})

	r.Register(&Tool{
		Name: "get_person_enrollment",
		Description: "The state of a person's most recent fingerprint enrolment: waiting for the terminal, " +
			"ready for a finger, completed, failed (with the terminal's words), expired or cancelled.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			return enrollmentState(t, a.String("external_id"))
		},
	})

	r.Register(&Tool{
		Name: "create_person",
		Description: "Add a person the terminals should recognise. Creates the record only: it does not " +
			"start a fingerprint enrolment and grants no access. Fails if the ID number is already used.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The badge, employee, student or reference number the organisation already uses. Unique within the company.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "full_name", Type: "string", Description: "The person's full name.", Required: true, MaxLen: 100},
			{Name: "category", Type: "string", Description: "Optional free-text type, e.g. staff, contractor, student.", MaxLen: 50},
		},
		Idempotent: false,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainPeople, DomainOnboarding, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			body := object{"external_id": a.String("external_id"), "full_name": a.String("full_name")}
			if c := a.String("category"); c != "" {
				body["category"] = c
			}
			person, resp, fail := post(t, http.MethodPost, consolePeople, body)
			if fail != nil {
				return *fail
			}
			out := succeeded(resp, object{
				"person": pick(person, personFields...),
				"note":   "Added. They have no access rules yet and no fingerprint enrolled.",
			})
			out.Handoff = handoffPerson(str(person, "external_id"), personLabel(person))
			return out
		},
	})

	r.Register(&Tool{
		Name: "grant_access",
		Description: "Add an access rule for a person: let them in, or keep them out, everywhere, at one " +
			"site, or at one terminal. The operator must approve it before it is added. A Keep out rule " +
			"always wins over a Let in rule.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "effect", Type: "string", Description: "ALLOW lets them in; DENY keeps them out.", Required: true, Enum: []string{"ALLOW", "DENY"}},
			{Name: "scope_type", Type: "string", Description: "COMPANY = everywhere (including terminals installed later); SITE = one site; TERMINAL = one terminal.", Required: true, Enum: []string{"COMPANY", "SITE", "TERMINAL"}},
			{Name: "site_id", Type: "string", Description: "The site's id (from list_sites) when scope_type is SITE.", MaxLen: 64, Identifier: true},
			{Name: "serial", Type: "string", Description: "The terminal's serial number when scope_type is TERMINAL.", MaxLen: 64, Identifier: true},
			{Name: "schedule_id", Type: "string", Description: "Optional schedule id (from list_schedules) limiting when the rule applies. Blank means any time.", MaxLen: 64},
			{Name: "application", Type: "string", Description: "Optional feature code to limit the rule to. Usually blank.", MaxLen: 64},
			{Name: "first_day", Type: "string", Description: "Optional first day the rule applies, YYYY-MM-DD. Blank means now.", Format: "date"},
			{Name: "last_day", Type: "string", Description: "Optional last day the rule applies (inclusive), YYYY-MM-DD. Blank means no end.", Format: "date"},
		},
		Destructive: true, Idempotent: false,
		MinRole: models.RoleManager,
		Domains: []string{DomainPermissions, DomainOnboarding, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			person, _, fail := get(t, consolePeople+"/"+Segment(a.String("external_id")))
			if fail != nil {
				return nil, failError(fail)
			}
			scope, warnings, err := describeScope(t, a)
			if err != nil {
				return nil, err
			}
			when := "at any time"
			if a.String("schedule_id") != "" {
				when = "on the chosen schedule"
			}
			window := ""
			switch {
			case a.String("first_day") != "" && a.String("last_day") != "":
				window = ", from " + a.String("first_day") + " to " + a.String("last_day")
			case a.String("first_day") != "":
				window = ", from " + a.String("first_day")
			case a.String("last_day") != "":
				window = ", until " + a.String("last_day")
			}
			c := grantConsequence(personLabel(person), a.String("effect"), scope, when, window)
			c.Warnings = warnings
			return &ConfirmationPlan{Consequence: c}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			body := object{"scope_type": a.String("scope_type"), "effect": a.String("effect")}
			switch a.String("scope_type") {
			case "SITE":
				body["site_id"] = a.String("site_id")
			case "TERMINAL":
				body["device_serial"] = a.String("serial")
			}
			if s := a.String("schedule_id"); s != "" {
				body["schedule_id"] = s
			}
			if app := a.String("application"); app != "" {
				body["application"] = app
			}
			// The same day arithmetic the Grant access dialog does: the first
			// day starts at midnight, the last day ends at its last instant,
			// so the rule covers the whole of both.
			if d := a.String("first_day"); d != "" {
				body["starts_at"] = d + "T00:00:00Z"
			}
			if d := a.String("last_day"); d != "" {
				body["ends_at"] = d + "T23:59:59.999Z"
			}
			rule, resp, fail := post(t, http.MethodPost, consolePeople+"/"+Segment(a.String("external_id"))+"/permissions", body)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{"rule": pick(rule, ruleFields...), "note": "Added. Terminals apply it on their next sync."})
		},
	})

	r.Register(&Tool{
		Name: "revoke_access",
		Description: "Remove one of a person's access rules (find the rule id with get_person). The operator " +
			"must approve it before it is removed.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "rule_id", Type: "string", Description: "The access rule's id, from get_person.", Required: true, MaxLen: 64, Identifier: true},
		},
		Destructive: true, Idempotent: true,
		MinRole: models.RoleManager,
		Domains: []string{DomainPermissions, DomainOnboarding, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			person, rule, err := findRule(t, a.String("external_id"), a.String("rule_id"))
			if err != nil {
				return nil, err
			}
			return &ConfirmationPlan{
				Consequence: revokeConsequence(personLabel(person), str(rule, "effect"), scopeLabel(rule)),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			// Refuse to remove a rule that does not belong to the named person:
			// the route is keyed by rule id alone, and the confirmation named
			// a person.
			if _, _, err := findRule(t, a.String("external_id"), a.String("rule_id")); err != nil {
				return Outcome{IsError: true, Status: models.ToolCallNotFound, Result: err.Error()}
			}
			_, resp, fail := post(t, http.MethodDelete, "/api/v1/console/permissions/"+Segment(a.String("rule_id")), nil)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{"removed": true, "note": "Removed. Terminals apply it on their next sync."})
		},
	})
	r.Register(&Tool{
		Name: "update_person",
		Description: "Correct a person's name or category. Only the fields given change; the ID number " +
			"cannot change and any fingerprint is kept.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "full_name", Type: "string", Description: "The corrected full name.", MaxLen: 100},
			{Name: "category", Type: "string", Description: "The new category, e.g. staff, contractor, student.", MaxLen: 50},
		},
		Idempotent: true,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainPeople, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			if a.String("full_name") == "" && a.String("category") == "" {
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: "Give a full_name or a category to change."}
			}
			id := a.String("external_id")
			// READ, MERGE, WRITE. The route requires the full name and treats
			// an absent category as "keep"; sending the existing values for
			// whatever the operator did not mention is what makes a one-field
			// correction a one-field change. The route itself preserves the
			// credential and refuses to change the ID number.
			existing, _, fail := get(t, consolePeople+"/"+Segment(id))
			if fail != nil {
				return *fail
			}
			body := object{"full_name": str(existing, "full_name"), "category": str(existing, "category")}
			if v := a.String("full_name"); v != "" {
				body["full_name"] = v
			}
			if v := a.String("category"); v != "" {
				body["category"] = v
			}
			person, resp, fail := post(t, http.MethodPut, consolePeople+"/"+Segment(id), body)
			if fail != nil {
				return *fail
			}
			changes := object{}
			if str(person, "full_name") != str(existing, "full_name") {
				changes["full_name"] = object{"from": str(existing, "full_name"), "to": str(person, "full_name")}
			}
			if str(person, "category") != str(existing, "category") {
				changes["category"] = object{"from": str(existing, "category"), "to": str(person, "category")}
			}
			out := succeeded(resp, object{"person": pick(person, personFields...), "changes": changes})
			out.Handoff = handoffPerson(id, personLabel(person))
			return out
		},
	})

	r.Register(&Tool{
		Name: "set_person_active",
		Description: "Deactivate a person (every terminal stops admitting them; record and fingerprint kept) " +
			"or activate them again. Deactivating needs the operator's approval.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
			{Name: "active", Type: "boolean", Description: "false to deactivate, true to activate.", Required: true},
		},
		Destructive: true, Idempotent: true,
		MinRole: models.RoleManager,
		Domains: []string{DomainPeople, DomainOnboarding, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			person, _, fail := get(t, consolePeople+"/"+Segment(a.String("external_id")))
			if fail != nil {
				return nil, failError(fail)
			}
			if boolOf(person, "active") == a.Bool("active") {
				if a.Bool("active") {
					return nil, errText(personLabel(person) + " is already active.")
				}
				return nil, errText(personLabel(person) + " is already inactive.")
			}
			if a.Bool("active") {
				// Reactivating restores what was; it runs without a card,
				// as the console's own dialog carries no danger tone for it.
				return nil, nil
			}
			return &ConfirmationPlan{Consequence: deactivateConsequence(personLabel(person))}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			id := a.String("external_id")
			existing, _, fail := get(t, consolePeople+"/"+Segment(id))
			if fail != nil {
				return *fail
			}
			if boolOf(existing, "active") == a.Bool("active") {
				state := "inactive"
				if a.Bool("active") {
					state = "active"
				}
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: personLabel(existing) + " is already " + state + "."}
			}
			// The same read-merge-write as update_person: the route needs
			// the name, and nothing but `active` may move.
			body := object{
				"full_name": str(existing, "full_name"),
				"category":  str(existing, "category"),
				"active":    a.Bool("active"),
			}
			person, resp, fail := post(t, http.MethodPut, consolePeople+"/"+Segment(id), body)
			if fail != nil {
				return *fail
			}
			note := "Deactivated. Terminals stop admitting them on their next sync."
			if a.Bool("active") {
				note = "Activated. Terminals admit them again on their next sync, subject to their access rules."
			}
			out := succeeded(resp, object{"person": pick(person, personFields...), "note": note})
			out.Handoff = handoffPerson(id, personLabel(person))
			return out
		},
	})

	r.Register(&Tool{
		Name: "list_person_credentials",
		Description: "Where a person's fingerprint is enrolled: each credential's state and the terminal " +
			"that holds it. A fingerprint works only at the terminal that captured it.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "The person's ID number.", Required: true, MaxLen: 50, Identifier: true},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			return personCredentials(t, a.String("external_id"))
		},
	})
}

// describeScope names the place a rule applies to, reading the site or
// terminal through the router so the confirmation shows a real name -- and so
// a site or terminal outside the operator's grants is refused before anybody
// is asked to approve anything.
func describeScope(t *Turn, a Args) (label string, warnings []string, err error) {
	switch a.String("scope_type") {
	case "SITE":
		if a.String("site_id") == "" {
			return "", nil, errText("site_id is required when scope_type is SITE")
		}
		site, _, fail := get(t, "/api/v1/console/sites/"+Segment(a.String("site_id")))
		if fail != nil {
			return "", nil, failError(fail)
		}
		return str(site, "name") + " (every terminal there, including ones installed later)", nil, nil
	case "TERMINAL":
		if a.String("serial") == "" {
			return "", nil, errText("serial is required when scope_type is TERMINAL")
		}
		term, _, fail := get(t, "/api/v1/console/terminals/"+Segment(a.String("serial")))
		if fail != nil {
			return "", nil, failError(fail)
		}
		return terminalLabel(term) + " (" + str(term, "site_name") + ")", nil, nil
	default:
		return "everywhere", []string{companyWideWarningTitle + ": " + companyWideWarningBody}, nil
	}
}

func findRule(t *Turn, externalID, ruleID string) (person object, rule object, err error) {
	person, _, fail := get(t, consolePeople+"/"+Segment(externalID))
	if fail != nil {
		return nil, nil, failError(fail)
	}
	rules, _, fail := get(t, consolePeople+"/"+Segment(externalID)+"/permissions")
	if fail != nil {
		return nil, nil, failError(fail)
	}
	for _, r := range pickList(rules, "permissions", ruleFields...) {
		if str(r, "id") == ruleID {
			return person, r, nil
		}
	}
	return nil, nil, errText("That person has no access rule with that id.")
}

func enrollmentState(t *Turn, externalID string) Outcome {
	m, resp, fail := get(t, consolePeople+"/"+Segment(externalID)+"/enrollment")
	if fail != nil {
		return *fail
	}
	out := object{"external_id": str(m, "external_id"), "biometric_enrolled": boolOf(m, "biometric_enrolled")}
	if e, ok := m["enrollment"].(object); ok {
		out["enrollment"] = pick(e, enrolFields...)
	} else {
		out["enrollment"] = nil
		out["note"] = "No enrolment has been started for this person."
	}
	return succeeded(resp, out)
}

// personCredentials is the projected credential list: state and place only.
// The route's SELECT list already carries no biometric material; the
// allow-list here is the second boundary.
func personCredentials(t *Turn, externalID string) Outcome {
	m, resp, fail := get(t, consolePeople+"/"+Segment(externalID)+"/credentials")
	if fail != nil {
		return *fail
	}
	creds := []object{}
	items, _ := m["credentials"].([]any)
	for _, item := range items {
		c, ok := item.(object)
		if !ok {
			continue
		}
		out := pick(c, credentialFields...)
		if term, ok := c["enrolled_at_terminal"].(object); ok {
			out["terminal"] = pick(term, "serial_number", "device_name", "site_name")
		}
		creds = append(creds, out)
	}
	return succeeded(resp, object{
		"external_id":      externalID,
		"count":            num(m, "count"),
		"enrolment_source": str(m, "enrolment_source"),
		"credentials":      creds,
	})
}

type textError string

func (e textError) Error() string { return string(e) }

func errText(s string) error { return textError(s) }

// failError turns a failed Outcome into the error a Confirm hook returns.
func failError(o *Outcome) error {
	if s, ok := o.Result.(string); ok {
		return textError(s)
	}
	return textError("that could not be looked up")
}
