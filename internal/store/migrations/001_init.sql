-- Projects: known codebases managed by gru.
CREATE TABLE IF NOT EXISTS projects (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL UNIQUE,
    runtime    TEXT NOT NULL DEFAULT 'claude-code',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- Sessions: one row per agent process lifecycle.
CREATE TABLE IF NOT EXISTS sessions (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id),
    runtime         TEXT NOT NULL DEFAULT 'claude-code',
    -- status values: starting | running | idle | needs_attention | completed | errored | killed
    status          TEXT NOT NULL DEFAULT 'starting',
    profile         TEXT,
    pid             INTEGER,
    pgid            INTEGER,
    attention_score REAL NOT NULL DEFAULT 1.0,
    started_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    ended_at        TEXT,         -- NULL while running
    last_event_at   TEXT,         -- NULL until first event
    tmux_session    TEXT,    -- NULL for externally detected sessions
    tmux_window     TEXT,    -- tmux window name within the project session
    name            TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    prompt          TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_project_id ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status     ON sessions(status);

-- Events: append-only log of all hook events from all runtimes.
CREATE TABLE IF NOT EXISTS events (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    runtime    TEXT NOT NULL,
    type       TEXT NOT NULL,
    timestamp  TEXT NOT NULL,
    payload    TEXT NOT NULL,      -- raw JSON
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_timestamp  ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type       ON events(type);

-- Schema version tracking (for future migrations).
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);
INSERT OR IGNORE INTO schema_migrations (version) VALUES (1);
