# Target architecture

## Implemented Phase 3 runtime

```text
Telegram Bot API
      |
      | POST /webhooks/telegram + secret-token header
      v
Gin middleware and bounded webhook adapter
      |
      v
typed Telegram update router
      |
      +----> user/catalog/update/admin application services
                    |
                    v
          consumer-owned store interfaces
                    |
                    v
          SQLC adapters and PostgreSQL

after database commit only:
typed response plan -> Telegram client adapter -> Telegram Bot API
```

The webhook owns HTTP concerns: method/path routing, content type, constant-time
secret comparison, body limit, tolerant JSON decoding, request timeout, request
ID, recovery, HTTP status, structured access logs, and bounded-label metrics. It
does not import generated SQLC queries or perform business mutations.

The Telegram package owns Bot API models, typed command/callback parsing,
message formatting, and its small `Messenger` boundary. It converts Telegram
profiles to application input and converts application results to escaped HTML
and inline keyboards. Callback data uses a `v1` prefix and remains below the
Telegram 64-byte limit.

The application package owns user status decisions, catalog pagination, receipt
semantics, admin authorization, durable session transitions, validation, and
integer VND parsing. Its interfaces are use-case-specific and defined by the
consumer. It imports neither Gin nor generated PostgreSQL/Telegram types.

PostgreSQL adapters map application models to SQLC parameters/results. Admin
catalog adapters own the transaction boundary that reauthorizes the actor,
locks the session, applies optimistic versions, mutates the resource, writes
audit evidence, completes the session, and completes the Telegram receipt.

## Durable state

- `users` is the Telegram identity and status source of truth. Sparse updates do
  not overwrite known profile data, and upsert never clears a ban.
- `admins` is the runtime authorization source of truth.
  `ADMIN_TELEGRAM_IDS` only creates missing bootstrap owner records.
- `admin_sessions` stores state, JSON payload, expiry, and optimistic version;
  no process-local session map exists.
- `telegram_update_receipts` provides concurrent claim, completed duplicate,
  failed retry, and stale-processing reclaim semantics.
- `categories.version` and existing `products.version` protect stale admin
  actions.
- `audit_logs.telegram_update_id` correlates mutations with the durable update
  receipt while before/after snapshots stay explicit and secret-free.

## Observability and security boundaries

Logs carry request/update/user/chat/resource identifiers and results, but never
the bot token, webhook secret, raw update, or arbitrary private message text.
Prometheus labels are limited to operation, method, update type, and result.
Each server/test receives its own registry, so collectors are never registered
through global mutable state.

Customer catalog reads require active categories and products. Admin callbacks
reauthorize against PostgreSQL and validate session ownership, expiry, session
version, resource identity, resource version, and target state. Error mapping
returns short user-facing messages instead of database details.

## Future runtime

Later phases add encrypted inventory, orders, payment reconciliation, a durable
outbox, delivery, broadcast, and Sheet synchronization behind the same
application and PostgreSQL boundaries. Those packages and commands are not
stubbed in Phase 3. External calls will remain outside database transactions.
