package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The event trail, /api/v1/console/events.
//
// SEC-08: access logs were unreachable from an operator session. The only route
// that read them authenticated with the SITE API KEY -- the provisioning
// credential installed on hardware at a door -- so the only way for an operator
// to see who came in was to hold the secret that registers terminals. The
// console could not show a door event at all.
//
// This route answers the audit's question in full: who, where, when, which
// terminal, which application, what action, granted or denied, WHY, which
// credential, and the state the platform was in when it decided.
//
// SCOPED BY GRANT, not just by company. An operator scoped to one site sees that
// site's events; the same rule RequireSiteGrant applies to every other
// site-shaped read. Without it, a site-scoped operator who could not open a
// terminal's detail page could still read every presentation at it.
//
// ROLE. VIEWER, unlike the operator audit trail at /console/audit which is
// ADMIN. The difference is deliberate: an event trail says what happened in the
// field, which is the product working, while an audit trail names which
// operators changed what, which is administrative information about colleagues.

// ConsoleListEvents handles GET /console/events
func ConsoleListEvents(c *gin.Context) {
	scope, err := operatorSiteScope(c)
	if err != nil {
		logError(c, "console resolve site scope", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve events"})
		return
	}

	query := database.EventQuery{
		Limit:  parseIntQuery(c, "limit", 0),
		Offset: parseIntQuery(c, "offset", 0),

		// nil means "every site in the company", matching operatorSiteScope's
		// contract. A non-nil slice is a real restriction and is passed as one,
		// so that "granted no sites" cannot be read as "no filter".
		Scoped:  scope != nil,
		SiteIDs: scope,

		SiteID:       strings.TrimSpace(c.Query("site_id")),
		DeviceSerial: strings.TrimSpace(c.Query("serial")),
		ExternalID:   strings.TrimSpace(c.Query("external_id")),
		PersonID:     strings.TrimSpace(c.Query("person_id")),
		EventType:    strings.TrimSpace(c.Query("event_type")),
		Application:  strings.TrimSpace(c.Query("application")),
		Decision:     strings.TrimSpace(c.Query("decision")),
		Direction:    strings.TrimSpace(c.Query("direction")),
		Query:        strings.TrimSpace(c.Query("q")),
	}

	from, ok := parseTimeQuery(c, "from")
	if !ok {
		return
	}
	to, ok := parseTimeQuery(c, "to")
	if !ok {
		return
	}
	query.From = from
	query.To = to

	if query.Decision != "" && !models.IsEventDecision(strings.ToUpper(query.Decision)) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "decision must be GRANTED, DENIED, RECORDED or ERROR"})
		return
	}

	page, err := database.ListEvents(c.GetInt64("company_id"), query)
	if err != nil {
		logError(c, "console list events", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve events"})
		return
	}

	c.JSON(http.StatusOK, page)
}

// parseIntQuery reads a non-negative integer query parameter.
//
// A malformed value falls back to the default rather than erroring: the store
// clamps limit and offset anyway, and failing a whole page read because a client
// sent `limit=abc` is less useful than serving the default page.
func parseIntQuery(c *gin.Context, name string, fallback int) int {
	raw := c.Query(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// parseTimeQuery reads an RFC3339 timestamp, answering 400 on a malformed one.
//
// UNLIKE the integer parameters, a bad timestamp is an error rather than a
// fallback. Silently ignoring `from=lastweek` would return the whole trail and
// look like an answer to the question that was asked -- and an operator reading
// a year of events believing they are reading a week is worse than an error.
func parseTimeQuery(c *gin.Context, name string) (*time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": name + " must be an RFC3339 timestamp, e.g. 2026-08-15T09:00:00Z"})
		return nil, false
	}
	return &parsed, true
}
