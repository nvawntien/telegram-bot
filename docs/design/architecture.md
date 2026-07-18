# Target architecture and repository layout

## Runtime flow

```text
Telegram / payment provider
            |
            v
       Gin HTTP API
            |
            v
   application services
            |
            v
       PostgreSQL  <--- goose migrations / sqlc queries
            |
            v
 transactional outbox
            |
            v
 worker claims and retries
            |
            +----> Telegram Bot API
            +----> payment provider
            +----> Google Sheets
```

The API authenticates and parses bounded requests, calls one application use
case, maps domain errors, and returns quickly. Handlers never update order state
directly. The application layer does not know Gin or Telegram message types.

## Final package layout

```text
cmd/
  api/                 API composition and lifecycle
  worker/              worker composition and lifecycle
  migrate/             goose CLI wrapper
internal/
  app/                 order, payment, inventory, delivery, wallet, admin use cases
  domain/              entities, Money, states, transitions, sentinel errors
  postgres/
    generated/         checked-in sqlc output
    transaction.go     transaction runner used by application services
    *_store.go         actor/use-case-oriented SQL adapters
  httpapi/             Gin engine, middleware, health, payment webhook transport
  telegram/
    bot.go             go-telegram/bot client adapter
    router.go          update/callback dispatch
    handlers/customer/ thin customer handlers
    handlers/admin/    thin RBAC-checked admin handlers
    keyboards/         callback encoding and presentation
  payment/
    provider.go         provider boundary
    manual/             manual confirmation adapter
    webhook/            signed provider webhook adapters
    vietqr/             instruction generator only
  crypto/              versioned inventory encryption envelope
  worker/
    outbox.go           claim/lease/retry orchestration
    delivery.go
    order_expiry.go
    broadcast.go
    sheet_sync.go
  config/               environment loading and startup validation
  observability/        slog and Prometheus instruments
migrations/             authoritative PostgreSQL schema history
sqlc/queries/           SQL grouped by use case
tests/integration/      real PostgreSQL concurrency/idempotency tests
deployments/            production examples and runbooks
docs/                   decisions and operational design
```

Migrations are sqlc's schema input, so generated code and deployed schema cannot
silently drift between duplicated schema folders.

## Boundaries and rules

- `domain` imports only the standard library.
- `app` may depend on domain and consumer-owned interfaces, never on Gin,
  Telegram SDK types, pgx rows, or provider JSON.
- PostgreSQL adapters translate generated query types to domain types.
- Telegram/payment/Sheet adapters translate external contracts at the edge.
- Transactions are opened by application services through a narrow transaction
  runner. Repositories receive the transaction-bound sqlc query set.
- Every query and network call takes a `context.Context`.
- Every goroutine is started by a runner and stops from the process context.
- Secrets, decrypted inventory, bot tokens, and webhook signatures are never
  logged or put in outbox/audit JSON.
- Mutable shop settings live in PostgreSQL; secrets and deployment addresses
  remain environment configuration.

## Phase 1 implementation

Phase 1 implements the runtime seams without empty domain scaffolding:

- validated immutable configuration, including a base64-decoded 32-byte
  inventory key;
- structured logger with JSON in production;
- bounded pgx pool and fail-fast startup ping;
- Gin liveness/readiness/metrics routes and bounded route labels;
- request IDs, panic recovery, access logs, HTTP timeouts, and graceful drain;
- worker lifecycle and recurring database dependency check;
- goose runner, first reusable `updated_at` trigger migration, and a compiled
  sqlc health query;
- Docker image, Compose API/worker/migrate/PostgreSQL topology, Make targets,
  lint config, and CI.

Telegram routes are intentionally not fake placeholders in Phase 1. They arrive
with real user/catalog services in Phase 3.
