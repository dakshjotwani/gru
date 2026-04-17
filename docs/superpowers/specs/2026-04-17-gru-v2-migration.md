# Gru v2 — v1 → v2 migration notes

**Date:** 2026-04-17
**Audience:** The operator on the day v2 ships. Not for new users — they start on v2.
**Companion spec:** [`2026-04-17-gru-v2-design.md`](./2026-04-17-gru-v2-design.md)

## What actually breaks

Nothing, if you do nothing. v2 is additive. Existing sessions keep running; existing projects keep launching.

- `Project.path` (string) is retained and read. The new `Project.workdirs` field (repeated string) is populated by the server the first time a v1 project is touched, from `path`. Writing to `path` still works.
- `Session.profile` is retained. New `Session.environment_instance_id` is added alongside; reads that don't know about it still work.
- `Session.tmux_session` / `tmux_window` are still populated by the `host` adapter identically to v1. The WebSocket terminal handler accepts both `session:window` and bare `session` targets.
- Existing `attention_score` semantics preserved; v2 just starts actually writing it based on hook events (v1 left it at zero).
- The event stream gets one new `type` value: `"env.lifecycle"`. Old clients ignore unknown types already.

## What you get for free after upgrading

- Hook-driven `attention_score`: paused +1.0, notification +0.8, tool error +0.5, up to +0.3 for 5–15 min staleness. Rankable via the existing `Session` message. Tunable in `~/.gru/server.yaml` under `attention.weights.*`.
- `--add-dir` support on launch: add secondary workdirs to `LaunchOptions.AddDirs` (from the CLI, the web dashboard, or whatever tool is launching sessions) and Claude Code sees them. No schema change required for this — it's just threaded through `LaunchOptions`.
- `gru env list / show / test` CLI: inspect adapters, print config schemas, run the 9-case conformance suite against a user-supplied `spec.yaml`. Doesn't touch the server.
- `host` and `command` adapters: both available. The `host` adapter is still implicit for v1-style launches; the `command` adapter is how you attach new sessions to bespoke infrastructure.

## If you want to start using the command adapter

Order of operations:

1. **Invoke the `gru:scaffold-env` skill** in a Claude Code session running in the project:
   ```
   (from a Claude session) "Use the gru:scaffold-env skill to set this project up."
   ```
   The skill audits whatever bootstrap exists, produces a cost-report, and optionally generates a `command`-adapter wrapper.

2. **Smoke-test the generated spec:**
   ```bash
   gru env test ./gru-env.yaml
   ```
   All 9 conformance cases should pass. If they don't, the skill's report tells you where to look.

3. **Launch a session against it.** v2's launch path can target any registered adapter; `host` stays the default unless the project spec says otherwise. How you wire the per-project adapter selection depends on how you want to configure it — see the spec's §Project section.

## Migration gotchas

- **tmux session naming.** v1 used one tmux session per project (`gru-<projectName>`) with multiple windows. The `host` adapter preserves this exactly. The newer `PersistentPty` layer (used by `command` adapter) creates one tmux session per Gru session instead — so `tmux ls` on a heavily-loaded v2 machine will list many short-lived `gru-<shortID>` sessions alongside the long-lived per-project ones. Expected.
- **Events that arrived before the attention engine started.** On server restart, the engine has empty state. Existing running sessions resume with `attention_score=0` and rebuild from subsequent hooks. Staleness ramp picks up from the next hook event's timestamp, not from when the session actually started. This is fine in practice — the operator rarely cares about pre-restart attention context.
- **Budget counters.** Declared in the spec (`budget_dollars_used`, `budget_tokens_used`) but not yet populated. Proto additions land only when the ingestion side is ready to write them. You'll see them show up as 0 everywhere for now.
- **`environment_specs` SQLite table.** Not yet added. For v2's first cut, `gru env test` reads spec files from disk. A project's environment reference is a path, not a foreign-key name. When the table lands, the `Project.environment_spec_name` proto field (currently unused) will point at it.

## How to tell if something broke

Post-upgrade checks the operator should run once:

1. **Existing sessions keep running.**
   ```bash
   gru status
   ```
   All v1 sessions should appear with their old status. Nothing should flip to `errored` because of the upgrade.

2. **New launches still work.**
   ```bash
   gru launch --profile feat-dev "test prompt"
   ```
   A new session appears, reaches `running`, and hooks flow.

3. **Attention scores start moving.** Let an existing session go idle; after the Stop hook fires, its `attention_score` should be ≥ 1.0. In `gru status` or the web UI, it should bubble up when sorted by score.

4. **Terminal attach works.** Pull up the web terminal for an existing session. It should attach without "no tmux target" errors.

If any of the above fails, the offending change is one of:
- host adapter refactor (tmux naming drift) → check `internal/env/host`
- attention wiring (score stuck at 0) → check `internal/ingestion/handler.go`'s call to `attention.OnEvent`
- terminal handler regression → check `internal/server/terminal.go` doesn't require TmuxWindow

## Rollback

`git revert` the v2 merge. SQLite schema is strictly additive, so the v1 binary reads the v2 database without issue. No data migration is needed to go back.
