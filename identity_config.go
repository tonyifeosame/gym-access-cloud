package main

import (
	"errors"
	"fmt"
	"log"

	"access-terminal-cloud-api/handlers"
	"access-terminal-cloud-api/oidc"
)

// configureIdentityProviders wires Sign in with Google and password-reset
// email delivery from the environment.
//
// BOTH ARE OFF BY DEFAULT and both refuse to start half-configured: a
// deployment that set GOOGLE_CLIENT_ID and forgot the secret, or selected an
// SMTP provider and forgot the host, has made a mistake that is cheap to
// report at deploy time and expensive to discover from the first operator who
// cannot get back in.
//
// BOTH NEED CONSOLE_URL when the console is not served from this origin. The
// Google callback has to send the browser somewhere afterwards, and a reset
// email has to contain an absolute link; a relative one would be relative to
// nothing. Requiring it whenever either feature is on is simpler to reason
// about than guessing from CORS settings, and the development proxy makes the
// value obvious (http://localhost:5173).
//
// Split out of main so the rule can be tested without booting the server.
func configureIdentityProviders() error {
	if err := handlers.ConsoleURLFromEnv(); err != nil {
		return err
	}

	googleCfg, err := oidc.ConfigFromEnv()
	switch {
	case errors.Is(err, oidc.ErrNotConfigured):
		handlers.ConfigureGoogleSignIn(nil)
	case err != nil:
		return err
	default:
		provider, err := oidc.New(*googleCfg)
		if err != nil {
			return err
		}
		if handlers.ConsoleURL() == "" {
			return fmt.Errorf("%s is required when Google sign-in is configured: the "+
				"callback has to send the browser back to the console", handlers.EnvConsoleURL)
		}
		handlers.ConfigureGoogleSignIn(provider)
		log.Printf("Google sign-in: enabled (client %s, callback %s)",
			redactClientID(googleCfg.ClientID), googleCfg.RedirectURL)
	}

	return nil
}

// redactClientID keeps a client id recognisable in a log without printing the
// whole of it. Not a secret, but not something the log needs in full either.
func redactClientID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8] + "…" + id[len(id)-4:]
}
