package handlers

import (
	"net/http"

	"access-terminal-cloud-api/database"

	"github.com/gin-gonic/gin"
)

// The setup facts the overview cannot work out for itself.
//
// EVERY OTHER ONBOARDING STEP IS ALREADY ANSWERABLE from data the console holds:
// how many terminals, how many features, how many people. The step that decides
// whether the deployment admits anybody is not, because access rules are
// readable one person at a time -- so the browser would have to make one request
// per person to find out whether anybody has been granted anything.
//
// A SEPARATE READ RATHER THAN A FIELD ON THE PEOPLE PAGE. The people list is
// paginated, searchable and filterable, and every number on it describes THE
// MATCH the caller asked for. A company-wide figure riding along on that
// envelope would be the one value that ignored the filters, which is exactly the
// kind of number somebody later reads as "people without access, matching this
// search" and is wrong about.
//
// VIEWER, matching the per-person permissions read this aggregates. A viewer at
// a front desk may already ask "does this person have any rules" about anybody
// in the company; the total is not a new disclosure, and it is what makes the
// overview's setup guidance work for the operator who most often opens it.
//
// NOTHING HERE IS A DECISION. It reports what is configured; the engine decides
// what happens at the door, and this endpoint neither reads nor influences it.
type consoleOnboardingState struct {
	// Active people with no access rule at all.
	//
	// NAMED FOR THE FACT, not for the advice. "people_without_access" is
	// checkable against the database; "needs_attention" would be this console's
	// opinion baked into the API, and the next client to read it would have to
	// guess what threshold produced it.
	PeopleWithoutAccess int `json:"people_without_access"`
}

// ConsoleOnboardingState handles GET /api/v1/console/onboarding
func ConsoleOnboardingState(c *gin.Context) {
	withoutAccess, err := database.CountPeopleWithoutAccess(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console onboarding state", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to retrieve setup state"})
		return
	}

	c.JSON(http.StatusOK, consoleOnboardingState{PeopleWithoutAccess: withoutAccess})
}
