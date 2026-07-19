# Phase 7 report

## Completed

Phase 7 implements durable transactional delivery from accepted payment through
confirmed Telegram send, plus conservative ambiguous-send handling and audited
manual recovery. No broadcast, Sheet Sync, bank refund execution, production
payment provider, or deployment rollout was added.

## Files added

- migration `00018_transactional_delivery_phase7.sql`;
- delivery domain/application/store/Telegram adapter/metrics implementation;
- redacted admin delivery service and controlled recovery store;
- Phase 7 PostgreSQL integration test suite;
- delivery outbox, state machine, ambiguity, and manual-recovery design docs.

## Files changed

Payment/manual/wallet acceptance, API/worker wiring, configuration, order read
models, Telegram router/parser/views, reconciliation, SQLC queries/generated
code, CI, README, schema/state/transaction/reservation/roadmap docs, and the
expected migration version were updated. No previous migration was edited.

## Migrations

Migration `00018` extends the existing `outbox_events` queue instead of creating
a second queue. It adds order/recipient linkage, claimed/sending stage, leases,
retry/ambiguity/manual/permanent state, Telegram evidence, manual actor/reason/
time, optimistic version, guarded indexes/constraints, and an active-order
unique index. `delivery_attempts` gains safe Telegram classification/evidence,
job linkage, append-only enforcement, and immutable per-attempt events. Down is
guarded against silently deleting delivery history.

## Delivery job lifecycle

Jobs move through `pending`, `processing` (`claimed`, then `sending`),
`retryable_failed`, `ambiguous`, `manual_review`, `permanent_failed`, and
`completed`. Automatic claim excludes ambiguity and all terminal states.
Attempts record a started event and one safe result event; rows are append-only.

## Transactional job creation

External/manual/wallet accepted-payment paths share one transaction core. After
exact encrypted inventory claim, the transaction locks the order, verifies
recipient and exact active reserved mappings, inserts the deduplicated job,
changes `reserving -> delivering`, appends status history and safe audit, then
commits. It neither decrypts nor calls Telegram.

## Backfill strategy

At each bounded worker run, eligible `reserving` orders without a job are
selected in stable order with row locks and `SKIP LOCKED`. The same handoff core
rechecks exact mappings and global deduplication. Repeated/concurrent backfill is
idempotent and produces an audit row only when a job is created.

## Worker claiming

Due pending/retryable jobs are claimed by stable `next_attempt_at/created_at/id`
order using `FOR UPDATE SKIP LOCKED`. The committed lease precedes decryption or
network I/O. Stale `claimed` is safe to retry; stale `sending` becomes ambiguous.
Batch/run/job timeouts, polling ticker, cancellation, graceful shutdown, and a
sanitized panic boundary are configured.

## Secure decryption boundary

The worker revalidates job/order/recipient/mapping/reserved ownership and exact
quantity before using the existing versioned inventory cipher. Plaintext byte
slices exist only while building the single bounded escaped Telegram message
and are cleared best-effort. They are never placed in queue payload, attempts,
audit, session, receipt, callback, logs, metrics, or error details.

## Telegram send classification

Typed adapter results require `ok=true`, matching chat, and a positive message
ID. Explicit 429 honors `retry_after`; 5xx and definite pre-write connect
failure are retryable. Blocked/chat missing/deactivated/invalid content and
recipient failures are permanent. After-write timeout/reset, malformed/invalid
success response, and otherwise uncertain transport outcomes are ambiguous.
The adapter never exposes token URLs or raw responses in errors.

## Success finalization

After confirmed Telegram success, one new transaction locks the job/order and
exact reserved inventory, changes exactly the ordered quantity to `sold`, sets
`sold_order_id`, marks order delivered with timestamp, completes the job with
chat/message evidence, and appends attempt/history/audit. Rollback leaves order
and inventory unchanged.

## Retry policy

Retry scheduling is PostgreSQL-durable and uses bounded exponential backoff,
bounded jitter, maximum delay, and `max(backoff,retry_after)`. Attempts are
finite. No transaction sleeps and no loop retries the same send immediately.

## Permanent failure policy

Permanent recipient/content failure or exhausted attempts marks the job
`permanent_failed`, changes `delivering -> delivery_failed`, and retains exact
inventory reserved for operator review. It is never automatically claimed.

## Ambiguous-send policy

Uncertain sends set `ambiguous`, retain inventory reserved and order delivering,
and remove automatic eligibility. `sending` is persisted before network I/O, so
a stale post-send process is reviewed. If Telegram confirms but DB finalization
fails, a separate best-effort transaction preserves message evidence and
ambiguity; a later stale scan remains conservative if persistence is unavailable.

## Manual review

`/admin` lists pending, processing, retryable, ambiguous, review, and permanent
jobs. Detail exposes redacted order/chat/product/quantity/state/version,
sanitized errors, timestamps, message evidence, and attempt history. It never
loads encrypted inventory or plaintext. Reconciliation counts are read-only.

## Manual redelivery

Retry and mark-delivered are multi-step durable sessions with mandatory reason,
explicit confirmation, session owner/state/version/expiry, job version/state,
active PostgreSQL authorization, update idempotency, and atomic audit. Retry
queues the same durable job for a new attempt generation; Telegram is called
later by the worker. Mark delivered requires a positive message ID and atomically
sells exact inventory without sending Telegram.

## Inventory state guarantees

Delivery starts from `reserved`. Only confirmed send or verified manual
completion changes the exact mapped set to `sold`. Ambiguity, retry, and
permanent failure never release inventory. SQL guards reject wrong owner,
quantity mismatch, invalid order/job state, stale version, and competing active
job. No path implements `sold -> reserved/available`.

## Reconciliation

The service/admin view reports delivering order without job, active job with
wrong order state, completed job without delivered order, delivered quantity
mismatch, sold inventory without completed job, delivered order retaining
reserved inventory, multiple active jobs, stale processing, unresolved
ambiguity, and success evidence without completed job. It does not auto-repair
dangerous cases.

## Plaintext boundaries

Queue payloads contain IDs only. Attempt/audit/history/session/receipt/callback
data contains safe metadata only. Logs avoid outbound messages, crypto metadata,
raw Telegram response, token URL, and panic values. Prometheus labels are
bounded result/status/reason classes, never business IDs or secrets. There is no
customer/admin plaintext export or reveal endpoint.

## Tests

Unit and fake HTTP tests cover state transitions, invalid/manual rules,
backoff/jitter/retry-after/exhaustion, message escaping/size/no truncation,
Telegram success/429/5xx/blocked/chat missing/malformed/reset/timeout and token
redaction. PostgreSQL tests cover transactional creation, backfill, 100-worker
claiming, success, retryable/permanent/ambiguous outcomes, stale claimed/sending
recovery, confirmed-send finalization failure, manual retry/completion,
authorization/reason/evidence, reconciliation, and runtime plaintext searches.

## Commands run

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
git diff --exit-code -- internal/postgres/generated
make lint
make test-integration
INTEGRATION_DATABASE_URL="<local Compose test URL>" \
  go test -tags=integration -count=1 ./tests/integration -run '^TestMigrationCycle$'
docker compose config --quiet
```

## Test results

Baseline before Phase 7 passed all commands above at Phase 6. Final validation
passed unit, race, vet, build, SQLC drift, lint, full PostgreSQL integration,
empty-schema `up -> down-to-zero -> up` through migration 18, and Compose
configuration. No test calls the real Telegram API.

## Security decisions

- PostgreSQL uniqueness, row locks, exact-set updates, and append-only history
  remain the final protection against duplicate job/ownership/sale effects.
- Telegram runs outside every transaction and delivery is never marked before
  trusted success evidence.
- Uncertain transport and post-send finalization failure favor review over
  duplicate credential delivery.
- Manual operations cannot accept a callback-supplied recipient or decrypt;
  reason/evidence/session/version/audit are mandatory.
- The single-message policy rejects oversize before send rather than truncating
  or creating partial-send ambiguity.
- Runtime-generated leakage markers were absent from all tested metadata stores.

## Known limitations

- Delivery supports a single bounded Telegram message; no multipart/document
  strategy exists.
- Telegram offers no implemented delivery-history lookup; ambiguous evidence is
  resolved by the documented operator procedure.
- Manual recipient reassignment is intentionally unsupported.
- Reconciliation is operator-triggered, not a scheduled repair system.
- Production payment provider, bank refund execution, key rotation, broadcast,
  Sheet Sync, production deployment, and full backup/restore runbooks remain.

## Next phase

Phase 8: broadcast queue, Google Sheet Sync, business statistics,
backup/restore, production deployment, monitoring, operational runbooks, and
final system hardening.
