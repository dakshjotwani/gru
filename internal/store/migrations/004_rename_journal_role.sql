-- Rename the singleton role: "journal" → "assistant". The session is still
-- the same one-per-machine Claude Code process with the same tools; the new
-- name is just a better fit for its evolved job (spawning & triaging minions,
-- not just journal capture). The on-disk prompt + ~/.gru/journal/ dir keep
-- their historic names — only the DB role field moves.
UPDATE sessions SET role = 'assistant' WHERE role = 'journal';
