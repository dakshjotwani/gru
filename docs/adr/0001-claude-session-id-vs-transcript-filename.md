# Claude session id ≠ transcript filename across `--resume`

**Decision.** The tailer treats Claude's Notification hook as the authoritative source for `transcript_path`, and self-heals onto the path it learns from `~/.gru/notify/<gru-sid>.jsonl`. The deterministic `<gru-sid>.jsonl` is only a launch-time guess.

**Context.** `gru launch` passes `--session-id <gru-sid>` to Claude, so the initial transcript is `~/.claude/projects/<hash>/<gru-sid>.jsonl`. But when the user runs `claude --resume <gru-sid>` inside the tmux pane, Claude mints a *fresh* uuid for the resumed conversation and writes to `<fresh-uuid>.jsonl`. The gru session row's id never changes; the on-disk transcript filename does.

**Why this approach.** Alternatives considered:

- *Persist a (gru-sid → claude-uuid) mapping table*: extra schema, extra ingestion path, still needs a hook to learn the new uuid. Pure overhead on top of what the Notification hook already tells us.
- *Glob the project dir for "most recently modified `.jsonl`"*: collides when multiple gru sessions share a cwd (the gru repo's own minions plus any debugging session in the same workdir). This was the rev-1 heuristic and produced cross-session pollution; rev 2 explicitly rejects it.
- *Ignore `--resume`*: would leave the tailer on a stale file forever after the first resume; UI would freeze.

The Notification hook fires within seconds of any active session and carries Claude's current `transcript_path`. Trusting it lets a single mechanism handle both fresh launches and resumes without server-side bookkeeping.

**Consequences.**

- Until the first Notification hook fires for a resumed session, the tailer may be reading a stale (or non-existent) transcript. Acceptable: idle resumed sessions converge on first hook; active ones converge faster.
- The notify file is shared across `--resume` boundaries — every Claude that runs in the gru session writes to the same `<gru-sid>.jsonl`. Cross-Claude pollution is prevented at the hook layer (`stdin.session_id` must match the cwd-local id) and in the tailer (`hook_event_name == "Notification"` guard on transcript-path swaps).
- `Manager.Start` prefers the deterministic `<gru-sid>.jsonl` exact match when it exists on disk, so non-resumed sessions restart cleanly without waiting for a hook. Resumed sessions (whose `<gru-sid>.jsonl` doesn't exist post-resume) keep whatever path was last persisted.
