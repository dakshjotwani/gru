-- name: CreateSession :one
INSERT INTO sessions (id, project_id, runtime, status, profile, pid, pgid, tmux_session, tmux_window, name, description, prompt, role, transcript_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ? LIMIT 1;

-- name: GetAssistantSession :one
SELECT * FROM sessions
WHERE role = 'assistant'
  AND status IN ('starting','running','idle','needs_attention')
ORDER BY started_at DESC
LIMIT 1;

-- name: ListSessions :many
SELECT * FROM sessions
WHERE project_id = COALESCE(NULLIF(sqlc.arg(project_id), ''), project_id)
  AND status = COALESCE(NULLIF(sqlc.arg(status), ''), status)
ORDER BY started_at DESC;

-- name: ListNonTerminalSessions :many
-- Used by the tailer manager at startup to find every session that needs a
-- live tailer goroutine.
SELECT * FROM sessions
WHERE status IN ('starting','running','idle','needs_attention');

-- name: UpdateSessionStatus :one
UPDATE sessions
SET status   = sqlc.arg(status),
    ended_at = COALESCE(ended_at, sqlc.narg(ended_at))
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateSessionDerived :exec
-- Single writer of derived fields, called from the tailer's commit
-- transaction. Combines status / attention_score / claude_stop_reason /
-- permission_mode / last_event_at into one statement so the row never
-- transiently disagrees with the events projection.
UPDATE sessions
SET status             = ?,
    attention_score    = ?,
    last_event_at      = ?,
    claude_stop_reason = ?,
    permission_mode    = ?
WHERE id = ?;

-- name: UpdateSessionTranscriptPath :exec
UPDATE sessions SET transcript_path = ? WHERE id = ?;

-- name: UpdateSessionAttentionScore :one
UPDATE sessions
SET attention_score = sqlc.arg(attention_score)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateSessionPID :exec
UPDATE sessions SET pid = ?, pgid = ? WHERE id = ?;

-- name: DeleteEventsForSession :exec
DELETE FROM events WHERE session_id = sqlc.arg(id);

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = sqlc.arg(id);

-- name: ListTerminalSessionIDs :many
-- Returns IDs of every terminal (completed/errored/killed) session, skipping
-- assistant-role singletons. Used by PruneSessions to delete in a single
-- atomic loop without an extra ListSessions round-trip.
SELECT id FROM sessions
WHERE status IN ('completed','errored','killed')
  AND role <> 'assistant';
