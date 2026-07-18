# Implemented PostgreSQL schema

Phase 2 implements the schema in goose migrations `00001` through `00013`.
Business rows use `bigint GENERATED ALWAYS AS IDENTITY`; Telegram identifiers
remain external `bigint` values. This keeps joins and queue indexes compact while
supporting Telegram's signed 64-bit identifier range.

## Shared conventions

- Money is VND stored as `bigint` and represented by Go `domain.Money` (`int64`).
  Prices and totals are non-negative. Wallet ledger deltas are signed: credits
  and refunds are positive, debits are negative, and adjustments are non-zero.
- Business timestamps are `timestamptz`. Mutable rows use the PostgreSQL
  `set_updated_at()` trigger, so timestamp policy is consistent across every
  writer, including admin scripts and future services.
- Evolvable states are constrained `text`, not PostgreSQL enums. Go exposes
  typed constants and validates domain transitions separately.
- Historical/financial rows use `ON DELETE RESTRICT`. The schema does not
  cascade-delete orders, payments, inventory mappings, ledgers, or audit data.
- `order_status_history`, `wallet_ledger_entries`, and `audit_logs` have a
  trigger that rejects `UPDATE` and `DELETE`.
- JSON payloads are constrained to their expected object/array shape. Outbox
  and audit payloads must contain sanitized identifiers/data, never decrypted
  inventory or provider secrets.

## Migration dependency order

| Migration | Tables and infrastructure |
|---|---|
| `00001_foundation` | `set_updated_at()` and schema metadata |
| `00002_users_admins` | append-only guard, `users`, `admins` |
| `00003_catalog` | `categories`, `products` |
| `00004_orders` | `orders`, `order_items`, `order_status_history` |
| `00005_inventory` | `inventory_items`, `order_inventory_items` |
| `00006_payments` | `payments`, `payment_events` |
| `00007_wallet` | `wallet_accounts`, `wallet_ledger_entries` |
| `00008_bank_accounts` | `bank_accounts` |
| `00009_outbox_delivery` | `outbox_events`, `delivery_attempts` |
| `00010_broadcasts` | `broadcasts`, `broadcast_recipients` |
| `00011_admin_sessions_audit` | `admin_sessions`, `audit_logs` |
| `00012_sheet_sync` | `sheet_sync_runs` |
| `00013_shop_settings` | singleton `shop_settings` |

Each file has a dependency-safe `Down` section. Integration tests prove the
full `up -> down-to-zero -> up` cycle in an isolated schema.

## Identity and authorization

`users` has a unique positive `telegram_user_id`, optional username/display
name, and constrained `active`, `banned`, or `disabled` status. Indexes support
Telegram lookup, active-user scans, and recent-user views.

`admins` references `users` and constrains roles to `owner`, `admin`,
`operator`, or `support`. The `(is_active, role, id)` index supports future RBAC
lookups. Configuration may bootstrap owners, but this table is the durable
runtime authority.

## Catalog and inventory

`categories` has a unique slug, active flag, and non-negative sort order.
`products` belongs to a category, snapshots price as non-negative VND, and
constrains delivery to `inventory` or `contact`. Contact delivery requires a
URL, while inventory delivery forbids one. The
`products(category_id, is_active, id)` index matches the active catalog query.
Products deliberately contain no stock counter.

Each `inventory_items` row represents one encrypted digital good. It stores an
encrypted envelope, key ID, and a 32-byte fingerprint unique per product. State
consistency is enforced as follows:

- `available`: no reservation, sale, or disabled reason;
- `reserved`: requires `reserved_order_id` and `reserved_until`;
- `sold`: requires `sold_order_id` and has no reservation;
- `disabled`: has no reservation or sale assignment.

The partial claim index orders available items by `(product_id, created_at,
id)`, matching the `FOR UPDATE SKIP LOCKED` query. A second partial index finds
expired reservations. `order_inventory_items` has a unique inventory ID and a
composite foreign key proving its order item belongs to the same order.

## Orders

`orders` belongs to a user and constrains currency to VND. It stores subtotal,
total, unique payment reference, per-user idempotency key, expiry and lifecycle
timestamps, plus an optimistic `version`. Status is one of:

```text
pending_payment payment_review paid reserving delivering delivered expired
cancelled out_of_stock delivery_failed refunded
```

`order_items` retains product name, unit price, quantity, and line-total
snapshots. Checks require positive quantity, non-negative integer money, and an
exact line-total calculation. `order_status_history` is append-only and uses
the same constrained status vocabulary.

Indexes match ownership history, status operations, and pending-order expiry:
`(user_id, created_at desc, id desc)`, `(status, created_at, id)`, and a partial
`(expires_at, id)` pending index.

## Payments and wallet

`payments` supports order, wallet top-up, and refund purposes. Amounts are
positive VND. `(provider, payment_reference)` is unique, and a partial unique
index on `(provider, provider_transaction_id)` prevents one provider transfer
from being applied twice. Confirmed payments require `confirmed_at`.

`payment_events` uses unique `(provider, external_event_id)` as its replay
guard. It stores a 32-byte payload hash, sanitized JSON object, signature result,
and constrained processing state; no webhook secret or raw sensitive payload is
stored.

`wallet_accounts` has one non-negative materialized balance per user. The
append-only ledger remains the audit source. Ledger idempotency is scoped to
`(account_id, idempotency_key)` so independent accounts may use the same
external key without collision.

## Durable work and operations

`outbox_events` constrains pending/processing/completed/failed state, attempts,
worker lock ownership, and completion timestamps. Partial due-work and lease
indexes support multiple workers. The claim query uses `FOR UPDATE SKIP LOCKED`;
completion verifies the worker ID before changing the row.

`delivery_attempts` preserves ordered, unique attempts per order. `broadcasts`
and `broadcast_recipients` constrain lifecycle/counters and provide claim
indexes. Recipient primary keys make expansion idempotent.

`admin_sessions` is durable, versioned, unique per admin, and indexed by expiry.
`audit_logs` is append-only and indexed by resource, actor, and creation time.
`sheet_sync_runs` deduplicates source revisions and idempotency keys while
retaining bounded result counts/errors. `shop_settings` is a versioned singleton
with an optional restricted foreign key to the default bank account.

## Encryption boundary

The schema stores only encrypted inventory/account data, key identifiers, and
fixed-size fingerprints. AES-GCM envelope creation, HMAC fingerprinting, and key
rotation are Phase 4 application concerns. No committed integration fixture
contains plaintext inventory credentials.
