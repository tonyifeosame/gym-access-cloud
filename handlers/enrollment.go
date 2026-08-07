package handlers

import (
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// StartEnrollment handles POST /enrollment/start
func StartEnrollment(c *gin.Context) {
	var req models.EnrollmentStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	companyID := c.GetInt64("company_id")

	// Check if member exists
	member, err := database.GetMemberByID(companyID, req.MemberID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}

	// Create enrollment request
	enrollment, err := database.CreateEnrollmentRequest(companyID, req.MemberID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create enrollment request"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Enrollment request created",
		"member":  member,
		"request": enrollment,
	})
}

// GetPendingEnrollments handles GET /enrollment/pending
func GetPendingEnrollments(c *gin.Context) {
	requests, err := database.GetPendingEnrollmentRequests(c.GetInt64("company_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve pending enrollments"})
		return
	}

	c.JSON(http.StatusOK, requests)
}

// SubmitEnrollmentResult handles POST /enrollment/result
func SubmitEnrollmentResult(c *gin.Context) {
	var req models.EnrollmentResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Complete enrollment (updates member fingerprint and request status)
	if err := database.CompleteEnrollment(c.GetInt64("company_id"), req.MemberID, req.FingerprintTemplate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete enrollment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Enrollment completed successfully",
		"member_id": req.MemberID,
	})
}
