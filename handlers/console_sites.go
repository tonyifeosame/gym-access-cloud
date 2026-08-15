package handlers

import (
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Site management, /api/v1/console/sites.
//
// The reads live in console.go alongside the rest of the dashboard; this file
// is the writes, because they share one concern that the reads do not: they are
// the only place in the operator API where a PROVISIONING SECRET exists.
//
// THE RULE THAT GOVERNS THIS FILE. A site API key registers terminals and
// rotates their device credentials. It is returned by exactly two handlers here,
// each in a response type named for the fact, and by nothing else anywhere. The
// database cannot help a handler leak one -- it stores a SHA-256 hash and the
// plaintext exists only in the local variable that just generated it.
//
// AUTHORIZATION. Everything here is ADMIN, which is stricter than the MANAGER
// gate on day-to-day writes and looser than the OWNER gate on what the whole
// deployment is for. Creating a site mints a credential and retiring one stops
// doors opening; neither is day-to-day work. It also lands correctly on the
// grant rule: ADMIN and OWNER are never site-scoped, so "an operator scoped to
// Site A creates Site B" is not a state that can arise -- and the routes that
// name a site still go through RequireSiteGrant, which resolves it inside the
// caller's company and answers 404 for anyone else's.

// ConsoleCreateSite handles POST /console/sites
//
// Returns 201 with the site AND its one-time provisioning key.
//
// The key is generated server-side from crypto/rand. A caller-supplied key
// would be a caller-chosen secret for the credential that provisions door
// hardware, and there is no version of that worth offering.
func ConsoleCreateSite(c *gin.Context) {
	var req models.ConsoleSiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	site, credential, err := database.CreateSite(c.GetInt64("company_id"), database.NewSite{
		Name:     req.Name,
		Address:  req.Address,
		Timezone: req.Timezone,
	})
	switch {
	case errors.Is(err, models.ErrSiteNameRequired),
		errors.Is(err, models.ErrSiteNameTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrSiteNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "console create site", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create site"})
		return
	}

	// The KEY IS NOT IN THE AUDIT RECORD, and the prefix is. That is the whole of
	// what database/audit.go means by "redacted by the caller": the trail must be
	// able to say which credential was minted without being a place to read one.
	recordAudit(c, auditSiteCreated, auditTargetSite, site.ID, site.Name, gin.H{
		"timezone":       site.Timezone,
		"api_key_prefix": credential.Prefix,
	})

	// The ONE response in the operator API that carries a provisioning key.
	c.JSON(http.StatusCreated, models.ConsoleSiteCreatedResponse{
		Site: *site,
		Credential: models.ConsoleSiteCredential{
			APIKey:       credential.Key,
			APIKeyPrefix: credential.Prefix,
			ShownOnce:    true,
		},
	})
}

// ConsoleUpdateSite handles PUT /console/sites/:site_id
//
// Metadata only. RequireSiteGrant has already resolved the site inside the
// caller's company and put its internal id in the context, so a site belonging
// to another tenant never reaches here.
//
// `active: false` DEACTIVATES, which is reversible: the site key and every
// terminal at the site stop authenticating, and nothing is destroyed. That is a
// different operation from DELETE, and the two are kept apart deliberately --
// "we are shutting this depot for a month" and "this depot is gone" should not
// be the same button.
func ConsoleUpdateSite(c *gin.Context) {
	var req models.ConsoleSiteUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	site, err := database.UpdateSite(c.GetInt64("company_id"), c.GetInt64("site_id"),
		database.SiteUpdate{
			Name:     req.Name,
			Address:  req.Address,
			Timezone: req.Timezone,
			Active:   req.Active,
		})
	switch {
	case errors.Is(err, models.ErrSiteNameRequired),
		errors.Is(err, models.ErrSiteNameTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrSiteNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrSiteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	case err != nil:
		logError(c, "console update site", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update site"})
		return
	}

	// Only the fields the caller actually sent. Recording every column would make
	// a rename indistinguishable from a deactivation in the trail.
	changes := gin.H{}
	if req.Name != nil {
		changes["name"] = *req.Name
	}
	if req.Address != nil {
		changes["address"] = *req.Address
	}
	if req.Timezone != nil {
		changes["timezone"] = *req.Timezone
	}
	if req.Active != nil {
		changes["active"] = *req.Active
	}
	recordAudit(c, auditSiteUpdated, auditTargetSite, site.ID, site.Name, changes)

	c.JSON(http.StatusOK, site)
}

// ConsoleRetireSite handles DELETE /console/sites/:site_id
//
// Soft-deletes the site AND every terminal at it, in one transaction, and
// reports how many that was.
//
// THIS STOPS DOORS OPENING. A terminal whose site is retired fails
// authentication immediately on its own device key as well as on the site key,
// because both middlewares refuse a device whose site is gone. The count comes
// back in the response so a client can state it rather than leaving an operator
// to find out from the hardware.
//
// One-way through the API. PUT with `active: false` is the reversible
// alternative and is what an operator closing a location temporarily wants.
func ConsoleRetireSite(c *gin.Context) {
	result, err := database.RetireSite(c.GetInt64("company_id"), c.GetInt64("site_id"))
	if errors.Is(err, models.ErrSiteNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	}
	if err != nil {
		logError(c, "console retire site", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retire site"})
		return
	}

	// The terminal count goes into the record as well as the response. "How many
	// doors stopped working when this site was retired" is the question somebody
	// asks afterwards, and the response is gone by then.
	recordAudit(c, auditSiteRetired, auditTargetSite, result.SitePublicID,
		result.SiteName, gin.H{"terminals_retired": result.TerminalsRetired})

	// 200 with a body rather than 204: the terminal count is the whole point,
	// and an empty response would discard the one fact the operator needs.
	c.JSON(http.StatusOK, models.ConsoleSiteRetiredResponse{
		Retired:          true,
		TerminalsRetired: result.TerminalsRetired,
	})
}

// ConsoleRotateSiteAPIKey handles POST /console/sites/:site_id/api-key
//
// Issues a replacement provisioning key and invalidates the previous one
// immediately. No overlap window: a window is a period in which a credential
// believed to be revoked still provisions door hardware.
//
// The response carries the new key ONCE, and the count of terminals at the site
// that have never been issued a device credential of their own -- the ones that
// certainly still authenticate with the site key and have just been locked out.
// Terminals holding their own device key are unaffected; that is a different
// secret and rotation does not touch it.
func ConsoleRotateSiteAPIKey(c *gin.Context) {
	credential, legacy, err := database.RotateSiteAPIKey(
		c.GetInt64("company_id"), c.GetInt64("site_id"))
	if errors.Is(err, models.ErrSiteNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	}
	if err != nil {
		logError(c, "console rotate site key", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rotate the site key"})
		return
	}

	// The prefix, never the key. And the count of terminals this just locked out,
	// because that is the consequence somebody will be asking about.
	//
	// Named by site_name rather than by UUID: RequireSiteGrant puts the name in
	// the context and the rotation returns no site body, so this is the
	// natural-key case recordAudit documents.
	recordAudit(c, auditSiteKeyRotated, auditTargetSite, "", c.GetString("site_name"),
		gin.H{
			"api_key_prefix":   credential.Prefix,
			"legacy_terminals": legacy,
		})

	c.JSON(http.StatusOK, models.ConsoleSiteRotatedResponse{
		Credential: models.ConsoleSiteCredential{
			APIKey:       credential.Key,
			APIKeyPrefix: credential.Prefix,
			ShownOnce:    true,
		},
		LegacyTerminals: legacy,
	})
}
