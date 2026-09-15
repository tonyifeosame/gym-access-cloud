package assistant

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Confirmation tokens.
//
// A consequential tool does not run when the model calls it. It records a
// confirmation -- the tool, the exact validated arguments, who was asked --
// and the operator is shown the consequence with a token. Approval presents
// the token back; the server checks it and runs the STORED arguments.
//
// THE TOKEN PROVES NOTHING THE ROW DOES NOT ALREADY SAY. It is an HMAC over
// the row's identity so that a token cannot be minted, moved to another
// conversation, session or user, or re-pointed at different arguments, without
// the server's key. Expiry and single use are properties of the ROW -- short
// (five minutes), because the consequence text describes the world as it was
// when the question was asked, and consumed atomically on settlement -- so
// neither needs to be in the signature.
//
// THE MODEL NEVER SEES A TOKEN. It goes from the server to the browser in the
// SSE event and comes back in the confirmation request; the model's tool
// result says only that a confirmation was requested.

const (
	confirmationTTL     = 5 * time.Minute
	confirmationVersion = "v1"
)

var (
	ErrConfirmationInvalid  = errors.New("confirmation token invalid")
	ErrConfirmationMismatch = errors.New("confirmation does not belong to this operator")
	ErrConfirmationPhrase   = errors.New("the confirmation phrase did not match")
)

func (s *Service) issueConfirmation(t *Turn, tool string, canonical json.RawMessage, plan *ConfirmationPlan) (*pendingConfirmation, error) {
	row, err := database.CreateAssistantConfirmation(models.AssistantConfirmation{
		ConversationID: t.ConversationID,
		CompanyID:      t.CompanyID,
		UserID:         t.UserID,
		SessionID:      t.SessionID,
		ToolName:       tool,
		Arguments:      canonical,
		ArgumentsHash:  hashArguments(canonical),
		PhraseRequired: plan.PhraseRequired,
	}, confirmationTTL)
	if err != nil {
		return nil, err
	}
	return &pendingConfirmation{
		ID:             row.ID,
		TokenID:        row.TokenID,
		Token:          s.signConfirmation(row),
		ExpiresAt:      row.ExpiresAt,
		ToolName:       tool,
		Arguments:      canonical,
		Consequence:    plan.Consequence,
		PhraseRequired: plan.PhraseRequired,
	}, nil
}

func confirmationMessage(c *models.AssistantConfirmation) []byte {
	return []byte(strings.Join([]string{
		c.TokenID,
		strconv.FormatInt(c.ConversationID, 10),
		strconv.FormatInt(c.UserID, 10),
		strconv.FormatInt(c.SessionID, 10),
		c.ToolName,
		c.ArgumentsHash,
	}, "|"))
}

func (s *Service) signConfirmation(c *models.AssistantConfirmation) string {
	mac := hmac.New(sha256.New, s.confirmationSecret)
	mac.Write(confirmationMessage(c))
	return confirmationVersion + "." + c.TokenID + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyConfirmation checks a presented token against the row it names and
// the operator presenting it. It does not consume the row.
func (s *Service) verifyConfirmation(t *Turn, token, phrase string) (*models.AssistantConfirmation, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != confirmationVersion {
		return nil, ErrConfirmationInvalid
	}
	row, err := database.GetAssistantConfirmation(t.CompanyID, parts[1])
	if err != nil {
		if errors.Is(err, database.ErrConfirmationNotFound) {
			return nil, ErrConfirmationInvalid
		}
		return nil, err
	}
	if !hmac.Equal([]byte(s.signConfirmation(row)), []byte(token)) {
		return nil, ErrConfirmationInvalid
	}
	if row.UserID != t.UserID || row.SessionID != t.SessionID || row.ConversationID != t.ConversationID {
		return nil, ErrConfirmationMismatch
	}
	if row.ConsumedAt != nil {
		return nil, database.ErrConfirmationSettled
	}
	if time.Now().After(row.ExpiresAt) {
		return nil, database.ErrConfirmationExpired
	}
	if row.PhraseRequired != "" && strings.TrimSpace(phrase) != row.PhraseRequired {
		return nil, ErrConfirmationPhrase
	}
	return row, nil
}

// describeConfirmationError turns a verification failure into the message
// the console shows.
func describeConfirmationError(err error) (int, string) {
	switch {
	case errors.Is(err, database.ErrConfirmationExpired):
		return 410, "That confirmation has expired. Ask the assistant again if you still want to do this."
	case errors.Is(err, database.ErrConfirmationSettled):
		return 409, "That confirmation has already been used."
	case errors.Is(err, ErrConfirmationMismatch):
		return 409, "That confirmation belongs to a different session."
	case errors.Is(err, ErrConfirmationPhrase):
		return 400, "The confirmation phrase did not match."
	case errors.Is(err, ErrConfirmationInvalid):
		return 400, "That confirmation is not valid."
	default:
		return 500, fmt.Sprintf("Could not check the confirmation: %v", err)
	}
}

// PreflightConfirmation checks a token without settling it, so the HTTP
// layer can refuse a bad one with a plain status before a stream opens.
func (s *Service) PreflightConfirmation(in TurnInput, token, phrase string) (int, string) {
	t := &Turn{
		ID:             newTurnID(),
		ConversationID: in.Conversation.ID,
		CompanyID:      in.Operator.CompanyID,
		UserID:         in.Operator.UserID,
		SessionID:      in.Operator.SessionID,
		ClientIP:       in.ClientIP,
		UserAgent:      in.UserAgent,
	}
	if _, err := s.verifyConfirmation(t, token, phrase); err != nil {
		if errors.Is(err, database.ErrConfirmationExpired) {
			s.expireConfirmation(t, token)
		}
		return describeConfirmationError(err)
	}
	return 0, ""
}

// expireConfirmation settles a lapsed row once, so the trail shows it.
func (s *Service) expireConfirmation(t *Turn, token string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return
	}
	row, err := database.GetAssistantConfirmation(t.CompanyID, parts[1])
	if err != nil || row.ConsumedAt != nil {
		return
	}
	if err := database.SettleAssistantConfirmation(row.ID, models.ConfirmationExpired); err == nil {
		s.recordSettlement(t, row, models.ToolCallConfirmationExpired)
	}
}
