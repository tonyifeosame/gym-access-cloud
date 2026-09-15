package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"access-terminal-cloud-api/models"
)

// The model behind the assistant, behind an interface.
//
// THE LOOP DOES NOT KNOW THE SDK. It works in the assistant's own block
// representation (models.AssistantBlock), which is also what the transcript
// stores; this file converts to and from the SDK's types at the edge. That
// keeps the loop, the dispatcher and every test independent of the SDK, and
// lets the tests drive the loop with a scripted model.

// ModelRequest is one model call.
type ModelRequest struct {
	System    string
	Tools     []Definition
	Messages  []models.AssistantMessage
	MaxTokens int
}

// ModelResponse is what came back.
type ModelResponse struct {
	Blocks     []models.AssistantBlock
	StopReason string // end_turn | tool_use | max_tokens | refusal | ...
	Usage      models.AssistantUsage
}

// Model is what the loop needs from a model.
type Model interface {
	// Complete runs one call. onText receives text deltas as they stream.
	Complete(ctx context.Context, req ModelRequest, onText func(string)) (*ModelResponse, error)
	// Name is the model id, for the conversation record.
	Name() string
}

// ErrModelUnavailable wraps a transport or rate-limit failure the operator
// can retry.
var ErrModelUnavailable = errors.New("assistant model unavailable")

// --- the SDK-backed model -------------------------------------------------------

// AnthropicModel calls the Claude API.
type AnthropicModel struct {
	client anthropic.Client
	model  string
}

// NewAnthropicModel builds a model client from the environment: it returns
// nil when no ANTHROPIC_API_KEY is set, which is how the assistant stays
// absent from a deployment that has not been given a key.
func NewAnthropicModel(modelID string) *AnthropicModel {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil
	}
	if modelID == "" {
		modelID = DefaultModel
	}
	return &AnthropicModel{
		client: anthropic.NewClient(
			option.WithAPIKey(key),
			option.WithRequestTimeout(modelCallTimeout),
			option.WithMaxRetries(2),
		),
		model: modelID,
	}
}

// DefaultModel is the Phase 1 model.
const DefaultModel = "claude-sonnet-5"

const modelCallTimeout = 120 * time.Second

func (m *AnthropicModel) Name() string { return m.model }

// Complete streams one call and accumulates the final message.
func (m *AnthropicModel) Complete(ctx context.Context, req ModelRequest, onText func(string)) (*ModelResponse, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(m.model),
		MaxTokens: int64(req.MaxTokens),
		Messages:  toSDKMessages(req.Messages),
		Tools:     toSDKTools(req.Tools),
		// The system prompt is the stable prefix; caching it (with the tools
		// before it) is what keeps a long conversation cheap.
		System: []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
	}

	stream := m.client.Messages.NewStreaming(ctx, params)
	message := anthropic.Message{}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return nil, err
		}
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok && onText != nil {
				onText(text.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 &&
			apiErr.StatusCode != 429 && apiErr.StatusCode != 408 {
			// A request the API refused outright is a bug in this package,
			// not something to retry; say so in the log with the status.
			return nil, fmt.Errorf("assistant: model request refused (%d): %w", apiErr.StatusCode, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrModelUnavailable, err)
	}

	return &ModelResponse{
		Blocks:     fromSDKContent(message.Content),
		StopReason: string(message.StopReason),
		Usage: models.AssistantUsage{
			InputTokens:      message.Usage.InputTokens,
			OutputTokens:     message.Usage.OutputTokens,
			CacheReadTokens:  message.Usage.CacheReadInputTokens,
			CacheWriteTokens: message.Usage.CacheCreationInputTokens,
		},
	}, nil
}

func toSDKTools(defs []Definition) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(defs))
	for _, d := range defs {
		properties, _ := d.InputSchema["properties"].(map[string]any)
		required, _ := d.InputSchema["required"].([]string)
		tool := anthropic.ToolParam{
			Name:        d.Name,
			Description: anthropic.String(d.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties:  properties,
				Required:    required,
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out
}

func toSDKMessages(messages []models.AssistantMessage) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, m := range messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					blocks = append(blocks, anthropic.NewTextBlock(b.Text))
				}
			case "tool_use":
				var input any = map[string]any{}
				if len(b.Input) > 0 {
					_ = json.Unmarshal(b.Input, &input)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ID, input, b.Name))
			case "tool_result":
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
			case "thinking":
				if b.Signature != "" {
					blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Thinking))
				}
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == "assistant" {
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		} else {
			out = append(out, anthropic.NewUserMessage(blocks...))
		}
	}
	return out
}

func fromSDKContent(content []anthropic.ContentBlockUnion) []models.AssistantBlock {
	out := make([]models.AssistantBlock, 0, len(content))
	for _, block := range content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			out = append(out, models.AssistantBlock{Type: "text", Text: v.Text})
		case anthropic.ToolUseBlock:
			out = append(out, models.AssistantBlock{
				Type:  "tool_use",
				ID:    v.ID,
				Name:  v.Name,
				Input: json.RawMessage(v.JSON.Input.Raw()),
			})
		case anthropic.ThinkingBlock:
			out = append(out, models.AssistantBlock{Type: "thinking", Thinking: v.Thinking, Signature: v.Signature})
		}
	}
	return out
}
