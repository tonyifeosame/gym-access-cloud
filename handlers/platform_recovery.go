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

// Recovery of last resort for a locked-out tenant administrator.
//
// See database/platform_recovery.go for the whole of the reasoning; the short
// version is that self-service signup creates a company of ONE, and a company of
// one had no route back into its own account. forgot-password mints a token with
// nowhere to send it, the console's administrative reset needs a second
// administrator, and the first-operator route is refused into a company that
// already has one.
//
// THIS IS NOT A GENERAL RESET ROUTE AND MUST NOT BECOME ONE. The store refuses
// unless the target is the company's only OWNER-or-ADMIN, which is the only case
// where nobody inside the company can do it instead. A vendor credential able to
// reset any operator at any time would be a standing way into every customer,
// and that is what the predicate exists to prevent.
//
// WHAT IT RETURNS AND TO WHOM. The link is returned ONCE, to an authenticated
// platform administrator, exactly as an invitation is by PlatformCreateFirstOperator
// and as an administrative reset is by ConsoleResetOperatorPassword. It is never
// returned to the customer, never to an unauthenticated caller, and never
// written into any tenant-facing response -- the customer's own forgot-password
// screen still answers 202 and learns nothing.
//
// AND IT IS AUDITED INTO THE CUSTOMER'S OWN TRAIL, so the tenant can see that
// their vendor issued a reset for their owner account without having to ask the
// vendor. A recovery mechanism whose use is only visible to the party using it
// is one nobody can hold to account.

// auditOwnerRecoveryIssued marks a vendor-issued recovery in the tenant's trail.
//
// A DISTINCT ACTION rather than reusing OPERATOR_RESET_ISSUED, which is what an
// administrator inside the company produces. The two are different events with
// different people behind them, and a trail that spelled them the same way would
// make "did our vendor reset our owner account" unanswerable.
const auditOwnerRecoveryIssued = "COMPANY_OWNER_RECOVERY_ISSUED"

// PlatformIssueOwnerRecovery handles
// POST /api/v1/platform/companies/:company_id/recovery
//
// Issues a single-use password-reset link for a company's SOLE administrator.
//
// Refused with 409 when the company has more than one, because that company can
// recover itself from its own console and this surface has no business reaching
// into it. Refused with 409 when it has none, because resetting a manager or a
// viewer would hand back an account that still cannot administer anything.
func PlatformIssueOwnerRecovery(c *gin.Context) {
	publicID := c.Param("company_id")

	target, err := database.SoleAdministratorForRecovery(publicID)
	switch {
	case errors.Is(err, models.ErrCompanyNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Company not found"})
		return
	case errors.Is(err, database.ErrCompanyHasOtherAdmins):
		c.JSON(http.StatusConflict, gin.H{
			"error": "That company has more than one administrator, so one of them " +
				"can issue the reset from their own console. This route is only for " +
				"a company whose single administrator is locked out."})
		return
	case errors.Is(err, database.ErrCompanyHasNoAdmin):
		c.JSON(http.StatusConflict, gin.H{
			"error": "That company has no active owner or administrator to recover."})
		return
	case err != nil:
		logError(c, "platform resolve sole administrator", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue the reset"})
		return
	}

	// The same call the console's administrative reset makes. There is one token
	// mechanism on this platform and this is not a second one: single-use,
	// short-lived, stored as a hash, and superseding any outstanding token for
	// the account.
	//
	// issuedBy is 0 and issuedByEmail empty for the same reason the audit actor
	// is denormalised: a platform administrator is not a row in `users`, and
	// user_credential_tokens.issued_by_user_id references that table.
	token, err := database.IssueCredentialToken(target.ID, models.TokenPurposeReset, 0, "")
	if err != nil {
		logError(c, "platform issue owner recovery", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue the reset"})
		return
	}

	recordPlatformAudit(c, target.CompanyID, auditOwnerRecoveryIssued,
		target.PublicID, target.Email, gin.H{
			"role":       target.Role,
			"expires_at": token.ExpiresAt,
			"reason":     "sole administrator locked out",
		})

	// The account is NOT flagged must_change_password. Redeeming the link IS the
	// password change -- the operator chooses it themselves and nobody else ever
	// holds it -- so forcing another one at the next sign-in would ask somebody
	// to change a password they set thirty seconds earlier.
	log.Printf("request_id=%s platform=owner-recovery-issued company=%s operator=%s expires=%s",
		middleware.RequestID(c), publicID, target.PublicID,
		token.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))

	c.JSON(http.StatusCreated, gin.H{
		"operator": operatorSummary{
			ID:       target.PublicID,
			Email:    target.Email,
			FullName: target.FullName,
			Role:     target.Role,
		},
		"reset":    token,
		"delivery": invitationDeliveryNotice,
	})
}
