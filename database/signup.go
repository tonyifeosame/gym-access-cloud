package database

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"access-terminal-cloud-api/models"
)

// Self-service signup: a customer with nothing becomes a company with an owner.
//
// WHAT THIS CLOSES. Onboarding previously required a platform administrator --
// create the company, then create its first operator, then hand over an
// invitation link the platform cannot actually deliver. That is a vendor
// process, not a product, and every step of it demands something a new customer
// has never heard of: a company id, a slug, a claim code, a provisioning key.
// The four things a customer DOES have are their name, their company's name, an
// address and a password, and this function turns exactly those into a working
// tenant.
//
// WHY IT IS NOT A NEW IDENTITY OR A NEW AUTH PATH. Nothing here is a second way
// to be authenticated. It creates the same rows CreateCompany, CreateSite and
// CreateUser create, through the same functions, with the same validation and
// the same bcrypt policy -- the handler then opens an ORDINARY operator session
// with CreateSession. A signup that minted its own kind of session, or its own
// kind of account, would be a parallel authentication system to keep in step
// with the real one for ever.
//
// WHAT IT DELIBERATELY CANNOT REACH. Nothing in the platform-administration
// surface. This creates ONE company and puts ONE owner in it; it cannot list
// tenants, cannot touch an existing one, and cannot create a platform
// administrator. That is enforced by there being no such statement in this file
// rather than by a check -- the same construction database/platform.go uses to
// keep a support credential away from a customer's roster.
//
// ONE TRANSACTION, THREE ROWS. A company with no site is a tenant whose console
// shows nothing and whose first terminal has nowhere to go; a company with no
// operator is one nobody can sign into. Either half-built state would need SQL
// against production to repair, which is precisely what this exists to stop
// needing. So all three commit together or none of them do.
//
// THE SITE'S PROVISIONING KEY IS GENERATED AND THROWN AWAY. createSite mints one
// because a site without a credential could never register hardware; only its
// SHA-256 is stored, and the plaintext is discarded here rather than returned.
// An unauthenticated endpoint must not be able to hand a browser the secret that
// registers door hardware, and the safest way to guarantee that is for the
// return type to have nowhere to put it. The owner rotates the key from the
// console when they are ready to install a terminal, which is an authenticated,
// ADMIN-gated, audited action.

// The first site every new company gets.
//
// "Main Site" rather than the company's name: migration 002 already uses that
// exact label for the site it adopts pre-tenancy rows into, and an owner renames
// it in one click. A site called after the company reads as a duplicate of the
// company the moment a second location is added.
const signupSiteName = "Main Site"

// Column bounds, checked here rather than left to the schema.
//
// companies.name and users.full_name are both VARCHAR(150). Postgres answers an
// over-length value with error 22001, which a handler cannot tell apart from the
// database being unwell -- the same reason CreateUser validates the role instead
// of relying on the CHECK constraint.
const (
	maxCompanyNameLength = 150
	maxFullNameLength    = 150
)

// slugAttempts bounds the search for a free slug.
//
// The candidates are the derived slug, then a handful of numbered variants, then
// randomly suffixed ones. The random tail is what makes exhaustion effectively
// impossible; the bound is here so a pathological case fails loudly instead of
// looping against the database for ever.
const slugAttempts = 12

// Signup errors. Handlers map these to status codes; anything else is a fault.
var (
	ErrFullNameRequired = errors.New("your name is required")

	ErrCompanyNameTooLong = fmt.Errorf(
		"company name must be %d characters or fewer", maxCompanyNameLength)
	ErrFullNameTooLong = fmt.Errorf(
		"your name must be %d characters or fewer", maxFullNameLength)

	// ErrEmailInUse is a 409 and, unavoidably, tells the caller that an address
	// is taken.
	//
	// Login refuses to disclose that and this cannot: a signup form that
	// accepted a duplicate address would create an account nobody can log into,
	// because users.email is unique GLOBALLY -- there is no tenant selector on
	// the login form, so an address has to resolve to exactly one account for
	// authentication to be decidable at all. The mitigation is the rate limiter
	// this route shares with login, not a vaguer message.
	ErrEmailInUse = errors.New("that email address is already in use")

	// ErrCompanyNameUnavailable means no slug could be derived. Reachable only
	// if every candidate collided, which needs a deliberate attempt.
	ErrCompanyNameUnavailable = errors.New(
		"a company identifier could not be derived from that name")
)

// NewSignup is what a registration form supplies, and the whole of it.
//
// FOUR FIELDS. No role, no company id, no slug, no site: the role is forced to
// OWNER, the company and its site are created here, and the slug is derived.
// Every field added to this struct is one more thing somebody has to be told
// before they can start using the product.
type NewSignup struct {
	FullName    string
	CompanyName string
	Email       string
	Password    string
}

// SignupResult is the tenant that now exists.
//
// NOTE WHAT IS ABSENT: the site's provisioning key. See the file comment -- the
// type has nowhere to put it, which is a stronger guarantee than a handler
// remembering not to serialise one.
type SignupResult struct {
	User    *models.User
	Company *models.PlatformCompany
	Site    *models.ConsoleSite
}

// RegisterCompany creates a company, its first site and its OWNER, atomically.
//
// The role is forced rather than accepted. The first account in a company has to
// be able to create the others and there is nobody to grant it anything --
// exactly the reasoning CreateFirstOperator applies to the bootstrap path.
//
// must_change_password is NOT set, and that is the difference between this and
// every other way an account is created. The flag means "the credential in use
// was chosen by somebody other than its owner", which is true of an invitation
// and an administrative reset and false here: the person who chose this password
// is the person typing it.
func RegisterCompany(in NewSignup) (*SignupResult, error) {
	fullName := strings.TrimSpace(in.FullName)
	if fullName == "" {
		return nil, ErrFullNameRequired
	}
	if utf8.RuneCountInString(fullName) > maxFullNameLength {
		return nil, ErrFullNameTooLong
	}

	companyName := strings.TrimSpace(in.CompanyName)
	if companyName == "" {
		return nil, ErrCompanyNameRequired
	}
	if utf8.RuneCountInString(companyName) > maxCompanyNameLength {
		return nil, ErrCompanyNameTooLong
	}

	email := NormalizeEmail(in.Email)
	if err := ValidateEmail(email); err != nil {
		return nil, err
	}
	if err := ValidatePassword(in.Password); err != nil {
		return nil, err
	}

	// Refused BEFORE anything is created, so the common case -- somebody who
	// already has an account trying to sign up again -- does not consume a slug
	// and roll back a transaction. The insert below is still the authority: two
	// concurrent signups on one address both pass this check and the unique
	// index decides, which is why the violation is mapped rather than assumed
	// away.
	if taken, err := emailInUse(email); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrEmailInUse
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	company, companyID, err := createCompanyForSignup(tx, companyName, email)
	if err != nil {
		return nil, err
	}

	// The credential is DISCARDED. See the file comment: an unauthenticated
	// caller must not be handed the secret that registers door hardware, and the
	// site is perfectly usable without anybody ever seeing it -- the owner
	// rotates a fresh one from the console when there is hardware to install.
	site, _, err := createSite(tx, companyID, NewSite{Name: signupSiteName})
	if err != nil {
		return nil, err
	}

	user, err := createUser(tx, companyID, models.NewUser{
		Email:    email,
		FullName: fullName,
		Password: in.Password,
		Role:     models.RoleOwner,
	})
	if IsUniqueViolation(err) {
		// The race the pre-check above cannot close. Everything rolls back, so
		// the company and site created moments ago do not survive as an orphan
		// tenant nobody can sign into.
		return nil, ErrEmailInUse
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &SignupResult{User: user, Company: company, Site: site}, nil
}

// emailInUse reports whether a live account already holds an address.
//
// Soft-deleted rows are excluded, matching the partial unique index: a deleted
// account's address is free for reuse, so it is not the account this address
// refers to any more.
func emailInUse(email string) (bool, error) {
	var exists bool
	err := DB.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM users WHERE email = $1 AND deleted_at IS NULL)`,
		email).Scan(&exists)
	return exists, err
}

// createCompanyForSignup inserts the tenant, choosing a slug that is free.
//
// THE CUSTOMER NEVER SEES THIS HAPPEN, which is the entire point. A slug is a
// platform concept -- it appears in URLs and in the bootstrap variable -- and
// asking somebody signing up to invent a unique one, or refusing their signup
// because another customer is also called "Harbour Freight", would be exposing
// an implementation detail as a customer-facing failure. So a taken slug is
// resolved by trying the next candidate rather than by reporting a conflict.
//
// The company NAME is left exactly as typed. Two companies may share one; only
// the derived identifier has to be unique.
func createCompanyForSignup(tx queryRower, companyName, email string) (
	*models.PlatformCompany, int64, error) {

	base := signupSlugBase(companyName, email)

	for attempt := 0; attempt < slugAttempts; attempt++ {
		slug, err := slugCandidate(base, attempt)
		if err != nil {
			return nil, 0, err
		}

		company, companyID, err := createCompany(tx, NewCompany{
			Name: companyName,
			Slug: slug,
			// The address they signed up with. It is the only contact this
			// tenant has, and a company row with no way to reach anybody is how
			// support tickets become unanswerable.
			ContactEmail: email,
		})
		if errors.Is(err, ErrCompanySlugTaken) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		return company, companyID, nil
	}

	return nil, 0, ErrCompanyNameUnavailable
}

// signupSlugBase derives the stem of a company's identifier.
//
// Three sources in order of how much they mean to the customer: the company
// name, then the local part of the address they signed up with, then a
// constant. The fallbacks matter more than they look -- a company written
// entirely in a non-Latin script normalises to nothing, and refusing that
// signup would be refusing a customer over a naming rule they never saw.
func signupSlugBase(companyName, email string) string {
	base := models.NormalizeSlug(companyName)

	if models.ValidateSlug(base) != nil {
		if at := strings.LastIndex(email, "@"); at > 0 {
			base = models.NormalizeSlug(email[:at])
		}
	}
	if models.ValidateSlug(base) != nil {
		base = "company"
	}

	// Room for the longest suffix a candidate can carry, inside the 64-character
	// column. Trimmed of hyphens afterwards so a truncation cannot leave a slug
	// ending in one, which ValidateSlug refuses.
	const maxBase = 48
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	return base
}

// slugCandidate returns the nth identifier to try for a base.
//
// Numbered first, because "harbour-freight-2" is recognisable to the person who
// typed "Harbour Freight" and a random tail is not. Random after that: a purely
// numbered sequence turns a popular company name into an ever-lengthening scan,
// and the tail also stops a slug from disclosing how many companies of that name
// have signed up.
func slugCandidate(base string, attempt int) (string, error) {
	switch {
	case attempt == 0:
		return base, nil
	case attempt < 5:
		return fmt.Sprintf("%s-%d", base, attempt+1), nil
	}

	raw := make([]byte, 3)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("deriving a company identifier: %w", err)
	}
	return base + "-" + hex.EncodeToString(raw), nil
}
