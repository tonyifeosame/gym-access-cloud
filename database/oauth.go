package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"access-terminal-cloud-api/models"
)

// The OAuth authorization server's store (migrations/037_datavase_oauth.sql).
//
// ---------------------------------------------------------------------------
// WHAT THIS FILE IS RESPONSIBLE FOR, AND WHAT IT REFUSES TO BE
// ---------------------------------------------------------------------------
//
// Every decision that has to be ATOMIC lives here, in SQL, in one statement or
// one transaction: consuming a code, rotating a refresh token, detecting reuse,
// revoking a family. Those are the places where two concurrent requests would
// otherwise both succeed, and a handler that read-then-wrote would be the bug.
//
// Everything that is POLICY -- what a consent screen says, which scopes a role
// may grant, how long a form token lives -- is not here. This file stores and
// compares; models decides.
//
// ---------------------------------------------------------------------------
// THE PLAINTEXT EXISTS IN ONE LOCAL VARIABLE
// ---------------------------------------------------------------------------
//
// generateOAuthSecret produces a token, its hash goes to the database, and the
// plaintext is returned to exactly one caller -- the same discipline
// generateSiteKey and generateAPIKey already keep. Nothing reads a token back
// out of the database, because there is nothing to read: the column holds a
// SHA-256 digest.
//
// SHA-256 rather than bcrypt, for the reason already written in sites.go and
// api_credentials.go: 256 bits from crypto/rand is not open to dictionary
// attack, does not need a slow KDF, and has to be verifiable on every request
// without burning CPU.

// HashOAuthSecret returns the stored form of an OAuth code, token or client
// secret.
//
// Exported because the authentication path hashes what a caller presented
// before looking it up, and that has to be the same function that produced the
// stored value. Two spellings of "the hash" is how an authentication system
// stops authenticating.
func HashOAuthSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// generateOAuthSecret returns a new secret of one class and its storage form.
//
// 32 bytes from crypto/rand. Not math/rand and not a UUID, for the reason
// generateAPIKey records: a v4 UUID carries 122 bits and is produced by
// libraries with varying opinions about entropy sources, which is not a thing
// to be casual about for a bearer token that reaches a customer's sites.
//
// environment is "" for the authorization code, which carries none: it is
// exchanged at the deployment that issued it, within two minutes.
func generateOAuthSecret(prefix, environment string) (secret, hash string, err error) {
	if environment != "" && !models.APIEnvironments[environment] {
		return "", "", fmt.Errorf("%w: %q", models.ErrUnknownEnvironment, environment)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generating oauth secret: %w", err)
	}

	secret = prefix
	if environment != "" {
		secret += environment + "_"
	}
	secret += hex.EncodeToString(raw)
	return secret, HashOAuthSecret(secret), nil
}

// ---------------------------------------------------------------------------
// Clients
// ---------------------------------------------------------------------------

// OAuthClientConfig is what a DEPLOYMENT supplies to configure a client.
//
// NOT A REQUEST BODY. There is no registration endpoint; this struct is filled
// from environment configuration at startup and from test fixtures, and that is
// the whole set of writers. A redirect URI can therefore only be allow-listed
// by whoever operates the service.
type OAuthClientConfig struct {
	ClientID string
	Name     string

	// Secret is the plaintext client secret, or "" for a public client. It is
	// hashed here and never stored, logged or read back.
	Secret string

	RedirectURIs []string
	Scopes       []string
}

// UpsertOAuthClient writes a configured client, creating or updating it.
//
// IDEMPOTENT BY client_id, because it runs on every boot. A deployment that
// changes its redirect URI changes a variable and restarts; a deployment that
// changes nothing rewrites the same row, which is cheaper to reason about than
// a first-run-only path that silently ignores later configuration changes.
//
// AN EMPTY Secret DOES NOT CLEAR AN EXISTING ONE, and that asymmetry is
// deliberate: a deployment that forgot to set the variable would otherwise
// silently downgrade a confidential client to a public one, which is a security
// change nobody asked for. Clearing a secret is done by rotating it to a new
// value or by removing the client.
func UpsertOAuthClient(in OAuthClientConfig) (*models.OAuthClient, error) {
	clientID := strings.ToLower(strings.TrimSpace(in.ClientID))
	name := strings.TrimSpace(in.Name)
	if clientID == "" {
		return nil, errors.New("oauth client: client_id is required")
	}
	if name == "" {
		return nil, errors.New("oauth client: name is required")
	}
	if len(in.RedirectURIs) == 0 {
		return nil, errors.New("oauth client: at least one redirect URI is required")
	}
	for _, uri := range in.RedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return nil, err
		}
	}

	scopes, err := models.ExpandScopes(in.Scopes)
	if err != nil {
		return nil, fmt.Errorf("oauth client %q: %w", clientID, err)
	}
	// THE PHASE BOUNDARY, checked against the EXPANDED set so an implication
	// cannot smuggle in a scope that was not asked for directly. See
	// models.OAuthGrantableScopes for why a grant's set is narrower than a
	// credential's.
	for _, scope := range scopes {
		if !models.GrantableByOAuth(scope) {
			return nil, fmt.Errorf("oauth client %q: %w: %q (available: %s)",
				clientID, models.ErrOAuthScopeNotGrantable, scope,
				strings.Join(models.GrantableOAuthScopes(), ", "))
		}
	}

	var secretHash any
	if in.Secret != "" {
		secretHash = HashOAuthSecret(in.Secret)
	}

	var (
		client     models.OAuthClient
		redirects  pq.StringArray
		stored     pq.StringArray
		hashStored sql.NullString
	)
	err = DB.QueryRow(`
		INSERT INTO oauth_clients (client_id, name, secret_hash, redirect_uris, scopes, active)
		VALUES ($1, $2, $3, $4, $5, TRUE)
		ON CONFLICT (client_id) DO UPDATE
		   SET name          = EXCLUDED.name,
		       secret_hash   = COALESCE(EXCLUDED.secret_hash, oauth_clients.secret_hash),
		       redirect_uris = EXCLUDED.redirect_uris,
		       scopes        = EXCLUDED.scopes,
		       active        = TRUE,
		       updated_at    = CURRENT_TIMESTAMP
		RETURNING id, client_id, name, secret_hash, redirect_uris, scopes, active`,
		clientID, name, secretHash, pq.Array(in.RedirectURIs), pq.Array(scopes)).
		Scan(&client.ID, &client.ClientID, &client.Name, &hashStored, &redirects, &stored,
			&client.Active)
	if err != nil {
		return nil, fmt.Errorf("configuring oauth client %q: %w", clientID, err)
	}

	client.Confidential = hashStored.Valid
	client.RedirectURIs = []string(redirects)
	client.Scopes = []string(stored)
	return &client, nil
}

// validateRedirectURI refuses a URI that cannot safely be an allow-list entry.
//
// HTTPS, OR http ON A LOOPBACK HOST. An http:// redirect to a remote host puts
// an authorization code on the wire in clear; a loopback exception is what RFC
// 8252 section 7.3 allows so a native client can develop against itself, and it
// cannot be intercepted by a network.
//
// NO FRAGMENT. The redirect appends query parameters; a URI that already
// carries a fragment would silently lose them in the browser.
func validateRedirectURI(uri string) error {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" || len(trimmed) > models.MaxOAuthRedirectURILength {
		return fmt.Errorf("oauth client: redirect URI %q is empty or too long", uri)
	}
	if strings.Contains(trimmed, "#") {
		return fmt.Errorf("oauth client: redirect URI %q must not contain a fragment", uri)
	}
	if strings.HasPrefix(trimmed, "https://") {
		return nil
	}
	if strings.HasPrefix(trimmed, "http://127.0.0.1") ||
		strings.HasPrefix(trimmed, "http://localhost") ||
		strings.HasPrefix(trimmed, "http://[::1]") {
		return nil
	}
	return fmt.Errorf("oauth client: redirect URI %q must be https, or http on a loopback host", uri)
}

// OAuthClientByID reads a configured client by its public identifier.
//
// Returns nil, nil when there is no such client OR when it is inactive. The
// caller answers both as invalid_client: an integrator is not entitled to learn
// that a client id exists but has been turned off, and there is nothing they
// could do differently if they did.
func OAuthClientByID(clientID string) (*models.OAuthClient, error) {
	var (
		client    models.OAuthClient
		redirects pq.StringArray
		scopes    pq.StringArray
		hash      sql.NullString
	)
	err := DB.QueryRow(`
		SELECT id, client_id, name, secret_hash, redirect_uris, scopes, active
		  FROM oauth_clients
		 WHERE client_id = $1 AND active`,
		strings.ToLower(strings.TrimSpace(clientID))).
		Scan(&client.ID, &client.ClientID, &client.Name, &hash, &redirects, &scopes,
			&client.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading oauth client: %w", err)
	}
	client.Confidential = hash.Valid
	client.RedirectURIs = []string(redirects)
	client.Scopes = []string(scopes)
	return &client, nil
}

// AuthenticateOAuthClientSecret reports whether a presented secret matches.
//
// CONSTANT TIME, and the stored value is a hash, so what is compared is two
// digests rather than a digest and a secret. A byte-by-byte compare with an
// early exit would leak the stored digest one character at a time to a caller
// willing to measure -- the same reasoning CSRFMatches already records.
//
// A PUBLIC CLIENT HAS NOTHING TO PROVE and this returns false for one: the
// caller must not reach here for a public client at all, and answering "yes" to
// an empty secret would turn a configuration mistake into an authentication
// bypass.
func AuthenticateOAuthClientSecret(clientID int64, presented string) (bool, error) {
	var hash sql.NullString
	err := DB.QueryRow(
		`SELECT secret_hash FROM oauth_clients WHERE id = $1 AND active`, clientID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading oauth client secret: %w", err)
	}
	if !hash.Valid || presented == "" {
		return false, nil
	}
	return subtle.ConstantTimeCompare(
		[]byte(hash.String), []byte(HashOAuthSecret(presented))) == 1, nil
}

// ---------------------------------------------------------------------------
// Authorization codes
// ---------------------------------------------------------------------------

// OAuthCodeIssue is one consent, as the authorization endpoint grants it.
type OAuthCodeIssue struct {
	ClientID  int64
	CompanyID int64
	UserID    int64

	RedirectURI string
	Scopes      []string

	CodeChallenge       string
	CodeChallengeMethod string
}

// IssueOAuthCode mints an authorization code and returns its plaintext ONCE.
//
// Everything the token endpoint will check is written here and nowhere else.
// The client cannot restate any of it at exchange time and be believed: it can
// only present values that must EQUAL what this row already holds.
func IssueOAuthCode(in OAuthCodeIssue) (string, error) {
	if in.ClientID <= 0 || in.CompanyID <= 0 || in.UserID <= 0 {
		return "", errors.New("oauth code: client, company and owner are required")
	}
	if !models.ValidCodeChallenge(in.CodeChallenge) || in.CodeChallengeMethod != "S256" {
		return "", errors.New("oauth code: an S256 code challenge is required")
	}
	if len(in.Scopes) == 0 {
		return "", models.ErrNoScopes
	}

	code, hash, err := generateOAuthSecret(models.OAuthCodePrefix, "")
	if err != nil {
		return "", err
	}

	_, err = DB.Exec(`
		INSERT INTO oauth_authorization_codes
		       (code_hash, client_id, company_id, user_id, redirect_uri, scopes,
		        code_challenge, code_challenge_method, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP + $9::interval)`,
		hash, in.ClientID, in.CompanyID, in.UserID, in.RedirectURI, pq.Array(in.Scopes),
		in.CodeChallenge, in.CodeChallengeMethod,
		fmt.Sprintf("%d seconds", int(models.OAuthCodeLifetime.Seconds())))
	if err != nil {
		return "", fmt.Errorf("issuing authorization code: %w", err)
	}
	return code, nil
}

// OAuthGrant is a freshly minted pair, returned ONCE.
//
// AccessToken and RefreshToken are plaintext and exist here only on the way out
// of the token endpoint. Nothing stores this struct and nothing logs it.
type OAuthGrant struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Scopes       []string

	// AccessTokenID and FamilyID identify the grant for the audit record --
	// neither is a secret and neither can be presented as one.
	AccessTokenID string
	FamilyID      string

	// CompanyID and UserID are who the grant belongs to.
	//
	// RETURNED RATHER THAN LOOKED UP AGAIN. The audit record needs a company,
	// and the alternative -- authenticating the token that was just minted to
	// find out -- is a second query for something this function already knew,
	// on the one path where an extra round trip is paid by a client waiting
	// for its token.
	CompanyID int64
	UserID    int64
}

// OAuthExchange is what the token endpoint presents for an authorization code.
type OAuthExchange struct {
	ClientRowID int64
	Code        string
	RedirectURI string
	Verifier    string
	Environment string
}

// ExchangeOAuthCode consumes an authorization code and mints a grant.
//
// ---------------------------------------------------------------------------
// THE CONSUME IS THE CONCURRENCY CONTROL
// ---------------------------------------------------------------------------
//
// The UPDATE ... WHERE consumed_at IS NULL RETURNING is what makes a code
// single-use under concurrency. Two simultaneous exchanges cannot both match
// the WHERE clause; exactly one gets the row and the other is told the code was
// already used. A read-then-write, however carefully ordered, would let both
// through under load -- which is the failure mode that turns a single-use code
// into a reusable one exactly when somebody is attacking it.
//
// ---------------------------------------------------------------------------
// WHAT IS CHECKED, AND IN WHICH ORDER
// ---------------------------------------------------------------------------
//
//  1. The code exists and belongs to THIS client. A code presented by another
//     client is not valid, whatever else is right about the request.
//  2. It has not expired.
//  3. It has not been consumed. A SECOND PRESENTATION REVOKES THE FAMILY the
//     first one produced (RFC 6749 section 4.1.2): the server cannot tell which
//     presentation was legitimate, so it ends both.
//  4. The redirect URI equals the one bound at consent.
//  5. The PKCE verifier hashes to the bound challenge, compared in constant
//     time.
//
// Every one of those is a refusal with the SAME outcome for the caller
// (invalid_grant), because telling them apart would be an oracle for guessing
// codes. They are distinguished internally only so the server can act on the
// replay case.
func ExchangeOAuthCode(in OAuthExchange) (*OAuthGrant, error) {
	if !models.ValidOAuthCodeShape(in.Code) {
		return nil, models.ErrOAuthCodeUnknown
	}
	if !models.ValidCodeVerifier(in.Verifier) {
		return nil, models.ErrOAuthCodeUnknown
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("exchanging authorization code: %w", err)
	}
	defer tx.Rollback()

	hash := HashOAuthSecret(in.Code)

	// Read the row for this client, under a row lock so the consume below
	// cannot race a concurrent exchange between the read and the update.
	var (
		id            int64
		companyID     int64
		userID        int64
		redirectURI   string
		scopes        pq.StringArray
		challenge     string
		expiresAt     time.Time
		consumedAt    sql.NullTime
		issuedFamily  sql.NullString
		clientRowID   int64
		nowIsAfterExp bool
	)
	err = tx.QueryRow(`
		SELECT id, client_id, company_id, user_id, redirect_uri, scopes,
		       code_challenge, expires_at, consumed_at, issued_family::text,
		       CURRENT_TIMESTAMP > expires_at
		  FROM oauth_authorization_codes
		 WHERE code_hash = $1
		   FOR UPDATE`, hash).
		Scan(&id, &clientRowID, &companyID, &userID, &redirectURI, &scopes,
			&challenge, &expiresAt, &consumedAt, &issuedFamily, &nowIsAfterExp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrOAuthCodeUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("reading authorization code: %w", err)
	}

	if clientRowID != in.ClientRowID {
		// Not this client's code. Nothing is consumed and nothing is revoked:
		// a wrong-client presentation is as likely to be a misconfiguration as
		// an attack, and destroying another client's grant on the strength of
		// it would be a denial of service anybody could trigger.
		return nil, models.ErrOAuthCodeUnknown
	}

	if consumedAt.Valid {
		// REPLAY. Revoke whatever the first exchange produced, then refuse.
		if issuedFamily.Valid && issuedFamily.String != "" {
			if err := revokeFamilyTx(tx, issuedFamily.String, models.OAuthRevokedCodeReplay); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("revoking a replayed grant: %w", err)
			}
		}
		return nil, models.ErrOAuthCodeConsumed
	}

	if nowIsAfterExp {
		return nil, models.ErrOAuthCodeExpired
	}

	// The redirect URI, compared exactly. Constant time is not required for a
	// value that is not secret, but it costs nothing and keeps one habit.
	if subtle.ConstantTimeCompare([]byte(redirectURI), []byte(in.RedirectURI)) != 1 {
		return nil, models.ErrOAuthCodeUnknown
	}

	// PKCE. The verifier is hashed and base64url-encoded without padding (RFC
	// 7636 section 4.2) and compared against the stored challenge in constant
	// time: the challenge is not a secret, but the comparison is on the path an
	// attacker with a stolen code is actively probing.
	sum := sha256.Sum256([]byte(in.Verifier))
	derived := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(derived)) != 1 {
		return nil, models.ErrOAuthCodeUnknown
	}

	grant, err := mintGrantTx(tx, mintGrant{
		ClientRowID: in.ClientRowID,
		CompanyID:   companyID,
		UserID:      userID,
		Scopes:      []string(scopes),
		Environment: in.Environment,
		// A fresh family: this consent is the root of a new rotation chain,
		// and its deadline is measured from here.
		FamilyID:  "",
		FamilyEnd: time.Now().Add(models.OAuthRefreshTokenLifetime),
	})
	if err != nil {
		return nil, err
	}

	// Consume, and record which family this code produced so a replay can end
	// it. The WHERE clause is what makes the consume single-use.
	result, err := tx.Exec(`
		UPDATE oauth_authorization_codes
		   SET consumed_at = CURRENT_TIMESTAMP, issued_family = $2::uuid
		 WHERE id = $1 AND consumed_at IS NULL`, id, grant.FamilyID)
	if err != nil {
		return nil, fmt.Errorf("consuming authorization code: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("consuming authorization code: %w", err)
	}
	if affected != 1 {
		// Unreachable while the row lock above is held, and refused rather
		// than assumed: a code that was consumed under us must not also have
		// minted a token.
		return nil, models.ErrOAuthCodeConsumed
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing authorization code exchange: %w", err)
	}
	return grant, nil
}

// OAuthRefresh is what the token endpoint presents for a refresh token.
type OAuthRefresh struct {
	ClientRowID  int64
	RefreshToken string
	Environment  string

	// Scopes optionally NARROWS the grant (RFC 6749 section 6). A request that
	// names a scope outside the grant is refused rather than trimmed, because a
	// client asking for something it does not have is a bug it needs to see.
	Scopes []string
}

// RefreshOAuthGrant rotates a refresh token, or detects that one was reused.
//
// ---------------------------------------------------------------------------
// ROTATION WITH REUSE DETECTION
// ---------------------------------------------------------------------------
//
// Every successful refresh marks the presented token rotated and mints a new
// one in the same family. A token presented when it is ALREADY rotated, or
// already revoked, means two parties hold tokens from one chain -- and the
// server cannot tell which of them is the thief. So the whole family dies:
// every refresh token in it and every access token issued under it. Both
// parties are forced back through consent, which the customer will notice,
// which is the point.
//
// THE FAMILY'S DEADLINE IS NOT EXTENDED BY ROTATION. The new token expires when
// the family would have, not sixty days from now. A chain that renewed its own
// deadline on every refresh would be a permanent grant wearing a rotating
// disguise, and a customer who connected an integration once would never be
// asked again.
func RefreshOAuthGrant(in OAuthRefresh) (*OAuthGrant, error) {
	if !models.ValidOAuthRefreshTokenShape(in.RefreshToken) {
		return nil, models.ErrOAuthGrantUnknown
	}
	if models.OAuthTokenEnvironment(in.RefreshToken) != in.Environment {
		return nil, models.ErrOAuthGrantUnknown
	}

	tx, err := DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("refreshing grant: %w", err)
	}
	defer tx.Rollback()

	var (
		id          int64
		clientRowID int64
		companyID   int64
		userID      int64
		familyID    string
		scopes      pq.StringArray
		expiresAt   time.Time
		rotatedAt   sql.NullTime
		revokedAt   sql.NullTime
		expired     bool
	)
	err = tx.QueryRow(`
		SELECT id, client_id, company_id, user_id, family_id::text, scopes,
		       expires_at, rotated_at, revoked_at, CURRENT_TIMESTAMP > expires_at
		  FROM oauth_refresh_tokens
		 WHERE token_hash = $1
		   FOR UPDATE`, HashOAuthSecret(in.RefreshToken)).
		Scan(&id, &clientRowID, &companyID, &userID, &familyID, &scopes,
			&expiresAt, &rotatedAt, &revokedAt, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrOAuthGrantUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("reading refresh token: %w", err)
	}

	if clientRowID != in.ClientRowID {
		// Another client's token. Refused without revoking anything, for the
		// same reason a wrong-client code is: a denial of service anybody could
		// trigger is not an improvement on an authentication failure.
		return nil, models.ErrOAuthGrantUnknown
	}

	if rotatedAt.Valid || revokedAt.Valid {
		// REUSE. End the family and refuse.
		if err := revokeFamilyTx(tx, familyID, models.OAuthRevokedReuse); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("revoking a reused family: %w", err)
		}
		return nil, models.ErrOAuthGrantReused
	}

	if expired {
		return nil, models.ErrOAuthGrantUnknown
	}

	granted := []string(scopes)
	if len(in.Scopes) > 0 {
		narrowed, err := narrowScopes(granted, in.Scopes)
		if err != nil {
			return nil, err
		}
		granted = narrowed
	}

	grant, err := mintGrantTx(tx, mintGrant{
		ClientRowID: in.ClientRowID,
		CompanyID:   companyID,
		UserID:      userID,
		Scopes:      granted,
		Environment: in.Environment,
		FamilyID:    familyID,
		FamilyEnd:   expiresAt,
	})
	if err != nil {
		return nil, err
	}

	// Mark the presented token rotated. The WHERE clause repeats the
	// not-yet-rotated condition so this is safe even if the row lock were ever
	// relaxed: a second rotation of one token cannot succeed.
	result, err := tx.Exec(`
		UPDATE oauth_refresh_tokens
		   SET rotated_at = CURRENT_TIMESTAMP,
		       replaced_by = (SELECT id FROM oauth_refresh_tokens WHERE token_hash = $2)
		 WHERE id = $1 AND rotated_at IS NULL AND revoked_at IS NULL`,
		id, HashOAuthSecret(grant.RefreshToken))
	if err != nil {
		return nil, fmt.Errorf("rotating refresh token: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return nil, models.ErrOAuthGrantReused
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing refresh rotation: %w", err)
	}
	return grant, nil
}

// narrowScopes restricts a grant to a subset of what it already holds.
//
// A requested scope outside the grant is ErrOAuthScopeUngrantable, not a silent
// trim: a client that believes it narrowed to members:write and was quietly
// given members:read would discover it one 403 at a time.
func narrowScopes(granted, requested []string) ([]string, error) {
	held := make(map[string]bool, len(granted))
	for _, s := range granted {
		held[s] = true
	}
	for _, s := range requested {
		if !held[s] {
			return nil, fmt.Errorf("%w: %q is not in this grant", models.ErrOAuthScopeUngrantable, s)
		}
	}
	// Re-expanded so an implication cannot be dropped by a narrowing: asking
	// for members:write alone still carries members:read, exactly as the
	// original grant does.
	return models.ExpandScopes(requested)
}

// mintGrant is the internal input to mintGrantTx.
type mintGrant struct {
	ClientRowID int64
	CompanyID   int64
	UserID      int64
	Scopes      []string
	Environment string

	// FamilyID is "" to start a new rotation chain.
	FamilyID string
	// FamilyEnd is when the refresh chain expires, regardless of rotation.
	FamilyEnd time.Time
}

// mintGrantTx writes an access token and a refresh token inside a transaction.
func mintGrantTx(tx *sql.Tx, in mintGrant) (*OAuthGrant, error) {
	if len(in.Scopes) == 0 {
		return nil, models.ErrNoScopes
	}

	family := in.FamilyID
	if family == "" {
		if err := tx.QueryRow(`SELECT gen_random_uuid()::text`).Scan(&family); err != nil {
			return nil, fmt.Errorf("starting a refresh family: %w", err)
		}
	}

	accessToken, accessHash, err := generateOAuthSecret(models.OAuthAccessPrefix, in.Environment)
	if err != nil {
		return nil, err
	}
	refreshToken, refreshHash, err := generateOAuthSecret(models.OAuthRefreshPrefix, in.Environment)
	if err != nil {
		return nil, err
	}

	var accessPublicID string
	err = tx.QueryRow(`
		INSERT INTO oauth_access_tokens
		       (token_hash, client_id, company_id, user_id, family_id, environment,
		        scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, CURRENT_TIMESTAMP + $8::interval)
		RETURNING public_id::text`,
		accessHash, in.ClientRowID, in.CompanyID, in.UserID, family, in.Environment,
		pq.Array(in.Scopes),
		fmt.Sprintf("%d seconds", int(models.OAuthAccessTokenLifetime.Seconds()))).
		Scan(&accessPublicID)
	if err != nil {
		return nil, fmt.Errorf("issuing access token: %w", err)
	}

	// The refresh token expires when the FAMILY does, not a fresh sixty days
	// from now. See the note on RefreshOAuthGrant.
	_, err = tx.Exec(`
		INSERT INTO oauth_refresh_tokens
		       (token_hash, client_id, company_id, user_id, family_id, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7)`,
		refreshHash, in.ClientRowID, in.CompanyID, in.UserID, family,
		pq.Array(in.Scopes), in.FamilyEnd.UTC())
	if err != nil {
		return nil, fmt.Errorf("issuing refresh token: %w", err)
	}

	return &OAuthGrant{
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		ExpiresIn:     int(models.OAuthAccessTokenLifetime.Seconds()),
		Scopes:        append([]string(nil), in.Scopes...),
		AccessTokenID: accessPublicID,
		FamilyID:      family,
		CompanyID:     in.CompanyID,
		UserID:        in.UserID,
	}, nil
}

// revokeFamilyTx ends an entire rotation chain: every refresh token in it and
// every access token issued under it.
//
// IDEMPOTENT. Already-revoked rows are left alone (the WHERE excludes them), so
// a second detection does not rewrite the reason of the first -- the record of
// WHY a family died is the part an incident review needs.
func revokeFamilyTx(tx *sql.Tx, familyID, reason string) error {
	if !models.OAuthRevocationReasons[reason] {
		return fmt.Errorf("oauth: %q is not a revocation reason", reason)
	}
	if _, err := tx.Exec(`
		UPDATE oauth_refresh_tokens
		   SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = $2
		 WHERE family_id = $1::uuid AND revoked_at IS NULL`, familyID, reason); err != nil {
		return fmt.Errorf("revoking refresh family: %w", err)
	}
	if _, err := tx.Exec(`
		UPDATE oauth_access_tokens
		   SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = $2
		 WHERE family_id = $1::uuid AND revoked_at IS NULL`, familyID, reason); err != nil {
		return fmt.Errorf("revoking access tokens in family: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Resource authentication
// ---------------------------------------------------------------------------

// AuthenticateOAuthAccessToken resolves a presented access token to the tenant
// and grant it belongs to.
//
// THE ONE QUERY. One indexed equality on the hash, joined to the client, the
// company and the consenting operator, with every liveness condition in the
// WHERE clause. A single nil covers unknown, expired, revoked, client
// deactivated, operator deactivated and company deactivated alike -- the caller
// reports one code for all of them, because an integrator cannot act
// differently on any of them and telling them apart is how an endpoint becomes
// an oracle.
//
// THE OPERATOR IS RE-CHECKED ON EVERY REQUEST, not merely at consent. A grant
// is an operator lending their access to a machine; when the operator is
// disabled, the lending stops. Nothing has to remember that tokens exist.
//
// THE SITE RESTRICTION IS COMPUTED HERE from the operator's CURRENT grants,
// applying the same rule RequireSiteGrant applies to the operator themselves:
// ADMIN and OWNER reach every site; for MANAGER and VIEWER an empty grant set
// also means every site, and any grant narrows to exactly those.
func AuthenticateOAuthAccessToken(presented, environment string) (*models.OAuthTokenIdentity, error) {
	if !models.ValidOAuthAccessTokenShape(presented) {
		return nil, nil
	}
	if models.OAuthTokenEnvironment(presented) != environment {
		return nil, nil
	}

	var (
		identity models.OAuthTokenIdentity
		scopes   pq.StringArray
		role     string
	)
	err := DB.QueryRow(`
		SELECT t.id, t.public_id::text, t.company_id, t.user_id, u.email, u.role,
		       c.id, c.client_id, c.name, t.environment, t.scopes
		  FROM oauth_access_tokens t
		  JOIN oauth_clients c ON c.id = t.client_id
		  JOIN companies    co ON co.id = t.company_id
		  JOIN users         u ON u.id = t.user_id
		 WHERE t.token_hash = $1
		   AND t.revoked_at IS NULL
		   AND t.expires_at > CURRENT_TIMESTAMP
		   AND c.active
		   AND co.active
		   AND co.deleted_at IS NULL
		   AND u.active
		   AND u.deleted_at IS NULL
		   AND u.company_id = t.company_id`, HashOAuthSecret(presented)).
		Scan(&identity.ID, &identity.PublicID, &identity.CompanyID, &identity.UserID,
			&identity.UserEmail, &role, &identity.ClientRowID, &identity.ClientID,
			&identity.ClientName, &identity.Environment, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("authenticating oauth access token: %w", err)
	}

	identity.Scopes = []string(scopes)

	siteIDs, err := grantedSiteIDs(identity.UserID, role)
	if err != nil {
		return nil, err
	}
	identity.SiteIDs = siteIDs

	return &identity, nil
}

// grantedSiteIDs resolves the site restriction a grant inherits from its owner.
//
// nil (meaning every site in the company) for ADMIN and OWNER, and for a
// MANAGER or VIEWER who holds no grants -- absence is the default rather than a
// wildcard row, exactly as RequireSiteGrant already reads it, so adding a site
// to a company does not require revisiting anybody's tokens.
func grantedSiteIDs(userID int64, role string) ([]int64, error) {
	if role == models.RoleAdmin || role == models.RoleOwner {
		return nil, nil
	}
	rows, err := DB.Query(
		`SELECT g.site_id
		   FROM user_site_grants g
		   JOIN sites s ON s.id = g.site_id AND s.deleted_at IS NULL
		  WHERE g.user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("reading grant site restriction: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning grant site: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---------------------------------------------------------------------------
// Revocation
// ---------------------------------------------------------------------------

// OAuthRevocation reports what a revocation actually ended, for the audit
// record. Nothing here is a secret.
type OAuthRevocation struct {
	// Found reports whether the presented token matched anything. RFC 7009
	// section 2.2 requires the ENDPOINT to answer 200 either way; this is for
	// the log line, not for the response.
	Found bool

	// FamilyID is the chain that was ended, or "".
	FamilyID string

	CompanyID int64
	UserID    int64
}

// RevokeOAuthToken implements RFC 7009 for one presented token.
//
// A REFRESH TOKEN ENDS ITS WHOLE FAMILY, because a client saying "forget this
// grant" means the grant, not one link of a chain it does not know it is on. An
// ACCESS TOKEN ends only itself: a client may legitimately be discarding one
// short-lived token while keeping the relationship.
//
// ONLY THE PRESENTING CLIENT'S OWN TOKENS. A token belonging to another client
// is reported as not found -- otherwise any configured client could end any
// other's grants by guessing.
func RevokeOAuthToken(clientRowID int64, presented string) (*OAuthRevocation, error) {
	hash := HashOAuthSecret(presented)

	switch {
	case models.ValidOAuthRefreshTokenShape(presented):
		tx, err := DB.Begin()
		if err != nil {
			return nil, fmt.Errorf("revoking refresh token: %w", err)
		}
		defer tx.Rollback()

		var out OAuthRevocation
		err = tx.QueryRow(`
			SELECT family_id::text, company_id, user_id
			  FROM oauth_refresh_tokens
			 WHERE token_hash = $1 AND client_id = $2`, hash, clientRowID).
			Scan(&out.FamilyID, &out.CompanyID, &out.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return &OAuthRevocation{}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading refresh token for revocation: %w", err)
		}
		if err := revokeFamilyTx(tx, out.FamilyID, models.OAuthRevokedByClient); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing revocation: %w", err)
		}
		out.Found = true
		return &out, nil

	case models.ValidOAuthAccessTokenShape(presented):
		var out OAuthRevocation
		err := DB.QueryRow(`
			UPDATE oauth_access_tokens
			   SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = $3
			 WHERE token_hash = $1 AND client_id = $2 AND revoked_at IS NULL
			 RETURNING COALESCE(family_id::text, ''), company_id, user_id`,
			hash, clientRowID, models.OAuthRevokedByClient).
			Scan(&out.FamilyID, &out.CompanyID, &out.UserID)
		if errors.Is(err, sql.ErrNoRows) {
			return &OAuthRevocation{}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("revoking access token: %w", err)
		}
		out.Found = true
		return &out, nil
	}

	// Not a token this server ever issued. RFC 7009 section 2.2: the endpoint
	// answers 200 regardless, so nothing is reported here either.
	return &OAuthRevocation{}, nil
}

// ---------------------------------------------------------------------------
// Housekeeping
// ---------------------------------------------------------------------------

// PurgeExpiredOAuthGrants deletes what can no longer be presented.
//
// NOT LOAD-BEARING. Every check in AuthenticateOAuthAccessToken, ExchangeOAuthCode
// and RefreshOAuthGrant already refuses an expired row, so this bounds growth
// rather than maintaining correctness -- which is why it runs beside the other
// api_housekeeping sweeps rather than on its own schedule.
//
// A GRACE PERIOD IS KEPT ON CONSUMED AND ROTATED ROWS, because they are the
// evidence of a replay. Deleting a consumed code the moment it expires would
// turn "this code was used twice" into "this code never existed", which is the
// one distinction the reuse detection depends on. The window is generous: these
// rows are small and few.
func PurgeExpiredOAuthGrants(ctx context.Context) (int64, error) {
	const replayEvidenceWindow = "7 days"

	var total int64

	result, err := DB.ExecContext(ctx, `
		DELETE FROM oauth_authorization_codes
		 WHERE expires_at < CURRENT_TIMESTAMP - $1::interval`, replayEvidenceWindow)
	if err != nil {
		return 0, fmt.Errorf("purging authorization codes: %w", err)
	}
	if n, err := result.RowsAffected(); err == nil {
		total += n
	}

	result, err = DB.ExecContext(ctx, `
		DELETE FROM oauth_access_tokens
		 WHERE expires_at < CURRENT_TIMESTAMP - $1::interval`, replayEvidenceWindow)
	if err != nil {
		return 0, fmt.Errorf("purging access tokens: %w", err)
	}
	if n, err := result.RowsAffected(); err == nil {
		total += n
	}

	// Refresh tokens are deleted last and only when nothing points at them:
	// replaced_by is a self-reference, and removing a predecessor that a live
	// successor still names would break the chain an incident review walks.
	result, err = DB.ExecContext(ctx, `
		DELETE FROM oauth_refresh_tokens t
		 WHERE t.expires_at < CURRENT_TIMESTAMP - $1::interval
		   AND NOT EXISTS (SELECT 1 FROM oauth_refresh_tokens s
		                    WHERE s.replaced_by = t.id)`, replayEvidenceWindow)
	if err != nil {
		return 0, fmt.Errorf("purging refresh tokens: %w", err)
	}
	if n, err := result.RowsAffected(); err == nil {
		total += n
	}

	return total, nil
}
