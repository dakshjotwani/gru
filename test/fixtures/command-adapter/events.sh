#!/usr/bin/env bash
# Reference `events` script. Long-lived. Emits a "started" event on launch
# and then heartbeats every 30s until the sandbox is removed.
set -euo pipefail

REF="${1:?missing provider_ref}"

printf '{"kind":"started","detail":"fixture events online"}\n'

while [[ -d "${REF}" ]]; do
  printf '{"kind":"heartbeat"}\n'
  sleep 30
done

printf '{"kind":"stopped","detail":"sandbox gone"}\n'
