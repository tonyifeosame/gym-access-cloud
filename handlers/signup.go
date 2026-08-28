package handlers

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"

	"github.com/gin-gonic/gin"
)

// Self-service signup, POST /api/v1/auth/register.
//
// THE FIRST ROUTE INTO THE PRODUCT FOR SOMEBODY NOBODY HAS ONBOARDED. Until
// this existed, a new customer needed a platform administrator to create their
// company and mint their first owner, and then to hand over an invitation link
// the platform has no way to deliver. This is one form and one request.
//
// UNAUTHENTICATED BY NECESSITY, like login, forgot-password and redeem: the
// caller has no credential because obtaining one is what they are here for. It
// shares those routes' rate limiter, so an attacker cannot get a fresh
// allowance by moving between signing in and signing up, and mass tenant
// creation from one address is bounded by the same bucket.
//
// WHAT THIS ROUTE CANNOT REACH, AND WHY THAT IS STRUCTURAL RATHER THAN CHECKED:
//
//	Platform administration. It calls database.RegisterCompany, which creates
//	one company and one owner inside it. There is no function in that path that
//	lists tenants, touches an existing one, or creates a platform admin -- the
//	third credential class is a different table with a different cookie and
//	nothing here authenticates against it.
//
//	Site provisioning keys. The company's first site is created in the same
//	transaction and its credential is minted and discarded; only the hash is
//	stored. SignupResult has no field that could carry one, so the response
//	below cannot leak what it never receives.
//
// IT ENDS IN AN ORDINARY SESSION. CreateSession, the same cookie, the same CSRF
// derivation, the same body login returns -- built by the same
// buildSessionResponse. Signup is a way to get an account, not a second way to
// be authenticated, and inventing a parallel session here would be a second
// mechanism to keep in step with the real one for ever.

// auditCompanyRegistered records a tenant that created itself.
//
// A DIFFERENT ACTION FROM COMPANY_CREATED, which is what the platform surface
// writes. A reader of the trail must be able to tell "our vendor set this up"
// from "somebody signed up on the website", and reusing one name for both would
// make that undiscoverable afterwards.
const auditCompanyRegistered = "COMPANY_REGISTERED"

// envPublicSignup switches the route off for a deployment that does not want it.
const envPublicSignup = "PUBLIC_SIGNUP_ENABLED"

// PublicSignupEnabled reports whether self-service signup is offered.
//
// DEFAULT ON, because a console with no way in is the problem this flow exists
// to solve, and a deployment that has to set a variable to be usable is one that
// ships broken.
//
// The switch exists for the deployment where it is wrong: an installation run
// for a single organisation, or one behind a private network, where an
// anonymous endpoint that creates tenants is a surface with no customer behind
// it. Turning it off answers 403 -- and deliberately not 404, because a route
// that pretends not to exist sends whoever hit it looking for a typo rather than
// for the switch.
func PublicSignupEnabled() bool {
	raw := strings.TrimSpace(os.Getenv(envPublicSignup))
	if raw == "" {
		return true
	}
	switch strings.ToLower(raw) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// OperatorRegister handles POST /api/v1/auth/register
//
// 201 with the SAME BODY login returns, and the session cookie already set. The
// customer is signed in by the time this responds -- sending somebody who has
// just chosen a password to a login form to type it again is a step that only
// exists when signup and authentication were built as separate things.
func OperatorRegister(c *gin.Context) {
	if !PublicSignupEnabled() {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Self-service signup is not enabled on this deployment"})
		return
	}

	// The same CSRF defence login uses, and for the same reason: this endpoint
	// has no session yet to carry a token, and an HTML form submitted cross-site
	// cannot send application/json.
	if !requireJSON(c) {
		return
	}

	var req models.SignupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := database.RegisterCompany(database.NewSignup{
		FullName:    req.FullName,
		CompanyName: req.CompanyName,
		Email:       req.Email,
		Password:    req.Password,
	})
	switch {
	case errors.Is(err, database.ErrFullNameRequired),
		errors.Is(err, database.ErrFullNameTooLong),
		errors.Is(err, database.ErrCompanyNameRequired),
		errors.Is(err, database.ErrCompanyNameTooLong),
		errors.Is(err, models.ErrInvalidEmail),
		errors.Is(err, models.ErrPasswordTooShort),
		errors.Is(err, models.ErrPasswordTooLong):
		// The store's own words. Which field was refused and why is exactly what
		// the person filling in the form needs, and restating the rules here
		// would be a second copy to keep in step.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return

	case errors.Is(err, database.ErrEmailInUse):
		// Unavoidably an existence disclosure; see the note on ErrEmailInUse.
		// The alternative is accepting an address that can never be signed in
		// with, because the login form has no tenant selector.
		c.JSON(http.StatusConflict, gin.H{
			"error": "That email address is already in use. Sign in instead, or " +
				"reset your password."})
		return

	case errors.Is(err, database.ErrCompanyNameUnavailable):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return

	case err != nil:
		logError(c, "register company", err)
		c.JSON(http.StatusInternalServerError,
			gin.H{"error": "Could not create your account"})
		return
	}

	// From here the tenant EXISTS and is committed. Every failure below is a
	// failure to sign them in, not a failure to create the account, and none of
	// them may report otherwise -- an owner told "signup failed" who then cannot
	// sign up again because their address is taken is stranded.
	creds, err := database.CreateSession(result.User.ID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		logError(c, "create session for new owner", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Your account was created but could not be signed in. " +
				"Please sign in with the password you chose."})
		return
	}

	// Resolved the same way every subsequent request will resolve it, exactly as
	// login does, so the body returned here is what /me returns next.
	identity, err := database.AuthenticateSession(creds.Token)
	if err != nil || identity == nil {
		logError(c, "resolve new owner session", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Your account was created but could not be signed in. " +
				"Please sign in with the password you chose."})
		return
	}

	grants, applications, ok := loadSessionContext(c, identity,
		"Your account was created but could not be signed in. "+
			"Please sign in with the password you chose.")
	if !ok {
		return
	}

	recordSignupAudit(c, result)

	setSessionCookie(c, creds.Token)
	log.Printf("request_id=%s signup=success operator=%s company=%s slug=%s ip=%s",
		middleware.RequestID(c), identity.UserPublicID, identity.CompanyPublicID,
		identity.CompanySlug, c.ClientIP())

	c.JSON(http.StatusCreated, buildSessionResponse(identity, grants, applications))
}

// recordSignupAudit writes the tenant's first trail entry.
//
// NOT recordAudit: that reads the operator identity out of the gin context, and
// there is none here -- the caller was anonymous when the request began. The
// actor is the account that was just created, which is the truthful answer to
// "who created this company".
//
// The site is named in the change set rather than given a record of its own. It
// was not a decision anybody made; it is part of what registering a company
// means, and the trail should read that way.
func recordSignupAudit(c *gin.Context, result *database.SignupResult) {
	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:   result.User.CompanyID,
		ActorUserID: result.User.ID,
		ActorEmail:  result.User.Email,
		ActorRole:   result.User.Role,
		IPAddress:   c.ClientIP(),
		UserAgent:   c.Request.UserAgent(),
		RequestID:   middleware.RequestID(c),
		Action:      auditCompanyRegistered,
		TargetType:  auditTargetCompany,

		TargetPublicID: result.Company.ID,
		TargetLabel:    result.Company.Name,
		Changes: map[string]any{
			"slug":       result.Company.Slug,
			"site":       result.Site.Name,
			"self_serve": true,
		},
	})
}
