package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"access-terminal-cloud-api/models"
)

// Browser sessions for operators (migrations/008_operator_accounts.sql).
//
// A session is an opaque 256-bit token from crypto/rand, stored only as its
// SHA-256 hash. This is the same construction as a device credential and for
// the same reason: the secret is machine-generated and unguessable, so it needs
// no slow KDF, but it IS verified on every single dashboard request and must
// stay a single index probe. Passwords are the opposite case and use bcrypt --
// see users.go.
//
// WHY NOT A SIGNED TOKEN. Revocation has to be immediate. Logging out,
// disabling an account, changing a role or changing a password must all take
// effect on the next request, and a self-contained token stays valid until it
// expires no matter what the database says. This system already has one
// credential class it cannot revoke without re-keying a whole site; repeating
// that for the humans would be the wrong lesson to learn from it.
//
// TWO EXPIRIES. idle_expires_at slides forward as the session is used, so an
// operator working through the day is not logged out mid-task.
// absolute_expires_at never moves, so a browser tab polling in the background
// cannot keep a session alive indefinitely. The sliding update clamps to the
// absolute, which the schema also enforces.

// sessionTokenPrefix marks the credential in a log or a secret scanner, the way
// atd_ does for device keys. The prefix is not secret and not part of the
// entropy; the 32 random bytes after it are.
const sessionTokenPrefix = "ats_"

// Session lifetimes. Both are overridable, and both are bounds rather than
// guesses: 12 hours of inactivity covers a working day with a lunch break, and
// a week is long enough that a daily user rarely re-authenticates while being
// short enough that a forgotten laptop is not a standing invitation.
const (
	defaultSessionIdleTimeout     = 12 * time.Hour
	defaultSessionAbsoluteTimeout = 7 * 24 * time.Hour

	// sessionTouchInterval throttles the sliding-expiry write. Without it every
	// authenticated request would UPDATE the session row, turning a dashboard
	// that polls into a write-heavy workload for no benefit.
	sessionTouchInterval = time.Minute
)

// SessionIdleTimeout is how long a session survives without being used.
func SessionIdleTimeout() time.Duration {
	return getEnvSeconds("SESSION_IDLE_TIMEOUT_SECONDS", defaultSessionIdleTimeout)
}

// SessionAbsoluteTimeout is the longest a session may live, however active.
func SessionAbsoluteTimeout() time.Duration {
	return getEnvSeconds("SESSION_ABSOLUTE_TIMEOUT_SECONDS", defaultSessionAbsoluteTimeout)
}

// hashSessionSecret returns the stored form of a session token or CSRF token.
func hashSessionSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// generateSecret returns 32 bytes of randomness as hex, with an optional prefix.
func generateSecret(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating session secret: %w", err)
	}
	return prefix + hex.EncodeToString(raw), nil
}

// csrfDerivationLabel domain-separates the CSRF token from anything else that
// might one day be derived from a session token.
const csrfDerivationLabel = "accesslink-csrf-v1:"

// deriveCSRFToken computes a session's CSRF token from the session token.
//
// WHY DERIVED RATHER THAN RANDOM AND STORED. GET /auth/me has to return the CSRF
// token: after a page reload the dashboard has lost its in-memory copy but still
// holds the session cookie, and without the token it could not make a single
// unsafe request. Only the SHA-256 of the token is stored, so /me cannot read it
// back -- and the alternatives are all worse. Rotating on /me would break a
// second tab. Storing the plaintext would put a live credential in the table.
// Signing it would introduce a server key to manage.
//
// Deriving it costs nothing in strength. A CSRF token has exactly one secrecy
// requirement -- a cross-site attacker must not be able to obtain it -- and that
// attacker cannot read the cookie, so it cannot derive this either. Anyone who
// CAN read the cookie already holds the session and gains nothing. The reverse
// direction is a SHA-256 preimage, so the CSRF token never yields the session
// token. What is stored stays a hash, so a database read still yields neither.
func deriveCSRFToken(sessionToken string) string {
	sum := sha256.Sum256([]byte(csrfDerivationLabel + sessionToken))
	return hex.EncodeToString(sum[:])
}

// CreateSession opens a session for an operator and returns the two plaintext
// secrets, which exist here and nowhere else.
//
// The token goes into the __Host-al_session cookie. The CSRF token goes into
// the response body: in a split-origin deployment the dashboard's JavaScript
// cannot read a cookie scoped to the API host, so a double-submit cookie would
// silently fail there while appearing to work in development.
func CreateSession(userID int64, ipAddress, userAgent string) (*models.SessionCredentials, error) {
	token, err := generateSecret(sessionTokenPrefix)
	if err != nil {
		return nil, err
	}
	csrfToken := deriveCSRFToken(token)

	var creds models.SessionCredentials
	creds.Token = token
	creds.CSRFToken = csrfToken

	// LEAST clamps the idle expiry to the absolute one, so a configuration with
	// an idle timeout longer than the absolute cannot violate the CHECK.
	err = DB.QueryRow(`
		INSERT INTO user_sessions (user_id, token_hash, csrf_token_hash,
		                           idle_expires_at, absolute_expires_at,
		                           ip_address, user_agent)
		VALUES ($1, $2, $3,
		        LEAST(CURRENT_TIMESTAMP + make_interval(secs => $4::float8),
		              CURRENT_TIMESTAMP + make_interval(secs => $5::float8)),
		        CURRENT_TIMESTAMP + make_interval(secs => $5::float8),
		        NULLIF($6,''), NULLIF($7,''))
		RETURNING public_id, idle_expires_at, absolute_expires_at,
		          CEIL(EXTRACT(EPOCH FROM (absolute_expires_at - CURRENT_TIMESTAMP)))::int`,
		userID, hashSessionSecret(token), hashSessionSecret(csrfToken),
		SessionIdleTimeout().Seconds(), SessionAbsoluteTimeout().Seconds(),
		truncate(ipAddress, 45), truncate(userAgent, 255)).
		Scan(&creds.PublicID, &creds.IdleExpiresAt, &creds.AbsoluteExpiresAt,
			&creds.ExpiresInSeconds)
	if err != nil {
		return nil, err
	}

	return &creds, nil
}

// AuthenticateSession resolves the operator behind a session token.
//
// Returns nil when the token is unknown, revoked, expired by either clock, or
// belongs to a disabled account or company -- all of which the caller answers
// as 401. An error means the database failed and must be answered as 500: an
// outage reported as "not logged in" would have every operator re-entering a
// password that was never the problem.
func AuthenticateSession(token string) (*models.OperatorIdentity, error) {
	if token == "" {
		return nil, nil
	}

	var (
		identity    models.OperatorIdentity
		storedHash  string
		sessionLive bool
		needsTouch  bool
		companyOK   bool
	)

	// Liveness is decided in SQL rather than by comparing the returned
	// timestamps against time.Now().
	//
	// This was originally forced: the columns were TIMESTAMP WITHOUT TIME ZONE
	// holding the database server's wall clock, so a Go-side comparison was
	// wrong by that server's UTC offset and would have kept expired sessions
	// alive for an extra hour. Migration 010 made them TIMESTAMPTZ, so that
	// hazard is gone and a Go-side comparison would now be correct.
	//
	// It stays in SQL anyway, because the reason to prefer it never depended on
	// the column type: an expiry decided here is decided against ONE clock, the
	// database's. Comparing in Go introduces the API process's clock as a second
	// authority, and two hosts whose NTP has drifted apart then disagree about
	// whether a session is still valid. expires_in_seconds is returned as a
	// remaining DURATION for the same reason -- it is the one form a client can
	// act on without trusting anybody's wall clock but its own elapsed time.
	err := DB.QueryRow(`
		SELECT s.id, s.public_id, s.token_hash, s.csrf_token_hash,
		       s.idle_expires_at, s.absolute_expires_at,
		       CEIL(EXTRACT(EPOCH FROM (s.absolute_expires_at - CURRENT_TIMESTAMP)))::int
		           AS expires_in_seconds,
		       (s.revoked_at IS NULL
		        AND s.idle_expires_at > CURRENT_TIMESTAMP
		        AND s.absolute_expires_at > CURRENT_TIMESTAMP) AS session_live,
		       (s.last_used_at <= CURRENT_TIMESTAMP - make_interval(secs => $2::float8))
		           AS needs_touch,
		       u.id, u.public_id, u.company_id, u.email, u.full_name, u.role, u.active,
		       c.public_id, c.name, c.slug,
		       (c.active AND c.deleted_at IS NULL) AS company_ok
		  FROM user_sessions s
		  JOIN users u ON u.id = s.user_id
		  JOIN companies c ON c.id = u.company_id
		 WHERE s.token_hash = $1
		   AND u.deleted_at IS NULL`,
		hashSessionSecret(token), sessionTouchInterval.Seconds()).
		Scan(&identity.SessionID, &identity.SessionPublicID, &storedHash,
			&identity.CSRFTokenHash,
			&identity.IdleExpiresAt, &identity.AbsoluteExpiresAt,
			&identity.ExpiresInSeconds,
			&sessionLive, &needsTouch,
			&identity.UserID, &identity.UserPublicID, &identity.CompanyID,
			&identity.Email, &identity.FullName, &identity.Role, &identity.Active,
			&identity.CompanyPublicID, &identity.CompanyName, &identity.CompanySlug,
			&companyOK)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// The lookup already matched on the hash; this constant-time compare guards
	// against a future refactor that widens the query, exactly as
	// AuthenticateDevice does.
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashSessionSecret(token))) != 1 {
		return nil, nil
	}

	// Revoked, expired by either clock, or belonging to an account or company
	// that has been switched off: all of them are "not logged in".
	if !sessionLive || !identity.Active || !companyOK {
		return nil, nil
	}

	// Recomputed, never read from the table: only the hash is stored. This is
	// what lets /auth/me hand the token back after a page reload.
	identity.CSRFToken = deriveCSRFToken(token)

	// Slide the idle window, at most once a minute. Best effort: a failed touch
	// costs the operator nothing until the window actually lapses, whereas
	// failing the request would log them out because a bookkeeping write missed.
	if needsTouch {
		var idleExpires time.Time
		err := DB.QueryRow(`
			UPDATE user_sessions
			   SET last_used_at = CURRENT_TIMESTAMP,
			       idle_expires_at = LEAST(
			           CURRENT_TIMESTAMP + make_interval(secs => $2::float8),
			           absolute_expires_at)
			 WHERE id = $1
			RETURNING idle_expires_at`,
			identity.SessionID, SessionIdleTimeout().Seconds()).Scan(&idleExpires)
		if err == nil {
			identity.IdleExpiresAt = idleExpires
		}
	}

	return &identity, nil
}

// CSRFMatches reports whether a presented X-CSRF-Token belongs to the session
// that stored hash came from.
//
// Constant time, like every other credential comparison here: a byte-by-byte
// early exit would leak the stored value one character at a time to a caller
// willing to measure.
func CSRFMatches(storedHash, presented string) bool {
	if storedHash == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(storedHash), []byte(hashSessionSecret(presented))) == 1
}

// RevokeSession ends one session. Idempotent: revoking an already-revoked
// session is not an error, because a logout that raced with an expiry sweep
// should still look like a successful logout.
func RevokeSession(sessionID int64) error {
	_, err := DB.Exec(`
		UPDATE user_sessions SET revoked_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND revoked_at IS NULL`, sessionID)
	return err
}

// RevokeUserSessions ends every session an operator holds, optionally sparing
// one -- the tab they are working in, when they change their own password.
// Pass 0 for exceptSessionID to revoke all of them. Returns how many were live.
func RevokeUserSessions(userID, exceptSessionID int64) (int, error) {
	result, err := DB.Exec(`
		UPDATE user_sessions SET revoked_at = CURRENT_TIMESTAMP
		 WHERE user_id = $1 AND revoked_at IS NULL AND id <> $2`,
		userID, exceptSessionID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// revokeUserSessions is the same operation inside a caller's transaction, so
// that disabling an account and cutting off its sessions commit together. There
// is no window in which one has happened and the other has not.
func revokeUserSessions(db execer, userID, exceptSessionID int64) error {
	_, err := db.Exec(`
		UPDATE user_sessions SET revoked_at = CURRENT_TIMESTAMP
		 WHERE user_id = $1 AND revoked_at IS NULL AND id <> $2`,
		userID, exceptSessionID)
	return err
}

// ListUserSessions returns an operator's live sessions, newest first. Nothing
// here can be replayed as a session: the token hash is not part of it.
func ListUserSessions(userID int64) ([]models.SessionInfo, error) {
	rows, err := DB.Query(`
		SELECT public_id, issued_at, last_used_at, idle_expires_at,
		       absolute_expires_at, COALESCE(ip_address, '')
		  FROM user_sessions
		 WHERE user_id = $1
		   AND revoked_at IS NULL
		   AND idle_expires_at > CURRENT_TIMESTAMP
		   AND absolute_expires_at > CURRENT_TIMESTAMP
		 ORDER BY issued_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.SessionInfo
	for rows.Next() {
		var s models.SessionInfo
		if err := rows.Scan(&s.PublicID, &s.IssuedAt, &s.LastUsedAt,
			&s.IdleExpiresAt, &s.AbsoluteExpiresAt, &s.IPAddress); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// PurgeExpiredSessions deletes sessions that can no longer authenticate
// anything, for the scheduled maintenance sweep.
//
// Rows are kept for a retention window after they die rather than being deleted
// the moment they expire, because "when did this operator last sign in, and from
// where" is a question worth being able to answer after an incident.
func PurgeExpiredSessions(retentionDays int) (int, error) {
	affected, err := PurgeExpiredSessionsContext(context.Background(), retentionDays)
	return int(affected), err
}

// PurgeExpiredSessionsContext is the same sweep, cancellable, for the scheduled
// maintenance task.
//
// A LIVE SESSION IS NEVER TOUCHED. The predicate matches only rows that are
// already dead -- past their absolute expiry, or revoked -- and then only after
// the retention window on top of that. There is deliberately no clause about
// idle expiry: an idle-expired session can still be resurrected by nothing, but
// its row is what an incident review reads to answer "who signed in, from
// where", and the absolute expiry will collect it soon enough anyway.
func PurgeExpiredSessionsContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays < 0 {
		retentionDays = 0
	}

	result, err := DB.ExecContext(ctx, `
		DELETE FROM user_sessions
		 WHERE (absolute_expires_at < CURRENT_TIMESTAMP - make_interval(days => $1))
		    OR (revoked_at IS NOT NULL
		        AND revoked_at < CURRENT_TIMESTAMP - make_interval(days => $1))`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// truncate bounds a value to what its column can hold. A user agent longer than
// the column is a browser quirk, not a reason to fail a login.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
