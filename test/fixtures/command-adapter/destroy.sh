#!/usr/bin/env bash
# Reference `destroy` script. Idempotent — succeeds if the sandbox is already
# gone (or if invoked with an empty provider_ref after a failed create).
set -euo pipefail

REF="${1:-}"
if [[ -z "${REF}" ]]; then
  echo "destroy called with empty provider_ref; nothing to do" >&2
  exit 0
fi
rm -rf "${REF}"
