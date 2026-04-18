#!/usr/bin/env bash
# install-minion-projects.sh — copy the gru-on-gru minion templates from
# this repo into ~/.gru/projects/ so they can be launched via:
#
#   gru launch gru-minion-fullstack "do X"
#   gru launch gru-minion-frontend  "tweak the queue sort"
#
# Safe to re-run — existing project dirs are skipped unless you pass
# --force, in which case they're overwritten.
#
# Source layout (in-repo):
#   .gru/envs/minion-fullstack.yaml
#   .gru/envs/minion-frontend.yaml
#   scripts/gru-env/minion/*.sh
#
# Destination layout (installed):
#   ~/.gru/projects/gru-minion-fullstack/
#     spec.yaml
#     scripts/*.sh
#   ~/.gru/projects/gru-minion-frontend/
#     spec.yaml
#     scripts/*.sh
#
# The spec's `create:` / `exec:` / etc. template paths are rewritten to
# point at the sibling scripts/ directory inside each project.
set -euo pipefail

FORCE=0
if [[ "${1:-}" == "--force" ]]; then
  FORCE=1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="${HOME}/.gru/projects"

install_one() {
  local variant="$1"   # fullstack | frontend
  local parent_url="${2:-}"

  local name="gru-minion-${variant}"
  local dest_dir="${DEST}/${name}"
  local dest_spec="${dest_dir}/spec.yaml"
  local dest_scripts="${dest_dir}/scripts"

  if [[ -d "$dest_dir" && "$FORCE" -eq 0 ]]; then
    echo "skip ${name}: ${dest_dir} already exists (pass --force to overwrite)"
    return 0
  fi

  mkdir -p "$dest_scripts"
  cp -R "${ROOT}/scripts/gru-env/minion/"*.sh "${dest_scripts}/"
  chmod +x "${dest_scripts}/"*.sh

  # Generate the spec with template paths pointing at the sibling scripts/.
  # The minion-fullstack variant needs no parent_server_url; the frontend
  # variant bakes it into the create command.
  local workdir_default
  workdir_default="${HOME}/workspace/gru"

  # {{.SpecDir}} is the directory of this spec.yaml (populated by
  # internal/env/spec at load time). Using it lets the project dir move
  # around without hardcoding an absolute path.
  if [[ "$variant" == "fullstack" ]]; then
    cat > "$dest_spec" <<'YAML'
name: __NAME__
adapter: command
workdirs:
  - __WORKDIR__
config:
  create:   "{{.SpecDir}}/scripts/create.sh {{.SessionID}} fullstack"
  exec:     "{{.SpecDir}}/scripts/exec.sh {{.ProviderRef}}"
  exec_pty: "{{.SpecDir}}/scripts/exec-pty.sh {{.ProviderRef}}"
  destroy:  "{{.SpecDir}}/scripts/destroy.sh {{.ProviderRef}}"
  status:   "{{.SpecDir}}/scripts/status.sh {{.ProviderRef}}"
  events:   "{{.SpecDir}}/scripts/events.sh {{.ProviderRef}}"
YAML
  else
    cat > "$dest_spec" <<YAML
name: __NAME__
adapter: command
workdirs:
  - __WORKDIR__
config:
  create:   "{{.SpecDir}}/scripts/create.sh {{.SessionID}} frontend ${parent_url}"
  exec:     "{{.SpecDir}}/scripts/exec.sh {{.ProviderRef}}"
  exec_pty: "{{.SpecDir}}/scripts/exec-pty.sh {{.ProviderRef}}"
  destroy:  "{{.SpecDir}}/scripts/destroy.sh {{.ProviderRef}}"
  status:   "{{.SpecDir}}/scripts/status.sh {{.ProviderRef}}"
  events:   "{{.SpecDir}}/scripts/events.sh {{.ProviderRef}}"
YAML
  fi
  # Substitute the non-template placeholders.
  sed -i.bak "s|__NAME__|${name}|; s|__WORKDIR__|${workdir_default}|" "$dest_spec"
  rm -f "$dest_spec.bak"

  echo "installed ${name} → ${dest_dir}"
}

mkdir -p "$DEST"
install_one fullstack
install_one frontend "http://localhost:7777"

echo ""
echo "Ready to launch:"
echo "  gru launch gru-minion-fullstack --name <name> --description <...> \"<prompt>\""
echo "  gru launch gru-minion-frontend  --name <name> --description <...> \"<prompt>\""
