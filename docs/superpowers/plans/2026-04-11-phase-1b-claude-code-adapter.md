# Gru Phase 1b — Claude Code Adapter & Hook Scripts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire Claude Code hook events into Gru by implementing a Claude-specific EventNormalizer, verifying session ownership in the ingestion handler, a deployable hook shell script, and a `gru init <project-dir>` command that installs it.

**Architecture:** A `ClaudeNormalizer` satisfies the `EventNormalizer` interface from Phase 1a and is registered in the adapter Registry. The ingestion handler reads `X-Gru-Session-ID` from the request header, looks up the session in the DB (returning 404 if not found), and sets `evt.SessionID` on the normalized event — no auto-creation. Sessions are created by `gru launch`, which injects `GRU_SESSION_ID` into the tmux environment; the hook script only fires when that variable is set. The `gru init` subcommand copies the hook script and merges hook entries into `.claude/settings.json`.

**Tech Stack:** Go 1.23+, `encoding/json`, `path/filepath`, `os`, `github.com/google/uuid`, existing `internal/adapter`, `internal/ingestion`, `internal/store` packages from Phase 1a.

---

## File Map

```
internal/adapter/claude/normalizer.go       # ClaudeNormalizer: EventNormalizer for Claude Code
internal/adapter/claude/normalizer_test.go  # unit tests for ClaudeNormalizer
internal/ingestion/handler.go               # MODIFIED: verify session ownership via X-Gru-Session-ID header
internal/ingestion/handler_test.go          # MODIFIED: tests for session ownership check
hooks/claude-code.sh                        # hook script template (fire-and-forget curl)
cmd/gru/init.go                             # `gru init <project-dir>` implementation
cmd/gru/init_test.go                        # unit tests for init command
cmd/gru/main.go                             # MODIFIED: add "init" case
```

---

## Task 1: Claude Code EventNormalizer

**Files:**
- Create: `internal/adapter/claude/normalizer.go`
- Create: `internal/adapter/claude/normalizer_test.go`

### Step 1 — Write the failing test

- [ ] Create `internal/adapter/claude/normalizer_test.go`:

```go
package claude_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
)

func TestClaudeNormalizer_RuntimeID(t *testing.T) {
	n := claude.NewNormalizer()
	if got := n.RuntimeID(); got != "claude-code" {
		t.Fatalf("RuntimeID() = %q; want %q", got, "claude-code")
	}
}

func TestClaudeNormalizer_PreToolUse(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{
		"hook_event_name": "PreToolUse",
		"session_id": "sess-abc",
		"cwd": "/home/user/myproject",
		"tool_name": "Bash",
		"tool_input": {"command": "ls"}
	}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeToolPre {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeToolPre)
	}
	// SessionID is set by the ingestion handler from the X-Gru-Session-ID header
	// after normalization; the normalizer itself does not set it.
	if ev.ID == "" {
		t.Error("ID must not be empty")
	}
	if ev.Runtime != "claude-code" {
		t.Errorf("Runtime = %q; want %q", ev.Runtime, "claude-code")
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

func TestClaudeNormalizer_PostToolUse(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"PostToolUse","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeToolPost {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeToolPost)
	}
}

func TestClaudeNormalizer_PostToolUseFailure(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"PostToolUseFailure","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeToolError {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeToolError)
	}
}

func TestClaudeNormalizer_Stop(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Stop","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeSessionEnd {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeSessionEnd)
	}
}

func TestClaudeNormalizer_Notification(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Notification","session_id":"s1","cwd":"/p","message":"hello"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeNotification {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeNotification)
	}
}

func TestClaudeNormalizer_SubagentStart(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"SubagentStart","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeSubagentStart {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeSubagentStart)
	}
}

func TestClaudeNormalizer_SubagentStop(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"SubagentStop","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventTypeSubagentEnd {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventTypeSubagentEnd)
	}
}

func TestClaudeNormalizer_UnknownEvent(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"WhateverNew","session_id":"s1","cwd":"/p"}`)
	_, err := n.Normalize(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for unknown hook_event_name, got nil")
	}
}

func TestClaudeNormalizer_PayloadPreserved(t *testing.T) {
	n := claude.NewNormalizer()
	rawStr := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/p","tool_name":"Bash","tool_input":{"command":"ls"}}`
	ev, err := n.Normalize(context.Background(), json.RawMessage(rawStr))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ev.Payload) == 0 {
		t.Error("Payload must not be empty")
	}
}
```

- [ ] Run the test and confirm it fails (package doesn't exist yet):

```bash
cd /path/to/gru
go test ./internal/adapter/claude/...
```

Expected output:
```
can't load package: package github.com/dakshjotwani/gru/internal/adapter/claude: ...no Go files...
```

### Step 2 — Implement the normalizer

- [ ] Create `internal/adapter/claude/normalizer.go`:

> **Note:** Session and project identity is known before the hook fires — Gru injects
> `GRU_SESSION_ID` at launch time and the ingestion handler looks up the session by the
> `X-Gru-Session-ID` header. The normalizer only needs to map event types and extract
> tool/notification metadata. `SessionID` is left empty here; the handler sets
> `evt.SessionID` from the header after `Normalize()` returns.

```go
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/google/uuid"
)

// hookPayload is the raw shape of a Claude Code hook event.
type hookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	Message       string          `json:"message,omitempty"`
}

// Normalizer converts Claude Code hook payloads into GruEvents.
type Normalizer struct{}

// NewNormalizer returns a ready-to-use Normalizer.
func NewNormalizer() *Normalizer { return &Normalizer{} }

// RuntimeID satisfies EventNormalizer; identifies Claude Code events.
func (n *Normalizer) RuntimeID() string { return "claude-code" }

// Normalize parses raw Claude Code hook JSON and returns a GruEvent.
// SessionID is intentionally left empty — the ingestion handler sets it from
// the X-Gru-Session-ID request header after this call returns.
// Returns an error if hook_event_name is unrecognised.
func (n *Normalizer) Normalize(_ context.Context, raw json.RawMessage) (*adapter.GruEvent, error) {
	var p hookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("claude normalizer: unmarshal: %w", err)
	}

	eventType, err := mapEventType(p.HookEventName)
	if err != nil {
		return nil, err
	}

	return &adapter.GruEvent{
		ID:        uuid.NewString(),
		Runtime:   "claude-code",
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}, nil
}

// mapEventType converts a Claude Code hook_event_name to a GruEvent EventType.
func mapEventType(name string) (adapter.EventType, error) {
	switch name {
	case "PreToolUse":
		return adapter.EventTypeToolPre, nil
	case "PostToolUse":
		return adapter.EventTypeToolPost, nil
	case "PostToolUseFailure":
		return adapter.EventTypeToolError, nil
	case "Stop":
		return adapter.EventTypeSessionEnd, nil
	case "Notification":
		return adapter.EventTypeNotification, nil
	case "SubagentStart":
		return adapter.EventTypeSubagentStart, nil
	case "SubagentStop":
		return adapter.EventTypeSubagentEnd, nil
	default:
		return "", fmt.Errorf("claude normalizer: unknown hook_event_name %q", name)
	}
}
```

- [ ] Run tests and confirm they pass:

```bash
cd /path/to/gru
go test ./internal/adapter/claude/... -v
```

Expected output (all PASS):
```
--- PASS: TestClaudeNormalizer_RuntimeID (0.00s)
--- PASS: TestClaudeNormalizer_PreToolUse (0.00s)
--- PASS: TestClaudeNormalizer_PostToolUse (0.00s)
--- PASS: TestClaudeNormalizer_PostToolUseFailure (0.00s)
--- PASS: TestClaudeNormalizer_Stop (0.00s)
--- PASS: TestClaudeNormalizer_Notification (0.00s)
--- PASS: TestClaudeNormalizer_SubagentStart (0.00s)
--- PASS: TestClaudeNormalizer_SubagentStop (0.00s)
--- PASS: TestClaudeNormalizer_UnknownEvent (0.00s)
--- PASS: TestClaudeNormalizer_PayloadPreserved (0.00s)
ok  	github.com/dakshjotwani/gru/internal/adapter/claude
```

- [ ] Commit:

```bash
git add internal/adapter/claude/
git commit -m "feat: add Claude Code EventNormalizer"
```

---

## Task 2: Register ClaudeNormalizer at server startup

**Files:**
- Modify: `cmd/gru/server.go` — register `ClaudeNormalizer` in the adapter Registry

### Step 1 — Write a failing test

The Registry already has tests in Phase 1a. Add a single integration-level check: after `server.go` wires things up, the Registry must return a `"claude-code"` normalizer.

- [ ] Add to `internal/adapter/normalizer_test.go` (append new test function):

```go
func TestRegistry_ClaudeCodeRegistered(t *testing.T) {
	r := adapter.NewRegistry()
	r.Register(claude.NewNormalizer())
	n := r.Get("claude-code")
	if n == nil {
		t.Fatal("expected claude-code normalizer to be registered, got nil")
	}
	if n.RuntimeID() != "claude-code" {
		t.Errorf("RuntimeID() = %q; want %q", n.RuntimeID(), "claude-code")
	}
}
```

Also add the import at the top of that test file:
```go
import (
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
)
```

- [ ] Run and confirm it fails (import of `claude` sub-package is new, will fail to compile if adapter package doesn't yet import it):

```bash
go test ./internal/adapter/... -run TestRegistry_ClaudeCodeRegistered -v
```

### Step 2 — Wire the normalizer in server startup

- [ ] Edit `cmd/gru/server.go`. Find where the adapter `Registry` is constructed (or created inline) and add:

```go
import "github.com/dakshjotwani/gru/internal/adapter/claude"

// inside runServer, after registry := adapter.NewRegistry() (or equivalent)
registry.Register(claude.NewNormalizer())
```

- [ ] Run the test and confirm it passes:

```bash
go test ./internal/adapter/... -v
```

Expected: all adapter tests PASS.

- [ ] Commit:

```bash
git add cmd/gru/server.go internal/adapter/normalizer_test.go
git commit -m "feat: register ClaudeNormalizer in server adapter registry"
```

---

## Task 3: Ingestion handler — verify session ownership

**Files:**
- Modify: `internal/ingestion/handler.go`
- Modify: `internal/ingestion/handler_test.go`

The handler reads `X-Gru-Session-ID` from the request header, looks up the session in the DB, and rejects requests for sessions Gru did not launch. After a successful lookup it sets `evt.SessionID` on the normalized event (the normalizer leaves it empty).

### Step 1 — Write failing tests

- [ ] Add the following test cases to `internal/ingestion/handler_test.go`.

These tests assume the handler already has a constructor like `NewHandler(store, registry)`. Append after existing tests:

```go
func TestHandler_MissingSessionIDHeader(t *testing.T) {
	s := newTestStore(t)
	reg := adapter.NewRegistry()
	reg.Register(claude.NewNormalizer())
	h := ingestion.NewHandler(s, reg)

	body := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("X-Gru-Runtime", "claude-code")
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("Content-Type", "application/json")
	// X-Gru-Session-ID header intentionally omitted
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandler_UnknownSessionID(t *testing.T) {
	s := newTestStore(t)
	reg := adapter.NewRegistry()
	reg.Register(claude.NewNormalizer())
	h := ingestion.NewHandler(s, reg)

	body := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("X-Gru-Runtime", "claude-code")
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Session-ID", "not-a-real-session")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandler_KnownSessionID(t *testing.T) {
	s := newTestStore(t)
	// Pre-seed a session so the handler can look it up.
	ctx := context.Background()
	_ = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:        "known-sess-1",
		ProjectID: "proj-1",
		Runtime:   "claude-code",
		Status:    "starting",
	})

	reg := adapter.NewRegistry()
	reg.Register(claude.NewNormalizer())
	h := ingestion.NewHandler(s, reg)

	body := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
	req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	req.Header.Set("X-Gru-Runtime", "claude-code")
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Session-ID", "known-sess-1")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}
}
```

Add required imports to the test file:
```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
)
```

- [ ] Run and confirm they fail (ownership check not yet implemented):

```bash
go test ./internal/ingestion/... -run "TestHandler_MissingSessionIDHeader|TestHandler_UnknownSessionID|TestHandler_KnownSessionID" -v
```

Expected: compilation error or test failures.

### Step 2 — Implement session ownership check in the handler

- [ ] Edit `internal/ingestion/handler.go`. In the `ServeHTTP` method, before calling `normalizer.Normalize`, add:

```go
// Verify session ownership — only Gru-launched sessions are accepted.
sessionID := r.Header.Get("X-Gru-Session-ID")
if sessionID == "" {
	http.Error(w, "missing X-Gru-Session-ID header", http.StatusBadRequest)
	return
}
_, err := h.store.Queries().GetSession(r.Context(), sessionID)
if errors.Is(err, sql.ErrNoRows) {
	http.Error(w, "session not found", http.StatusNotFound)
	return
} else if err != nil {
	http.Error(w, "internal error", http.StatusInternalServerError)
	return
}
```

Then after `ev, err := normalizer.Normalize(...)` succeeds, set:

```go
// SessionID is known from the verified header; the normalizer leaves it empty.
ev.SessionID = sessionID
```

- [ ] Run the tests and confirm they pass:

```bash
go test ./internal/ingestion/... -v
```

Expected: all ingestion tests PASS.

- [ ] Commit:

```bash
git add internal/ingestion/
git commit -m "feat: verify session ownership in ingestion handler via X-Gru-Session-ID"
```

---

## Task 4: Hook script template

**Files:**
- Create: `hooks/claude-code.sh`

### Step 1 — Create the script

- [ ] Create `hooks/claude-code.sh`:

```bash
#!/bin/bash
# Gru hook script for Claude Code.
# Only fires when this session was launched by gru (GRU_SESSION_ID is set).
[ -n "$GRU_SESSION_ID" ] || exit 0

curl -s -m 2 -X POST \
  "http://${GRU_HOST:-localhost}:${GRU_PORT:-7777}/events" \
  -H "Authorization: Bearer ${GRU_API_KEY}" \
  -H "Content-Type: application/json" \
  -H "X-Gru-Runtime: claude-code" \
  -H "X-Gru-Session-ID: ${GRU_SESSION_ID}" \
  -d "$CLAUDE_HOOK_EVENT" &
```

- [ ] Make it executable:

```bash
chmod +x hooks/claude-code.sh
```

- [ ] Manual smoke test (verify syntax only — no running server required):

```bash
bash -n hooks/claude-code.sh && echo "syntax OK"
```

Expected:
```
syntax OK
```

- [ ] Commit:

```bash
git add hooks/claude-code.sh
git commit -m "feat: add claude-code hook script template"
```

---

## Task 5: `gru init` command

**Files:**
- Create: `cmd/gru/init.go`
- Create: `cmd/gru/init_test.go`
- Modify: `cmd/gru/main.go`

> **Hook behavior note:** The hook script is installed globally in the project but only fires
> when `GRU_SESSION_ID` is set — so manually running `claude` in the same project will not
> send events to Gru. Only sessions launched by `gru launch` will be tracked.

### Step 1 — Write failing tests

- [ ] Create `cmd/gru/init_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunInit_CreatesHookScriptAndSettings(t *testing.T) {
	projectDir := t.TempDir()

	// Point hookSrcPath to the actual hook script in the repo root.
	// In tests we override via the package-level variable hookScriptSrc.
	origSrc := hookScriptSrc
	hookScriptSrc = filepath.Join(repoRoot(), "hooks", "claude-code.sh")
	defer func() { hookScriptSrc = origSrc }()

	if err := runInit([]string{projectDir}); err != nil {
		t.Fatalf("runInit error: %v", err)
	}

	// Hook script must be copied.
	hookDst := filepath.Join(projectDir, ".gru", "hooks", "gru-hook.sh")
	info, err := os.Stat(hookDst)
	if err != nil {
		t.Fatalf("hook script not created at %s: %v", hookDst, err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("hook script must be executable")
	}

	// settings.json must exist and contain all expected hook keys.
	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json not valid JSON: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("settings.json missing 'hooks' object")
	}

	expected := []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "Notification", "Stop", "SubagentStart", "SubagentStop"}
	for _, key := range expected {
		if _, exists := hooks[key]; !exists {
			t.Errorf("hooks missing key %q", key)
		}
	}
}

func TestRunInit_MergesExistingSettings(t *testing.T) {
	projectDir := t.TempDir()

	// Pre-create a settings.json with existing keys.
	claudeDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"/old/hook.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	origSrc := hookScriptSrc
	hookScriptSrc = filepath.Join(repoRoot(), "hooks", "claude-code.sh")
	defer func() { hookScriptSrc = origSrc }()

	if err := runInit([]string{projectDir}); err != nil {
		t.Fatalf("runInit error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(projectDir, ".claude", "settings.json"))
	var settings map[string]interface{}
	json.Unmarshal(data, &settings)

	// Existing non-hook key must be preserved.
	if settings["theme"] != "dark" {
		t.Errorf("existing 'theme' key was lost; settings: %s", data)
	}

	hooks := settings["hooks"].(map[string]interface{})
	// All 7 hook types must be present.
	for _, key := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "Notification", "Stop", "SubagentStart", "SubagentStop"} {
		if _, exists := hooks[key]; !exists {
			t.Errorf("hooks missing key %q after merge", key)
		}
	}
}

func TestRunInit_MissingArgument(t *testing.T) {
	if err := runInit([]string{}); err == nil {
		t.Fatal("expected error when no project dir given, got nil")
	}
}

// repoRoot walks up from the test binary location to find the module root.
// Works because go test sets the working directory to the package directory.
func repoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
```

- [ ] Run and confirm it fails:

```bash
go test ./cmd/gru/... -run "TestRunInit" -v
```

Expected: compilation error (runInit, hookScriptSrc not defined yet).

### Step 2 — Implement `cmd/gru/init.go`

- [ ] Create `cmd/gru/init.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// hookScriptSrc is the path to the hook script to copy.
// Overridable in tests.
var hookScriptSrc = func() string {
	// Default: resolve relative to the binary's location using the module root convention.
	// At runtime the binary is built from the repo root; at test time repoRoot() is used.
	dir, _ := os.Executable()
	root := filepath.Dir(filepath.Dir(dir)) // binary lives in bin/, go up twice
	candidate := filepath.Join(root, "hooks", "claude-code.sh")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fallback: relative to working directory (useful when running directly with `go run`).
	return filepath.Join("hooks", "claude-code.sh")
}()

// hookTypes lists every Claude Code hook event that Gru intercepts.
var hookTypes = []string{
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Notification",
	"Stop",
	"SubagentStart",
	"SubagentStop",
}

// runInit implements the `gru init <project-dir>` subcommand.
func runInit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gru init <project-dir>")
	}
	projectDir := args[0]

	// 1. Copy hook script to <project-dir>/.gru/hooks/gru-hook.sh
	hookDst := filepath.Join(projectDir, ".gru", "hooks", "gru-hook.sh")
	if err := os.MkdirAll(filepath.Dir(hookDst), 0o755); err != nil {
		return fmt.Errorf("create .gru/hooks dir: %w", err)
	}
	if err := copyFile(hookScriptSrc, hookDst, 0o755); err != nil {
		return fmt.Errorf("copy hook script: %w", err)
	}

	// 2. Read or initialise .claude/settings.json
	settingsDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")

	settings := map[string]interface{}{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err2 := json.Unmarshal(data, &settings); err2 != nil {
			return fmt.Errorf("parse existing settings.json: %w", err2)
		}
	}

	// 3. Merge hook entries — one entry per hook type, matcher "" catches all tools.
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	hookEntry := map[string]interface{}{
		"type":    "command",
		"command": hookDst,
	}
	hookBlock := []interface{}{
		map[string]interface{}{
			"matcher": "",
			"hooks":   []interface{}{hookEntry},
		},
	}
	for _, ht := range hookTypes {
		hooks[ht] = hookBlock
	}
	settings["hooks"] = hooks

	// 4. Write back settings.json
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}

	// 5. Print instructions.
	fmt.Printf("Gru hooks installed for %s\n\n", projectDir)
	fmt.Printf("Hook script: %s\n", hookDst)
	fmt.Printf("Settings:    %s\n\n", settingsPath)
	fmt.Println("Next step: set GRU_API_KEY in your shell environment:")
	fmt.Println("  export GRU_API_KEY=<your-key>")
	fmt.Println("  # Optionally: export GRU_HOST=<host>  GRU_PORT=<port>")

	return nil
}

// copyFile copies src to dst with the given permission bits.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
```

- [ ] Run tests and confirm they pass:

```bash
go test ./cmd/gru/... -run "TestRunInit" -v
```

Expected:
```
--- PASS: TestRunInit_CreatesHookScriptAndSettings (0.00s)
--- PASS: TestRunInit_MergesExistingSettings (0.00s)
--- PASS: TestRunInit_MissingArgument (0.00s)
ok  	github.com/dakshjotwani/gru/cmd/gru
```

### Step 3 — Wire `init` into `cmd/gru/main.go`

- [ ] Edit `cmd/gru/main.go`. Locate the `switch` (or `if/else`) that dispatches subcommands and add:

```go
case "init":
    if err := runInit(args[1:]); err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }
```

Also update the usage/help text to include `init`:

```go
fmt.Fprintf(os.Stderr, "Usage: gru <server|init> [args...]\n")
```

- [ ] Verify the binary builds:

```bash
go build ./cmd/gru/...
```

Expected: no output (clean build).

- [ ] Smoke-test the subcommand with a temp dir:

```bash
TMPDIR=$(mktemp -d)
./gru init "$TMPDIR" && echo "init OK"
ls "$TMPDIR/.gru/hooks/" "$TMPDIR/.claude/"
```

Expected output similar to:
```
Gru hooks installed for /tmp/tmpXXXXXX

Hook script: /tmp/tmpXXXXXX/.gru/hooks/gru-hook.sh
Settings:    /tmp/tmpXXXXXX/.claude/settings.json

Next step: set GRU_API_KEY in your shell environment:
  export GRU_API_KEY=<your-key>
  # Optionally: export GRU_HOST=<host>  GRU_PORT=<port>
init OK
gru-hook.sh
settings.json
```

- [ ] Commit:

```bash
git add cmd/gru/init.go cmd/gru/init_test.go cmd/gru/main.go
git commit -m "feat: add gru init subcommand to install Claude Code hooks"
```

---

## Task 6: Full integration smoke test

**No new files.** Verify everything wires together before closing the phase.

- [ ] Run all tests:

```bash
go test ./... -v 2>&1 | tail -20
```

Expected: no FAIL lines. All packages report `ok`.

- [ ] Run vet and staticcheck:

```bash
go vet ./...
```

Expected: no output.

- [ ] Commit (if any lint fixes were needed):

```bash
git add -p
git commit -m "chore: fix vet warnings from phase 1b"
```

- [ ] Tag the phase complete:

```bash
git tag phase-1b
```

---

## Self-review checklist

- [x] Every task has complete, copy-pasteable code with no placeholders or "TBD"
- [x] Every task writes a failing test before the implementation (TDD)
- [x] Type and function names are consistent: `Normalizer`, `NewNormalizer`, `hookScriptSrc`, `runInit`
- [x] All `EventType` constants used (`EventTypeToolPre`, `EventTypeToolPost`, `EventTypeToolError`, `EventTypeSessionEnd`, `EventTypeNotification`, `EventTypeSubagentStart`, `EventTypeSubagentEnd`) match the `adapter.EventType` definitions from Phase 1a
- [x] Ingestion handler reads `X-Gru-Session-ID`, returns 400 if missing, 404 if not found, sets `evt.SessionID` from the header after normalization
- [x] `GetSession` query signature matches the Phase 1a store contract exactly
- [x] `GruEvent` fields (`ID`, `SessionID`, `ProjectID`, `Runtime`, `Type`, `Timestamp`, `Payload`) match Phase 1a's `GruEvent` struct
- [x] All 7 Claude Code hook types are handled: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `Stop`, `Notification`, `SubagentStart`, `SubagentStop`
- [x] `hooks/claude-code.sh` guards with `[ -n "$GRU_SESSION_ID" ] || exit 0` so it only fires for Gru-launched sessions
- [x] `hooks/claude-code.sh` sends `X-Gru-Session-ID: ${GRU_SESSION_ID}` header and uses fire-and-forget `curl ... &` so Claude Code is not blocked
- [x] `gru init` merges (not replaces) existing `settings.json` content
- [x] `hookScriptSrc` is a package-level variable so tests can override it without `os.Chdir`
