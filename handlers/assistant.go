package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"access-terminal-cloud-api/assistant"
	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// The in-console assistant's HTTP surface.
//
// Every route here sits under the console's OperatorAuthMiddleware, so the
// caller is a signed-in operator; the writes also require the CSRF header.
// The assistant then acts as that operator (assistant.Turn) and nothing else.
//
// THE ASSISTANT IS ABSENT UNTIL ENABLED. With ASSISTANT_ENABLED unset, or no
// model key, or no confirmation secret, every route but /capabilities answers
// 503 and /capabilities says enabled=false, which is what the console reads
// to hide the launcher.

var assistantService *assistant.Service

// SetAssistant installs the service the routes use.
func SetAssistant(s *assistant.Service) { assistantService = s }

// Assistant returns the installed service (the tests read it).
func Assistant() *assistant.Service { return assistantService }

func assistantOrRefuse(c *gin.Context) *assistant.Service {
	if assistantService == nil || !assistantService.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "The assistant is not enabled.", "code": "assistant_disabled"})
		return nil
	}
	return assistantService
}

// AssistantCapabilities says whether the assistant is on and what it can do
// for this operator.
func AssistantCapabilities(c *gin.Context) {
	if assistantService == nil {
		c.JSON(http.StatusOK, models.AssistantCapabilities{Enabled: false})
		return
	}
	c.JSON(http.StatusOK, assistantService.Capabilities(c.GetString("role")))
}

// AssistantListConversations lists the operator's own conversations.
func AssistantListConversations(c *gin.Context) {
	if assistantOrRefuse(c) == nil {
		return
	}
	list, err := database.ListAssistantConversations(c.GetInt64("company_id"), c.GetInt64("user_id"), 20)
	if err != nil {
		logError(c, "list assistant conversations", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve conversations"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": len(list), "conversations": list})
}

// AssistantCreateConversation opens a conversation.
func AssistantCreateConversation(c *gin.Context) {
	s := assistantOrRefuse(c)
	if s == nil {
		return
	}
	conv, err := database.CreateAssistantConversation(c.GetInt64("company_id"), c.GetInt64("user_id"),
		s.ModelName(), assistant.PromptHash())
	if err != nil {
		logError(c, "create assistant conversation", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start a conversation"})
		return
	}
	c.JSON(http.StatusCreated, conv)
}

// AssistantGetConversation reads one conversation and its transcript.
func AssistantGetConversation(c *gin.Context) {
	if assistantOrRefuse(c) == nil {
		return
	}
	conv := loadConversation(c)
	if conv == nil {
		return
	}
	messages, err := database.ListAssistantMessages(conv.ID)
	if err != nil {
		logError(c, "read assistant conversation", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the conversation"})
		return
	}
	// The console shows text and tool calls; the model's thinking is not
	// part of the record a person reads.
	for i := range messages {
		kept := messages[i].Blocks[:0]
		for _, b := range messages[i].Blocks {
			if b.Type != "thinking" {
				kept = append(kept, b)
			}
		}
		messages[i].Blocks = kept
	}
	c.JSON(http.StatusOK, gin.H{"conversation": conv, "messages": messages})
}

func loadConversation(c *gin.Context) *models.AssistantConversation {
	conv, err := database.GetAssistantConversation(c.GetInt64("company_id"), c.GetInt64("user_id"), c.Param("id"))
	if errors.Is(err, database.ErrConversationNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Conversation not found"})
		return nil
	}
	if err != nil {
		logError(c, "load assistant conversation", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read the conversation"})
		return nil
	}
	return conv
}

func turnInput(c *gin.Context, conv *models.AssistantConversation) assistant.TurnInput {
	identity := middleware.Operator(c)
	name := identity.FullName
	if name == "" {
		name = identity.Email
	}
	return assistant.TurnInput{
		Conversation: conv,
		Operator: assistant.Operator{
			UserID:      identity.UserID,
			SessionID:   identity.SessionID,
			Role:        identity.Role,
			FullName:    name,
			CompanyID:   identity.CompanyID,
			CompanyName: identity.CompanyName,
		},
		Cookie:    c.GetHeader("Cookie"),
		CSRF:      c.GetHeader(middleware.CSRFHeader),
		ClientIP:  c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}
}

// AssistantPostMessage runs one turn and streams it as server-sent events.
func AssistantPostMessage(c *gin.Context) {
	s := assistantOrRefuse(c)
	if s == nil {
		return
	}
	conv := loadConversation(c)
	if conv == nil {
		return
	}
	if conv.Status != models.ConversationOpen {
		c.JSON(http.StatusConflict, gin.H{"error": assistant.ConversationFullMessage, "code": "conversation_closed"})
		return
	}
	var req models.AssistantMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A message is required"})
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || len(req.Text) > 4000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A message of up to 4000 characters is required"})
		return
	}
	if len(req.ClientMessageID) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client_message_id is too long"})
		return
	}

	release, ok := s.BeginTurn(c.GetInt64("user_id"))
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "The assistant is still answering your last message.", "code": "turn_in_progress"})
		return
	}
	defer release()

	in := turnInput(c, conv)
	in.UserText = req.Text
	in.ClientMessageID = req.ClientMessageID

	streamTurn(c, func(emit assistant.Emitter) {
		s.RunTurn(c.Request.Context(), in, emit)
	})
}

// AssistantSettleConfirmation approves or rejects a pending confirmation and
// streams the assistant's follow-up.
func AssistantSettleConfirmation(c *gin.Context) {
	s := assistantOrRefuse(c)
	if s == nil {
		return
	}
	conv := loadConversation(c)
	if conv == nil {
		return
	}
	if conv.Status != models.ConversationOpen {
		c.JSON(http.StatusConflict, gin.H{"error": assistant.ConversationFullMessage, "code": "conversation_closed"})
		return
	}
	var req models.AssistantConfirmationRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Token) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A confirmation token is required"})
		return
	}

	release, ok := s.BeginTurn(c.GetInt64("user_id"))
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "The assistant is still answering your last message.", "code": "turn_in_progress"})
		return
	}
	defer release()

	in := turnInput(c, conv)

	// Verification happens before the stream opens, so a bad token gets a
	// plain JSON status the console can act on. Settle checks again under
	// the stream; a failure there (a race with a second approval) becomes a
	// turn.failed event.
	streamTurn(c, func(emit assistant.Emitter) {
		if status, message := s.Settle(c.Request.Context(), in, strings.TrimSpace(req.Token), req.Approve, req.Phrase, emit); status != 0 {
			emit.Emit(assistant.EventTurnFailed, gin.H{"code": assistant.FailInternal, "message": message, "retryable": false})
		}
	}, func() (int, string) {
		return s.PreflightConfirmation(in, strings.TrimSpace(req.Token), req.Phrase)
	})
}

// streamTurn opens the SSE response and runs the turn on it. `preflight`,
// when given, runs before headers are sent and may refuse with a status.
func streamTurn(c *gin.Context, run func(assistant.Emitter), preflight ...func() (int, string)) {
	for _, check := range preflight {
		if status, message := check(); status != 0 {
			c.JSON(status, gin.H{"error": message})
			return
		}
	}
	emitter := assistant.NewSSEEmitter(c.Writer)
	stop := make(chan struct{})
	go emitter.RunKeepalive(stop, 15*time.Second)
	defer func() {
		close(stop)
		emitter.Close()
	}()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("request_id=%s assistant turn panicked: %v", middleware.RequestID(c), r)
			emitter.Emit(assistant.EventTurnFailed, gin.H{"code": assistant.FailInternal,
				"message": "The assistant hit an internal problem.", "retryable": true})
		}
	}()
	run(emitter)
}
