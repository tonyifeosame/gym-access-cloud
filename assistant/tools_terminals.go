package assistant

import (
	"strconv"
	"strings"

	"access-terminal-cloud-api/models"
)

// Terminals, sites, schedules and events -- all read-only.
//
// Routes: GET /console/terminals (ConsoleListTerminals), GET /console/terminals/:serial
// (ConsoleGetTerminal), GET /console/terminals/summary (ConsoleTerminalSummary),
// GET /console/sites (ConsoleListSites), GET /console/sites/:id (ConsoleGetSite),
// GET /console/schedules (ConsoleListSchedules), GET /console/events (ConsoleListEvents).

const consoleTerminals = "/api/v1/console/terminals"

func registerTerminalTools(r *Registry) {
	r.Register(&Tool{
		Name: "list_terminals",
		Description: "The company's terminals with their health: online, offline, fault, updating, not set " +
			"up yet, or disabled. Optionally narrowed to one site or one status, or to those with a " +
			"software update waiting.",
		Params: []Param{
			{Name: "site_id", Type: "string", Description: "Only terminals at this site (id from list_sites).", MaxLen: 64},
			{Name: "status", Type: "string", Description: "Only terminals in this state.", Enum: []string{"ONLINE", "OFFLINE", "ERROR", "UPDATING", "PROVISIONING", "DISABLED"}},
			{Name: "needs_update", Type: "boolean", Description: "Only terminals with a software update waiting."},
			{Name: "limit", Type: "integer", Description: "Rows per page (1-100).", Min: intPtr(1), Max: intPtr(100)},
			{Name: "offset", Type: "integer", Description: "Rows to skip.", Min: intPtr(0), Max: intPtr(100000)},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			limit := a.Int("limit")
			if limit == 0 {
				limit = 50
			}
			params := map[string]string{"limit": strconv.Itoa(limit), "offset": strconv.Itoa(a.Int("offset"))}
			if a.Bool("needs_update") {
				params["outdated"] = "true"
			}
			m, resp, fail := get(t, Query(consoleTerminals, params))
			if fail != nil {
				return *fail
			}
			// Site and status are narrowed here, on the projected page: the
			// route has no such filters, and the page is the operator's own
			// scoped fleet either way.
			all := pickList(m, "terminals", terminalFields...)
			out := []object{}
			for _, term := range all {
				if s := a.String("site_id"); s != "" && str(term, "site_public_id") != s {
					continue
				}
				if s := a.String("status"); s != "" && str(term, "status") != s {
					continue
				}
				out = append(out, term)
			}
			return succeeded(resp, object{
				"terminals": out,
				"total":     num(m, "total"),
				"has_more":  boolOf(m, "has_more"),
				"note":      "status meanings: ONLINE in contact; OFFLINE not heard from recently; ERROR reporting a fault; UPDATING installing software; PROVISIONING not set up yet; DISABLED switched off by staff.",
			})
		},
	})

	r.Register(&Tool{
		Name:        "get_terminal",
		Description: "One terminal: where it is, its health, what it is assigned to do, its software version and whether an update is waiting.",
		Params: []Param{
			{Name: "serial", Type: "string", Description: "The terminal's serial number.", Required: true, MaxLen: 64},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, consoleTerminals+"/"+Segment(a.String("serial")))
			if fail != nil {
				return *fail
			}
			out := pick(m, terminalFields...)
			if rel, ok := m["release"].(object); ok {
				out["release_state"] = str(rel, "state")
			}
			if rd, ok := m["readiness"].(object); ok {
				out["readiness"] = str(rd, "state")
			}
			return succeeded(resp, out)
		},
	})

	r.Register(&Tool{
		Name:        "get_fleet_summary",
		Description: "How many terminals the company has and how many are online, offline, reporting a fault or waiting for a software update.",
		ReadOnly:    true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, consoleTerminals+"/summary")
			if fail != nil {
				return *fail
			}
			return succeeded(resp, pick(m, "total", "online", "offline", "error", "updating", "provisioning", "disabled", "firmware_outdated"))
		},
	})

	r.Register(&Tool{
		Name:        "list_sites",
		Description: "The company's sites, with how many terminals each has and what its terminals do during an internet outage.",
		ReadOnly:    true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/sites")
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{"sites": pickList(m, "sites", siteFields...)})
		},
	})

	r.Register(&Tool{
		Name:        "get_site",
		Description: "One site and its offline policy.",
		Params: []Param{
			{Name: "site_id", Type: "string", Description: "The site's id, from list_sites.", Required: true, MaxLen: 64},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/sites/"+Segment(a.String("site_id")))
			if fail != nil {
				return *fail
			}
			return succeeded(resp, pick(m, siteFields...))
		},
	})

	r.Register(&Tool{
		Name:        "list_schedules",
		Description: "The company's schedules (time windows an access rule can be limited to) and how many rules use each.",
		ReadOnly:    true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			m, resp, fail := get(t, "/api/v1/console/schedules")
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{"schedules": pickList(m, "schedules", scheduleFields...)})
		},
	})

	r.Register(&Tool{
		Name: "list_events",
		Description: "What happened at the terminals: each time somebody presented themselves and whether they " +
			"were let in, kept out, or something went wrong. Newest first. Filter by person, terminal, " +
			"site, decision, or a time window.",
		Params: []Param{
			{Name: "external_id", Type: "string", Description: "Only events for this person's ID number.", MaxLen: 50},
			{Name: "serial", Type: "string", Description: "Only events at this terminal.", MaxLen: 64},
			{Name: "site_id", Type: "string", Description: "Only events at this site.", MaxLen: 64},
			{Name: "decision", Type: "string", Description: "Only this outcome.", Enum: []string{"GRANTED", "DENIED", "RECORDED", "ERROR"}},
			{Name: "from", Type: "string", Description: "Only events at or after this instant (RFC 3339, e.g. 2026-09-15T00:00:00Z).", MaxLen: 40},
			{Name: "to", Type: "string", Description: "Only events before this instant (RFC 3339).", MaxLen: 40},
			{Name: "limit", Type: "integer", Description: "Rows per page (1-100).", Min: intPtr(1), Max: intPtr(100)},
			{Name: "offset", Type: "integer", Description: "Rows to skip.", Min: intPtr(0), Max: intPtr(100000)},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleViewer,
		Run: func(t *Turn, a Args) Outcome {
			limit := a.Int("limit")
			if limit == 0 {
				limit = 20
			}
			params := map[string]string{
				"external_id": a.String("external_id"),
				"serial":      a.String("serial"),
				"site_id":     a.String("site_id"),
				"decision":    strings.ToUpper(a.String("decision")),
				"from":        a.String("from"),
				"to":          a.String("to"),
				"limit":       strconv.Itoa(limit),
				"offset":      strconv.Itoa(a.Int("offset")),
			}
			m, resp, fail := get(t, Query("/api/v1/console/events", params))
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"events":   pickList(m, "events", eventFields...),
				"total":    num(m, "total"),
				"has_more": boolOf(m, "has_more"),
			})
		},
	})
}

// registerPhase1Tools is the whole Phase 1 catalogue.
func registerPhase1Tools(r *Registry) {
	registerPeopleTools(r)
	registerEnrollmentTools(r)
	registerTerminalTools(r)
}
