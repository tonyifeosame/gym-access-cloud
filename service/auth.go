package service

import (
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Authentication of a public request.
//
// The only way a request becomes a TenantContext. It takes the raw credential
// as presented and the DEPLOYMENT's environment, and answers with either a
// context or a service error carrying the registry code the handler will serve.
//
// ---------------------------------------------------------------------------
// TWO CREDENTIAL CLASSES, ONE ENTRY POINT, DISPATCHED BY PREFIX
// ---------------------------------------------------------------------------
//
// atp_ is an integration credential issued in the console (030); ato_ is an
// OAuth access token granted by an operator to a configured client (037). They
// are told apart by the class prefix BEFORE any database work, so a token
// presented to a deployment that has never configured a client costs a string
// comparison rather than a query, and neither class can ever be looked up in
// the other's table.
//
// ANYTHING THAT IS NEITHER SHAPE IS api_credential_invalid, unchanged: a
// session cookie value, a site key or a JWT pasted into the header is refused
// on shape without a lookup, exactly as it was before this dispatch existed.
//
// WHAT IS DISTINGUISHED AND WHAT IS NOT. A missing credential and one of the
// wrong shape are told apart from a wrong environment, because both are
// answerable by the caller without help: "you sent nothing" and "that is a
// staging key". Every other refusal -- unknown, revoked, expired, superseded
// past its grace, company inactive -- is api_credential_invalid. The store
// answers those with a single nil (see database.AuthenticateAPICredential), and
// P2 does not widen that query: the registry's revoked/expired codes stay
// reserved for a later decision about how much a refusal should say.
//
// A DATABASE FAILURE IS NOT AN INVALID CREDENTIAL. It is returned as
// service_unavailable so that an integrator during an outage is told to retry,
// not sent looking for a credential problem that does not exist.

// BearerCredential extracts the credential from an Authorization header value.
//
// Only the Bearer scheme is recognised, case-insensitively as RFC 6750 allows.
// Anything else -- Basic, a bare token, an empty header -- is "" and the caller
// reports api_credential_missing.
func BearerCredential(authorization string) string {
	scheme, token, found := strings.Cut(strings.TrimSpace(authorization), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// Authenticate resolves a presented credential to the tenant it belongs to.
func Authenticate(presented, environment, requestID string) (*TenantContext, error) {
	if presented == "" {
		return nil, ErrCredentialMissing()
	}

	if strings.HasPrefix(presented, models.OAuthAccessPrefix) {
		return authenticateOAuth(presented, environment, requestID)
	}

	if !models.ValidAPIKeyShape(presented) {
		return nil, ErrCredentialInvalid()
	}
	if models.APIKeyEnvironment(presented) != environment {
		return nil, ErrCredentialWrongEnvironment()
	}

	identity, err := database.AuthenticateAPICredential(presented, environment)
	if err != nil {
		return nil, ErrUnavailable(err)
	}
	if identity == nil {
		return nil, ErrCredentialInvalid()
	}
	return FromCredential(identity, requestID), nil
}

// authenticateOAuth resolves an ato_ access token.
//
// The same refusal vocabulary as the credential path, deliberately: an
// integrator handling 401s does not have to learn a second set of codes because
// the customer connected through OAuth rather than pasting a key. Unknown,
// expired, revoked, client deactivated, operator deactivated and company
// deactivated are all api_credential_invalid -- the store answers them with a
// single nil, and an integrator can act on none of them differently.
//
// A WRONG-ENVIRONMENT TOKEN IS TOLD APART, as it is for atp_, because that one
// IS actionable: it means a staging token reached production, and the answer is
// to look at configuration rather than at the grant.
func authenticateOAuth(presented, environment, requestID string) (*TenantContext, error) {
	if !models.ValidOAuthAccessTokenShape(presented) {
		return nil, ErrCredentialInvalid()
	}
	if models.OAuthTokenEnvironment(presented) != environment {
		return nil, ErrCredentialWrongEnvironment()
	}

	identity, err := database.AuthenticateOAuthAccessToken(presented, environment)
	if err != nil {
		return nil, ErrUnavailable(err)
	}
	if identity == nil {
		return nil, ErrCredentialInvalid()
	}
	return FromOAuthGrant(identity, requestID), nil
}
