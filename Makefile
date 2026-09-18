ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: build run test test-integration lint migrate-up migrate-down docker-up docker-down
build:
	go build -o bin/gateway ./cmd/gateway
	go build -o bin/migrate ./cmd/migrate
run:
	go run ./cmd/gateway
test:
	go test ./...
test-integration:
	@TEST_DATABASE_URL="$(DATABASE_URL)" go test -race ./...
lint:
	go vet ./...
migrate-up:
	go run ./cmd/migrate up
migrate-down:
	go run ./cmd/migrate down
docker-up:
	docker compose up -d --wait
docker-down:
	docker compose down
