package assistant

import (
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/models"
)

// The operator audit trail: who changed what.
//
// Route: GET /console/audit (ConsoleListAuditEvents). ADMIN, because the
// trail names which operators did what -- administrative information about
// colleagues rather than the product working. The router enforces that; the
// tool's MinRole only decides who is shown it.
//
// TWO COLUMNS ARE NOT PROJECTED, AND BOTH DELIBERATELY.
//
//   - ip_address. The address an operator acted from is infrastructure, and
//     the same rule that keeps a terminal's SSID and address out of a
//     diagnostic keeps this out of an audit row.
//
//   - changes. It is FREE-FORM by design -- whatever the handler that wrote
//     the row passed -- and the handlers that mint credentials put key and
//     code prefixes in it (console_sites.go's api_key_prefix, claim.go's
//     code_prefix, console_api_credentials.go's key_prefix), while the
//     announcement handlers put first_seen_ip there. A projection cannot
//     allow-list the inside of a column whose shape each writer chooses, so
//     the column does not come through at all. What changed in detail is on
//     the console's own audit screen, which the note says.
//
// The trail is therefore WHO, WHAT ACTION, AGAINST WHICH TARGET, AND WHEN --
// which is the question the tool exists to answer.

const consoleAudit = "/api/v1/console/audit"

func registerAuditTools(r *Registry) {
	r.Register(&Tool{
		Name: "list_audit",
		Description: "The operator audit trail: which member of staff changed what, and when. Newest " +
			"first. Filter by action, by the kind of thing acted on, by who did it, or by a time " +
			"window. Says what was done and to what; the field-by-field detail is on the console's " +
			"audit screen.",
		Params: []Param{
			{Name: "action", Type: "string", Description: "Only this action, e.g. PERSON_CREATED, PERMISSION_GRANTED, TERMINAL_APPROVED.", MaxLen: 64, Identifier: true},
			{Name: "target_type", Type: "string", Description: "Only this kind of target, e.g. PERSON, TERMINAL, SITE, SCHEDULE.", MaxLen: 64, Identifier: true},
			{Name: "actor", Type: "string", Description: "Only actions by this operator's email address.", MaxLen: 255, Identifier: true},
			{Name: "since", Type: "string", Description: "Only actions at or after this instant (RFC 3339, e.g. 2026-09-15T00:00:00Z).", MaxLen: 40},
			{Name: "until", Type: "string", Description: "Only actions before this instant (RFC 3339).", MaxLen: 40},
			{Name: "limit", Type: "integer", Description: "Rows per page (1-200).", Min: intPtr(1), Max: intPtr(200)},
			{Name: "offset", Type: "integer", Description: "Rows to skip.", Min: intPtr(0), Max: intPtr(100000)},
		},
		ReadOnly: true, Idempotent: true, ParallelSafe: true,
		MinRole: models.RoleAdmin,
		Run: func(t *Turn, a Args) Outcome {
			// The instants are checked here rather than left to the route,
			// which answers 400 for a malformed one: the refusal the model
			// reads should name the form to use.
			for _, name := range []string{"since", "until"} {
				if err := validInstant(name, a.String(name)); err != nil {
					return Outcome{IsError: true, Status: models.ToolCallInvalid, Result: err.Error()}
				}
			}
			limit := a.Int("limit")
			if limit == 0 {
				limit = 25
			}
			params := map[string]string{
				"action":      a.String("action"),
				"target_type": a.String("target_type"),
				"actor":       a.String("actor"),
				"since":       a.String("since"),
				"until":       a.String("until"),
				"limit":       strconv.Itoa(limit),
				"offset":      strconv.Itoa(a.Int("offset")),
			}
			m, resp, fail := get(t, Query(consoleAudit, params))
			if fail != nil {
				return *fail
			}
			return succeeded(resp, object{
				"entries":  pickList(m, "entries", auditFields...),
				"total":    num(m, "total"),
				"has_more": boolOf(m, "has_more"),
				"note": "Each row says who did it, what they did and to what. The values that " +
					"changed are not shown here; open the console's audit screen for those.",
			})
		},
	})
}

// validInstant checks an optional RFC 3339 filter and names the argument it
// belongs to, so the model is told which of two it spelt wrongly.
func validInstant(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err != nil {
		return errText(name + " must be an RFC 3339 instant, e.g. 2026-09-15T00:00:00Z")
	}
	return nil
}
