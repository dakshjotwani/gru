#!/usr/bin/env bash
# Reference `exec` script. argv[1] is the provider_ref (sandbox path);
# argv[2..] is the agent command. Change into the sandbox and run the cmd.
set -euo pipefail

REF="${1:?missing provider_ref}"
shift

cd "${REF}"
exec "$@"
