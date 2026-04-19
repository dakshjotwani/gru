# Gru off-terminal design — operator mobile, tablet, and desktop polish

Date: 2026-04-18
Status: design
Scope: Spec A (operator off-terminal). Spec C (external chat / WhatsApp) sketched at end.

## Thesis

Gru's value is mission control for Claude Code fleets. Today that surface is
a desktop browser, and it leaks: on mobile there's no way to act on a session
(and no notifications arrive), and on desktop the in-browser terminal drops
keys (Ctrl+C, Tab, Esc), forcing the operator back to ssh+tmux. This spec
removes those leaks so Gru is the control surface from any device the
operator carries.

## Scope

**In scope (Spec A):**

- Desktop terminal key pass-through fix (Ctrl+C, Tab, Esc, arrows).
- PWA shell (manifest + service worker), installable on iOS + iPadOS.
- Web Push notifications to iPhone / iPad, with approve/deny action buttons
  for permission-prompt events (actionable from the lock screen).
- Chat-primary mobile view (iPhone default; iPad toggle).
- Terminal-primary tablet view (iPad default when keyboard attached).
- Device registration endpoints (label + push subscription, no auth token).
- Server tailnet-bind; bearer token interceptor removed entirely.

**Out of scope (addressed in Spec C or later):**

- External users (operator's mom, WhatsApp bot, public tenants).
- Public HTTPS endpoint; TLS cert management; abuse handling.
- Native iOS / Android apps.
- Inline free-text reply from the lock screen (iOS Web Push doesn't support
  text-input notification actions).

## Interlocks resolved up front

These three threads were brainstormed as one because they interlock:

| Thread | Resolution |
|---|---|
| Desktop key pass-through | Scoped as a sub-section of Spec A — small, self-contained. Fixes `attachCustomKeyEventHandler` in xterm.js and makes global `keydown` handlers in `App.tsx` yield to the focused terminal. |
| PWA vs native | **PWA.** iOS 16.4 (March 2023, stable ~3 years) shipped Web Push for home-screen-installed PWAs. Native ruled out. |
| External chat intake | **Deferred to Spec C.** External users bring tenancy, abuse, public endpoints, paid APIs — a different shape from operator-only tooling. |

## Architecture

```
  iPhone PWA ─┐
              │                                                    APNs (public)
  iPad PWA  ──┼──── tailnet ────→ Gru Server ──push──→ Apple ─────→ devices
              │                    │
  Desktop   ──┘                    │ (no auth; tailnet is the boundary)
                                   │
                        ┌──────────┴──────────┐
                        │                     │
                 Attention engine      Push dispatcher (new)
                 (existing)            /internal/push/
                        │                     │
                        └────→ SessionEvents stream (existing)
                                              │
                          Hook scripts POST /events (existing)
```

**New server-side components:**

- `internal/push/` — Web Push dispatcher. Signs with VAPID, delivers to
  Apple's push endpoint (APNs under the hood). Subscribes to the same
  `Publisher` that feeds `SubscribeEvents`; fires push when a session's
  hook event warrants operator attention and no dashboard is actively
  viewing that session.
- `internal/devices/` — Device registry, backed by SQLite. Stores
  `{id, label, endpoint, p256dh, auth, action_token_secret, created_at,
  last_seen_at}`. The `action_token_secret` is per-device random bytes
  used to HMAC-sign approve/deny tokens embedded in push payloads.
- HTTP handlers under `/devices`, `/actions/:token`, `/manifest.json`,
  `/sw.js`.

**Removed server-side components:**

- `internal/server/auth.go` (the `BearerAuth` interceptor) — deleted.
- `api_key` field in `~/.gru/server.yaml` — removed; `scripts/dev.sh`
  stops writing it; a warning is logged if it's still present.
- `VITE_GRU_API_KEY` env var and all call sites in `web/src/` — removed.

**Server bind change:**

Default bind moves from `0.0.0.0:<port>` to a Tailscale-detected set.
Config key `server.bind` accepts:

- `tailnet` (default) — loopback + detected Tailscale interface IP.
- `loopback` — `127.0.0.1` only.
- `all` — `0.0.0.0` (explicit opt-in, triggers a loud warning at
  startup: "no app-level auth; expose at your own risk").

Detection: shell out to `tailscale ip -4` at startup; if that fails, fall
back to `loopback` and log a warning.

## PWA shell

The existing React dashboard (Vite, `web/`) becomes the PWA — no separate
app, no rewrite. Changes are additive:

**Manifest (`web/public/manifest.json`):**

- `name`: "Gru"
- `short_name`: "Gru"
- `display`: `standalone`
- `scope`: `/`
- `start_url`: `/`
- `theme_color`: `#0d1117`
- `background_color`: `#0d1117`
- `icons`: 192x192 and 512x512 (generated from existing favicon + badge
  treatment; checked into `web/public/icons/`).

**Service Worker (`web/public/sw.js`):**

- `install`: pre-cache the app shell (index.html + built JS/CSS bundles)
  so the PWA loads offline to a "no tailnet" screen rather than a blank
  crash.
- `push`: parse payload, call `self.registration.showNotification(title,
  {body, actions, data, tag})`. The `tag` is the session id, so a newer
  notification for the same session replaces the older one.
- `notificationclick`: branch on `event.action`. If `approve` or `deny`,
  `fetch('/actions/' + data.actionToken + '?a=' + event.action, { method: 'POST' })` and show
  a brief toast via `registration.showNotification` on success/failure.
  Otherwise, `clients.openWindow('/sessions/' + data.sessionId)`.
- `pushsubscriptionchange`: fetch new subscription and `PUT /devices/:id`
  to update endpoint/keys.

**Responsive breakpoints (added to `App.module.css`):**

| Width | Form factor | Default view |
|---|---|---|
| ≤ 600px | iPhone portrait | Chat-primary; attention queue in slide-over drawer |
| 601–1024px | iPad portrait, small tablet, narrow window | Hybrid; toggle button in top bar |
| > 1024px | iPad landscape + keyboard, desktop | Terminal-primary |

View preference is per-session and persisted in `localStorage` so the
operator's choice survives reload.

**Install flow (iOS):**

1. Operator opens `http://gru.<tailnet>.ts.net:17777` in iOS Safari.
2. Gru detects `navigator.standalone === false` + iOS UA and shows an
   "Add to Home Screen" prompt (since iOS doesn't trigger `beforeinstallprompt`).
3. After install + launch, the PWA requests `Notification.requestPermission()`.
4. On grant, calls `registration.pushManager.subscribe({ applicationServerKey, userVisibleOnly: true })`.
5. `POST /devices` with `{ label, endpoint, keys: { p256dh, auth } }`.
   Label defaults to a generated `iPhone-<short-uuid>` which the operator
   can rename in a settings screen.
6. Device ID stored in `localStorage`; used for `PUT /devices/:id` on
   endpoint rotation.

## Chat view

A mobile-native renderer for a single session's event stream. Same
`SubscribeEvents` gRPC subscription as the terminal; different presentation.
Route: `/sessions/:id` with view mode (`chat` | `terminal`) in local state.

**Event → bubble mapping** (using canonical `adapter.EventType` values
from `internal/adapter/normalizer.go`):

| `GruEvent.Type` | Rendering |
|---|---|
| `user.prompt` *(new; see prerequisite below)* | Right-aligned bubble, user's message. |
| `session.idle` | Left-aligned bubble with the assistant's final message for the turn (extracted from the `Stop` hook payload). |
| `tool.pre` | Collapsed chip: "🔧 Bash: `npm test`" (icon varies by tool). Tap to expand full args. |
| `tool.post` | Result (truncated if > 20 lines) shown attached under the matching chip; "show more" expands. |
| `tool.error` | Same as `tool.post` but in red / warning style. |
| `notification.needs_attention` | Prominent yellow banner with Approve / Deny buttons *inline* (mirrors the push action) plus free-text input. |
| `notification` | Left-aligned system bubble, muted styling. |
| `session.start` / `session.end` / `session.crash` | Hairline divider with timestamp and the outcome. |
| `subagent.start` / `subagent.end` | Subtle nested divider, indented. |

**Prerequisite for the chat view:** Claude Code fires a `UserPromptSubmit`
hook on every user turn, but `internal/adapter/claude/normalizer.go`
doesn't map it today. The chat view needs to see user messages as
bubbles, so Spec A adds `EventUserPrompt EventType = "user.prompt"` and
the corresponding `UserPromptSubmit` case in `mapEventType`. This is a
~10-line change and is noted as a named prerequisite in the rollout
order below.

**Input bar:**

- iOS-standard multi-line text input, auto-grows up to 5 lines.
- Send button calls existing `SendInput` gRPC (which translates to
  `tmux send-keys`).
- Keyboard behavior:
  - iPhone: `Return` inserts newline; tap send button to submit.
  - iPad with keyboard: `Return` submits; `Shift+Return` inserts newline
    (matches iMessage/Claude/Slack conventions).
- A small "⏎ terminal" icon at the left of the input bar flips to
  terminal view for this session.

**Scroll behavior:**

- Auto-scroll to bottom on new event unless the user has scrolled up.
- "↓ jump to latest" floating button appears when scrolled up during a
  live stream; disappears when caught up.
- Pull-to-refresh re-fetches the latest snapshot (useful if tailnet
  dropped and reconnected).

**Session list (shared with desktop, mobile polish):**

- `AttentionQueue` renders compressed on narrow widths.
- Swipe-left on a session row exposes Kill / Delete / Mute (iOS-native
  trailing swipe actions via CSS scroll snap or a small library).
- Pull-to-refresh.

## Notifications

**Trigger policy (server-side `internal/push/`):**

Push fires when:

1. A `notification` hook event arrives (Claude is asking a permission
   prompt or similar). Always pushed — high-signal.
2. A `stop` event arrives AND `attention_score > push_threshold` (new
   config, default `0.7`) AND no active `SubscribeEvents` stream is
   watching this session ID.

Deduplication: push payload carries `tag: session_id`. A newer push with
the same tag replaces the older one in the OS notification tray. The
server rate-limits to at most one push per session per 30 seconds.

**Payload:**

```json
{
  "title": "<project_name> — <session_name>",
  "body":  "<80-char summary of the triggering event>",
  "tag":   "<session_id>",
  "actions": [
    {"action": "approve", "title": "Approve"},
    {"action": "deny",    "title": "Deny"}
  ],
  "data": {
    "sessionId": "<uuid>",
    "eventId":   "<uuid>",
    "actionToken": "<hmac-signed blob, 5-min TTL>"
  }
}
```

`actions` is populated only for permission-prompt events. For other
events it's omitted and the notification is navigation-only.

**Approve / deny action flow:**

1. Server mints `actionToken = base64url(HMAC-SHA256(device.action_token_secret, deviceId + eventId + expiresAt)) + "." + deviceId + "." + eventId + "." + expiresAt`.
2. Apple delivers the push; iOS shows the notification with Approve/Deny.
3. Operator taps Approve.
4. Service Worker `notificationclick` handler fires:
   `fetch('/actions/' + token + '?a=approve', { method: 'POST' })`.
5. Server parses token, looks up device, verifies HMAC, checks expiry,
   checks idempotency (by `(eventId, action)`), resolves the session's
   pending input (typically by `SendInput` with "1\n" for approve,
   "2\n" for deny — actual mapping depends on the prompt).
6. Server responds 200. SW shows a brief confirmation notification
   ("✓ approved" — auto-dismiss after 2s) via `registration.showNotification`.

**Failure modes:**

- Tailnet unavailable when the operator taps the action: `fetch` fails,
  SW shows a notification "Couldn't reach Gru — open app to act." User
  opens the PWA, the pending prompt is still there, resolves in-app.
- Token expired: server returns 410, SW says "Too late — open Gru."
- Idempotency collision (double-tap, or approve-then-deny race): server
  records the first action, returns 409 on the second, SW shows
  "Already resolved" and dismisses.
- Push delivery failure (device offline > few days, endpoint expired):
  server marks device `stale`, stops pushing until next `/devices`
  check-in.

## Terminal passthrough fix (Thread 1)

**Symptoms (operator journal 2026-04-18):** on the desktop dashboard,
Ctrl+C, Tab, and Esc don't reach the terminal, forcing a fallback to
ssh+tmux.

**Root causes (diagnosed):**

- `web/src/App.tsx` has a top-level `keydown` listener that captures
  `Escape` to deselect the current minion. It runs regardless of what
  element has focus, stealing Esc from the terminal.
- `Tab`: the browser moves focus to the next focusable element before
  xterm's key handler runs. Xterm's `attachCustomKeyEventHandler` isn't
  used, so xterm has no way to claim it.
- `Ctrl+C`: xterm handles this natively via its own selection logic. The
  intermittent failures reported are likely when a text selection is
  active (browser prefers copy). A custom handler that prefers
  terminal-interrupt over copy when the terminal is focused fixes it.

**Fix:**

1. In `web/src/components/TerminalPanel.tsx`, after `term.open(container)`,
   call `term.attachCustomKeyEventHandler((e) => { ... })`. The handler:
   - Returns `false` (i.e. xterm should handle it, ignore browser
     default) for `Ctrl+C`, `Ctrl+D`, `Ctrl+Z`, `Tab`, `Escape`,
     arrow keys, `Ctrl+\`.
   - Calls `e.preventDefault()` and `e.stopPropagation()` on those
     events so neither the browser nor the App-level handlers act.
   - Returns `true` (default-through) for everything else.
2. In `web/src/App.tsx`, gate the global `keydown` handler on
   `document.activeElement`: if the active element is inside a terminal
   container (detected via `closest('[data-terminal-focused]')`), return
   early.
3. Add `data-terminal-focused` attribute (or a similar dataset marker)
   on the terminal container element and a subtle border highlight in
   `TerminalPanel.module.css` when the terminal is focused, so the
   operator has a visual cue that keys are captured.
4. Smoke-test on macOS Chrome, macOS Safari, and iPad Safari (with
   Magic Keyboard) that Ctrl+C sends SIGINT, Tab produces a tab
   character, Esc reaches the tmux pane.

**Out of scope:** touch keyboard bar on desktop; custom keyboard
shortcuts; VIM-mode bindings. Those belong to future polish.

## Device registration & server changes

**New migration `006_devices.sql`:**

```sql
CREATE TABLE devices (
  id                    TEXT PRIMARY KEY,
  label                 TEXT NOT NULL,
  push_endpoint         TEXT NOT NULL,
  push_p256dh           TEXT NOT NULL,
  push_auth             TEXT NOT NULL,
  action_token_secret   TEXT NOT NULL,
  created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  stale                 INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX devices_stale ON devices(stale) WHERE stale = 0;

CREATE TABLE action_log (
  event_id   TEXT NOT NULL,
  action     TEXT NOT NULL,
  device_id  TEXT NOT NULL,
  resolved_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (event_id, action)
);
```

`action_log` gives idempotency: the server rejects a second attempt to
resolve the same `(event_id, action)`.

**HTTP endpoints (plain HTTP, not gRPC, since browsers and service
workers are the clients):**

- `POST /devices` — body `{ label, endpoint, p256dh, auth }`; returns
  `{ id }`. Per-device `action_token_secret` generated server-side.
- `GET /devices` — list, for the settings screen.
- `PUT /devices/:id` — update endpoint/keys on `pushsubscriptionchange`.
- `DELETE /devices/:id` — revoke.
- `POST /actions/:token?a=approve|deny` — verify token, check expiry,
  check idempotency, dispatch `SendInput`; returns 200, 409, or 410.
- `GET /manifest.json`, `GET /sw.js`, `GET /icons/*.png` — served
  statically by the existing web asset handler.

**Go dependency:** `github.com/SherClockHolmes/webpush-go` (or similar
maintained alternative). VAPID keys generated once at server startup if
absent, persisted in `~/.gru/server.yaml` as `push.vapid_private` and
`push.vapid_public`.

**Config additions (`~/.gru/server.yaml`):**

```yaml
server:
  bind: tailnet   # tailnet | loopback | all
push:
  vapid_private: <auto-generated>
  vapid_public:  <auto-generated>
  threshold:     0.7     # attention score above which stop-events push
  rate_limit_s:  30      # min seconds between pushes for same session
```

**Removals (breaking):**

- `api_key` field no longer read (warning logged if present).
- `VITE_GRU_API_KEY` no longer read.
- `internal/server/auth.go` deleted; middleware chain in `service.go`
  drops the `BearerAuth` wrapper.
- `scripts/dev.sh` stops generating an API key.

## Testing strategy

**Unit tests:**

- `internal/push/`: payload construction, VAPID signing (vs. known
  test vectors), threshold + rate-limit gating, dedup by tag.
- `internal/devices/`: CRUD, token mint + verify, HMAC correctness,
  expiry enforcement, `action_log` idempotency.
- `web/src/`: chat event→bubble mapping (unit snapshots of each event
  type); responsive breakpoint behavior via jsdom viewport mocks.

**Integration tests (Go):**

- Fake Web Push endpoint server in the test. Register device → fire
  synthetic `notification` event → assert push body delivered to fake
  endpoint → simulate action token fetch → assert `SendInput` called.
- Server bind: with `bind: tailnet` and no tailnet present, asserts
  graceful fallback to loopback + warning log.
- Removed auth: assert an anonymous gRPC call succeeds (regression
  against accidentally leaving auth middleware in place).

**Manual smoke tests (operator on own devices):**

1. Install PWA on iPhone from Safari (Add to Home Screen). Verify it
   launches standalone, not in a Safari chrome.
2. Grant notification permission. Register device. Check
   `~/.gru/gru.db devices` table.
3. Launch a Claude session on desktop. When it hits a permission
   prompt, verify push arrives on iPhone within ~5 seconds.
4. Tap Approve from lock screen. Verify session continues.
5. Fly through chat view on iPhone — scroll old events, send a
   free-text reply, confirm it reaches the tmux pane.
6. On iPad Magic Keyboard, confirm terminal view opens by default and
   Ctrl+C / Tab / Esc all reach the terminal.
7. On desktop Chrome, same keyboard test.
8. Turn off Tailscale on phone, trigger a push — notification still
   arrives (APNs is public), but Approve fails gracefully.

**Regression coverage:**

- Existing desktop dashboard flows unchanged: session list, attention
  queue, launch modal, kill, delete, prune.
- Worktree sessions render identically.

## Migration & rollout

- **Server**: removing `api_key` + bearer auth is breaking for any existing
  external callers (there shouldn't be any — the project is single-operator,
  pre-1.0). `dev.sh` auto-migrates local state by overwriting `server.yaml`
  without the key.
- **Frontend**: the Vite build drops `VITE_GRU_API_KEY`. Docs in
  `CLAUDE.md` updated to reflect tailnet-only deployment.
- **Hook scripts**: `hooks/gru-hook.sh` currently POSTs to `/events`
  without auth (check before merging) — should continue to work. If
  any hook script was passing a Bearer header, it becomes a no-op.

Order of landing (driven by the plan, but noted here so the spec
reviewer can sanity-check the dependency graph):

1. **Server auth removal + tailnet bind.** Smallest, highest-impact,
   can ship alone. Unblocks mobile Safari reaching the server.
2. **Desktop passthrough fix.** Independent of everything else; can
   ship in parallel.
3. **UserPromptSubmit normalizer case** (`EventUserPrompt`). Small
   prerequisite for the chat view.
4. **Devices registry + PWA shell (manifest + SW).** No push yet, but
   the app is installable and device registration works.
5. **Push dispatcher + action endpoints.** Wires notifications end to
   end, including approve/deny.
6. **Chat view + responsive breakpoints.** Last, because it's the most
   UI work and doesn't gate the other pieces.

## Open implementation questions

These should be resolved during plan-writing, not in this design:

- Do we use `tsnet` (a Go library) to enumerate the tailnet IP, or
  shell out to `tailscale ip -4`? Shelling out has zero new deps.
- Does `webpush-go` keep up with current VAPID semantics, or is a
  lesser-known fork preferable? Verify at plan time.
- What deep-link URL format for notifications — `/sessions/:id?view=chat`
  or a dedicated route? Probably the former so desktop uses the same
  URL with `view=terminal`.
- VAPID key rotation: probably "regenerate + invalidate all devices and
  force re-pair". Not a blocker for v1.
- Do we show an "Unregister from other devices" affordance in the
  settings screen? Nice, not blocking.

---

## Spec C (sketched only — future, separate document)

**"External chat intake: Ask Gru + WhatsApp for non-operators."**

Motivation from the operator's journal (2026-04-18): their mom doesn't
trust Shopify's metrics and wants automated agents to help her
e-commerce business. The stretch goal is having her text a project
description to a WhatsApp number and have Gru spawn an appropriate fleet
with minimal operator intervention, similar to openclaw.

Why this is a separate spec:

- **External users** — introduces multi-tenancy, per-user project
  namespacing, rate limits, cost caps, abuse handling. None of these
  exist in today's single-operator model.
- **Public endpoints** — Spec A is tailnet-bound. Spec C requires a
  public HTTPS endpoint for WhatsApp Business API webhooks and possibly
  for a web onboarding flow for tenants. That brings TLS, DDoS
  concerns, a real domain, and reinstating app-level auth.
- **Paid third-party APIs** — WhatsApp Business (Meta), which is a
  qualified vendor onboarding process on its own.
- **Trust model** — operator approves tenants out-of-band; tenants
  can't spawn sessions that touch the operator's machines; probably
  requires a new sandboxed Environment adapter variant.
- **Scope size** — rough order-of-magnitude, Spec A is 1–2 weeks of
  work. Spec C is 4–8 weeks.

Rough shape (not design):

- Entry points: operator-facing "Ask Gru" natural-language project
  creation (could share code with Spec A chat view), and an external
  WhatsApp webhook handler.
- Tenancy: new table `tenants`, each `project` belongs to a tenant,
  every RPC scoped.
- Sandboxing: leverages the `env` abstraction (micro-VMs / containers
  / cloud sandboxes — see the "sandboxing beyond worktrees" note).
- Cost control: per-tenant token budget, per-tenant rate limit.
- Spec to be drafted separately when Spec A is landing.
