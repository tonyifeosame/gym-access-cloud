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

// recordEnrolmentEvent writes what a terminal reported about an enrolment.
//
// BEST EFFORT, and logged when it fails. The enrolment itself has already
// committed by the time this runs, so failing the response here would make the
// terminal retry an enrolment the platform has accepted -- producing a second
// capture at the sensor for a finger that is already stored.
//
// The event carries no biometric material and no locator. It says who, at which
// terminal, and what happened.
func recordEnrolmentEvent(c *gin.Context, externalID, eventType, decision, reasonCode, detail string) {
	companyID := c.GetInt64("company_id")
	personID, _ := database.ResolvePersonRowID(companyID, externalID)

	payload := map[string]any{}
	if detail != "" {
		payload["detail"] = detail
	}

	if _, err := database.RecordAccessEvent(database.AccessEvent{
		CompanyID:         companyID,
		SiteID:            c.GetInt64("site_id"),
		DeviceID:          c.GetInt64("device_id"),
		PersonID:          personID,
		SubjectExternalID: externalID,
		EventType:         eventType,
		Decision:          decision,
		ReasonCode:        reasonCode,
		Payload:           payload,
	}); err != nil {
		logError(c, "record enrolment event", err)
	}
}

// SubmitEnrollmentResult handles POST /enrollment/result
func SubmitEnrollmentResult(c *gin.Context) {
	var req models.EnrollmentResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	companyID := c.GetInt64("company_id")
	deviceID := c.GetInt64("device_id")

	// ---------------------------------------------------------------------
	// WAS THIS ENROLMENT CANCELLED?
	// ---------------------------------------------------------------------
	//
	// The one case where a report does not bind a credential. An operator
	// cancelling means cancelled: the job is cancelled so the terminal cannot
	// complete it, and if that terminal had already been handed the job and
	// somebody presented a finger before its window closed, the report arrives
	// here and must not quietly produce the credential the operator just
	// stopped.
	//
	// EVERY OTHER CASE BINDS, including a report against an enrolment that had
	// expired or failed a moment earlier, and including one with no enrolment
	// at all -- which is the bench path, a technician enrolling at a terminal's
	// own console with no platform involvement. Breaking that would take away
	// the only way to enrol during an outage. The full reasoning is on
	// database.DispositionForDeviceReport.
	//
	// Gated on deviceID because this handler is mounted twice and only the
	// device path knows which terminal is speaking.
	if deviceID != 0 {
		disposition, err := database.DispositionForDeviceReport(companyID, deviceID, req.MemberID)
		if err != nil {
			logError(c, "read enrolment disposition", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete enrollment"})
			return
		}
		if !disposition.Bind {
			// 200, NOT an error. The terminal did nothing wrong and there is
			// nothing for it to retry -- retrying would re-present a report the
			// platform has already decided about, for ever. `bound: false` is
			// the honest answer, and the event is where an operator finds out.
			recordEnrolmentEvent(c, req.MemberID, models.EventEnrolFailed,
				models.DecisionDenied, "ENROLMENT_CANCELLED", disposition.Reason)

			c.JSON(http.StatusOK, gin.H{
				"message":   "Enrollment was cancelled before it was reported; no credential was recorded",
				"member_id": req.MemberID,
				"bound":     false,
				"reason":    disposition.Reason,
			})
			return
		}
	}

	// Complete enrollment (updates member fingerprint and request status).
	// No such member means there is nothing to enrol against -- a 404, not a
	// server fault, so the terminal does not retry a template it can never store.
	err := database.CompleteEnrollment(companyID, deviceID, req.MemberID, req.FingerprintTemplate)

	// A PERSON AN OPERATOR DELETED IS A SETTLED ANSWER, NOT A MISSING ONE.
	//
	// 410 Gone, not 404. Both are terminal for a well-behaved client, but 404
	// reads as "try again later, it might turn up" and this never will -- and
	// the terminal's retry policy treats the two differently in practice: the
	// enrolment report for a deleted person was retried until its budget ran
	// out and then DISCARDED, leaving the platform believing a credential sat
	// on a sensor that had erased it.
	//
	// The placement is converged to REMOVED here rather than left stale,
	// because that stale row is what would block re-enrolling the person if
	// they were ever restored.
	if errors.Is(err, models.ErrPersonDeleted) {
		if deviceID != 0 {
			if _, removeErr := database.RecordPlacement(deviceID, database.PlacementReport{
				ExternalID: req.MemberID,
				State:      models.PlacementRemoved,
			}); removeErr != nil {
				// Logged, never returned. The answer to the terminal does not
				// depend on our bookkeeping succeeding, and a failure here must
				// not turn a settled 410 back into something retryable.
				logError(c, "converging placement for a deleted person", removeErr)
			}
		}
		c.JSON(http.StatusGone, gin.H{
			"error":     "This person has been deleted; the enrolment cannot be recorded",
			"member_id": req.MemberID,
			"terminal":  true,
		})
		return
	}

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

	// The field event: what the TERMINAL did, written where the report arrives.
	//
	// SEPARATE FROM THE AUDIT TRAIL, deliberately. An operator starting an
	// enrolment is an administrative decision and is audited in
	// console_enrollment.go; this is the hardware reporting an outcome, and it
	// belongs with the other things terminals report. Two authors, two trails.
	//
	// NOT AN ACCESS EVENT EITHER. It carries DecisionRecorded rather than
	// GRANTED or DENIED, because nothing was admitted or refused -- no door
	// moved and no relay fired. An enrolment that showed up in an access log as
	// a grant would put a door opening into the trail an attendance report is
	// built from.
	if deviceID != 0 {
		recordEnrolmentEvent(c, req.MemberID, models.EventEnrolled,
			models.DecisionRecorded, "", "")
	}

	if deviceID != 0 && req.Credential != nil {
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
