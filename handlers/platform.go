package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Platform administration endpoints, /api/v1/platform/*.
//
// GP-01 and CON-01: NO API CREATED A COMPANY. `companies` had exactly one writer
// -- the INSERT in migration 002 that created 'Default Company' so pre-existing
// rows had a tenant to belong to -- so onboarding a second customer required
// somebody with SQL against production, and there was no way to rename one, put
// one on hold, or set its retention policy either.
//
// WHY THIS IS A SEPARATE IDENTITY RATHER THAN A BIGGER OPERATOR ROLE. The
// obvious fix is a role above OWNER that reaches every company. It is wrong, and
// 008_operator_accounts.sql already said why: every function in database/ takes
// a companyID and every query filters on it, and that single-tenant-per-call
// contract is what makes the tenancy boundary checkable at all. An operator that
// could reach several companies would dissolve it everywhere at once.
//
// So users.company_id stays NOT NULL, every console query is untouched, and this
// file authenticates a DIFFERENT credential class against a different table with
// a different cookie -- the same separation the site key and the operator
// session already have.
//
// WHAT THIS SURFACE DELIBERATELY CANNOT REACH. There is no route here that reads
// a tenant's people, credentials, events, terminals or site keys, and no
// function in database/platform.go that could serve one. That is enforced by
// absence rather than by a role check, because a support credential that can
// read every customer's biometric roster is the most valuable secret on the
// installation and the safest way not to leak it is to be unable to load it.
//
// The tenant-facing counts this surface DOES return stop at cardinality: how
// many sites, terminals, people and operators, which is what "is this tenant
// healthy and how big is it" needs and is the whole of what onboarding and
// support have a claim to.

// Audit actions for the platform surface. Written against the company they act
// on, so they appear in that tenant's own trail -- a customer asking "who
// created this company and who was given the first account" is asking about
// their own installation and should not need their vendor to answer it.
const (
	auditCompanyCreated       = "COMPANY_CREATED"
	auditCompanyUpdated       = "COMPANY_UPDATED"
	auditCompanyDeactivated   = "COMPANY_DEACTIVATED"
	auditCompanyReactivated   = "COMPANY_REACTIVATED"
	auditFirstOperatorCreated = "COMPANY_FIRST_OPERATOR_CREATED"

	auditTargetCompany = "COMPANY"
)

// actorRolePlatform names the credential class in the audit trail.
//
// NOT one of the operator roles. A reader of the trail must be able to tell that
// a change came from the platform rather than from somebody inside the company,
// and reusing "OWNER" for it would be a lie that is impossible to detect later.
const actorRolePlatform = "PLATFORM"

// setPlatformCookie writes the platform session cookie.
//
// Same attributes as the operator cookie and for the same reasons, with one
// difference that matters: the name. Both cookies are Path=/, so a browser
// genuinely sends each to the other's routes, and the only thing that keeps a
// platform session from being offered to a console route as though it were an
// operator's is that the two middlewares read different names.
func setPlatformCookie(c *gin.Context, token string) {
	name, secure := middleware.PlatformCookieConfig()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, token, 0, "/", "", secure, true)
}

func clearPlatformCookie(c *gin.Context) {
	name, secure := middleware.PlatformCookieConfig()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", "", secure, true)
}

// recordPlatformAudit writes one platform action into the affected company's
// trail.
//
// The actor is denormalised as an email with the PLATFORM role and no
// actor_user_id, because a platform administrator is not a row in `users` and
// the column references that table. That is exactly the case the denormalised
// actor columns were added for.
func recordPlatformAudit(c *gin.Context, companyID int64, action,
	targetPublicID, label string, changes gin.H) {

	identity := middleware.PlatformAdmin(c)

	entry := database.AuditEntry{
		CompanyID:      companyID,
		ActorRole:      actorRolePlatform,
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         action,
		TargetType:     auditTargetCompany,
		TargetPublicID: targetPublicID,
		TargetLabel:    label,
	}
	if identity != nil {
		entry.ActorEmail = identity.Email
	}
	if len(changes) > 0 {
		entry.Changes = map[string]any(changes)
	}

	database.WriteAuditEvent(entry)
}

// buildPlatformSession assembles the body login and /me share, for the same
// reason the operator one does: whatever the console gets at login is exactly
// what it gets on the next reload.
func buildPlatformSession(identity *models.PlatformIdentity) models.PlatformSessionResponse {
	return models.PlatformSessionResponse{
		Admin: models.PlatformAdmin{
			ID:       identity.AdminPublicID,
			Email:    identity.Email,
			FullName: identity.FullName,
			Active:   identity.Active,
		},
		CSRFToken: identity.CSRFToken,
		// Derived from the remaining duration the database reported rather than
		// forwarded from a stored timestamp, for the reason set out on the
		// operator session response: the instant is then expressed against the
		// clock that generated the response, and the duration beside it stays
		// authoritative for a client that trusts neither.
		SessionExpiresAt: time.Now().UTC().
			Add(time.Duration(identity.ExpiresInSeconds) * time.Second),
		SessionExpiresInSeconds: identity.ExpiresInSeconds,
	}
}

// PlatformLogin handles POST /api/v1/platform/login
//
// Mounted behind the same rate limiter class the operator login uses, and every
// rejection is the same 401 with the same message: unknown address, wrong
// password, disabled account. The store also spends one bcrypt comparison on an
// unknown address so the timing does not answer what the message refuses to.
func PlatformLogin(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	admin, err := database.AuthenticatePlatformPassword(req.Email, req.Password)

	var locked *models.AccountLockedError
	switch {
	case errors.As(err, &locked):
		c.Header("Retry-After", strconv.Itoa(locked.RetryAfter()))
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       "Too many failed attempts, this account is temporarily locked",
			"retry_after": locked.RetryAfter(),
		})
		return
	case errors.Is(err, models.ErrInvalidCredentials):
		log.Printf("request_id=%s platform_auth=failed email=%s ip=%s",
			middleware.RequestID(c), redactEmail(database.NormalizeEmail(req.Email)), c.ClientIP())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	case err != nil:
		logError(c, "authenticate platform admin", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
		return
	}

	adminID, err := database.PlatformAdminIDByPublicID(admin.ID)
	if err != nil {
		logError(c, "resolve platform admin", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
		return
	}

	creds, err := database.CreatePlatformSession(adminID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		logError(c, "create platform session", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
		return
	}

	// Resolved the same way every subsequent request will resolve it, so the body
	// returned here is exactly what /me returns next.
	identity, err := database.AuthenticatePlatformSession(creds.Token)
	if err != nil || identity == nil {
		logError(c, "resolve new platform session", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication unavailable"})
		return
	}

	setPlatformCookie(c, creds.Token)
	log.Printf("request_id=%s platform_auth=success admin=%s ip=%s",
		middleware.RequestID(c), identity.AdminPublicID, c.ClientIP())

	c.JSON(http.StatusOK, buildPlatformSession(identity))
}

// PlatformMe handles GET /api/v1/platform/me
//
// Returns the CSRF token, which is why it is derived from the session token
// rather than stored: after a page reload the browser still holds the cookie but
// the console has lost its in-memory copy.
func PlatformMe(c *gin.Context) {
	identity := middleware.PlatformAdmin(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}
	c.JSON(http.StatusOK, buildPlatformSession(identity))
}

// PlatformLogout handles POST /api/v1/platform/logout
//
// Idempotent. The cookie is cleared AND the row is revoked: clearing alone would
// leave a session anyone holding a copy of the cookie could keep using.
func PlatformLogout(c *gin.Context) {
	identity := middleware.PlatformAdmin(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	if err := database.RevokePlatformSession(identity.SessionID); err != nil {
		logError(c, "revoke platform session", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to log out"})
		return
	}

	clearPlatformCookie(c)
	c.Status(http.StatusNoContent)
}

// PlatformListCompanies handles GET /api/v1/platform/companies
func PlatformListCompanies(c *gin.Context) {
	companies, err := database.ListCompanies()
	if err != nil {
		logError(c, "platform list companies", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve companies"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count":     len(companies),
		"companies": companies,
	})
}

// PlatformGetCompany handles GET /api/v1/platform/companies/:company_id
func PlatformGetCompany(c *gin.Context) {
	company, err := database.GetCompanyByPublicID(c.Param("company_id"))
	switch {
	case errors.Is(err, models.ErrCompanyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Company not found"})
		return
	case err != nil:
		logError(c, "platform get company", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve the company"})
		return
	}

	c.JSON(http.StatusOK, company)
}

// PlatformCreateCompany handles POST /api/v1/platform/companies
//
// THE ENDPOINT THE PLATFORM DID NOT HAVE. Onboarding a customer was a manual
// INSERT against production before this existed.
//
// The company is created EMPTY and with no operator. Issuing the first one is a
// separate call, so an onboarding that fails halfway leaves a tenant somebody
// can see and finish rather than a half-built one inside a single request.
func PlatformCreateCompany(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	var req models.PlatformCompanyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	company, err := database.CreateCompany(database.NewCompany{
		Name:         req.Name,
		Slug:         req.Slug,
		ContactEmail: req.ContactEmail,
		Timezone:     req.Timezone,
	})
	switch {
	case errors.Is(err, database.ErrCompanyNameRequired),
		errors.Is(err, models.ErrInvalidSlug):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, database.ErrCompanySlugTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "platform create company", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create the company"})
		return
	}

	if id, err := database.CompanyIDByPublicID(company.ID); err == nil {
		recordPlatformAudit(c, id, auditCompanyCreated, company.ID, company.Name, gin.H{
			"slug":     company.Slug,
			"timezone": company.Timezone,
		})
	}

	log.Printf("request_id=%s platform=company-created company=%s slug=%s",
		middleware.RequestID(c), company.ID, company.Slug)

	c.JSON(http.StatusCreated, company)
}

// PlatformUpdateCompany handles PUT /api/v1/platform/companies/:company_id
//
// Rename, contact, timezone, retention policy, and activate/deactivate.
//
// DEACTIVATION IS THE SHARP ONE. A company with active=false fails the
// company_ok predicate in AuthenticateSession, so every operator session in it
// stops resolving on the next request -- the tenant is suspended, not deleted,
// and nothing is destroyed. It is audited as its own action rather than as a
// field change for exactly that reason.
func PlatformUpdateCompany(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	publicID := c.Param("company_id")

	before, err := database.GetCompanyByPublicID(publicID)
	switch {
	case errors.Is(err, models.ErrCompanyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Company not found"})
		return
	case err != nil:
		logError(c, "platform load company", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update the company"})
		return
	}

	var req models.PlatformCompanyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	company, err := database.UpdateCompany(publicID, database.CompanyUpdate{
		Name:               req.Name,
		ContactEmail:       req.ContactEmail,
		Timezone:           req.Timezone,
		Active:             req.Active,
		DeactivatedReason:  req.DeactivatedReason,
		EventRetentionDays: req.EventRetentionDays,
		AuditRetentionDays: req.AuditRetentionDays,
	})
	switch {
	case errors.Is(err, models.ErrCompanyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Company not found"})
		return
	case errors.Is(err, database.ErrCompanyNameRequired):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "platform update company", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update the company"})
		return
	}

	action := auditCompanyUpdated
	changes := gin.H{}
	if req.Name != nil {
		changes["name"] = *req.Name
	}
	if req.Timezone != nil {
		changes["timezone"] = *req.Timezone
	}
	if req.EventRetentionDays != nil {
		changes["event_retention_days"] = *req.EventRetentionDays
	}
	if req.AuditRetentionDays != nil {
		changes["audit_retention_days"] = *req.AuditRetentionDays
	}
	if req.Active != nil && *req.Active != before.Active {
		if *req.Active {
			action = auditCompanyReactivated
		} else {
			action = auditCompanyDeactivated
			if req.DeactivatedReason != nil {
				changes["reason"] = *req.DeactivatedReason
			}
		}
	}

	if id, err := database.CompanyIDByPublicID(company.ID); err == nil {
		recordPlatformAudit(c, id, action, company.ID, company.Name, changes)
	}

	c.JSON(http.StatusOK, company)
}

// PlatformCreateFirstOperator handles
// POST /api/v1/platform/companies/:company_id/operators
//
// ONLY INTO A COMPANY THAT HAS NO OPERATORS, refused by a query predicate rather
// than a check-then-insert. This is onboarding: once a customer has an owner,
// every further account is created by them through the console. A platform
// identity that could add an operator to a running tenant at any time would be a
// standing back door into every customer on the installation.
//
// THE DEFAULT IS AN INVITATION, not a password. The response then carries a
// single-use link and no credential, so the vendor never knows the customer's
// owner password -- which is the whole of PPL-02 applied to the one account that
// matters most.
func PlatformCreateFirstOperator(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	publicID := c.Param("company_id")

	var req models.PlatformFirstOperatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, token, err := database.CreateFirstOperatorForCompany(publicID, models.NewUser{
		Email:    req.Email,
		FullName: req.FullName,
		Password: req.Password,
		Role:     models.RoleOwner,
	})
	switch {
	case errors.Is(err, models.ErrCompanyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Company not found"})
		return
	case errors.Is(err, database.ErrCompanyHasOperators):
		c.JSON(http.StatusConflict, gin.H{
			"error": "That company already has an operator. Further accounts are " +
				"created from its own console."})
		return
	case errors.Is(err, models.ErrInvalidEmail),
		errors.Is(err, models.ErrPasswordTooShort),
		errors.Is(err, models.ErrPasswordTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case database.IsUniqueViolation(err):
		c.JSON(http.StatusConflict, gin.H{"error": "That email address is already in use"})
		return
	case err != nil:
		logError(c, "platform create first operator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create the operator"})
		return
	}

	recordPlatformAudit(c, user.CompanyID, auditFirstOperatorCreated,
		user.PublicID, user.Email, gin.H{
			"role":    user.Role,
			"invited": token != nil,
		})

	response := gin.H{
		"operator": operatorSummary{
			ID:       user.PublicID,
			Email:    user.Email,
			FullName: user.FullName,
			Role:     user.Role,
		},
		"must_change_password": true,
	}
	if token != nil {
		response["invitation"] = token
		response["delivery"] = invitationDeliveryNotice
	}

	c.JSON(http.StatusCreated, response)
}
