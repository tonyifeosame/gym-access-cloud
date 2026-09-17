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
// 09:00-13:00") and parseWindows turns that into the route's shape, refusing
// anything it does not understand. The route validates again.

const consoleSchedules = "/api/v1/console/schedules"

const windowsHelp = "Windows as text, separated by ';': days then a time range, e.g. " +
	"\"Mon-Fri 08:00-18:00; Sat 09:00-13:00\" or \"Daily 06:00-22:00\". A range ending at or " +
	"before its start runs overnight (\"Fri 22:00-06:00\")."

func registerScheduleTools(r *Registry) {
	r.Register(&Tool{
		Name: "create_schedule",
		Description: "Add a schedule (a set of weekly time windows). On its own it admits nobody: it is " +
			"used by grant_access to limit when a rule applies.",
		Params: []Param{
			{Name: "name", Type: "string", Description: "A name unique within the company.", Required: true, MaxLen: 100},
			{Name: "windows", Type: "string", Description: windowsHelp, Required: true, MaxLen: 500},
			{Name: "timezone", Type: "string", Description: "Optional IANA zone, e.g. Africa/Lagos. Blank uses the company default.", MaxLen: 64},
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
			{Name: "timezone", Type: "string", Description: "A new IANA zone.", MaxLen: 64},
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

// --- window text ---------------------------------------------------------------------

// Mon=1 .. Sun=64, the mask migration 002 established.
var dayBits = map[string]int{
	"mon": 1, "tue": 2, "wed": 4, "thu": 8, "fri": 16, "sat": 32, "sun": 64,
}

var dayOrder = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

const everyDay = 127

// parseWindows turns "Mon-Fri 08:00-18:00; Sat 09:00-13:00" into the route's
// windows. Each entry is DAYS then TIMES; DAYS is a comma list of day names
// (three letters or full) and ranges (Mon-Fri), or Daily/Everyday/Weekdays/
// Weekends; TIMES is HH:MM-HH:MM.
func parseWindows(text string) ([]object, error) {
	out := []object{}
	for _, entry := range strings.Split(text, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		fields := strings.Fields(entry)
		if len(fields) < 2 {
			return nil, fmt.Errorf("window %q: expected days then a time range, e.g. \"Mon-Fri 08:00-18:00\"", entry)
		}
		times := fields[len(fields)-1]
		days := strings.Join(fields[:len(fields)-1], "")
		mask, err := parseDays(days)
		if err != nil {
			return nil, fmt.Errorf("window %q: %v", entry, err)
		}
		start, end, err := parseTimeRange(times)
		if err != nil {
			return nil, fmt.Errorf("window %q: %v", entry, err)
		}
		out = append(out, object{"days_of_week": mask, "start_time": start, "end_time": end})
	}
	if len(out) == 0 {
		return nil, errText("windows is empty; give at least one, e.g. \"Mon-Fri 08:00-18:00\"")
	}
	return out, nil
}

func parseDays(s string) (int, error) {
	switch strings.ToLower(s) {
	case "daily", "everyday", "every-day", "all":
		return everyDay, nil
	case "weekdays":
		return 1 | 2 | 4 | 8 | 16, nil
	case "weekends", "weekend":
		return 32 | 64, nil
	}
	mask := 0
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if from, to, ok := strings.Cut(part, "-"); ok {
			a, errA := dayIndex(from)
			b, errB := dayIndex(to)
			if errA != nil {
				return 0, errA
			}
			if errB != nil {
				return 0, errB
			}
			for i := a; ; i = (i + 1) % 7 {
				mask |= dayBits[dayOrder[i]]
				if i == b {
					break
				}
			}
			continue
		}
		i, err := dayIndex(part)
		if err != nil {
			return 0, err
		}
		mask |= dayBits[dayOrder[i]]
	}
	if mask == 0 {
		return 0, errText("no days given")
	}
	return mask, nil
}

func dayIndex(name string) (int, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if len(key) > 3 {
		key = key[:3]
	}
	for i, d := range dayOrder {
		if d == key {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown day %q", name)
}

func parseTimeRange(s string) (string, string, error) {
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return "", "", fmt.Errorf("time range %q must be HH:MM-HH:MM", s)
	}
	start, err := normaliseClock(from)
	if err != nil {
		return "", "", err
	}
	end, err := normaliseClock(to)
	if err != nil {
		return "", "", err
	}
	return start, end, nil
}

func normaliseClock(s string) (string, error) {
	s = strings.TrimSpace(s)
	var h, m int
	if n, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return "", fmt.Errorf("time %q must be HH:MM", s)
	}
	if h == 24 {
		h = 0
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}
