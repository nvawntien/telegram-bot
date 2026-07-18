# Proposed PostgreSQL schema

## Identifier, time, and money conventions

- Business rows use `bigint GENERATED ALWAYS AS IDENTITY`. Telegram IDs remain
  external `bigint` values with unique constraints. One strategy keeps joins
  compact and avoids UUID randomness in the PostgreSQL queue indexes.
- All business timestamps are `timestamptz`, written by PostgreSQL in UTC and
  displayed in the user's timezone only at the transport edge.
- VND values are `bigint` and Go `Money int64`; no decimal or floating-point
  arithmetic. Non-negative totals have `CHECK (amount >= 0)`. Signed ledger
  deltas must be non-zero and agree with entry type.
- Mutable rows have `created_at`, `updated_at`, and the shared
  `set_updated_at()` trigger. Append-only event/ledger/audit rows only have
  `created_at` or their event timestamp.
- Evolvable states use `text` plus named check constraints instead of PostgreSQL
  enum types, allowing controlled additions without enum migration locks.
- Hard deletion is limited to data with no business history. Products,
  inventory, users, and bank accounts are normally disabled.

## Tables and invariants

### `users`

- `id bigint identity primary key`
- `telegram_id bigint not null unique check (telegram_id > 0)`
- `username text`, `full_name text not null default ''`
- `status text not null check (status in ('active','banned'))`
- `ban_reason text`, `last_seen_at timestamptz`
- `created_at`, `updated_at`

Indexes: unique Telegram lookup; `(status, id)` for broadcasts; recent-user
admin view on `created_at desc`.

### `admins`

- `id bigint identity primary key`
- `user_id bigint not null unique references users(id) on delete restrict`
- `role text not null check (role in ('owner','admin','operator','support'))`
- `is_active boolean not null default true`
- `created_by bigint references admins(id) on delete restrict`
- `created_at`, `updated_at`

Admin allowlist from configuration bootstraps owners; the table is authoritative
for runtime RBAC. Index `(is_active, role)` supports authorization lookup.

### `categories`

- `id bigint identity primary key`
- `name text not null`, `slug text not null unique`, `emoji text not null`
- `sort_order integer not null default 0`
- `is_active boolean not null default true`
- `created_at`, `updated_at`

Constraints reject blank names/slugs. Index `(is_active, sort_order, id)` powers
the customer catalog.

### `products`

- `id bigint identity primary key`
- `category_id bigint not null references categories(id) on delete restrict`
- `name text not null`, `slug text not null`, `description text`
- `price_vnd bigint not null check (price_vnd >= 0)`
- `fulfilment_type text not null check (... in ('inventory','contact'))`
- `contact_url text`, `is_active boolean not null default true`
- `version bigint not null default 1`
- `created_at`, `updated_at`

Unique `(category_id, slug)`. Catalog index
`(category_id, is_active, id)`. A constraint requires `contact_url` for contact
products. Order items retain name and price snapshots, so later edits are safe.

### `inventory_items`

- `id bigint identity primary key`
- `product_id bigint not null references products(id) on delete restrict`
- `encrypted_payload bytea not null`
- `encryption_key_id text not null`
- `payload_fingerprint bytea not null`
- `status text not null check (... in ('available','reserved','sold','disabled'))`
- `reserved_order_id bigint references orders(id) on delete restrict`
- `reserved_until timestamptz`
- `sold_order_id bigint references orders(id) on delete restrict`
- `disabled_reason text`
- `created_at`, `updated_at`

Unique `(product_id, payload_fingerprint)` prevents duplicate imports without
decrypting stored goods. A named check enforces state/column consistency:
available has no order references; reserved has `reserved_order_id` and expiry;
sold has `sold_order_id`; disabled cannot be newly claimed. Partial claim index:
`(product_id, created_at, id) WHERE status='available'`. Expired reservation
index: `(reserved_until, id) WHERE status='reserved'`.

Payload uses a versioned AES-256-GCM envelope with random nonce. The encryption
key is never persisted. The fingerprint is HMAC-SHA-256 with a distinct derived
key, not a raw hash vulnerable to guessing.

### `orders`

- `id bigint identity primary key`
- `user_id bigint not null references users(id) on delete restrict`
- `status text not null` with the state-machine check
- `currency char(3) not null default 'VND' check (currency='VND')`
- `subtotal_vnd`, `total_vnd bigint not null check (... >= 0)`
- `payment_code text not null unique`
- `idempotency_key text not null`
- `expires_at timestamptz not null`
- `paid_at`, `delivery_started_at`, `delivered_at`, `cancelled_at` timestamps
- `version bigint not null default 1`
- `created_at`, `updated_at`

Unique `(user_id, idempotency_key)` prevents duplicate callbacks from creating
two orders. Indexes: `(user_id, created_at desc)` for history;
`(status, expires_at, id) WHERE status='pending_payment'` for expiry; and
`(status, updated_at)` for operations.

### `order_items`

- `id bigint identity primary key`
- `order_id bigint not null references orders(id) on delete restrict`
- `product_id bigint not null references products(id) on delete restrict`
- `product_name text not null`, `unit_price_vnd bigint not null`
- `quantity integer not null check (quantity > 0)`
- `line_total_vnd bigint not null`
- `created_at`

Checks require non-negative prices and
`line_total_vnd = unit_price_vnd * quantity` with overflow-safe application
validation. Index `(order_id, id)`; optionally unique `(order_id, product_id)`
while the initial UX has one line per product.

### `order_inventory_items`

- `order_id bigint not null references orders(id) on delete restrict`
- `order_item_id bigint not null references order_items(id) on delete restrict`
- `inventory_item_id bigint not null unique references inventory_items(id) on delete restrict`
- `created_at`
- primary key `(order_id, inventory_item_id)`

This durable mapping is retained through delivery failure/refund for audit.

### `order_status_history`

- `id bigint identity primary key`, `order_id` foreign key
- `from_status text`, `to_status text not null`, `reason_code text`
- `actor_type text`, `actor_id bigint`, `request_id text`
- `created_at`

Index `(order_id, id)`. This is append-only and records every transition even
when several transition steps occur within one payment transaction.

### `payments`

- `id bigint identity primary key`
- `order_id bigint references orders(id) on delete restrict`
- `user_id bigint not null references users(id) on delete restrict`
- `purpose text check (... in ('order','wallet_topup','refund'))`
- `provider text not null`, `provider_transaction_id text`
- `payment_code text not null`, `amount_vnd bigint not null check (amount_vnd > 0)`
- `status text check (... in ('created','confirmed','failed','review','refunded'))`
- `confirmed_at`, `created_at`, `updated_at`

Unique partial `(provider, provider_transaction_id)` where transaction ID is
not null; unique payment code per active instruction; indexes on order and user
history.

### `payment_events`

- `id bigint identity primary key`
- `provider text not null`, `external_event_id text not null`
- `provider_transaction_id text`, `event_type text not null`
- `payload_digest bytea not null`, `sanitized_payload jsonb not null`
- `signature_verified boolean not null`
- `processing_status text check (... in ('received','processed','review','rejected'))`
- `processing_error_code text`, `received_at`, `processed_at`

Unique `(provider, external_event_id)` is the primary replay guard. A provider
transaction unique index prevents the same transfer funding multiple orders.
Raw secrets/signatures/account credentials are not stored in JSON.

### `wallet_accounts`

- `id bigint identity primary key`
- `user_id bigint not null unique references users(id) on delete restrict`
- `balance_vnd bigint not null default 0 check (balance_vnd >= 0)`
- `version bigint not null default 1`
- `created_at`, `updated_at`

`balance_vnd` is a locked materialized balance for efficient debit, not the only
record. Every update and resulting balance is represented in the immutable
ledger in the same transaction.

### `wallet_ledger_entries`

- `id bigint identity primary key`
- `account_id bigint not null references wallet_accounts(id) on delete restrict`
- `entry_type text check (... in ('credit','debit','refund','adjustment'))`
- `amount_vnd bigint not null check (amount_vnd <> 0)`
- `balance_after_vnd bigint not null check (balance_after_vnd >= 0)`
- `reference_type text not null`, `reference_id bigint not null`
- `idempotency_key text not null unique`
- `created_at`

Sign/type checks require credits/refunds to be positive and debits negative.
Index `(account_id, id desc)` supports statements. Rows are never updated.

### `bank_accounts`

- `id bigint identity primary key`
- `bank_bin text not null`, `bank_name text not null`, `account_name text not null`
- `encrypted_account_number bytea not null`, `account_number_fingerprint bytea not null unique`
- `encryption_key_id text not null`, `display_last4 text not null`
- `sort_order integer not null default 0`, `is_active boolean not null default true`
- `created_at`, `updated_at`

Index `(is_active, sort_order, id)`. Full account numbers are decrypted only to
create instructions and never logged.

### `delivery_attempts`

- `id bigint identity primary key`
- `order_id bigint not null references orders(id) on delete restrict`
- `attempt_number integer not null check (attempt_number > 0)`
- `channel text not null default 'telegram'`
- `status text check (... in ('started','succeeded','retryable_failed','permanent_failed'))`
- `telegram_message_id bigint`, `error_code text`, `error_detail text`
- `started_at`, `finished_at`

Unique `(order_id, attempt_number)`, index `(order_id, started_at desc)`. Error
detail is bounded/sanitized and contains no payload.

### `outbox_events`

- `id bigint identity primary key`
- `event_type text not null`, `aggregate_type text not null`, `aggregate_id bigint not null`
- `deduplication_key text not null unique`, `payload jsonb not null`
- `status text check (... in ('pending','processing','completed','failed'))`
- `attempts integer not null default 0`, `max_attempts integer not null`
- `next_attempt_at timestamptz not null default now()`
- `locked_by text`, `locked_at timestamptz`, `last_error_code text`, `last_error_detail text`
- `created_at`, `updated_at`, `completed_at`

Partial claim index `(next_attempt_at, id) WHERE status IN ('pending','processing')`
supports new work and expired lease recovery. A consistency check ties lock
columns to processing status. Payload contains identifiers, never decrypted
inventory.

### `broadcasts`

- `id bigint identity primary key`
- `created_by bigint not null references admins(id) on delete restrict`
- `content jsonb not null`, `status text` (`draft/queued/running/completed/cancelled/failed`)
- `scheduled_at`, `started_at`, `finished_at`, `cancelled_at`
- `total_count`, `success_count`, `failed_count` non-negative integers
- `created_at`, `updated_at`

Index `(status, scheduled_at, id)` supports claims.

### `broadcast_recipients`

- `broadcast_id`, `user_id` foreign keys; primary key `(broadcast_id,user_id)`
- `status text` (`pending/sending/sent/retry/failed/skipped`)
- `attempts`, `next_attempt_at`, `telegram_message_id`, bounded `last_error_code`
- `created_at`, `updated_at`, `sent_at`

Partial index `(next_attempt_at, broadcast_id, user_id)` for pending/retry rows.
The primary key makes recipient expansion idempotent.

### `shop_settings`

- singleton `id smallint primary key default 1 check (id=1)`
- `shop_name text not null`, `support_contact text not null`
- `default_bank_account_id bigint references bank_accounts(id) on delete restrict`
- `order_expire_minutes integer not null check (> 0)`
- `version bigint not null default 1`, `created_at`, `updated_at`

Secrets are excluded. Optimistic versioning prevents concurrent admin settings
wizards from silently overwriting each other.

### `admin_sessions`

- `id bigint identity primary key`
- `admin_id bigint not null references admins(id) on delete cascade`
- `state text not null`, `payload jsonb not null default '{}'`
- `version bigint not null default 1`, `expires_at timestamptz not null`
- `created_at`, `updated_at`

One active session per admin via unique `admin_id`; index on `expires_at` for
cleanup. Payload schemas are validated by the relevant application use case.

### `audit_logs`

- `id bigint identity primary key`
- `actor_type text not null`, `actor_id bigint`, `action text not null`
- `resource_type text not null`, `resource_id bigint`
- `before_data jsonb`, `after_data jsonb`, `request_id text`
- `created_at`

Indexes `(resource_type, resource_id, id desc)`, `(actor_type, actor_id, id desc)`,
and `created_at desc`. Append-only permissions prevent application update/delete.
Sanitization is mandatory before insertion.

### `sheet_sync_runs`

- `id bigint identity primary key`
- `source_id text not null`, `source_version text not null`
- `idempotency_key text not null unique`
- `status text` (`running/completed/partial/failed`)
- `imported_count`, `skipped_count`, `failed_count` non-negative integers
- `error_details jsonb not null default '[]'`
- `started_at`, `finished_at`, `created_at`, `updated_at`

Unique `(source_id, source_version)` avoids replaying the same published sheet
revision. Errors are bounded per row and omit inventory plaintext.

## Migration order

1. Reusable trigger, users/admins, categories/products.
2. Orders and order items without inventory foreign keys.
3. Inventory and mapping tables; then add order references.
4. Payments, wallet, bank accounts, settings.
5. Outbox/delivery, broadcast, sessions, audit, sheet runs.
6. Seed only explicit shop/admin bootstrap data; never sample inventory.

Each phase adds constraints before code starts writing the new shape. Destructive
schema cleanup follows expand/backfill/contract migrations.
