# Implementation roadmap and risk register

## Safe implementation order

### Phase 1 — foundation (implemented)

- Go module and three process commands.
- Gin server, structured logs, request IDs, panic recovery, timeouts, metrics,
  liveness/readiness, graceful shutdown.
- Validated environment configuration and bounded pgx pool.
- Goose, sqlc, Docker Compose PostgreSQL, Makefile, lint, and CI.

Exit: unit tests/vet/build pass; migration and Compose boot are verified against
real PostgreSQL.

### Phase 2 — core database (implemented)

1. Add schema in dependency order with constraints and partial indexes.
2. Add domain Money/states/errors and transaction runner.
3. Add use-case-oriented sqlc queries and generated code.
4. Add PostgreSQL integration-test harness and audit writer.

Exit: migrations round-trip; constraints reject invalid states/money; sqlc diff
is clean; real-DB transaction tests pass.

### Phase 3 — users, catalog, admin authorization, Telegram entry (implemented)

1. Add go-telegram/bot webhook adapter and secret-token validation in Gin.
2. Implement user upsert/ban and admin table/role checks.
3. Implement category/product read and admin mutation services plus audit.
4. Preserve `/start`, `/menu`, `/product(s)`, `/support`, `/myid` UI.
5. Persist admin sessions before any multi-step wizard.

Exit achieved: unknown updates are safe; webhook/callback inputs are validated;
runtime admin authorization and sessions are durable; concurrent updates are
deduplicated; catalog changes, audit, session completion, and receipt completion
are atomic; handlers contain no SQL/status writes.

### Phase 4 — encrypted inventory (implemented)

1. Implement versioned AES-GCM envelope/HMAC fingerprint and key validation.
2. Add import/list/disable services with redacted outputs.
3. Add atomic claim/release queries and 100-goroutine last-item test.

Exit achieved: AES-GCM/HKDF/HMAC design is versioned; admin outputs are
redacted; import is durable and duplicate-safe; claim/release is atomic;
sensitive expired reservations are held for recovery; observable persistence,
logs, metrics, callbacks, and Telegram responses contain no plaintext; exactly
one of 100 concurrent claimants wins the last item.

### Phase 5 — orders and VietQR (implemented)

1. Implement guarded order state machine and idempotent create/cancel/history.
2. Add expiry worker and late-event seam.
3. Add bank selection and VietQR instruction adapter.
4. Preserve quantity/QR/check-payment customer flow with ownership checks.

Exit achieved: ten concurrent duplicate creates produce one order/item/reference;
ownership is guarded in PostgreSQL for list/view/cancel; bank and product
snapshots remain stable; expiry is multi-worker safe; pending orders do not
reserve inventory; VietQR never implies payment acceptance.

### Phase 6 — payments and wallet

Status: implemented. The signed adapter is private/test-only; a production
provider adapter and banking refund execution remain explicitly unimplemented.

1. Manual provider and signed webhook provider adapter.
2. Exact payment reconciliation and event/transaction idempotency.
3. Wallet accounts, atomic ledger credit/debit, top-up and balance checkout.
4. Review workflow for late/mismatched/out-of-stock payments; no network refund.

Exit: ten duplicate webhooks create one payment effect; wallet cannot go
negative; every balance change has one idempotent ledger entry.

### Phase 7 — outbox and delivery (implemented)

1. Outbox claim/lease/retry runner and exponential backoff.
2. Telegram delivery adapter and post-send completion transaction.
3. Durable manual fallback, exhausted retry, and admin notification.
4. Multi-worker and crash-after-payment integration tests.

Exit achieved: one durable job is created with payment acceptance; Phase 6 rows
backfill idempotently; retries are bounded; ambiguous outcomes never auto-retry;
confirmed success alone sells exact inventory and marks delivered; 100 workers
claim one job once; crash boundaries and plaintext leakage are tested.

### Phase 8 — operations

1. Broadcast recipients/rate limiter/retry-after/resume/cancel.
2. Sheet adapter/run history/per-row validation/idempotent inventory import.
3. Indexed statistics and complete audit views.
4. Dashboard metrics, backup/restore guide, deployment hardening, security review.

Exit: remaining Definition of Done checks and restore drill pass.

## Primary risks and controls

| Risk | Impact | Control / proof |
|---|---|---|
| Payment webhook semantics are provider-specific and currently unspecified | False credit or replay | Provider fixture contract, HMAC/signature verification, unique event and transaction IDs, exact amount/reference checks |
| Telegram Bot API has rate limits and ambiguous timeout outcomes | Duplicate or missing delivery | Durable attempts, idempotent completion, retry-after support, manual recovery view; never mark delivered before success |
| Digital goods are high-value secrets | Credential disclosure | AES-256-GCM, separate HMAC fingerprint key, key IDs/rotation, redacted logs/audit, least-privilege DB access |
| `SKIP LOCKED` code looks correct but fails under real contention | Oversell | PostgreSQL integration tests with 100 goroutines and multiple concurrent orders; partial unique active-mapping constraint |
| Late payment races expiry | Wrong delivery/refund | Same order row lock, state recheck, explicit `payment_review`, deterministic operator flow |
| Outbox worker dies while processing | Stuck fulfilment | Leases, expired-lease reclamation, max attempts, bounded backoff, multi-worker test |
| Telegram send succeeds but response is lost | Possible duplicate credential message | Persist sending marker/evidence, move to ambiguous, never auto-retry, require audited verification; do not expose item to another buyer |
| Wallet cached balance drifts from ledger | Financial inconsistency | Same locked transaction, balance-after ledger value, periodic reconciliation query, immutable entries |
| Sheet is treated as stock authority | Selling undeliverable items | Catalog import separated from encrypted inventory import; real available rows are authoritative |
| Admin callback/session replay | Unauthorized mutation | Active RBAC lookup, durable versioned session, expiry, ownership/resource/state validation, audit/request ID |
| Broadcast overloads Telegram or PostgreSQL | Rate limiting and noisy retries | Recipient batches, global/per-chat pacing, retry-after, partial indexes, resumable cursor/state |
| Bigint sequence reveals volume | Low confidentiality risk | Accepted for operational simplicity; never expose internal IDs as authorization evidence |
| Migration blocks large tables later | Availability | Expand/backfill/contract, concurrent indexes where applicable, statement/lock timeouts in runbook |
| Phase scope expands into premature abstractions | Slower delivery and hidden rules | Consumer-owned interfaces only, explicit services/SQL, phase exit tests before next surface |

## Decisions still required before their phase

These do not block Phase 1 and should be resolved from provider/business facts,
not guessed in infrastructure code:

- first production automatic payment provider and its official webhook contract;
- exact admin role-to-permission matrix;
- refund execution mechanism and settlement SLA;
- Telegram ambiguous-send policy acceptable to the operator;
- Google Sheet column contract and revision/idempotency source;
- production secret manager and inventory key-rotation procedure.
