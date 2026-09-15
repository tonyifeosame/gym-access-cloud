package assistant

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Server-sent events for one turn.
//
// One response stream per user message. Each event is `event: <type>`,
// `id: <seq>`, `data: <json>`; a comment line every fifteen seconds keeps a
// proxy from closing a stream that is waiting on the model. There is no
// resumption: a client that loses the stream re-sends its message with the
// same client_message_id and gets the persisted outcome back (loop.go).

// Event types.
const (
	EventTurnStarted          = "turn.started"
	EventAssistantDelta       = "assistant.delta"
	EventAssistantMessage     = "assistant.message"
	EventToolCall             = "tool.call"
	EventToolResult           = "tool.result"
	EventHandoff              = "handoff"
	EventConfirmationRequired = "confirmation.required"
	EventTurnCompleted        = "turn.completed"
	EventTurnFailed           = "turn.failed"
)

// Emitter writes events. It is safe for use from the goroutine running the
// turn and the keepalive ticker.
type Emitter interface {
	Emit(event string, data any)
}

type sseEmitter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher http.Flusher
	seq     int
	closed  bool
}

// NewSSEEmitter starts an event stream on w (headers are sent immediately).
func NewSSEEmitter(w http.ResponseWriter) *sseEmitter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	e := &sseEmitter{w: w, flusher: flusher}
	e.flush()
	return e
}

func (e *sseEmitter) Emit(event string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	body, err := json.Marshal(data)
	if err != nil {
		body = []byte(`{}`)
	}
	e.seq++
	fmt.Fprintf(e.w, "event: %s\nid: %d\ndata: %s\n\n", event, e.seq, body)
	e.flush()
}

func (e *sseEmitter) keepalive() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	io.WriteString(e.w, ": keepalive\n\n")
	e.flush()
}

// Close stops further events.
func (e *sseEmitter) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
}

func (e *sseEmitter) flush() {
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

// RunKeepalive pings until stop is closed.
func (e *sseEmitter) RunKeepalive(stop <-chan struct{}, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			e.keepalive()
		}
	}
}

// collectingEmitter records events; the tests read it.
type collectingEmitter struct {
	mu     sync.Mutex
	Events []struct {
		Event string
		Data  any
	}
}

func (c *collectingEmitter) Emit(event string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Events = append(c.Events, struct {
		Event string
		Data  any
	}{event, data})
}
