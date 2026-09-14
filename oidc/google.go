// Package oidc is the server side of "Sign in with Google".
//
// It implements exactly the slice of OpenID Connect the console needs: the
// authorization-code flow with PKCE, the code exchange at the token endpoint,
// and verification of the ID token that comes back. Nothing here knows about
// operators, sessions or companies -- it turns a callback into a verified
// identity (subject, email, whether Google vouches for the address) and stops.
// Deciding whether that identity may sign in is the store's job, in
// database/google_identity.go.
//
// WRITTEN AGAINST THE STANDARD LIBRARY rather than pulling in an OIDC client.
// The whole thing is a discovery document, an HTTPS form post and an RS256
// signature over two base64 segments; the verification rules are short enough
// to read in one sitting, and every one of them is asserted by a test. A
// dependency would have been more code to audit, not less.
//
// THE ISSUER IS CONFIGURABLE for one reason: the tests stand up a fake Google
// on a loopback address and point this package at it. Production never sets it
// and gets accounts.google.com.
package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Environment variables. All three are required together: a deployment that
// sets two of them has a typo, not a preference, and refusing to start says so
// at deploy time rather than at the first sign-in attempt.
const (
	EnvGoogleClientID     = "GOOGLE_CLIENT_ID"
	EnvGoogleClientSecret = "GOOGLE_CLIENT_SECRET"
	EnvGoogleRedirectURL  = "GOOGLE_REDIRECT_URL"

	// EnvGoogleIssuerURL overrides the issuer, for tests. Unset means Google.
	EnvGoogleIssuerURL = "GOOGLE_ISSUER_URL"
)

// GoogleIssuer is the issuer every production deployment talks to.
const GoogleIssuer = "https://accounts.google.com"

// Config is what a Provider needs to talk to Google.
type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is the API's own callback, registered with Google verbatim.
	// Explicit rather than derived from the request, because a callback built
	// from a Host header is a callback an attacker can choose.
	RedirectURL string
	// Issuer defaults to GoogleIssuer.
	Issuer string
	// HTTPClient defaults to one with a short timeout. Google answering slowly
	// must not hold an operator's browser open indefinitely.
	HTTPClient *http.Client
}

// ErrNotConfigured is returned by ConfigFromEnv when no Google variable is set:
// the feature is simply off, which is a valid state and not an error to log.
var ErrNotConfigured = errors.New("google sign-in is not configured")

// ConfigFromEnv reads the Google configuration.
//
// Returns ErrNotConfigured when NONE of the variables is set, and a descriptive
// error when SOME are -- a half-configured provider is a mistake somebody
// should hear about before the first operator hits a 500.
func ConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ClientID:     strings.TrimSpace(os.Getenv(EnvGoogleClientID)),
		ClientSecret: strings.TrimSpace(os.Getenv(EnvGoogleClientSecret)),
		RedirectURL:  strings.TrimSpace(os.Getenv(EnvGoogleRedirectURL)),
		Issuer:       strings.TrimSpace(os.Getenv(EnvGoogleIssuerURL)),
	}
	if cfg.ClientID == "" && cfg.ClientSecret == "" && cfg.RedirectURL == "" {
		return nil, ErrNotConfigured
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string
	if c.ClientID == "" {
		missing = append(missing, EnvGoogleClientID)
	}
	if c.ClientSecret == "" {
		missing = append(missing, EnvGoogleClientSecret)
	}
	if c.RedirectURL == "" {
		missing = append(missing, EnvGoogleRedirectURL)
	}
	if len(missing) > 0 {
		return fmt.Errorf("google sign-in is partly configured: %s must also be set",
			strings.Join(missing, ", "))
	}

	redirect, err := url.Parse(c.RedirectURL)
	if err != nil || redirect.Scheme == "" || redirect.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, got %q", EnvGoogleRedirectURL, c.RedirectURL)
	}
	if redirect.Scheme != "https" && !isLoopback(redirect.Hostname()) {
		return fmt.Errorf("%s must use https (got %q); plain http is only "+
			"accepted on a loopback address", EnvGoogleRedirectURL, c.RedirectURL)
	}

	if c.Issuer == "" {
		c.Issuer = GoogleIssuer
	}
	issuer, err := url.Parse(c.Issuer)
	if err != nil || issuer.Scheme == "" || issuer.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, got %q", EnvGoogleIssuerURL, c.Issuer)
	}
	if issuer.Scheme != "https" && !isLoopback(issuer.Hostname()) {
		return fmt.Errorf("%s must use https (got %q)", EnvGoogleIssuerURL, c.Issuer)
	}
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	return nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Identity is what a verified ID token says about the person who signed in.
//
// Subject is the stable identifier and the only thing an account should be
// keyed on: a Google user can change their address and keep their account, and
// an address can be released and re-registered by somebody else. Email is
// carried for LINKING an existing account on first sign-in, and only when
// EmailVerified is true -- an unverified address is a claim, not a fact.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	// HostedDomain is the Google Workspace domain, when the account has one.
	HostedDomain string
}

// Provider talks to one issuer.
type Provider struct {
	cfg  Config
	http *http.Client

	mu        sync.Mutex
	discovery *discoveryDocument
	fetchedAt time.Time
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
}

// New builds a Provider. It performs no network I/O: discovery is fetched on
// first use so that a deployment can start while Google is unreachable, and a
// sign-in attempt then fails rather than the whole API.
func New(cfg Config) (*Provider, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Provider{cfg: cfg, http: client, keys: map[string]*rsa.PublicKey{}}, nil
}

// RedirectURL is the callback this provider was configured with.
func (p *Provider) RedirectURL() string { return p.cfg.RedirectURL }

// discoveryDocument is the subset of the OpenID configuration this package reads.
type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// discoveryTTL bounds how long a fetched configuration is trusted. Google's
// does not change, and the cost of a stale one is a failed sign-in, not a
// security fault -- the ID token is still verified against a fresh key set.
const discoveryTTL = time.Hour

func (p *Provider) discover(ctx context.Context) (*discoveryDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.discovery != nil && time.Since(p.fetchedAt) < discoveryTTL {
		return p.discovery, nil
	}

	var doc discoveryDocument
	if err := p.getJSON(ctx, p.cfg.Issuer+"/.well-known/openid-configuration", &doc); err != nil {
		return nil, fmt.Errorf("fetching openid configuration: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return nil, errors.New("openid configuration is missing an endpoint")
	}
	p.discovery = &doc
	p.fetchedAt = time.Now()
	return &doc, nil
}

func (p *Provider) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", target, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

// FlowSecrets are the per-attempt values the caller keeps (in a cookie) between
// sending the browser to Google and receiving it back.
type FlowSecrets struct {
	// State is echoed back by Google and compared to the cookie. It binds the
	// callback to the browser that started the flow.
	State string
	// Nonce is embedded in the ID token, so a token minted for a different
	// attempt -- or replayed from one -- is refused.
	Nonce string
	// Verifier is the PKCE secret. Its S256 challenge goes to Google with the
	// request; the verifier itself goes only with the code exchange.
	Verifier string
}

// NewFlowSecrets mints the three random values for one attempt.
func NewFlowSecrets() (*FlowSecrets, error) {
	state, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return nil, err
	}
	return &FlowSecrets{State: state, Nonce: nonce, Verifier: verifier}, nil
}

func randomToken(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// AuthURL is where to send the browser.
func (p *Provider) AuthURL(ctx context.Context, secrets *FlowSecrets) (string, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return "", err
	}

	challenge := sha256.Sum256([]byte(secrets.Verifier))
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", p.cfg.RedirectURL)
	q.Set("scope", "openid email profile")
	q.Set("state", secrets.State)
	q.Set("nonce", secrets.Nonce)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	// The account chooser, every time. An operator on a shared machine must be
	// able to pick which Google account signs in, rather than being silently
	// signed in as whoever last used the browser.
	q.Set("prompt", "select_account")

	sep := "?"
	if strings.Contains(doc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return doc.AuthorizationEndpoint + sep + q.Encode(), nil
}

// Exchange trades the callback's code for an ID token and verifies it.
//
// Every failure is returned as an error the caller reports GENERICALLY. Which
// check failed is logged server-side; the browser learns only that the sign-in
// did not complete.
func (p *Provider) Exchange(ctx context.Context, code string, secrets *FlowSecrets) (*Identity, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", p.cfg.ClientID)
	form.Set("client_secret", p.cfg.ClientSecret)
	form.Set("redirect_uri", p.cfg.RedirectURL)
	form.Set("code_verifier", secrets.Verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token exchange: reading response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var oauthErr struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &oauthErr)
		return nil, fmt.Errorf("token exchange answered %d: %s %s",
			res.StatusCode, oauthErr.Error, oauthErr.Description)
	}

	var tokens struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("token exchange: decoding response: %w", err)
	}
	if tokens.IDToken == "" {
		return nil, errors.New("token exchange returned no id_token")
	}

	return p.verifyIDToken(ctx, doc, tokens.IDToken, secrets.Nonce, time.Now())
}

// clockSkew is the leeway allowed on exp and iat. Google's clock and this
// host's are both NTP-disciplined; a minute covers any real drift without
// meaningfully extending a token's life.
const clockSkew = time.Minute

type idTokenClaims struct {
	Issuer        string          `json:"iss"`
	Subject       string          `json:"sub"`
	Audience      json.RawMessage `json:"aud"`
	Expires       int64           `json:"exp"`
	IssuedAt      int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified bool            `json:"email_verified"`
	Name          string          `json:"name"`
	HostedDomain  string          `json:"hd"`
}

// verifyIDToken checks the signature and every claim that matters.
//
// The order is deliberate: the signature is checked FIRST, so that no claim is
// read out of a token that Google did not sign. Everything after it is about a
// genuine Google token being the RIGHT one -- for this client, this attempt,
// and now.
func (p *Provider) verifyIDToken(ctx context.Context, doc *discoveryDocument,
	raw, expectedNonce string, now time.Time) (*Identity, error) {

	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("id_token is not a compact JWS")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("id_token header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("id_token header: %w", err)
	}
	// RS256 and nothing else. Accepting whatever `alg` says is the classic JWT
	// mistake -- "none", or an HMAC keyed with the public key.
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("id_token alg %q is not RS256", header.Alg)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("id_token signature: %w", err)
	}

	key, err := p.key(ctx, doc, header.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return nil, errors.New("id_token signature does not verify")
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("id_token payload: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("id_token payload: %w", err)
	}

	// Google issues both spellings. Only the configured issuer's forms are
	// accepted; a token from any other issuer, however well signed, is refused.
	if !issuerMatches(claims.Issuer, p.cfg.Issuer, doc.Issuer) {
		return nil, fmt.Errorf("id_token issuer %q is not %s", claims.Issuer, p.cfg.Issuer)
	}
	if !audienceContains(claims.Audience, p.cfg.ClientID) {
		return nil, errors.New("id_token was not issued for this client")
	}
	if claims.Expires == 0 || now.After(time.Unix(claims.Expires, 0).Add(clockSkew)) {
		return nil, errors.New("id_token has expired")
	}
	if claims.IssuedAt != 0 && time.Unix(claims.IssuedAt, 0).After(now.Add(clockSkew)) {
		return nil, errors.New("id_token was issued in the future")
	}
	if expectedNonce == "" || claims.Nonce != expectedNonce {
		return nil, errors.New("id_token nonce does not match this sign-in attempt")
	}
	if claims.Subject == "" {
		return nil, errors.New("id_token carries no subject")
	}

	return &Identity{
		Subject:       claims.Subject,
		Email:         strings.ToLower(strings.TrimSpace(claims.Email)),
		EmailVerified: claims.EmailVerified,
		Name:          strings.TrimSpace(claims.Name),
		HostedDomain:  claims.HostedDomain,
	}, nil
}

func issuerMatches(claimed, configured, discovered string) bool {
	normalise := func(s string) string {
		s = strings.TrimRight(s, "/")
		return strings.TrimPrefix(s, "https://")
	}
	claimed = normalise(claimed)
	return claimed != "" && (claimed == normalise(configured) || claimed == normalise(discovered))
}

// audienceContains handles `aud` as either a string or an array of strings,
// both of which the specification permits.
func audienceContains(raw json.RawMessage, clientID string) bool {
	if len(raw) == 0 || clientID == "" {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == clientID
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, aud := range many {
			if aud == clientID {
				return true
			}
		}
	}
	return false
}

// jwksRefreshFloor bounds how often an unknown key id triggers a refetch, so a
// flood of tokens with invented kids cannot turn this into a request amplifier
// against Google.
const jwksRefreshFloor = time.Minute

// key resolves a signing key by id, refreshing the key set at most once when
// the id is unknown -- Google rotates keys, and a token signed moments after a
// rotation is genuine.
func (p *Provider) key(ctx context.Context, doc *discoveryDocument, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	if key, ok := p.keys[kid]; ok {
		p.mu.Unlock()
		return key, nil
	}
	tooSoon := !p.keysAt.IsZero() && time.Since(p.keysAt) < jwksRefreshFloor
	p.mu.Unlock()

	if tooSoon {
		return nil, fmt.Errorf("id_token signed with unknown key %q", kid)
	}
	if err := p.refreshKeys(ctx, doc); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if key, ok := p.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("id_token signed with unknown key %q", kid)
}

func (p *Provider) refreshKeys(ctx context.Context, doc *discoveryDocument) error {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		return fmt.Errorf("fetching signing keys: %w", err)
	}

	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || k.Kid == "" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		exponent := new(big.Int).SetBytes(e)
		if !exponent.IsInt64() || exponent.Int64() < 3 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}
	}

	p.mu.Lock()
	p.keys = keys
	p.keysAt = time.Now()
	p.mu.Unlock()
	return nil
}
