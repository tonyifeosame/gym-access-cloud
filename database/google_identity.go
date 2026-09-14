package database

import (
	"database/sql"
	"errors"

	"access-terminal-cloud-api/models"
)

// Sign in with Google (migrations/034_google_identity.sql).
//
// THE PROTOCOL HAS ALREADY HAPPENED by the time anything here runs. The oidc
// package has exchanged the code, verified the signature, the issuer, the
// audience, the expiry and the nonce, and handed over an Identity. This file
// answers one question about it: which operator, if any, is this -- and it
// answers it the same way for a returning user and a first-time one, inside one
// transaction.
//
// ---------------------------------------------------------------------------
// THE RULES
// ---------------------------------------------------------------------------
//
//	1. A subject already linked to a live account IS that account. The email
//	   in today's token is not consulted; Google's subject is the identity.
//
//	2. Otherwise, a VERIFIED email that matches exactly one live account links
//	   that account to the subject. This is the account-linking step, and it
//	   happens once. An unverified address links nothing: Google has not
//	   vouched for it, so it is a claim rather than a fact.
//
//	3. An address whose account is already linked to a DIFFERENT subject is a
//	   conflict, refused. Two Google accounts cannot both be somebody, and the
//	   second to arrive does not get to take over.
//
//	4. No account at all is refused. Google sign-in is a way to prove who you
//	   are to an account that exists; it is not self-service signup, which has
//	   its own route, its own switch and its own audit action.
//
// ACTIVE AND COMPANY CHECKS APPLY EXACTLY AS THEY DO TO A PASSWORD. A disabled
// account or a disabled company cannot be signed in to by any method.
//
// ---------------------------------------------------------------------------
// WHAT HAPPENS TO A PASSWORD SOMEBODY ELSE CHOSE
// ---------------------------------------------------------------------------
//
// must_change_password marks an account whose password was set by an
// administrator or an invitation and is therefore known to a third party. An
// operator who signs in with Google instead of that password has proved control
// of the account without ever using it -- and if the flag were left standing
// the console would demand the current password, which an invited operator
// does not know, on a screen with no way past.
//
// So a Google sign-in on a flagged account REPLACES that password with one
// nobody holds and clears the flag. The third-party-known credential is dead,
// which is the outcome the flag was pushing towards; the operator can set a
// password of their own through the reset route whenever they want one. This
// is stricter than leaving things as they were, not looser.

// Google sign-in refusals. Each maps to one outcome code the browser is sent
// back with; none of them carries anything about the account beyond that.
var (
	ErrGoogleNoAccount       = errors.New("no active operator account is linked to that Google account")
	ErrGoogleEmailUnverified = errors.New("google has not verified that email address")
	ErrGoogleAccountConflict = errors.New("that operator account is linked to a different Google account")
)

// GoogleSignIn is what the verified ID token said.
type GoogleSignIn struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// GoogleSignInResult is the resolved operator plus what changed to get there.
type GoogleSignInResult struct {
	User *models.User
	// Linked is true when THIS sign-in attached the Google account to the
	// operator, so the caller can audit the linking as a distinct event.
	Linked bool
	// PasswordRetired is true when a password chosen by somebody else was
	// replaced -- see the note at the top of this file.
	PasswordRetired bool
}

// AuthenticateGoogle resolves a verified Google identity to an operator.
func AuthenticateGoogle(in GoogleSignIn) (*GoogleSignInResult, error) {
	if in.Subject == "" {
		return nil, ErrGoogleNoAccount
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		userID           int64
		userActive       bool
		companyOK        bool
		mustChange       bool
		linkedSubject    sql.NullString
		result           GoogleSignInResult
		matchedBySubject = true
	)

	// Rule 1: the subject. FOR UPDATE so that two concurrent first sign-ins
	// for the same address serialise on the row rather than both linking.
	err = tx.QueryRow(`
		SELECT u.id, u.active, (c.active AND c.deleted_at IS NULL), u.must_change_password,
		       u.google_subject
		  FROM users u
		  JOIN companies c ON c.id = u.company_id
		 WHERE u.google_subject = $1 AND u.deleted_at IS NULL
		 FOR UPDATE OF u`, in.Subject).
		Scan(&userID, &userActive, &companyOK, &mustChange, &linkedSubject)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		matchedBySubject = false
	case err != nil:
		return nil, err
	}

	if !matchedBySubject {
		// Rules 2 to 4: the address, and only a verified one.
		if !in.EmailVerified {
			return nil, ErrGoogleEmailUnverified
		}
		email := NormalizeEmail(in.Email)
		if ValidateEmail(email) != nil {
			return nil, ErrGoogleNoAccount
		}

		err = tx.QueryRow(`
			SELECT u.id, u.active, (c.active AND c.deleted_at IS NULL), u.must_change_password,
			       u.google_subject
			  FROM users u
			  JOIN companies c ON c.id = u.company_id
			 WHERE u.email = $1 AND u.deleted_at IS NULL
			 FOR UPDATE OF u`, email).
			Scan(&userID, &userActive, &companyOK, &mustChange, &linkedSubject)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGoogleNoAccount
		}
		if err != nil {
			return nil, err
		}
		if linkedSubject.Valid && linkedSubject.String != in.Subject {
			return nil, ErrGoogleAccountConflict
		}
	}

	// Checked BEFORE anything is written, so a disabled account is neither
	// linked nor stamped as having signed in.
	if !userActive || !companyOK {
		return nil, ErrGoogleNoAccount
	}

	if !matchedBySubject {
		if _, err := tx.Exec(`
			UPDATE users
			   SET google_subject = $2, google_linked_at = CURRENT_TIMESTAMP
			 WHERE id = $1`, userID, in.Subject); err != nil {
			return nil, err
		}
		result.Linked = true
	}

	if mustChange {
		retired, err := unredeemablePassword()
		if err != nil {
			return nil, err
		}
		hash, err := hashPassword(retired)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`
			UPDATE users
			   SET password_hash = $2,
			       password_changed_at = CURRENT_TIMESTAMP,
			       must_change_password = FALSE
			 WHERE id = $1`, userID, hash); err != nil {
			return nil, err
		}
		// And every session that password may have opened, as SetUserPassword
		// does: a credential known to a third party is retired along with any
		// use they were making of it. The session this sign-in creates is
		// opened after the transaction commits, so it is unaffected.
		if err := revokeUserSessions(tx, userID, 0); err != nil {
			return nil, err
		}
		result.PasswordRetired = true
	}

	// The same bookkeeping a password login does: the lock is cleared because
	// the person has proved who they are, which is what the lock was waiting
	// for.
	user, err := scanUser(tx.QueryRow(`
		UPDATE users
		   SET failed_login_count = 0,
		       locked_until = NULL,
		       last_login_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		RETURNING `+userColumns, userID))
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	result.User = user
	return &result, nil
}
