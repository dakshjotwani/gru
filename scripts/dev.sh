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

# Build the server binary first so we catch compile errors early.
echo "building gru..."
cd "$ROOT"
go build -o /tmp/gru-dev ./cmd/gru

echo "starting gru server..."
/tmp/gru-dev server > "$SERVER_PIPE" 2>&1 &
SERVER_PID=$!
prefix_log "server" "\033[0;34m" "$LOG_DIR/server.log" "$SERVER_PIPE" &
SERVER_LOG_PID=$!

echo "starting web dashboard..."
cd "$ROOT/web"
npm run dev > "$WEB_PIPE" 2>&1 &
WEB_PID=$!
prefix_log "web   " "\033[0;32m" "$LOG_DIR/web.log" "$WEB_PIPE" &
WEB_LOG_PID=$!

echo ""
echo "gru server:    http://localhost:7777"
echo "web dashboard: http://localhost:3000"
echo "logs:          $LOG_DIR/"
echo ""
echo "  tail -f $LOG_DIR/server.log"
echo "  tail -f $LOG_DIR/web.log"
echo ""
echo "press Ctrl+C to stop both"

wait "$SERVER_PID" "$WEB_PID"
