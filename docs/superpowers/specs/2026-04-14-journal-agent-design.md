# Journal Agent Design

**Date:** 2026-04-14
**Status:** Design approved; ready for implementation planning.

## Summary

A machine-scoped, always-on Claude Code session — the **journal agent** — that captures the user's rough thoughts, proposes Gru agents to spawn, and builds up a durable profile of the user's preferences over time.

The journal agent runs as a Gru session with a dedicated `journal` profile. The server treats it as a reserved singleton: it is started automatically alongside the server, respawned on crash, pinned to the top of the web dashboard, and not killable via normal flows.

This is the v1 of a feature that could later expand into structured project discovery, attention-queue integration, and cross-machine sync. Those are explicitly out of scope here.

## Goals

- Zero-friction capture of rough thoughts ("super rough notes" is an explicit requirement).
- Agent reads those thoughts and proposes concrete Gru sessions to spawn in the right projects with the right prompts.
- Agent learns user preferences over time into a single profile doc that informs future proposals.
- The feature reuses existing Gru machinery (profiles, supervisor, session lifecycle) rather than introducing parallel infrastructure.

## Non-goals (v1)

- Web-based journal viewer or editor.
- Structured/SQL-backed journal schema.
- Attention-queue integration for proposals.
- Seeded brief files for spawned sessions.
- Journal-agent-driven follow-up on spawned sessions (the dashboard already shows this).
- Multi-machine / team journal.
- Scheduled journal summarization or cleanup jobs.
- RBAC or generalized session-role framework.

## User flow

1. **Capture.** User types `/jot <rough note>` in the journal session's chat, or edits the day's journal file (`~/.gru/journal/YYYY-MM-DD.md`) directly in their editor. `/jot` appends silently and returns only an ack — no agent reasoning, no conversational response.
2. **Review.** User runs `/suggest`. The agent reads all entries newer than its last-reviewed marker (stored in `~/.gru/journal/.state`), plus any prior context it needs.
3. **Propose.** Agent surfaces proposed spawns one at a time. Each proposal includes the target project, a one-line "why," and the concrete prompt the spawned session would run.
4. **Decide.** User responds "go", "skip", or edits the proposal. On "go", the agent runs `gru launch --project <id> --prompt "<prompt>"`, reports the new session's short-id, and updates `.state`. Then it surfaces the next proposal.
5. **Monitor.** The user tracks the spawned session via the normal Gru dashboard. The journal agent does not poll or follow up.

Ad-hoc chat still works — the user can ask the agent questions ("what did I say about the scraper last week?") without invoking a slash command. Normal messages get normal responses.

## Storage layout

All under `~/.gru/`, plain files, agent-owned:

```
~/.gru/
├── journal/
│   ├── 2026-04-14.md        # today's entries, append-only
│   ├── 2026-04-13.md
│   └── .state               # last-reviewed marker (file + byte-offset)
├── profile.md               # curated user profile doc
└── ...                      # (existing server.yaml, logs/, hooks/)
```

**Journal files.** One markdown file per day. `/jot` appends `\n## HH:MM\n<text>\n`. Direct editor writes are also supported and are picked up by `/suggest`. The agent may rewrite or restructure a day's file during `/suggest` to tidy messy notes, but must never delete content.

**`.state`.** Small file tracking the last file + byte offset the agent reviewed. Lets `/suggest` show only new entries without re-reading history.

**`profile.md`.** Single curated document the agent owns. Human-readable, human-editable. Sections like `## Roles & projects`, `## Preferences`, `## Do / don't auto-spawn`. Updated by the agent per the rules below.

**Session-start loading policy.** On fresh session start or after context compaction: `profile.md` + today's journal + last 3 days of journal. Older days are lazy-loaded when a thought references them.

## Profile update rules

- **Explicit signals → auto-write.** When the user directly states a preference or correction ("stop suggesting Python for this", "I always want tests before implementation"), the agent edits `profile.md` silently and mentions the edit once in chat.
- **Inferred signals → propose.** When the agent notices a pattern ("seems like you prefer small PRs"), it proposes the addition and only writes on confirmation.

This matches the pattern used by the Claude Code auto-memory system.

## Agent behavior (journal profile)

The `journal` profile defines working directory, permissions, tool access, and system prompt.

**Working directory:** `~/.gru/journal/`. `profile.md` is accessed via `../profile.md`.

**Filesystem access:**
- Read/write within `~/.gru/`.
- Read-only within configured workspace roots (default `~/workspace`, configured in `server.yaml`).
- No other filesystem access.

**Allowed tools:**
- Built-in: Read, Edit, Write, Glob, Grep.
- Bash: scoped to `gru` CLI invocations and basic filesystem inspection.
- Any MCP servers configured on the user's machine — the profile passes these through rather than hard-coding. This lets the agent reach Slack, calendars, etc. to enrich context around a journal entry when helpful.
- Network: whatever the allowed MCPs require; no other outbound network.

**System prompt teaches the agent to:**
1. On startup, read `profile.md`, today's journal, and the last 3 days.
2. On `/jot <text>`: append `\n## HH:MM\n<text>\n` to today's file. Respond with only an ack. Do not reason about or respond to the content.
3. On `/suggest`:
   - Read entries newer than the `.state` marker.
   - Group entries by intent.
   - Before proposing a spawn, run `gru status` and check whether a live session (`starting`/`running`/`idle`) in the candidate project already overlaps the intent. If so, either skip with a note ("looks like session `abc123` is already on this") or surface the proposal with an overlap flag.
   - Surface proposals **one at a time**. Each is `{project, one-line why, concrete prompt}`.
   - Wait for "go" / "skip" / edit before moving to the next.
   - On "go": run `gru launch --project <id> --prompt "<prompt>"`, report the new session's short-id, update `.state`.
4. Project resolution:
   - First try `gru` CLI (whatever enumerates known projects today).
   - If no match, search configured workspace roots by repo name / fuzzy match.
   - If still no match, propose registering the project via `gru` first and stop.
5. Profile updates: per the auto-write / propose rules above.
6. Use MCPs and CLI as needed to enrich context around an entry before proposing (e.g., check the relevant Slack channel mentioned in a note).

The system prompt lives in `internal/controller/profiles/journal.md` (separate file for easy iteration); the rest of the profile definition lives alongside the existing profile definitions.

## Singleton lifecycle

The journal session is a **server-managed singleton** identified by a new `role` column on the sessions table.

**Schema change.** Migration adds `role TEXT NOT NULL DEFAULT ''` to `sessions`. `role = 'journal'` identifies the singleton; empty string is the default for all other sessions. Scalar string (not an enum) so future roles are cheap to add.

**Startup.** On `gru server start`, after DB init and before the supervisor begins its normal reconcile loop:
1. Query for a session with `role = 'journal'` and `status IN ('starting','running','idle')`.
2. If none, spawn one via the existing `LaunchSession` path using the `journal` profile, with role set to `'journal'`.
3. If a stale journal session exists (`errored`/`killed`), spawn a replacement and leave the old row for history.

**Supervisor.** The existing 10-second reconciler extends its per-session handling:
- For a session with `role = 'journal'`, if its tmux window is gone, **respawn** it (instead of marking `errored`).
- Backoff between respawn attempts: 5s, 15s, 60s, then 60s steady. Protects against crash loops.
- Each respawn is logged under `~/.gru/logs/`.

**Config.** `~/.gru/server.yaml` gains:
```yaml
journal:
  enabled: true
  workspace_roots:
    - ~/workspace
```
`enabled: false` makes startup and the supervisor skip all journal logic.

**Shutdown.** `gru server stop` tears down the journal session like any other. `gru kill <journal-id>` is rejected server-side with `FailedPrecondition` and a message pointing the user to `journal.enabled=false`. The user CLI and web UI both hide the kill affordance for the journal session.

## API / schema changes

Minimal and additive.

**`proto/gru/v1/gru.proto`:**
- Add `string role = N;` to the `Session` message. Empty string = normal session; `"journal"` for the singleton.
- `LaunchSession` request gains an optional `role` field. Only the server itself sets `"journal"`; CLI and web calls cannot.
- `KillSession`: server-side guard returns `FailedPrecondition` if the target session has `role = "journal"`.

No new RPCs.

**`internal/store/queries/`:**
- Migration for the `role` column.
- New query `GetJournalSession` → `SELECT ... WHERE role = 'journal' AND status IN ('starting','running','idle') LIMIT 1`.

Regenerate via `make sqlc` and `make proto`.

## Web UI

Scoped changes to the session-list component only.

- **Sort.** The journal-role session sorts to the top, always, above normal status-based sort.
- **Row treatment.** Distinct styling: a subtle left accent, a badge (e.g. "📓 Journal") in place of the runtime label, a slightly tinted background. Exact visual polish during implementation.
- **Row click.** Opens the session detail / attach flow like any other session.
- **Kill affordance.** Hidden or disabled with a tooltip ("managed by server; disable via config"). Matches the server-side guard.
- **Empty state.** When `journal.enabled=false` and no journal session exists, a thin dismissible banner at the top of the session list: "Journal agent is disabled. Enable in `~/.gru/server.yaml`."

Touched files: the session-list component in `web/src/`, plus whatever generated types change from the proto update.

## Testing

Scope testing to what breaks if wrong.

**Go unit tests:**
- Store: `GetJournalSession` returns the live journal session and ignores `errored`/`killed` rows. Migration applies cleanly on an existing DB.
- Supervisor: given a dead journal session, reconciler respawns with backoff; given a live one, no-op; given `journal.enabled=false`, skips respawn logic entirely.
- Server startup: cold DB spawns one journal session; warm DB with a live journal session does not spawn another; warm DB with only a dead journal session respawns.
- `KillSession`: returns `FailedPrecondition` for a journal-role target.

**Integration (real SQLite, real tmux where feasible):**
- End-to-end: start server with `journal.enabled=true` in a temp home → journal session appears → kill its tmux window manually → supervisor respawns it.

**Frontend:**
- Session-list test: journal row renders with badge, sorts to top, kill affordance hidden.

**Manual verification (called out in the plan):**
- Real user loop: `/jot` → `/suggest` → proposal → "go" → session spawns in the right project.
- Duplicate-work detection when a session already covers an intent.
- Profile auto-write after an explicit correction; proposal flow for an inferred preference.

Not tested in code: agent judgment quality (that's prompt iteration), specific MCP integrations (user-configured).

## V1 scope checklist

Included:
1. `journal` profile (working dir, tools, MCP pass-through, permissions).
2. System prompt in `internal/controller/profiles/journal.md` teaching `/jot`, `/suggest`, project resolution, profile updates, duplicate-work check.
3. `~/.gru/journal/YYYY-MM-DD.md` append store + `.state` marker.
4. `~/.gru/profile.md` with auto-write / propose rules.
5. `role` column on sessions, server startup spawn, supervisor respawn with backoff, `KillSession` guard.
6. Filesystem read scope including workspace roots.
7. Web UI pin + badge + hidden kill for the journal session.
8. Config block in `~/.gru/server.yaml`.

Deferred (see Non-goals).

## Open risks

- **Prompt quality is the product.** The code here is a small envelope; the agent's usefulness depends entirely on the system prompt. Expect iteration after v1 ships.
- **MCP blast radius.** Passing through user-configured MCPs means the journal agent inherits whatever network access those MCPs have. That's intentional (user consent already granted when they configured the MCPs), but worth flagging.
- **`.state` drift.** If a user manually edits old journal files, `.state` byte-offsets become meaningless for those files. For v1, `/suggest` treats anything *newer* than the last-reviewed day as unreviewed; edits to already-reviewed days are invisible unless the user says so. Acceptable for v1.
- **Singleton reconciliation races.** If two servers somehow run against the same DB, both will try to own the journal. Gru is single-machine, single-server today, so this is not an active risk, but the `role` column makes resolving it later straightforward.
