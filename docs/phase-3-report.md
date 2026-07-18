# Phase 3 completion report

## Completed

- Added a production-shaped Gin Telegram webhook vertical slice through typed
  routing, application services, SQLC/PostgreSQL, and a Telegram client adapter.
- Added customer catalog navigation and durable audited admin category/product
  workflows without adding order, payment, inventory, delivery, broadcast, or
  Sheet behaviour.
- Preserved all Phase 1/2 endpoints, migrations, domain constraints, transaction
  runner conventions, and tests.

## Files added

- `migrations/00014_telegram_phase3.sql`.
- `internal/app/` user, catalog, update receipt, and admin services/tests.
- `internal/postgres/app_store.go` and `internal/postgres/admin_store.go`.
- `internal/telegram/` client, parser, router, views, and tests.
- `internal/httpapi/telegram_webhook.go` and its HTTP tests.
- `internal/observability/telegram_metrics.go`.
- `sqlc/queries/telegram.sql` and the Phase 3 PostgreSQL integration suite.
- `docs/phase-3-working-notes.md`, this report, and
  `docs/design/target-architecture.md`.

## Files changed

- API composition, Gin route registration, configuration, metrics tests,
  Compose environment, `.env.example`, Go module files, CI, README, and design
  documents.
- User/catalog/admin SQL queries and checked-in generated SQLC models/query
  methods.
- Migration integration expectations now target schema version 14.

## Migrations

Migration 00014 creates `telegram_update_receipts`, its consistency constraints,
stale-processing indexes, and `updated_at` trigger. It correlates audit rows to
updates with a nullable restricted foreign key and adds optimistic
`categories.version`. The down migration removes only Phase 3 additions.

The isolated PostgreSQL harness proves empty database up, down-to-zero, and up
again. Existing migrations were not edited.

## Telegram webhook

`POST /webhooks/telegram` is registered on the existing Gin server. It requires
JSON, verifies `X-Telegram-Bot-Api-Secret-Token` with constant-time comparison,
limits request bodies, accepts future JSON fields, rejects malformed/trailing
JSON, applies a request timeout, propagates request IDs, and reports generic
client/server responses without leaking secrets or payloads.

Webhook registration is an explicit deployment operation. API startup does not
call `setWebhook`, so multiple replicas do not create hidden external side
effects.

## Update idempotency

Delivery is at least once with durable deduplication. A new, failed, or stale
processing receipt may be claimed; a current-processing or completed duplicate
returns accepted semantics and is not routed again. Concurrent insertion/claim
is serialized by PostgreSQL.

Admin catalog mutation, audit, session completion, and receipt completion share
one transaction. The service does not claim network exactly-once.

## Customer commands

`/start`, `/menu`, `/products`, `/support`, and `/myid` are implemented. The
inline flow supports main menu, active categories, active products by category,
product details, pagination, support, and context-preserving back navigation.
Unknown commands receive a short hint. No order/payment command is exposed.

## Catalog services

The read service returns application models rather than SQLC rows, bounds pages,
rejects negative pages, maps not-found errors, and uses deterministic
`sort_order` plus ID ordering. Customer queries only return active products
through active categories. Admin queries include inactive records.

## Admin authorization

`ADMIN_TELEGRAM_IDS` is an idempotent bootstrap source only. Startup creates
missing owner records but never reactivates a revoked admin. Every `/admin`
command, callback, session operation, and mutation checks current `users` and
`admins` state in PostgreSQL. Bootstrap intentionally emits no repeated startup
audit rows; this avoids noise and is documented as an operational decision.

## Admin sessions

Category/product create, edit, toggle, and cancel workflows use
`admin_sessions`. Sessions persist state/payload/expiry/version, survive a new
service instance, and reject expired, stale, cross-admin, or concurrently
advanced requests. No session is kept in global process memory and no database
transaction stays open while waiting for text input.

## Audited mutations

Category create/update/activate/deactivate and product
create/update/activate/deactivate validate the active admin, session owner and
version, input, category/resource state, and resource version. The catalog row,
explicit stable before/after snapshot, audit row, session completion, and update
completion commit atomically. Audit failure rolls back the catalog mutation.

## Tests

- Unit tests cover user/admin decisions, catalog pagination, session/audit
  models, integer Money, command/callback parsing, callback limits, escaping,
  config validation, Telegram errors/timeouts, and isolated metrics registries.
- HTTP tests cover secrets, content type, malformed/trailing/oversized JSON,
  unknown and `/start` updates, duplicate responses, request IDs, metrics,
  errors, and panic recovery.
- PostgreSQL tests cover user upsert/ban, active catalog filtering, stable
  pagination, bootstrap/revocation, restart-safe sessions, concurrent session
  versions, ownership/expiry, receipt concurrency/reclaim/completion, audited
  category/product atomicity, rollback on audit failure, retained order history,
  concurrent duplicate mutation, and post-commit Telegram failure.
- Telegram API tests use local fake HTTP servers and never call the network.

## Commands run

Baseline and final validation use:

```text
git status --short
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
git diff --exit-code -- internal/postgres/generated
make lint
make test-integration
docker compose config --quiet
```

Migration validation additionally runs an empty-database up, down-to-zero, up
cycle through the existing integration convention.

## Test results

The pre-change Phase 2 baseline passed unit, race, vet, build, SQLC drift, lint,
integration, and Compose validation. Phase 3 unit, HTTP, Telegram adapter,
PostgreSQL integration, lint, and build checks pass. The final command matrix is
rerun immediately before the documentation commit.

## Security decisions

- Bot token and webhook secret are distinct, required API configuration and are
  never accepted via query parameters or logged.
- Raw updates and private text are not stored; receipts retain only bounded
  type/status/error metadata.
- Telegram HTML escapes external names/descriptions; callbacks are versioned,
  typed, bounded, and never authorize an actor.
- Runtime admin authorization comes from PostgreSQL. Session/resource optimistic
  versions, ownership, expiry, and state checks prevent stale callback writes.
- Telegram API calls happen after commit and outside transactions.

## Known limitations

- A Telegram confirmation failure after commit is logged and measured but has
  no response outbox/retry in Phase 3. The committed mutation is never reopened
  by a duplicate update.
- Webhook registration and TLS/public ingress remain deployment operations.
- Support information is immutable process configuration until a later shop
  settings service exists.
- Orders, payment providers, VietQR, wallet, encrypted inventory, delivery,
  broadcast, and Google Sheet synchronization remain unavailable.

## Next phase

Phase 4 adds versioned authenticated inventory encryption, redacted import and
administration, atomic claim/release queries, and real PostgreSQL contention
tests without changing the Phase 3 Telegram/catalog contracts.
