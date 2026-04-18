-- Env-centric project model. Projects become references to env spec files on
-- disk under ~/.gru/projects/<name>/spec.yaml. The spec owns workdirs,
-- adapter config, and everything else about the environment. Gru only keeps
-- the pointer.
--
-- Breaking change: existing projects rows were keyed by a UUID and carried a
-- repo path + additional_workdirs column. Neither field makes sense in the
-- new model, and there's no clean way to derive a spec.yaml from a bare
-- filesystem path after the fact. This migration wipes all sessions, events,
-- and projects and rebuilds the schema. Operators are expected to stand up
-- their projects under ~/.gru/projects/ post-migration — see
-- docs/workflows/gru-on-gru-minions.md and scripts/install-minion-projects.sh.

-- Wipe (order matters: events → sessions → projects for the FKs).
DELETE FROM events;
DELETE FROM sessions;
DELETE FROM projects;

-- SQLite supports DROP COLUMN only from 3.35. Use the table-recreate pattern
-- so this works on older installs too.
DROP TABLE projects;

CREATE TABLE projects (
    id         TEXT PRIMARY KEY,                        -- absolute path to spec.yaml
    name       TEXT NOT NULL,                           -- display label (usually the directory basename)
    adapter    TEXT NOT NULL DEFAULT '',                -- cached from the spec for quick listing
    runtime    TEXT NOT NULL DEFAULT 'claude-code',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

INSERT OR IGNORE INTO schema_migrations (version) VALUES (5);
