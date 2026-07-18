# Phase 2 completion report

## Completed

Phase 2 establishes the constrained commerce database and the primitives that
future application services will use. It deliberately does not add Telegram
handlers, webhook routes, payment-provider adapters, delivery calls, or complete
order/payment flows.

## Files added

- Twelve dependency-ordered migrations, `00002` through `00013`.
- `internal/domain`: integer VND money, typed statuses, order transitions,
  sentinel errors, and unit tests.
- Use-case SQL files for users, catalog, orders, inventory, payments, and outbox,
  plus checked-in generated sqlc code.
- `internal/postgres/transaction.go`.
- `tests/integration`: isolated PostgreSQL harness and migration, constraint,
  foreign-key, trigger, transaction, inventory-claim, and outbox-claim tests.
- `internal/observability/metrics_test.go`.

## Files changed

- `cmd/api` and `cmd/worker` now use process-specific configuration loaders;
  the API injects Prometheus registration/gathering dependencies into the Gin
  server.
- `cmd/migrate` supports `down-to-zero` for a complete migration round trip.
- `Makefile` exposes integration tests and repeatable local migration commands.
- CI provisions PostgreSQL 18 and runs both unit/race and integration suites.
- README, schema, transaction, and roadmap documentation now describe the
  implemented Phase 2 state.

## Database migrations

The schema is split by dependency: identity/RBAC, catalog, orders, inventory,
payments, wallet, bank accounts, outbox/delivery, broadcasts, admin/audit,
sheet-sync, and settings. All migrations have reversible `Up`/`Down` sections.
The complete `up -> down-to-zero -> up` cycle succeeds on PostgreSQL 18.

Primary/foreign keys, constrained state vocabularies, integer-money checks,
idempotency constraints, ownership mappings, append-only guards, lifecycle
consistency checks, and use-case indexes are enforced in PostgreSQL.

## Domain invariants

- `Money` is `int64` VND; creation rejects negative values, arithmetic detects
  overflow, and multiplication requires positive quantity.
- User, order, inventory, payment, outbox, and wallet-entry states are typed and
  validated.
- Order transitions are explicit. Terminal states cannot reopen; delivered
  cannot return to pending; expired cannot auto-deliver and may only enter the
  late-payment review path.
- Wallet ledger signs are tied to entry type: credits/refunds positive, debits
  negative, adjustments non-zero.

## SQLC queries

Queries are grouped by use case rather than a generic repository. They cover
Telegram-user upsert/lookup, active catalog reads, pending-order creation and
guarded status updates, order ownership/locking/history, inventory counting and
claiming, idempotent payment events/payments, and outbox insert/claim/complete/
retry operations. Every generated method accepts `context.Context`.

Inventory and outbox claims use ordered `FOR UPDATE SKIP LOCKED`. Generated code
is checked in and reproducible with sqlc v1.30.0.

## Integration tests

Tests use the PostgreSQL service already provided by Docker Compose locally and
by the CI service container remotely. Each test creates a cryptographically
random schema, scopes both pgx/sql connections with `search_path`, runs goose
migrations automatically, and drops only that schema during cleanup.

Coverage includes migration round trip; required check/unique/foreign-key
failures; updated-at and append-only triggers; real commit/error/panic transaction
behaviour; exact and insufficient inventory claims; excluded sold/disabled
inventory; competing inventory transactions; competing outbox workers; future
outbox scheduling; and completed-event exclusion.

## Commands run

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
make lint
make test-integration
docker compose config --quiet
```

The migration cycle is also run directly through `cmd/migrate` and within the
isolated integration suite.

## Test results

All unit, race, vet, build, lint, sqlc drift, migration, constraint,
transaction, inventory concurrency, and outbox concurrency checks pass against
PostgreSQL 18.

## Known limitations

- Schema and persistence primitives exist, but application services do not yet
  orchestrate the full order/payment/delivery lifecycle.
- Inventory encryption envelope and fingerprint derivation are intentionally
  deferred to Phase 4; only encrypted-storage constraints exist now.
- Outbox claiming is proven at the SQL/transaction level; no external worker
  runner or Telegram delivery adapter is implemented.
- Payment-provider signature semantics and refund execution require provider and
  business contracts from later phases.

## Next phase

Phase 3 adds the Telegram Gin webhook adapter, user/catalog services, durable
admin authorization/session handling, audited catalog mutations, and the
existing customer command/callback experience on top of these primitives.
