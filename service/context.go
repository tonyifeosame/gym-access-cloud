package service

import (
	"strconv"

	"access-terminal-cloud-api/models"
)

// The authenticated context every public API operation receives.
//
// ---------------------------------------------------------------------------
// WHERE THE TENANT COMES FROM, AND WHERE IT CANNOT
// ---------------------------------------------------------------------------
//
// A service method is called with a *TenantContext as its second argument,
// after the context.Context, and takes the tenant from nothing else. There is
// no method on any service that accepts a company id, and no input struct with
// a field that names one -- a test enumerates the package by reflection to keep
// it that way.
//
// A TenantContext can be built in exactly one way: from an authenticated
// models.APICredentialIdentity, which the database produced from a credential
// row. Its fields are unexported so a handler cannot assemble one from a body,
// a query parameter or a header, and there is deliberately no setter.
//
// The identity struct itself is public and any test can build one -- that is
// the seam the service tests use. What the design closes is the path from a
// REQUEST to a tenant: it runs through the credential store and nowhere else.
//
// ---------------------------------------------------------------------------
// TWO WAYS TO BE AUTHENTICATED, ONE CONTEXT
// ---------------------------------------------------------------------------
//
// Since 037 a public request may arrive with an INTEGRATION CREDENTIAL (atp_,
// issued by an administrator in the console) or with an OAUTH ACCESS TOKEN
// (ato_, granted by an operator to a configured client). Both are authenticated
// bearer credentials belonging to one company with one scope set, so both
// produce the same context and every service method downstream is unchanged.
//
// WHAT THE PRINCIPAL KIND IS FOR, AND WHAT IT IS NOT FOR. It is for the two
// places the difference is real: an audit record has to say which kind of thing
// acted, and the idempotency store keys on api_credentials.id and therefore has
// no row for a token. NO AUTHORIZATION DECISION READS IT. A handler that
// branched on the principal would be two authorization models wearing one type,
// which is the thing this file exists to prevent.
type TenantContext struct {
	companyID int64

	// principal names which credential class authenticated. Exactly one of
	// credentialID and tokenID is non-zero.
	principal    string
	credentialID int64
	tokenID      int64

	// ownerUserID is the operator who granted an OAuth token, and 0 for an
	// integration credential. Carried so an audit row can name the human
	// behind a machine request.
	ownerUserID    int64
	ownerUserEmail string

	// clientRowID is the OAuth client, and 0 for an integration credential. It
	// is the per-caller rate bucket's subject; see RateSubject.
	clientRowID int64

	keyPrefix   string
	environment string
	scopes      []string
	siteIDs     []int64
	requestID   string
}

// Principal kinds.
const (
	// PrincipalCredential is an integration credential issued in the console.
	PrincipalCredential = "credential"

	// PrincipalOAuth is an access token granted through the OAuth flow.
	PrincipalOAuth = "oauth"
)

// FromCredential builds the context an authenticated public request runs as.
//
// requestID is the correlation id the response will carry; it is threaded
// through so a service failure can be logged against the request without the
// service ever seeing the request itself.
func FromCredential(identity *models.APICredentialIdentity, requestID string) *TenantContext {
	if identity == nil {
		return nil
	}
	return &TenantContext{
		companyID:    identity.CompanyID,
		principal:    PrincipalCredential,
		credentialID: identity.ID,
		keyPrefix:    identity.KeyPrefix,
		environment:  identity.Environment,
		scopes:       append([]string(nil), identity.Scopes...),
		siteIDs:      append([]int64(nil), identity.SiteIDs...),
		requestID:    requestID,
	}
}

// FromOAuthGrant builds the context an OAuth-authenticated request runs as.
//
// The counterpart to FromCredential, and it is a SECOND CONSTRUCTOR rather than
// a widened first one so that neither path can be reached with half an identity
// from the other. Both take an identity the database produced from a stored
// hash; neither takes anything a request could set.
//
// KeyPrefix carries the CLIENT IDENTIFIER here ("datavase"), where the
// credential path carries the key prefix. Both are non-secret display forms of
// "which credential did this", which is what every log line and audit row
// downstream wants from it -- so those call sites stay unchanged and keep
// working for both.
func FromOAuthGrant(identity *models.OAuthTokenIdentity, requestID string) *TenantContext {
	if identity == nil {
		return nil
	}
	return &TenantContext{
		companyID:      identity.CompanyID,
		principal:      PrincipalOAuth,
		tokenID:        identity.ID,
		ownerUserID:    identity.UserID,
		ownerUserEmail: identity.UserEmail,
		clientRowID:    identity.ClientRowID,
		keyPrefix:      identity.ClientID,
		environment:    identity.Environment,
		scopes:         append([]string(nil), identity.Scopes...),
		siteIDs:        append([]int64(nil), identity.SiteIDs...),
		requestID:      requestID,
	}
}

// CompanyID is the tenant. It is read by the service layer to open a scoped
// transaction and by nothing above it.
func (t *TenantContext) CompanyID() int64 { return t.companyID }

// CredentialID identifies the integration credential for usage accounting and
// idempotency.
//
// ZERO FOR AN OAUTH GRANT, and callers must treat zero as "there is no
// api_credentials row here" rather than as a tenant. Both
// api_usage_daily.credential_id and idempotency_records.credential_id are
// foreign keys into that table, so an OAuth request has nothing to write there
// -- IdempotencyMiddleware already passes through on a zero and that is the
// correct behaviour, not an oversight.
func (t *TenantContext) CredentialID() int64 { return t.credentialID }

// Principal reports which credential class authenticated: PrincipalCredential
// or PrincipalOAuth. For audit records and log lines only; no authorization
// decision reads it.
func (t *TenantContext) Principal() string { return t.principal }

// TokenID identifies the OAuth access token, or 0 for an integration
// credential.
func (t *TenantContext) TokenID() int64 { return t.tokenID }

// OwnerUserID is the operator who granted an OAuth token, or 0.
func (t *TenantContext) OwnerUserID() int64 { return t.ownerUserID }

// OwnerUserEmail is that operator's address, or "". It names the human behind a
// machine request in the audit trail.
func (t *TenantContext) OwnerUserEmail() string { return t.ownerUserEmail }

// RateSubject is the identity a per-caller rate-limit bucket is kept under.
//
// An integration credential is its row id, as it always was. An OAuth grant is
// keyed on the CLIENT AND COMPANY rather than the token, because a token is
// replaced on every refresh -- a per-token bucket would hand a client a fresh
// full allowance whenever it chose to refresh, which is a limit that can be
// reset by the party it limits.
//
// BOTH ROW IDS, NOT THE CLIENT'S NAME. api_rate_buckets.subject_key is capped
// at 64 characters and a client_id may be 64 on its own, so keying on the name
// would be a length the database refuses -- as a 503 on every request, on the
// day somebody configured a long client id.
//
// The two key spaces cannot collide: one is decimal digits, the other is
// prefixed. The same shape the adopt limiter already uses for "session:<id>".
func (t *TenantContext) RateSubject() string {
	if t.principal == PrincipalOAuth {
		return "oauth:" + strconv.FormatInt(t.clientRowID, 10) +
			":" + strconv.FormatInt(t.companyID, 10)
	}
	return strconv.FormatInt(t.credentialID, 10)
}

// KeyPrefix is the non-secret display form, for log lines and audit rows.
func (t *TenantContext) KeyPrefix() string { return t.keyPrefix }

// Environment is the credential's environment (live or test).
func (t *TenantContext) Environment() string { return t.environment }

// RequestID is the correlation id.
func (t *TenantContext) RequestID() string { return t.requestID }

// Scopes is a copy of the credential's expanded scope set.
func (t *TenantContext) Scopes() []string { return append([]string(nil), t.scopes...) }

// HasScope is a membership test. Implication was expanded when the credential
// was issued, so nothing is inferred here.
func (t *TenantContext) HasScope(scope string) bool {
	for _, held := range t.scopes {
		if held == scope {
			return true
		}
	}
	return false
}

// RequireScope is HasScope as a refusal.
func (t *TenantContext) RequireScope(scope string) error {
	if !t.HasScope(scope) {
		return ErrInsufficientScope(scope)
	}
	return nil
}

// RestrictedToSites reports whether the credential names specific sites.
// False means every site in the company, matching the operator grant rule.
func (t *TenantContext) RestrictedToSites() bool { return len(t.siteIDs) > 0 }

// SiteIDs is a copy of the restriction; empty when unrestricted.
func (t *TenantContext) SiteIDs() []int64 { return append([]int64(nil), t.siteIDs...) }

// ReachesSite reports whether the credential may act on a site in its company.
//
// The site's TENANCY is not this function's concern: a site id from another
// company must already have been refused as not-found by the lookup that
// produced it. This answers only the narrower question of the credential's own
// restriction.
func (t *TenantContext) ReachesSite(siteID int64) bool {
	if len(t.siteIDs) == 0 {
		return true
	}
	for _, id := range t.siteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}

// RequireSite is ReachesSite as a refusal.
func (t *TenantContext) RequireSite(siteID int64) error {
	if !t.ReachesSite(siteID) {
		return ErrSiteNotPermitted()
	}
	return nil
}
