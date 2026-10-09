.PHONY: dev test test-race lint migrate seed generate

dev:
	go run ./cmd/api

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...

migrate:
	go run ./cmd/migrate up

seed:
	go run ./cmd/seed

generate:
	sqlc generate
