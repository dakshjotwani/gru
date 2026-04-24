# tmux control mode (`tmux -CC` / `-C`) — research report for Gru's web terminal

**Date:** 2026-04-24
**Status:** research, not yet implemented
**Goal:** replace `tmux attach-session` (which streams the full tmux client UI over the WebSocket) with `tmux -C attach`, so the browser receives only the inner pane bytes — no status bar, no prefix-key handling, no copy-mode interception — while keeping tmux's session persistence and gaining real history-on-reconnect.

## Why we care

Today (`internal/server/terminal.go:71`) the terminal handler does:

```go
cmd := exec.Command("tmux", "attach-session", "-t", target)
ptmx, err := pty.Start(cmd)
// ... pipe ptmx ↔ websocket
```

The browser then renders that PTY stream via xterm.js. The WebSocket carries the full tmux client UI:

- Status bar (consumes a row, conflicts with our chrome).
- Prefix-key handling (`Ctrl-B` is intercepted by tmux before the inner program sees it).
- Copy-mode (mouse drags enter tmux's copy-mode; xterm.js's selection is hidden behind it).
- Tmux's internal scrollback (which conflicts with xterm.js's own scrollback).
- Status-line repaints and window-name escape sequences.

We use tmux purely for **pane persistence** — the inner Claude Code agent stays alive when nobody's attached. We do not need a tmux client UI.

Two simpler workarounds were considered and rejected:

- `tmux pipe-pane -O` — mirrors raw pane bytes but never replays the existing screen on (re)connect. You lose everything that happened while disconnected.
- `tmux attach -f no-status` — hides the status line but leaves the rest of tmux's input layer (prefix keys, mouse mode, copy mode) in front of the program.

Control mode is the full solution. iTerm2 uses it for its native tmux integration.

## 1. The control-mode protocol

### 1a. `-C` vs `-CC`

From `tmux.c:410-414`:

```c
case 'C':
    if (flags & CLIENT_CONTROL)
        flags |= CLIENT_CONTROLCONTROL;
    else
        flags |= CLIENT_CONTROL;
    break;
```

- **`-C`** (`CLIENT_CONTROL`) — plain control mode. Newline-terminated text protocol on stdin/stdout. The "tmux client UI" is replaced by this protocol; inner programs in panes still run normally.
- **`-CC`** (`CLIENT_CONTROL | CLIENT_CONTROLCONTROL`) — same protocol, plus two extras (`client.c:344-362`, `control.c:769-801`):
  1. The terminal is put into raw mode and echo is disabled (`cfmakeraw(&tio)`).
  2. The entire control-mode stream is wrapped in a DCS envelope: `\033P1000p` (ESC P 1000 p) when it starts, `\033\\` (ESC \\) when it exits.

The DCS envelope exists so iTerm2 can launch tmux from inside an existing terminal — the parent passes the DCS body through and iTerm2 watches for the magic prefix to switch into "tmux integration" mode mid-stream.

**For Gru, use `-C`, not `-CC`.** We launch tmux ourselves on a pipe (or PTY) we own; the bytes go straight to the WebSocket. The DCS wrapping is dead weight. `-CC` also requires a real tcgetattr-able fd; with `cmd.StdinPipe()` it would error with `tcgetattr failed: Inappropriate ioctl for device`.

Useful invocation forms:

- `tmux -C attach -t TARGET` — attach to existing session.
- `tmux -C new -A -s TARGET 'CMD'` — attach-or-create.

The flag goes on `tmux` itself, before the subcommand.

### 1b. Line-based framing

Everything is line-based. Two kinds of lines on the server-to-client stream:

1. **Command-response blocks**, framed by `%begin … %end` or `%begin … %error`.
2. **Notifications**, single lines starting with `%`.

Per the man page (4082-4098): "Each command will produce one block of output on standard output. An output block consists of a `%begin` line followed by the output (which may be empty). The output block ends with a `%end` or `%error`. **A notification will never occur inside an output block.**"

That last guarantee is what makes the protocol parseable with a small state machine.

`%begin` / `%end` / `%error` carry three space-separated arguments: `<unix-time> <command-number> <flags>`. Real captured stream of `tmux -C attach -t demo` followed by three pipelined commands then detach:

```
%begin 1777073087 286 0
%end 1777073087 286 0
%session-changed $0 demo
%begin 1777073087 291 1
demo: 1 windows (created Fri Apr 24 16:24:47 2026) (attached)
%end 1777073087 291 1
%begin 1777073087 292 1
0: zsh* (1 panes) [80x24] [layout b25d,80x24,0,0,0] @0 (active)
%end 1777073087 292 1
%begin 1777073087 293 1
%end 1777073087 293 1
%layout-change @0 b25d,80x24,0,0,0 b25d,80x24,0,0,0 *
%begin 1777073087 296 1
%end 1777073087 296 1
%exit
```

Command 286 fires before any user input — it's the implicit attach. Numbers 291/292/293 are the three pipelined commands, in order. The `flags` field is documented as "currently not used"; treat it as opaque.

### 1c. Notifications (the full alphabet)

These are the only `%`-lines tmux emits outside `%begin/%end` blocks.

| Notification | Format | Notes |
|---|---|---|
| `%output` | `%output %<pane-id> <data>` | Pane bytes. `<data>` is octal-escaped; see §1d. |
| `%extended-output` | `%extended-output %<pane-id> <age-ms> ... : <data>` | Replaces `%output` when `pause-after` is on. |
| `%begin` / `%end` / `%error` | `<ts> <cmd-num> <flags>` | Brackets a command response. |
| `%session-changed` | `$<sess-id> <name>` | This control client is now attached to a different session. |
| `%client-session-changed` | `<client-name> $<sess-id> <name>` | Some other client switched session. |
| `%session-renamed` | `$<sess-id> <name>` | |
| `%session-window-changed` | `$<sess-id> @<win-id>` | Active window in a session changed. |
| `%sessions-changed` | (no args) | Some session was created/destroyed. Re-run `list-sessions`. |
| `%window-add` | `@<win-id>` | Window linked into the current session. |
| `%window-close` | `@<win-id>` | |
| `%window-renamed` | `@<win-id> <name>` | |
| `%window-pane-changed` | `@<win-id> %<pane-id>` | Active pane in a window changed (tmux 2.5+). |
| `%unlinked-window-add` / `%unlinked-window-close` / `%unlinked-window-renamed` | `@<win-id> [name]` | Same as `%window-*` but the window isn't part of *this* control client's session. |
| `%layout-change` | `@<win-id> <layout> <visible-layout> <flags>` | E.g. `%layout-change @0 b25d,80x24,0,0,0 b25d,80x24,0,0,0 *`. |
| `%pane-mode-changed` | `%<pane-id>` | Pane entered/left copy mode. |
| `%pause` | `%<pane-id>` | Pane was paused (pause-after). |
| `%continue` | `%<pane-id>` | Pane resumed. |
| `%paste-buffer-changed` / `%paste-buffer-deleted` | `<buffer-name>` | |
| `%client-detached` | `<client-name>` | |
| `%subscription-changed` | `<name> $<sid> @<wid> <wi> %<pid> ... : <value>` | Format subscriptions; throttled to once/sec. |
| `%message` | `<message>` | From `display-message`. |
| `%config-error` | `<error>` | Config-file parse error. |
| `%exit` | `[reason]` | Last line tmux ever sends; the client should disconnect. |

ID conventions:

- Session ids: `$<n>` (e.g. `$0`).
- Window ids: `@<n>` (e.g. `@7`).
- Pane ids: `%<n>` (e.g. `%4`).

When a pane id appears in `%output`, the leading `%` is part of the id, so the line literally reads `%output %4 ...` — two `%`s with a space between.

### 1d. The `%output` byte encoding

From `control.c:631-642`:

```c
for (i = 0; i < size; i++) {
    if (new_data[i] < ' ' || new_data[i] == '\\') {
        evbuffer_add_printf(message, "\\%03o", new_data[i]);
    } else {
        ...append run of safe bytes...
    }
}
```

Rules:

- Bytes `< 0x20` (anything below space, including `\r`, `\n`, `\t`, `\033`) emit as a literal `\` followed by **exactly three octal digits**: `\033` for ESC, `\011` for TAB, `\012` for LF, `\015` for CR.
- A literal `\` (0x5C) emits as `\134`.
- Bytes `0x20..0x7E` go through as-is.
- **Bytes `0x80..0xFF` go through as raw single bytes.** UTF-8 multibyte sequences pass through unchanged. Confirmed empirically: feeding `café☃` (`63 61 66 c3 a9 e2 98 83`) into a pane produces `%output %0 café` followed by raw bytes `e2 98 83` on the wire, each verbatim.

Implications for the decoder:

- Read the line as raw bytes (do **not** UTF-8-decode it before unescaping — high bytes are part of the payload).
- Walk byte-by-byte: if `\\` followed by three octal digits, decode; else pass through.
- On a Go PTY, the line discipline can inject lone `\r` between `\n`s — strip them defensively (iTerm2 does this in `decodeEscapedOutput`).

**Important corollary:** the inner program's bytes can never be misinterpreted as a control-mode keyword. Every `%`, every newline, every `\` and every ESC in the pane output is octal-escaped. So the pane's raw bytes can contain literal "%output …" or DCS sequences — they all come out as `\045output …` or `\033…`. The "what if the inner program emits CC-looking bytes" risk does not exist for `%output`.

### 1e. Sending commands back

stdin is line-based: write a tmux command followed by `\n`. Multiple commands can be pipelined on one line with `;` (the captured trace above shows three commands and three `%begin/%end` blocks in order). The empty line is a special case — it triggers detach (`control.c:561-565`):

```c
if (*line == '\0') { /* empty line detach */
    free(line);
    c->flags |= CLIENT_EXIT;
    break;
}
```

The grammar is just the tmux command grammar — anything you'd type at the shell. So:

- **Send keys (literal text):** `send-keys -t %4 -l 'hello'` (note: `-l` for literal so `Enter` etc aren't interpreted as key names).
- **Send keys (named):** `send-keys -t %4 Enter`.
- **Send keys (hex bytes):** `send-keys -H -t %4 0x03` — most robust for arbitrary byte streams.
- **Resize:** `refresh-client -C 80x24` (whole client) or `refresh-client -C @<wid>:<W>x<H>` (per-window, tmux 3.4+).
- **Capture history:** `capture-pane -peJ -t %4 -S -1000` (stdout, escapes, joined, 1000 lines back).
- **Switch session:** `attach-session -t othersess` — protocol stays alive, you get `%session-changed`.
- **Detach:** `detach` or empty line.

Responses are matched to commands by **FIFO order**. iTerm2 maintains a `commandQueue_` (`TmuxGateway.m`); the command number in `%begin` is informational — the invariant is "responses come back in the same order commands were sent."

### 1f. Backpressure and pause mode

tmux's control writer keeps a per-pane queue (`control.c:54-75`, the `control_pane` struct). `CONTROL_BUFFER_HIGH = 8192` bytes is the high-watermark. If the client is too slow, tmux either pauses the pane or — if pause mode is off — kicks with `%exit too far behind` (`control.c:457-462`, `CONTROL_MAXIMUM_AGE = 300000` ms = 5 minutes).

Opt into pause mode with `refresh-client -fpause-after=N` (seconds). When a pane's queued data is older than N, tmux emits `%pause %<pid>` and stops sending output for that pane. Resume with `refresh-client -A %<pid>:continue`, then re-fetch state with `capture-pane`.

iTerm2 turns this on as soon as it detects tmux ≥ 3.2 (`TmuxController.m:1163-1184`). **Gru should always set this** — clients can stall (PWA suspend, network issues).

Other useful flags:

- `wait-exit` — tmux waits for the client to send EOF/empty line on stdin before fully disconnecting after `%exit`. Lets you flush late output.
- `no-output` — suppress `%output` entirely; useful for observer/manager clients.

## 2. Pane content lifecycle

### 2a. Initial paint — there is none for free

**tmux does not auto-replay a pane's screen on attach.** The `%session-changed` notification fires, `%layout-change` fires (because the client just affected the size), but no `%output` for existing screen content arrives. You only get `%output` for new writes after the attach. Confirmed by the captured trace: after attach, no `%output` emits until the inner program produces fresh bytes.

You **must** explicitly fetch the existing screen with `capture-pane`. Variants:

- `capture-pane -p -t %<pid>` — visible screen, plain text, no attributes.
- `capture-pane -p -e -t %<pid>` — visible screen with SGR escape sequences for color/attrs.
- `capture-pane -p -e -J -t %<pid>` — `-J` joins wrapped lines.
- `capture-pane -p -e -J -N -t %<pid>` — `-N` preserves trailing spaces (tmux 3.1+).
- `capture-pane -p -e -J -t %<pid> -S -1000` — include 1000 lines of scrollback (negative = lines back; `-S -` = entire history).
- `capture-pane -p -P -C -t %<pid>` — `-P` captures any partial/incomplete escape sequence sitting in tmux's input parser, `-C` octal-escapes non-printables. iTerm2 does this in addition to the normal capture so it can replay an interrupted ANSI sequence cleanly.

**iTerm2's reference invocation** (`TmuxWindowOpener.m:267-269`):

```
capture-pane -peqJN -t "%PANE" -S -<MAX_HISTORY>
```

Flags: `-q` quiets errors (e.g. dead pane), `-N` preserves trailing spaces, `-J` joins wraps, `-e` includes attributes, `-p` to stdout, `-S -N` from N lines back.

The data comes back inside a `%begin/%end` block as **lines separated by `\n`**, with embedded ANSI escape sequences for color. **This output is NOT octal-escaped** — `\xxx` octal escaping only applies to `%output` notifications and to `capture-pane -C`. The captured-pane bytes are terminal-native and can be written directly to xterm.js with `term.write(data)`.

### 2b. Incremental updates

After attach, every byte the inner program writes shows up as `%output %<pane-id> <octal-escaped-bytes>`. Per-pane: one `%output` per coalesced batch tmux's writer flushed; batch size is bounded by `CONTROL_BUFFER_HIGH`.

Pause mode uses `%extended-output %<pid> <age-ms> ... : <data>` instead — same payload encoding, plus an age field telling you how stale the batch was when sent.

The `%output` line ends with a single `\n`. Within the payload, `\012` is a real LF the program wrote, `\015` is a real CR — those are part of the program's terminal output and forward to xterm.js as-is.

### 2c. Scrollback

tmux keeps history per pane (default `history-limit = 2000`, configurable). Control mode does **not** stream history automatically; fetch on demand with `capture-pane -S -N` or `capture-pane -S -`. There is no incremental scrollback push — only the current visible-screen byte stream.

For Gru this is fine: xterm.js maintains its own scrollback. We only need to (a) fetch enough history once on (re)attach to backfill the user's view, and (b) trust xterm.js to retain everything that subsequently arrives via `%output`.

### 2d. Per-pane resize

Two flavors:

- **Whole-client size** (one size for everything): `refresh-client -C <W>x<H>` — also accepts `<W>,<H>`. From `cmd-refresh-client.c:117-129`. Sets `c->tty.sx/sy` and triggers a recalc. **You must call this once on attach** — without it (`resize.c:91-94`) the control client is treated as having "no size" and is excluded from window-size negotiation, so tmux can't pick sensible pane geometry.
- **Per-window size** (tmux 3.4+): `refresh-client -C @<wid>:<W>x<H>`. Each window in the session gets its own size; tmux composes them.

Per-pane resize doesn't go through `refresh-client`; pane sizes derive from window size and layout. To change *layout* use `resize-pane -t %<pid> -x N -y N` or `select-layout`.

## 3. Multiple windows and panes

A control client is attached to **one session at a time** but receives output and notifications for **every pane in every window of that session**. There is no implicit "subscribe to one pane only" — `%output` for pane `%4` arrives whether you currently care about it or not.

You **can** opt out per pane with `refresh-client -A %4:off`, which tells tmux to stop sending `%output` for that pane until you flip it back to `on`. Useful if Gru only renders one pane.

The client learns topology via three commands and four notifications:

- **Bootstrap:** `list-sessions -F "#{session_id} #{session_name}"`, `list-windows -F "<format>" -t $<sid>`, `list-panes -F "<format>" -t @<wid>`.
- **Live:** `%window-add @<wid>`, `%window-close @<wid>`, `%window-pane-changed @<wid> %<pid>`, `%layout-change @<wid> <layout-string> ...`.

The compact layout string (e.g. `b25d,80x24,0,0,0`) is documented in tmux's `layout.c`. iTerm2 parses it in `TmuxLayoutParser.m`. Format: `<checksum>,<WxH>,<X>,<Y>,<pane-id>` for a leaf, with comma-separated children inside `[]` (vertical splits) or `{}` (horizontal splits).

**For Gru, since each session is one window with one pane**, you can simplify drastically:

- After attach, `list-windows` returns one row, `list-panes` returns one row → cache that pane id.
- Ignore `%layout-change`, `%window-add`, `%window-close` (or treat any of them as "session is in trouble, kill the connection").
- Use `refresh-client -A %<pid>:on` only for the pane we care about — but with one-pane-per-session that's redundant.

## 4. iTerm2's implementation as reference

### 4a. Bootstrap on attach

`TmuxController.m:openWindowsInitial` (line 633) → `openWindowsOfSize:` (line 667). On attach iTerm2 sends, **as a single pipelined command list** (one line, `;`-separated):

1. `show -v -q -t $<sid> @iterm2_id` — get/set session GUID.
2. `refresh-client -C <W>,<H>` — set the client size.
3. `show -v -q -t $<sid> @iterm2_size`, `@hidden`, `@buried_indexes`, `@affinities`, `@per_window_settings`, `@per_tab_settings`, `@origins`, `@hotkeys`, `@tab_colors` — pull iTerm2's sticky window-management metadata (user-options stuffed into the tmux session for persistence).
4. `list-sessions -F "#{session_id} #{session_name}"`.
5. `list-windows -F "<long-format>"` — gets layout, name, flags.

For each window opened, `TmuxWindowOpener` then sends per-pane:

- `list-panes -t "%<pid>" -F "<state-format>"` — pane state.
- `capture-pane -peqJN -t "%<pid>" -S -<MAX_HISTORY>` — full history+screen with attributes.
- `capture-pane -p -P -C -t "%<pid>"` — partial/pending escape buffer.
- `show-options -v -q -p -t %<pid> @uservars` — user vars.

Importantly, **iTerm2 sets `acceptNotifications = NO` until after the bootstrap response arrives** (`TmuxGateway.m:89`, `TmuxController.m:627`). It still parses incoming notifications but ignores them. This avoids applying live `%output` before fetching the initial screen.

### 4b. Output framing parser

`TmuxGateway.m:executeToken:` (line 751). It's a switch on line prefix with three responsibilities:

- If a `%begin` is open and the line isn't `%end <id>` / `%error <id>`, append the line to `currentCommandResponse_` (the response body for the open command).
- If the line is `%end <id>` / `%error <id>` matching the head of the command queue, complete the command — invoke its callback with the accumulated response.
- If no `%begin` is open and the line is a `%`-notification, dispatch by prefix.

The `%output` parser (`parseOutputCommandData:`, line 263) walks bytes manually because the payload can contain bogus UTF-8 (it's an opaque byte stream). It null-terminates, finds the first space (after `%output`), then the `%` introducing the pane id, parses the integer pane id with `strtol`, and hands the rest to `decodeEscapedOutput` (the octal decoder).

### 4c. Resize round-trip

When iTerm2's user resizes the terminal window:

1. `set -t $<sid> @iterm2_size W,H` — persist.
2. `refresh-client -C W,H`.
3. `list-windows -F "<format>"` — re-fetch layout to discover the new pane sizes tmux computed.

Per-window resize (3.4+) uses `refresh-client -C @<wid>:WxH`. iTerm2 doesn't expect a useful response from `refresh-client`; it relies on the followup `list-windows` and the `%layout-change` notification to learn the result.

### 4d. Scrollback

iTerm2 calls `capture-pane -peqJN -t "%<pid>" -S -<MAX_HISTORY>` once per pane on attach. It feeds each line through a stripped-down VT100 parser (`TmuxHistoryParser.m`) to convert the SGR-laden text into iTerm2's internal `screen_char_t` cells, and prepends those to the pane's local scrollback. From then on, `%output` updates flow into the same VT100 emulator that handles non-tmux sessions.

For Gru with xterm.js this is even simpler: the captured text already contains SGR escapes, so we write the entire `capture-pane -peJ` output directly into the xterm instance with `term.write(data)`. xterm parses it the same way it would parse output from a normal PTY.

### 4e. Quirks iTerm2 documents/works around

- **tmux 1.8 command size cap**: send-keys batches must stay under ~1024 bytes; iTerm2 chunks at 1000 (`TmuxGateway.m:941-947`).
- **`refresh-client -C @W:HxV` requires tmux 3.4+** (`refreshClientSupportsWindowArgument` checks via `versionAtLeastDecimalNumberWithString:@"3.4"`).
- **tmux 1.8 unlink-window bug**: if `unlink-window` destroys the current session, `%end` is missing for the open command but `%exit` arrives — special-case in `executeToken:` line 774-781.
- **Status-line 2.9/2.91 bug**: heights came back off-by-one; iTerm2 adds 1 to the requested height for those versions.
- **`list-windows` `\t` literal vs tab byte**: tmux can emit literal `\t` in its format output instead of a real tab in some versions; `_shouldWorkAroundTabBug`.
- **Escape-sequence boundary on capture**: this is what `capture-pane -P -C` is for. Without `-P`, an escape sequence that's mid-stream when you call `capture-pane` is dropped; tmux's parser hasn't finalized it. iTerm2 fetches the partial sequence separately and feeds it into its parser before the live stream resumes.
- **`%output` bytes can contain invalid UTF-8** — never try to decode the raw response body as UTF-8 before octal-unescaping; parse the raw bytes.
- **Pause mode is a one-way street**: once paused, tmux discards the pane's queued output. To resume cleanly you must re-fetch screen state with `capture-pane`.
- **`%sessions-changed` carries no payload**; you have to re-`list-sessions` to find out what changed.

## 5. Lift to Go server + xterm.js client

### 5a. Server side (Go)

Replace today's `tmux attach-session` with `tmux -C attach -t TARGET`. The pipe's stdout becomes the control-mode stream, stdin is the command channel. Pseudocode shape:

```go
cmd := exec.Command("tmux", "-C", "attach", "-t", target)
stdin, _ := cmd.StdinPipe()
stdout, _ := cmd.StdoutPipe()
cmd.Start()
```

You **don't need a PTY here** — control mode doesn't care about ICANON/ECHO/etc. A plain pipe avoids line-discipline weirdness (no spurious `\r` injection). PTY would also work but adds noise.

Two goroutines:

**Reader (tmux stdout → state machine → websocket).** Use `bufio.Reader.ReadBytes('\n')` (NOT `bufio.Scanner` — its 64KB default is too small; a busy pane in pause-mode catch-up can produce megabyte lines). For each line:

1. If a `%begin` block is open, append the raw line to the in-flight response buffer until `%end <id> ...` or `%error <id> ...` arrives.
2. Else, dispatch on leading token:
   - `%output %<pid> <data>` — strip prefix, octal-decode `<data>`, send the resulting bytes to the websocket. (For Gru's one-pane-per-session model, ignore the pane id or assert it matches.)
   - `%extended-output ...` — same, with the latency field optionally surfaced.
   - `%begin` — open a new block.
   - `%window-close`, `%exit` — terminate, surface to the client as a close frame.
   - `%layout-change`, `%session-*`, `%window-*` — log/ignore.

The octal decoder is ~15 lines of Go:

```go
func decodeOutput(b []byte) []byte {
    out := make([]byte, 0, len(b))
    for i := 0; i < len(b); i++ {
        if b[i] != '\\' || i+3 >= len(b) {
            out = append(out, b[i])
            continue
        }
        d1, d2, d3 := b[i+1], b[i+2], b[i+3]
        if d1 < '0' || d1 > '7' || d2 < '0' || d2 > '7' || d3 < '0' || d3 > '7' {
            out = append(out, b[i])
            continue
        }
        out = append(out, (d1-'0')<<6|(d2-'0')<<3|(d3-'0'))
        i += 3
    }
    return out
}
```

**Writer (websocket → tmux stdin).** For each input frame from the browser, translate keystrokes into a tmux command. Two strategies:

- **Hex `send-keys -H`** (recommended): encodes every byte as a 2-char hex literal. Each byte becomes a separate "key"; tmux concatenates them. Form: `send-keys -H -t %<pid> 0x68 0x65 0x6c 0x6c 0x6f\n` for `hello`. Bulletproof for arbitrary bytes.
- **Literal `send-keys -l`**: `send-keys -l -t %<pid> -- "hello"\n`. Be careful escaping `"`, `\`, `$`, backticks. Hex-`-H` is simpler.

Either way, **batch into ~512-byte payloads** to stay well under tmux's command-size limit. Use one trailing `\n` per command.

**On attach** (server-initiated; do this immediately after `cmd.Start`):

```
refresh-client -fpause-after=10,wait-exit ; refresh-client -C 80x24 ; capture-pane -peJ -t %<pid> -S -2000
```

Wait for all three `%begin/%end` blocks. Send the captured-pane response as the initial websocket payload (this is the replay). Then start forwarding `%output`.

Handle `%pause %<pid>` by emitting a "stalled" status to the UI; on user activity send `refresh-client -A %<pid>:continue ; capture-pane -peJ -t %<pid> -S -200` and replay.

**On resize from the browser:** `refresh-client -C <W>x<H>\n`. (Or `refresh-client -C @<wid>:<W>x<H>` for per-window if on tmux ≥ 3.4.)

**On detach/disconnect:** send a single `\n` (empty line) to detach cleanly. **Don't kill the tmux session** — that's the whole point.

**On `%exit` from tmux:** mark the session errored upstream; emit a close frame to the websocket.

The pane id to target: get it once via `list-panes -t @<wid> -F "#{pane_id}"` during bootstrap, then cache it. Pane ids `%<n>` are stable for the life of the pane.

### 5b. Client side (xterm.js)

Almost no changes from today. The decoded `%output` bytes are already terminal-native (CR/LF/ESC sequences from the inner program), so `term.write(bytes)` works as before. The initial `capture-pane -peJ` output is also terminal-native (lines separated by `\n`, with SGR escapes embedded), so `term.write(initialSnapshot)` once before the live stream.

xterm.js keeps doing its own scrollback. No need to pull more history later; tmux's history is purely a recovery tool when a *different* control client reattaches.

Browser keystroke handler keeps doing what it does today — bytes go up the websocket; the server translates them to `send-keys`. Mouse events you can pass to tmux via `send-keys -M` if needed.

### 5c. Reconnect semantics

Because tmux owns the pane state, "reconnect" is "new `tmux -C attach -t TARGET` + new `capture-pane`". The browser tab paints a fresh full-screen snapshot from the new capture, then resumes the live stream — no protocol-level reconnection plumbing inside control mode itself. **This is the killer feature vs `pipe-pane`.**

## 6. Tradeoffs

### vs `pipe-pane -O`

- **Pros of pipe-pane**: trivial setup, raw bytes (no octal decoding), no command/response parsing, no protocol state machine.
- **Cons**: no initial screen — only bytes that arrive *after* you start piping. On reconnect you've missed everything that happened while gone unless you separately call `capture-pane`. No notifications (don't learn the pane died, the inner program changed window title, etc.). One pipe per pane.
- **Verdict**: pipe-pane is what you'd use for append-only logging. For an interactive viewer you'd reinvent half of control mode (capture-pane on attach, polling for liveness, separate command channel for input).

### vs `tmux attach -f no-status`

- **Pros**: minimal change from today; no decoder.
- **Cons**: still a tmux client UI in the way. Still get prefix-key handling (Ctrl-B intercepted), copy-mode entered on mouse drag, mouse-mode escape sequences interpreted by tmux, status-line repaints (rows reserved unless status disabled too), title/cursor escape sequences from tmux for window-name updates. The websocket carries tmux's *redraw* of the pane — every cell change becomes `\033[H\033[K...`-style cursor moves, much more bytes than the inner program emitted, doubling up with xterm.js's own state.
- **Verdict**: half-step. Control mode is the full step.

### vs raw PTY (no tmux at all)

- **Cons**: lose pane persistence — the inner program dies when nobody's attached. This is precisely what tmux is buying us, so off the table.

### Risks and failure modes for control mode

- **Inner program emits binary garbage**: completely safe. Every non-printable byte in `%output` is octal-escaped; nothing in pane content can be misinterpreted as a control-mode token.
- **Slow client**: tmux drops you with `%exit too far behind` after 5 minutes of buffer pressure unless you opt into pause mode with `refresh-client -fpause-after=N`. **Always set this.**
- **tmux server crash / inner pane dies**: get `%exit` (with optional reason) and the stream closes. Wrap in a graceful websocket close.
- **Concurrent attach**: multiple control clients can attach to the same session. Each gets its own `%output` stream. They can fight over size — the last `refresh-client -C` wins; two browsers at different viewport sizes will keep reflowing the pane. iTerm2 stores `@iterm2_size` as a session option to survive this; Gru could pin one canonical size or do the same.
- **`refresh-client -C` is required for size to take effect**: without it, control clients are flagged "no size" and excluded from window-size negotiation (`resize.c:91-94`). Forgetting this is the most common bug.
- **First command might race the prompt**: if you connect to `tmux -C attach` and immediately write commands, they arrive before `%session-changed` lands. Control mode handles this fine — commands are queued — but defer parsing topology until after `%session-changed`.
- **Version skew**: `pause-after`, `wait-exit`, `%pause`, `%continue`, `%subscription-changed`, `refresh-client -B`, `refresh-client -C @WID:WxH` are all tmux 3.x features (3.2 for pause, 3.4 for per-window sizing). Read `#{version}` via `display-message -p '#{version}'` or fall back gracefully.
- **`%output` decoder must tolerate truncated trailing escape**: tmux only ever emits well-formed `\xxx` triples, but if your line buffer splits a line in the middle (too-small buffer) you can read a partial sequence. Keep reading until LF; lines can be megabytes for a busy pane in pause-mode catch-up.

## 7. Recommended implementation sketch

### Server (Go), per session

1. On launch: `cmd := exec.Command("tmux", "-C", "attach", "-t", sessionTarget)`; `cmd.StdinPipe()` + `cmd.StdoutPipe()` (no PTY needed).
2. Cache the pane id via one-shot `tmux list-panes -t sessionTarget -F '#{pane_id}'` (out of band, doesn't need control mode).
3. Spin two goroutines:
   - **Reader**: `bufio.Reader`, `ReadBytes('\n')` loop; small state machine `{idle, inResponseBlock(cmdNum, buf)}`. On `%output` decode bytes and write to `paneSink chan []byte`. On `%begin/%end` accumulate and complete pending responses (FIFO matched against a `responseQueue`). On `%exit`, close everything.
   - **Writer**: serialize a `commandQueue chan command`, write `cmd + "\n"` to stdin, push the future onto `responseQueue`.
4. On websocket connect, send these as one pipelined line:
   ```
   refresh-client -fpause-after=10,wait-exit ; refresh-client -C <W>x<H> ; capture-pane -peJ -t %<pid> -S -2000
   ```
   Forward the `capture-pane` response body to the websocket as the initial paint. Then unblock the `paneSink → websocket` pump.
5. Translate websocket input frames to `send-keys -H -t %<pid> 0xXX 0xXX ...\n`, batched ≤512 bytes per command.
6. Translate websocket resize frames to `refresh-client -C <W>x<H>\n`.
7. On `%pause %<pid>` (rare with `pause-after=10`), emit a UI status; on user activity send `refresh-client -A %<pid>:continue ; capture-pane -peJ -t %<pid> -S -200` and replay.
8. On websocket close, send a single `\n` (empty line) to detach cleanly. **Don't kill the tmux session.**
9. On `%exit` from tmux, mark the session errored upstream.

### Client (xterm.js)

Unchanged from today, except:

- The first message after open is the initial paint (full screen + scrollback) — `term.reset(); term.write(initialBytes)`.
- Subsequent messages are `term.write(chunk)`.
- Resize → send dimensions to the server; the server handles the tmux-side refresh.
- xterm.js keeps its own scrollback exactly as before; no need to fetch more history at runtime.

### Code size estimate

The whole server-side parser/encoder is **~200-300 lines of Go**. The bytes flowing to the browser are byte-for-byte identical to what they'd be from a raw PTY — xterm.js can't tell the difference. We get tmux's persistence for free, and the prefix key, status line, copy mode, and mouse-mode interception all stay safely on the other side of the control protocol where we never see them.

## 8. Open questions for implementation

1. **Where does the parser live?** New file `internal/server/tmux_control.go`? Or a new package `internal/tmux/control/` since it's reusable beyond the terminal handler?
2. **Should we bootstrap pane id from gRPC `LaunchSession` instead of `list-panes` on attach?** The launcher already knows the tmux target; it could store the pane id in the DB at launch time and we'd skip the discovery step.
3. **Multiple browsers attached to the same session** — pin to one size, or per-tab independent resizes that fight? iTerm2's `@iterm2_size` approach is one option; another is "first attacher wins, late attachers get the existing size".
4. **Do we want to expose pause/resume to the UI?** When `%pause` arrives, surface a "stalled — reconnect to refresh" indicator? Or just silently re-attach on next user activity?
5. **Control mode for the journal session vs minion sessions** — same code path, or is the journal special? (Probably same — journal already runs in tmux just like minions.)
6. **Migration**: ship behind a feature flag (`server.yaml: terminal_mode: legacy | control`) for one release, then flip default and remove legacy?

## Sources

- tmux 3.6a man page (`man tmux`)
- tmux source: `control.c`, `control-notify.c`, `cmd-refresh-client.c`, `client.c`, `tmux.c`, `server-client.c`, `resize.c`, `cmd-attach-session.c`, `cmd-new-session.c` — github.com/tmux/tmux
- iTerm2 source: `TmuxGateway.m`, `TmuxController.m`, `TmuxWindowOpener.m`, `TmuxHistoryParser.m` — github.com/gnachman/iTerm2
- Live capture of the protocol against a local tmux server for verification of message formats and sequencing.
