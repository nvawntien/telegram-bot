.PHONY: build test test-race test-integration vet lint check sqlc migrate-up migrate-down migrate-down-to-zero compose-up compose-down

SQLC_VERSION ?= v1.30.0
GOLANGCI_LINT_VERSION ?= v2.8.0
DATABASE_URL ?= postgres://shop:shop@localhost:5432/shop?sslmode=disable
INTEGRATION_DATABASE_URL ?= $(DATABASE_URL)

build:
	go build ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	INTEGRATION_DATABASE_URL="$(INTEGRATION_DATABASE_URL)" go test -tags=integration -count=1 -timeout=10m ./tests/integration

vet:
	go vet ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

check: test vet lint build

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

migrate-up:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate up

migrate-down:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate down

migrate-down-to-zero:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/migrate down-to-zero

compose-up:
	docker compose up --build -d

compose-down:
	docker compose down
