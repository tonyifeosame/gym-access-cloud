package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/service"
)

// The public API's site write routes (037).
//
// THE SAME SHAPE THE MEMBER WRITES ALREADY HAVE, deliberately: decode strictly
// with unknown fields refused, call ONE service method, hand any failure to
// RespondServiceError, and audit what was actually changed. Nothing here
// queries the database, reads a company id from the request, or chooses a
// status for a failure.
//
// WHAT IS NOT HERE, AND WILL NOT BE ADDED QUIETLY. There is no delete: retiring
// a site cascades to every terminal at the location and stops doors opening by
// fingerprint, which is a decision that belongs with a person in the console.
// There is no route that reads or rotates the provisioning key, and no field on
// either body that could reach one.
//
// THE AUDIT ACTOR IS THE CREDENTIAL OR THE GRANT. recordIntegrationAudit
// already writes the non-secret display form of whichever authenticated --
// a key prefix for an integration credential, the client id for an OAuth grant
// -- so both appear in the trail without this file knowing which it has.

// siteCreateBody is POST /sites.
//
// POINTERS THROUGHOUT so "absent" and "empty" are different requests: the
// service answers missing_field for the first and invalid_field for the second,
// and an integrator debugging a blank in their own data needs to be told which
// one they sent.
type siteCreateBody struct {
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	Country  *string `json:"country"`
	Timezone *string `json:"timezone"`
	// Active is accepted only so that sending it can be REFUSED by name. A new
	// site is always active; silently ignoring the field would leave a caller
	// believing they created a deactivated location.
	Active *bool `json:"active"`
}

// sitePatchBody is PATCH /sites/{site_id}. Absent fields keep their value.
type sitePatchBody struct {
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	Country  *string `json:"country"`
	Timezone *string `json:"timezone"`
	Active   *bool   `json:"active"`
}

// PublicCreateSite handles POST /api/public/v1/sites. Scope sites:write.
func PublicCreateSite(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	var body siteCreateBody
	if !decodePublicBody(c, &body) {
		return
	}

	site, err := publicAPI().sites.Create(c.Request.Context(), tc, service.SiteInput{
		Name:     body.Name,
		Address:  body.Address,
		Country:  body.Country,
		Timezone: body.Timezone,
		Active:   body.Active,
	})
	if err != nil {
		RespondServiceError(c, "public create site", err)
		return
	}

	recordIntegrationAudit(c, tc, auditSiteCreated, auditTargetSite, site.ID, site.Name, gin.H{
		"name":     site.Name,
		"country":  site.Country,
		"timezone": site.Timezone,
		"address":  site.Address,
	})
	c.JSON(http.StatusCreated, site)
}

// PublicUpdateSite handles PATCH /api/public/v1/sites/{site_id}. Scope
// sites:write. Partial: absent fields keep their value.
//
// THE AUDIT RECORDS WHAT WAS ASKED FOR, not the whole resulting row. A trail
// that restated every field on every update would make it impossible to see, at
// a glance, that somebody deactivated a site.
func PublicUpdateSite(c *gin.Context) {
	tc := tenantOrRefuse(c)
	if tc == nil {
		return
	}
	if !checkQuery(c) {
		return
	}
	var body sitePatchBody
	if !decodePublicBody(c, &body) {
		return
	}

	site, err := publicAPI().sites.Update(c.Request.Context(), tc, c.Param("site_id"),
		service.SiteInput{
			Name:     body.Name,
			Address:  body.Address,
			Country:  body.Country,
			Timezone: body.Timezone,
			Active:   body.Active,
		})
	if err != nil {
		RespondServiceError(c, "public update site", err)
		return
	}

	changes := gin.H{}
	if body.Name != nil {
		changes["name"] = site.Name
	}
	if body.Address != nil {
		changes["address"] = site.Address
	}
	if body.Country != nil {
		changes["country"] = site.Country
	}
	if body.Timezone != nil {
		changes["timezone"] = site.Timezone
	}
	if body.Active != nil {
		changes["active"] = site.Active
	}
	recordIntegrationAudit(c, tc, auditSiteUpdated, auditTargetSite, site.ID, site.Name, changes)
	c.JSON(http.StatusOK, site)
}
