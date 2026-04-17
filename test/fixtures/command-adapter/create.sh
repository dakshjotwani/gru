#!/usr/bin/env bash
# Reference command-adapter `create` script. Provisions a sandbox directory
# under ~/.gru-test/<session> and prints the canonical JSON result on stdout.
set -euo pipefail

SESSION_ID="${1:-${GRU_SESSION_ID:-anon}}"
WORKDIR="${2:-${GRU_WORKDIR:-/tmp}}"

ROOT="${GRU_FIXTURE_ROOT:-${HOME}/.gru-test/fixtures}"
SANDBOX="${ROOT}/${SESSION_ID}"

mkdir -p "${SANDBOX}"
echo "provisioned sandbox at ${SANDBOX}" >&2

# The last non-empty line of stdout MUST be the canonical JSON result.
printf '{"provider_ref": "%s", "pty_holders": ["tmux"]}\n' "${SANDBOX}"
