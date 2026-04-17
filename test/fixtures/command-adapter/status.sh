#!/usr/bin/env bash
# Reference `status` script. Reports running=true iff the sandbox exists.
set -euo pipefail

REF="${1:?missing provider_ref}"

if [[ -d "${REF}" ]]; then
  printf '{"running": true, "detail": {"sandbox": "%s"}}\n' "${REF}"
else
  printf '{"running": false}\n'
fi
