package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// The legacy people surface, /api/v1/members, authenticated by the site key.
//
// DEPRECATED but not removed: existing tooling speaks this contract, and the
// console's /console/people is the replacement. What changed is what it
// discloses.
//
// NO RESPONSE HERE CARRIES CREDENTIAL MATERIAL. The projection below drops
// fingerprint_template, which this endpoint used to return for every person in
// the company to anyone holding any site key. That field is a credential
// locator today and was designed to hold a template; either way a provisioning
// secret installed on hardware at one location must not be able to read it for
// the whole tenant.
//
// Credentials are reachable through the console, per person, behind an operator
// session -- see handlers/console_credentials.go. Sealed biometric material
// reaches devices over the sync path and reaches nothing else, ever.

// GetMembers handles GET /members
func GetMembers(c *gin.Context) {
	members, err := database.GetAllMembers(c.GetInt64("company_id"))
	if err != nil {
		logError(c, "list members", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve members"})
		return
	}

	// A nil slice marshals to `null`, which a strict client parser will reject
	// where it expects an array. Empty results must still be an empty array.
	out := make([]models.MemberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, models.NewMemberResponse(m))
	}
	c.JSON(http.StatusOK, out)
}

// GetMember handles GET /members/:id
func GetMember(c *gin.Context) {
	memberID := c.Param("id")

	// A missing member is a 404; anything else is a server fault and must not be
	// reported as "not found", or a database outage looks like an empty roster.
	member, err := database.GetMemberByID(c.GetInt64("company_id"), memberID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if err != nil {
		logError(c, "get member", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve member"})
		return
	}
	c.JSON(http.StatusOK, models.NewMemberResponse(*member))
}

// CreateMember handles POST /members
func CreateMember(c *gin.Context) {
	var member models.Member
	if err := c.ShouldBindJSON(&member); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Re-using an existing member_id is the caller's mistake, not a server
	// fault. Reporting it as 500 made a client retry a request that could never
	// succeed.
	err := database.CreateMember(c.GetInt64("company_id"), &member)
	if database.IsUniqueViolation(err) {
		c.JSON(http.StatusConflict, gin.H{"error": "Member ID already exists"})
		return
	}
	if err != nil {
		logError(c, "create member", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create member"})
		return
	}

	c.JSON(http.StatusCreated, models.NewMemberResponse(member))
}

// UpdateMember handles PUT /members/:id
func UpdateMember(c *gin.Context) {
	memberID := c.Param("id")

	var req models.MemberUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	member := models.Member{
		MemberID:            memberID,
		FullName:            req.FullName,
		MembershipType:      req.MembershipType,
		Active:              req.Active,
		FingerprintTemplate: req.FingerprintTemplate,
	}
	// A member that does not exist (or was soft-deleted) produces no row to
	// update. That is a 404, not a server fault.
	err := database.UpdateMember(c.GetInt64("company_id"), &member)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if err != nil {
		logError(c, "update member", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update member"})
		return
	}

	c.JSON(http.StatusOK, models.NewMemberResponse(member))
}

// DeleteMember handles DELETE /members/:id
func DeleteMember(c *gin.Context) {
	memberID := c.Param("id")

	if err := database.DeleteMember(c.GetInt64("company_id"), memberID); err != nil {
		logError(c, "delete member", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete member"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Member deleted successfully"})
}

// GetMemberChanges handles GET /members/changes
func GetMemberChanges(c *gin.Context) {
	since := c.Query("since")
	if since == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "since parameter required"})
		return
	}

	members, err := database.GetMembersChangedSince(c.GetInt64("company_id"), since)
	if err != nil {
		logError(c, "list member changes", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve member changes"})
		return
	}

	// Projected like every other read here. This is a CHANGES FEED, not the sync
	// path: terminals receive credentials as sync jobs, which carry sealed
	// material to the device that needs it. Nothing in the field reads this
	// endpoint -- the deployed firmware calls four device routes and this is not
	// one of them -- so the template it used to disclose served no client and
	// exposed every person in the company to any site key.
	out := make([]models.MemberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, models.NewMemberResponse(m))
	}
	c.JSON(http.StatusOK, out)
}
