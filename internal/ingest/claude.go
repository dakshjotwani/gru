package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// claudeHookPayload is the shape `gru hook ingest` reads from stdin.
// We only declare the fields we actually consume — Claude adds new
// keys regularly and the translator should ignore unknown noise.
type claudeHookPayload struct {
	HookEventName    string          `json:"hook_event_name"`
	SessionID        string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	NotificationType string          `json:"notification_type,omitempty"`
	Message          string          `json:"message,omitempty"`
	StopReason       string          `json:"stop_reason,omitempty"`
	ToolName         string          `json:"tool_name,omitempty"`
	Source           string          `json:"source,omitempty"` // SessionStart subtype: startup/resume
	raw              json.RawMessage // captured separately for unknown-arm pass-through
}

// TranslateClaudeHook parses a Claude Code hook payload from stdin
// bytes, resolves the gru session id, and returns the corresponding
// gru Event ready for Append.
//
// The sibling-Claude guard runs here: Claude's own session_id (the
// process firing the hook) must match the gru session id resolved
// from the cwd-local lookup file, otherwise we reject. This is the
// rule that today's debug session pulled bash-side; in rev-3 it
// lives in one Go function with one set of tests.
//
// On guard rejection, returns ("", nil) — caller exits 0. The cwd
// lookup file is the canonical source for gru session id; Claude
// scrubs hook env so $GRU_SESSION_ID is unreliable.
func TranslateClaudeHook(stdinPayload []byte) (sessionID string, ev Event, err error) {
	var p claudeHookPayload
	if err := json.Unmarshal(stdinPayload, &p); err != nil {
		return "", Event{}, fmt.Errorf("translate: parse stdin: %w", err)
	}
	p.raw = stdinPayload

	gruSID, err := resolveSessionID(p.Cwd)
	if err != nil || gruSID == "" {
		// No cwd file = not a gru-launched session. Exit cleanly.
		return "", Event{}, nil
	}
	if p.SessionID != "" && p.SessionID != gruSID {
		// Sibling-Claude guard: a bare `claude` (or a --resume'd
		// agent that minted a fresh uuid) is firing a hook in this
		// gru session's cwd. Reject — its events are not ours.
		return "", Event{}, nil
	}

	ev = translateEvent(p)
	return gruSID, ev, nil
}

// resolveSessionID reads the cwd-local file written at launch by the
// controller. Returns "" if no file exists (i.e. not a gru-launched
// agent).
func resolveSessionID(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	// Primary: <cwd>/.gru/session-id (non-worktree launches).
	if data, err := os.ReadFile(filepath.Join(cwd, ".gru", "session-id")); err == nil {
		return string(trimNewline(data)), nil
	}
	// Fallback: worktree convention <project>/.gru/sessions/<short>.
	short := filepath.Base(cwd)
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(cwd)))
	if data, err := os.ReadFile(filepath.Join(projectRoot, ".gru", "sessions", short)); err == nil {
		return string(trimNewline(data)), nil
	}
	return "", nil
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

// translateEvent maps a Claude hook payload onto gru's grammar. Hook
// events we don't have a typed arm for land on TypeUnknown with the
// raw payload preserved — visible in the log without forcing a code
// change in the fold.
func translateEvent(p claudeHookPayload) Event {
	switch p.HookEventName {
	case "SessionStart":
		// Sub-source startup vs resume — we treat both as
		// session_started for now; the rev-3 contract is no --resume
		// support, so a "resume" SessionStart will arrive only when
		// the user broke that contract; logging is enough.
		return Event{Type: TypeSessionStarted, Trigger: p.Source}

	case "UserPromptSubmit":
		return Event{Type: TypeTurnStarted, Trigger: "user_prompt"}

	case "PostToolUse":
		ok := true
		return Event{Type: TypeToolCompleted, Tool: p.ToolName, Ok: &ok}
	case "PostToolUseFailure":
		ok := false
		return Event{Type: TypeToolCompleted, Tool: p.ToolName, Ok: &ok}

	case "Stop":
		return Event{Type: TypeTurnEnded, StopReason: p.StopReason}
	case "StopFailure":
		return Event{Type: TypeTurnEnded, StopReason: "error"}

	case "Notification":
		// Only the three attention-affecting subtypes flip status.
		// Other Notification subtypes (if any future ones) land in
		// `unknown` until we explicitly fold them.
		switch p.NotificationType {
		case "idle_prompt", "permission_prompt", "elicitation_dialog":
			return Event{Type: TypeAttentionRequested, Reason: p.NotificationType, Message: p.Message}
		}
	}

	// Unknown / passthrough.
	return Event{Type: TypeUnknown, ClaudeEvent: p.HookEventName, Raw: p.raw}
}
