package bootstrap

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The first platform administrator.
//
// The same problem EnsureFirstOperator solves, one level up. A database built
// from migrations/ has no platform_admins row, so a fresh installation has no
// identity that can create a company -- and GP-01 was that no API created one at
// all. Fixing that with an endpoint would be worse than the disease: a "create
// the first platform administrator" route has to be reachable without
// authentication, and its safety would rest forever on a runtime check that the
// table is still empty, on the surface that can create tenants.
//
// THE SAME SAFETY RULE APPLIES, and it matters more here. This may only act on
// an installation with NO live platform administrator. It cannot overwrite one,
// re-enable one, reset a password or promote anybody. One live administrator and
// it does nothing, which is what stops these variables from becoming a standing
// back door into the highest-privilege identity on the deployment.

// Environment variables for the platform administrator. Deliberately distinct
// from the operator ones: setting BOOTSTRAP_OPERATOR_* and getting a platform
// administrator would be a surprise on the credential class where surprises are
// least affordable.
const (
	EnvPlatformEmail    = "BOOTSTRAP_PLATFORM_EMAIL"
	EnvPlatformPassword = "BOOTSTRAP_PLATFORM_PASSWORD"
	EnvPlatformName     = "BOOTSTRAP_PLATFORM_NAME"
)

// PlatformResult is what the platform bootstrap did, with nothing secret in it.
type PlatformResult struct {
	Outcome Outcome
	Email   string
}

// EnsureFirstPlatformAdmin creates the initial platform administrator when the
// installation has none.
//
// The four cases mirror EnsureFirstOperator exactly, including which of them are
// fatal:
//
//	neither variable set          do nothing, say nothing
//	exactly one set               ERROR: a half-configured bootstrap is a
//	                              mistake, and guessing which half was meant is
//	                              not this function's job
//	both set, no administrator    create it
//	both set, one exists          do nothing, warn that the variables are inert
//
// An invalid address or a password below the policy is an error ONLY in the
// third case. On an established installation the variables are never read for
// their content, so a stale value cannot stop the API starting.
//
// The password is never logged, never returned, and never appears in an error.
func EnsureFirstPlatformAdmin() (PlatformResult, error) {
	email := strings.TrimSpace(os.Getenv(EnvPlatformEmail))
	// Not trimmed: whitespace may be part of a password, and silently altering a
	// credential is worse than accepting an odd one.
	password := os.Getenv(EnvPlatformPassword)

	switch {
	case email == "" && password == "":
		return PlatformResult{Outcome: NotConfigured}, nil
	case password == "":
		return PlatformResult{}, fmt.Errorf("%s is set but %s is not; both are "+
			"required to create the first platform administrator",
			EnvPlatformEmail, EnvPlatformPassword)
	case email == "":
		return PlatformResult{}, fmt.Errorf("%s is set but %s is not; both are "+
			"required to create the first platform administrator",
			EnvPlatformPassword, EnvPlatformEmail)
	}

	fullName := strings.TrimSpace(os.Getenv(EnvPlatformName))
	if fullName == "" {
		fullName = defaultFullName(email)
	}

	admin, err := database.CreateFirstPlatformAdmin(models.NewPlatformAdmin{
		Email:    email,
		FullName: fullName,
		Password: password,
	})

	switch {
	case errors.Is(err, models.ErrOperatorsExist):
		// Not a failure. Saying so is the only way somebody learns the variables
		// can be removed, and leaving a live password in a deployment's
		// environment for no reason is the actual risk.
		log.Printf("WARNING: %s and %s are set but ignored -- this installation "+
			"already has a platform administrator, and the bootstrap never "+
			"overwrites one. Remove both variables from the environment.",
			EnvPlatformEmail, EnvPlatformPassword)
		return PlatformResult{Outcome: SkippedOperatorsExist}, nil

	case errors.Is(err, models.ErrInvalidEmail):
		return PlatformResult{}, fmt.Errorf("%s=%q is not a valid email address",
			EnvPlatformEmail, email)

	case errors.Is(err, models.ErrPasswordTooShort), errors.Is(err, models.ErrPasswordTooLong):
		// The policy, never the value.
		return PlatformResult{}, fmt.Errorf("%s does not meet the password policy: %w",
			EnvPlatformPassword, err)

	case err != nil:
		return PlatformResult{}, fmt.Errorf("creating the first platform administrator: %w", err)
	}

	log.Printf("Bootstrap: created the first platform administrator %s. This "+
		"identity creates companies and issues their first operator; it cannot "+
		"read any tenant's data. Sign in, then remove %s and %s from the "+
		"environment.", admin.Email, EnvPlatformEmail, EnvPlatformPassword)

	return PlatformResult{Outcome: Created, Email: admin.Email}, nil
}
