-- name: CreateArtifact :one
INSERT INTO artifacts (id, session_id, title, mime_type, size_bytes, token)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetArtifact :one
SELECT * FROM artifacts WHERE id = ? LIMIT 1;

-- name: GetArtifactByToken :one
SELECT * FROM artifacts WHERE token = ? LIMIT 1;

-- name: ListArtifactsBySession :many
SELECT * FROM artifacts
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: SumArtifactsForSession :one
SELECT
  COUNT(*)                                      AS count,
  CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER) AS bytes_used
FROM artifacts
WHERE session_id = ?;

-- name: DeleteArtifact :exec
DELETE FROM artifacts WHERE id = ?;

-- name: ListAllArtifactSessionDirs :many
SELECT DISTINCT session_id FROM artifacts;

-- name: ListArtifactIDsBySession :many
SELECT id FROM artifacts WHERE session_id = ?;
