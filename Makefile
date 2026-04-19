.PHONY: proto sqlc build build-web test generate lint dev serve

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
