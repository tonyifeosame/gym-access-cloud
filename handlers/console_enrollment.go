package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator-driven fingerprint enrolment, from the console.
//
// ---------------------------------------------------------------------------
// THE WORKFLOW THIS COMPLETES
// ---------------------------------------------------------------------------
//
// Adding a person and enrolling their finger are two actions, and they always
// were on the terminal's side -- a CREATE job leaves a member row with NO
// credential, by design, because biometric material is captured at a sensor and
// never travels. What was missing was the second action having any way to
// happen: `POST /enrollment/start` wrote a row nothing delivered, so the only
// working route to a bound finger was somebody with a USB cable typing
// `enroll <member> <name>` into the serial console of the right terminal.
//
// The serial console is a bench tool. It is not a workflow for a gym operator,
// and at a site with more than one door it is not even a reliable one for a
// technician -- there is nothing on a unit that says which of them it is.
//
// So the operator chooses the terminal, here, where they can see the list.
//
// ---------------------------------------------------------------------------
// WHY THE START ROUTE NAMES THE TERMINAL AND NOT THE PERSON
// ---------------------------------------------------------------------------
//
// POST /console/terminals/:serial/enrollments, not
// POST /console/people/:external_id/enrollment.
//
// The terminal is the resource being COMMANDED: the operator is telling one
// door to enter enrolment mode. Putting it in the path is what lets
// RequireTerminalGrant do the authorization -- it resolves the serial inside the
// caller's company, applies their site grant, and puts the resolved device_id in
// the context. A serial in the BODY would have to be resolved and authorized by
// hand in the handler, which is the same rule written a second time and is
// exactly how a tenancy boundary develops a hole.
//
// The reads are person-addressed, because "what is happening with this person's
// enrolment" is a question about the person.

// ConsoleStartEnrollment handles POST /console/terminals/:serial/enrollments
//
// MANAGER, behind RequireTerminalGrant. The terminal is authorised by the
// middleware; the person is resolved inside the caller's company by the store.
func ConsoleStartEnrollment(c *gin.Context) {
	var req models.ConsoleEnrollmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	identity := middleware.Operator(c)
	input := database.StartEnrollmentInput{
		CompanyID: c.GetInt64("company_id"),
		// SET BY RequireTerminalGrant, not resolved here. One resolution, one
		// authorization -- see the note at the top of this file.
		DeviceID:         c.GetInt64("device_id"),
		ExternalID:       req.ExternalID,
		ExpiresInSeconds: req.ExpiresInSeconds,
	}
	if identity != nil {
		input.ActorUserID = identity.UserID
		input.ActorEmail = identity.Email
	}

	enrollment, err := database.StartFingerprintEnrollment(input)
	switch {
	case errors.Is(err, models.ErrPersonNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	case errors.Is(err, models.ErrDeviceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Terminal not found"})
		return
	case errors.Is(err, database.ErrTerminalNotEnrollable):
		// 409 rather than 400: the request is well formed and the operator is
		// allowed to make it. The terminal is in a state that cannot answer,
		// which is a fact about the world rather than about the message.
		c.JSON(http.StatusConflict, gin.H{
			"error": "That terminal cannot run an enrolment. It is disabled, retired, " +
				"or has never been provisioned with a credential of its own.",
		})
		return
	case err != nil:
		logError(c, "console start enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start the enrolment"})
		return
	}

	// AUDIT, NOT AN EVENT. This is an operator deciding something -- the person,
	// the door, the window -- and it belongs in the trail that names which
	// operator did what. The field event is written when the TERMINAL reports
	// what happened, which is a different fact with a different author. See the
	// note in handlers/enrollment.go.
	recordAudit(c, auditEnrollmentStarted, auditTargetPerson, enrollment.ID,
		enrollment.ExternalID, gin.H{
			"terminal":           enrollment.TerminalSerial,
			"site":               enrollment.SiteName,
			"expires_in_seconds": req.ExpiresInSeconds,
		})

	c.JSON(http.StatusCreated, enrollment)
}

// ConsoleGetPersonEnrollment handles GET /console/people/:external_id/enrollment
//
// VIEWER: "is she enrolled yet, and at which door" is a front-desk question.
//
// THE POLLED ENDPOINT. A console screen watching an enrolment reads this every
// couple of seconds, so it does two things in one response -- the enrolment's
// state and the person's credential state -- because a client that had to
// reconcile two endpoints could render "Enrolled successfully" beside "Not
// enrolled" for one refresh interval.
func ConsoleGetPersonEnrollment(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	externalID := c.Param("external_id")

	member, err := database.GetMemberByID(companyID, externalID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	}
	if err != nil {
		logError(c, "console get person for enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the enrolment"})
		return
	}

	response := models.ConsoleEnrollmentResponse{
		ExternalID:        member.MemberID,
		BiometricEnrolled: member.FingerprintTemplate != "",
	}

	enrollment, err := database.LatestEnrollmentForPerson(companyID, externalID)
	switch {
	case errors.Is(err, database.ErrEnrollmentNotFound):
		// NULL, not an error and not an empty object. "This person has never had
		// an enrolment" is a different fact from "their last one failed", and
		// the console renders them differently -- one offers the action, the
		// other reports an outcome.
		response.Enrollment = nil
	case err != nil:
		logError(c, "console read enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the enrolment"})
		return
	default:
		response.Enrollment = enrollment
	}

	c.JSON(http.StatusOK, response)
}

// ConsoleCancelEnrollment handles DELETE /console/people/:external_id/enrollment
//
// MANAGER. Stops a live enrolment and cancels the job carrying it, so the
// selected terminal can no longer complete it -- AckJobCompleted has always
// refused a CANCELLED job, and this relies on that rather than restating it.
//
// A terminal that had ALREADY fetched the job may still be showing the prompt.
// It stops when its window closes, and if somebody presents a finger before
// then, the report is refused rather than bound: see
// database.DispositionForDeviceReport. Cancelling means cancelled.
func ConsoleCancelEnrollment(c *gin.Context) {
	companyID := c.GetInt64("company_id")
	externalID := c.Param("external_id")

	identity := middleware.Operator(c)
	reason := "cancelled by an operator"
	if identity != nil {
		reason = "cancelled by " + identity.Email
	}

	enrollment, err := database.CancelFingerprintEnrollment(companyID, externalID, reason)
	switch {
	case errors.Is(err, models.ErrPersonNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Person not found"})
		return
	case errors.Is(err, database.ErrEnrollmentNotFound):
		// 409, not 404. The person exists and the route is right; there is
		// simply nothing live to stop -- most often because it finished while
		// the operator was reaching for the button.
		c.JSON(http.StatusConflict, gin.H{"error": "There is no enrolment in progress for this person"})
		return
	case err != nil:
		logError(c, "console cancel enrollment", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel the enrolment"})
		return
	}

	recordAudit(c, auditEnrollmentCancelled, auditTargetPerson, enrollment.ID,
		enrollment.ExternalID, gin.H{"terminal": enrollment.TerminalSerial})

	c.JSON(http.StatusOK, enrollment)
}
