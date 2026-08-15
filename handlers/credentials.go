package handlers

import (
	"errors"
	"log"
	"net/http"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Operator credential handover: invitations, forced first change, and reset.
//
// PPL-02 and SEC-10, which are the same problem at two ends of an account's
// life. THE PLATFORM HAD NO WAY TO GIVE SOMEBODY A CREDENTIAL AND NO WAY FOR
// THEM TO RECOVER ONE.
//
// Creating an operator required the CALLER to choose the new operator's password
// and hand it over themselves. The realistic consequence is not hypothetical:
// initial credentials travel by chat or email in plain text and typically stay
// unchanged, so the administrator who created the account knows the password
// indefinitely and nothing records that they do. At the other end there was no
// reset at all -- a sole OWNER who forgot their password needed database access
// to recover, which is the same failure the company-creation blocker had: an
// ordinary operation only somebody with SQL could perform.
//
// WHAT IS DELIBERATELY NOT HERE: delivery. This platform has no transactional
// email, and writing a send function that logs the link would be worse than
// being explicit about it. The token is returned ONCE to the administrator who
// minted it, the response says so in a field rather than in documentation, and
// wiring real delivery is a change in this file and nowhere else.
//
// THE SELF-SERVICE RESET IS AN ENUMERATION SURFACE by necessity -- it is
// unauthenticated and takes an email address. Every response is 202 with the
// same body whether or not the address exists, and the store returns the same
// thing for both cases so that this handler cannot accidentally branch.

// invitationDeliveryNotice is returned beside every minted token.
//
// In the payload rather than in the documentation, because a client that stores
// the token is the failure mode and the response is what a developer reads.
const invitationDeliveryNotice = "This link is shown once and is not stored. " +
	"Send it to the operator over a channel you trust; the platform does not " +
	"deliver it."

// Audit actions for credential handover.
const (
	auditOperatorInvited      = "OPERATOR_INVITED"
	auditOperatorResetIssued  = "OPERATOR_RESET_ISSUED"
	auditOperatorResetRedeem  = "OPERATOR_CREDENTIAL_REDEEMED"
	auditOperatorSelfResetReq = "OPERATOR_RESET_REQUESTED"
)

// ConsoleInviteOperator handles POST /console/operators/:operator_id/invite
//
// Issues a fresh invitation for an account that has never signed in.
//
// SEPARATE FROM CREATION so an invitation that did not arrive can be re-sent
// without deleting and recreating the account -- which would lose its site
// grants and its audit history. Issuing supersedes: any outstanding token for
// the account is invalidated first, so re-sending cannot leave two live links
// that two different people could each redeem.
func ConsoleInviteOperator(c *gin.Context) {
	caller := middleware.Operator(c)
	target, _, ok := loadConsoleOperator(c)
	if !ok {
		return
	}

	if !mayManageRole(caller.Role, target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not manage an operator at that role"})
		return
	}

	// An account that has signed in has a password its owner chose. Re-inviting
	// it would be a reset wearing an invitation's name, and the two are audited
	// differently on purpose -- so the caller is told to use the reset route
	// rather than being quietly given one.
	if target.LastLoginAt != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": "That operator has already signed in. Use the password reset " +
				"route instead of an invitation."})
		return
	}

	token, err := database.IssueCredentialToken(target.ID, models.TokenPurposeInvite,
		caller.UserID, caller.Email)
	if err != nil {
		logError(c, "console invite operator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue the invitation"})
		return
	}

	if err := database.SetMustChangePassword(target.ID, true); err != nil {
		logError(c, "console flag invited operator", err)
	}

	recordAudit(c, auditOperatorInvited, auditTargetOperator, target.PublicID,
		target.Email, gin.H{"expires_at": token.ExpiresAt})

	c.JSON(http.StatusCreated, gin.H{
		"invitation": token,
		"delivery":   invitationDeliveryNotice,
	})
}

// ConsoleResetOperatorPassword handles POST /console/operators/:operator_id/reset
//
// An administrative reset: mints a single-use link that lets the operator set
// their own password, WITHOUT the administrator choosing one.
//
// THIS REPLACES THE PATH WHERE AN ADMINISTRATOR TYPED A NEW PASSWORD and read it
// out to somebody. That path still exists on PUT /console/operators/:id for a
// deployment that cannot deliver a link at all, but it is no longer the only
// answer to "they are locked out" -- and it is audited as a password reset
// precisely because the administrator ends up knowing the credential.
func ConsoleResetOperatorPassword(c *gin.Context) {
	caller := middleware.Operator(c)
	target, _, ok := loadConsoleOperator(c)
	if !ok {
		return
	}

	if !mayManageRole(caller.Role, target.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "You may not manage an operator at that role"})
		return
	}

	token, err := database.IssueCredentialToken(target.ID, models.TokenPurposeReset,
		caller.UserID, caller.Email)
	if err != nil {
		logError(c, "console reset operator password", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue the reset"})
		return
	}

	recordAudit(c, auditOperatorResetIssued, auditTargetOperator, target.PublicID,
		target.Email, gin.H{"expires_at": token.ExpiresAt})

	c.JSON(http.StatusCreated, gin.H{
		"reset":    token,
		"delivery": invitationDeliveryNotice,
	})
}

// RequestPasswordReset handles POST /api/v1/auth/forgot-password
//
// Unauthenticated by necessity: somebody who has forgotten their password cannot
// authenticate to ask for a new one.
//
// ALWAYS 202, ALWAYS THE SAME BODY. Unknown address, disabled account, deleted
// account, database hiccup on the lookup -- the caller learns nothing about
// which. A reset endpoint that says "no such account" is an enumeration oracle
// for every address somebody wants to test, and this one is exposed to the
// public internet permanently.
//
// AND THE TOKEN IS NOT IN THE RESPONSE. This is the one place a minted token
// must not be returned to its caller, because the caller is unauthenticated and
// has proved nothing. Until delivery exists, a self-service reset therefore
// completes only in the log -- which is stated plainly in the response rather
// than dressed up as success.
func RequestPasswordReset(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The accepted response is written the same way on every path below, so a
	// future edit cannot make one branch answer differently from another.
	accepted := gin.H{
		"status": "accepted",
		"message": "If that address belongs to an operator account, a password " +
			"reset has been issued. Contact your administrator if you do not " +
			"receive it.",
	}

	userID, err := database.FindOperatorForReset(req.Email)
	if err != nil {
		logError(c, "look up operator for reset", err)
		c.JSON(http.StatusAccepted, accepted)
		return
	}
	if userID == 0 {
		log.Printf("request_id=%s auth=reset-requested match=none ip=%s",
			middleware.RequestID(c), c.ClientIP())
		c.JSON(http.StatusAccepted, accepted)
		return
	}

	token, err := database.IssueCredentialToken(userID, models.TokenPurposeReset, 0, "")
	if err != nil {
		logError(c, "issue self-service reset", err)
		c.JSON(http.StatusAccepted, accepted)
		return
	}

	// The one place a token is written to the operational log, and the reason is
	// that there is nowhere else for it to go yet. An installation with delivery
	// wired removes this line in the same change that adds the send.
	log.Printf("request_id=%s auth=reset-requested match=yes user=%d expires=%s "+
		"DELIVERY NOT CONFIGURED -- reset link: %s",
		middleware.RequestID(c), userID, token.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		token.Token)

	c.JSON(http.StatusAccepted, accepted)
}

// RedeemCredential handles POST /api/v1/auth/redeem
//
// Sets a password from an invitation or a reset link. Unauthenticated, because
// THE TOKEN IS THE AUTHORISATION -- somebody redeeming an invitation has never
// had a password and somebody redeeming a reset has forgotten theirs.
//
// Mounted behind the credential rate limiter: a redemption endpoint that takes a
// bearer secret in the body is a guessing surface, even against 256 bits.
//
// Every session for the account is revoked by the store, including any the
// redeemer holds. Somebody setting a password through a reset link usually
// believes the old one was known to somebody else, and leaving the sessions that
// password opened alive would make the change cosmetic.
func RedeemCredential(c *gin.Context) {
	if !requireJSON(c) {
		return
	}

	var req models.RedeemTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A token is required"})
		return
	}

	user, err := database.RedeemCredentialToken(req.Token, req.NewPassword, c.ClientIP())
	switch {
	case errors.Is(err, models.ErrPasswordTooShort), errors.Is(err, models.ErrPasswordTooLong):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return

	// Not found, expired and already-redeemed are reported DISTINCTLY, unlike a
	// login failure. They are not an enumeration oracle: a caller who holds the
	// token already holds the secret, and "that link has expired" is the
	// difference between somebody requesting a new one and somebody concluding
	// the platform is broken.
	case errors.Is(err, models.ErrTokenNotFound):
		log.Printf("request_id=%s auth=redeem-rejected reason=unknown ip=%s",
			middleware.RequestID(c), c.ClientIP())
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrTokenExpired):
		c.JSON(http.StatusGone, gin.H{"error": err.Error()})
		return
	case errors.Is(err, models.ErrTokenRedeemed):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	case err != nil:
		logError(c, "redeem credential token", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set the password"})
		return
	}

	// Written directly rather than through recordAudit: there is no operator
	// identity on this request to read, and the actor IS the account the change
	// was made to.
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:      user.CompanyID,
		ActorUserID:    user.ID,
		ActorEmail:     user.Email,
		ActorRole:      user.Role,
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         auditOperatorResetRedeem,
		TargetType:     auditTargetOperator,
		TargetPublicID: user.PublicID,
		TargetLabel:    user.Email,
	})

	log.Printf("request_id=%s auth=redeemed operator=%s ip=%s",
		middleware.RequestID(c), user.PublicID, c.ClientIP())

	// 204 rather than a session. Redeeming sets a password; it does not sign
	// anybody in. The console sends them to the login screen, where the new
	// credential is exercised once before anything depends on it.
	c.Status(http.StatusNoContent)
}
