package email

import (
	"context"
	"log"
	"net/mail"
)

// LogSender writes each message to the process log instead of sending it.
//
// FOR DEVELOPMENT ONLY, and named so nobody mistakes it for delivery. It exists
// so that a developer can exercise the reset flow end to end -- request a
// reset, copy the link out of the terminal, redeem it -- without a relay. The
// body of a reset email contains the link, so this sender puts a secret in the
// log, exactly as the pre-delivery handler did; a production deployment that
// selects it has selected the old behaviour with more words.
type LogSender struct {
	from *mail.Address
}

// NewLog builds the sender.
func NewLog(from *mail.Address) *LogSender { return &LogSender{from: from} }

func (s *LogSender) Name() string { return "log" }

// Send logs the message.
func (s *LogSender) Send(_ context.Context, m Message) error {
	if err := ValidateRecipient(m.To); err != nil {
		return err
	}
	log.Printf("email provider=log NOT DELIVERED\nFrom: %s\nTo: %s\nSubject: %s\n\n%s",
		s.from, m.To, m.Subject, m.Text)
	return nil
}
