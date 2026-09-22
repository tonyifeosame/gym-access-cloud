package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// The OAuth 2.0 authorization server (migrations/037).
//
// ---------------------------------------------------------------------------
// WHAT THIS IS FOR
// ---------------------------------------------------------------------------
//
// A customer connects a third-party product -- Datavase is the first -- from
// THAT product's own screen. They are sent here, sign in to AccessLink if they
// are not already, read exactly what is being asked for, and allow it. What the
// product ends up holding is a short-lived access token and a rotating refresh
// token, both revocable, both narrowed to what the consenting operator was
// themselves entitled to grant.
//
// NO CONSOLE NAVIGATION IS INVOLVED. The pages here are served by the API, not
// by the console single-page app, so the flow does not depend on the dashboard
// being reachable, on a particular route existing in it, or on the customer
// knowing where anything is. They see an AccessLink sign-in page and an
// AccessLink consent page and they are sent straight back.
//
// ---------------------------------------------------------------------------
// WHAT THESE HANDLERS DELIBERATELY DO NOT REUSE
// ---------------------------------------------------------------------------
//
// NOT THE CONSOLE'S JSON AUTH ROUTES. POST /api/v1/auth/login answers JSON and
// requires a JSON content type as its own CSRF defence; a browser form cannot
// send one. So sign-in here is a form post with its own defence (see
// oauthFormGuard) and reuses the STORE -- database.AuthenticatePassword and
// database.CreateSession -- rather than the HTTP route. One password policy, one
// lockout, one session table, two entry points.
//
// NOT THE CONSOLE'S ERROR SHAPE, AND NOT THE PUBLIC API'S EITHER. RFC 6749
// section 5.2 defines the token endpoint's body, and a client library written
// against the RFC parses `error` as a string at the top level. The resource
// routes keep the AccessLink envelope; everything under /oauth/ speaks the
// protocol. The boundary is exactly the path prefix and is stated in the
// reference.
//
// ---------------------------------------------------------------------------
// WHAT NEVER APPEARS IN A LOG, AN AUDIT ROW OR AN ERROR BODY
// ---------------------------------------------------------------------------
//
// The authorization code, the access token, the refresh token, the PKCE
// verifier and the client secret. What is logged and audited instead: the
// client id, the company, the consenting operator, the granted scopes, the
// access token's public id and the refresh family id. None of those can be
// presented as a credential, and together they answer every question an
// incident review asks.

// oauthNonceCookie carries the consent form's double-submit token.
//
// __Host- so a sibling subdomain cannot set it, SameSite=Lax so a cross-site
// POST does not carry it, and short-lived.
//
// WHAT ACTUALLY STOPS A CROSS-SITE CONSENT, and it is worth being exact because
// the obvious answer is the wrong one. It is NOT that the page cannot be read
// cross-origin: with no CORS_ALLOWED_ORIGINS configured the global middleware
// answers Access-Control-Allow-Origin: * , so a script elsewhere can fetch this
// page and read it.
//
// It is that SameSite=Lax is on BOTH cookies. A cross-site POST carries neither
// the session nor this nonce, so the submission is unauthenticated and the
// guard fails. And a cross-origin read is necessarily a NO-CREDENTIALS read --
// `*` cannot be combined with Allow-Credentials, and the middleware only sends
// credentials to an allow-listed origin -- so what such a script receives is
// the signed-out sign-in form, bound to a nonce the victim's browser never
// stored. There is no ordering of those that produces a consent.
//
// The double-submit is therefore the second lock rather than the first, and it
// is here because a page carrying a decision button should not rely on one.
const oauthNonceCookie = "__Host-al_oauth"

// oauthDevNonceCookie is the name used ONLY when SESSION_COOKIE_INSECURE is
// set, for local development that cannot offer TLS. The name has to change with
// the flag for the reason middleware.SessionCookieConfig already records: a
// __Host- cookie without Secure is refused by the browser outright.
const oauthDevNonceCookie = "al_oauth"

func oauthNonceCookieConfig() (name string, secure bool) {
	if os.Getenv("SESSION_COOKIE_INSECURE") != "" {
		return oauthDevNonceCookie, false
	}
	return oauthNonceCookie, true
}

// oauthPageCSP is the policy every page here is served under.
//
// Nothing external, no script at all, and no framing. These pages carry a
// synchronizer token and, on the sign-in form, a password field; a policy that
// permitted script would make an injected string into a credential theft, and
// one that permitted framing would make the consent button clickjackable.
const oauthPageCSP = "default-src 'none'; style-src 'unsafe-inline'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// ---------------------------------------------------------------------------
// The authorization request
// ---------------------------------------------------------------------------

// oauthRequest is one authorization request, as parsed from the query string or
// from the consent form's hidden fields.
//
// THE FORM RESTATES THE REQUEST AND THE SERVER RE-VALIDATES ALL OF IT. Nothing
// is remembered between the GET and the POST except the operator's session and
// the form nonce, so there is no server-side state to expire, to leak between
// instances, or to be confused between two tabs -- and nothing the form says is
// trusted, because every field is checked against the configured client again.
type oauthRequest struct {
	ResponseType  string
	ClientID      string
	RedirectURI   string
	Scope         string
	State         string
	Challenge     string
	ChallengeMode string
}

func readOAuthRequest(c *gin.Context) oauthRequest {
	get := c.Query
	if c.Request.Method == http.MethodPost {
		get = c.PostForm
	}
	return oauthRequest{
		ResponseType:  strings.TrimSpace(get("response_type")),
		ClientID:      strings.TrimSpace(get("client_id")),
		RedirectURI:   strings.TrimSpace(get("redirect_uri")),
		Scope:         get("scope"),
		State:         get("state"),
		Challenge:     strings.TrimSpace(get("code_challenge")),
		ChallengeMode: strings.TrimSpace(get("code_challenge_method")),
	}
}

// OAuthAuthorize handles GET /api/public/v1/oauth/authorize.
//
// ---------------------------------------------------------------------------
// THE ORDER OF THE CHECKS IS THE SECURITY PROPERTY
// ---------------------------------------------------------------------------
//
// RFC 6749 section 4.1.2.1 draws a line, and it is the most important rule in
// this file: until the client AND the redirect URI have been verified against
// configuration, NOTHING MAY BE REDIRECTED ANYWHERE. A server that reported
// "unknown client" by redirecting to the URI the request supplied would be an
// open redirector that anybody could aim at any host.
//
// So: unknown client, or a redirect URI that is not on that client's
// allow-list, renders an error PAGE at this URL. Everything after that point is
// reported by redirecting to the verified URI with `error` and the client's own
// `state`, which is what lets the client show the customer something useful.
func OAuthAuthorize(c *gin.Context) {
	req := readOAuthRequest(c)

	client, target, ok := resolveOAuthTarget(c, req)
	if !ok {
		return
	}

	scopes, ok := requestedScopes(c, req, client, target)
	if !ok {
		return
	}

	identity := oauthSessionOperator(c)
	if identity == nil {
		renderSignIn(c, req, client, "")
		return
	}

	renderConsentOrRefusal(c, req, client, identity, scopes)
}

// OAuthAuthorizeSubmit handles POST /api/public/v1/oauth/authorize.
//
// Two actions arrive here and they are told apart by the `action` field:
//
//	signin   the customer filled in the sign-in form. Authenticate, open a
//	         session, and render the CONSENT screen -- never the grant itself.
//	         Signing in is not consenting, and a flow that skipped the consent
//	         screen because the password was typed on the previous page would be
//	         asking somebody to authorise something they were never shown.
//
//	allow /  the customer decided. `allow` mints a code and redirects; anything
//	deny     else is a refusal and redirects with access_denied.
//
// The whole authorization request is re-read from the form and re-validated
// from configuration before either action is considered.
func OAuthAuthorizeSubmit(c *gin.Context) {
	req := readOAuthRequest(c)

	client, target, ok := resolveOAuthTarget(c, req)
	if !ok {
		return
	}
	scopes, ok := requestedScopes(c, req, client, target)
	if !ok {
		return
	}

	// CSRF. Checked before anything is authenticated or decided, and its
	// failure is a re-render rather than an error: a stale tab is the common
	// cause, and telling somebody to start again is the useful answer.
	if !oauthFormGuard(c) {
		identity := oauthSessionOperator(c)
		if identity == nil {
			renderSignIn(c, req, client,
				"This page expired. Sign in again to continue.")
			return
		}
		renderConsentOrRefusal(c, req, client, identity, scopes)
		return
	}

	if c.PostForm("action") == "signin" {
		identity, message := oauthSignIn(c)
		if identity == nil {
			renderSignIn(c, req, client, message)
			return
		}
		renderConsentOrRefusal(c, req, client, identity, scopes)
		return
	}

	identity := oauthSessionOperator(c)
	if identity == nil {
		renderSignIn(c, req, client, "Sign in to continue.")
		return
	}

	granted, err := models.ValidateOAuthScopeRequest(scopes, client, identity.Role,
		middleware.RoleAtLeast)
	if err != nil {
		// Reachable when an operator's role changed between the page being
		// rendered and the form being submitted. Refused rather than narrowed.
		redirectOAuthError(c, target, req.State, models.OAuthErrInvalidScope,
			"This account cannot grant the requested permissions.")
		return
	}

	if c.PostForm("action") != "allow" {
		recordOAuthAudit(c, oauthAuditEntry{
			CompanyID: identity.CompanyID,
			UserID:    identity.UserID,
			Email:     identity.Email,
			Role:      identity.Role,
			Action:    auditOAuthConsentDenied,
			Label:     client.ClientID,
			Changes:   gin.H{"client_id": client.ClientID, "scopes": granted},
		})
		// The form token is spent on a decision, whichever way it went, so the
		// page cannot be submitted twice from a back button.
		clearOAuthNonce(c)
		redirectOAuthError(c, target, req.State, models.OAuthErrAccessDenied,
			"The account holder declined the request.")
		return
	}

	code, err := database.IssueOAuthCode(database.OAuthCodeIssue{
		ClientID:            client.ID,
		CompanyID:           identity.CompanyID,
		UserID:              identity.UserID,
		RedirectURI:         req.RedirectURI,
		Scopes:              granted,
		CodeChallenge:       req.Challenge,
		CodeChallengeMethod: "S256",
	})
	if err != nil {
		logError(c, "issue authorization code", err)
		redirectOAuthError(c, target, req.State, models.OAuthErrServerError,
			"The authorization code could not be issued. Please try again.")
		return
	}

	// THE CODE IS NOT IN THIS RECORD. What is recorded is that this operator
	// granted these scopes to this client, which is the auditable fact; the
	// code is a secret with a two-minute life and no business in a trail
	// anybody can read.
	recordOAuthAudit(c, oauthAuditEntry{
		CompanyID: identity.CompanyID,
		UserID:    identity.UserID,
		Email:     identity.Email,
		Role:      identity.Role,
		Action:    auditOAuthConsentGranted,
		Label:     client.ClientID,
		Changes: gin.H{
			"client_id":    client.ClientID,
			"client_name":  client.Name,
			"scopes":       granted,
			"redirect_uri": req.RedirectURI,
		},
	})

	clearOAuthNonce(c)
	redirectOAuthSuccess(c, target, req.State, code)
}

// resolveOAuthTarget verifies the client and the redirect URI.
//
// Reports false having already rendered the error page. See the note on
// OAuthAuthorize for why a failure here must not redirect.
func resolveOAuthTarget(c *gin.Context, req oauthRequest) (*models.OAuthClient, *url.URL, bool) {
	if req.ClientID == "" {
		renderOAuthProblem(c, http.StatusBadRequest, "That link is not complete",
			"The application did not say which integration is asking. Start again from the application you were using.")
		return nil, nil, false
	}

	client, err := database.OAuthClientByID(req.ClientID)
	if err != nil {
		logError(c, "read oauth client", err)
		renderOAuthProblem(c, http.StatusServiceUnavailable, "Something went wrong",
			"We could not check that application just now. Please try again in a moment.")
		return nil, nil, false
	}
	if client == nil {
		// Unknown and deactivated are one answer. A page that distinguished
		// them would enumerate which integrations a deployment has configured.
		renderOAuthProblem(c, http.StatusBadRequest, "That application is not connected here",
			"AccessLink does not recognise the application that sent you. Start again from the application you were using.")
		return nil, nil, false
	}

	if req.RedirectURI == "" || !client.AllowsRedirectURI(req.RedirectURI) {
		renderOAuthProblem(c, http.StatusBadRequest, "That link is not one we can return you to",
			"For your safety AccessLink only returns you to addresses the application has registered with us. Start again from the application you were using.")
		return nil, nil, false
	}

	target, err := url.Parse(req.RedirectURI)
	if err != nil {
		// Unreachable: the stored value was validated when the client was
		// configured. Refused rather than assumed.
		renderOAuthProblem(c, http.StatusBadRequest, "That link is not one we can return you to",
			"Start again from the application you were using.")
		return nil, nil, false
	}
	return client, target, true
}

// requestedScopes validates the protocol parameters that CAN be reported by
// redirect, and resolves the scope request against the client.
//
// The operator's own bound is NOT applied here: it needs a role, and there may
// be no session yet. It is applied at consent, where there is.
func requestedScopes(c *gin.Context, req oauthRequest, client *models.OAuthClient,
	target *url.URL) ([]string, bool) {

	if req.ResponseType != "code" {
		redirectOAuthError(c, target, req.State, models.OAuthErrUnsupportedResponseType,
			"Only the authorization code flow is supported.")
		return nil, false
	}
	if req.ChallengeMode != "S256" {
		redirectOAuthError(c, target, req.State, models.OAuthErrInvalidRequest,
			"code_challenge_method must be S256.")
		return nil, false
	}
	if !models.ValidCodeChallenge(req.Challenge) {
		redirectOAuthError(c, target, req.State, models.OAuthErrInvalidRequest,
			"A valid S256 code_challenge is required.")
		return nil, false
	}
	if len(req.State) > models.MaxOAuthStateLength {
		redirectOAuthError(c, target, "", models.OAuthErrInvalidRequest,
			"state is longer than this server will echo.")
		return nil, false
	}
	if models.ScopeRequestTooLong(req.Scope) {
		// REFUSED RATHER THAN TRUNCATED. Reading the first n characters would
		// grant a prefix of what was asked for and say nothing about it, and a
		// client that believes it holds a scope it does not is worse off than
		// one that got an error.
		redirectOAuthError(c, target, req.State, models.OAuthErrInvalidRequest,
			"scope is longer than this server will read.")
		return nil, false
	}

	scopes := models.ParseScopeRequest(req.Scope)
	for _, name := range scopes {
		if !models.KnownScope(name) || !client.PermitsScope(name) {
			redirectOAuthError(c, target, req.State, models.OAuthErrInvalidScope,
				"One of the requested scopes is not available to this application.")
			return nil, false
		}
	}
	return scopes, true
}

// oauthSessionOperator resolves the AccessLink account behind the browser, or
// nil.
//
// THE SAME SESSION THE CONSOLE USES, resolved with the same function, so an
// operator already signed in is not asked again and a revoked session is not
// honoured here either. Nothing about the session's behaviour changes: this
// reads it, and this file never writes one except through database.CreateSession
// on the sign-in path.
//
// A STORE FAILURE IS TREATED AS "NOT SIGNED IN", which renders the sign-in
// form; the attempt behind it will meet the same outage and report it honestly.
// The alternative -- a 500 page -- would leave a customer mid-connect with
// nothing to do.
func oauthSessionOperator(c *gin.Context) *models.OperatorIdentity {
	name, _ := middleware.SessionCookieConfig()
	token, err := c.Cookie(name)
	if err != nil || token == "" {
		return nil
	}
	identity, err := database.AuthenticateSession(token)
	if err != nil {
		logError(c, "authenticate session for oauth consent", err)
		return nil
	}
	return identity
}

// oauthSignIn authenticates the sign-in form and opens a session.
//
// Returns nil and a message for the page on every failure. EVERY CREDENTIAL
// REJECTION IS THE SAME MESSAGE -- unknown address, wrong password, disabled
// account, disabled company -- for the reason OperatorLogin already records:
// the caller cannot act differently on any of them, and telling them apart is
// how a form becomes an account-enumeration oracle. The store also spends one
// bcrypt comparison on an unknown address, so the timing does not answer what
// the message refuses to.
func oauthSignIn(c *gin.Context) (*models.OperatorIdentity, string) {
	email := c.PostForm("email")
	password := c.PostForm("password")
	if strings.TrimSpace(email) == "" || password == "" {
		return nil, "Enter your email address and password."
	}

	// THE SAME ALLOWANCE THE CONSOLE'S LOGIN DRAWS ON, spent before the
	// password is looked at. The /oauth tree has a limiter of its own, but a
	// password form must not be a second budget: an attacker who could
	// alternate between here and POST /api/v1/auth/login would get twice the
	// attempts from one address. See middleware.SpendLoginAttempt.
	if !middleware.SpendLoginAttempt(c) {
		return nil, "Too many sign-in attempts. Please wait a minute and try again."
	}

	user, err := database.AuthenticatePassword(email, password)

	var locked *models.AccountLockedError
	switch {
	case errors.As(err, &locked):
		return nil, "Too many failed attempts. This account is temporarily locked; try again shortly."
	case errors.Is(err, models.ErrInvalidCredentials):
		// The SAME log line the console's login writes, with the same
		// redaction, plus a source. A failed sign-in nobody can correlate to
		// an account is of little use during an incident, and redactEmail is
		// what decides how much of one may be written down.
		log.Printf("request_id=%s auth=failed source=oauth_consent email=%s ip=%s",
			middleware.RequestID(c), redactEmail(database.NormalizeEmail(email)), c.ClientIP())
		return nil, "That email address and password do not match."
	case err != nil:
		logError(c, "authenticate operator for oauth consent", err)
		return nil, "We could not sign you in just now. Please try again in a moment."
	}

	creds, err := database.CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		logError(c, "create session for oauth consent", err)
		return nil, "We could not sign you in just now. Please try again in a moment."
	}
	identity, err := database.AuthenticateSession(creds.Token)
	if err != nil || identity == nil {
		logError(c, "resolve new session for oauth consent", err)
		return nil, "We could not sign you in just now. Please try again in a moment."
	}

	// EXACTLY THE COOKIE THE CONSOLE SETS, through the same helper, so a
	// customer who signs in here is signed in to the console too and one that
	// signs out there is signed out here. Two session cookies with different
	// attributes for one session table is how a sign-out stops working.
	setSessionCookie(c, creds.Token)
	return identity, ""
}

// ---------------------------------------------------------------------------
// The consent form's CSRF defence
// ---------------------------------------------------------------------------

// issueOAuthNonce mints the double-submit token and sets its cookie.
//
// The value is <32 hex>.<unix seconds>. The timestamp is not signed and does
// not need to be: an attacker cannot set this cookie for our host at all (the
// __Host- prefix makes it host-only and un-settable by a sibling subdomain), so
// the only party who can produce a matching pair is a page we served.
func issueOAuthNonce(c *gin.Context) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failing is not a condition to serve a consent page
		// through: without a nonce the form has no CSRF defence at all.
		panic("oauth: generating a form nonce: " + err.Error())
	}
	nonce := hex.EncodeToString(raw) + "." + strconv.FormatInt(time.Now().Unix(), 10)

	name, secure := oauthNonceCookieConfig()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, nonce, int(models.OAuthConsentFormLifetime.Seconds()), "/", "", secure, true)
	return nonce
}

func clearOAuthNonce(c *gin.Context) {
	name, secure := oauthNonceCookieConfig()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", "", secure, true)
}

// oauthFormGuard reports whether a submission carries a form token matching the
// cookie we set, within its lifetime.
//
// CONSTANT-TIME COMPARISON, for the reason database.CSRFMatches already
// records: a byte-by-byte compare with an early exit leaks the expected value
// one character at a time to a caller willing to measure.
func oauthFormGuard(c *gin.Context) bool {
	name, _ := oauthNonceCookieConfig()
	cookie, err := c.Cookie(name)
	if err != nil || cookie == "" {
		return false
	}
	submitted := c.PostForm("form_token")
	if submitted == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(cookie), []byte(submitted)) != 1 {
		return false
	}

	_, stamp, found := strings.Cut(cookie, ".")
	if !found {
		return false
	}
	issued, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(issued, 0))
	return age >= 0 && age <= models.OAuthConsentFormLifetime
}

// ---------------------------------------------------------------------------
// Redirects back to the client
// ---------------------------------------------------------------------------

// redirectOAuthSuccess returns the customer to the client with a code.
func redirectOAuthSuccess(c *gin.Context, target *url.URL, state, code string) {
	redirectTo(c, target, map[string]string{"code": code, "state": state})
}

// redirectOAuthError returns the customer to the client with a protocol error.
//
// ONLY EVER CALLED WITH A TARGET THAT HAS ALREADY BEEN VERIFIED against the
// client's allow-list. resolveOAuthTarget is the only thing that produces one.
func redirectOAuthError(c *gin.Context, target *url.URL, state, code, description string) {
	redirectTo(c, target, map[string]string{
		"error":             code,
		"error_description": description,
		"state":             state,
	})
}

func redirectTo(c *gin.Context, target *url.URL, params map[string]string) {
	// A copy, so a handler cannot accumulate parameters onto the parsed
	// allow-list entry across calls.
	out := *target
	query := out.Query()
	for k, v := range params {
		if v == "" {
			continue
		}
		query.Set(k, v)
	}
	out.RawQuery = query.Encode()

	// 303, not 302: the POST path redirects too, and a 302 after a form post
	// leaves some clients repeating the POST at the new location -- which here
	// would be the customer's browser POSTing a consent form at the client's
	// callback.
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusSeeOther, out.String())
}

// ---------------------------------------------------------------------------
// The token endpoint
// ---------------------------------------------------------------------------

// OAuthToken handles POST /api/public/v1/oauth/token.
//
// FORM-ENCODED IN, JSON OUT, which is RFC 6749 sections 4.1.3 and 5.1 and is
// what every client library expects. The endpoint is NOT behind the public
// API's bearer middleware: the caller is a client proving itself with a code or
// a refresh token, and requiring an access token to obtain an access token
// would be a loop.
func OAuthToken(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")

	client, ok := authenticateOAuthClient(c)
	if !ok {
		return
	}

	switch c.PostForm("grant_type") {
	case "authorization_code":
		oauthExchangeCode(c, client)
	case "refresh_token":
		oauthRefresh(c, client)
	case "":
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidRequest,
			"grant_type is required.")
	default:
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrUnsupportedGrantType,
			"Supported grant types are authorization_code and refresh_token.")
	}
}

func oauthExchangeCode(c *gin.Context, client *models.OAuthClient) {
	grant, err := database.ExchangeOAuthCode(database.OAuthExchange{
		ClientRowID: client.ID,
		Code:        c.PostForm("code"),
		RedirectURI: strings.TrimSpace(c.PostForm("redirect_uri")),
		Verifier:    c.PostForm("code_verifier"),
		Environment: APIEnvironment(),
	})
	switch {
	case errors.Is(err, models.ErrOAuthCodeConsumed):
		// A REPLAY, and the store has already ended whatever the first
		// exchange produced. Recorded, because this is the signal that a code
		// leaked -- and recorded without the code.
		log.Printf("request_id=%s oauth=code_replayed client=%s ip=%s",
			middleware.RequestID(c), client.ClientID, c.ClientIP())
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidGrant,
			"That authorization code is not valid.")
		return
	case err != nil && (errors.Is(err, models.ErrOAuthCodeUnknown) ||
		errors.Is(err, models.ErrOAuthCodeExpired)):
		// ONE ANSWER FOR UNKNOWN, EXPIRED, WRONG CLIENT, WRONG REDIRECT AND
		// WRONG VERIFIER. Telling them apart would be an oracle for guessing
		// codes, and a client can do nothing differently for any of them
		// beyond starting the flow again.
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidGrant,
			"That authorization code is not valid.")
		return
	case err != nil:
		logError(c, "exchange authorization code", err)
		writeOAuthError(c, http.StatusServiceUnavailable, models.OAuthErrTemporarilyUnavailable,
			"The token could not be issued. Please retry.")
		return
	}

	auditOAuthGrant(c, client, grant, auditOAuthTokenIssued)
	writeOAuthGrant(c, grant)
}

func oauthRefresh(c *gin.Context, client *models.OAuthClient) {
	grant, err := database.RefreshOAuthGrant(database.OAuthRefresh{
		ClientRowID:  client.ID,
		RefreshToken: c.PostForm("refresh_token"),
		Environment:  APIEnvironment(),
		Scopes:       models.ParseScopeRequest(c.PostForm("scope")),
	})
	switch {
	case errors.Is(err, models.ErrOAuthGrantReused):
		// REUSE. The family is already revoked by the store. This is the one
		// OAuth failure that is worth an alert: it means two parties held
		// tokens from one chain.
		log.Printf("request_id=%s oauth=refresh_reuse client=%s ip=%s",
			middleware.RequestID(c), client.ClientID, c.ClientIP())
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidGrant,
			"That refresh token is not valid. The grant has been revoked; the account holder must reconnect.")
		return
	case errors.Is(err, models.ErrOAuthGrantUnknown):
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidGrant,
			"That refresh token is not valid.")
		return
	case errors.Is(err, models.ErrOAuthScopeUngrantable):
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidScope,
			"The requested scope is not part of this grant.")
		return
	case err != nil:
		logError(c, "refresh oauth grant", err)
		writeOAuthError(c, http.StatusServiceUnavailable, models.OAuthErrTemporarilyUnavailable,
			"The token could not be refreshed. Please retry.")
		return
	}

	auditOAuthGrant(c, client, grant, auditOAuthTokenRefreshed)
	writeOAuthGrant(c, grant)
}

// writeOAuthGrant serialises RFC 6749 section 5.1.
func writeOAuthGrant(c *gin.Context, grant *database.OAuthGrant) {
	c.JSON(http.StatusOK, models.OAuthTokenResponse{
		AccessToken:  grant.AccessToken,
		TokenType:    models.OAuthTokenTypeBearer,
		ExpiresIn:    grant.ExpiresIn,
		RefreshToken: grant.RefreshToken,
		Scope:        models.FormatScopeGrant(grant.Scopes),
	})
}

// OAuthRevoke handles POST /api/public/v1/oauth/revoke (RFC 7009).
//
// ALWAYS 200 WHEN THE CLIENT IS AUTHENTICATED, whether or not the token existed
// (RFC 7009 section 2.2). A client cleaning up must not have to distinguish
// "already gone" from "never yours", and an endpoint that did would tell
// whoever asked whether a guessed token is real.
func OAuthRevoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

	client, ok := authenticateOAuthClient(c)
	if !ok {
		return
	}

	presented := c.PostForm("token")
	if presented == "" {
		writeOAuthError(c, http.StatusBadRequest, models.OAuthErrInvalidRequest,
			"token is required.")
		return
	}

	revoked, err := database.RevokeOAuthToken(client.ID, presented)
	if err != nil {
		logError(c, "revoke oauth token", err)
		writeOAuthError(c, http.StatusServiceUnavailable, models.OAuthErrTemporarilyUnavailable,
			"The token could not be revoked. Please retry.")
		return
	}

	if revoked.Found {
		recordOAuthAudit(c, oauthAuditEntry{
			CompanyID: revoked.CompanyID,
			UserID:    revoked.UserID,
			Email:     client.ClientID,
			Role:      actorRoleOAuthClient,
			Action:    auditOAuthGrantRevoked,
			Label:     client.ClientID,
			Changes: gin.H{
				"client_id": client.ClientID,
				"family_id": revoked.FamilyID,
				"reason":    models.OAuthRevokedByClient,
			},
		})
	}

	c.Status(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Client authentication
// ---------------------------------------------------------------------------

// authenticateOAuthClient identifies the client behind a token or revocation
// request.
//
// TWO METHODS, BOTH FROM RFC 6749 SECTION 2.3.1: client_secret_basic (the
// Authorization header) and client_secret_post (form fields). Basic is
// preferred by the RFC and is checked first; post exists because a number of
// widely used client libraries only implement it.
//
// A PUBLIC CLIENT SENDS NO SECRET AND IS ACCEPTED WITHOUT ONE, because PKCE is
// mandatory here and is what binds the code to the client instance. A public
// client that DOES send a secret is refused rather than ignored: it means the
// client is misconfigured, and quietly accepting it would hide that until the
// day somebody assumed the secret was doing something.
//
// A 401 CARRIES WWW-Authenticate, per RFC 6749 section 5.2.
func authenticateOAuthClient(c *gin.Context) (*models.OAuthClient, bool) {
	clientID, secret, hasBasic := basicClientCredentials(c)
	if !hasBasic {
		clientID = strings.TrimSpace(c.PostForm("client_id"))
		secret = c.PostForm("client_secret")
	}

	if clientID == "" {
		writeOAuthClientError(c, "A client_id is required.")
		return nil, false
	}

	client, err := database.OAuthClientByID(clientID)
	if err != nil {
		logError(c, "read oauth client", err)
		writeOAuthError(c, http.StatusServiceUnavailable, models.OAuthErrTemporarilyUnavailable,
			"Client configuration could not be read. Please retry.")
		return nil, false
	}
	if client == nil {
		writeOAuthClientError(c, "That client is not recognised.")
		return nil, false
	}

	if !client.Confidential {
		if secret != "" {
			writeOAuthClientError(c, "This client is registered without a secret; do not send one.")
			return nil, false
		}
		return client, true
	}

	ok, err := database.AuthenticateOAuthClientSecret(client.ID, secret)
	if err != nil {
		logError(c, "authenticate oauth client", err)
		writeOAuthError(c, http.StatusServiceUnavailable, models.OAuthErrTemporarilyUnavailable,
			"Client authentication could not be checked. Please retry.")
		return nil, false
	}
	if !ok {
		writeOAuthClientError(c, "Client authentication failed.")
		return nil, false
	}
	return client, true
}

// basicClientCredentials reads RFC 6749 section 2.3.1 Basic credentials.
//
// The values are form-urlencoded inside the header, which the RFC requires and
// which a surprising number of servers forget -- a client secret containing a
// `+` or a `/` decodes to the wrong string without it.
func basicClientCredentials(c *gin.Context) (id, secret string, ok bool) {
	header := c.GetHeader("Authorization")
	scheme, encoded, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Basic") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", false
	}
	rawID, rawSecret, found := strings.Cut(string(decoded), ":")
	if !found {
		return "", "", false
	}
	id, err = url.QueryUnescape(rawID)
	if err != nil {
		return "", "", false
	}
	secret, err = url.QueryUnescape(rawSecret)
	if err != nil {
		return "", "", false
	}
	return id, secret, true
}

func writeOAuthClientError(c *gin.Context, description string) {
	c.Header("WWW-Authenticate", `Basic realm="accesslink"`)
	writeOAuthError(c, http.StatusUnauthorized, models.OAuthErrInvalidClient, description)
}

// writeOAuthError answers in the protocol's shape.
//
// The description is written for a developer reading a log and never carries a
// token, a code, an internal error string, or a hint about whether an account
// or a resource exists.
func writeOAuthError(c *gin.Context, status int, code, description string) {
	c.Header("Cache-Control", "no-store")
	if status >= 500 {
		c.Header("Retry-After", "5")
	}
	c.AbortWithStatusJSON(status, models.OAuthErrorBody{
		Error:       code,
		Description: description,
	})
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// actorRoleOAuthClient marks a machine acting under a grant, beside
// actorRoleIntegration for an API key. The Activity page renders an unknown
// role as its raw label, so nothing there needs to change.
const actorRoleOAuthClient = "OAUTH_CLIENT"

type oauthAuditEntry struct {
	CompanyID int64
	UserID    int64
	Email     string
	Role      string
	Action    string
	Label     string
	Changes   gin.H
}

// recordOAuthAudit writes one authorization-server action.
//
// NOT recordAudit, because that one reads the operator and the company from a
// gin context populated by OperatorAuthMiddleware, and these routes are not
// behind it -- a consent is granted by a session this handler resolved itself.
// Everything else is the same entry the console writes.
func recordOAuthAudit(c *gin.Context, in oauthAuditEntry) {
	if in.CompanyID == 0 {
		// An audit row with no company cannot be stored (the column is NOT
		// NULL) and cannot be read by anybody. Dropped rather than guessed.
		return
	}
	changes := in.Changes
	if changes == nil {
		changes = gin.H{}
	}
	changes["via"] = "oauth"

	database.WriteAuditEvent(database.AuditEntry{
		CompanyID:      in.CompanyID,
		ActorUserID:    in.UserID,
		ActorEmail:     in.Email,
		ActorRole:      in.Role,
		IPAddress:      c.ClientIP(),
		UserAgent:      c.Request.UserAgent(),
		RequestID:      middleware.RequestID(c),
		Action:         in.Action,
		TargetType:     auditTargetOAuthGrant,
		TargetPublicID: "",
		TargetLabel:    in.Label,
		Changes:        map[string]any(changes),
	})
}

// auditOAuthGrant records an issuance or a refresh.
//
// NEITHER TOKEN IS IN THE RECORD. The access token's PUBLIC ID and the refresh
// FAMILY id are, because those identify the grant for an incident review and
// neither can be presented as a credential -- the same discipline the
// integration-credential trail keeps by recording a key prefix.
func auditOAuthGrant(c *gin.Context, client *models.OAuthClient,
	grant *database.OAuthGrant, action string) {

	recordOAuthAudit(c, oauthAuditEntry{
		CompanyID: grant.CompanyID,
		// THE ACTOR IS THE CLIENT, not the operator: there is no human on this
		// request. The operator who authorised it is named in `changes`, which
		// is where an administrator reviewing the trail looks for "who let this
		// in", without the record claiming they were present.
		UserID: grant.UserID,
		Email:  client.ClientID,
		Role:   actorRoleOAuthClient,
		Action: action,
		Label:  client.ClientID,
		Changes: gin.H{
			"client_id":       client.ClientID,
			"scopes":          grant.Scopes,
			"access_token_id": grant.AccessTokenID,
			"family_id":       grant.FamilyID,
		},
	})
}

// ---------------------------------------------------------------------------
// The pages
// ---------------------------------------------------------------------------

// consentPageData is what the templates render. Every field is escaped by
// html/template; nothing here is written into a script context, because there
// is no script.
type consentPageData struct {
	Title       string
	Heading     string
	Message     string
	ClientName  string
	CompanyName string
	Email       string
	Scopes      []models.OAuthConsentScope
	Request     oauthRequest
	FormToken   string
	// Refusal is set when the signed-in account may not grant what was asked.
	// The page then offers only a way back to the application.
	Refusal string
}

// renderSignIn shows the AccessLink sign-in form inside the connect flow.
func renderSignIn(c *gin.Context, req oauthRequest, client *models.OAuthClient, message string) {
	renderOAuthPage(c, http.StatusOK, oauthSignInTemplate, consentPageData{
		Title:      "Sign in to AccessLink",
		ClientName: client.Name,
		Message:    message,
		Request:    req,
		FormToken:  issueOAuthNonce(c),
	})
}

// renderConsentOrRefusal shows what is being asked for, or explains that this
// account cannot grant it.
func renderConsentOrRefusal(c *gin.Context, req oauthRequest, client *models.OAuthClient,
	identity *models.OperatorIdentity, scopes []string) {

	granted, err := models.ValidateOAuthScopeRequest(scopes, client, identity.Role,
		middleware.RoleAtLeast)
	if err != nil {
		// THE CLIENT IS NOT TOLD WHY FROM HERE. The human is told plainly that
		// their account cannot grant this and who can; the application only
		// learns that the request was declined, if they choose to go back.
		// Which roles a customer's staff hold is the customer's business.
		renderOAuthPage(c, http.StatusOK, oauthConsentTemplate, consentPageData{
			Title:      "AccessLink",
			ClientName: client.Name,
			Email:      identity.Email,
			Request:    req,
			FormToken:  issueOAuthNonce(c),
			Refusal: "Your account does not have permission to connect " + client.Name +
				". Ask an administrator or the account owner to complete this connection.",
		})
		return
	}

	renderOAuthPage(c, http.StatusOK, oauthConsentTemplate, consentPageData{
		Title:       "Connect " + client.Name,
		ClientName:  client.Name,
		CompanyName: identity.CompanyName,
		Email:       identity.Email,
		Scopes:      models.DescribeScopes(granted),
		Request:     req,
		FormToken:   issueOAuthNonce(c),
	})
}

// renderOAuthProblem is the page for a request that cannot be returned to the
// application at all. There is no button: the only safe next step is to start
// again from wherever they came from.
func renderOAuthProblem(c *gin.Context, status int, heading, message string) {
	renderOAuthPage(c, status, oauthProblemTemplate, consentPageData{
		Title:   "AccessLink",
		Heading: heading,
		Message: message,
	})
}

func renderOAuthPage(c *gin.Context, status int, tmpl *template.Template, data consentPageData) {
	c.Header("Content-Security-Policy", oauthPageCSP)
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Cache-Control", "no-store")
	// Content type BEFORE the status: gin defers the header write until the
	// first body write, so this order works either way -- but a reader should
	// not have to know that to be sure the page is served as HTML.
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := tmpl.Execute(c.Writer, data); err != nil {
		logError(c, "render oauth page", err)
	}
	c.Abort()
}

// oauthPageStyle is inline because the policy above forbids loading anything.
// A connect flow that depends on a stylesheet request succeeding is one that
// renders as unstyled text the first time a CDN is slow.
const oauthPageStyle = `
:root { color-scheme: light dark; }
body { margin:0; padding:2rem 1rem; font:16px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;
       background:#f6f7f9; color:#14181f; display:flex; justify-content:center; }
@media (prefers-color-scheme: dark) { body { background:#101318; color:#e8eaed; } }
main { width:100%; max-width:26rem; background:#fff; border-radius:12px; padding:1.75rem;
       box-shadow:0 1px 3px rgba(0,0,0,.12); }
@media (prefers-color-scheme: dark) { main { background:#1a1e25; box-shadow:none;
       border:1px solid #2b313a; } }
h1 { font-size:1.25rem; margin:0 0 .5rem; }
p { margin:0 0 1rem; }
.muted { color:#5b6472; font-size:.9rem; }
@media (prefers-color-scheme: dark) { .muted { color:#9aa4b2; } }
ul { list-style:none; margin:0 0 1.25rem; padding:0; }
li { padding:.6rem 0; border-top:1px solid #e6e8ec; }
@media (prefers-color-scheme: dark) { li { border-top-color:#2b313a; } }
li b { display:block; font-weight:600; }
label { display:block; margin:0 0 1rem; font-weight:600; }
input[type=email], input[type=password] { display:block; width:100%; box-sizing:border-box;
       margin-top:.35rem; padding:.6rem; font:inherit; border:1px solid #c6ccd5;
       border-radius:8px; background:#fff; color:inherit; }
@media (prefers-color-scheme: dark) { input[type=email], input[type=password] {
       background:#11151b; border-color:#39414d; } }
.actions { display:flex; gap:.75rem; }
button { flex:1; padding:.7rem 1rem; font:inherit; font-weight:600; border-radius:8px;
       border:1px solid transparent; cursor:pointer; }
button.primary { background:#1f6feb; color:#fff; }
button.secondary { background:transparent; border-color:#c6ccd5; color:inherit; }
.error { background:#fdeaea; border:1px solid #f3bcbc; color:#7d1f1f; padding:.6rem .75rem;
       border-radius:8px; margin:0 0 1rem; }
@media (prefers-color-scheme: dark) { .error { background:#3a1c1c; border-color:#5d2a2a;
       color:#f2c5c5; } }
.write { font-size:.8rem; font-weight:600; color:#8a5a00; }
@media (prefers-color-scheme: dark) { .write { color:#e0a33a; } }
`

// oauthRequestFields restates the authorization request as hidden inputs, so
// the POST carries what the GET was asked and the server re-validates all of
// it. Nothing here is trusted -- see the note on oauthRequest.
const oauthRequestFields = `
<input type="hidden" name="response_type" value="{{.Request.ResponseType}}">
<input type="hidden" name="client_id" value="{{.Request.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.Request.RedirectURI}}">
<input type="hidden" name="scope" value="{{.Request.Scope}}">
<input type="hidden" name="state" value="{{.Request.State}}">
<input type="hidden" name="code_challenge" value="{{.Request.Challenge}}">
<input type="hidden" name="code_challenge_method" value="{{.Request.ChallengeMode}}">
<input type="hidden" name="form_token" value="{{.FormToken}}">
`

var (
	oauthSignInTemplate = template.Must(template.New("signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.Title}}</title><style>` + oauthPageStyle + `</style></head>
<body><main>
<h1>Sign in to AccessLink</h1>
<p class="muted">{{.ClientName}} is asking to connect to your AccessLink account.</p>
{{if .Message}}<p class="error" role="alert">{{.Message}}</p>{{end}}
<form method="post" autocomplete="on">
` + oauthRequestFields + `
<label>Email address
<input type="email" name="email" autocomplete="username" required autofocus></label>
<label>Password
<input type="password" name="password" autocomplete="current-password" required></label>
<input type="hidden" name="action" value="signin">
<div class="actions"><button type="submit" class="primary">Sign in</button></div>
</form>
</main></body></html>`))

	oauthConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.Title}}</title><style>` + oauthPageStyle + `</style></head>
<body><main>
{{if .Refusal}}
<h1>You cannot connect {{.ClientName}}</h1>
<p>{{.Refusal}}</p>
<form method="post">
` + oauthRequestFields + `
<input type="hidden" name="action" value="deny">
<div class="actions"><button type="submit" class="secondary">Back to {{.ClientName}}</button></div>
</form>
{{else}}
<h1>Connect {{.ClientName}}?</h1>
<p class="muted">{{.ClientName}} is asking for access to
{{if .CompanyName}}<b>{{.CompanyName}}</b>{{else}}your account{{end}} on AccessLink.
You are signed in as {{.Email}}.</p>
<ul>
{{range .Scopes}}<li><b>{{.Description}}</b>
<span class="muted">{{.Name}}</span>
{{if .Write}}<span class="write">Can make changes</span>{{end}}</li>{{end}}
</ul>
<p class="muted">You can disconnect {{.ClientName}} at any time, and this
connection never includes fingerprints, passwords or terminal credentials.</p>
<form method="post">
` + oauthRequestFields + `
<div class="actions">
<button type="submit" name="action" value="deny" class="secondary">Cancel</button>
<button type="submit" name="action" value="allow" class="primary">Allow</button>
</div>
</form>
{{end}}
</main></body></html>`))

	oauthProblemTemplate = template.Must(template.New("problem").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow">
<title>{{.Title}}</title><style>` + oauthPageStyle + `</style></head>
<body><main>
<h1>{{.Heading}}</h1>
<p>{{.Message}}</p>
</main></body></html>`))
)
