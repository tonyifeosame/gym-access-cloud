package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/middleware"
	"access-terminal-cloud-api/models"
)

// Turn is one user message being handled: who is asking, and the means to
// act as them.
//
// THE COOKIE AND CSRF TOKEN ARE THE OPERATOR'S OWN, copied from the request
// that started the turn and held in memory for its duration only. They are
// never persisted, never logged and never sent anywhere but back into this
// process's own router. That is the entire authorisation model: the assistant
// is the operator, for the length of one turn, and no longer.
type Turn struct {
	ID             string
	ConversationID int64
	CompanyID      int64
	UserID         int64
	SessionID      int64
	Role           string
	ClientIP       string
	UserAgent      string

	cookie string
	csrf   string
	router http.Handler
	ctx    context.Context

	// calls counts internal requests made in this turn, for the cap.
	calls int
}

// Response is what a tool sees of an internal request.
type Response struct {
	Status    int
	Body      []byte
	RequestID string
	Route     string
}

// JSON decodes the body into v. A body that is not JSON is an error.
func (r Response) JSON(v any) error {
	if len(r.Body) == 0 {
		return fmt.Errorf("empty response")
	}
	return json.Unmarshal(r.Body, v)
}

// ErrorMessage reads the API's own `error` field, which is written for
// customers, so it is what the model should relay.
func (r Response) ErrorMessage() string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &body); err == nil && body.Error != "" {
		return body.Error
	}
	return http.StatusText(r.Status)
}

// Call performs one request through the console's router as the operator.
//
// ONLY CONSOLE ROUTES. The path must start with /api/v1/console/: the site-key
// tree, the device tree, the public API and the platform tree answer to other
// principals and are never reachable from here, whatever a tool asks for.
func (t *Turn) Call(method, path string, body any) (Response, error) {
	if !strings.HasPrefix(path, "/api/v1/console/") {
		return Response{}, fmt.Errorf("assistant: %s is outside the console API", path)
	}
	if t.calls >= maxInternalCallsPerTurn {
		return Response{}, fmt.Errorf("assistant: too many requests in one turn")
	}
	t.calls++

	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req = req.WithContext(t.ctx)
	req.Header.Set("Cookie", t.cookie)
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set(middleware.CSRFHeader, t.csrf)
		req.Header.Set("Content-Type", "application/json")
	}
	requestID := newRequestID()
	req.Header.Set(middleware.RequestIDHeader, requestID)
	req.Header.Set("User-Agent", fmt.Sprintf("AccessLink-Assistant/1 conversation=%d turn=%s", t.ConversationID, t.ID))
	// The address the router sees is the process's own. The operator's real
	// address is recorded on the assistant tables instead; it must not be
	// forged onto the internal request, because X-Forwarded-For is precisely
	// the header the deployment is configured not to trust.
	req.RemoteAddr = "127.0.0.1:0"

	rec := httptest.NewRecorder()
	t.router.ServeHTTP(rec, req)

	route := method + " " + routeWithoutQuery(path)
	return Response{Status: rec.Code, Body: rec.Body.Bytes(), RequestID: requestID, Route: route}, nil
}

// Query builds a console path with query parameters, escaping every value.
func Query(path string, params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		if v != "" {
			values.Set(k, v)
		}
	}
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}

// Segment escapes one path segment supplied by the model.
func Segment(s string) string {
	return url.PathEscape(s)
}

func routeWithoutQuery(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}

// maxInternalCallsPerTurn bounds the requests one turn may make, however the
// model combines tools.
const maxInternalCallsPerTurn = 48

// --- running one tool -----------------------------------------------------------

// toolResult is what Execute hands the loop.
type toolResult struct {
	Outcome
	// Content is the text placed in the model's tool_result block.
	Content string
	// Confirmation is set when the tool paused for approval.
	Confirmation *pendingConfirmation
	DurationMS   int
}

type pendingConfirmation struct {
	ID             int64
	TokenID        string
	Token          string
	ExpiresAt      time.Time
	ToolName       string
	Arguments      json.RawMessage
	Consequence    models.AssistantConsequence
	PhraseRequired string
}

// Execute validates arguments, asks for confirmation where the tool requires
// it, runs the tool, and records the call. `approved` is the confirmation
// being honoured, or nil for a fresh call.
func (s *Service) Execute(t *Turn, name string, raw json.RawMessage, approved *models.AssistantConfirmation) toolResult {
	started := time.Now()
	record := models.AssistantToolCallRecord{
		ConversationID: t.ConversationID,
		TurnID:         t.ID,
		CompanyID:      t.CompanyID,
		UserID:         t.UserID,
		SessionID:      t.SessionID,
		ToolName:       name,
		Arguments:      raw,
		ClientIP:       t.ClientIP,
		UserAgent:      t.UserAgent,
	}
	finish := func(r toolResult) toolResult {
		r.DurationMS = int(time.Since(started).Milliseconds())
		record.Status = r.Status
		record.HTTPStatus = r.HTTPStatus
		record.Route = r.Route
		record.RequestID = r.RequestID
		record.DurationMS = r.DurationMS
		if approved != nil {
			record.ConfirmationID = approved.ID
		}
		if r.Confirmation != nil {
			record.ConfirmationID = r.Confirmation.ID
		}
		if !r.IsError && r.Content != "" {
			sum := sha256.Sum256([]byte(r.Content))
			record.ResultDigest = hex.EncodeToString(sum[:])
		}
		if _, err := database.RecordAssistantToolCall(record); err != nil {
			log.Printf("assistant: tool call record lost (tool=%s status=%s): %v", name, r.Status, err)
		}
		return r
	}
	fail := func(status string, message string) toolResult {
		return finish(toolResult{
			Outcome: Outcome{IsError: true, Status: status},
			Content: message,
		})
	}

	tool, ok := s.registry.Get(name)
	if !ok || !middleware.RoleAtLeast(t.Role, tool.MinRole) {
		// Unknown to this role: the model was never shown it, so the call is
		// a hallucination or a probe. Either way it is refused without
		// touching the router.
		return fail(models.ToolCallInvalid, "That tool is not available.")
	}

	args, err := tool.ValidateArgs(raw)
	if err != nil {
		return fail(models.ToolCallInvalid, "Invalid arguments: "+err.Error())
	}
	// Re-encode the validated arguments so the record and the confirmation
	// hash cover exactly what will run, not the model's raw text.
	canonical, _ := canonicalJSON(args)
	record.Arguments = canonical

	// A consequential tool asks first. The plan may read through the router
	// to describe the action; those reads count toward the turn's cap.
	if tool.Confirm != nil && approved == nil {
		plan, err := tool.Confirm(t, args)
		if err != nil {
			return fail(models.ToolCallFailed, "Could not prepare that action: "+err.Error())
		}
		if plan != nil {
			pending, err := s.issueConfirmation(t, tool.Name, canonical, plan)
			if err != nil {
				log.Printf("assistant: issuing confirmation for %s: %v", tool.Name, err)
				return fail(models.ToolCallFailed, "Could not ask for confirmation.")
			}
			content, _ := json.Marshal(map[string]any{
				"status":      "confirmation_required",
				"consequence": plan.Consequence,
				"note": "The operator has been shown this and asked to approve or reject it. " +
					"Do not call the tool again. Tell the operator briefly what you are waiting for and stop.",
			})
			return finish(toolResult{
				Outcome:      Outcome{Status: models.ToolCallConfirmationRequested, Result: nil},
				Content:      string(content),
				Confirmation: pending,
			})
		}
	}
	if approved != nil {
		// The confirmation's arguments are what run -- never the call's.
		if approved.ToolName != tool.Name || approved.ArgumentsHash != hashArguments(canonical) {
			return fail(models.ToolCallInvalid, "The confirmation does not match this action.")
		}
	}

	timeout := tool.MaxDuration
	if timeout == 0 {
		timeout = defaultToolTimeout
	}
	ctx, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	inner := *t
	inner.ctx = ctx
	outcome := tool.Run(&inner, args)
	t.calls = inner.calls

	if outcome.Status == "" {
		outcome.Status = models.ToolCallExecuted
	}
	if approved != nil && outcome.Status == models.ToolCallExecuted {
		outcome.Status = models.ToolCallConfirmedExecuted
	}

	var content string
	if outcome.IsError {
		if s, ok := outcome.Result.(string); ok {
			content = s
		} else if outcome.Result != nil {
			b, _ := json.Marshal(outcome.Result)
			content = string(b)
		} else {
			content = "The action failed."
		}
	} else {
		body, err := resultBytes(tool.Name, outcome.Result)
		if err != nil {
			log.Printf("assistant: %v", err)
			return fail(models.ToolCallFailed, "The result could not be shown.")
		}
		if len(body) > maxResultBytes {
			return fail(models.ToolCallFailed, "The result was too large to show; ask for fewer rows.")
		}
		content = string(body)
	}

	return finish(toolResult{Outcome: outcome, Content: content})
}

// canonicalJSON encodes validated arguments with sorted keys, so the same
// arguments always hash the same.
func canonicalJSON(args Args) (json.RawMessage, error) {
	// encoding/json sorts map keys.
	return json.Marshal(map[string]any(args))
}

func hashArguments(canonical json.RawMessage) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// mapStatus turns an internal HTTP status into the stored outcome and the
// message the model is given. The messages name no other company, no
// internal id and no route.
func mapStatus(resp Response) (status string, message string, isError bool) {
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return models.ToolCallExecuted, "", false
	case resp.Status == http.StatusUnauthorized:
		return models.ToolCallFailed, "Your session has ended. Sign in again to continue.", true
	case resp.Status == http.StatusForbidden:
		msg := resp.ErrorMessage()
		if strings.Contains(strings.ToLower(msg), "site") {
			return models.ToolCallRefusedScope, "Not permitted: " + msg, true
		}
		return models.ToolCallRefusedRole, "Not permitted for your role: " + msg, true
	case resp.Status == http.StatusNotFound:
		return models.ToolCallNotFound, "Not found in your company: " + resp.ErrorMessage(), true
	case resp.Status == http.StatusConflict, resp.Status == http.StatusBadRequest,
		resp.Status == http.StatusUnprocessableEntity:
		return models.ToolCallInvalid, resp.ErrorMessage(), true
	case resp.Status == http.StatusTooManyRequests:
		return models.ToolCallFailed, "Too many requests; try again in a moment.", true
	default:
		return models.ToolCallFailed, "That is temporarily unavailable.", true
	}
}

// failed is the Outcome for an internal request that did not succeed.
func failed(resp Response) Outcome {
	status, message, _ := mapStatus(resp)
	return Outcome{
		IsError:    true,
		Status:     status,
		HTTPStatus: resp.Status,
		Route:      resp.Route,
		RequestID:  resp.RequestID,
		Result:     message,
	}
}

// succeeded is the Outcome for a successful internal request with a
// projected result.
func succeeded(resp Response, result any) Outcome {
	return Outcome{
		Result:     result,
		Status:     models.ToolCallExecuted,
		HTTPStatus: resp.Status,
		Route:      resp.Route,
		RequestID:  resp.RequestID,
	}
}

const defaultToolTimeout = 10 * time.Second
