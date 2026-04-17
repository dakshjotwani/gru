-- name: UpsertProject :one
INSERT INTO projects (id, name, path, runtime)
VALUES (?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
    name    = excluded.name,
    runtime = excluded.runtime
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? LIMIT 1;

-- name: GetProjectByPath :one
SELECT * FROM projects WHERE path = ? LIMIT 1;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY name ASC;

-- name: UpdateProjectAdditionalWorkdirs :one
UPDATE projects
SET additional_workdirs = sqlc.arg(additional_workdirs)
WHERE id = sqlc.arg(id)
RETURNING *;
