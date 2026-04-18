#!/usr/bin/env bash
# dev.sh — start gru server + web dashboard with prefixed, tee'd logs.
#
# The default path (no env vars set) matches v1 behavior:
#   state dir: ~/.gru
#   server port: 7777
#   web port:    3000
#
# Env overrides (used by the gru-on-gru minion flow — see
# docs/workflows/gru-on-gru-minions.md):
#
#   GRU_STATE_DIR      override the state dir (server.yaml, logs, default db)
#   GRU_SERVER_PORT    server port; 0 = ephemeral, bound port published to
#                      $GRU_STATE_DIR/server.port via `gru server --port-file`
#   GRU_WEB_PORT       vite port; 0 = ephemeral, URL captured from vite stdout
#   GRU_SKIP_SERVER=1  skip `gru server` entirely (frontend-only mode); the
#                      dashboard will talk to whatever VITE_GRU_SERVER_URL
#                      already points at
#   VITE_GRU_SERVER_URL   pre-set to point the web at an existing backend;
#                         never overwritten when already present
#
# When ports are ephemeral the resolved URLs are also written to
# $GRU_STATE_DIR/urls.json so the minion agent (or a human) can `cat` them.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${GRU_STATE_DIR:-${HOME}/.gru}"
LOG_DIR="${STATE_DIR}/logs"
SERVER_PORT="${GRU_SERVER_PORT:-7777}"
WEB_PORT="${GRU_WEB_PORT:-3000}"
SKIP_SERVER="${GRU_SKIP_SERVER:-}"
mkdir -p "$LOG_DIR"

SERVER_PIPE="/tmp/gru-dev-server-$$"
WEB_PIPE="/tmp/gru-dev-web-$$"
PORT_FILE="${STATE_DIR}/server.port"
URLS_FILE="${STATE_DIR}/urls.json"

SERVER_PID=""
WEB_PID=""
SERVER_LOG_PID=""
WEB_LOG_PID=""

# prefix_log <label> <color_code> <logfile> <pipe>
# Reads from a named pipe, prepends colored timestamp+label to each line,
# and tees raw (no color) to the log file.
prefix_log() {
  local label="$1" color="$2" logfile="$3" pipe="$4"
  local reset="\033[0m"
  while IFS= read -r line; do
    local ts
    ts="$(date '+%H:%M:%S')"
    printf "${color}[%s %s]${reset} %s\n" "$ts" "$label" "$line"
    printf "[%s %s] %s\n" "$ts" "$label" "$line" >> "$logfile"
  done < "$pipe"
}

cleanup() {
  echo ""
  echo "shutting down..."
  [[ -n "$SERVER_PID"     ]] && kill "$SERVER_PID"     2>/dev/null || true
  [[ -n "$WEB_PID"        ]] && kill "$WEB_PID"        2>/dev/null || true
  # closing the pipes unblocks the log readers
  [[ -n "$SERVER_LOG_PID" ]] && kill "$SERVER_LOG_PID" 2>/dev/null || true
  [[ -n "$WEB_LOG_PID"    ]] && kill "$WEB_LOG_PID"    2>/dev/null || true
  wait 2>/dev/null || true
  rm -f "$SERVER_PIPE" "$WEB_PIPE"
  echo "logs saved to $LOG_DIR"
}
trap cleanup EXIT INT TERM

mkfifo "$SERVER_PIPE" "$WEB_PIPE"

# Rotate logs each dev session so they don't grow unbounded.
: > "$LOG_DIR/server.log"
: > "$LOG_DIR/web.log"

# Ensure the state dir has a server.yaml with a stable API key.
# The key is created once and reused across restarts so the web dashboard
# and hook scripts always talk to the same server with the same credentials.
# When GRU_STATE_DIR is a non-default minion dir, this file is the first one
# written there — matches the minion-env.sh contract.
GRU_CONFIG_FILE="${STATE_DIR}/server.yaml"
mkdir -p "$(dirname "$GRU_CONFIG_FILE")"
if [[ ! -f "$GRU_CONFIG_FILE" ]] || ! grep -q "^api_key:" "$GRU_CONFIG_FILE"; then
  GENERATED_KEY="$(openssl rand -hex 16 2>/dev/null || \
    od -vN 16 -A n -t x1 /dev/urandom | tr -d ' \n')"
  cat > "$GRU_CONFIG_FILE" <<YAML
addr: :${SERVER_PORT}
api_key: ${GENERATED_KEY}
db_path: ${STATE_DIR}/gru.db
YAML
  echo "created $GRU_CONFIG_FILE with new API key"
else
  # Existing file: make sure the addr matches the requested port. This
  # lets a minion re-run with a different GRU_SERVER_PORT without nuking
  # state, and lets the human flow add :0 support by editing server.yaml.
  if grep -q "^addr:" "$GRU_CONFIG_FILE"; then
    # macOS sed needs an explicit '' after -i; GNU sed doesn't. Use a
    # portable pattern: write to a tmp file and mv.
    awk -v port="${SERVER_PORT}" \
      '/^addr:/ {print "addr: :" port; next} {print}' \
      "$GRU_CONFIG_FILE" > "$GRU_CONFIG_FILE.tmp"
    mv "$GRU_CONFIG_FILE.tmp" "$GRU_CONFIG_FILE"
  else
    echo "addr: :${SERVER_PORT}" >> "$GRU_CONFIG_FILE"
  fi
fi
GRU_API_KEY="$(grep '^api_key:' "$GRU_CONFIG_FILE" | awk '{print $2}' | tr -d '"'\''[:space:]')"
export VITE_GRU_API_KEY="${GRU_API_KEY}"

# Detect the Tailscale IP so the frontend (running in a remote browser) can
# reach the gRPC server directly. Falls back to localhost if tailscale isn't
# running or isn't on PATH.
TAILSCALE_IP="$(tailscale ip -4 2>/dev/null | head -1 || true)"

# Ensure nvm-managed node/npm is on PATH if not already present.
NVM_BIN="${HOME}/.nvm/alias/default"
if [[ -f "$NVM_BIN" ]]; then
  NVM_DEFAULT_VERSION="$(cat "$NVM_BIN")"
  NVM_DEFAULT_BIN="${HOME}/.nvm/versions/node/${NVM_DEFAULT_VERSION}/bin"
  # Resolve symlink (e.g. "22" -> "v22.22.2")
  if [[ ! -d "$NVM_DEFAULT_BIN" ]]; then
    NVM_DEFAULT_VERSION="$(ls "${HOME}/.nvm/versions/node/" | grep "^v${NVM_DEFAULT_VERSION#v}" | tail -1)"
    NVM_DEFAULT_BIN="${HOME}/.nvm/versions/node/${NVM_DEFAULT_VERSION}/bin"
  fi
  if [[ -d "$NVM_DEFAULT_BIN" && ":${PATH}:" != *":${NVM_DEFAULT_BIN}:"* ]]; then
    export PATH="${NVM_DEFAULT_BIN}:${PATH}"
  fi
fi

cd "$ROOT"
GO="${GO:-$(command -v go 2>/dev/null)}"

RESOLVED_SERVER_URL=""

if [[ -z "$SKIP_SERVER" ]]; then
  echo "building gru..."
  "$GO" build -o /tmp/gru-dev ./cmd/gru

  echo "starting gru server..."
  # Remove any stale port file so our poll-for-existence loop below only
  # succeeds on the fresh bind.
  rm -f "$PORT_FILE"
  # Strip TMUX/TMUX_PANE so the server's tmux subcommands (for agent sessions)
  # don't inherit a $TMUX from the shell that ran this script. Gru's agent
  # sessions are siblings, not nested — without this, tmux warns about nesting
  # when make dev is launched from inside a tmux pane.
  env -u TMUX -u TMUX_PANE /tmp/gru-dev server --port-file "$PORT_FILE" > "$SERVER_PIPE" 2>&1 &
  SERVER_PID=$!
  prefix_log "server" "\033[0;34m" "$LOG_DIR/server.log" "$SERVER_PIPE" &
  SERVER_LOG_PID=$!

  # Poll for the port file (max 10s). The server writes it immediately after
  # net.Listen succeeds, so normally this resolves in <500ms.
  for i in $(seq 1 100); do
    if [[ -f "$PORT_FILE" ]]; then
      break
    fi
    sleep 0.1
  done
  if [[ ! -f "$PORT_FILE" ]]; then
    echo "ERROR: gru server did not produce a port file at $PORT_FILE within 10s"
    exit 1
  fi
  BOUND_ADDR="$(cat "$PORT_FILE")"
  BOUND_PORT="${BOUND_ADDR##*:}"
  RESOLVED_SERVER_URL="http://localhost:${BOUND_PORT}"
  echo "gru server bound to ${BOUND_ADDR}"
fi

# Only overwrite VITE_GRU_SERVER_URL when the caller didn't pre-set it.
# The minion-frontend env spec sets this to the parent's URL; respecting
# that lets frontend-only minions point their dashboard at the parent.
if [[ -z "${VITE_GRU_SERVER_URL:-}" ]]; then
  if [[ -n "$RESOLVED_SERVER_URL" ]]; then
    if [[ -n "$TAILSCALE_IP" && "${GRU_STATE_DIR:-}" == "" ]]; then
      # Preserve v1 tailnet behavior for the default path.
      export VITE_GRU_SERVER_URL="http://${TAILSCALE_IP}:${BOUND_PORT}"
    else
      export VITE_GRU_SERVER_URL="$RESOLVED_SERVER_URL"
    fi
  else
    export VITE_GRU_SERVER_URL="http://localhost:7777"
  fi
fi

echo "starting web dashboard..."
cd "$ROOT/web"
NPM="${NPM:-$(command -v npm)}"
if [[ ! -d node_modules ]]; then
  echo "installing web dependencies..."
  "$NPM" install --no-fund --no-audit >/dev/null 2>&1
fi

# Vite reads GRU_WEB_PORT in its config (web/vite.config.ts). Port 0 asks
# vite for an ephemeral port; its stdout ("Local: http://localhost:XXXXX/")
# carries the actual bound port, which we scrape into urls.json below.
export GRU_WEB_PORT
"$NPM" run dev > "$WEB_PIPE" 2>&1 &
WEB_PID=$!
prefix_log "web   " "\033[0;32m" "$LOG_DIR/web.log" "$WEB_PIPE" &
WEB_LOG_PID=$!

# Capture the bound web port by grepping the web log. Vite prints one
# "Local:   http://localhost:XXXX/" line on startup. Poll the log file
# with a 15s cap (vite's first build on a cold cache can be slow).
#
# The `|| true` is load-bearing: pipefail + grep exit 1 on no-match would
# otherwise trip set -e during the early iterations before vite has
# written anything.
WEB_BOUND_PORT=""
for i in $(seq 1 150); do
  if [[ -f "$LOG_DIR/web.log" ]]; then
    WEB_BOUND_PORT="$(grep -oE 'localhost:[0-9]+' "$LOG_DIR/web.log" 2>/dev/null | head -1 | awk -F: '{print $2}' || true)"
    if [[ -n "$WEB_BOUND_PORT" ]]; then
      break
    fi
  fi
  sleep 0.1
done
WEB_URL=""
if [[ -n "$WEB_BOUND_PORT" ]]; then
  WEB_URL="http://localhost:${WEB_BOUND_PORT}"
fi

# Publish urls.json so tooling (and the minion agent) can discover the
# resolved endpoints without parsing our stdout.
SERVER_URL_FOR_JSON="${RESOLVED_SERVER_URL:-${VITE_GRU_SERVER_URL:-}}"
cat > "$URLS_FILE" <<JSON
{
  "server_url": "${SERVER_URL_FOR_JSON}",
  "web_url": "${WEB_URL}",
  "state_dir": "${STATE_DIR}",
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
JSON

echo ""
if [[ -z "$SKIP_SERVER" ]]; then
  echo "gru server:    ${RESOLVED_SERVER_URL}"
fi
if [[ -n "$WEB_URL" ]]; then
  if [[ -n "$TAILSCALE_IP" && "${GRU_STATE_DIR:-}" == "" ]]; then
    echo "web dashboard: ${WEB_URL}  (local)"
    echo "               http://${TAILSCALE_IP}:${WEB_BOUND_PORT}  (tailnet)"
  else
    echo "web dashboard: ${WEB_URL}"
  fi
fi
echo "gRPC backend:  ${VITE_GRU_SERVER_URL}  (baked into frontend)"
echo "logs:          $LOG_DIR/"
echo "urls.json:     $URLS_FILE"
echo ""
echo "  tail -f $LOG_DIR/server.log"
echo "  tail -f $LOG_DIR/web.log"
echo ""
echo "press Ctrl+C to stop both"

if [[ -n "$SERVER_PID" ]]; then
  wait "$SERVER_PID" "$WEB_PID"
else
  wait "$WEB_PID"
fi
