package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// OAuth client configuration (037).
//
// ---------------------------------------------------------------------------
// CONFIGURATION, NOT REGISTRATION, AND NOT SOURCE
// ---------------------------------------------------------------------------
//
// There is no client-registration endpoint, on purpose: one would let anybody
// who can reach the server create a client with a redirect URI of their
// choosing, and the allow-listed redirect URI is the whole of the defence an
// authorization-code flow has. So clients come from the environment, are
// written at startup, and are idempotent -- a deployment that changes nothing
// rewrites the same row.
//
// NO SECRET IS IN THIS FILE OR ANY OTHER. The client secret is read from the
// environment and hashed before it reaches the database; it is never logged,
// never returned by any endpoint, and there is nothing to read back.
//
// ---------------------------------------------------------------------------
// OFF UNLESS CONFIGURED, FATAL WHEN HALF-CONFIGURED
// ---------------------------------------------------------------------------
//
// A deployment that names no redirect URI has no OAuth client, and its
// authorize endpoint answers "that application is not connected here" -- which
// is the correct answer. A deployment that set a client id and forgot the
// redirect URI has made a mistake that is cheap to report at deploy time and
// expensive to discover from the first customer who tries to connect, so it
// refuses to start. That is the same posture configureIdentityProviders takes.

// Environment variables. One integration today; the shape generalises by
// adding a second block rather than by inventing a syntax for a list, because a
// list in one variable is how a redirect URI eventually gets mis-parsed.
const (
	// EnvDatavaseClientID names the client. Optional: it defaults to
	// "datavase", which is what the integration's own documentation says.
	EnvDatavaseClientID = "OAUTH_DATAVASE_CLIENT_ID"

	// EnvDatavaseRedirectURIs is the comma-separated allow-list, and it is what
	// TURNS THE INTEGRATION ON. Every entry must be https, or http on a
	// loopback host for local development.
	EnvDatavaseRedirectURIs = "OAUTH_DATAVASE_REDIRECT_URIS"

	// EnvDatavaseClientSecret makes it a confidential client. Optional: PKCE is
	// mandatory either way, so a public client is safe -- but a client that
	// runs on a server it controls should prove itself as well, and this is how.
	EnvDatavaseClientSecret = "OAUTH_DATAVASE_CLIENT_SECRET"

	// EnvDatavaseScopes bounds what the client may ever be granted,
	// space-separated. Optional, defaulting to the Phase 3A set.
	EnvDatavaseScopes = "OAUTH_DATAVASE_SCOPES"

	// EnvDatavaseName is what the consent screen calls it.
	EnvDatavaseName = "OAUTH_DATAVASE_NAME"
)

const (
	defaultDatavaseClientID = "datavase"
	defaultDatavaseName     = "Datavase"
)

// defaultDatavaseScopes is the Phase 3A ceiling: sites, read and write.
//
// DELIBERATELY NOT "everything the registry defines". A client's configured
// maximum is the last line of defence when a consent screen is misread, and
// the honest maximum for an integration that manages locations is the
// locations. Widening it later is a configuration change with a decision behind
// it, which is the right amount of friction.
var defaultDatavaseScopes = []string{models.ScopeSitesRead, models.ScopeSitesWrite}

// errOAuthNotConfigured reports that this deployment has no OAuth client.
var errOAuthNotConfigured = errors.New("no oauth client is configured")

// configureOAuthClients writes the configured clients at startup.
//
// Split out of main so the rule can be tested without booting the server, in
// the same way configureIdentityProviders is.
func configureOAuthClients() error {
	cfg, err := datavaseClientFromEnv()
	switch {
	case errors.Is(err, errOAuthNotConfigured):
		log.Printf("OAuth: no client configured (set %s to connect an integration); "+
			"the authorization endpoints answer but recognise no application",
			EnvDatavaseRedirectURIs)
		return nil
	case err != nil:
		return err
	}

	client, err := database.UpsertOAuthClient(*cfg)
	if err != nil {
		return err
	}

	kind := "public (PKCE only)"
	if client.Confidential {
		kind = "confidential"
	}
	// The redirect URIs and scopes ARE logged, in full. Neither is a secret,
	// and "which address will this server hand an authorization code to" is
	// precisely the question somebody reviewing a deployment needs answered
	// without opening a database.
	log.Printf("OAuth: client %q (%s) configured, %s, scopes=[%s], redirect_uris=[%s]",
		client.ClientID, client.Name, kind,
		strings.Join(client.Scopes, " "), strings.Join(client.RedirectURIs, ", "))
	return nil
}

// datavaseClientFromEnv reads the client from the environment.
func datavaseClientFromEnv() (*database.OAuthClientConfig, error) {
	raw := strings.TrimSpace(os.Getenv(EnvDatavaseRedirectURIs))
	if raw == "" {
		// Nothing configured. A client id or a secret WITHOUT a redirect URI is
		// a half-configured deployment and is refused, because the variable
		// that was set says somebody meant to turn this on.
		if strings.TrimSpace(os.Getenv(EnvDatavaseClientID)) != "" ||
			strings.TrimSpace(os.Getenv(EnvDatavaseClientSecret)) != "" {
			return nil, fmt.Errorf("%s is required when %s or %s is set: an OAuth client "+
				"with no allow-listed redirect URI cannot complete a flow",
				EnvDatavaseRedirectURIs, EnvDatavaseClientID, EnvDatavaseClientSecret)
		}
		return nil, errOAuthNotConfigured
	}

	var redirects []string
	for _, part := range strings.Split(raw, ",") {
		uri := strings.TrimSpace(part)
		if uri != "" {
			redirects = append(redirects, uri)
		}
	}
	if len(redirects) == 0 {
		return nil, fmt.Errorf("%s is set but names no usable redirect URI", EnvDatavaseRedirectURIs)
	}

	scopes := defaultDatavaseScopes
	if named := strings.Fields(os.Getenv(EnvDatavaseScopes)); len(named) > 0 {
		for _, scope := range named {
			if !models.KnownScope(scope) {
				return nil, fmt.Errorf("%s names %q, which is not a scope this build serves (%s)",
					EnvDatavaseScopes, scope, strings.Join(models.AllScopes(), ", "))
			}
			// Refused here as well as at the store, so the message an operator
			// reads at boot names the variable they set rather than an
			// internal one. See models.OAuthGrantableScopes.
			if !models.GrantableByOAuth(scope) {
				return nil, fmt.Errorf("%s names %q, which is not available to an OAuth "+
					"client in this version (available: %s)",
					EnvDatavaseScopes, scope, strings.Join(models.GrantableOAuthScopes(), ", "))
			}
		}
		scopes = named
	}

	clientID := strings.TrimSpace(os.Getenv(EnvDatavaseClientID))
	if clientID == "" {
		clientID = defaultDatavaseClientID
	}
	name := strings.TrimSpace(os.Getenv(EnvDatavaseName))
	if name == "" {
		name = defaultDatavaseName
	}

	return &database.OAuthClientConfig{
		ClientID:     clientID,
		Name:         name,
		Secret:       os.Getenv(EnvDatavaseClientSecret),
		RedirectURIs: redirects,
		Scopes:       scopes,
	}, nil
}
