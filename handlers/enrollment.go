package handlers

import (
	"database/sql"
	"errors"
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
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if err != nil {
		logError(c, "get member for enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start enrollment"})
		return
	}

	// Create enrollment request
	enrollment, err := database.CreateEnrollmentRequest(companyID, req.MemberID)
	if err != nil {
		logError(c, "create enrollment request", err)
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
		logError(c, "list pending enrollments", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve pending enrollments"})
		return
	}
	// Empty must be `[]`, not `null`, so a client can iterate unconditionally
	if requests == nil {
		requests = []models.EnrollmentRequest{}
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

	// Complete enrollment (updates member fingerprint and request status).
	// No such member means there is nothing to enrol against -- a 404, not a
	// server fault, so the terminal does not retry a template it can never store.
	err := database.CompleteEnrollment(c.GetInt64("company_id"), req.MemberID, req.FingerprintTemplate)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if err != nil {
		logError(c, "complete enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete enrollment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "Enrollment completed successfully",
		"member_id": req.MemberID,
	})
}
