package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/oidc"

	"github.com/gin-gonic/gin"
)

// Sign in with Google, /api/v1/auth/google/* and /api/v1/auth/providers.
//
// ---------------------------------------------------------------------------
// ONE SESSION MECHANISM
// ---------------------------------------------------------------------------
//
// A Google sign-in ends in database.CreateSession, the same cookie, the same
// CSRF derivation and the same identity lookup a password sign-in ends in.
// Nothing below builds a session of its own, and /me cannot tell the two apart
// -- which is the point. Google is a way to prove who somebody is, not a second
// way to be logged in.
//
// ---------------------------------------------------------------------------
// THE SHAPE OF THE FLOW, AND WHY IT IS SERVER-SIDE
// ---------------------------------------------------------------------------
//
// The console is deployed on one origin and this API on another, and the
// console ships a Content Security Policy of `script-src 'self'`. Google's
// in-page button is a third-party script, so it cannot load there, and a
// browser-side flow would have handed the console a token to forward anyway.
// The authorization-code flow needs nothing in the page but a link:
//
//	browser  GET /auth/google/start?next=/people
//	  -> 303 to accounts.google.com, with a state cookie set on THIS origin
//	browser  (signs in at Google)
//	  -> Google 302s to GET /auth/google/callback?code=…&state=…
//	  -> the code is exchanged, the ID token verified, the operator resolved
//	  -> the session cookie is set and the browser is 303'd to CONSOLE_URL/next
//
// Every failure on the way back is a 303 to CONSOLE_URL/login?error=<code>,
// with the reason in the server log. The browser learns which of six things
// happened and nothing about any account.
//
// ---------------------------------------------------------------------------
// THE STATE COOKIE
// ---------------------------------------------------------------------------
//
// The per-attempt secrets -- state, nonce, PKCE verifier -- and the sanitised
// `next` path live in one short-lived cookie, HttpOnly, SameSite=Lax and
// __Host- prefixed in production. Lax is what lets the browser send it on the
// top-level navigation back from Google, and __Host- is what stops a cookie
// planted from another host on the same registrable domain being accepted in
// its place. The callback compares the echoed `state` to the cookie's and
// refuses on any mismatch, so a callback URL pasted into somebody else's
// browser resolves to nothing.

// Outcome codes the browser is sent back with. STABLE: the console branches on
// them, and they are part of what a frontend test asserts.
const (
	googleOutcomeCancelled  = "google_cancelled"
	googleOutcomeFailed     = "google_failed"
	googleOutcomeNoAccount  = "google_no_account"
	googleOutcomeUnverified = "google_email_unverified"
	googleOutcomeConflict   = "google_account_conflict"
	googleOutcomeState      = "google_state_mismatch"
)

// auditOperatorGoogleLinked records the one-time linking of a Google account.
// A distinct action from a login, because it is a change to the account that
// somebody reviewing the trail should be able to find.
const auditOperatorGoogleLinked = "OPERATOR_GOOGLE_LINKED"

// EnvConsoleURL is where the browser is sent after a redirect-based flow, and
// the base for links the platform puts in email. Absolute, no trailing slash.
const EnvConsoleURL = "CONSOLE_URL"

// stateCookieTTL bounds how long a started sign-in stays redeemable. Long
// enough to type a password and pass a second factor at Google; short enough
// that an abandoned attempt does not sit in the browser for a day.
const stateCookieTTL = 10 * time.Minute

var (
	googleProvider *oidc.Provider
	consoleURL     string
)

// ConfigureGoogleSignIn installs the provider, or disables the feature with nil.
func ConfigureGoogleSignIn(p *oidc.Provider) { googleProvider = p }

// GoogleSignInEnabled reports whether the routes will do anything.
func GoogleSignInEnabled() bool { return googleProvider != nil }

// ConfigureConsoleURL sets where redirect flows land and email links point.
//
// An empty value means "the same origin as this API", which is what the
// development proxy makes true. A split-origin deployment sets it explicitly,
// and startup refuses a redirect-based feature without it.
func ConfigureConsoleURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		consoleURL = ""
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New(EnvConsoleURL + " must be an absolute URL, got " + strconvQuote(raw))
	}
	if parsed.Scheme != "https" && !strings.HasPrefix(parsed.Host, "localhost") &&
		!strings.HasPrefix(parsed.Host, "127.0.0.1") {
		return errors.New(EnvConsoleURL + " must use https unless it is on localhost")
	}
	consoleURL = strings.TrimRight(raw, "/")
	return nil
}

// ConsoleURL is the configured console origin, or "" for same-origin.
func ConsoleURL() string { return consoleURL }

// ConsoleURLFromEnv reads and validates CONSOLE_URL.
func ConsoleURLFromEnv() error { return ConfigureConsoleURL(os.Getenv(EnvConsoleURL)) }

func strconvQuote(s string) string { return `"` + s + `"` }

// consoleLink joins a console path onto the configured origin.
func consoleLink(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return consoleURL + path
}

// safeNext keeps a post-login destination inside the console: a path, not a
// URL, and not a protocol-relative one. Anything else becomes the root.
func safeNext(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.HasPrefix(raw, "/\\") || strings.ContainsAny(raw, "\r\n") {
		return "/"
	}
	return raw
}

// AuthProviders handles GET /api/v1/auth/providers
//
// Unauthenticated and static: what ways in this deployment offers. The console
// reads it once to decide which controls to draw. Nothing here varies with who
// is asking, so there is nothing for it to disclose.
func AuthProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"password": gin.H{"enabled": true},
		"google": gin.H{
			"enabled":    GoogleSignInEnabled(),
			"start_path": "/api/v1/auth/google/start",
		},
		"signup": gin.H{"enabled": PublicSignupEnabled()},
	})
}

// oauthState is what the state cookie carries.
type oauthState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"next"`
}

func oauthCookieName() (string, bool) {
	name, secure := middleware.SessionCookieConfig()
	// Same prefix rule as the session cookie: __Host- when Secure is on.
	if strings.HasPrefix(name, "__Host-") {
		return "__Host-al_oauth", secure
	}
	return "al_oauth", secure
}

func setOAuthCookie(c *gin.Context, state oauthState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	name, secure := oauthCookieName()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, base64.RawURLEncoding.EncodeToString(raw),
		int(stateCookieTTL.Seconds()), "/", "", secure, true)
	return nil
}

func clearOAuthCookie(c *gin.Context) {
	name, secure := oauthCookieName()
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", "", secure, true)
}

func readOAuthCookie(c *gin.Context) (*oauthState, bool) {
	name, _ := oauthCookieName()
	value, err := c.Cookie(name)
	if err != nil || value == "" {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	var state oauthState
	if err := json.Unmarshal(raw, &state); err != nil || state.State == "" ||
		state.Nonce == "" || state.Verifier == "" {
		return nil, false
	}
	return &state, true
}

// googleUnavailable answers a route whose feature is switched off.
//
// 403 rather than 404, for the reason signup gives: a route that pretends not
// to exist sends whoever hit it looking for a typo rather than for the switch.
func googleUnavailable(c *gin.Context) {
	c.JSON(http.StatusForbidden, gin.H{
		"error": "Google sign-in is not enabled on this deployment"})
}

// GoogleStart handles GET /api/v1/auth/google/start
//
// A browser navigation, not an XHR: the response is a redirect to Google. The
// destination after sign-in is taken from `next` and sanitised here, once, so
// the callback trusts what it reads back from the cookie.
func GoogleStart(c *gin.Context) {
	if !GoogleSignInEnabled() {
		googleUnavailable(c)
		return
	}

	secrets, err := oidc.NewFlowSecrets()
	if err != nil {
		logError(c, "google sign-in: mint flow secrets", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Sign-in unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	target, err := googleProvider.AuthURL(ctx, secrets)
	if err != nil {
		// Discovery failed: Google is unreachable from here. The console's own
		// login still works, and the operator is told so rather than bounced
		// to a page that never loads.
		logError(c, "google sign-in: build authorization url", err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "Google could not be reached to start the sign-in. Sign in with " +
				"your password, or try again shortly."})
		return
	}

	if err := setOAuthCookie(c, oauthState{
		State:    secrets.State,
		Nonce:    secrets.Nonce,
		Verifier: secrets.Verifier,
		Next:     safeNext(c.Query("next")),
	}); err != nil {
		logError(c, "google sign-in: write state cookie", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Sign-in unavailable"})
		return
	}

	// No caching, ever: a cached redirect would replay a spent state.
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusSeeOther, target)
}

// GoogleCallback handles GET /api/v1/auth/google/callback
//
// Google sends the browser here. Every exit is a redirect back to the console;
// the only things this handler ever writes to the response are cookies and a
// Location header.
func GoogleCallback(c *gin.Context) {
	if !GoogleSignInEnabled() {
		googleUnavailable(c)
		return
	}

	c.Header("Cache-Control", "no-store")
	fail := func(outcome, reason string, err error) {
		clearOAuthCookie(c)
		if err != nil {
			log.Printf("request_id=%s auth=google-rejected outcome=%s ip=%s: %s: %v",
				middleware.RequestID(c), outcome, c.ClientIP(), reason, err)
		} else {
			log.Printf("request_id=%s auth=google-rejected outcome=%s ip=%s: %s",
				middleware.RequestID(c), outcome, c.ClientIP(), reason)
		}
		c.Redirect(http.StatusSeeOther, consoleLink("/login?error="+outcome))
	}

	// The cookie first: without it nothing that follows can be trusted, and
	// a callback with no cookie is the pasted-link case.
	state, ok := readOAuthCookie(c)
	if !ok {
		fail(googleOutcomeState, "no state cookie on the callback", nil)
		return
	}
	if echoed := c.Query("state"); echoed == "" || echoed != state.State {
		fail(googleOutcomeState, "state parameter does not match the cookie", nil)
		return
	}

	// The person said no at Google's consent screen, or Google refused. Not a
	// fault, and reported as the one outcome that is nobody's mistake.
	if oauthErr := c.Query("error"); oauthErr != "" {
		if oauthErr == "access_denied" {
			fail(googleOutcomeCancelled, "the user declined at google", nil)
		} else {
			fail(googleOutcomeFailed, "google returned error="+oauthErr, nil)
		}
		return
	}

	code := c.Query("code")
	if code == "" {
		fail(googleOutcomeFailed, "callback carried no code", nil)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	identity, err := googleProvider.Exchange(ctx, code, &oidc.FlowSecrets{
		State: state.State, Nonce: state.Nonce, Verifier: state.Verifier,
	})
	if err != nil {
		fail(googleOutcomeFailed, "code exchange or token verification failed", err)
		return
	}

	result, err := database.AuthenticateGoogle(database.GoogleSignIn{
		Subject:       identity.Subject,
		Email:         identity.Email,
		EmailVerified: identity.EmailVerified,
	})
	switch {
	case errors.Is(err, database.ErrGoogleNoAccount):
		fail(googleOutcomeNoAccount, "no account for this google identity", nil)
		return
	case errors.Is(err, database.ErrGoogleEmailUnverified):
		fail(googleOutcomeUnverified, "google has not verified the address", nil)
		return
	case errors.Is(err, database.ErrGoogleAccountConflict):
		fail(googleOutcomeConflict, "address belongs to an account linked elsewhere", nil)
		return
	case err != nil:
		logError(c, "google sign-in: resolve operator", err)
		fail(googleOutcomeFailed, "store error", nil)
		return
	}
	user := result.User

	// FROM HERE ON THIS IS A LOGIN, and it is the same login.
	creds, err := database.CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		logError(c, "google sign-in: create session", err)
		fail(googleOutcomeFailed, "session could not be created", nil)
		return
	}
	sessionIdentity, err := database.AuthenticateSession(creds.Token)
	if err != nil || sessionIdentity == nil {
		logError(c, "google sign-in: resolve new session", err)
		fail(googleOutcomeFailed, "session could not be resolved", nil)
		return
	}

	if result.Linked {
		database.WriteAuditEvent(database.AuditEntry{
			CompanyID:      user.CompanyID,
			ActorUserID:    user.ID,
			ActorEmail:     user.Email,
			ActorRole:      user.Role,
			IPAddress:      c.ClientIP(),
			UserAgent:      c.Request.UserAgent(),
			RequestID:      middleware.RequestID(c),
			Action:         auditOperatorGoogleLinked,
			TargetType:     auditTargetOperator,
			TargetPublicID: user.PublicID,
			TargetLabel:    user.Email,
			Changes: map[string]any{
				"password_retired": result.PasswordRetired,
			},
		})
	}

	clearOAuthCookie(c)
	setSessionCookie(c, creds.Token)
	log.Printf("request_id=%s auth=success method=google operator=%s company=%s linked=%t ip=%s",
		middleware.RequestID(c), sessionIdentity.UserPublicID, sessionIdentity.CompanySlug,
		result.Linked, c.ClientIP())

	c.Redirect(http.StatusSeeOther, consoleLink(safeNext(state.Next)))
}
