ifeq ($(IN_NIX_SHELL),)
# Outside Nix dev shell — re-exec under it so all targets get the hermetic
# toolchain (go, node, tmux, gnumake) from flake.nix devShells.default.
# This lets humans and agents run plain `make <target>` without entering
# `nix develop` first. The bounce is a no-op cost once inside the shell.
# Note: .PHONY is intentionally absent here; the pattern rule %: must match
# all targets for the bounce to work.
%:
	@nix develop --command make $(MAKECMDGOALS)
else
# Inside Nix dev shell — run targets natively (no second Nix invocation).
.PHONY: proto sqlc build build-web test generate lint dev serve install uninstall redeploy status doctor

proto:
	go tool buf generate

sqlc:
	go tool sqlc generate -f internal/store/sqlc.yaml

build:
	go build -o ./gru ./cmd/gru/

# Builds the frontend into web/dist so the backend can serve it on
# a single port (for tailscale serve / HTTPS proxying).
build-web:
	cd web && npm install && npm run build

test:
	go test ./...

generate: proto sqlc

lint:
	go tool buf lint
	go vet ./...

dev:
	./scripts/dev.sh

# Single-port production mode: builds the frontend, builds the Go
# binary, and runs the backend serving the built frontend from
# web/dist. Use this under `tailscale serve` for HTTPS (required
# for Web Push on iOS PWAs).
serve: build-web build
	./gru server

install:
	./scripts/install-gru.sh install

uninstall:
	./scripts/install-gru.sh uninstall

status:
	./scripts/install-gru.sh status

redeploy:
	./scripts/redeploy.sh

doctor:
	@echo "go:    $$(go version 2>/dev/null || echo MISSING)"
	@echo "node:  $$(node --version 2>/dev/null || echo MISSING)"
	@echo "tmux:  $$(tmux -V 2>/dev/null || echo MISSING)"
	@echo "git:   $$(git --version 2>/dev/null || echo MISSING)"
	@command -v gru >/dev/null 2>&1 && echo "gru:   $$(command -v gru)" || echo "gru:   not on PATH"

endif
