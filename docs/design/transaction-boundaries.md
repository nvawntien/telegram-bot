# Transaction boundaries and idempotency

Network calls are never made while a database transaction is open. Transactions
are short, use explicit row locks, and commit business state together with any
outbox/audit/ledger evidence needed to recover.

## Implemented transaction primitive

Phase 2 provides `postgres.Transactor`, backed directly by `pgxpool.Pool`:

```go
WithinTransaction(ctx, func(context.Context, *generated.Queries) error) error
WithinTransactionOptions(ctx, pgx.TxOptions, func(context.Context, *generated.Queries) error) error
```

The default method uses explicit `pgx.TxOptions{}`; callers may select options
when a future use case proves it needs them. A successful callback commits. A
callback error triggers rollback and remains discoverable through `errors.Is`.
If rollback also fails, both errors are joined. A panic triggers a bounded
rollback with an uncancelled context and then re-panics with the original value.
There is no global transaction or hidden retry/network behaviour.

Real PostgreSQL tests prove commit, callback-error rollback, and panic rollback.
The inventory and outbox tests also prove that two open transactions using
`FOR UPDATE SKIP LOCKED` do not receive the same row.

## Telegram update claim and completion (implemented)

Every update first claims its `telegram_update_receipts` row in a short
transaction. One concurrent claimant changes a new, failed, or stale-processing
row to `processing`; current-processing and completed duplicates return accepted
duplicate semantics without dispatch. Failed work records a bounded error code
and may be reclaimed. Processing older than `TELEGRAM_UPDATE_STALE_SECONDS` may
also be reclaimed after a crash. This is at-least-once delivery with durable
deduplication, not network exactly-once.

Customer read flows complete the receipt before attempting the Telegram reply.
For an admin catalog mutation, one transaction reauthorizes the database admin,
locks and verifies session owner/expiry/version, validates the resource version,
mutates the catalog, writes stable before/after audit JSON, completes the
session, and completes the receipt. Any failure rolls the whole transaction
back. The Telegram confirmation happens after commit and can never roll back
the mutation.

Starting, advancing, and cancelling durable admin sessions each use their own
short request transaction. No transaction remains open while an operator types
the next message. Optimistic session and resource versions reject stale or
concurrent callbacks.

Admin bootstrap upserts the configured Telegram user and ensures a missing
owner admin record in one transaction. It never reactivates an existing revoked
record. Bootstrap is intentionally idempotent and does not create a startup
audit row, avoiding repeated audit noise; runtime authorization always reads
`users` and `admins` from PostgreSQL.

Inventory sections are implemented in Phase 4. Ordering is Phase 5. Payment and
wallet boundaries below are implemented in Phase 6. Delivery is implemented in
Phase 7. Broadcast and Sheet sections remain later-phase targets.

## Create order

One transaction:

1. Lock the PostgreSQL Telegram user; reject missing, banned, or disabled user.
2. Load active product and validate fulfilment type, requested quantity, and
   integer multiplication overflow.
3. Count available inventory as an availability hint only.
4. Insert order using unique `(user_id,idempotency_key)` and payment code.
5. Lock the selected active AES bank account and copy its protected envelope
   and safe display metadata into the order.
6. Insert immutable order-item name/price snapshots.
7. Append initial state history, complete the update receipt, and commit.

The confirmation callback carries a stable flow ID but never price, amount, or
account number. Database uniqueness on `(user_id,idempotency_key)` and payment
reference resolves sequential/concurrent retry races. After commit, the adapter
decrypts only the order snapshot and builds the VietQR URL without network I/O.
Telegram failure cannot roll back the order.

## Confirm provider/manual payment and claim inventory

One serializable-by-locking transaction:

1. Insert or load the unique payment event. A processed duplicate returns its
   existing outcome.
2. `SELECT ... FOR UPDATE` the order and relevant payment/wallet row.
3. Validate current state, expiry, provider signature result, payment code,
   exact amount, and unused provider transaction ID.
4. Insert/update payment and record `paid`, then `reserving`, status history.
5. Claim exactly the required inventory using ordered
   `FOR UPDATE SKIP LOCKED`; create mapping rows.
6. The exact-set SQL updates zero rows when the count is short. Preserve the
   confirmed external payment, move the order to `out_of_stock`, create a review
   case, and mark the event review in this same transaction.
7. On success, insert the unique payment allocation, verify the exact reserved
   mapping, create one delivery job, and transition to `delivering`.
8. Mark the event completed and commit.

The normal path commits no external side effect. Job creation and the order
transition are atomic with acceptance; inventory is not decrypted.

## Telegram delivery

The worker claims due rows in a short committed transaction, then loads and
revalidates the reserved mapping, decrypts temporary byte slices, builds one
bounded escaped message, persists `sending`, and calls Telegram without a
database transaction. Confirmed success starts a new transaction that locks
job/order/inventory, sells the exact quantity, marks order delivered, completes
the job with message evidence, and appends attempt/history/audit.

Retryable, permanent, and ambiguous outcomes use separate finalizer
transactions. A stale `sending` lease is ambiguous. If Telegram succeeds but
finalization fails, a new best-effort transaction preserves evidence and
ambiguity. Manual actions reauthorize admin/session/job versions and commit
resolution, audit, session, and update receipt atomically; they never decrypt or
call Telegram.

## Wallet payment/top-up

Lock `wallet_accounts FOR UPDATE`, look up the unique ledger idempotency key,
calculate the new `int64` balance with overflow checks, reject negative result,
update the materialized balance, and append the immutable ledger row in one
transaction. Wallet order payment claims exact inventory before debit; any
short claim rolls back the entire transaction. Debit, payment/allocation,
history, mappings, and Telegram receipt then commit together. Provider top-up
payment, allocation, cached credit, ledger row, and intent status commit together.

## Generic outbox foundation

The original generic claim flow remains available for non-delivery events. A
delivery event does not use its blanket expired-`processing` reclaim rule:
Phase 7 applies the stage-aware policy in **Telegram delivery** above so a stale
`sending` job becomes ambiguous instead of being sent again.

Claim transaction:

1. Select due pending rows or expired processing leases with
   `FOR UPDATE SKIP LOCKED` and a small batch limit.
2. Set `processing`, worker identity, lease time, and increment attempts.
3. Commit before calling Telegram/Google/provider.

After each network call, use a new completion transaction that locks the event
and verifies the worker lease. A stale worker cannot overwrite a newer claim.
Retryable failure stores a bounded error and calculated `next_attempt_at`;
permanent/exhausted failure marks the event failed and creates the relevant
admin notification intent.

## Delivery success/failure

Telegram delivery calls occur outside a transaction after persisting the
attempt's `sending` stage. After confirmed success, one transaction locks order/event/items,
inserts a succeeded delivery attempt, changes mapped inventory to sold, changes
order to delivered, appends history, and completes outbox. Re-entry sees the
completed/delivered state and does not resend.

On retryable failure, a transaction inserts the attempt and reschedules outbox.
On exhaustion, it also transitions the order to `delivery_failed` and enqueues
an admin notification. Inventory mapping is retained. Uncertain network results,
stale `sending`, and confirmed-send database finalization failures are never
automatically rescheduled; they enter ambiguous/manual review instead.

## Order expiry

A configurable worker claims due `pending_payment` orders ordered by expiry and
ID with `FOR UPDATE SKIP LOCKED`. The transaction rechecks state/time,
transitions each selected row to expired, increments version, appends system
history, and commits. It calls neither Telegram nor payment/inventory adapters.
Notifications and late-payment policy are deferred.

## Customer/admin cancellation

Customer cancellation locks an ownership-scoped order row, validates an
unexpired `pending_payment` state and optimistic version, updates with an owner/
state/time/version guard, appends one history row, completes the update receipt,
and commits. A repeated cancellation returns the existing cancelled outcome
without another history row. Non-pending states cannot use this path.

## Bank administration

Create, edit, activate, and deactivate each reauthorize the PostgreSQL admin,
lock and verify the durable session owner/state/expiry/version, lock/validate
the bank resource where applicable, mutate the encrypted envelope/metadata,
write redacted audit JSON, finish the session, complete the update receipt, and
commit. Account-number encryption happens before the transaction; plaintext is
never stored in a session. There is no hard-delete use case.

## Inventory import

The application validates and parses the message, then creates AES-GCM envelopes
and keyed fingerprints before opening the write transaction. If cryptography
fails, no database write has started. Best-effort clearing shortens the lifetime
of temporary byte slices without claiming process-memory zeroization.

The write transaction reauthorizes the active admin, locks/verifies the durable
session owner/state/expiry/version, verifies an inventory product, inserts each
envelope with unique `(product_id,payload_fingerprint)` and `ON CONFLICT DO
NOTHING`, writes one count-only audit summary, completes the session, and
completes the Telegram receipt. Database, audit, session, or receipt failure
rolls all inserted rows back. Telegram summary delivery occurs after commit.

## Inventory disable and re-enable

One transaction reauthorizes the admin, locks/verifies the toggle session and
redacted item metadata, checks optimistic version and typed transition, updates
`available -> disabled` or `disabled -> available` with a SQL state/version
guard, writes a metadata-only audit, completes the session, and completes the
receipt. Reserved and sold rows cannot pass the guard.

## Inventory claim

The service validates positive quantity and a future bounded deadline. One
transaction locks the existing order, requires `reserving`, locks the matching
order item, and requires its exact product/quantity. Ordered `FOR UPDATE SKIP
LOCKED` changes only `available` authenticated Phase 4 rows to `reserved`;
mapping rows and a safe
system audit are inserted in the same transaction. A short selection or any
mapping/audit error returns an error, rolling every selected row back. Claim
returns IDs only and never decrypts inventory.

## Inventory release and recovery

Release locks the order and accepts only the typed reason implied by
`cancelled`, `expired`, `refunded`, or `out_of_stock`. It locks reserved rows
owned by that order, returns them to available, marks active mappings released
with timestamp/reason, and writes audit in one transaction. No matching rows is
an idempotent zero release; mapping history is never deleted.

Recovery locks the order and first verifies that an expired reservation exists.
Safe terminal outcomes use the same release transaction. Sensitive states keep
the item reserved and insert at most one recovery-required audit marker per
order/status. Time alone never returns inventory to sale.

## Broadcast

Creating a broadcast and audit row is one transaction. Recipient expansion uses
idempotent inserts in bounded batches. Recipient claims are short transactions;
Telegram sends occur outside them. Counters are updated from recipient state,
not trusted increments, so restart/retry cannot inflate totals.

## Sheet sync

Create a unique run by source version. Validate rows outside write transactions.
Apply small batches with row-level result capture; one bad row does not roll
back valid unrelated rows. Complete/partial/failed counts and bounded error JSON
are committed at the end. Sheet stock never creates deliverable inventory
unless rows contain valid encrypted-goods import data through the inventory use
case.
