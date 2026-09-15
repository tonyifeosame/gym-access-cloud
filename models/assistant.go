package models

import (
	"encoding/json"
	"time"
)

// The in-console assistant's wire shapes (Phase 1).
//
// WHAT THESE ARE NOT. None of these types carries a credential, a secret, a
// raw API body or anything the model could use to reach the API on its own.
// The assistant acts as the signed-in operator through the console's own
// routes; these types describe conversations, what the model asked to do, and
// what a person confirmed -- the record, not a capability.

// Conversation statuses.
const (
	ConversationOpen    = "OPEN"
	ConversationClosed  = "CLOSED"
	ConversationExpired = "EXPIRED"
)

// Tool call outcomes, as stored in assistant_tool_calls.status.
const (
	ToolCallExecuted              = "EXECUTED"
	ToolCallRefusedRole           = "REFUSED_ROLE"
	ToolCallRefusedScope          = "REFUSED_SCOPE"
	ToolCallNotFound              = "NOT_FOUND"
	ToolCallInvalid               = "INVALID"
	ToolCallFailed                = "FAILED"
	ToolCallConfirmationRequested = "CONFIRMATION_REQUESTED"
	ToolCallConfirmedExecuted     = "CONFIRMED_EXECUTED"
	ToolCallConfirmationExpired   = "CONFIRMATION_EXPIRED"
	ToolCallConfirmationRejected  = "CONFIRMATION_REJECTED"
)

// Confirmation outcomes.
const (
	ConfirmationApproved = "APPROVED"
	ConfirmationRejected = "REJECTED"
	ConfirmationExpired  = "EXPIRED"
)

// AssistantConversation is one operator's conversation with the assistant.
type AssistantConversation struct {
	ID        int64  `json:"-"`
	PublicID  string `json:"id"`
	CompanyID int64  `json:"-"`
	UserID    int64  `json:"-"`

	Title      string `json:"title"`
	Status     string `json:"status"`
	Model      string `json:"model"`
	PromptHash string `json:"-"`
	TurnCount  int    `json:"turn_count"`

	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`

	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastMessageAt time.Time `json:"last_message_at"`
}

// AssistantBlock is the assistant's own, model-neutral representation of one
// content block. It is what is persisted and what is replayed to the model;
// it is deliberately not the SDK's type, so the stored transcript does not
// change shape when the SDK does.
type AssistantBlock struct {
	Type string `json:"type"` // text | tool_use | tool_result | thinking

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// thinking (kept so an interrupted turn can be replayed on the same model)
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// AssistantMessage is one persisted transcript entry.
type AssistantMessage struct {
	Seq       int              `json:"seq"`
	Role      string           `json:"role"`
	Blocks    []AssistantBlock `json:"blocks"`
	CreatedAt time.Time        `json:"created_at"`
}

// AssistantConfirmation is a pending or settled confirmation.
type AssistantConfirmation struct {
	ID             int64
	TokenID        string
	ConversationID int64
	CompanyID      int64
	UserID         int64
	SessionID      int64
	ToolName       string
	Arguments      json.RawMessage
	ArgumentsHash  string
	PhraseRequired string
	IssuedAt       time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	Outcome        string
}

// AssistantToolCallRecord is what is written for every tool the model asked
// to run, whatever became of it.
type AssistantToolCallRecord struct {
	ConversationID int64
	TurnID         string
	CompanyID      int64
	UserID         int64
	SessionID      int64
	ToolName       string
	Arguments      json.RawMessage
	Route          string
	RequestID      string
	Status         string
	HTTPStatus     int
	ConfirmationID int64
	ResultDigest   string
	DurationMS     int
	ClientIP       string
	UserAgent      string
}

// AssistantUsage is one model call's token accounting.
type AssistantUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Add accumulates another call's usage.
func (u *AssistantUsage) Add(other AssistantUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
}

// Total is the figure the company budget is measured against.
func (u AssistantUsage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// AssistantCompanyUsage is a company's ledger row for one month.
type AssistantCompanyUsage struct {
	CompanyID int64
	Period    time.Time
	AssistantUsage
	Turns     int
	ToolCalls int
}

// --- requests and responses --------------------------------------------------

// AssistantCapabilities tells the console whether to offer the assistant.
type AssistantCapabilities struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
	// Tools this operator's role can see, by name, so the console can say what
	// the assistant is able to do for them.
	Tools []string `json:"tools,omitempty"`
}

// AssistantMessageRequest is one user turn.
type AssistantMessageRequest struct {
	Text string `json:"text" binding:"required"`
	// ClientMessageID lets a client that lost the stream re-send the same turn
	// and receive the persisted outcome rather than running it again.
	ClientMessageID string `json:"client_message_id,omitempty"`
}

// AssistantConfirmationRequest settles a pending confirmation.
type AssistantConfirmationRequest struct {
	Token   string `json:"token" binding:"required"`
	Approve bool   `json:"approve"`
	Phrase  string `json:"phrase,omitempty"`
}

// AssistantConsequence is the wording a person reads before approving.
type AssistantConsequence struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Warnings []string `json:"warnings,omitempty"`
}

// AssistantHandoff points the console at the existing screen that finishes
// what the assistant started -- fingerprint capture, for one.
type AssistantHandoff struct {
	Kind  string `json:"kind"`
	Route string `json:"route"`
	Label string `json:"label"`
}
