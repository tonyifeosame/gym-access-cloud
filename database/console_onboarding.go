package database

// What a company still has to do before the product does anything for it.
//
// ---------------------------------------------------------------------------
// WHY THIS IS A SERVER-SIDE COUNT AND NOT ARITHMETIC IN THE BROWSER
// ---------------------------------------------------------------------------
//
// The console's overview raises the setup steps a new customer has not finished
// yet -- no terminals, no features, nobody added -- and each of those is a count
// the API already reports. The step that was MISSING is the one that decides
// whether anybody actually gets in:
//
//	Absence of permission is not permission.
//
// A person with no access rule reaches nothing, by design (models/authorization.go).
// So a customer who adds a terminal and a roster and stops has a deployment that
// admits nobody, and every onboarding item on the overview has gone quiet.
//
// THE CONSOLE COULD NOT ANSWER THIS. Permissions are readable only one person at
// a time (`GET /console/people/:external_id/permissions`), so a browser would
// have to fan out one request per person to find out -- fifty requests to render
// a dashboard tile, and a number that is a sample rather than a count the moment
// the roster outgrows a page. Summing `permission_count` across schedules would
// be worse: it silently omits every rule that has no schedule, so it reports
// zero for a company whose rules all apply at any time.
//
// One indexed aggregate, computed where the data is, is the only version of this
// figure that is true.

// CountPeopleWithoutAccess returns how many of a company's active people have no
// access rule IN FORCE RIGHT NOW.
//
// ---------------------------------------------------------------------------
// "HAS A ROW" AND "CAN GET IN" ARE NOT THE SAME QUESTION
// ---------------------------------------------------------------------------
//
// This used to count any rule that had not been deleted, on the reasoning that
// it should agree with ListPersonPermissions -- which draws the person's Access
// panel. That agreement was real but it was agreement about the wrong thing.
// The panel LISTS rules and grades each one; the overview makes a claim about
// PEOPLE, in words the customer reads as "these people cannot get in":
//
//	"Nobody can get in yet"        when the figure covers the whole roster
//	"3 people have no access"      otherwise
//
// Neither sentence is true of somebody whose only rule expired last month. The
// console already knew that -- accessVocabulary.standingOf() grades every rule
// IN_FORCE, NOT_YET, EXPIRED or INACTIVE, and the panel badges the last three --
// so the overview was the one surface still treating a lapsed row as access.
//
// ---------------------------------------------------------------------------
// THE PREDICATE IS THE ENGINE'S, NOT A THIRD OPINION
// ---------------------------------------------------------------------------
//
// The three conditions below are the SQL of what database/authorization.go
// already decides in Go, one rule at a time (see `ruleAdmits`):
//
//	active           an INACTIVE rule is switched off; the engine skips it in
//	                 its WHERE clause, and so does the console's own badge.
//	starts_at <= now `at.Before(rule.StartsAt)` is not yet valid. Equality is
//	                 valid, which is why this is <= and not <.
//	ends_at   >  now `!at.Before(rule.EndsAt)` has expired. Equality is EXPIRED,
//	                 which is why this is > and not >=.
//
// A THIRD IMPLEMENTATION WOULD BE THE BUG. Three surfaces now answer "is this
// rule live" -- the engine in Go, the console in TypeScript, this in SQL -- and
// they agree by construction rather than by coincidence. Any future change to
// what "in force" means has to move all three, which is the point of saying so
// here.
//
// SCHEDULES ARE NOT EVALUATED, exactly as standingOf does not evaluate them. A
// schedule narrows WHEN a live rule applies; whether this minute falls inside a
// night-shift window is the engine's answer in the terminal's own timezone, and
// a count that tried to answer it would be reporting "cannot get in AT 14:02 ON
// A TUESDAY" under a heading that reads "has no access". A person whose rule is
// in force but out of hours has been granted access; they are waiting for
// Tuesday, not for an administrator.
//
// EFFECT IS NOT CONSIDERED, for the same reason. A person whose only live rule
// is a DENY holds a rule somebody wrote deliberately, and the Access panel shows
// it to them as one; calling that "no access" would report a customer's own
// decision back to them as an unfinished step.
//
// INACTIVE PEOPLE ARE EXCLUDED. A deactivated person is deliberately not
// admitted, so counting them as "needs access granted" would turn a decision the
// customer made into a chore the console nags them about -- the same reasoning
// that keeps a deactivated site out of the attention list.
//
// NOT SCOPED BY SITE, because people are not: the schema has no person-to-site
// relationship, and the people list says so on its own screen. This is a
// company-wide figure for every operator who can read it.
//
// `idx_permissions_person_live` covers the first condition, and the two
// timestamp comparisons are evaluated over the handful of rows a person has.
func CountPeopleWithoutAccess(companyID int64) (int, error) {
	var count int
	err := DB.QueryRow(`
		SELECT count(*)
		  FROM people p
		 WHERE p.company_id = $1
		   AND p.deleted_at IS NULL
		   AND p.active
		   AND NOT EXISTS (
		         SELECT 1
		           FROM permissions pm
		          WHERE pm.person_id = p.id
		            AND pm.deleted_at IS NULL
		            AND pm.active
		            AND (pm.starts_at IS NULL OR pm.starts_at <= CURRENT_TIMESTAMP)
		            AND (pm.ends_at   IS NULL OR pm.ends_at   >  CURRENT_TIMESTAMP)
		       )`, companyID).Scan(&count)
	return count, err
}
