---
name: using-gru
description: Use when you have produced a deliverable the operator should review (PDF, Markdown design doc, rendered spec, code review writeup) or want to attach an external URL to your current session (GitHub PR, Slack thread, Figma file). Surfaces the deliverable as a dashboard tab or link chip so the operator sees it without scrolling terminal scrollback. No-ops cleanly if not running inside a Gru session.
---

# Using Gru — agent-side capabilities

You may be a minion running inside a Gru-managed session, in which case the operator is watching your work through Gru's dashboard rather than your terminal directly. This skill catalogs the things you can do from inside a session to push artifacts back out where they can see them.

## First: are you in Gru?

Walk up from your current directory looking for `.gru/session-id`:

```bash
dir="$PWD"
while [ "$dir" != "/" ]; do
  if [ -f "$dir/.gru/session-id" ]; then
    echo "in gru: session $(cat "$dir/.gru/session-id")"
    break
  fi
  dir="$(dirname "$dir")"
done
```

If the file isn't found, you're not running inside a Gru session — every `gru artifact …` / `gru link …` call below will be a no-op or error. Don't surface anything; the operator isn't watching this terminal through a dashboard.

If the file is present: that's the only signal you need. The CLI helpers below read it themselves, so you don't pass the session ID explicitly.

## Capabilities

### Surface a deliverable (artifact)

Use when you have produced something self-contained that the operator should review: a rendered design doc, a code-review writeup, a generated spec, a resume PDF, anything you'd otherwise paste a link to. The artifact becomes a tab in the operator's session view; PDFs render with the browser's PDF viewer, Markdown renders inline with proper formatting.

```bash
gru artifact add --title "Design Review" --file ./review.md
gru artifact add --title "Q3 Roadmap"    --file ./roadmap.pdf
```

MVP MIME allowlist: `application/pdf`, `text/markdown`. The MIME is inferred from the file extension; anything outside the allowlist gets rejected with a clear error.

**When to use:** when the work is done (or done enough to look at). A finished deliverable, a checkpoint summary the operator should sanity-check, the rendered output of a long-running job.

**When *not* to use:**
- Intermediate scratch work, in-progress notes, or "thinking out loud" — those belong in the terminal scrollback or the journal.
- Bytes the operator can already see another way (a file already pushed to a remote branch they can `git diff`).
- Frequent updates of the same content. Artifacts are immutable; uploading a new one creates a new tab. Wait until you have something stable.

### Attach an external link

Use when this session lives somewhere else too — a GitHub PR you opened, a Slack thread coordinating the work, a Figma file you're producing comps in. The link shows up as a compact chip above the active tab so the operator can one-click jump to it.

```bash
gru link add --title "GitHub PR #428"       --url https://github.com/org/repo/pull/428
gru link add --title "Slack: #foo-incident" --url https://acme.slack.com/archives/C0/p1234567890
```

URL scheme allowlist: `https`, `http`, `mailto`. The dashboard derives the icon from the hostname, so don't bother sending one.

**When to use:** the moment you create the off-Gru artifact (the moment `gh pr create` returns a URL, the moment you start a Slack thread, etc). Don't wait. A link that exists in your terminal scrollback but not in the session is a link the operator has to dig for.

## Caps and errors

- Per artifact: 25 MB (PDF) / 5 MB (MD). Per session: 50 artifacts, 100 MB total. Per session: 20 links.
- A 409 means you've hit a per-session cap; the response includes current count and bytes used. Free space yourself with `gru artifact delete <id>` (use `gru artifact list` to see what's there) — Gru does not auto-evict.
- A 410 means the session is in a terminal state (the operator killed it, or it crashed). Stop trying to upload.

## Adding a new capability

The shape is uniform: one `gru <verb>` subcommand, one section in this file describing when to use it. The cwd-rooted `.gru/session-id` stays the only "you're in Gru" signal — don't introduce per-capability discovery files, don't add env vars, don't require system-prompt injection. Anything addressable as "minion produces X, operator wants to see X" should fit this pattern.
