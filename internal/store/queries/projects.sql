-- name: UpsertProject :one
INSERT INTO projects (id, name, adapter, runtime)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    name    = excluded.name,
    adapter = excluded.adapter,
    runtime = excluded.runtime
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? LIMIT 1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name ASC;

-- name: RenameProject :one
UPDATE projects
SET name = sqlc.arg(name)
WHERE id = sqlc.arg(id)
RETURNING *;
