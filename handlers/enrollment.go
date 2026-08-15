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

	// The structured placement, when the caller is a DEVICE and sent one.
	//
	// Best effort and deliberately non-fatal: the enrolment itself has already
	// committed, and failing the response now would make the terminal retry an
	// enrolment the platform has already accepted -- producing a second
	// enrolment attempt at the sensor for a finger that is already stored.
	//
	// Gated on device_id because this handler is mounted TWICE: once behind the
	// device credential and once behind the site key. Only the device path knows
	// which terminal is speaking, and a placement has to name one.
	response := gin.H{
		"message":   "Enrollment completed successfully",
		"member_id": req.MemberID,
	}

	if deviceID := c.GetInt64("device_id"); deviceID != 0 && req.Credential != nil {
		placement, err := database.RecordPlacement(deviceID, database.PlacementReport{
			ExternalID:     req.MemberID,
			CredentialType: req.Credential.CredentialType,
			TemplateFormat: req.Credential.TemplateFormat,
			Vendor:         req.Credential.Vendor,
			Slot:           req.Credential.Slot,
			State:          models.PlacementPlaced,
		})
		if err != nil {
			// Logged, not returned. The locator in `people` is still written and
			// the terminal still works exactly as it did before placements
			// existed, so this degrades to the old behaviour rather than to a
			// broken one.
			logError(c, "record enrolment placement", err)
		} else {
			response["credential_id"] = placement.CredentialID
			response["placement_id"] = placement.PlacementID
		}
	}

	c.JSON(http.StatusOK, response)
}
