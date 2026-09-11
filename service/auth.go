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
