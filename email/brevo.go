package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"time"
)

// Brevo's transactional email API, over HTTPS.
//
// WHY THIS EXISTS BESIDE THE SMTP PROVIDER. The production host cannot open an
// outbound SMTP connection at all -- 587 and 465 both time out from Render --
// so a relay is not a transport this deployment can use, however it is
// configured. Brevo exposes the same sending over plain HTTPS on 443, which
// every deployment can reach. Nothing above the Sender interface changes: the
// reset handler builds the same message, queues it the same way, and logs the
// same delivered/failed line with the same request id.
//
// ONE ENDPOINT, ONE REQUEST. POST /v3/smtp/email with the API key in the
// `api-key` header and the message as JSON; 201 with a messageId is delivery
// to Brevo's queue, anything else is a failure with Brevo's own reason. There
// is no retry here -- the reset handler treats a failure as "the operator
// asks again", exactly as it does for SMTP.
//
// THE BASE URL IS CONFIGURABLE FOR ONE REASON: the tests point it at an
// httptest server. Production never sets it.

// Brevo environment variables.
const (
	// EnvBrevoAPIKey is the transactional API key (`xkeysib-…`) from
	// Brevo → SMTP & API → API keys. NOT the SMTP key (`xsmtpsib-…`).
	EnvBrevoAPIKey = "BREVO_API_KEY"
	// EnvBrevoAPIURL overrides the API base, for tests. Unset means Brevo.
	EnvBrevoAPIURL = "BREVO_API_URL"
)

// brevoDefaultURL is the production API base.
const brevoDefaultURL = "https://api.brevo.com/v3"

// BrevoConfig is what the Brevo sender needs.
type BrevoConfig struct {
	APIKey string
	// BaseURL defaults to brevoDefaultURL; must be https unless loopback.
	BaseURL string
	From    *mail.Address
	// HTTPClient defaults to one with a 20-second timeout.
	HTTPClient *http.Client
}

func brevoConfigFromEnv(from *mail.Address) (BrevoConfig, error) {
	cfg := BrevoConfig{
		APIKey:  strings.TrimSpace(os.Getenv(EnvBrevoAPIKey)),
		BaseURL: strings.TrimSpace(os.Getenv(EnvBrevoAPIURL)),
		From:    from,
	}
	return cfg, cfg.validate()
}

func (c *BrevoConfig) validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("%s is required for %s=brevo_api", EnvBrevoAPIKey, EnvProvider)
	}
	if c.From == nil {
		return fmt.Errorf("%s is required", EnvFrom)
	}
	if c.BaseURL == "" {
		c.BaseURL = brevoDefaultURL
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, got %q", EnvBrevoAPIURL, c.BaseURL)
	}
	host := parsed.Hostname()
	if parsed.Scheme != "https" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return fmt.Errorf("%s must use https (got %q); the API key travels in a header",
			EnvBrevoAPIURL, c.BaseURL)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return nil
}

// BrevoSender delivers through Brevo's transactional API.
type BrevoSender struct {
	cfg  BrevoConfig
	http *http.Client
}

// NewBrevo builds the sender. No network I/O: an unreachable API at startup
// is a delivery failure later, not a reason the API cannot boot.
func NewBrevo(cfg BrevoConfig) *BrevoSender {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &BrevoSender{cfg: cfg, http: client}
}

func (s *BrevoSender) Name() string { return "brevo_api" }

// brevoRequest is the subset of POST /v3/smtp/email this sender uses.
type brevoRequest struct {
	Sender      brevoAddress   `json:"sender"`
	To          []brevoAddress `json:"to"`
	Subject     string         `json:"subject"`
	TextContent string         `json:"textContent"`
	HTMLContent string         `json:"htmlContent,omitempty"`
	// Tags let the Brevo dashboard group these; not delivery-affecting.
	Tags []string `json:"tags,omitempty"`
}

type brevoAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Send delivers one message and returns nil once Brevo has accepted it.
//
// Brevo's own reason is kept in the error: "sender not valid", "unauthorized"
// and "daily limit reached" send an operator to three different places, and
// the log line is where they will read it.
func (s *BrevoSender) Send(ctx context.Context, m Message) error {
	if err := ValidateRecipient(m.To); err != nil {
		return err
	}

	body, err := json.Marshal(brevoRequest{
		Sender:      brevoAddress{Name: s.cfg.From.Name, Email: s.cfg.From.Address},
		To:          []brevoAddress{{Email: m.To}},
		Subject:     m.Subject,
		TextContent: m.Text,
		HTMLContent: m.HTML,
		Tags:        []string{"password-reset"},
	})
	if err != nil {
		return fmt.Errorf("brevo: encoding message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL+"/smtp/email",
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("brevo: building request: %w", err)
	}
	req.Header.Set("api-key", s.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("brevo: sending: %w", err)
	}
	defer res.Body.Close()

	// Brevo answers 201 for a queued message. Anything else carries a JSON
	// {code, message}; both are kept, bounded, so a log line names the cause
	// without ever echoing a whole error page.
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode == http.StatusCreated || res.StatusCode == http.StatusAccepted {
		return nil
	}

	var failure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &failure)
	switch {
	case failure.Code != "" || failure.Message != "":
		return fmt.Errorf("brevo: %d %s: %s", res.StatusCode, failure.Code, failure.Message)
	case len(raw) > 0:
		return fmt.Errorf("brevo: %d: %s", res.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	default:
		return errors.New("brevo: " + res.Status)
	}
}
