# Nix flake for cross-platform dev; bash for per-OS service install

**Decision.** Introduce a `flake.nix` providing `devShells.default`, `packages.gru`, and `apps.gru`. Use it for hermetic builds and ad-hoc `nix run`. Keep per-OS bash install scripts and supervisor unit files: the launchd plist on macOS and a systemd user unit on Linux. The Makefile self-bounces into `nix develop` so any caller — humans or agents — gets the correct toolchain without entering the dev shell first.

## Context

Gru runs on four platforms: macOS (primary dev machine), WSL Ubuntu, bare-metal Ubuntu, and NixOS. A single operator manages all four boxes, with multiple parallel agents working on gru in git worktrees. Several pain points motivated a change:

- Host tool pollution: `go`, `node`, `buf`, and `sqlc` versions drift across machines, causing "works on my box" failures.
- Agents lose the gru CLI when invoked from fresh tmux panes or CI-like environments with stripped `PATH`.
- NixOS refuses to execute binaries built outside Nix's sandbox, so a plain `go install` workflow doesn't work there.
- Build and service management spread across Make, bash, launchd, and (proposed) systemd, with no shared cross-platform entry point.

## Why this approach

**Alternatives considered:**

- *Full Nix (nix-darwin + homeManagerModules + nixosModules)*: rejected because nix-darwin is a real commitment on the macOS box — it takes over launchd config and requires aligning the system-level Nix daemon with the module. Even in Nix, you still write a launchd service definition AND a systemd service definition; the per-OS supervisor units don't share meaningful code. The declarative NixOS service module is useful but is a separate, larger increment that can land after the flake provides hermetic builds.

- *mise/asdf + .tool-versions + bash everywhere*: pins binary versions but still installs tools onto the host. This doesn't help NixOS at all (binaries are dynamically linked against glibc paths that don't exist in the Nix store). Version pinning without isolation is a partial fix.

- *Devcontainers*: requires Docker Desktop on macOS (paid license, high RAM, VM overhead). CLI ergonomics are poor for agents not running inside VS Code's container mode. The filesystem mount boundary also complicates the tmux-based session model gru relies on.

- *Direnv + nix-direnv*: would auto-activate the dev shell on `cd`, but requires installing direnv per machine and adds silent failure modes when shells initialize in unexpected orders (e.g., tmux panes that don't source `.envrc`). The self-bouncing Makefile provides the same ergonomic outcome — any `make <target>` call drops into the Nix shell automatically — with one fewer moving part per machine.

## Consequences

- **Hermetic dev and builds via `nix develop`.** No host pollution. `go`, `node`, `tmux`, `git`, and `gnumake` come from `nixpkgs`; `buf` and `sqlc` are go tools declared in `go.mod` and require no separate nixpkgs entry.
- **macOS install path is semantically unchanged.** `deploy/launchd/com.gru.server.plist` and the bash install/uninstall/status logic in `scripts/install-gru.sh` are untouched. Existing installs do not need migration.
- **Linux gets a systemd user unit** at `~/.config/systemd/user/gru-server.service`. Server logs go to journald (`journalctl --user -u gru-server`); only the autodeploy log remains a file at `~/.gru/logs/autodeploy.log`. WSL users get a linger hint on install.
- **NixOS service install is deferred.** `nix run github:dakshjotwani/gru` works for one-off use. A proper NixOS module (`nixosModules.gru-server`) is a future increment.
- **Adds a Nix prerequisite per machine.** The Determinate Systems installer (`https://install.determinate.systems/nix`) is the easiest path for macOS, WSL, and bare Ubuntu. NixOS already has Nix.
- **Web/dist is not baked into `packages.gru` in v1.** The Nix package builds the Go binary only; a TODO marks where `buildNpmPackage` should be added. Servers installed via `nix run` must separately supply `web/dist` or set `GRU_WEB_DIST` to an existing directory. This is a known limitation documented here.
