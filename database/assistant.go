package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"access-terminal-cloud-api/models"
)

// Storage for the in-console assistant (migrations/036_assistant.sql).
//
// EVERY READ IS SCOPED BY COMPANY AND USER. A conversation belongs to one
// operator in one company, and there is no route that reads another
// operator's -- an ADMIN reads the effects of the assistant's actions in the
// audit trail, like any other action, not the conversation that led to them.

var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrConfirmationNotFound = errors.New("confirmation not found")
	ErrConfirmationSettled  = errors.New("confirmation already settled")
	ErrConfirmationExpired  = errors.New("confirmation expired")
)

const assistantConversationColumns = `
	id, public_id::text, company_id, user_id, title, status, model, prompt_hash,
	turn_count, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
	created_at, updated_at, last_message_at`

func scanConversation(row scannable) (*models.AssistantConversation, error) {
	var c models.AssistantConversation
	err := row.Scan(&c.ID, &c.PublicID, &c.CompanyID, &c.UserID, &c.Title, &c.Status,
		&c.Model, &c.PromptHash, &c.TurnCount, &c.InputTokens, &c.OutputTokens,
		&c.CacheReadTokens, &c.CacheWriteTokens, &c.CreatedAt, &c.UpdatedAt,
		&c.LastMessageAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateAssistantConversation opens a conversation for one operator.
func CreateAssistantConversation(companyID, userID int64, model, promptHash string) (*models.AssistantConversation, error) {
	return scanConversation(DB.QueryRow(`
		INSERT INTO assistant_conversations (company_id, user_id, model, prompt_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING `+assistantConversationColumns, companyID, userID, model, promptHash))
}

// GetAssistantConversation reads one conversation, scoped to its owner.
func GetAssistantConversation(companyID, userID int64, publicID string) (*models.AssistantConversation, error) {
	if !looksLikeUUID(publicID) {
		return nil, ErrConversationNotFound
	}
	c, err := scanConversation(DB.QueryRow(`
		SELECT `+assistantConversationColumns+`
		  FROM assistant_conversations
		 WHERE public_id = $1 AND company_id = $2 AND user_id = $3`,
		publicID, companyID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	return c, err
}

// ListAssistantConversations lists an operator's own conversations, newest first.
func ListAssistantConversations(companyID, userID int64, limit int) ([]models.AssistantConversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := DB.Query(`
		SELECT `+assistantConversationColumns+`
		  FROM assistant_conversations
		 WHERE company_id = $1 AND user_id = $2
		 ORDER BY last_message_at DESC, id DESC
		 LIMIT $3`, companyID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AssistantConversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// SetAssistantConversationTitle records the title once, from the first message.
func SetAssistantConversationTitle(id int64, title string) error {
	if len(title) > 120 {
		title = title[:120]
	}
	_, err := DB.Exec(`UPDATE assistant_conversations SET title = $2, updated_at = CURRENT_TIMESTAMP
	                    WHERE id = $1 AND title = ''`, id, title)
	return err
}

// CloseAssistantConversation marks a conversation closed (it stays readable).
func CloseAssistantConversation(id int64, status string) error {
	_, err := DB.Exec(`UPDATE assistant_conversations SET status = $2, updated_at = CURRENT_TIMESTAMP
	                    WHERE id = $1`, id, status)
	return err
}

// RecordAssistantTurn adds one turn's token usage to the conversation and to
// the company's monthly ledger.
func RecordAssistantTurn(conversationID, companyID int64, usage models.AssistantUsage, toolCalls int) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		UPDATE assistant_conversations
		   SET turn_count = turn_count + 1,
		       input_tokens = input_tokens + $2,
		       output_tokens = output_tokens + $3,
		       cache_read_tokens = cache_read_tokens + $4,
		       cache_write_tokens = cache_write_tokens + $5,
		       updated_at = CURRENT_TIMESTAMP,
		       last_message_at = CURRENT_TIMESTAMP
		 WHERE id = $1`,
		conversationID, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens,
		usage.CacheWriteTokens); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO assistant_company_usage
		       (company_id, period, input_tokens, output_tokens, cache_read_tokens,
		        cache_write_tokens, turns, tool_calls)
		VALUES ($1, date_trunc('month', CURRENT_DATE)::date, $2, $3, $4, $5, 1, $6)
		ON CONFLICT (company_id, period) DO UPDATE
		   SET input_tokens = assistant_company_usage.input_tokens + EXCLUDED.input_tokens,
		       output_tokens = assistant_company_usage.output_tokens + EXCLUDED.output_tokens,
		       cache_read_tokens = assistant_company_usage.cache_read_tokens + EXCLUDED.cache_read_tokens,
		       cache_write_tokens = assistant_company_usage.cache_write_tokens + EXCLUDED.cache_write_tokens,
		       turns = assistant_company_usage.turns + 1,
		       tool_calls = assistant_company_usage.tool_calls + EXCLUDED.tool_calls,
		       updated_at = CURRENT_TIMESTAMP`,
		companyID, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens,
		usage.CacheWriteTokens, toolCalls); err != nil {
		return err
	}
	return tx.Commit()
}

// AssistantUsageThisMonth reads a company's ledger for the current month.
func AssistantUsageThisMonth(companyID int64) (models.AssistantCompanyUsage, error) {
	var u models.AssistantCompanyUsage
	u.CompanyID = companyID
	err := DB.QueryRow(`
		SELECT period, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       turns, tool_calls
		  FROM assistant_company_usage
		 WHERE company_id = $1 AND period = date_trunc('month', CURRENT_DATE)::date`,
		companyID).Scan(&u.Period, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens,
		&u.CacheWriteTokens, &u.Turns, &u.ToolCalls)
	if errors.Is(err, sql.ErrNoRows) {
		return u, nil
	}
	return u, err
}

// --- messages ------------------------------------------------------------------

// AppendAssistantMessage persists one transcript entry and returns its seq.
// clientMessageID is recorded for user messages a client may re-send.
func AppendAssistantMessage(conversationID int64, role string, blocks []models.AssistantBlock, clientMessageID string) (int, error) {
	content, err := json.Marshal(blocks)
	if err != nil {
		return 0, err
	}
	var seq int
	err = DB.QueryRow(`
		INSERT INTO assistant_messages (conversation_id, seq, role, content, client_message_id)
		VALUES ($1,
		        (SELECT COALESCE(MAX(seq), 0) + 1 FROM assistant_messages WHERE conversation_id = $1),
		        $2, $3, NULLIF($4, ''))
		RETURNING seq`, conversationID, role, content, clientMessageID).Scan(&seq)
	return seq, err
}

// FindAssistantMessageSeq returns the seq of the user message a client sent
// under this id, or 0 when there is none.
func FindAssistantMessageSeq(conversationID int64, clientMessageID string) (int, error) {
	if clientMessageID == "" {
		return 0, nil
	}
	var seq int
	err := DB.QueryRow(`SELECT seq FROM assistant_messages
	                     WHERE conversation_id = $1 AND client_message_id = $2`,
		conversationID, clientMessageID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// ListAssistantMessages reads a conversation's transcript in order.
func ListAssistantMessages(conversationID int64) ([]models.AssistantMessage, error) {
	rows, err := DB.Query(`
		SELECT seq, role, content, created_at
		  FROM assistant_messages
		 WHERE conversation_id = $1
		 ORDER BY seq`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AssistantMessage{}
	for rows.Next() {
		var m models.AssistantMessage
		var content []byte
		if err := rows.Scan(&m.Seq, &m.Role, &content, &m.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(content, &m.Blocks); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- tool calls ------------------------------------------------------------------

// RecordAssistantToolCall writes the record of one tool call. It never fails
// the caller: like the audit writer, a lost record is logged loudly rather
// than turning a completed action into a reported failure.
func RecordAssistantToolCall(record models.AssistantToolCallRecord) (int64, error) {
	args := record.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var id int64
	err := DB.QueryRow(`
		INSERT INTO assistant_tool_calls
		       (conversation_id, turn_id, company_id, user_id, session_id, tool_name,
		        arguments, route, request_id, status, http_status, confirmation_id,
		        result_digest, duration_ms, client_ip, user_agent)
		VALUES (NULLIF($1, 0), $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, 0),
		        NULLIF($12, 0), NULLIF($13, ''), $14, $15, $16)
		RETURNING id`,
		record.ConversationID, record.TurnID, record.CompanyID, record.UserID,
		record.SessionID, record.ToolName, []byte(args), record.Route, record.RequestID,
		record.Status, record.HTTPStatus, record.ConfirmationID, record.ResultDigest,
		record.DurationMS, record.ClientIP, truncateString(record.UserAgent, 512)).Scan(&id)
	return id, err
}

// CountAssistantToolCalls is what the tests read.
func CountAssistantToolCalls(companyID int64, status string) (int, error) {
	var n int
	err := DB.QueryRow(`SELECT count(*) FROM assistant_tool_calls
	                     WHERE company_id = $1 AND ($2 = '' OR status = $2)`, companyID, status).Scan(&n)
	return n, err
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// --- confirmations -----------------------------------------------------------------

const assistantConfirmationColumns = `
	id, token_id::text, COALESCE(conversation_id, 0), company_id, user_id, session_id,
	tool_name, arguments, arguments_hash, COALESCE(phrase_required, ''),
	issued_at, expires_at, consumed_at, COALESCE(outcome, '')`

func scanConfirmation(row scannable) (*models.AssistantConfirmation, error) {
	var c models.AssistantConfirmation
	var args []byte
	var consumed sql.NullTime
	err := row.Scan(&c.ID, &c.TokenID, &c.ConversationID, &c.CompanyID, &c.UserID,
		&c.SessionID, &c.ToolName, &args, &c.ArgumentsHash, &c.PhraseRequired,
		&c.IssuedAt, &c.ExpiresAt, &consumed, &c.Outcome)
	if err != nil {
		return nil, err
	}
	c.Arguments = json.RawMessage(args)
	if consumed.Valid {
		c.ConsumedAt = &consumed.Time
	}
	return &c, nil
}

// CreateAssistantConfirmation records what a consequential tool would run
// with, before anybody is asked.
func CreateAssistantConfirmation(c models.AssistantConfirmation, ttl time.Duration) (*models.AssistantConfirmation, error) {
	return scanConfirmation(DB.QueryRow(`
		INSERT INTO assistant_confirmations
		       (conversation_id, company_id, user_id, session_id, tool_name, arguments,
		        arguments_hash, phrase_required, expires_at)
		VALUES (NULLIF($1, 0), $2, $3, $4, $5, $6, $7, NULLIF($8, ''),
		        CURRENT_TIMESTAMP + ($9 * interval '1 second'))
		RETURNING `+assistantConfirmationColumns,
		c.ConversationID, c.CompanyID, c.UserID, c.SessionID, c.ToolName,
		[]byte(c.Arguments), c.ArgumentsHash, c.PhraseRequired, int(ttl.Seconds())))
}

// GetAssistantConfirmation reads one by its token id, scoped to the company.
func GetAssistantConfirmation(companyID int64, tokenID string) (*models.AssistantConfirmation, error) {
	if !looksLikeUUID(tokenID) {
		return nil, ErrConfirmationNotFound
	}
	c, err := scanConfirmation(DB.QueryRow(`
		SELECT `+assistantConfirmationColumns+`
		  FROM assistant_confirmations
		 WHERE token_id = $1 AND company_id = $2`, tokenID, companyID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConfirmationNotFound
	}
	return c, err
}

// SettleAssistantConfirmation consumes a confirmation exactly once.
//
// THE CHECK IS IN THE UPDATE. Two approvals racing for the same token both
// read an unconsumed row; only one of them updates it, because the predicate
// requires consumed_at to still be NULL. The loser gets ErrConfirmationSettled.
func SettleAssistantConfirmation(id int64, outcome string) error {
	res, err := DB.Exec(`
		UPDATE assistant_confirmations
		   SET consumed_at = CURRENT_TIMESTAMP, outcome = $2
		 WHERE id = $1 AND consumed_at IS NULL`, id, outcome)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrConfirmationSettled
	}
	return nil
}

// --- retention -------------------------------------------------------------------

// PurgeAssistantConversationsContext deletes conversations idle for longer
// than the retention window. Messages go with them; tool calls and
// confirmations keep their rows with the conversation reference nulled.
func PurgeAssistantConversationsContext(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	res, err := DB.ExecContext(ctx, `
		DELETE FROM assistant_conversations
		 WHERE last_message_at < CURRENT_TIMESTAMP - ($1 * interval '1 day')`, retentionDays)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
