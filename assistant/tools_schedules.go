package assistant

import (
	"fmt"
	"net/http"
	"strings"

	"access-terminal-cloud-api/models"
)

// Schedules: the time windows an access rule can be limited to.
//
// Routes: POST /console/schedules (ConsoleCreateSchedule), PUT
// /console/schedules/:id (ConsoleUpdateSchedule), GET /console/schedules.
//
// WINDOWS ARE WRITTEN, NOT STRUCTURED. The registry takes strings, numbers
// and booleans, and a schedule's windows are a list of objects -- so the
// model writes them the way a person would ("Mon-Fri 08:00-18:00; Sat
// 09:00-13:00") and parseWindows (tools_schedules_parse.go) turns that into
// the route's shape, refusing anything it does not understand exactly. The
// route validates again, but cannot tell a misread window from a meant one.

const consoleSchedules = "/api/v1/console/schedules"

const windowsHelp = "Windows as text, separated by ';': days then a 24-hour time range, e.g. " +
	"\"Mon-Fri 08:00-18:00; Sat 09:00-13:00\" or \"Daily 06:00-22:00\". Days: Daily, Weekdays, Weekends, " +
	"Mon or Monday, a comma list (Sat,Sun) or a hyphen range (Mon-Fri); nothing else. Times HH:MM only " +
	"(no am/pm); an end of 24:00 means the end of the day. A range ending at or before its start runs " +
	"overnight (\"Fri 22:00-06:00\")."

func registerScheduleTools(r *Registry) {
	r.Register(&Tool{
		Name: "create_schedule",
		Description: "Add a schedule (a set of weekly time windows). On its own it admits nobody: it is " +
			"used by grant_access to limit when a rule applies.",
		Params: []Param{
			{Name: "name", Type: "string", Description: "A name unique within the company.", Required: true, MaxLen: 100},
			{Name: "windows", Type: "string", Description: windowsHelp, Required: true, MaxLen: 500},
			{Name: "timezone", Type: "string", Description: "Optional IANA zone, e.g. Africa/Lagos. Blank means the site's own zone (UTC if the site has none).", MaxLen: 64},
			{Name: "description", Type: "string", Description: "Optional note.", MaxLen: 200},
		},
		Idempotent: false,
		MinRole:    models.RoleManager,
		Domains:    []string{DomainSchedules, DomainAudit},
		Run: func(t *Turn, a Args) Outcome {
			windows, err := parseWindows(a.String("windows"))
			if err != nil {
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
			}
			if err := validTimezone(a.String("timezone")); err != nil {
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
			}
			body := object{"name": a.String("name"), "windows": windows}
			if v := a.String("timezone"); v != "" {
				body["timezone"] = v
			}
			if v := a.String("description"); v != "" {
				body["description"] = v
			}
			m, resp, fail := post(t, http.MethodPost, consoleSchedules, body)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"schedule": pick(m, scheduleFields...),
				"note":     "Added. It limits nothing until a rule names it.",
			})
		},
	})

	r.Register(&Tool{
		Name: "update_schedule",
		Description: "Change a schedule's name, windows, timezone or whether it is active. Only the fields " +
			"given change. If access rules use the schedule, the operator must approve the change.",
		Params: []Param{
			{Name: "schedule_id", Type: "string", Description: "The schedule's id, from list_schedules.", Required: true, MaxLen: 64, Identifier: true},
			{Name: "name", Type: "string", Description: "A new name.", MaxLen: 100},
			{Name: "windows", Type: "string", Description: "New windows, replacing all existing ones. " + windowsHelp, MaxLen: 500},
			{Name: "timezone", Type: "string", Description: "A new IANA zone, e.g. Europe/London.", MaxLen: 64},
			{Name: "active", Type: "boolean", Description: "false pauses the schedule (rules using it admit nobody), true resumes it."},
		},
		Destructive: true, Idempotent: true,
		MinRole: models.RoleManager,
		Domains: []string{DomainSchedules, DomainPermissions, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			change, err := scheduleChange(a)
			if err != nil {
				return nil, err
			}
			schedule, err := findSchedule(t, a.String("schedule_id"))
			if err != nil {
				return nil, err
			}
			dependents := num(schedule, "permission_count")
			if dependents == 0 {
				// Nothing depends on it: the change is as safe as creating it.
				return nil, nil
			}
			return &ConfirmationPlan{
				Consequence: scheduleChangeConsequence(str(schedule, "name"), dependents, change),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			if _, err := scheduleChange(a); err != nil {
				return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
			}
			body := object{}
			if v := a.String("name"); v != "" {
				body["name"] = v
			}
			if v := a.String("windows"); v != "" {
				windows, err := parseWindows(v)
				if err != nil {
					return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
				}
				body["windows"] = windows
			}
			if v := a.String("timezone"); v != "" {
				body["timezone"] = v
			}
			if a.Has("active") {
				body["active"] = a.Bool("active")
			}
			m, resp, fail := post(t, http.MethodPut, consoleSchedules+"/"+Segment(a.String("schedule_id")), body)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"schedule": pick(m, scheduleFields...),
				"note":     "Changed. Terminals apply it on their next sync.",
			})
		},
	})

	r.Register(&Tool{
		Name: "delete_schedule",
		Description: "Remove a schedule. Only one that no access rule uses can be removed; the platform " +
			"refuses while any rule still refers to it, and says how many. The operator must approve.",
		Params: []Param{
			{Name: "schedule_id", Type: "string", Description: "The schedule's id, from list_schedules.", Required: true, MaxLen: 64, Identifier: true},
		},
		Destructive: true, Idempotent: false,
		MinRole: models.RoleManager,
		// PERMISSIONS IS DECLARED EVEN THOUGH A SUCCESSFUL DELETE CHANGES NO
		// RULE. A cached rules view carries each rule's schedule_name, and
		// the safe direction to be wrong in is a refresh nobody needed
		// rather than a screen naming a schedule that no longer exists.
		Domains: []string{DomainSchedules, DomainPermissions, DomainAudit},
		Confirm: func(t *Turn, a Args) (*ConfirmationPlan, error) {
			schedule, err := findSchedule(t, a.String("schedule_id"))
			if err != nil {
				return nil, err
			}
			if dependents := num(schedule, "permission_count"); dependents > 0 {
				// REFUSED HERE RATHER THAN ASKED. The route answers 409 while
				// any rule refers to the schedule, so a card for this would be
				// asking the operator to approve something that cannot happen.
				// The count is what they need to act on, so it is in the
				// refusal.
				return nil, errText(fmt.Sprintf("%s cannot be deleted: %s still use it. "+
					"Change or remove those rules first, or pause the schedule with update_schedule "+
					"active=false, which stops it admitting anybody without deleting it.",
					str(schedule, "name"), fmtCount(dependents, "access rule")))
			}
			return &ConfirmationPlan{
				Consequence: deleteScheduleConsequence(str(schedule, "name"), 0),
			}, nil
		},
		Run: func(t *Turn, a Args) Outcome {
			id := a.String("schedule_id")
			schedule, err := findSchedule(t, id)
			if err != nil {
				return Outcome{IsError: true, Status: models.ToolCallNotFound, Result: err.Error()}
			}
			name := str(schedule, "name")
			_, resp, fail := post(t, http.MethodDelete, consoleSchedules+"/"+Segment(id), nil)
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"deleted":  true,
				"note":     "Deleted. No access rule referred to it, so nobody's access changed.",
				"schedule": object{"id": id, "name": name},
			})
		},
	})
}

// scheduleChange summarises what an update asks for, and refuses an empty one.
func scheduleChange(a Args) (string, error) {
	parts := []string{}
	if v := a.String("name"); v != "" {
		parts = append(parts, "rename it to "+v)
	}
	if v := a.String("windows"); v != "" {
		if _, err := parseWindows(v); err != nil {
			return "", err
		}
		parts = append(parts, "set its windows to "+v)
	}
	if v := a.String("timezone"); v != "" {
		if err := validTimezone(v); err != nil {
			return "", err
		}
		parts = append(parts, "set its timezone to "+v)
	}
	if a.Has("active") {
		if a.Bool("active") {
			parts = append(parts, "resume it")
		} else {
			parts = append(parts, "pause it")
		}
	}
	if len(parts) == 0 {
		return "", errText("Give a name, windows, timezone or active to change.")
	}
	return "This will " + strings.Join(parts, ", ") + ".", nil
}

// findSchedule reads the list (there is no single-schedule route) and finds
// one by id, so the confirmation can name it and count its dependents.
func findSchedule(t *Turn, id string) (object, error) {
	m, _, fail := get(t, consoleSchedules)
	if fail != nil {
		return nil, failError(fail)
	}
	for _, s := range pickList(m, "schedules", scheduleFields...) {
		if str(s, "id") == id {
			return s, nil
		}
	}
	return nil, errText("No schedule has that id.")
}
