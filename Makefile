.PHONY: build test test-race vet lint check sqlc migrate-up migrate-down compose-up compose-down

SQLC_VERSION ?= v1.30.0
GOLANGCI_LINT_VERSION ?= v2.8.0

build:
	go build ./cmd/...

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

check: test vet lint build

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down

