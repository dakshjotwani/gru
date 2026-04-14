-- Add role column for reserved/singleton sessions.
-- Empty string = normal session; "journal" = the server-managed journal singleton.
ALTER TABLE sessions ADD COLUMN role TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_sessions_role ON sessions(role);
