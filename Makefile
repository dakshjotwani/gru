.PHONY: proto sqlc build test generate lint dev

proto:
	go tool buf generate

sqlc:
	go tool sqlc generate -f internal/store/sqlc.yaml

build:
	go build ./cmd/gru/...

test:
	go test ./...

generate: proto sqlc

lint:
	go tool buf lint
	go vet ./...

dev:
	./scripts/dev.sh
