package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LogPath returns the per-session event log path. Both the appender
// and the tailer use this so they don't disagree on layout.
func LogPath(homeDir, sessionID string) string {
	return filepath.Join(homeDir, ".gru", "events", sessionID+".jsonl")
}

// Append writes one Event to the per-session log. Atomic across
// concurrent callers within OS PIPE_BUF guarantees — Event lines are
// well under that, so multiple gru hook ingest processes appending
// concurrently do not interleave.
//
// The Version and Ts fields are populated automatically if zero/empty.
// Callers should set Type and any type-specific fields and leave the
// metadata to Append.
func Append(homeDir, sessionID string, ev Event) error {
	if sessionID == "" {
		return fmt.Errorf("ingest.Append: empty sessionID")
	}
	if ev.Version == 0 {
		ev.Version = SchemaVersion
	}
	if ev.Ts == "" {
		ev.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ingest.Append: marshal: %w", err)
	}
	if len(line) > 4000 {
		// PIPE_BUF on macOS is 512, on Linux 4096; we want to stay well
		// under so concurrent appends don't interleave. If a payload
		// approaches the cap, the translator should be slimming it down
		// rather than passing through large blobs.
		return fmt.Errorf("ingest.Append: line too large (%d bytes); shrink the event", len(line))
	}

	path := LogPath(homeDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ingest.Append: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ingest.Append: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ingest.Append: write: %w", err)
	}
	return nil
}
