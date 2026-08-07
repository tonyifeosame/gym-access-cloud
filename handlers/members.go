package handlers

import (
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// GetMembers handles GET /members
func GetMembers(c *gin.Context) {
	members, err := database.GetAllMembers(c.GetInt64("company_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve members"})
		return
	}
	// A nil slice marshals to `null`, which a strict client parser will reject
	// where it expects an array. Empty results must still be an empty array.
	if members == nil {
		members = []models.Member{}
	}
	c.JSON(http.StatusOK, members)
}

// GetMember handles GET /members/:id
func GetMember(c *gin.Context) {
	memberID := c.Param("id")
	member, err := database.GetMemberByID(c.GetInt64("company_id"), memberID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	c.JSON(http.StatusOK, member)
}

// CreateMember handles POST /members
func CreateMember(c *gin.Context) {
	var member models.Member
	if err := c.ShouldBindJSON(&member); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := database.CreateMember(c.GetInt64("company_id"), &member); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create member"})
		return
	}

	c.JSON(http.StatusCreated, member)
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
	if err := database.UpdateMember(c.GetInt64("company_id"), &member); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update member"})
		return
	}

	c.JSON(http.StatusOK, member)
}

// DeleteMember handles DELETE /members/:id
func DeleteMember(c *gin.Context) {
	memberID := c.Param("id")

	if err := database.DeleteMember(c.GetInt64("company_id"), memberID); err != nil {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve member changes"})
		return
	}
	if members == nil {
		members = []models.Member{}
	}

	c.JSON(http.StatusOK, members)
}
