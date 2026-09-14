package handlers

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/url"
	"strings"
	"time"

	"access-terminal-cloud-api/email"
	"access-terminal-cloud-api/middleware"

	"github.com/gin-gonic/gin"
)

// Delivery of the self-service password reset.
//
// THIS IS THE CHANGE handlers/credentials.go SAID WOULD BE "A CHANGE IN THIS
// FILE AND NOWHERE ELSE" once delivery existed. The token is still minted by the
// same store, still single-use, still expires on the same clock. What changes is
// where it goes: into an email to the address that asked, instead of into the
// operational log for somebody with server access to act on.
//
// ---------------------------------------------------------------------------
// WHY THE SEND IS ASYNCHRONOUS
// ---------------------------------------------------------------------------
//
// The endpoint answers 202 with an identical body whether or not the address
// has an account. That symmetry is worth nothing if the RESPONSE TIME differs:
// an SMTP conversation takes hundreds of milliseconds and happens only for an
// address that exists, so a synchronous send would let anybody measure which
// addresses are accounts. The send runs in a goroutine with its own deadline,
// and the response leaves at the same moment on both paths. A delivery failure
// is logged with the request id; it cannot change the answer.
//
// ---------------------------------------------------------------------------
// WHAT THE EMAIL CONTAINS
// ---------------------------------------------------------------------------
//
// The link, how long it lasts, and what to do if it was not you. It does NOT
// name the company, the operator's role or anything else about the account:
// the message is sent to whoever controls the mailbox, and that is not always
// the operator.

var resetSender email.Sender

// ConfigurePasswordResetDelivery installs the sender, or disables delivery
// with nil -- in which case the reset request logs the link, exactly as it did
// before delivery existed.
func ConfigurePasswordResetDelivery(sender email.Sender) { resetSender = sender }

// PasswordResetDeliveryEnabled reports whether a reset produces an email.
func PasswordResetDeliveryEnabled() bool { return resetSender != nil }

// resetDeliveryTimeout bounds one send. Longer than an SMTP conversation should
// take, short enough that a hung relay releases the goroutine.
const resetDeliveryTimeout = 30 * time.Second

// resetLink is the URL the email carries. The console strips the token from
// the address bar on arrival (see RedeemPage); this is where it is put there.
func resetLink(token string) string {
	return consoleLink("/redeem?token=" + url.QueryEscape(token))
}

// passwordResetMessage renders the email. Pure, so the wording can be asserted
// without a transport.
func passwordResetMessage(to, token string, lifetime time.Duration) email.Message {
	link := resetLink(token)
	window := describeWindow(lifetime)

	text := strings.Join([]string{
		"Somebody asked to reset the password for the AccessLink account at " + to + ".",
		"",
		"To choose a new password, open this link:",
		"",
		"    " + link,
		"",
		"The link works once and expires in " + window + ".",
		"",
		"If you did not ask for this, you can ignore this message. Your password has " +
			"not changed, and the link will expire on its own.",
		"",
		"— AccessLink",
	}, "\n")

	htmlBody := strings.Join([]string{
		`<!doctype html><html><body style="font-family:system-ui,sans-serif;line-height:1.5;color:#1b1b1b">`,
		`<p>Somebody asked to reset the password for the AccessLink account at <strong>` +
			html.EscapeString(to) + `</strong>.</p>`,
		`<p><a href="` + html.EscapeString(link) + `" style="display:inline-block;padding:10px 16px;` +
			`background:#1f4fd8;color:#fff;text-decoration:none;border-radius:6px">Choose a new password</a></p>`,
		`<p style="color:#555">Or copy this address into your browser:<br>` +
			`<code>` + html.EscapeString(link) + `</code></p>`,
		`<p>The link works once and expires in ` + html.EscapeString(window) + `.</p>`,
		`<p>If you did not ask for this, you can ignore this message. Your password has ` +
			`not changed, and the link will expire on its own.</p>`,
		`<p>— AccessLink</p>`,
		`</body></html>`,
	}, "\n")

	return email.Message{
		To:      to,
		Subject: "Reset your AccessLink password",
		Text:    text,
		HTML:    htmlBody,
	}
}

// describeWindow says how long a link lasts, in the coarsest unit that is
// still accurate. "1 hour" reads better than "3600 seconds", and a reader
// deciding whether to act now does not need the seconds.
func describeWindow(d time.Duration) string {
	switch {
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "1 hour"
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d >= time.Minute:
		return "1 minute"
	default:
		return "less than a minute"
	}
}

// deliverPasswordReset sends the email in the background.
//
// Everything it needs is copied out of the request before the goroutine
// starts: a gin.Context is not valid once the handler returns, and reading it
// from another goroutine is the bug the gin documentation warns about first.
func deliverPasswordReset(c *gin.Context, to, token string, lifetime time.Duration) {
	requestID := middleware.RequestID(c)
	message := passwordResetMessage(to, token, lifetime)
	sender := resetSender

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), resetDeliveryTimeout)
		defer cancel()

		if err := sender.Send(ctx, message); err != nil {
			// The address is logged redacted, as the failed-login line does:
			// enough to correlate, not enough to harvest.
			log.Printf("request_id=%s auth=reset-delivery-failed provider=%s email=%s: %v",
				requestID, sender.Name(), redactEmail(to), err)
			return
		}
		log.Printf("request_id=%s auth=reset-delivered provider=%s email=%s",
			requestID, sender.Name(), redactEmail(to))
	}()
}
