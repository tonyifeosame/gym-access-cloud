package service

import (
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
type TenantContext struct {
	companyID    int64
	credentialID int64
	keyPrefix    string
	environment  string
	scopes       []string
	siteIDs      []int64
	requestID    string
}

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
		credentialID: identity.ID,
		keyPrefix:    identity.KeyPrefix,
		environment:  identity.Environment,
		scopes:       append([]string(nil), identity.Scopes...),
		siteIDs:      append([]int64(nil), identity.SiteIDs...),
		requestID:    requestID,
	}
}

// CompanyID is the tenant. It is read by the service layer to open a scoped
// transaction and by nothing above it.
func (t *TenantContext) CompanyID() int64 { return t.companyID }

// CredentialID identifies the credential for usage accounting and idempotency.
func (t *TenantContext) CredentialID() int64 { return t.credentialID }

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
