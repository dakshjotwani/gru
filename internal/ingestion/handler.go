package ingestion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/attention"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Publisher broadcasts ingested events to active SubscribeEvents streams.
type Publisher struct {
	mu   sync.Mutex
	subs map[string]chan *gruv1.SessionEvent
}

func NewPublisher() *Publisher {
	return &Publisher{subs: make(map[string]chan *gruv1.SessionEvent)}
}

func (p *Publisher) Subscribe(id string, ch chan *gruv1.SessionEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[id] = ch
}

func (p *Publisher) Unsubscribe(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, id)
}

func (p *Publisher) Publish(evt *gruv1.SessionEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range p.subs {
		select {
		case ch <- evt:
		default:
			// Slow subscriber: drop rather than block ingestion.
		}
	}
}

// Handler handles POST /events from hook scripts.
type Handler struct {
	store     *store.Store
	reg       *adapter.Registry
	pub       *Publisher
	attention *attention.Engine
}

// NewHandler wires the HTTP handler. The attention engine is optional — if
// nil, attention_score is passed through unchanged (v1 behavior).
func NewHandler(s *store.Store, reg *adapter.Registry, pub *Publisher) http.Handler {
	return &Handler{store: s, reg: reg, pub: pub}
}

// NewHandlerWithAttention wires the handler with an attention engine that
// scores each event into session.attention_score on the way to SQLite.
func NewHandlerWithAttention(s *store.Store, reg *adapter.Registry, pub *Publisher, a *attention.Engine) http.Handler {
	return &Handler{store: s, reg: reg, pub: pub, attention: a}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Require X-Gru-Session-ID header (400 if missing).
	sessionID := r.Header.Get("X-Gru-Session-ID")
	if sessionID == "" {
		http.Error(w, "missing X-Gru-Session-ID header", http.StatusBadRequest)
		return
	}

	// 2. Require X-Gru-Runtime header (400 if missing).
	runtime := r.Header.Get("X-Gru-Runtime")
	if runtime == "" {
		http.Error(w, "missing X-Gru-Runtime header", http.StatusBadRequest)
		return
	}

	normalizer := h.reg.Get(runtime)
	if normalizer == nil {
		http.Error(w, fmt.Sprintf("unknown runtime: %s", runtime), http.StatusBadRequest)
		return
	}

	// 3. Look up session by X-Gru-Session-ID header — return 404 if not found. No auto-creation.
	q := h.store.Queries()
	sess, err := q.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("look up session: %v", err), http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusInternalServerError)
		return
	}

	evt, err := normalizer.Normalize(r.Context(), json.RawMessage(body))
	if err != nil {
		http.Error(w, fmt.Sprintf("normalize: %v", err), http.StatusUnprocessableEntity)
		return
	}

	// 4. Normalize event, store it, publish it.
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	evt.SessionID = sess.ID
	evt.ProjectID = sess.ProjectID

	if _, err := q.CreateEvent(r.Context(), store.CreateEventParams{
		ID:        evt.ID,
		SessionID: evt.SessionID,
		ProjectID: evt.ProjectID,
		Runtime:   evt.Runtime,
		Type:      string(evt.Type),
		Timestamp: evt.Timestamp.UTC().Format(time.RFC3339),
		Payload:   string(evt.Payload),
	}); err != nil {
		http.Error(w, fmt.Sprintf("store event: %v", err), http.StatusInternalServerError)
		return
	}

	// Derive the new session status from the event type.
	newStatus := sess.Status
	switch evt.Type {
	case adapter.EventSessionStart:
		// Claude Code started — session is now active.
		newStatus = "running"
	case adapter.EventSessionIdle:
		// Turn complete; Claude is at the prompt waiting for next input.
		newStatus = "idle"
	case adapter.EventSessionEnd:
		// Process exited normally.
		newStatus = "completed"
	case adapter.EventSessionCrash:
		// Fatal API error (StopFailure) or process crash.
		newStatus = "errored"
	case adapter.EventNeedsAttention:
		// Claude blocked on a permission prompt or MCP elicitation.
		newStatus = "needs_attention"
	case adapter.EventToolPre, adapter.EventSubagentStart:
		// Claude started doing work — transition from any pre-active state.
		if sess.Status == "starting" || sess.Status == "idle" || sess.Status == "needs_attention" {
			newStatus = "running"
		}
	case adapter.EventToolPost, adapter.EventToolError, adapter.EventSubagentEnd:
		// Tool/subagent finished — promote starting→running but don't
		// override idle/needs_attention (Stop fires separately for those).
		if sess.Status == "starting" {
			newStatus = "running"
		}
	}

	lastEventAt := evt.Timestamp.UTC().Format(time.RFC3339)
	attentionScore := sess.AttentionScore
	if h.attention != nil {
		// Status already transitioned above; forget the score for sessions
		// that are terminal so we don't keep state for them.
		switch newStatus {
		case "completed", "errored", "killed":
			h.attention.Forget(sess.ID)
			attentionScore = 0
		default:
			snap := h.attention.OnEvent(sess.ID, string(evt.Type))
			attentionScore = snap.Score
		}
	}
	if err := q.UpdateSessionLastEvent(r.Context(), store.UpdateSessionLastEventParams{
		Status:         newStatus,
		LastEventAt:    &lastEventAt,
		AttentionScore: attentionScore,
		ID:             sess.ID,
	}); err != nil {
		// Non-fatal: event is stored and published even if the session timestamp update fails.
		// Log and continue.
		fmt.Printf("ingestion: update session last_event_at %s: %v\n", sess.ID, err)
	}

	protoEvt := &gruv1.SessionEvent{
		Id:        evt.ID,
		SessionId: evt.SessionID,
		ProjectId: evt.ProjectID,
		Runtime:   evt.Runtime,
		Type:      string(evt.Type),
		Timestamp: timestamppb.New(evt.Timestamp),
		Payload:   evt.Payload,
	}
	h.pub.Publish(protoEvt)

	w.WriteHeader(http.StatusAccepted)
}
