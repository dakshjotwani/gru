package devices

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// ActionResolver is the callback the /actions handler invokes once a
// token is verified. sessionID is what the device is replying for;
// response is the text the server will inject into the Claude session
// (typically "1\n" for approve, "2\n" for deny, depending on the
// prompt shape). The handler is pluggable so this package doesn't
// depend on controller or session store internals.
type ActionResolver func(ctx context.Context, sessionID, response string) error

// EventLookup resolves an eventID to its originating sessionID and
// (for permission prompts) the prompt shape so the resolver can map
// approve/deny to the correct input text. Returning ok=false means
// the event isn't a pending permission prompt (no action applicable).
type EventLookup func(ctx context.Context, eventID string) (sessionID string, ok bool)

// HandlerDeps bundles the dependencies of the Mux below so tests can
// inject fakes without touching global state.
type HandlerDeps struct {
	Registry *Registry
	Resolve  ActionResolver
	Lookup   EventLookup
	Now      func() time.Time // injectable for tests; defaults to time.Now
}

// Mux returns an http.ServeMux wired with the device + action endpoints.
// Callers can use this directly (tests) or use Register to mount the
// same routes onto an existing mux.
func Mux(deps HandlerDeps) *http.ServeMux {
	mux := http.NewServeMux()
	Register(mux, deps)
	return mux
}

// Register mounts the device + action endpoints onto an existing mux.
// The server's main mux uses this to avoid a second ServeMux in front
// of the same CORS wrapper.
func Register(mux *http.ServeMux, deps HandlerDeps) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	mux.HandleFunc("POST /devices", deps.registerDevice)
	mux.HandleFunc("GET /devices", deps.listDevices)
	mux.HandleFunc("PUT /devices/{id}", deps.updateDevice)
	mux.HandleFunc("DELETE /devices/{id}", deps.deleteDevice)
	mux.HandleFunc("POST /actions/{token}", deps.action)
}

// registerRequest is the body shape POSTed by the PWA after the user
// grants Notification permission and the browser produces a push
// subscription.
type registerRequest struct {
	Label    string `json:"label"`
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

type registerResponse struct {
	ID string `json:"id"`
}

type deviceSummary struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
}

func (d HandlerDeps) registerDevice(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	dev, err := d.Registry.Register(r.Context(), req.Label, Subscription{
		Endpoint: req.Endpoint, P256dh: req.P256dh, Auth: req.Auth,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(registerResponse{ID: dev.ID})
}

func (d HandlerDeps) listDevices(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Registry.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]deviceSummary, len(rows))
	for i, row := range rows {
		out[i] = deviceSummary{ID: row.ID, Label: row.Label, CreatedAt: row.CreatedAt, LastSeenAt: row.LastSeenAt}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (d HandlerDeps) updateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req registerRequest // same shape, but label is optional here
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.Registry.UpdateSubscription(r.Context(), id, Subscription{
		Endpoint: req.Endpoint, P256dh: req.P256dh, Auth: req.Auth,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d HandlerDeps) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.Registry.Delete(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d HandlerDeps) action(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	action := r.URL.Query().Get("a")
	if action != "approve" && action != "deny" {
		http.Error(w, "a must be approve or deny", http.StatusBadRequest)
		return
	}

	// Find the device that signed this token. The token carries the
	// device ID in plaintext so we can look up the signing secret.
	_, deviceID, _, _, err := ParseActionToken(token)
	if err != nil {
		http.Error(w, "malformed token", http.StatusBadRequest)
		return
	}
	dev, err := d.Registry.Get(r.Context(), deviceID)
	if err != nil {
		http.Error(w, "unknown device", http.StatusUnauthorized)
		return
	}
	at, err := VerifyActionToken(token, dev.ActionTokenSecret, d.Now())
	switch {
	case errors.Is(err, ErrActionTokenExpired):
		http.Error(w, "expired", http.StatusGone)
		return
	case errors.Is(err, ErrActionTokenBadSignature), errors.Is(err, ErrActionTokenMalformed):
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Record the action first — the PK on (event_id, action) gives us
	// idempotency. If the insert fails because of a duplicate, the
	// caller (likely a double-tap) gets a 409.
	if err := d.Registry.RecordAction(r.Context(), at.EventID, action, at.DeviceID); err != nil {
		if isUniqueViolation(err) {
			http.Error(w, "already resolved", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Resolve the action into a SendInput on the underlying session.
	sessionID, ok := d.Lookup(r.Context(), at.EventID)
	if !ok {
		// Event wasn't a pending permission prompt. Record is already
		// there (stops future taps); reply 404 so the SW says
		// "couldn't act — open Gru".
		http.Error(w, "event not pending", http.StatusNotFound)
		return
	}
	input := "1\n"
	if action == "deny" {
		input = "2\n"
	}
	if err := d.Resolve(r.Context(), sessionID, input); err != nil {
		log.Printf("devices: resolve action session=%s: %v", sessionID, err)
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// isUniqueViolation returns true if err is a SQLite UNIQUE constraint
// failure. Kept loose because we go through the sqlc layer, which
// wraps the underlying driver error in varying ways.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") || strings.Contains(s, "constraint failed: PRIMARY KEY")
}

