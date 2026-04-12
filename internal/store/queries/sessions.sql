-- name: CreateSession :one
INSERT INTO sessions (id, project_id, runtime, status, profile, pid, pgid, tmux_session, tmux_window)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ? LIMIT 1;

-- name: ListSessions :many
SELECT * FROM sessions
WHERE project_id = COALESCE(NULLIF(sqlc.arg(project_id), ''), project_id)
  AND status = COALESCE(NULLIF(sqlc.arg(status), ''), status)
ORDER BY started_at DESC;

-- name: UpdateSessionStatus :one
UPDATE sessions
SET status   = sqlc.arg(status),
    ended_at = COALESCE(ended_at, sqlc.narg(ended_at))
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateSessionLastEvent :exec
UPDATE sessions
SET status = ?,
    last_event_at = ?,
    attention_score = ?
WHERE id = ?;

-- name: UpdateSessionPID :exec
UPDATE sessions SET pid = ?, pgid = ? WHERE id = ?;
