-- State pipeline rev 2: transcript-tailer architecture.
--
-- Adds columns to `sessions` for state derived from Claude's per-session
-- JSONL transcript (see docs/superpowers/specs/2026-04-24-state-pipeline-design.md):
--
--   - transcript_path: full path to ~/.claude/projects/<hash>/<sid>.jsonl
--   - claude_stop_reason: last assistant.stop_reason ("end_turn", "tool_use", ...)
--   - permission_mode: last seen permission-mode value ("default", "plan", ...)
--
-- Drops and recreates `events` so it gets a monotonic seq column. The events
-- table is now a derived projection rebuilt from the JSONL on every server
-- start — no migration of historical rows is necessary, and there is no
-- persisted offset (see anti-pattern #12 in the spec).

ALTER TABLE sessions ADD COLUMN transcript_path TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN claude_stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN permission_mode TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_events_session_id;
DROP INDEX IF EXISTS idx_events_timestamp;
DROP INDEX IF EXISTS idx_events_type;
DROP TABLE IF EXISTS events;

CREATE TABLE events (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    id         TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id),
    runtime    TEXT NOT NULL,
    type       TEXT NOT NULL,
    timestamp  TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX idx_events_session_id ON events(session_id);
CREATE INDEX idx_events_seq        ON events(seq);
