#!/usr/bin/env bash
# dev.sh — start gru server + web dashboard with prefixed, tee'd logs.
#
# Logs are written to:
#   ~/.gru/logs/server.log
#   ~/.gru/logs/web.log
#
# An agent monitoring gru can tail those files or grep them for issues:
#   tail -f ~/.gru/logs/server.log
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG_DIR="${HOME}/.gru/logs"
mkdir -p "$LOG_DIR"

SERVER_PIPE="/tmp/gru-dev-server-$$"
WEB_PIPE="/tmp/gru-dev-web-$$"

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

# Ensure ~/.gru/server.yaml exists with a stable API key.
# The key is created once and reused across restarts so the web dashboard
# and hook scripts always talk to the same server with the same credentials.
GRU_CONFIG_FILE="${HOME}/.gru/server.yaml"
mkdir -p "$(dirname "$GRU_CONFIG_FILE")"
if [[ ! -f "$GRU_CONFIG_FILE" ]] || ! grep -q "^api_key:" "$GRU_CONFIG_FILE"; then
  GENERATED_KEY="$(openssl rand -hex 16 2>/dev/null || \
    od -vN 16 -A n -t x1 /dev/urandom | tr -d ' \n')"
  cat > "$GRU_CONFIG_FILE" <<YAML
addr: :7777
api_key: ${GENERATED_KEY}
db_path: ${HOME}/.gru/gru.db
YAML
  echo "created $GRU_CONFIG_FILE with new API key"
fi
GRU_API_KEY="$(grep '^api_key:' "$GRU_CONFIG_FILE" | awk '{print $2}' | tr -d '"'\''[:space:]')"
export VITE_GRU_API_KEY="${GRU_API_KEY}"

# Detect the Tailscale IP so the frontend (running in a remote browser) can
# reach the gRPC server directly.  Falls back to localhost if tailscale isn't
# running or isn't on PATH.
TAILSCALE_IP="$(tailscale ip -4 2>/dev/null | head -1 || true)"
if [[ -n "$TAILSCALE_IP" ]]; then
  export VITE_GRU_SERVER_URL="http://${TAILSCALE_IP}:7777"
else
  export VITE_GRU_SERVER_URL="http://localhost:7777"
fi

# Build the server binary first so we catch compile errors early.
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

echo "building gru..."
cd "$ROOT"
GO="${GO:-$(command -v go 2>/dev/null)}"
"$GO" build -o /tmp/gru-dev ./cmd/gru

echo "starting gru server..."
# Strip TMUX/TMUX_PANE so the server's tmux subcommands (for agent sessions)
# don't inherit a $TMUX from the shell that ran this script. Gru's agent
# sessions are siblings, not nested — without this, tmux warns about nesting
# when make dev is launched from inside a tmux pane.
env -u TMUX -u TMUX_PANE /tmp/gru-dev server > "$SERVER_PIPE" 2>&1 &
SERVER_PID=$!
prefix_log "server" "\033[0;34m" "$LOG_DIR/server.log" "$SERVER_PIPE" &
SERVER_LOG_PID=$!

echo "starting web dashboard..."
cd "$ROOT/web"
NPM="${NPM:-$(command -v npm)}"
if [[ ! -d node_modules ]]; then
  echo "installing web dependencies..."
  "$NPM" install --no-fund --no-audit >/dev/null 2>&1
fi
"$NPM" run dev > "$WEB_PIPE" 2>&1 &
WEB_PID=$!
prefix_log "web   " "\033[0;32m" "$LOG_DIR/web.log" "$WEB_PIPE" &
WEB_LOG_PID=$!

echo ""
echo "gru server:    http://localhost:7777"
if [[ -n "$TAILSCALE_IP" ]]; then
  echo "web dashboard: http://localhost:3000  (local)"
  echo "               http://${TAILSCALE_IP}:3000  (tailnet)"
  echo "gRPC backend:  ${VITE_GRU_SERVER_URL}  (baked into frontend)"
else
  echo "web dashboard: http://localhost:3000"
fi
echo "logs:          $LOG_DIR/"
echo ""
echo "  tail -f $LOG_DIR/server.log"
echo "  tail -f $LOG_DIR/web.log"
echo ""
echo "press Ctrl+C to stop both"

wait "$SERVER_PID" "$WEB_PID"
