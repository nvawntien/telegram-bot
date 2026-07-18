# Telegram Shop Bot — Go rewrite

Production-oriented rewrite of
[`kentzu213/telegram-shop-bot`](https://github.com/kentzu213/telegram-shop-bot)
as a Go modular monolith. The reference repository defines the Telegram product
experience; this repository redesigns persistence, transactions, concurrency,
idempotency, auditability, and recovery around PostgreSQL.

Current status: **Phase 1 foundation complete**. Telegram shop features are
documented and intentionally arrive phase-by-phase; there are no fake webhook
handlers or plaintext inventory shortcuts in the foundation.

## Architecture

The deployable image contains three commands:

- `api`: Gin HTTP server for health/metrics now and Telegram/payment webhooks in
  later phases.
- `worker`: cancellable background runner; outbox, delivery, expiry, broadcast,
  and Sheet jobs are added behind it.
- `migrate`: goose migration runner.

```text
Telegram/payment webhook -> Gin API -> application service -> PostgreSQL
                                                             |
                                                             v
                                                     transactional outbox
                                                             |
                                                             v
                                                          worker
```

External APIs are never called inside a database transaction. See the
[architecture ADR](docs/adr/0001-modular-monolith-and-durable-workers.md) and
[target architecture](docs/design/architecture.md).

## Prerequisites

- Go 1.26.x
- Docker with Compose v2-compatible commands
- PostgreSQL 18 for local parity (provided by Compose)
- Optional: `openssl` to generate an inventory encryption key

## Start locally with Docker Compose

The checked-in defaults are development-only and let the Phase 1 services boot
without real Telegram calls:

```bash
docker compose up --build -d
docker compose ps
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready
curl http://localhost:8080/metrics
```

Expected health response:

```json
{"status":"ok"}
```

Stop services without deleting PostgreSQL data:

```bash
docker compose down
```

For any non-local environment, copy `.env.example`, replace every placeholder,
and especially generate a new key:

```bash
openssl rand -base64 32
```

Never reuse the example encryption key or commit `.env`.

## Run processes from the host

Start PostgreSQL, export variables from `.env.example` with a host-reachable
`DATABASE_URL`, then:

```bash
go run ./cmd/migrate up
go run ./cmd/api
go run ./cmd/worker
```

Migration commands are `up`, `up-by-one`, `down`, `status`, and `version`.
Production deployment should run `migrate up` as a one-shot release step before
starting API/worker instances.

## Development checks

```bash
make test
make test-race
make vet
make build
make lint
make sqlc
```

`make sqlc` uses sqlc v1.30.0. Generated code is committed; CI regenerates it
and fails on drift. `make lint` uses golangci-lint v2.8.0.

## Phase 1 endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health/live` | Process is alive; no dependency call. |
| `GET` | `/health/ready` | Executes a bounded sqlc PostgreSQL health query. |
| `GET` | `/metrics` | Prometheus HTTP counters and duration histograms. |

The server sets/request-propagates `X-Request-ID`, uses structured access logs,
recovers request panics, bounds headers and HTTP timeouts, and drains on
`SIGINT`/`SIGTERM`.

## Configuration

All configuration is immutable after startup and validated before opening the
service. Required variables:

- `DATABASE_URL`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_WEBHOOK_SECRET` (minimum 16 characters)
- `TELEGRAM_WEBHOOK_URL` (HTTPS in production)
- `ADMIN_TELEGRAM_IDS` (comma-separated positive IDs)
- `INVENTORY_ENCRYPTION_KEY` (exactly 32 bytes after standard-base64 decoding)

Operational variables and defaults are documented in `.env.example`, including
order expiry, delivery retry, logging, metrics, shutdown timeout, and pgx pool
limits. Secrets are validated now even though their adapters land later, which
prevents an unsafe production configuration from becoming accepted by default.

## Design documents

- [Node.js analysis and feature parity](docs/design/nodejs-parity.md)
- [Database schema proposal](docs/design/database-schema.md)
- [Order state machine](docs/design/order-state-machine.md)
- [Transaction boundaries](docs/design/transaction-boundaries.md)
- [Roadmap and risks](docs/design/roadmap-and-risks.md)
- [Phase 1 completion report](docs/phase-1-report.md)

## Repository layout

```text
cmd/                    api, worker, migrate commands
internal/config/        environment parsing and validation
internal/httpapi/       Gin server, middleware, health endpoints
internal/observability/ slog and Prometheus metrics
internal/postgres/      pgx lifecycle and generated sqlc code
internal/worker/        cancellable worker process foundation
migrations/             goose schema history
sqlc/queries/           typed SQL source
docs/                   architecture, transactions, roadmap, reports
```

The fuller phase-by-phase layout is in the architecture document. Packages are
created when they gain working behaviour, not as empty skeletons.

## Current limitations

- Telegram and payment webhook routes are not part of Phase 1.
- Core business tables and repositories start in Phase 2.
- The worker currently monitors its PostgreSQL dependency; durable job types
  start with the outbox/delivery phases.
- Compose values are suitable only for local development.

