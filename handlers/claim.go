package handlers

import (
	"errors"
	"net/http"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Device claim codes.
//
// firmware-protocol-requirements.md §6. Two routes with opposite authentication
// stories, which is why they are in one file: the console mints a code with an
// authenticated, scoped, audited operator identity, and the terminal redeems it
// with no credential at all -- because not having one yet is the entire point.
//
// WHAT THIS REMOVES. Enrolling a terminal required the SITE API KEY on an
// installer's laptop. That key registers devices at the site and rotates any of
// their credentials, so bringing up one door left the site's provisioning secret
// in a shell history and a terminal's scrollback, and the device key it produced
// was then read out and pasted somewhere else.
//
// WHAT IT DOES NOT REMOVE, stated because the firmware document is explicit
// about it: the installer still needs a serial cable, because the code has to be
// typed somewhere and the fitted keypad cannot reach the admin menu on this
// hardware revision. This removes the credential exposure, not the cable.

// ClaimRequest is the body a terminal sends. The field names are the firmware's
// (provisioning.cpp, buildClaimBody) and are a wire contract.
type ClaimRequest struct {
	ClaimCode    string `json:"claim_code" binding:"required"`
	SerialNumber string `json:"serial_number" binding:"required"`
}

// ClaimDevice handles POST /api/v1/devices/claim
//
// UNAUTHENTICATED BY NECESSITY. The caller is a terminal that has no credential;
// obtaining one is what it is here for.
//
// EVERY FAILURE IS THE SAME 401 WITH THE SAME BODY. Wrong code, expired code,
// superseded code, correct code with the wrong serial -- all identical.
// Distinguishing them would let an unauthenticated caller learn that a code is
// real but the serial is wrong, which is exactly the fact that turns an
// intercepted code back into something worth having. The rate limiter bounds how
// fast that indistinguishable answer can be sampled.
//
// The status codes matter to the firmware and are chosen for it: 404 means "this
// server does not implement claim codes" and sends the installer to `set key`;
// any other 4xx means the code was refused. Answering 404 for a bad code would
// tell an installer the server lacks the feature it just used.
func ClaimDevice(c *gin.Context) {
	var req ClaimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 400, not 404: the endpoint exists and the body was wrong.
		c.JSON(http.StatusBadRequest, gin.H{"error": "claim_code and serial_number are required"})
		return
	}

	claimed, err := database.RedeemClaimCode(req.ClaimCode, req.SerialNumber, c.ClientIP())
	switch {
	case errors.Is(err, database.ErrClaimCodeRequired),
		errors.Is(err, database.ErrClaimSerialRequired):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return

	case errors.Is(err, database.ErrClaimRefused),
		errors.Is(err, models.ErrDeviceDisabled),
		errors.Is(err, models.ErrDeviceSiteMismatch):
		// A disabled terminal and a site mismatch collapse into the same
		// refusal deliberately. Both are real states, and neither is anything
		// an unauthenticated caller should be able to learn.
		//
		// Logged so an operator CAN find out, from the one place that is
		// entitled to know.
		logClaimRefusal(c, req.SerialNumber, err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "The claim code was refused"})
		return

	case err != nil:
		logError(c, "redeem claim code", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to claim device"})
		return
	}

	// AUDITED WITH NO OPERATOR. There is no session here, so the audit record
	// carries the site, the serial, the code that was used and the address that
	// used it. "Which unit claimed this code, from where, and when" is the
	// question an installation dispute turns into.
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:      claimed.CompanyID,
		ActorEmail:     claimed.IssuedByEmail,
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         auditDeviceClaimed,
		TargetType:     auditTargetTerminal,
		TargetPublicID: claimed.ClaimPublicID,
		TargetLabel:    claimed.SerialNumber,
		Changes: map[string]any{
			"site":           claimed.SiteName,
			"bootstrap_jobs": claimed.BootstrapJobs,
			// The KEY IS NOT HERE and must never be. The prefix is not recorded
			// either: the audit row already names the device, and the device row
			// carries the prefix.
			"issued_by": claimed.IssuedByEmail,
		},
	})

	// The response the firmware parses (provisioning.cpp, parseClaimResponse).
	// It reads api_key and optionally serial_number, and refuses a body over 512
	// bytes -- so nothing else belongs in here.
	c.JSON(http.StatusOK, gin.H{
		"api_key":       claimed.APIKey,
		"serial_number": claimed.SerialNumber,
	})
}

// logClaimRefusal records a refused claim in the operational log.
//
// Unauthenticated brute force against this endpoint should be VISIBLE, and the
// response deliberately says nothing, so the log is the only place the
// distinction survives.
func logClaimRefusal(c *gin.Context, serial string, err error) {
	logError(c, "claim refused serial="+serial, err)
}

// ---------------------------------------------------------------------------
// Issuing, from the console
// ---------------------------------------------------------------------------

// ConsoleClaimCodeRequest mints a code for one serial at one site.
type ConsoleClaimCodeRequest struct {
	SerialNumber string `json:"serial_number" binding:"required"`

	// ExpiresInMinutes is optional. The store defaults it and caps it: a code
	// that lives for a week is a site key with extra steps.
	ExpiresInMinutes int `json:"expires_in_minutes,omitempty"`
}

// ConsoleIssueClaimCode handles POST /console/sites/:site_id/claim-codes
//
// ADMIN, matching site key rotation. Issuing a claim code is authorising
// hardware to join a site and receive a credential, which is the same class of
// act as minting the provisioning key -- not day-to-day work.
//
// The code comes back ONCE. Nothing stores it and no read endpoint returns it.
func ConsoleIssueClaimCode(c *gin.Context) {
	var req ConsoleClaimCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	actor := middleware.Operator(c)
	actorEmail := ""
	if actor != nil {
		actorEmail = actor.Email
	}

	ttl := time.Duration(req.ExpiresInMinutes) * time.Minute

	issued, err := database.IssueClaimCode(c.GetInt64("company_id"), c.GetInt64("site_id"),
		req.SerialNumber, ttl, actorUserID(actor), actorEmail)
	switch {
	case errors.Is(err, database.ErrClaimSerialRequired),
		errors.Is(err, models.ErrDeviceSerialTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrSiteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Site not found"})
		return
	case err != nil:
		logError(c, "issue claim code", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue claim code"})
		return
	}

	// The PREFIX is audited, never the code. Same rule site key creation
	// follows: the trail must be able to say which credential was minted
	// without being a place to read one.
	recordAudit(c, auditDeviceClaimCodeIssued, auditTargetTerminal, "", issued.SerialNumber,
		gin.H{
			"site":             issued.SiteName,
			"code_prefix":      issued.Prefix,
			"expires_at":       issued.ExpiresAt,
			"superseded_codes": issued.SupersededCount,
		})

	c.JSON(http.StatusCreated, gin.H{
		"claim_code":    issued.Code,
		"code_prefix":   issued.Prefix,
		"serial_number": issued.SerialNumber,
		"site_name":     issued.SiteName,
		"expires_at":    issued.ExpiresAt,
		// Stated in the payload rather than left to documentation, because a
		// client that stores it is the failure mode.
		"shown_once": true,
		// So the console can warn that an installer's earlier printout has just
		// stopped working.
		"superseded_codes": issued.SupersededCount,
	})
}
