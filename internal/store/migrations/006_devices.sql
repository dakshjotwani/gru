-- Device registry + action log for Web Push notifications.
--
-- Each row in `devices` is a PWA install (iPhone, iPad, desktop browser)
-- that has registered to receive Web Push. Per-device
-- action_token_secret is used to HMAC-sign approve/deny action tokens
-- embedded in push payloads — so a token captured off one device can't
-- be replayed against another.
--
-- `action_log` is the idempotency table for /actions/<token>: the
-- PRIMARY KEY on (event_id, action) means the second tap on an
-- approve/deny notification is a no-op instead of a double-send.
--
-- Trust model: the server is bound to the tailnet interface, so these
-- endpoints are operator-only. No auth token is required to register
-- a device or invoke an action.

CREATE TABLE devices (
    id                    TEXT PRIMARY KEY,                      -- uuid
    label                 TEXT NOT NULL,                         -- 'daksh-iphone'
    push_endpoint         TEXT NOT NULL,
    push_p256dh           TEXT NOT NULL,
    push_auth             TEXT NOT NULL,
    action_token_secret   TEXT NOT NULL,                         -- hex-encoded 32 bytes
    created_at            TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    last_seen_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    stale                 INTEGER NOT NULL DEFAULT 0             -- 1 if push delivery failed permanently
);

CREATE INDEX devices_active ON devices(stale) WHERE stale = 0;

CREATE TABLE action_log (
    event_id    TEXT NOT NULL,
    action      TEXT NOT NULL,                                    -- 'approve' | 'deny'
    device_id   TEXT NOT NULL,
    resolved_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (event_id, action)
);
