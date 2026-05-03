# flake.nix — hermetic dev environment and reproducible build for gru.
#
# Design (see docs/adr/0003-nix-flake-for-cross-platform-dev.md):
#   - devShells.default  hermetic toolchain for local development
#   - packages.gru       reproducible Go binary wrapped with tmux on PATH
#   - apps.gru           nix run entry point
#
# Service installation is NOT done here. Use scripts/install-gru.sh for
# macOS (launchd) and Linux (systemd user unit). NixOS declarative service
# support is a future increment.
#
# Quick start:
#   nix develop          # enter the dev shell
#   nix build .#gru      # build the binary (replace vendorHash on first run)
#   nix run .#gru        # run the binary ad-hoc
{
  description = "gru — mission control for Claude Code session fleets";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        lib = pkgs.lib;

        # go.mod requires go 1.26.2. nixos-unstable tracks the latest Go
        # release; use pkgs.go (the default, latest stable) since 1.26 may not
        # have a dedicated attribute yet. Replace with pkgs.go_1_26 once it
        # appears in nixpkgs.
        goPkg = pkgs.go;

        gruPackage = pkgs.buildGoModule {
          pname = "gru";
          version = "0.0.1";

          src = ./.;

          # Hash of the vendored Go module set. Regenerate via:
          #   nix build .#gru   # error output prints the new hash
          vendorHash = "sha256-vu71H3XTr/vpf5UkXCuu1ccM06utP5GtamUl+Ha0u/U=";

          # Build only the CLI binary, not every package in the repo.
          subPackages = [ "cmd/gru" ];

          # Skip tests in the package derivation: several touch $HOME (which is
          # /homeless-shelter in the Nix sandbox) and would need a fixture
          # rewrite to run hermetically. Run tests via `make test` or
          # `nix develop -c go test ./...` instead.
          doCheck = false;

          # buf and sqlc are go tools declared in go.mod — they don't need
          # separate nixpkgs entries.

          # Wrap the installed binary so tmux is always on PATH at runtime.
          # gru spawns tmux windows for every managed session; if tmux is
          # missing the process will fail with a confusing exec error.
          nativeBuildInputs = [ pkgs.makeWrapper ];
          postInstall = ''
            wrapProgram $out/bin/gru \
              --prefix PATH : ${lib.makeBinPath [ pkgs.tmux ]}
          '';

          # TODO: bake the frontend into this package using buildNpmPackage so
          # that `nix run` serves the full SPA. For now, servers started via
          # `nix run` must set GRU_WEB_DIST to a pre-built web/dist directory.
          # See docs/adr/0003-nix-flake-for-cross-platform-dev.md consequences.
        };

      in {
        devShells.default = pkgs.mkShell {
          packages = [
            goPkg
            pkgs.nodejs_22
            pkgs.tmux
            pkgs.gnumake
            pkgs.git
          ];

          shellHook = ''
            echo "gru dev shell — go $(go version | awk '{print $3}')"
          '';
        };

        packages.gru = gruPackage;
        packages.default = gruPackage;

        apps.gru = {
          type = "app";
          program = "${gruPackage}/bin/gru";
        };
        apps.default = {
          type = "app";
          program = "${gruPackage}/bin/gru";
        };
      });
}
