// Package bootstrap creates the first operator account on an empty system.
//
// WHY THIS EXISTS. A database built from migrations/ has no operator accounts,
// so a fresh deployment has no way into the console. That is deliberate -- the
// alternative is shipping known credentials, which this project has done once
// and removed. Something has to create the first account, and on a managed
// platform there is no dependable shell to run a tool from.
//
// WHY IT IS NOT AN ENDPOINT. A "create the first owner" route is a permanent
// hole: it has to be reachable without authentication, so its safety rests
// entirely on a runtime check that the system is still empty, exposed to the
// public internet forever after. Configuration read at startup is not reachable
// by anyone who cannot already change the deployment's environment -- which is
// a higher bar than any endpoint can offer.
//
// THE SAFETY RULE. This may only act on an EMPTY system. It cannot overwrite an
// account, re-enable one, reset a password or change a role. One live operator
// and it does nothing. That is what keeps a stale variable from mattering on a
// running deployment, and what stops these variables from becoming a standing
// back door.
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

// Environment variables. The first two are required together; the rest are
// optional and exist so a deployment does not have to accept the defaults.
const (
	EnvEmail    = "BOOTSTRAP_OPERATOR_EMAIL"
	EnvPassword = "BOOTSTRAP_OPERATOR_PASSWORD"
	EnvName     = "BOOTSTRAP_OPERATOR_NAME"
	EnvCompany  = "BOOTSTRAP_OPERATOR_COMPANY"
)

// Outcome describes what the bootstrap did, for the caller to log or assert on.
type Outcome int

const (
	// NotConfigured: neither variable is set. The normal state of a deployment
	// whose owner already exists, and not worth a line of log output.
	NotConfigured Outcome = iota
	// SkippedOperatorsExist: configured, but the system already has an operator.
	SkippedOperatorsExist
	// Created: an OWNER was created.
	Created
)

// Result is what happened, without anything secret in it.
type Result struct {
	Outcome Outcome
	Email   string
	Company string
}

// EnsureFirstOperator creates the initial OWNER when the system has none.
//
// The four cases, which differ in whether they are fatal:
//
//	neither variable set            do nothing, say nothing
//	exactly one set                 ERROR: a half-configured bootstrap is a
//	                                mistake, and guessing which half was meant
//	                                is not this function's job
//	both set, no live operators     create the OWNER
//	both set, an operator exists    do nothing, warn that the variables are
//	                                now inert and should be removed
//
// An invalid address, a password below the policy or an unknown company slug is
// an error ONLY in the third case -- the one where the bootstrap would actually
// run. On an established system the variables are never read for their content
// at all, so a typo in a value nobody is using can never stop the API starting.
//
// The password is never logged, never returned, and never appears in an error.
func EnsureFirstOperator() (Result, error) {
	email := strings.TrimSpace(os.Getenv(EnvEmail))
	// Not trimmed: leading or trailing whitespace may be part of a password, and
	// silently altering a credential is worse than accepting an odd one.
	password := os.Getenv(EnvPassword)

	switch {
	case email == "" && password == "":
		return Result{Outcome: NotConfigured}, nil
	case password == "":
		return Result{}, fmt.Errorf("%s is set but %s is not; both are required to "+
			"create the first operator", EnvEmail, EnvPassword)
	case email == "":
		return Result{}, fmt.Errorf("%s is set but %s is not; both are required to "+
			"create the first operator", EnvPassword, EnvEmail)
	}

	companySlug := strings.TrimSpace(os.Getenv(EnvCompany))

	fullName := strings.TrimSpace(os.Getenv(EnvName))
	if fullName == "" {
		fullName = defaultFullName(email)
	}

	user, err := database.CreateFirstOperator(companySlug, models.NewUser{
		Email:    email,
		FullName: fullName,
		Password: password,
		Role:     models.RoleOwner,
	})

	switch {
	case errors.Is(err, models.ErrOperatorsExist):
		// Not a failure. The variables are inert from here on, and saying so is
		// the only way an operator learns they can be removed -- leaving a live
		// password in a deployment's environment for no reason is the risk.
		log.Printf("WARNING: %s and %s are set but ignored -- this system already "+
			"has an operator account, and the bootstrap never overwrites one. "+
			"Remove both variables from the environment.", EnvEmail, EnvPassword)
		return Result{Outcome: SkippedOperatorsExist}, nil

	case errors.Is(err, models.ErrCompanyNotFound):
		if companySlug != "" {
			return Result{}, fmt.Errorf("%s=%q does not match any company",
				EnvCompany, companySlug)
		}
		return Result{}, errors.New("no company exists to own the first operator")

	case errors.Is(err, models.ErrInvalidEmail):
		return Result{}, fmt.Errorf("%s=%q is not a valid email address", EnvEmail, email)

	case errors.Is(err, models.ErrPasswordTooShort), errors.Is(err, models.ErrPasswordTooLong):
		// The policy, never the value.
		return Result{}, fmt.Errorf("%s does not meet the password policy: %w", EnvPassword, err)

	case err != nil:
		return Result{}, fmt.Errorf("creating the first operator: %w", err)
	}

	log.Printf("Bootstrap: created the first operator %s (role %s, company %s). "+
		"Sign in, then remove %s and %s from the environment.",
		user.Email, user.Role, companySlugOrDefault(companySlug), EnvEmail, EnvPassword)

	return Result{Outcome: Created, Email: user.Email, Company: companySlug}, nil
}

// defaultFullName derives a display name when none was configured.
//
// users.full_name is NOT NULL with a non-blank CHECK, so something has to be
// written. The local part of the address is a better placeholder than a
// constant: it is recognisable in an audit line, and the owner can change it
// once they are signed in.
func defaultFullName(email string) string {
	if at := strings.LastIndex(email, "@"); at > 0 {
		return email[:at]
	}
	return "Owner"
}

func companySlugOrDefault(slug string) string {
	if slug == "" {
		return "the oldest company"
	}
	return slug
}
