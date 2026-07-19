# Phase 5 completion report

## Completed

- Implemented atomic idempotent pending-order creation with immutable product
  and protected bank instruction snapshots.
- Implemented quantity/bank/confirm Telegram callbacks, VietQR instructions,
  ownership-safe order list/detail, pending cancellation, encrypted audited bank
  administration, and a multi-worker-safe expiry job.
- Preserved the boundary against payment acceptance, wallet, inventory
  reservation/claim, delivery, refund, broadcast, and Sheet synchronization.

## Files added

- Migration `00016_orders_banks_vietqr.sql`.
- Order, bank, expiry, payment-reference application services and tests.
- Bank encryption, VietQR, PostgreSQL order/bank adapters, and expiry metrics.
- `tests/integration/phase5_test.go`.
- Order creation, expiry, VietQR design documents, and this report.

## Files changed

- SQLC order/bank queries and checked-in generated code.
- Domain order policy, Telegram parser/router/views, API/worker composition,
  configuration, Compose, CI, README, and existing design documents.

## Migrations

Migration 00016 adds bank display/encryption nonce/format/key-version/version
metadata and immutable protected bank instruction columns to orders. It
backfills safe display names, preserves legacy rows, validates complete AES
envelopes, adds bank history lookup, and restricts deletion of referenced bank
accounts. Its down migration refuses silent loss while Phase 5 rows exist.

The empty-schema `up -> down-to-zero -> up` cycle succeeds through version 16.
Existing Phase 4 data remains compatible.

## Order creation

The application transaction locks the PostgreSQL user, loads consistent active
product/category and bank state, validates bounded quantity, calculates integer
VND totals with overflow detection, performs an availability hint, inserts the
pending order and item snapshots, appends initial history, completes the update
receipt, and commits. Telegram and VietQR network calls are absent from the
transaction; VietQR generation itself performs no network I/O.

## Idempotency

Durable Telegram receipts stop replay before dispatch. The stable confirmation
flow ID derives an application key protected by unique
`(user_id,idempotency_key)`. Payment reference has its own unique constraint and
bounded random-collision retry. Ten concurrent calls for one operation produce
one order, one item, one reference, and one initial history row. A duplicate
returns the existing reference.

## Ownership guarantees

Customer list/detail/lock queries include Telegram ownership in PostgreSQL.
Cancellation also uses ID, internal owner ID, pending status, expiry, and
optimistic version in its conditional update. Missing and foreign IDs expose
the same not-found semantics. Integration tests prove User A cannot list, view,
or cancel User B's order.

## Order history

Creation, cancellation, and expiry append to the existing immutable
`order_status_history`. Creation uses `null -> pending_payment`; cancellation
uses the customer actor and reason `customer_cancelled`; expiry uses the system
actor and reason `payment_window_elapsed`. Duplicate operations do not append
duplicate history.

## Cancellation

Only an owned, unexpired `pending_payment` order can become `cancelled`.
Cancellation, history, and receipt completion are atomic. A history failure
rolls status and receipt back. Repeating an already-cancelled operation returns
an idempotent result and does not touch inventory.

## Expiry worker

The worker runs a configurable interval/batch/timeout job. PostgreSQL orders
overdue pending rows by expiry and ID, locks with `FOR UPDATE SKIP LOCKED`,
rechecks state/time, updates and appends history in one transaction. Paid,
future, and cancelled rows are excluded. Two service instances process the
same due set without duplicate history. Customer notification is deferred.

## Bank administration

Bank numbers use a dedicated AES-256-GCM/HKDF/HMAC adapter and key configuration.
Admins list redacted metadata and use durable owner/version/expiry sessions to
create, fully edit, activate, or deactivate accounts. Mutation, redacted audit,
session finish, and receipt completion commit together. Account input is
requested last and is never persisted in session JSON. No hard-delete use case
exists; the foreign key rejects deletion after an order references the account.

## VietQR instruction generation

The adapter validates BIN/account/reference/amount/names/expiry, resolves one
configured HTTPS URL, and encodes query parameters with the standard URL API.
Output is deterministic and supports Unicode/special characters. Telegram shows
amount, recipient, reference, expiry, and a warning that QR is not payment
proof. No instruction changes an order status.

## Inventory non-reservation boundary

Creation counts only available authenticated Phase 4 inventory and rejects a
short count. It does not call the claim primitive, update inventory status, or
insert an active mapping. Tests create two pending orders while the same rows
remain available and mappings remain absent. Stock is therefore indicative,
not guaranteed until atomic post-payment claim in the next phase.

## Tests

- Domain, Money, payment reference, bank crypto, VietQR, callbacks, formatters,
  config, metrics, expiry worker cancellation/timeout, and order service unit
  tests.
- Gin webhook and existing Telegram client/router tests remain green.
- PostgreSQL tests cover snapshots, validation, rollback, durable receipts,
  10-way duplicate concurrency, IDOR, cancellation, expiry multi-worker safety,
  bank audit/redaction/FK, non-reservation, and Telegram failure after commit.
- Existing Phase 1–4 tests remain enabled.

## Commands run

- Baseline before Phase 5: `git status`, `go test ./...`, `go test -race ./...`,
  `go vet ./...`, `go build ./...`, `make sqlc`, `make lint`,
  `make test-integration`, and `docker compose config --quiet`.
- Development checkpoints: targeted `go test`, full `go test ./...`, `go vet
  ./...`, `go build ./...`, `make sqlc`, `make lint`, `make test-integration`,
  and Compose validation.
- Final validation repeats the complete baseline plus migration cycle and Git
  metadata/security checks.

## Test results

The baseline was green before changes. Phase 5 unit, integration, concurrency,
ownership, worker, SQLC drift, lint, vet, build, race, migration, and Compose
checks are green at completion.

## Security review

- Customer callbacks supply no price, amount, reference, account number, or
  status. Server state is reloaded inside the use case.
- Database constraints resolve duplicate operation/reference races.
- Inactive bank/product/category and unauthenticated stock are rejected.
- Product and protected bank snapshots remain stable after source edits.
- Expiry selects only overdue pending rows and never calls inventory/payment.
- Account numbers, keys, QR URLs, inventory data, and identifiers are absent
  from metric labels and normal structured logs.
- Telegram handlers do not import SQLC or open transactions; application/domain
  packages do not import Gin.

## Known limitations

- VietQR is an instruction only; no transfer is confirmed automatically or
  manually.
- Pending orders do not reserve stock, so more pending quantity than available
  inventory may exist concurrently.
- Telegram replies have no outbox; send failure after commit requires the user
  to reopen `/orders` and does not resend a completed update automatically.
- Expiry does not notify customers. Bank/inventory key rotation is not yet an
  operator workflow.

## Next phase

Phase 6 adds payment acceptance, signed webhook/manual confirmation, wallet
ledger, late-payment reconciliation, and atomic inventory claim only after
accepted payment.
