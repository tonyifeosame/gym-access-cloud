// Package email is the platform's outbound mail.
//
// THERE WAS NONE. Every credential handover -- invitations, administrative
// resets, self-service resets -- minted a link and had nowhere to send it, and
// the handlers said so in their responses rather than pretending. This package
// is the delivery those notes were written around: one Sender interface, one
// message shape, and providers chosen by environment.
//
// WHAT A PROVIDER IS NOT ALLOWED TO DO: decide what is sent. Templates live
// with the feature that sends them (handlers/password_reset_email.go for the
// reset), and a provider moves bytes. Adding a provider is a file in this
// package and a case in FromEnv; nothing about a reset changes. brevo.go is
// exactly that: the deployment host cannot open SMTP at all, so the same
// message goes to Brevo over HTTPS instead.
//
// WHAT IS DELIBERATELY UNCONFIGURED BY DEFAULT: everything. With EMAIL_PROVIDER
// unset, FromEnv returns ErrNotConfigured and the platform behaves exactly as
// it did before this package existed. A deployment has to say which provider
// it wants, and a partly configured one is refused at startup rather than
// discovered by the first operator who cannot get back in.
package email

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
)

// Environment variables shared by every provider.
const (
	// EnvProvider selects the provider: "smtp", "brevo_api" or "log". Unset
	// means none.
	EnvProvider = "EMAIL_PROVIDER"
	// EnvFrom is the sender address, optionally with a display name:
	// `AccessLink <no-reply@example.com>`.
	EnvFrom = "EMAIL_FROM"
)

// ErrNotConfigured is returned by FromEnv when no provider is selected. The
// feature is off; that is a valid state and not an error to log as one.
var ErrNotConfigured = errors.New("email delivery is not configured")

// Message is one email. Text is required; HTML is optional and, when present,
// is sent as the alternative part so a client that can render it does.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender delivers messages. Implementations must be safe for concurrent use:
// the reset handler sends from a goroutine so that delivery latency does not
// reach the response.
type Sender interface {
	Send(ctx context.Context, m Message) error
	// Name identifies the provider in logs.
	Name() string
}

// FromEnv builds the configured sender.
func FromEnv() (Sender, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv(EnvProvider)))
	if provider == "" {
		return nil, ErrNotConfigured
	}

	from, err := parseFrom(os.Getenv(EnvFrom))
	if err != nil {
		return nil, err
	}

	switch provider {
	case "smtp":
		cfg, err := smtpConfigFromEnv(from)
		if err != nil {
			return nil, err
		}
		return NewSMTP(cfg), nil
	case "brevo_api":
		cfg, err := brevoConfigFromEnv(from)
		if err != nil {
			return nil, err
		}
		return NewBrevo(cfg), nil
	case "log":
		return NewLog(from), nil
	default:
		return nil, fmt.Errorf("%s=%q is not a known provider (smtp, brevo_api, log)", EnvProvider, provider)
	}
}

// parseFrom validates the sender address and returns it as an RFC 5322 address.
func parseFrom(raw string) (*mail.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s is required when %s is set", EnvFrom, EnvProvider)
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvFrom, err)
	}
	return addr, nil
}

// ValidateRecipient refuses an address the transport could not use, so a bad
// one fails here rather than as a bounce nobody reads.
func ValidateRecipient(to string) error {
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("recipient is required")
	}
	addr, err := mail.ParseAddress(to)
	if err != nil || addr.Address != to {
		return fmt.Errorf("recipient %q is not a bare email address", to)
	}
	return nil
}
