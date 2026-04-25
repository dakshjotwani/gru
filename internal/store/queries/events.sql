-- name: CreateEvent :one
INSERT INTO events (id, session_id, project_id, runtime, type, timestamp, payload)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = ? LIMIT 1;

-- name: ListEventsBySession :many
SELECT * FROM events
WHERE session_id = ?
ORDER BY seq ASC;

-- name: GetLatestEventForSession :one
SELECT * FROM events
WHERE session_id = ?
ORDER BY seq DESC
LIMIT 1;

-- name: ListEventsAfterSeq :many
SELECT * FROM events
WHERE seq > sqlc.arg(seq)
ORDER BY seq ASC
LIMIT sqlc.arg(lim);

-- name: GetHeadSeq :one
-- Returns the highest seq in the events table, or 0 if empty.
-- CAST to INTEGER pins the return type; COALESCE alone can leave it
-- as ANY/interface{} in sqlc's emitter.
SELECT CAST(COALESCE(MAX(seq), 0) AS INTEGER) AS head_seq FROM events;

-- name: DeleteEventsForSessionByID :exec
-- Wipe a session's projection rows so the tailer can rebuild from byte 0.
DELETE FROM events WHERE session_id = ?;
