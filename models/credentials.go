package models

import (
	"errors"
	"time"
)

// Operator credential handover (migrations/017_operator_credentials.sql).
//
// The answer to PPL-02 and SEC-10, which are one mechanism rather than two: an
// administrator must be able to give somebody an account, and get somebody back
// into one, WITHOUT ever knowing their password. Both are a single-use,
// short-lived, high-entropy token that authorises setting a credential without
// presenting the current one.
//
// Nothing here stores a plaintext token. Only a SHA-256 is persisted, exactly as
// site keys and device credentials already are, so a database disclosure hands
// an attacker no usable link.

// Token purposes. An invitation and a reset are the same mechanism -- a
// single-use, short-lived, high-entropy token authorising a password set without
// knowing the current one -- and differ only in what the account looks like
// beforehand.
const (
	TokenPurposeInvite = "INVITE"
	TokenPurposeReset  = "RESET"
)

// Credential token errors.
var (
	ErrTokenNotFound = errors.New("that link is not valid")
	ErrTokenExpired  = errors.New("that link has expired")
	ErrTokenRedeemed = errors.New("that link has already been used")
)

// CredentialToken is a minted invitation or reset, as returned ONCE to the
// caller that created it.
//
// Token is the plaintext and exists here and nowhere else -- only its SHA-256 is
// stored, so nothing can read it back. Same contract as a site key or a device
// credential, and the response type says so.
type CredentialToken struct {
	Token     string    `json:"token"`
	Purpose   string    `json:"purpose"`
	ExpiresAt time.Time `json:"expires_at"`

	// ShownOnce is stated in the payload rather than left to documentation,
	// because a client that stores it is the failure mode.
	ShownOnce bool `json:"shown_once"`
}

// RedeemTokenRequest sets a password using an invitation or reset link.
//
// No current password: the token IS the authorisation. That is the whole point
// of the mechanism, and why it is single-use and short-lived.
type RedeemTokenRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}
