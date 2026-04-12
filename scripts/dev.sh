#!/usr/bin/env bash
# dev.sh — start gru server + web dashboard, kill both on exit.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER_PID=""
WEB_PID=""

cleanup() {
  echo ""
  echo "shutting down..."
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null || true
  [[ -n "$WEB_PID"    ]] && kill "$WEB_PID"    2>/dev/null || true
  wait "$SERVER_PID" "$WEB_PID" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Build the server binary first so we catch compile errors early.
echo "building gru..."
cd "$ROOT"
go build -o /tmp/gru-dev ./cmd/gru

echo "starting gru server..."
/tmp/gru-dev server &
SERVER_PID=$!

echo "starting web dashboard..."
cd "$ROOT/web"
npm run dev &
WEB_PID=$!

echo ""
echo "gru server:    http://localhost:7777"
echo "web dashboard: http://localhost:3000"
echo ""
echo "press Ctrl+C to stop both"

wait
