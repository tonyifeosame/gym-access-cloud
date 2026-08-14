package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The operator console, /api/v1/console/*.
//
// Everything here authenticates with an operator SESSION. The site API key --
// the provisioning secret that registers terminals and rotates their
// credentials -- authenticates nothing in this file and is never returned by
// anything in it. That is the whole point of the split: a dashboard running on a
// machine the company does not control must not be able to obtain it.
//
// DOMAIN-NEUTRAL. A company, its sites, its terminals, its people and the
// capabilities it has enabled. Nothing here assumes the platform is being used
// for doors, or attendance, or anything else; what a deployment is FOR is
// configuration, exposed through the applications endpoints and rendered by the
// dashboard.
//
// REUSE OVER RESTATEMENT. Where a rule already exists in the data layer -- the
// sync jobs a person change enqueues, the tenancy filter on every query, the
// grant check in RequireSiteGrant -- these handlers call it rather than
// reimplementing it. Where reuse would be unsafe, the reason is written at the
// call site.

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// operatorSiteScope resolves which sites the caller may see in a LIST.
//
// Returns nil to mean "every site in the company", matching the rule
// RequireSiteGrant enforces for single-site routes: OWNER and ADMIN are never
// scoped, and an operator holding no grants is not scoped either. If the two
// ever disagreed, a list would show sites that the detail route then refused.
func operatorSiteScope(c *gin.Context) ([]int64, error) {
	identity := middleware.Operator(c)
	if middleware.RoleAtLeast(identity.Role, models.RoleAdmin) {
		return nil, nil
	}

	grants, err := database.ListSiteGrants(identity.UserID)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(grants))
	for _, grant := range grants {
		ids = append(ids, grant.SiteID)
	}
	return ids, nil
}

// consolePerson projects a person for the dashboard.
//
// The fingerprint template never crosses this boundary. It holds a credential
// locator rather than biometric data, but the frontend must treat biometrics as
// an abstraction either way -- so the only thing that travels is whether a
// credential exists.
func consolePerson(member models.Member) models.ConsolePerson {
	return models.ConsolePerson{
		ID:                member.PublicID,
		ExternalID:        member.MemberID,
		FullName:          member.FullName,
		Category:          member.MembershipType,
		Active:            member.Active,
		BiometricEnrolled: member.FingerprintTemplate != "",
		CreatedAt:         member.CreatedAt,
		UpdatedAt:         member.UpdatedAt,
	}
}

// consoleOperator projects an operator account.
func consoleOperator(user models.User, grants []models.SiteGrant) models.ConsoleOperator {
	if grants == nil {
		grants = []models.SiteGrant{}
	}
	return models.ConsoleOperator{
		ID:          user.PublicID,
		Email:       user.Email,
		FullName:    user.FullName,
		Role:        user.Role,
		Active:      user.Active,
		LastLoginAt: user.LastLoginAt,
		Sites:       grants,
		AllSites:    middleware.RoleAtLeast(user.Role, models.RoleAdmin) || len(grants) == 0,
		CreatedAt:   user.CreatedAt,
	}
}

// ---------------------------------------------------------------------------
// Company and sites
// ---------------------------------------------------------------------------

// ConsoleGetCompany handles GET /console/company
func ConsoleGetCompany(c *gin.Context) {
	company, err := database.GetCompany(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console get company", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve company"})
		return
	}
	c.JSON(http.StatusOK, company)
}

// ConsoleListSites handles GET /console/sites
//
// Narrowed to the caller's grants. The response carries no API keys: the
// projection does not select the column at all.
func ConsoleListSites(c *gin.Context) {
	scope, err := operatorSiteScope(c)
	if err != nil {
		logError(c, "console resolve site scope", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve sites"})
		return
	}

	sites, err := database.ListConsoleSites(c.GetInt64("company_id"), scope)
	if err != nil {
		logError(c, "console list sites", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve sites"})
		return
	}
	if sites == nil {
		sites = []models.ConsoleSite{}
	}

	c.JSON(http.StatusOK, gin.H{"count": len(sites), "sites": sites})
}

// ConsoleGetSite handles GET /console/sites/:site_id
//
// RequireSiteGrant has already resolved and authorized the site, and put its
// internal id in the context.
func ConsoleGetSite(c *gin.Context) {
	site, err := database.GetConsoleSite(c.GetInt64("company_id"), c.GetInt64("site_id"))
	if errors.Is(err, models.ErrSiteNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	}
	if err != nil {
		logError(c, "console get site", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve site"})
		return
	}
	c.JSON(http.StatusOK, site)
}

// Site settings reuse GetSiteSettings and UpdateSiteSettings unchanged. Both
// read site_id from the context, which RequireSiteGrant sets to a site the
// operator is authorized for -- exactly as the site-key middleware sets it to
// the site whose credential was presented. The settings write also enqueues a
// SETTINGS sync job for every terminal at that site, which is business logic
// worth reusing rather than restating.

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

// ConsoleListApplications handles GET /console/applications
//
// Returns what the company has configured, which of those are enabled, and the
// catalog the platform offers. The catalog is included so a dashboard does not
// hard-code the capability list: adding one to the platform makes it appear.
//
// A company with nothing enabled gets empty arrays, which is a legitimate state
// and not a reason for the console to assume a default set.
func ConsoleListApplications(c *gin.Context) {
	companyID := c.GetInt64("company_id")

	configured, err := database.ListCompanyApplications(companyID)
	if err != nil {
		logError(c, "console list applications", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve applications"})
		return
	}
	if configured == nil {
		configured = []models.CompanyApplication{}
	}

	enabled, err := database.EnabledApplications(companyID)
	if err != nil {
		logError(c, "console list enabled applications", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve applications"})
		return
	}

	codes := make([]string, 0, len(enabled))
	for _, app := range enabled {
		codes = append(codes, app.Code)
	}

	c.JSON(http.StatusOK, models.ConsoleApplications{
		Configured: configured,
		Enabled:    codes,
		Available:  models.ApplicationOrder,
	})
}

// ConsoleSetApplication handles PUT /console/applications/:code
//
// Enables, disables or configures one capability. Settings are left alone when
// the body omits them, so toggling `enabled` does not silently discard a
// configuration the caller did not mention.
//
// MULTI_PURPOSE is rejected here, and that rejection is the model: it is a
// TERMINAL mode, describing a device that serves whatever its company has
// enabled. It is not itself a capability a company can turn on.
func ConsoleSetApplication(c *gin.Context) {
	code := c.Param("code")

	var req models.ConsoleApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Absent means enable: a PUT to a capability's URL without saying otherwise
	// is a request to have it.
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if req.Settings != nil {
		var probe map[string]any
		if err := json.Unmarshal(req.Settings, &probe); err != nil {
			c.JSON(http.StatusBadRequest,
				gin.H{"error": "Application settings must be a JSON object"})
			return
		}
	}

	app, err := database.SetCompanyApplication(c.GetInt64("company_id"), code, enabled, req.Settings)
	if errors.Is(err, models.ErrInvalidApplication) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":     "Unknown application",
			"available": models.ApplicationOrder,
		})
		return
	}
	if err != nil {
		logError(c, "console set application", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update application"})
		return
	}

	c.JSON(http.StatusOK, app)
}

// ---------------------------------------------------------------------------
// Terminals
// ---------------------------------------------------------------------------

// ConsoleListTerminals handles GET /console/terminals
//
// Reuses the fleet inventory query, then narrows it to the caller's granted
// sites. DeviceInventory carries no credential material -- not the device key,
// not its hash, not the site key -- so there is nothing to strip.
func ConsoleListTerminals(c *gin.Context) {
	scope, err := operatorSiteScope(c)
	if err != nil {
		logError(c, "console resolve site scope", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve terminals"})
		return
	}

	devices, err := database.ListDevices(c.GetInt64("company_id"), c.Query("outdated") == "true")
	if err != nil {
		logError(c, "console list terminals", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve terminals"})
		return
	}

	terminals := make([]models.DeviceInventory, 0, len(devices))
	for _, device := range devices {
		if scope == nil || containsSiteID(scope, device.SiteID) {
			terminals = append(terminals, device)
		}
	}

	c.JSON(http.StatusOK, gin.H{"count": len(terminals), "terminals": terminals})
}

func containsSiteID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// ConsoleTerminalSummary handles GET /console/terminals/summary
//
// Company-wide counts. Not narrowed by grants: the summary is a rollup of the
// fleet, and a partial one attributed to the whole company would be misleading.
// A site-scoped operator sees the same header figures as their colleagues.
func ConsoleTerminalSummary(c *gin.Context) {
	summary, err := database.GetFleetSummary(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console terminal summary", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve summary"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ConsoleGetTerminal handles GET /console/terminals/:serial
//
// The full inventory row -- the same projection the list uses -- plus what the
// terminal is assigned to do and what that currently resolves to.
//
// It used to return only the application fields, which meant a detail view held
// LESS than the list row it was opened from. The three original fields are still
// present at the same paths; everything else is additive.
func ConsoleGetTerminal(c *gin.Context) {
	detail, err := loadTerminalDetail(c.GetInt64("company_id"), c.Param("serial"))
	if errors.Is(err, models.ErrDeviceNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	}
	if err != nil {
		logError(c, "console get terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve terminal"})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// loadTerminalDetail composes the inventory row with the application assignment.
//
// Two reads rather than one join: the inventory projection already carries a
// firmware join whose correctness matters, and the application resolution needs
// the company's enabled capabilities. Keeping them apart means neither has to
// know about the other, and both stay the single definition used everywhere
// else.
func loadTerminalDetail(companyID int64, serial string) (*models.TerminalDetail, error) {
	inventory, err := database.GetDeviceInventoryBySerial(companyID, serial)
	if err != nil {
		return nil, err
	}

	application, err := database.GetDeviceApplication(companyID, serial)
	if err != nil {
		return nil, err
	}

	return &models.TerminalDetail{
		DeviceInventory:       *inventory,
		ApplicationMode:       application.Mode,
		EffectiveApplications: application.Effective,
	}, nil
}

// ConsoleSetTerminalMode handles PUT /console/terminals/:serial/application-mode
//
// A terminal may only be pointed at a capability its company has enabled, or at
// MULTI_PURPOSE. Refused at assignment rather than discovered at the door.
func ConsoleSetTerminalMode(c *gin.Context) {
	var req models.ConsoleTerminalModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	companyID := c.GetInt64("company_id")
	err := database.SetDeviceApplicationMode(companyID, c.Param("serial"), req.ApplicationMode)

	switch {
	case errors.Is(err, models.ErrInvalidApplication):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":     "Unknown application mode",
			"available": append([]string{models.AppMultiPurpose}, models.ApplicationOrder...),
		})
		return
	case errors.Is(err, models.ErrApplicationNotEnabled):
		c.JSON(http.StatusConflict, gin.H{
			"error": "That application is not enabled for this company",
		})
		return
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case err != nil:
		logError(c, "console set terminal mode", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update terminal"})
		return
	}

	// Returns the same shape as the detail read, so a client can replace what it
	// is holding with the response rather than refetching.
	detail, err := loadTerminalDetail(companyID, c.Param("serial"))
	if err != nil {
		logError(c, "console reload terminal", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update terminal"})
		return
	}
	c.JSON(http.StatusOK, detail)
}

// ---------------------------------------------------------------------------
// People
// ---------------------------------------------------------------------------

// Paging bounds.
//
// A default is applied rather than returning everything: a console list is
// rendered into a browser, and a company with tens of thousands of people would
// otherwise serialise the lot on every page load. The ceiling exists so a
// caller cannot ask for that anyway by passing a large limit.
const (
	defaultPeopleLimit = 50
	maxPeopleLimit     = 200
	maxSearchLength    = 100
)

// ConsoleListPeople handles GET /console/people?limit=&offset=&q=
//
// People are company-wide in this schema, not site-scoped, so site grants do
// not narrow this list. That is a documented limitation of the data model
// rather than an oversight here, and the console reports what the API can
// actually enforce instead of pretending to a scope it cannot apply.
//
// `q` matches the external id or the full name, anywhere and case-insensitively.
// The search runs in SQL rather than in the browser precisely because the list
// is bounded: filtering one page of fifty would search the page, not the roster.
//
// The site-key API's GET /api/v1/members is deliberately NOT changed to match.
// Terminals and existing tooling speak that contract, and quietly bounding it
// would silently truncate a roster somebody depends on being complete.
func ConsoleListPeople(c *gin.Context) {
	query := database.PeopleQuery{
		Search: truncateSearch(strings.TrimSpace(c.Query("q"))),
		Limit:  boundedQueryInt(c, "limit", defaultPeopleLimit, 1, maxPeopleLimit),
		Offset: boundedQueryInt(c, "offset", 0, 0, 0),
	}

	page, err := database.ListConsolePeople(c.GetInt64("company_id"), query)
	if err != nil {
		logError(c, "console list people", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve people"})
		return
	}

	people := make([]models.ConsolePerson, 0, len(page.People))
	for _, member := range page.People {
		people = append(people, consolePerson(member))
	}

	c.JSON(http.StatusOK, models.ConsolePeoplePage{
		Count:   len(people),
		Total:   page.Total,
		Limit:   query.Limit,
		Offset:  query.Offset,
		HasMore: query.Offset+len(people) < page.Total,
		People:  people,
	})
}

// boundedQueryInt reads a query parameter and clamps it.
//
// Clamps rather than rejects: a limit of 5000 is a caller asking for as much as
// it can have, not a malformed request, and failing it would be less useful than
// answering with the maximum. Anything unparseable falls back to the default for
// the same reason. A max of 0 means unbounded above, for offset.
func boundedQueryInt(c *gin.Context, name string, fallback, min, max int) int {
	raw := c.Query(name)
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

// truncateSearch bounds the search term. A term longer than any stored value
// cannot match anything, so there is nothing to gain by passing it to the
// database.
func truncateSearch(term string) string {
	if len(term) > maxSearchLength {
		return term[:maxSearchLength]
	}
	return term
}

// ConsoleGetPerson handles GET /console/people/:external_id
func ConsoleGetPerson(c *gin.Context) {
	member, err := database.GetMemberByID(c.GetInt64("company_id"), c.Param("external_id"))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	}
	if err != nil {
		logError(c, "console get person", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve person"})
		return
	}
	c.JSON(http.StatusOK, consolePerson(*member))
}

// ConsoleCreatePerson handles POST /console/people
//
// Calls the existing CreateMember, which enqueues the CREATE sync job to every
// terminal in the company inside the same transaction as the insert. Writing a
// second insert here would be writing a second, subtly different version of the
// rule that keeps the fleet in step.
//
// Category is optional. people.membership_type is NOT NULL, so something has to
// be written, but requiring an operator to supply one would be the product
// assuming a workflow -- a company doing visitor management has no membership.
func ConsoleCreatePerson(c *gin.Context) {
	var req models.ConsolePersonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.ExternalID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "external_id is required"})
		return
	}

	category := req.Category
	if category == "" {
		category = defaultPersonCategory
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}

	member := models.Member{
		MemberID:       req.ExternalID,
		FullName:       req.FullName,
		MembershipType: category,
		Active:         active,
		// No credential. Biometric enrolment happens at a terminal, through the
		// enrolment flow; the console does not write credentials.
	}

	err := database.CreateMember(c.GetInt64("company_id"), &member)
	if database.IsUniqueViolation(err) {
		c.JSON(http.StatusConflict, gin.H{"error": "A person with that external_id already exists"})
		return
	}
	if err != nil {
		logError(c, "console create person", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create person"})
		return
	}

	c.JSON(http.StatusCreated, consolePerson(member))
}

// defaultPersonCategory fills people.membership_type when a caller supplies
// nothing. Deliberately generic: the column is a legacy name from when this was
// a single-purpose product, and the platform has no opinion about what class of
// person a company records.
const defaultPersonCategory = "STANDARD"

// ConsoleUpdatePerson handles PUT /console/people/:external_id
//
// READS THE PERSON FIRST, AND CARRIES THEIR CREDENTIAL THROUGH UNCHANGED.
// UpdateMember writes `fingerprint_template = NULLIF($4,”)`, so calling it with
// an empty template DELETES the person's biometric enrolment. An operator
// correcting a spelling in a name must not silently unenrol them -- and the
// console has no business writing credentials at all, in either direction.
func ConsoleUpdatePerson(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	externalID := c.Param("external_id")

	existing, err := database.GetMemberByID(companyID, externalID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	}
	if err != nil {
		logError(c, "console load person", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update person"})
		return
	}

	var req models.ConsolePersonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	member := models.Member{
		MemberID:       externalID,
		FullName:       req.FullName,
		MembershipType: existing.MembershipType,
		Active:         existing.Active,
		// Preserved, not cleared. See the note on this handler.
		FingerprintTemplate: existing.FingerprintTemplate,
	}
	if req.Category != "" {
		member.MembershipType = req.Category
	}
	if req.Active != nil {
		member.Active = *req.Active
	}

	if err := database.UpdateMember(companyID, &member); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
			return
		}
		logError(c, "console update person", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update person"})
		return
	}

	c.JSON(http.StatusOK, consolePerson(member))
}

// ConsoleDeletePerson handles DELETE /console/people/:external_id
//
// Soft delete, and the existing call enqueues the DELETE sync job that is the
// only way an offline terminal ever learns to forget a credential.
func ConsoleDeletePerson(c *gin.Context) {
	if err := database.DeleteMember(c.GetInt64("company_id"), c.Param("external_id")); err != nil {
		logError(c, "console delete person", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete person"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

// ConsoleListOperators handles GET /console/operators
func ConsoleListOperators(c *gin.Context) {
	users, err := database.ListUsers(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console list operators", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve operators"})
		return
	}

	operators := make([]models.ConsoleOperator, 0, len(users))
	for _, user := range users {
		grants, err := database.ListSiteGrants(user.ID)
		if err != nil {
			logError(c, "console list operator grants", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve operators"})
			return
		}
		operators = append(operators, consoleOperator(user, grants))
	}

	c.JSON(http.StatusOK, gin.H{"count": len(operators), "operators": operators})
}

// ConsoleGetOperator handles GET /console/operators/:operator_id
func ConsoleGetOperator(c *gin.Context) {
	user, grants, ok := loadConsoleOperator(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, consoleOperator(*user, grants))
}

// loadConsoleOperator resolves the operator named in the path, within the
// caller's company. Reports false once it has written an error response.
func loadConsoleOperator(c *gin.Context) (*models.User, []models.SiteGrant, bool) {
	user, err := database.GetUserByPublicID(c.GetInt64("company_id"), c.Param("operator_id"))
	if errors.Is(err, models.ErrUserNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Operator not found"})
		return nil, nil, false
	}
	if err != nil {
		logError(c, "console get operator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve operator"})
		return nil, nil, false
	}

	grants, err := database.ListSiteGrants(user.ID)
	if err != nil {
		logError(c, "console list operator grants", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve operator"})
		return nil, nil, false
	}
	return user, grants, true
}

// mayManageRole reports whether the caller is allowed to create or modify an
// account at the given role.
//
// An ADMIN manages everyone below an OWNER but cannot create one, promote
// anyone to one, or touch an existing one. Otherwise "ADMIN" would be a
// synonym for "OWNER" one request later, and the distinction the role matrix
// draws would be decorative.
func mayManageRole(callerRole, targetRole string) bool {
	if targetRole == models.RoleOwner {
		return callerRole == models.RoleOwner
	}
	return middleware.RoleAtLeast(callerRole, models.RoleAdmin)
}

// ConsoleCreateOperator handles POST /console/operators
func ConsoleCreateOperator(c *gin.Context) {
	caller := middleware.Operator(c)

	var req models.ConsoleOperatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !models.OperatorRoles[req.Role] {
		c.JSON(http.StatusBadRequest, gin.H{"error": models.ErrInvalidRole.Error()})
		return
	}
	if !mayManageRole(caller.Role, req.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not create an operator at that role"})
		return
	}

	companyID := c.GetInt64("company_id")
	user, err := database.CreateUser(companyID, models.NewUser{
		Email:    req.Email,
		FullName: req.FullName,
		Password: req.Password,
		Role:     req.Role,
	})
	switch {
	case database.IsUniqueViolation(err):
		c.JSON(http.StatusConflict, gin.H{"error": "That email address is already in use"})
		return
	case errors.Is(err, models.ErrInvalidEmail),
		errors.Is(err, models.ErrPasswordTooShort),
		errors.Is(err, models.ErrPasswordTooLong),
		errors.Is(err, models.ErrInvalidRole):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console create operator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create operator"})
		return
	}

	// Site grants, if the caller supplied any. A failure here is reported, but
	// the account exists: replaying the grants is a retry of one request, while
	// rolling back an account whose password the caller has already chosen is
	// not something the caller can easily redo.
	if len(req.SiteIDs) > 0 {
		if err := database.ReplaceSiteGrants(companyID, user.ID, req.SiteIDs); err != nil {
			if errors.Is(err, models.ErrSiteNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":    "The operator was created, but one or more sites were not found",
					"operator": consoleOperator(*user, nil),
				})
				return
			}
			logError(c, "console grant sites", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "The operator was created, but its sites could not be set"})
			return
		}
	}

	grants, err := database.ListSiteGrants(user.ID)
	if err != nil {
		logError(c, "console list operator grants", err)
		grants = nil
	}

	c.JSON(http.StatusCreated, consoleOperator(*user, grants))
}

// ConsoleUpdateOperator handles PUT /console/operators/:operator_id
//
// Role, active state and password, each optional.
//
// AN OPERATOR MAY NOT CHANGE THEIR OWN ROLE OR DISABLE THEMSELVES. Not a
// courtesy: the sole OWNER of a company demoting themselves would leave nobody
// able to manage operators, with no way back that does not involve the
// database. Password changes are self-service through /auth/password.
func ConsoleUpdateOperator(c *gin.Context) {
	caller := middleware.Operator(c)
	target, _, ok := loadConsoleOperator(c)
	if !ok {
		return
	}

	var req models.ConsoleOperatorUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if !mayManageRole(caller.Role, target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not modify that operator"})
		return
	}
	if req.Role != nil && !mayManageRole(caller.Role, *req.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not assign that role"})
		return
	}
	if target.ID == caller.UserID && (req.Role != nil || req.Active != nil) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "You cannot change your own role or disable your own account",
		})
		return
	}

	companyID := c.GetInt64("company_id")

	if req.Role != nil {
		if err := database.SetUserRole(companyID, target.ID, *req.Role); err != nil {
			if errors.Is(err, models.ErrInvalidRole) {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			logError(c, "console set operator role", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update operator"})
			return
		}
	}

	if req.Active != nil {
		if err := database.SetUserActive(companyID, target.ID, *req.Active); err != nil {
			logError(c, "console set operator active", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update operator"})
			return
		}
	}

	if req.Password != nil {
		// An administrative reset revokes every session the target holds --
		// pass 0, sparing none. Someone whose password an administrator has
		// just changed should not still be signed in somewhere.
		if err := database.SetUserPassword(target.ID, *req.Password, 0); err != nil {
			if errors.Is(err, models.ErrPasswordTooShort) || errors.Is(err, models.ErrPasswordTooLong) {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			logError(c, "console reset operator password", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update operator"})
			return
		}
	}

	updated, grants, ok := loadConsoleOperator(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, consoleOperator(*updated, grants))
}

// ConsoleDeleteOperator handles DELETE /console/operators/:operator_id
func ConsoleDeleteOperator(c *gin.Context) {
	caller := middleware.Operator(c)
	target, _, ok := loadConsoleOperator(c)
	if !ok {
		return
	}

	if !mayManageRole(caller.Role, target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not remove that operator"})
		return
	}
	if target.ID == caller.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You cannot remove your own account"})
		return
	}

	if err := database.SoftDeleteUser(c.GetInt64("company_id"), target.ID); err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Operator not found"})
			return
		}
		logError(c, "console delete operator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove operator"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ConsoleGetOperatorSites handles GET /console/operators/:operator_id/sites
func ConsoleGetOperatorSites(c *gin.Context) {
	user, grants, ok := loadConsoleOperator(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"count": len(grants),
		"sites": grants,
		// An empty grant set is not "no access": it means the operator is not
		// scoped, and reaches every site in the company. The flag says so
		// rather than leaving a dashboard to infer it from an empty array.
		"all_sites": middleware.RoleAtLeast(user.Role, models.RoleAdmin) || len(grants) == 0,
	})
}

// ConsoleSetOperatorSites handles PUT /console/operators/:operator_id/sites
//
// Replaces the grants wholesale. Every site is resolved inside the caller's own
// company before anything is written, so a grant can never point across a
// tenant boundary, and an unknown site fails the whole call rather than being
// skipped.
func ConsoleSetOperatorSites(c *gin.Context) {
	caller := middleware.Operator(c)
	target, _, ok := loadConsoleOperator(c)
	if !ok {
		return
	}

	if !mayManageRole(caller.Role, target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not modify that operator"})
		return
	}

	var req models.ConsoleSiteGrantsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	companyID := c.GetInt64("company_id")
	err := database.ReplaceSiteGrants(companyID, target.ID, req.SiteIDs)
	switch {
	case errors.Is(err, models.ErrSiteNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "One or more sites were not found"})
		return
	case errors.Is(err, models.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Operator not found"})
		return
	case err != nil:
		logError(c, "console set operator sites", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update site access"})
		return
	}

	grants, err := database.ListSiteGrants(target.ID)
	if err != nil {
		logError(c, "console list operator grants", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update site access"})
		return
	}
	if grants == nil {
		grants = []models.SiteGrant{}
	}

	c.JSON(http.StatusOK, gin.H{
		"count":     len(grants),
		"sites":     grants,
		"all_sites": middleware.RoleAtLeast(target.Role, models.RoleAdmin) || len(grants) == 0,
	})
}
