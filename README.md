# Telegram Shop Bot — Go rewrite

Production-oriented rewrite of
[`kentzu213/telegram-shop-bot`](https://github.com/kentzu213/telegram-shop-bot)
as a Go modular monolith. The reference repository defines the Telegram product
experience; this repository redesigns persistence, transactions, concurrency,
idempotency, auditability, and recovery around PostgreSQL.

Current status: **Phase 5 ordering and VietQR instructions complete**. The API
accepts secret-verified Telegram webhooks through Gin, durably deduplicates
updates, serves the active catalog, and provides PostgreSQL-backed, audited
category/product and redacted inventory administration. Authenticated
application-layer encryption, atomic inventory claim/release, and conservative
reservation recovery, atomic pending-order creation, ownership-safe history and
cancellation, encrypted bank administration, VietQR instructions, and the
order-expiry worker are implemented. Payment acceptance, inventory reservation
after payment, delivery, refund, broadcast, and Google Sheet workflows remain
disabled.

## Architecture

The deployable image contains three commands:

- `api`: Gin HTTP server for health, metrics, and the Telegram webhook.
- `worker`: cancellable background runner with the Phase 5 pending-order expiry
  job; outbox, delivery, broadcast, and Sheet jobs are added later.
- `migrate`: goose migration runner.

```text
Telegram -> Gin webhook -> update router -> application service -> PostgreSQL
                                                        commit |
                                                               v
                                             Telegram client -> Bot API
```

External APIs are never called inside a database transaction. See the
[architecture ADR](docs/adr/0001-modular-monolith-and-durable-workers.md) and
[target architecture](docs/design/target-architecture.md).

## Prerequisites

- Go 1.26.x
- Docker with Compose v2-compatible commands
- PostgreSQL 18 for local parity (provided by Compose)
- Optional: `openssl` to generate an inventory encryption key

## Start locally with Docker Compose

The checked-in defaults are development-only and let the current services boot
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

Migration commands are `up`, `up-by-one`, `down`, `down-to-zero`, `status`, and `version`.
Production deployment should run `migrate up` as a one-shot release step before
starting API/worker instances.

## Development checks

```bash
docker compose up -d
make migrate-up
make sqlc
make test
make test-race
make test-integration
make lint
make build
```

`make sqlc` uses sqlc v1.30.0. Generated code is committed; CI regenerates it
and fails on drift. `make lint` uses golangci-lint v2.8.0. `make
test-integration` connects to PostgreSQL through `INTEGRATION_DATABASE_URL`
(defaulting to the local Compose database), creates a unique schema per test,
runs migrations automatically, and removes the schema during cleanup.

## Telegram bot and webhook setup

1. Open `@BotFather` in Telegram, run `/newbot`, and store the issued token in
   a local `.env` as `TELEGRAM_BOT_TOKEN`. Never commit that file.
2. Generate a separate webhook secret of at least 16 characters, for example
   with `openssl rand -hex 32`, and set `TELEGRAM_WEBHOOK_SECRET`.
3. Set `TELEGRAM_WEBHOOK_URL` to the public HTTPS URL ending in
   `/webhooks/telegram`. Localhost is accepted only for local configuration;
   Telegram requires a reachable supported HTTPS endpoint.
4. Set `ADMIN_TELEGRAM_IDS` to the comma-separated Telegram IDs that may be
   bootstrapped once. Use `/myid` to obtain an ID. Runtime authorization always
   reads PostgreSQL, so a revoked admin is not restored at the next startup.
5. Start the API, then explicitly register the webhook. Startup never changes
   BotFather/webhook state:

```bash
export TELEGRAM_BOT_TOKEN='replace-locally'
export TELEGRAM_WEBHOOK_URL='https://bot.example.com/webhooks/telegram'
export TELEGRAM_WEBHOOK_SECRET='replace-with-a-random-secret'

curl --fail-with-body --request POST \
  "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=${TELEGRAM_WEBHOOK_URL}" \
  --data-urlencode "secret_token=${TELEGRAM_WEBHOOK_SECRET}" \
  --data-urlencode 'allowed_updates=["message","callback_query"]'
```

Telegram sends the configured secret in
`X-Telegram-Bot-Api-Secret-Token`. The bot token and webhook secret must be
different values. Do not paste either into logs, issue trackers, or fixtures.

To verify local request parsing without a real token or outbound Telegram call,
send an unknown update. Replace only the development secret if you changed the
Compose default:

```bash
curl --fail-with-body http://localhost:8080/webhooks/telegram \
  -H 'Content-Type: application/json' \
  -H 'X-Telegram-Bot-Api-Secret-Token: local-development-secret' \
  --data '{"update_id":900000001,"future_field":{"safe":true}}'
```

The response is `200 OK`. Repeating the same fixture is accepted and skipped by
the durable receipt guard.

## Supported Telegram features

Customer commands:

- `/start` and `/menu`: register/update the Telegram user and show the menu.
- `/products`: browse active categories, active products, and product details
  with bounded inline-keyboard pagination and back navigation. Product detail
  offers preset quantities, active bank selection, confirmation, and atomic
  pending-order creation.
- `/orders`: list only the caller's orders with deterministic pagination.
- `/order <id>`: open an ownership-scoped order detail. Inline buttons expose
  the same detail and allow an unexpired pending order to be cancelled.
- `/support`: show the validated `SUPPORT_CONTACT` value.
- `/myid`: show the caller's Telegram ID, never an internal database ID.

`/admin` opens administration only for an active PostgreSQL admin. The
durable multi-step menus can list, create, edit, activate, or deactivate
categories and products, change product category, and set integer VND prices.
The inventory menu shows per-product status counts, paginates redacted item
metadata, imports one opaque item per line, disables available items, and
re-enables disabled items. It never reveals or exports inventory plaintext,
ciphertext, nonce, or fingerprint. Cancel buttons close the persisted session.
The bank-account menu lists redacted accounts and uses durable sessions to
create, edit, activate, or deactivate encrypted account numbers. Catalog,
inventory, and bank mutations commit safe audit metadata, session completion,
and update completion atomically.

Banned/disabled users are denied. Unknown commands get a short menu hint.
VietQR output is only a transfer instruction. Opening the QR does not confirm a
payment, change the order to paid, reserve inventory, or deliver a secret.
There are deliberately no `/checkpay`, payment-confirmation, wallet, top-up, or
delivery flows in Phase 5.

## HTTP endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health/live` | Process is alive; no dependency call. |
| `GET` | `/health/ready` | Executes a bounded sqlc PostgreSQL health query. |
| `GET` | `/metrics` | Prometheus HTTP counters and duration histograms. |
| `POST` | `/webhooks/telegram` | Bounded, secret-verified Telegram update receiver. |

The server sets/request-propagates `X-Request-ID`, uses structured access logs,
recovers request panics, bounds headers and HTTP timeouts, and drains on
`SIGINT`/`SIGTERM`.

## Configuration

All configuration is immutable after startup and validated before opening a
dependency. API configuration requires:

- `DATABASE_URL`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_WEBHOOK_SECRET` (minimum 16 characters)
- `TELEGRAM_WEBHOOK_URL` (HTTPS in production)
- `ADMIN_TELEGRAM_IDS` (comma-separated positive IDs)
- `INVENTORY_ENCRYPTION_KEY` (exactly 32 bytes after standard-base64 decoding)
- `INVENTORY_ENCRYPTION_KEY_VERSION` (positive integer written to every new
  inventory item)
- `BANK_ACCOUNT_ENCRYPTION_KEY` (a separate standard-base64 32-byte key)
- `BANK_ACCOUNT_ENCRYPTION_KEY_VERSION` (positive current bank key version)

Inventory import is bounded by `INVENTORY_IMPORT_MAX_ITEMS`,
`INVENTORY_IMPORT_MAX_ITEM_BYTES`, and `INVENTORY_IMPORT_MAX_TOTAL_BYTES`.
One LF-delimited line is one opaque item; a terminal CR is removed for CRLF,
blank or whitespace-only lines are counted as rejected, and every other byte is
preserved. Embedded newlines are not supported.

Do not change or lose the encryption key while rows using its version exist.
Losing it makes those rows permanently undecryptable; replacing it under the
same version makes authentication fail. Phase 4 has a future-readable keyring
seam but no rotation command. Keep keys outside PostgreSQL and Git.

Order creation controls include `ORDER_EXPIRE_MINUTES`, `ORDER_MAX_QUANTITY`,
`PAYMENT_REFERENCE_PREFIX`, `PAYMENT_REFERENCE_RANDOM_BYTES`,
`VIETQR_BASE_URL`, `VIETQR_TEMPLATE`, `ORDER_PAGE_SIZE`, and
`BANK_ACCOUNT_PAGE_SIZE`. Other API controls include `TELEGRAM_WEBHOOK_BODY_LIMIT_BYTES`,
`TELEGRAM_WEBHOOK_TIMEOUT_SECONDS`, `TELEGRAM_UPDATE_STALE_SECONDS`,
`ADMIN_SESSION_TTL_MINUTES`, `TELEGRAM_API_TIMEOUT_SECONDS`, and
`SUPPORT_CONTACT`. Operational variables and defaults are documented in
`.env.example`. The worker uses `ORDER_EXPIRY_INTERVAL`,
`ORDER_EXPIRY_BATCH_SIZE`, and `ORDER_EXPIRY_RUN_TIMEOUT`; its separate loader
does not require Telegram tokens, admin IDs, HTTP/webhook settings, VietQR, or
encryption keys. The migration process requires only `DATABASE_URL` and an optional
`MIGRATIONS_DIR`.

Back up PostgreSQL with authenticated access controls appropriate to the
deployment. A backup contains ciphertext rather than plaintext inventory, but
still exposes operational metadata and remains sensitive. Back up encryption
keys separately with version labels and test restores; a database backup
without the matching key cannot recover inventory.

## Design documents

- [Node.js analysis and feature parity](docs/design/nodejs-parity.md)
- [Target architecture](docs/design/target-architecture.md)
- [Implemented database schema](docs/design/database-schema.md)
- [Order state machine](docs/design/order-state-machine.md)
- [Transaction boundaries](docs/design/transaction-boundaries.md)
- [Roadmap and risks](docs/design/roadmap-and-risks.md)
- [Inventory encryption](docs/design/inventory-encryption.md)
- [Inventory administration security](docs/design/inventory-admin-security.md)
- [Inventory reservation](docs/design/inventory-reservation.md)
- [Order creation](docs/design/order-creation.md)
- [Order expiry](docs/design/order-expiry.md)
- [VietQR payment instructions](docs/design/vietqr-payment-instructions.md)
- [Phase 1 completion report](docs/phase-1-report.md)
- [Phase 2 completion report](docs/phase-2-report.md)
- [Phase 3 completion report](docs/phase-3-report.md)
- [Phase 4 completion report](docs/phase-4-report.md)
- [Phase 5 completion report](docs/phase-5-report.md)

## Repository layout

```text
cmd/                    api, worker, migrate commands
internal/config/        environment parsing and validation
internal/httpapi/       Gin server, middleware, health endpoints
internal/app/           user, catalog, order, bank, receipt, admin, and inventory services
internal/bankcrypto/    encrypted bank-account number adapter
internal/inventorycrypto/ versioned encryption and keyed fingerprints
internal/vietqr/        deterministic payment-instruction URL adapter
internal/telegram/      Telegram client, typed router, callbacks, and views
internal/observability/ slog and Prometheus metrics
internal/postgres/      pgx lifecycle and generated sqlc code
internal/worker/        cancellable worker process foundation
migrations/             goose schema history
sqlc/queries/           typed SQL source
tests/integration/      isolated-schema PostgreSQL integration tests
docs/                   architecture, transactions, roadmap, reports
```

The fuller phase-by-phase layout is in the architecture document. Packages are
created when they gain working behaviour, not as empty skeletons.

## Current limitations

- Telegram confirmation delivery has no outbox in Phase 5. If sending after a
  successful commit fails, the database operation stays committed and the error
  is logged/measured; a duplicate update does not resend it.
- Pending orders check current authenticated available inventory but do not
  reserve it. Multiple pending orders may observe the same stock. Payment
  acceptance and atomic claim belong to Phase 6.
- No provider/manual payment confirmation, wallet, refund, automatic delivery,
  or late-payment reconciliation is implemented.
- Automatic reservation sweeping is deferred because no payment/delivery
  recovery flow can safely resolve sensitive order states yet.
- The worker expires overdue pending orders without notifying Telegram.
  Durable notification/outbox and external delivery begin in later phases.
- Compose values are suitable only for local development.
