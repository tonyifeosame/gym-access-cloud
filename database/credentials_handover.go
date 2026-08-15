package database

import (
	"database/sql"
	"errors"
	"time"

	"access-terminal-cloud-api/models"
)

// Operator credential handover (migrations/017_operator_credentials.sql).
//
// PPL-02 and SEC-10: the platform had no way to GIVE somebody a credential and
// no way for them to RECOVER one.
//
// Creating an operator required the caller to choose the password and hand it
// over themselves -- so initial credentials travelled by chat, usually stayed
// unchanged, and the administrator who created the account knew it indefinitely
// with nothing recording that they did. At the other end there was no reset at
// all: a sole OWNER who forgot their password needed database access.
//
// ONE MECHANISM FOR BOTH. An invitation and a reset are the same object: a
// single-use, short-lived, high-entropy token authorising a password set without
// knowing the current one. They differ in what the account looks like beforehand
// and in what the covering message says, not in how they work. Two mechanisms
// would be two token stores, two expiry sweeps and two places for the single-use
// guarantee to be got wrong.
//
// STORED AS A HASH. The plaintext exists in the response that mints it and
// nowhere else, so a database read yields nothing redeemable -- the same
// contract as a site key, a device key and a session token.
//
// WHAT THIS FILE DOES NOT DO: send anything. The platform has no transactional
// email, and pretending otherwise by writing a delivery function that logs would
// be worse than being explicit. The token is returned to the caller, who
// delivers it; the response says so. Wiring real delivery is a change here and
// nowhere else.

// Token lifetimes. An invitation is longer because it is often sent before
// somebody starts; a reset is short because the account is live and the window
// is an attack surface.
const (
	defaultInviteTokenTTL = 7 * 24 * time.Hour
	defaultResetTokenTTL  = 1 * time.Hour
)

// InviteTokenTTL is how long an invitation link stays usable.
func InviteTokenTTL() time.Duration {
	return getEnvSeconds("INVITE_TOKEN_TTL_SECONDS", defaultInviteTokenTTL)
}

// ResetTokenTTL is how long a password reset link stays usable.
func ResetTokenTTL() time.Duration {
	return getEnvSeconds("RESET_TOKEN_TTL_SECONDS", defaultResetTokenTTL)
}

// credentialTokenPrefix marks the value in a log or a secret scanner. Not part
// of the entropy; the 32 random bytes after it are.
const credentialTokenPrefix = "ali_"

// IssueCredentialToken mints an invitation or a reset for one operator.
//
// ANY OUTSTANDING TOKEN FOR THE ACCOUNT IS INVALIDATED FIRST. Two live
// invitations mean two people can each set the password, and the second silently
// overwrites the first -- so an administrator re-sending an invitation because
// the first "did not arrive" would leave both usable. Issuing supersedes.
func IssueCredentialToken(userID int64, purpose string, issuedBy int64,
	issuedByEmail string) (*models.CredentialToken, error) {

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	out, err := issueCredentialTokenTx(tx, userID, purpose, issuedBy, issuedByEmail)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// issueCredentialTokenTx is the body of the above, inside a caller's
// transaction.
//
// Split out for CreateInvitedUser, where the account and its invitation must
// commit together: an operator row created without the invitation that reaches
// its owner is an account nobody can ever sign in as and nobody knows to delete.
func issueCredentialTokenTx(tx *sql.Tx, userID int64, purpose string,
	issuedBy int64, issuedByEmail string) (*models.CredentialToken, error) {

	ttl := ResetTokenTTL()
	if purpose == models.TokenPurposeInvite {
		ttl = InviteTokenTTL()
	}

	token, err := generateSecret(credentialTokenPrefix)
	if err != nil {
		return nil, err
	}

	// Supersede. Marked redeemed rather than deleted so the trail of who issued
	// what survives -- an account with a suspicious number of resets is worth
	// being able to see.
	if _, err := tx.Exec(`
		UPDATE user_credential_tokens
		   SET redeemed_at = CURRENT_TIMESTAMP
		 WHERE user_id = $1 AND redeemed_at IS NULL`, userID); err != nil {
		return nil, err
	}

	var out models.CredentialToken
	err = tx.QueryRow(`
		INSERT INTO user_credential_tokens
		    (user_id, token_hash, purpose, issued_by_user_id, issued_by_email,
		     expires_at)
		VALUES ($1, $2, $3, NULLIF($4, 0)::bigint, NULLIF($5, ''),
		        CURRENT_TIMESTAMP + make_interval(secs => $6::float8))
		RETURNING purpose, expires_at`,
		userID, hashSessionSecret(token), purpose, issuedBy, issuedByEmail,
		ttl.Seconds()).
		Scan(&out.Purpose, &out.ExpiresAt)
	if err != nil {
		return nil, err
	}

	out.Token = token
	out.ShownOnce = true
	return &out, nil
}

// unredeemablePassword returns a password nobody holds.
//
// users.password_hash is NOT NULL, so an invited account still needs one -- and
// the only safe value is one that never existed in readable form anywhere. 32
// bytes from crypto/rand, hashed, and the plaintext discarded when this function
// returns. The account cannot be signed in to until its invitation is redeemed,
// which is the property the whole invitation flow rests on.
//
// NOT a placeholder constant and not derived from the address. Either would mean
// every invited account on every deployment shares one guessable credential
// during the window before it is redeemed.
func unredeemablePassword() (string, error) {
	// generateSecret already returns prefix + 64 hex characters from crypto/rand,
	// which is inside the password policy's bounds at both ends.
	return generateSecret("unset_")
}

// CreateInvitedUser creates an operator that nobody -- including the
// administrator creating it -- holds a credential for, and mints the invitation
// that lets its owner set one.
//
// PPL-02. The old path required the CALLER to choose the new operator's
// password and hand it over themselves, so initial credentials travelled by chat
// in plain text, usually stayed unchanged, and the administrator who created the
// account knew it indefinitely with nothing recording that they did.
//
// ONE TRANSACTION for the account, the flag and the token. A partial commit here
// has no good outcome: an account without its invitation is unreachable by its
// owner, and a token without its account is a link that resolves to nothing.
func CreateInvitedUser(companyID int64, in models.NewUser, issuedBy int64,
	issuedByEmail string) (*models.User, *models.CredentialToken, error) {

	password, err := unredeemablePassword()
	if err != nil {
		return nil, nil, err
	}
	in.Password = password

	tx, err := DB.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	// createUser rather than a second INSERT, so an invited account gets the same
	// address normalisation, role check and name policy as every other one.
	user, err := createUser(tx, companyID, in)
	if err != nil {
		return nil, nil, err
	}

	if _, err := tx.Exec(
		`UPDATE users SET must_change_password = TRUE WHERE id = $1`, user.ID); err != nil {
		return nil, nil, err
	}

	token, err := issueCredentialTokenTx(tx, user.ID, models.TokenPurposeInvite,
		issuedBy, issuedByEmail)
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return user, token, nil
}

// RedeemCredentialToken sets a password using an invitation or reset link.
//
// THE TOKEN IS THE AUTHORISATION. There is no current password, which is the
// entire point -- somebody redeeming an invitation has never had one, and
// somebody redeeming a reset has forgotten theirs.
//
// SINGLE USE, ENFORCED BY THE UPDATE'S OWN PREDICATE rather than by a
// check-then-write. Two concurrent redemptions cannot both match
// `redeemed_at IS NULL`, so exactly one wins and the other is told the link has
// been used.
//
// EVERY SESSION FOR THE ACCOUNT IS REVOKED, including any the redeemer holds.
// Somebody setting a password through a reset link usually believes the old one
// was known to somebody else, and leaving the sessions that password opened
// alive would make the change cosmetic.
func RedeemCredentialToken(token, newPassword, ip string) (*models.User, error) {
	if err := ValidatePassword(newPassword); err != nil {
		return nil, err
	}

	hash, err := hashPassword(newPassword)
	if err != nil {
		return nil, err
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		tokenID     int64
		userID      int64
		purpose     string
		expired     bool
		redeemed    bool
		hasSignedIn bool
	)

	err = tx.QueryRow(`
		SELECT t.id, t.user_id, t.purpose,
		       t.expires_at <= CURRENT_TIMESTAMP AS expired,
		       t.redeemed_at IS NOT NULL AS redeemed,
		       u.last_login_at IS NOT NULL AS has_signed_in
		  FROM user_credential_tokens t
		  JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1
		   AND u.deleted_at IS NULL
		   AND u.active`, hashSessionSecret(token)).
		Scan(&tokenID, &userID, &purpose, &expired, &redeemed, &hasSignedIn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrTokenNotFound
	}
	if err != nil {
		return nil, err
	}

	if redeemed {
		return nil, models.ErrTokenRedeemed
	}
	if expired {
		return nil, models.ErrTokenExpired
	}

	// An INVITE is for an account that has never signed in. Redeeming one
	// against a live account would be a way to take over somebody's session
	// history using a link that was issued before they ever used it.
	if purpose == models.TokenPurposeInvite && hasSignedIn {
		return nil, models.ErrTokenRedeemed
	}

	result, err := tx.Exec(`
		UPDATE user_credential_tokens
		   SET redeemed_at = CURRENT_TIMESTAMP, redeemed_ip = NULLIF($2, '')
		 WHERE id = $1 AND redeemed_at IS NULL`, tokenID, ip)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		// Somebody else redeemed it between the read and the write.
		return nil, models.ErrTokenRedeemed
	}

	var user models.User
	err = tx.QueryRow(`
		UPDATE users
		   SET password_hash = $2,
		       password_changed_at = CURRENT_TIMESTAMP,
		       must_change_password = FALSE,
		       failed_login_count = 0,
		       locked_until = NULL
		 WHERE id = $1
		RETURNING id, public_id, company_id, email, full_name, role, active,
		          last_login_at, created_at, updated_at`,
		userID, hash).
		Scan(&user.ID, &user.PublicID, &user.CompanyID, &user.Email, &user.FullName,
			&user.Role, &user.Active, &user.LastLoginAt, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}

	// Spare none: pass 0. See the note on this function.
	if err := revokeUserSessions(tx, userID, 0); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &user, nil
}

// FindOperatorForReset resolves an account by email for a password reset.
//
// RETURNS THE SAME THING FOR AN UNKNOWN ADDRESS AS FOR A KNOWN ONE, from the
// caller's point of view: it reports (0, nil) rather than an error. The handler
// then answers 202 either way.
//
// That is not politeness. A reset endpoint that says "no such account" is an
// enumeration oracle for every email address somebody wants to test, and it is
// unauthenticated by necessity.
func FindOperatorForReset(email string) (int64, error) {
	var userID int64
	err := DB.QueryRow(`
		SELECT id FROM users
		 WHERE email = $1 AND deleted_at IS NULL AND active`,
		NormalizeEmail(email)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return userID, nil
}

// SetMustChangePassword flags an account whose password was chosen by somebody
// else.
func SetMustChangePassword(userID int64, required bool) error {
	_, err := DB.Exec(
		`UPDATE users SET must_change_password = $2 WHERE id = $1`, userID, required)
	return err
}

// PurgeExpiredCredentialTokens removes spent and expired handover tokens.
//
// Retained for a window after they die, like sessions, because "who issued a
// reset for this account and when" is a question worth being able to answer
// after an incident.
func PurgeExpiredCredentialTokens(retentionDays int) (int64, error) {
	if retentionDays < 0 {
		retentionDays = 0
	}
	result, err := DB.Exec(`
		DELETE FROM user_credential_tokens
		 WHERE (expires_at < CURRENT_TIMESTAMP - make_interval(days => $1))
		    OR (redeemed_at IS NOT NULL
		        AND redeemed_at < CURRENT_TIMESTAMP - make_interval(days => $1))`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
