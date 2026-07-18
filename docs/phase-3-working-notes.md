# Phase 3 working notes

## Implementation plan

1. Add one forward-only Phase 3 migration for durable Telegram update receipts,
   audit/update correlation, and optimistic category versions.
2. Add use-case SQLC queries and PostgreSQL adapters behind consumer-owned
   application interfaces.
3. Implement user/catalog/admin services with transactional catalog mutation,
   audit, session guards, and receipt completion.
4. Add the go-telegram/bot adapter, typed callback/command routing, and a bounded
   secret-verified Gin webhook.
5. Prove the boundaries with unit, HTTP, fake Telegram API, and isolated-schema
   PostgreSQL tests before documenting and committing each stable checkpoint.

## Update idempotency policy

- Delivery is at least once; the service does not claim network exactly-once.
- A new update is inserted then atomically moved to `processing`.
- Concurrent or repeated `processing`/`completed` updates return accepted
  duplicate semantics without running application behaviour again.
- `failed` updates and processing leases older than the configured stale window
  may be claimed again.
- Admin catalog mutation, audit, session advancement, and receipt completion
  commit together. Telegram confirmation is sent only after commit.
- Customer reads/user sync complete the receipt before sending. A send failure
  is logged and measured but does not reopen the receipt automatically.

## Scope decisions

- `ADMIN_TELEGRAM_IDS` performs idempotent startup bootstrap only; every runtime
  authorization reads `users` and `admins` from PostgreSQL and never reactivates
  a revoked record.
- Webhook registration remains an explicit deployment operation, not an API
  startup side effect.
- Phase 3 uses durable admin sessions but does not introduce order, payment,
  inventory encryption, delivery, broadcast, or Sheet workflows.
