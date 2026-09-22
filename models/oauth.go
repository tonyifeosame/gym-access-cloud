package models

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The OAuth 2.0 authorization server's vocabulary (migrations/037).
//
// ---------------------------------------------------------------------------
// THREE SECRETS, THREE PREFIXES, ONE DISCIPLINE
// ---------------------------------------------------------------------------
//
//	atc_<64 hex>              authorization code. Seconds long, single use,
//	                          travels through a browser redirect.
//	ato_live_<64 hex>         access token. Presented on every resource
//	                          request, beside atp_ in the same header.
//	atr_live_<64 hex>         refresh token. Rotates on every use.
//
// The class prefix is distinguishable at a glance from ats_ (site), atd_
// (device) and atp_ (integration credential), for the reason generateSiteKey
// already records: one gets pasted where another belongs, and the first thing
// anybody does is look at the front of it.
//
// THE ENVIRONMENT IS IN THE TOKEN, as it is for atp_. A token minted by a
// staging deployment is refused by a live one on shape, before any lookup, so a
// misconfigured integration fails immediately and unambiguously rather than
// looking like a revoked grant.
//
// The authorization code carries NO environment. It is exchanged at the same
// deployment that issued it, within two minutes, and a segment there would be
// one more thing in a URL a human may read over somebody's shoulder.
//
// ---------------------------------------------------------------------------
// NOTHING IN THIS FILE CARRIES A PLAINTEXT SECRET ON A SERIALISED STRUCT
// ---------------------------------------------------------------------------
//
// OAuthTokenResponse is the ONE exception and it is the protocol's: RFC 6749
// section 5.1 requires the token endpoint to return the token it just minted.
// Every other struct here is a projection with no secret field, so no handler
// can serialise one by accident -- the same property models.APICredential has.

// Credential string shapes.
const (
	OAuthCodePrefix    = "atc_"
	OAuthAccessPrefix  = "ato_"
	OAuthRefreshPrefix = "atr_"

	// OAuthSecretHexLength is the hex length of the random half of every one
	// of them: 32 bytes from crypto/rand.
	OAuthSecretHexLength = 64
)

var (
	oauthCodePattern    = regexp.MustCompile(`^atc_[0-9a-f]{64}$`)
	oauthAccessPattern  = regexp.MustCompile(`^ato_(live|test)_[0-9a-f]{64}$`)
	oauthRefreshPattern = regexp.MustCompile(`^atr_(live|test)_[0-9a-f]{64}$`)
)

// ValidOAuthCodeShape reports whether a string could be an authorization code.
func ValidOAuthCodeShape(s string) bool { return oauthCodePattern.MatchString(s) }

// ValidOAuthAccessTokenShape reports whether a string could be an access token.
//
// Checked before the database is touched, exactly as ValidAPIKeyShape is: a
// caller sending a session cookie value or a site key into the Authorization
// header is refused without costing a query.
func ValidOAuthAccessTokenShape(s string) bool { return oauthAccessPattern.MatchString(s) }

// ValidOAuthRefreshTokenShape reports whether a string could be a refresh token.
func ValidOAuthRefreshTokenShape(s string) bool { return oauthRefreshPattern.MatchString(s) }

// OAuthTokenEnvironment extracts the environment segment from an access or
// refresh token, or "" when the string is not token-shaped.
func OAuthTokenEnvironment(token string) string {
	if !ValidOAuthAccessTokenShape(token) && !ValidOAuthRefreshTokenShape(token) {
		return ""
	}
	_, rest, _ := strings.Cut(token, "_")
	environment, _, _ := strings.Cut(rest, "_")
	return environment
}

// Lifetimes.
const (
	// OAuthCodeLifetime is how long an authorization code may be exchanged
	// for. RFC 6749 section 4.1.2 puts the ceiling at ten minutes; two is the
	// time a server-side exchange actually needs, including a retry, and the
	// code is single-use regardless.
	OAuthCodeLifetime = 2 * time.Minute

	// OAuthAccessTokenLifetime bounds the damage of a leaked access token
	// before anybody notices it leaked. An hour is the interval a client can
	// work with without refreshing constantly; the refresh token is what
	// carries the long-lived relationship, and that one rotates.
	OAuthAccessTokenLifetime = time.Hour

	// OAuthRefreshTokenLifetime is how long a client may stay connected
	// without the customer signing in again. Sixty days: long enough that a
	// working integration is not interrupted, short enough that an abandoned
	// one stops by itself.
	//
	// ROTATION DOES NOT EXTEND IT PAST THE FAMILY'S OWN AGE. See
	// database.RefreshOAuthGrant -- a chain that could renew its own deadline
	// forever would be a permanent grant wearing a rotating disguise.
	OAuthRefreshTokenLifetime = 60 * 24 * time.Hour

	// OAuthConsentFormLifetime bounds how long a rendered consent page may be
	// submitted from. The page carries a signed, single-purpose form token; a
	// page left open for an hour has to be reloaded rather than submitted.
	OAuthConsentFormLifetime = 15 * time.Minute
)

// Limits.
const (
	// MaxOAuthRedirectURILength matches what a browser will reliably carry and
	// what the column holds.
	MaxOAuthRedirectURILength = 512

	// MaxOAuthStateLength bounds the opaque value a client asks to have
	// echoed back. It is reflected into a redirect, so it is bounded before
	// anything is built from it.
	MaxOAuthStateLength = 512

	// MaxOAuthScopeRequestLength bounds the space-delimited scope parameter.
	MaxOAuthScopeRequestLength = 256
)

// Revocation reasons. A closed set rather than free text: this column is read
// during an incident, and "the customer disconnected" and "we detected a
// stolen token" must be distinguishable at a glance.
const (
	// OAuthRevokedByClient is RFC 7009: the client asked.
	OAuthRevokedByClient = "CLIENT_REVOKED"

	// OAuthRevokedReuse is a refresh token presented after it was rotated or
	// revoked. Two parties hold tokens from one chain; the family dies.
	OAuthRevokedReuse = "REUSE_DETECTED"

	// OAuthRevokedCodeReplay is an authorization code presented twice. RFC
	// 6749 section 4.1.2 asks that whatever the first exchange produced be
	// revoked, because the server cannot tell which presentation was the
	// legitimate one.
	OAuthRevokedCodeReplay = "CODE_REPLAYED"

	// OAuthRevokedByOwner is the customer withdrawing the grant.
	OAuthRevokedByOwner = "OWNER_REVOKED"
)

// OAuthRevocationReasons is the closed set, for validation at the store edge.
var OAuthRevocationReasons = map[string]bool{
	OAuthRevokedByClient:   true,
	OAuthRevokedReuse:      true,
	OAuthRevokedCodeReplay: true,
	OAuthRevokedByOwner:    true,
}

// ---------------------------------------------------------------------------
// Protocol errors
// ---------------------------------------------------------------------------
//
// RFC 6749 SECTION 5.2 IS NOT THE ACCESSLINK ERROR ENVELOPE, and the token
// endpoint must speak the protocol's shape rather than this API's. A client
// library written against the RFC parses `error` as a string at the top level;
// handing it {"error": {"type": …, "code": …}} would break every one of them.
//
// So: the OAUTH endpoints answer in the OAuth shape, and the RESOURCE endpoints
// answer in the AccessLink shape, which is what an integrator already expects
// from every other public route. The boundary is exactly the /oauth/ prefix and
// is stated in the reference.

// OAuth error codes, from RFC 6749 sections 4.1.2.1 and 5.2 and RFC 7636.
const (
	OAuthErrInvalidRequest          = "invalid_request"
	OAuthErrUnauthorizedClient      = "unauthorized_client"
	OAuthErrAccessDenied            = "access_denied"
	OAuthErrUnsupportedResponseType = "unsupported_response_type"
	OAuthErrInvalidScope            = "invalid_scope"
	OAuthErrServerError             = "server_error"
	OAuthErrTemporarilyUnavailable  = "temporarily_unavailable"
	OAuthErrInvalidClient           = "invalid_client"
	OAuthErrInvalidGrant            = "invalid_grant"
	OAuthErrUnsupportedGrantType    = "unsupported_grant_type"
)

// OAuthErrorBody is the wire shape of a token or revocation failure.
//
// NO REQUEST ID FIELD AND NO doc_url. The protocol defines exactly three
// members and a client library that validates the object would reject extras;
// the X-Request-ID header is on the response as it is on every other, which is
// where an integrator quoting a failure should look.
type OAuthErrorBody struct {
	Error string `json:"error"`
	// Description is for a human reading a log. It never contains a token, a
	// code, an internal error string or a hint about whether an account exists.
	Description string `json:"error_description,omitempty"`
	URI         string `json:"error_uri,omitempty"`
}

// OAuthTokenResponse is RFC 6749 section 5.1.
//
// THE ONE STRUCT IN THIS PACKAGE THAT CARRIES PLAINTEXT SECRETS, because the
// protocol requires it: this is the single moment a token exists outside the
// client that will hold it. Nothing stores this struct, nothing logs it, and no
// other endpoint returns one.
type OAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	// RefreshToken is absent when the grant did not produce one.
	RefreshToken string `json:"refresh_token,omitempty"`
	// Scope is the scopes ACTUALLY granted, space-delimited, which may be
	// narrower than what was asked for. RFC 6749 section 5.1 requires it to be
	// stated whenever it differs, and it is stated unconditionally here so a
	// client never has to infer it.
	Scope string `json:"scope"`
}

// OAuthTokenTypeBearer is the only token type this server issues.
const OAuthTokenTypeBearer = "Bearer"

// ---------------------------------------------------------------------------
// Client and grant projections
// ---------------------------------------------------------------------------

// OAuthClient is a configured client as the store reads it.
//
// SecretHash is deliberately absent. It is read and compared inside the
// database package and never carried here, so no handler can serialise it by
// accident -- the same property models.User has for a password hash.
type OAuthClient struct {
	ID       int64
	ClientID string
	Name     string

	// Confidential reports whether this client has a secret to prove. A public
	// client is permitted because PKCE is mandatory; a confidential one must
	// additionally authenticate at the token endpoint.
	Confidential bool

	RedirectURIs []string
	Scopes       []string
	Active       bool
}

// AllowsRedirectURI reports whether a redirect URI is on the allow-list.
//
// EXACT STRING COMPARISON. No prefix match, no wildcard, no normalisation, no
// "the host is the same so it is close enough": every loosening of this check
// that has ever been shipped turned into an open redirector with an
// authorization code attached. A client that needs two URIs configures two.
func (c *OAuthClient) AllowsRedirectURI(uri string) bool {
	for _, allowed := range c.RedirectURIs {
		if allowed == uri {
			return true
		}
	}
	return false
}

// PermitsScope reports whether a scope is within this client's configured
// maximum.
func (c *OAuthClient) PermitsScope(scope string) bool {
	for _, allowed := range c.Scopes {
		if allowed == scope {
			return true
		}
	}
	return false
}

// OAuthTokenIdentity is the authenticated caller behind a resource request made
// with an access token -- the OAuth counterpart to APICredentialIdentity.
//
// CompanyID is the ONLY source of tenancy, and it comes from the token row. No
// query parameter, no header and no body field may influence it.
//
// SiteIDs is NOT stored on the token. It is computed at authentication time
// from the consenting operator's CURRENT site grants, so narrowing an operator
// narrows every grant they made, immediately. EMPTY MEANS EVERY SITE IN THE
// COMPANY, matching the operator grant rule and the credential one.
type OAuthTokenIdentity struct {
	ID       int64
	PublicID string

	CompanyID int64

	// UserID is the operator who consented. Carried so an audit record can name
	// the human behind a machine request rather than only the client.
	UserID    int64
	UserEmail string

	// ClientID is the client's public identifier ("datavase"), used where the
	// credential path uses a key prefix: in log lines and audit rows. It is not
	// a secret.
	ClientID   string
	ClientName string

	// ClientRowID is the client's row. Carried because the per-caller rate
	// bucket is kept under the CLIENT and the company rather than the token: a
	// token is replaced on every refresh, so a per-token bucket would hand a
	// client a fresh full allowance whenever it chose to refresh -- a limit
	// that can be reset by the party it limits.
	//
	// AN INTEGER, so the bucket's subject key is bounded. The client_id string
	// is up to 64 characters and api_rate_buckets.subject_key is capped at 64,
	// so keying on the name would be a length the database refuses.
	ClientRowID int64

	Environment string
	Scopes      []string
	SiteIDs     []int64
}

// HasScope reports whether the grant carries a scope. Membership only;
// implication was expanded when the grant was made.
func (i *OAuthTokenIdentity) HasScope(scope string) bool {
	for _, held := range i.Scopes {
		if held == scope {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Request validation
// ---------------------------------------------------------------------------

// Errors from the authorization-server edge. Handlers map these onto protocol
// error codes; anything else coming back is a server fault.
var (
	ErrOAuthClientUnknown  = errors.New("unknown oauth client")
	ErrOAuthClientInactive = errors.New("oauth client is not active")
	ErrOAuthRedirectURI    = errors.New("redirect_uri is not registered for this client")
	ErrOAuthCodeUnknown    = errors.New("authorization code is not valid")
	ErrOAuthCodeExpired    = errors.New("authorization code has expired")
	ErrOAuthCodeConsumed   = errors.New("authorization code has already been used")
	ErrOAuthGrantUnknown   = errors.New("refresh token is not valid")
	ErrOAuthGrantReused    = errors.New("refresh token has already been used")

	// ErrOAuthScopeUngrantable is a scope the client may not hold, or one the
	// consenting operator's own role does not let them grant. Both are refusals
	// and the caller is not told which: an integration learning that a scope
	// exists but is above the signed-in operator is information about the
	// customer's staffing, not about the request.
	ErrOAuthScopeUngrantable = errors.New("scope cannot be granted")
)

// codeChallengePattern is the base64url form of a SHA-256 digest, unpadded:
// 43 characters. Anything else is not an S256 challenge.
var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidCodeChallenge reports whether a PKCE challenge is a well-formed S256
// challenge.
func ValidCodeChallenge(challenge string) bool {
	return codeChallengePattern.MatchString(challenge)
}

// codeVerifierPattern is RFC 7636 section 4.1: 43 to 128 characters from the
// unreserved set.
var codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// ValidCodeVerifier reports whether a PKCE verifier is well formed.
//
// THE LENGTH FLOOR IS THE SECURITY PROPERTY. A verifier is the only thing
// standing between an intercepted code and a token; 43 characters of the
// unreserved set is the RFC's minimum and corresponds to 256 bits. A client
// sending something shorter has not implemented PKCE, it has implemented a
// password, and it is refused rather than accepted at reduced strength.
func ValidCodeVerifier(verifier string) bool {
	return codeVerifierPattern.MatchString(verifier)
}

// ParseScopeRequest splits an OAuth `scope` parameter into its elements.
//
// Space-delimited per RFC 6749 section 3.3, with repeated and trailing spaces
// tolerated -- a client that builds the string by joining an array with a space
// and happens to include an empty element should not be told its request is
// malformed for it.
//
// AN EMPTY REQUEST IS NOT AN ERROR HERE. What an absent `scope` means is the
// authorization endpoint's decision (it means "everything this client is
// configured for"), and encoding that here would put the policy in the parser.
//
// AN OVERSIZED ONE IS NOT TRUNCATED. The caller checks ScopeRequestTooLong
// first and refuses; silently dropping the tail would leave a client believing
// it had asked for something it did not get, which is the failure
// ExpandScopes already refuses to produce for an unknown scope.
func ParseScopeRequest(raw string) []string {
	fields := strings.Fields(raw)
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// ScopeRequestTooLong reports a `scope` parameter longer than this server will
// read. Checked before parsing, so nothing is ever silently dropped.
func ScopeRequestTooLong(raw string) bool { return len(raw) > MaxOAuthScopeRequestLength }

// FormatScopeGrant renders a granted scope set for the token response.
func FormatScopeGrant(scopes []string) string { return strings.Join(scopes, " ") }

// ValidateOAuthScopeRequest resolves what a consent may actually grant.
//
// THREE BOUNDS, ALL OF WHICH APPLY, and the order they are applied in does not
// change the answer because every one of them can only narrow:
//
//	requested     what the client asked for, or the client's whole set when it
//	              asked for nothing.
//	client        what this client is configured to be allowed to hold.
//	issuerRole    what the signed-in operator may grant, from the scope
//	              registry's MinRole -- the same bound the console applies when
//	              an ADMIN issues an integration credential.
//
// The result is EXPANDED (members:write carries members:read) by the same
// ExpandScopes the credential path uses, so a permission check downstream stays
// a set membership test with no inference in it.
//
// A request that narrows to nothing is ErrOAuthScopeUngrantable rather than an
// empty grant: a token that can do nothing is not a token, and the client would
// discover it one 403 at a time.
func ValidateOAuthScopeRequest(requested []string, client *OAuthClient, issuerRole string,
	atLeast func(role, minimum string) bool) ([]string, error) {

	if client == nil {
		return nil, ErrOAuthClientUnknown
	}

	if len(requested) == 0 {
		requested = append([]string(nil), client.Scopes...)
	}

	for _, name := range requested {
		if !KnownScope(name) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownScope, name)
		}
		if !client.PermitsScope(name) {
			return nil, fmt.Errorf("%w: %q", ErrOAuthScopeUngrantable, name)
		}
		if !atLeast(issuerRole, Scopes[name].MinRole) {
			return nil, fmt.Errorf("%w: %q", ErrOAuthScopeUngrantable, name)
		}
	}

	expanded, err := ExpandScopes(requested)
	if err != nil {
		return nil, err
	}

	// EXPANSION IS RE-CHECKED AGAINST BOTH BOUNDS. members:write implies
	// members:read, and a client configured for write but not read would
	// otherwise be granted a scope its configuration does not name. Narrowing
	// silently would be worse: the client would hold a token whose stated scope
	// it was never allowed.
	for _, name := range expanded {
		if !client.PermitsScope(name) || !atLeast(issuerRole, Scopes[name].MinRole) {
			return nil, fmt.Errorf("%w: %q is implied by the request", ErrOAuthScopeUngrantable, name)
		}
	}
	return expanded, nil
}

// OAuthGrantableScopes is the closed set an OAuth CLIENT may be configured for
// in this version.
//
// ---------------------------------------------------------------------------
// A PHASE BOUNDARY, ENFORCED RATHER THAN INTENDED
// ---------------------------------------------------------------------------
//
// The scope registry describes what a CREDENTIAL can carry. This is the
// narrower question of what a third party may be granted through a consent
// screen, and in this version the answer is sites only -- which is exactly the
// surface that has been designed, documented and tested for a grant.
//
// IT IS NOT A STYLE RULE. The two differences between a credential and a grant
// are load-bearing:
//
//   - IDEMPOTENCY. idempotency_records.credential_id is a foreign key into
//     api_credentials, so a grant has no row to key an Idempotency-Key against
//     and the middleware passes one through (see middleware/idempotency.go).
//     That is harmless for the writes this phase serves -- a repeated
//     POST /sites is refused as a duplicate name and PATCH is idempotent by
//     construction -- and it is NOT harmless for a route whose replay can only
//     be made safe by a stored response. members:write is exactly such a route.
//
//   - USAGE ACCOUNTING. api_usage_daily has the same foreign key, so a grant's
//     traffic does not appear in the console's "is this key still in use"
//     report. An administrator reviewing a members integration should not have
//     to discover that.
//
// Widening this set is therefore a decision with work attached, not a
// configuration change. Refused at the STORE edge so the environment path and
// any test fixture are bound by the same rule.
var OAuthGrantableScopes = map[string]bool{
	ScopeSitesRead:  true,
	ScopeSitesWrite: true,
}

// GrantableByOAuth reports whether a scope may be configured on an OAuth client.
func GrantableByOAuth(scope string) bool { return OAuthGrantableScopes[scope] }

// GrantableOAuthScopes returns the set, sorted, for an error message.
func GrantableOAuthScopes() []string {
	out := make([]string, 0, len(OAuthGrantableScopes))
	for name := range OAuthGrantableScopes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ErrOAuthScopeNotGrantable names a scope this version will not put behind a
// consent screen. See OAuthGrantableScopes.
var ErrOAuthScopeNotGrantable = errors.New("scope is not available to an OAuth client in this version")

// OAuthConsentScope is one line on the consent screen.
//
// Built from the registry so the description a customer reads when they connect
// a third party is the same sentence the console shows beside the checkbox when
// an administrator issues a key. Two wordings for one capability is how a
// customer ends up believing they granted something else.
type OAuthConsentScope struct {
	Name        string
	Description string
	Write       bool
}

// DescribeScopes renders a scope set for a human, in registry order.
func DescribeScopes(scopes []string) []OAuthConsentScope {
	out := make([]OAuthConsentScope, 0, len(scopes))
	for _, name := range scopes {
		spec, ok := Scopes[name]
		if !ok {
			continue
		}
		out = append(out, OAuthConsentScope{
			Name:        spec.Name,
			Description: spec.Description,
			Write:       spec.Write,
		})
	}
	return out
}
