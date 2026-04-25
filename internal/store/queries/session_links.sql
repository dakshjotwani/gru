-- name: CreateSessionLink :one
INSERT INTO session_links (id, session_id, title, url)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetSessionLink :one
SELECT * FROM session_links WHERE id = ? LIMIT 1;

-- name: ListSessionLinksBySession :many
SELECT * FROM session_links
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: CountSessionLinksForSession :one
SELECT COUNT(*) AS count FROM session_links WHERE session_id = ?;

-- name: DeleteSessionLink :exec
DELETE FROM session_links WHERE id = ?;
