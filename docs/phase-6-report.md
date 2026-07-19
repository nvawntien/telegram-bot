# Phase 6 report

## Completed

Phase 6 implements durable, idempotent payment acceptance and wallet accounting
without crossing the Phase 7 delivery boundary. Exact accepted order payments
claim encrypted inventory atomically and stop at `reserving`. No code decrypts
inventory, sends purchased content, marks an order `delivering/delivered`, or
calls a banking refund API.

## Baseline

Before Phase 6 edits, `main` matched `origin/main` at Phase 5 and the worktree
was clean. `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build
./...`, SQLC generation/drift, lint, PostgreSQL integration, migration cycle,
and Docker Compose validation passed.

## Files added

- migration `00017_payments_wallet_phase6.sql`;
- payment provider, ingestion, acceptance, event-job, admin, wallet, and
  reconciliation application/infrastructure files;
- Gin payment webhook and payment/wallet Prometheus metrics;
- SQLC wallet and reconciliation queries/generated code;
- Phase 6 HTTP/provider/integration/concurrency tests;
- payment ingestion, wallet ledger, and reconciliation design documents.

## Files changed

API/worker wiring, configuration, Telegram parser/router/views, order/payment/
inventory/user queries, generated SQLC, CI, README, schema/state/transaction/
roadmap docs, and the migration version assertion were updated. No committed
migration was edited.

## Migrations

`00017` removes the incorrect provider/reference uniqueness that prevented
competing real transfers while retaining database uniqueness for provider
transaction IDs. It adds payment occurrence time, event lease/retry fields,
wallet top-up intents, append-only payment allocations, payment review cases,
related event targets, worker indexes, checks, and guarded down migration.

## Provider abstraction and webhook security

`WebhookVerifier` isolates provider transport DTOs. `signed_json` is the only
included adapter and is explicitly private/test-only, disabled unless
allowlisted, and not described as compatible with a real provider. It verifies
HMAC-SHA-256 over raw body with constant-time comparison and checks a signed
Unix timestamp against configured tolerance. Body, signature, secret, full
account identifier, and raw metadata are neither logged nor persisted.

The Gin endpoint enforces allowlist, JSON content type, byte limit, request
timeout, durable insert before `202`, safe exact-duplicate ACK, conflicting-body
`409`, authentication `401`, and retryable ingestion `503`.

## Payment-event ingestion and processing

Events are unique by `(provider,external_event_id)` and protected against a
same-ID/different-hash replay. The worker claims due or stale-processing rows
with `FOR UPDATE SKIP LOCKED`, bounded batches/run timeout, attempts, exponential
backoff, cancellation, and graceful process lifecycle. Business reviews are
terminal and are not retried indefinitely.

## Payment acceptance

Webhook and manual confirmation enter the same transaction core. It locks the
event/target, enforces exact reference, amount, currency, state, expiry,
transaction uniqueness and single allocation, then writes payment, order
history, exact inventory claim/mappings, allocation, audit, and event outcome
atomically. Exact duplicates are no-ops; conflicting or secondary transfers are
reviewed. `paid` is a recorded transient state and success stops at `reserving`.

## Manual confirmation

The admin workflow requires a mandatory manual transaction ID, reference,
integer amount, currency, RFC3339 occurrence time, and optional note. Active
PostgreSQL authorization, session owner/state/version/expiry, acceptance,
redacted audit, session completion, and Telegram receipt completion share one
transaction. Duplicate callbacks cannot create a second payment effect.

## Late, mismatch, duplicate, and stock policies

- Unknown reference, amount/currency mismatch, transaction conflict, and
  secondary payment: review, no inventory claim and no wallet credit.
- Expired/cancelled order: preserve payment evidence and review; never revive or
  claim automatically, even when provider occurrence precedes expiry.
- Exact external payment with insufficient stock: preserve confirmed payment,
  exact-set claim updates zero rows, move to `out_of_stock`, and create review.
- Wallet payment with insufficient stock: rollback payment/debit/ledger/order/
  inventory entirely and return insufficient inventory.
- No automatic bank refund or overpayment-to-wallet conversion exists.

## Wallet ledger, top-up, and order payment

Wallet creation is lazy and idempotent under unique user ownership. The ledger
uses signed immutable entries and per-account idempotency; cached non-negative
balance and ledger entry always share a row-locked transaction. `/balance` and
`/nap` provide integer VND UX and clearly state that QR creation is not credit.

Top-up intent creation is idempotent, bounded, expiring, reference-unique, and
snapshots the protected bank instruction. Exact accepted top-up payment creates
one allocation and one ledger credit. Expired/mismatched payment is reviewed.

Wallet order payment checks user ownership, pending/unexpired state and balance,
claims exact inventory, then writes debit ledger, cached balance, payment,
allocation, histories, mappings and Telegram receipt atomically. One hundred
concurrent attempts against a limited balance cannot overspend.

Admin credit/debit adjustment requires target user, positive amount, reason,
idempotency key and durable session. It cannot make the balance negative and
commits ledger, balance, audit, session, and receipt together.

## Reconciliation

The read-only reconciliation service reports completed events without payment,
confirmed payments without allocation/review, wallet balance/ledger mismatch,
reserved quantity/mapping mismatch, and reasonless reviews. Integration tests
require a clean report after competing payments. The operator runbook is in
`docs/design/payment-reconciliation.md`.

## Tests

Tests cover valid/invalid/missing/tampered signatures, timestamp boundary/replay,
normalization and redaction, unknown provider, malformed/oversized HTTP bodies,
durable/duplicate ACK, ingestion failure, exact payment, duplicate transaction,
mismatch, late payment, competing payments, stale reclaim, two workers, external
out-of-stock, exact top-up credit, wallet stock rollback, 100 concurrent debits,
admin manual/adjustment audit, reconciliation, and Telegram balance/top-up/order
payment with callback answers.

## Security and financial review

- Raw-body signature and signed timestamp are verified before trust.
- Database uniqueness protects event, transaction, allocation, wallet, top-up
  reference, and ledger operation boundaries.
- Exact-set inventory SQL claims all requested rows or zero rows.
- No handler imports SQLC; application/domain packages do not import Gin or pgx.
- No provider/network call occurs in a financial transaction.
- Logs/metrics use bounded labels and omit secrets/raw financial payloads.
- Ownership guards prevent one user viewing/paying another user's order/wallet.
- Ledger is append-only and balance is constrained non-negative.

## Commands and results

Baseline and final validation use:

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
git diff --exit-code
make lint
make test-integration
go test -tags=integration -count=1 ./tests/integration -run '^TestMigrationCycle$'
docker compose config --quiet
```

All passed. The migration test proves empty up through 17, down-to-zero, and up
again. Generated SQLC has no drift.

## Known limitations

- No production provider adapter exists; `signed_json` is private/test-only.
- Payment polling is interval-based; there is no notification wake-up.
- Review resolution records an audited operator decision but does not execute a
  banking refund or force an unsafe allocation.
- Reconciliation is a query/service/runbook, not a production scheduler.
- Delivery, inventory decryption, delivery outbox consumption, ambiguous-send
  recovery, and manual redelivery remain unimplemented.

## Next phase

Phase 7: transactional delivery outbox, encrypted inventory decryption at the
delivery boundary, Telegram delivery, retry/backoff, ambiguous-send recovery,
manual redelivery, and delivery audit.
