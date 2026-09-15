package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// Limits bound what one turn, one operator and one company may spend.
type Limits struct {
	MaxToolRounds        int
	MaxToolCalls         int
	TurnTimeout          time.Duration
	MaxTokensPerCall     int
	CompanyMonthlyTokens int64
	RetentionDays        int
}

// DefaultLimits are the Phase 1 defaults; LimitsFromEnv overrides them.
func DefaultLimits() Limits {
	return Limits{
		MaxToolRounds:        12,
		MaxToolCalls:         24,
		TurnTimeout:          180 * time.Second,
		MaxTokensPerCall:     8000,
		CompanyMonthlyTokens: 2_000_000,
		RetentionDays:        30,
	}
}

// LimitsFromEnv reads the ASSISTANT_* overrides.
func LimitsFromEnv() Limits {
	l := DefaultLimits()
	if n := envInt("ASSISTANT_MAX_TOOL_ROUNDS"); n > 0 {
		l.MaxToolRounds = n
	}
	if n := envInt("ASSISTANT_MAX_TOOL_CALLS"); n > 0 {
		l.MaxToolCalls = n
	}
	if n := envInt("ASSISTANT_TURN_TIMEOUT_SECONDS"); n > 0 {
		l.TurnTimeout = time.Duration(n) * time.Second
	}
	if n := envInt("ASSISTANT_COMPANY_MONTHLY_TOKENS"); n > 0 {
		l.CompanyMonthlyTokens = int64(n)
	}
	if n := envInt("ASSISTANT_RETENTION_DAYS"); n > 0 {
		l.RetentionDays = n
	}
	return l
}

func envInt(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return 0
	}
	return n
}

// Options assemble a Service.
type Options struct {
	// Router is the console's own engine; every tool call goes through it.
	Router http.Handler
	// Model is nil when the deployment has no model, which disables the
	// assistant whatever Enabled says.
	Model Model
	// Enabled is ASSISTANT_ENABLED. False by default, everywhere.
	Enabled bool
	// ConfirmationSecret signs confirmation tokens. Required when enabled.
	ConfirmationSecret []byte
	Limits             Limits
}

// Service is the assistant.
type Service struct {
	registry           *Registry
	router             http.Handler
	model              Model
	enabled            bool
	confirmationSecret []byte
	limits             Limits

	mu     sync.Mutex
	active map[int64]bool // user ids with a turn in progress
}

// New builds the service with the Phase 1 tools registered.
func New(opts Options) *Service {
	s := &Service{
		registry:           NewRegistry(),
		router:             opts.Router,
		model:              opts.Model,
		enabled:            opts.Enabled && opts.Model != nil && opts.Router != nil,
		confirmationSecret: opts.ConfirmationSecret,
		limits:             opts.Limits,
		active:             map[int64]bool{},
	}
	if s.limits.MaxToolRounds == 0 {
		s.limits = DefaultLimits()
	}
	if s.enabled && len(s.confirmationSecret) < 16 {
		log.Printf("assistant: ASSISTANT_CONFIRMATION_SECRET is missing or too short; the assistant stays disabled")
		s.enabled = false
	}
	registerPhase1Tools(s.registry)
	return s
}

// Enabled reports whether the assistant answers at all.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Registry exposes the tools (the tests read it).
func (s *Service) Registry() *Registry { return s.registry }

// Limits exposes the configured limits.
func (s *Service) Limits() Limits { return s.limits }

// ModelName is the configured model id.
func (s *Service) ModelName() string {
	if s.model == nil {
		return ""
	}
	return s.model.Name()
}

// Capabilities is what the console reads to decide whether to offer the
// assistant, and what it can do for this role.
func (s *Service) Capabilities(role string) models.AssistantCapabilities {
	if !s.Enabled() {
		return models.AssistantCapabilities{Enabled: false}
	}
	return models.AssistantCapabilities{
		Enabled: true,
		Model:   s.model.Name(),
		Tools:   s.registry.Names(role),
	}
}

// BeginTurn claims the one-turn-at-a-time slot for an operator. The returned
// function releases it; ok is false when a turn is already running.
func (s *Service) BeginTurn(userID int64) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[userID] {
		return nil, false
	}
	s.active[userID] = true
	return func() {
		s.mu.Lock()
		delete(s.active, userID)
		s.mu.Unlock()
	}, true
}

// Operator is who is asking.
type Operator struct {
	UserID      int64
	SessionID   int64
	Role        string
	FullName    string
	CompanyID   int64
	CompanyName string
}

// TurnInput is one user message, or a continuation after a confirmation.
type TurnInput struct {
	Conversation *models.AssistantConversation
	Operator     Operator
	Cookie       string
	CSRF         string
	ClientIP     string
	UserAgent    string

	// UserText is the operator's message. Empty for a continuation turn,
	// where the transcript already ends with what the model must answer.
	UserText        string
	ClientMessageID string
}

// Failure codes on turn.failed.
const (
	FailSessionExpired   = "session_expired"
	FailRateLimited      = "rate_limited"
	FailBudgetExhausted  = "budget_exhausted"
	FailToolLimit        = "tool_limit"
	FailTimeout          = "timeout"
	FailModelUnavailable = "model_unavailable"
	FailRefused          = "refused"
	FailInternal         = "internal"
)

type turnFailure struct {
	code      string
	message   string
	retryable bool
}

// RunTurn handles one user message end to end, emitting events as it goes.
//
// THE SHAPE OF A TURN: persist the user message; then up to MaxToolRounds
// rounds of "call the model, run the tools it asked for"; then persist the
// usage. Every tool call is executed through the router as the operator
// (dispatch.go). A consequential tool pauses the turn: its result tells the
// model a confirmation was requested, the operator is shown the card, and
// the model is expected to say so and stop. If it calls tools again after
// that, they are refused and the turn ends.
func (s *Service) RunTurn(parent context.Context, in TurnInput, emit Emitter) {
	turnID := newTurnID()
	ctx, cancel := context.WithTimeout(parent, s.limits.TurnTimeout)
	defer cancel()

	t := &Turn{
		ID:             turnID,
		ConversationID: in.Conversation.ID,
		CompanyID:      in.Operator.CompanyID,
		UserID:         in.Operator.UserID,
		SessionID:      in.Operator.SessionID,
		Role:           in.Operator.Role,
		ClientIP:       in.ClientIP,
		UserAgent:      in.UserAgent,
		cookie:         in.Cookie,
		csrf:           in.CSRF,
		router:         s.router,
		ctx:            ctx,
	}

	emit.Emit(EventTurnStarted, map[string]any{
		"turn_id":         turnID,
		"conversation_id": in.Conversation.PublicID,
	})

	fail := func(f turnFailure) {
		emit.Emit(EventTurnFailed, map[string]any{
			"turn_id":   turnID,
			"code":      f.code,
			"message":   f.message,
			"retryable": f.retryable,
		})
	}

	// Budget, before anything is spent.
	if over, err := s.overBudget(in.Operator.CompanyID); err != nil {
		log.Printf("assistant: reading usage: %v", err)
		fail(turnFailure{FailInternal, "The assistant could not check its budget.", true})
		return
	} else if over {
		fail(turnFailure{FailBudgetExhausted, "Your company has used its assistant allowance for this month.", false})
		return
	}

	// Replay: the same client message again means the stream was lost. Hand
	// back what was persisted rather than run the turn twice.
	if in.ClientMessageID != "" {
		seq, err := database.FindAssistantMessageSeq(in.Conversation.ID, in.ClientMessageID)
		if err != nil {
			fail(turnFailure{FailInternal, "The conversation could not be read.", true})
			return
		}
		if seq > 0 {
			s.replay(in.Conversation.ID, seq, turnID, emit)
			return
		}
	}

	if in.UserText != "" {
		text := fmt.Sprintf("[Today is %s, UTC.]\n\n%s", time.Now().UTC().Format("Monday 2 January 2006"), in.UserText)
		if _, err := database.AppendAssistantMessage(in.Conversation.ID, "user",
			[]models.AssistantBlock{{Type: "text", Text: text}}, in.ClientMessageID); err != nil {
			fail(turnFailure{FailInternal, "The message could not be saved.", true})
			return
		}
		_ = database.SetAssistantConversationTitle(in.Conversation.ID, firstLine(in.UserText))
	}

	transcript, err := database.ListAssistantMessages(in.Conversation.ID)
	if err != nil {
		fail(turnFailure{FailInternal, "The conversation could not be read.", true})
		return
	}

	system := SystemPrompt(in.Operator.FullName, in.Operator.Role, in.Operator.CompanyName)
	tools := s.registry.ForRole(in.Operator.Role)

	var usage models.AssistantUsage
	toolCalls := 0
	awaitingConfirmation := false
	stopReason := "end_turn"
	var failure *turnFailure

rounds:
	for round := 0; round < s.limits.MaxToolRounds; round++ {
		resp, err := s.model.Complete(ctx, ModelRequest{
			System:    system,
			Tools:     tools,
			Messages:  transcript,
			MaxTokens: s.limits.MaxTokensPerCall,
		}, func(delta string) {
			emit.Emit(EventAssistantDelta, map[string]any{"text": delta})
		})
		if err != nil {
			switch {
			case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
				failure = &turnFailure{FailTimeout, "The assistant took too long and stopped.", true}
			case errors.Is(err, ErrModelUnavailable):
				failure = &turnFailure{FailModelUnavailable, "The assistant is temporarily unavailable.", true}
			default:
				log.Printf("assistant: model call failed: %v", err)
				failure = &turnFailure{FailInternal, "The assistant hit an internal problem.", true}
			}
			break rounds
		}
		usage.Add(resp.Usage)

		assistantMsg := models.AssistantMessage{Role: "assistant", Blocks: resp.Blocks}
		if _, err := database.AppendAssistantMessage(in.Conversation.ID, "assistant", resp.Blocks, ""); err != nil {
			log.Printf("assistant: persisting assistant message: %v", err)
		}
		transcript = append(transcript, assistantMsg)
		if text := textOf(resp.Blocks); text != "" {
			emit.Emit(EventAssistantMessage, map[string]any{"text": text})
		}

		switch resp.StopReason {
		case "tool_use":
			// handled below
		case "refusal":
			failure = &turnFailure{FailRefused, "The assistant declined to continue with that request.", false}
			break rounds
		case "max_tokens":
			stopReason = "max_tokens"
			break rounds
		default:
			stopReason = resp.StopReason
			break rounds
		}

		// Run the tools, all results in one user message.
		results := []models.AssistantBlock{}
		for _, block := range resp.Blocks {
			if block.Type != "tool_use" {
				continue
			}
			toolCalls++
			if toolCalls > s.limits.MaxToolCalls {
				failure = &turnFailure{FailToolLimit, "The assistant made too many tool calls in one turn and stopped.", false}
				break rounds
			}
			emit.Emit(EventToolCall, map[string]any{
				"call_id": block.ID, "tool": block.Name, "arguments": json.RawMessage(nonEmpty(block.Input)),
			})

			var r toolResult
			if awaitingConfirmation {
				r = toolResult{
					Outcome: Outcome{IsError: true, Status: models.ToolCallInvalid},
					Content: "A confirmation is pending with the operator. Do not call tools until they answer.",
				}
			} else {
				r = s.Execute(t, block.Name, block.Input, nil)
			}
			results = append(results, models.AssistantBlock{
				Type: "tool_result", ToolUseID: block.ID, Content: r.Content, IsError: r.IsError,
			})
			emit.Emit(EventToolResult, map[string]any{
				"call_id": block.ID, "tool": block.Name, "status": r.Status,
				"http_status": r.HTTPStatus, "summary": summarise(block.Name, r),
			})
			if r.Handoff != nil {
				emit.Emit(EventHandoff, map[string]any{
					"call_id": block.ID, "kind": r.Handoff.Kind, "route": r.Handoff.Route, "label": r.Handoff.Label,
				})
			}
			if r.Confirmation != nil {
				awaitingConfirmation = true
				emit.Emit(EventConfirmationRequired, map[string]any{
					"call_id":         block.ID,
					"confirmation_id": r.Confirmation.TokenID,
					"token":           r.Confirmation.Token,
					"tool":            r.Confirmation.ToolName,
					"arguments":       r.Confirmation.Arguments,
					"consequence":     r.Confirmation.Consequence,
					"phrase_required": r.Confirmation.PhraseRequired,
					"expires_at":      r.Confirmation.ExpiresAt.UTC().Format(time.RFC3339),
				})
			}
			if r.HTTPStatus == http.StatusUnauthorized {
				failure = &turnFailure{FailSessionExpired, "Your session has ended. Sign in again to continue.", false}
			}
		}
		if _, err := database.AppendAssistantMessage(in.Conversation.ID, "user", results, ""); err != nil {
			log.Printf("assistant: persisting tool results: %v", err)
		}
		transcript = append(transcript, models.AssistantMessage{Role: "user", Blocks: results})
		if failure != nil {
			break rounds
		}
		if round == s.limits.MaxToolRounds-1 {
			failure = &turnFailure{FailToolLimit, "The assistant made too many tool calls in one turn and stopped.", false}
		}
	}

	if err := database.RecordAssistantTurn(in.Conversation.ID, in.Operator.CompanyID, usage, toolCalls); err != nil {
		log.Printf("assistant: recording turn usage: %v", err)
	}

	if failure != nil {
		fail(*failure)
		return
	}
	if awaitingConfirmation {
		stopReason = "confirmation"
	}
	emit.Emit(EventTurnCompleted, map[string]any{
		"turn_id":     turnID,
		"stop_reason": stopReason,
		"usage":       usage,
	})
}

// Settle answers a pending confirmation: approve runs the stored tool as
// the operator and lets the model narrate; reject tells the model so.
func (s *Service) Settle(parent context.Context, in TurnInput, token string, approve bool, phrase string, emit Emitter) (int, string) {
	t := &Turn{
		ID:             newTurnID(),
		ConversationID: in.Conversation.ID,
		CompanyID:      in.Operator.CompanyID,
		UserID:         in.Operator.UserID,
		SessionID:      in.Operator.SessionID,
		Role:           in.Operator.Role,
		ClientIP:       in.ClientIP,
		UserAgent:      in.UserAgent,
		cookie:         in.Cookie,
		csrf:           in.CSRF,
		router:         s.router,
		ctx:            parent,
	}

	row, err := s.verifyConfirmation(t, token, phrase)
	if err != nil {
		if errors.Is(err, database.ErrConfirmationExpired) {
			s.expireConfirmation(t, token)
		}
		return describeConfirmationError(err)
	}

	outcome := models.ConfirmationRejected
	if approve {
		outcome = models.ConfirmationApproved
	}
	if err := database.SettleAssistantConfirmation(row.ID, outcome); err != nil {
		return describeConfirmationError(err)
	}

	var note string
	if approve {
		r := s.Execute(t, row.ToolName, row.Arguments, row)
		emit.Emit(EventToolResult, map[string]any{
			"call_id": row.TokenID, "tool": row.ToolName, "status": r.Status,
			"http_status": r.HTTPStatus, "summary": summarise(row.ToolName, r),
		})
		if r.Handoff != nil {
			emit.Emit(EventHandoff, map[string]any{
				"call_id": row.TokenID, "kind": r.Handoff.Kind, "route": r.Handoff.Route, "label": r.Handoff.Label,
			})
		}
		if r.IsError {
			note = fmt.Sprintf("The operator approved %s, and it was attempted but did not succeed: %s", row.ToolName, r.Content)
		} else {
			note = fmt.Sprintf("The operator approved %s and it ran. Result: %s", row.ToolName, r.Content)
		}
	} else {
		s.recordSettlement(t, row, models.ToolCallConfirmationRejected)
		note = fmt.Sprintf("The operator rejected %s. It did not run. Do not retry it unless they ask again.", row.ToolName)
	}

	if _, err := database.AppendAssistantMessage(in.Conversation.ID, "user",
		[]models.AssistantBlock{{Type: "text", Text: note}}, ""); err != nil {
		return 500, "The confirmation was recorded but the conversation could not be updated."
	}
	in.UserText = ""
	in.ClientMessageID = ""
	s.RunTurn(parent, in, emit)
	return 0, ""
}

func (s *Service) recordSettlement(t *Turn, row *models.AssistantConfirmation, status string) {
	if _, err := database.RecordAssistantToolCall(models.AssistantToolCallRecord{
		ConversationID: t.ConversationID, TurnID: t.ID, CompanyID: t.CompanyID,
		UserID: t.UserID, SessionID: t.SessionID, ToolName: row.ToolName,
		Arguments: row.Arguments, Status: status, ConfirmationID: row.ID,
		ClientIP: t.ClientIP, UserAgent: t.UserAgent,
	}); err != nil {
		log.Printf("assistant: recording settlement: %v", err)
	}
}

// replay re-emits a persisted turn to a client that lost its stream.
func (s *Service) replay(conversationID int64, fromSeq int, turnID string, emit Emitter) {
	transcript, err := database.ListAssistantMessages(conversationID)
	if err != nil {
		emit.Emit(EventTurnFailed, map[string]any{"turn_id": turnID, "code": FailInternal,
			"message": "The conversation could not be read.", "retryable": true})
		return
	}
	for _, m := range transcript {
		if m.Seq <= fromSeq {
			continue
		}
		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
				if m.Role == "assistant" {
					emit.Emit(EventAssistantMessage, map[string]any{"text": b.Text})
				}
			case "tool_use":
				emit.Emit(EventToolCall, map[string]any{"call_id": b.ID, "tool": b.Name,
					"arguments": json.RawMessage(nonEmpty(b.Input))})
			case "tool_result":
				status := models.ToolCallExecuted
				if b.IsError {
					status = models.ToolCallFailed
				}
				emit.Emit(EventToolResult, map[string]any{"call_id": b.ToolUseID, "status": status, "summary": "replayed"})
			}
		}
	}
	emit.Emit(EventTurnCompleted, map[string]any{"turn_id": turnID, "stop_reason": "replayed", "replayed": true})
}

func (s *Service) overBudget(companyID int64) (bool, error) {
	if s.limits.CompanyMonthlyTokens <= 0 {
		return false, nil
	}
	u, err := database.AssistantUsageThisMonth(companyID)
	if err != nil {
		return false, err
	}
	return u.Total() >= s.limits.CompanyMonthlyTokens, nil
}

func textOf(blocks []models.AssistantBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	return s
}

func nonEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// summarise is the one-line description the console shows beside a tool
// call. It never includes the result body.
func summarise(tool string, r toolResult) string {
	switch {
	case r.Confirmation != nil:
		return "waiting for your approval"
	case r.IsError:
		return r.Content
	case r.Status == models.ToolCallConfirmedExecuted:
		return "done"
	default:
		return "ok"
	}
}

func newTurnID() string {
	return newRequestID() + newRequestID()
}
