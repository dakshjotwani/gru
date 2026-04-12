-- name: CreateEvent :one
INSERT INTO events (id, session_id, project_id, runtime, type, timestamp, payload)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListEventsBySession :many
SELECT * FROM events
WHERE session_id = ?
ORDER BY timestamp ASC;

-- name: GetLatestEventForSession :one
SELECT * FROM events
WHERE session_id = ?
ORDER BY timestamp DESC
LIMIT 1;
