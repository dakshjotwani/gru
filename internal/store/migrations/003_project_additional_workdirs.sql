-- Per-project default additional workdirs for multi-repo sessions.
-- Stored as a JSON array ('[]' when empty) so order is preserved. Each launch
-- can still override/append via LaunchSessionRequest.add_dirs.
ALTER TABLE projects ADD COLUMN additional_workdirs TEXT NOT NULL DEFAULT '[]';
