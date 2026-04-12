.PHONY: proto sqlc build test generate lint

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
