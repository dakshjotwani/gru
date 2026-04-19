-- name: CreateDevice :one
INSERT INTO devices (id, label, push_endpoint, push_p256dh, push_auth, action_token_secret)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDevice :one
SELECT * FROM devices WHERE id = ? LIMIT 1;

-- name: ListDevices :many
SELECT * FROM devices WHERE stale = 0 ORDER BY created_at ASC;

-- name: UpdateDeviceSubscription :one
UPDATE devices
SET push_endpoint = sqlc.arg(push_endpoint),
    push_p256dh   = sqlc.arg(push_p256dh),
    push_auth     = sqlc.arg(push_auth),
    last_seen_at  = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: MarkDeviceStale :exec
UPDATE devices SET stale = 1 WHERE id = ?;

-- name: DeleteDevice :exec
DELETE FROM devices WHERE id = ?;

-- name: RecordAction :exec
INSERT INTO action_log (event_id, action, device_id)
VALUES (?, ?, ?);

-- name: GetAction :one
SELECT * FROM action_log WHERE event_id = ? AND action = ? LIMIT 1;
