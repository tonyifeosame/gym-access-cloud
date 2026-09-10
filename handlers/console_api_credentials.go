package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Integration credential management, /api/v1/console/api-credentials.
//
// ---------------------------------------------------------------------------
// WHY THESE LIVE IN THE CONSOLE AND NOT IN THE PUBLIC API
// ---------------------------------------------------------------------------
//
// A credential cannot mint another credential. Issuing one is an administrative
// act by a human who is accountable for it, so it happens where every other
// administrative act happens: behind an operator session, behind CSRF, behind
// ADMIN, and written into the company's own audit trail.
//
// That also means an integration whose key leaks cannot quietly issue itself a
// replacement. The remedy is a person in the console, which is the same shape as
// site-key rotation and terminal revocation.
//
// ---------------------------------------------------------------------------
// ADMIN, MATCHING SITE-KEY ROTATION
// ---------------------------------------------------------------------------
//
// These routes are mounted on the same group as POST /sites/{id}/api-key and
// POST /terminals/{serial}/revoke -- the acts that mint or withdraw a machine
// credential. Nothing here is MANAGER, because "read your roster from outside"
// is a decision about the company's data leaving it.
//
// THE SECRET IS RETURNED ONCE, by issue and by rotate, and by nothing else. No
// read path in this file or in database/api_credentials.go can produce one.

// ConsoleListAPICredentials handles GET /console/api-credentials.
//
// Revoked and superseded credentials are included. "What integrations has this
// company ever had, and what happened to them" is the question asked during a
// review; a list that hid the revoked ones would answer a different one. The
// status field distinguishes them.
func ConsoleListAPICredentials(c *gin.Context) {
	list, err := database.ListAPICredentials(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "console list api credentials", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve integration credentials"})
		return
	}
	c.JSON(http.StatusOK, list)
}

// ConsoleGetAPICredential handles GET /console/api-credentials/:id.
func ConsoleGetAPICredential(c *gin.Context) {
	credential, err := database.GetAPICredential(c.GetInt64("company_id"), c.Param("id"))
	if errors.Is(err, models.ErrAPICredentialNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Integration credential not found"})
		return
	}
	if err != nil {
		logError(c, "console get api credential", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve the integration credential"})
		return
	}
	c.JSON(http.StatusOK, credential)
}

// ConsoleCreateAPICredential handles POST /console/api-credentials.
//
// THREE THINGS HAPPEN BEFORE THE DATABASE IS TOUCHED, and each answers a
// different failure:
//
//	scope expansion    an unknown scope is a 400 naming the ones that exist,
//	                   rather than a credential that silently cannot do what the
//	                   operator ticked;
//	issuer bounding    an operator cannot grant a scope their own role may not,
//	                   so the role ladder still means something once a scope
//	                   exists that ADMIN itself should not hand out;
//	environment        a deployment cannot mint a credential it would then
//	                   refuse to accept.
func ConsoleCreateAPICredential(c *gin.Context) {
	var req models.APICredentialRequest
	// Cached bind, so bodyHasKey below can tell an absent expires_at from an
	// explicit null without re-reading a drained body.
	if err := bindCachedJSON(c, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	identity := middleware.Operator(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	// EXPANDED HERE, ONCE. members:write stores members:read alongside it, so a
	// permission check downstream is a membership test with no inference in it
	// and the console shows the operator exactly what the key can do.
	scopes, err := models.ExpandScopes(req.Scopes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":            err.Error(),
			"available_scopes": models.AllScopes(),
		})
		return
	}

	// Checked against the EXPANDED set, so an implied scope cannot be used to
	// smuggle in something the issuer could not have asked for directly.
	if above := models.ScopesWithinIssuerRole(scopes, identity.Role, middleware.RoleAtLeast); above != "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error":    "Your role cannot grant this scope",
			"scope":    above,
			"required": models.Scopes[above].MinRole,
		})
		return
	}

	environment, err := resolveCredentialEnvironment(req.Environment)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// ABSENT MEANS "the default lifetime"; an explicit null means "never". The
	// binding tag cannot tell those apart, so the pointer does: a caller that
	// omitted the field gets a year, and one that deliberately sent null gets a
	// non-expiring credential and had to ask for it.
	expiresAt := req.ExpiresAt
	if !bodyHasKey(c, "expires_at") {
		defaulted := time.Now().Add(models.DefaultAPICredentialLifetime)
		expiresAt = &defaulted
	}

	issued, err := database.IssueAPICredential(database.APICredentialIssueInput{
		CompanyID:     c.GetInt64("company_id"),
		Name:          req.Name,
		Environment:   environment,
		Scopes:        scopes,
		SitePublicIDs: req.SiteIDs,
		ExpiresAt:     expiresAt,
		ActorUserID:   identity.UserID,
		ActorEmail:    identity.Email,
	})
	switch {
	case errors.Is(err, models.ErrAPICredentialLimit):
		c.JSON(http.StatusConflict, gin.H{
			"error": err.Error(),
			"code":  "API_CREDENTIAL_LIMIT_REACHED",
		})
		return
	case errors.Is(err, models.ErrAPICredentialNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrAPICredentialSiteUnknown),
		errors.Is(err, models.ErrAPICredentialNameRequired),
		errors.Is(err, models.ErrAPICredentialNameTooLong),
		errors.Is(err, models.ErrAPICredentialExpiryInPast),
		errors.Is(err, models.ErrNoScopes),
		errors.Is(err, models.ErrUnknownScope):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console issue api credential", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to issue the integration credential"})
		return
	}

	// THE PREFIX, NEVER THE SECRET. The same rule the site-key rotation record
	// keeps: enough to identify which credential this was, nothing that would
	// let a reader of the trail become it.
	recordAudit(c, auditAPICredentialIssued, auditTargetAPICredential,
		issued.ID, issued.Name, gin.H{
			"key_prefix":   issued.KeyPrefix,
			"environment":  issued.Environment,
			"scopes":       issued.Scopes,
			"grants_write": models.ScopeSetHasWrite(issued.Scopes),
			"all_sites":    issued.AllSites,
			"site_count":   len(issued.Sites),
			"expires_at":   issued.ExpiresAt,
		})

	c.JSON(http.StatusCreated, issued)
}

// ConsoleRotateAPICredential handles POST /console/api-credentials/:id/rotate.
//
// Answers with a NEW credential; the old one keeps working until its grace
// window closes. A grace of zero is an immediate cutover and is the right choice
// when the reason for rotating is that the old secret leaked, so it is
// expressible rather than being a special case somebody has to ask support for.
func ConsoleRotateAPICredential(c *gin.Context) {
	var req models.APICredentialRotateRequest
	// An empty body is a valid rotation with default grace, so a bind failure on
	// no body must not be an error. Anything present is still parsed strictly.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	identity := middleware.Operator(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	grace := models.DefaultAPIKeyRotationGrace
	if req.GraceSeconds != nil {
		if *req.GraceSeconds < 0 {
			c.JSON(http.StatusBadRequest,
				gin.H{"error": "grace_seconds cannot be negative"})
			return
		}
		grace = time.Duration(*req.GraceSeconds) * time.Second
	}

	issued, err := database.RotateAPICredential(
		c.GetInt64("company_id"), c.Param("id"), grace, identity.UserID, identity.Email)
	switch {
	case errors.Is(err, models.ErrAPICredentialNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Integration credential not found"})
		return
	case errors.Is(err, models.ErrAPICredentialRevoked),
		errors.Is(err, models.ErrAPICredentialSuperseded):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrAPIKeyGraceTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console rotate api credential", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to rotate the integration credential"})
		return
	}

	recordAudit(c, auditAPICredentialRotated, auditTargetAPICredential,
		issued.ID, issued.Name, gin.H{
			"key_prefix":        issued.KeyPrefix,
			"replaces":          c.Param("id"),
			"grace_seconds":     int(grace.Seconds()),
			"immediate_cutover": grace == 0,
			"reason":            req.Reason,
		})

	c.JSON(http.StatusOK, issued)
}

// ConsoleRevokeAPICredential handles DELETE /console/api-credentials/:id.
//
// Effective on the next request: the authentication path reads the row every
// time and nothing caches it. The row itself is kept, because the audit trail
// references it and because "what did this integration do before we turned it
// off" is the next question.
func ConsoleRevokeAPICredential(c *gin.Context) {
	var req models.APICredentialRevokeRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	identity := middleware.Operator(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	credential, err := database.RevokeAPICredential(
		c.GetInt64("company_id"), c.Param("id"), req.Reason, identity.UserID)
	switch {
	case errors.Is(err, models.ErrAPICredentialNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Integration credential not found"})
		return
	case errors.Is(err, models.ErrAPICredentialRevoked):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console revoke api credential", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to revoke the integration credential"})
		return
	}

	recordAudit(c, auditAPICredentialRevoked, auditTargetAPICredential,
		credential.ID, credential.Name, gin.H{
			"key_prefix": credential.KeyPrefix,
			"reason":     credential.RevokedReason,
		})

	c.JSON(http.StatusOK, credential)
}

// ConsoleRevokeAllAPICredentials handles POST /console/api-credentials/revoke-all.
//
// THE INCIDENT-RESPONSE CONTROL. One call, one audit record, one number to
// report. It matches every credential that can still authenticate, including the
// superseded half of a rotation -- "turn off every integration" must not leave
// the one nobody was thinking about still working.
func ConsoleRevokeAllAPICredentials(c *gin.Context) {
	var req models.APICredentialRevokeRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	identity := middleware.Operator(c)
	if identity == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return
	}

	revoked, err := database.RevokeAllAPICredentials(
		c.GetInt64("company_id"), req.Reason, identity.UserID)
	if err != nil {
		logError(c, "console revoke all api credentials", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to revoke the integration credentials"})
		return
	}

	// Written even when nothing was revoked. Somebody reaching for this control
	// is an event worth recording whether or not there was anything to turn off.
	recordAudit(c, auditAPICredentialRevokedAll, auditTargetAPICredential, "",
		"all integration credentials", gin.H{
			"revoked": revoked,
			"reason":  req.Reason,
		})

	c.JSON(http.StatusOK, models.APICredentialRevokedAll{Revoked: revoked})
}

// ConsoleAPICredentialUsage handles GET /console/api-credentials/:id/usage.
//
// The answer to "is this key still in use, and by what", which is what an
// operator needs before revoking something they no longer recognise. Counts come
// from the rate-limiter's own buckets rolled up daily, so they cost nothing
// extra on the request path.
func ConsoleAPICredentialUsage(c *gin.Context) {
	days := 30
	if raw := c.Query("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			days = parsed
		}
	}

	usage, err := database.GetAPICredentialUsage(c.GetInt64("company_id"), c.Param("id"), days)
	if errors.Is(err, models.ErrAPICredentialNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Integration credential not found"})
		return
	}
	if err != nil {
		logError(c, "console api credential usage", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Failed to retrieve credential usage"})
		return
	}
	c.JSON(http.StatusOK, usage)
}

// resolveCredentialEnvironment settles which environment a credential is minted
// for.
//
// A DEPLOYMENT CANNOT MINT A CREDENTIAL IT WOULD REFUSE. Naming the other
// environment is an error rather than a silent override: an operator who asked
// for a test key on a live deployment has misunderstood something, and handing
// them a live key labelled as what they asked for would compound it.
func resolveCredentialEnvironment(requested string) (string, error) {
	deployment := APIEnvironment()
	if requested == "" {
		return deployment, nil
	}

	normalised, err := models.NormaliseEnvironment(requested)
	if err != nil {
		return "", err
	}
	if normalised != deployment {
		return "", errors.New(
			"this deployment issues " + deployment + " credentials; a " + normalised +
				" credential would not authenticate against it")
	}
	return normalised, nil
}
