# Transaction boundaries and idempotency

Network calls are never made while a database transaction is open. Transactions
are short, use explicit row locks, and commit business state together with any
outbox/audit/ledger evidence needed to recover.

## Create order

One transaction:

1. Upsert/lock the Telegram user as needed; reject banned user.
2. Load active product and validate fulfilment type, requested quantity, and
   integer multiplication overflow.
3. Count available inventory as an availability hint only.
4. Insert order using unique `(user_id,idempotency_key)` and payment code.
5. Insert immutable order-item name/price snapshots and payment instruction.
6. Append initial state history and commit.

After commit, the handler returns/sends the VietQR instruction. If Telegram
fails, a repeated callback returns the same order from the idempotency key.

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
6. If the count is short, roll back the partial claim and in a separate explicit
   resolution transaction record `out_of_stock`, audit, and refund/review intent.
7. Record `delivering` and insert unique `order.delivery_requested` outbox row.
8. Mark event processed and commit.

The normal path commits no external side effect. A crash after commit leaves the
outbox row for any worker.

## Wallet payment/top-up

Lock `wallet_accounts FOR UPDATE`, look up the unique ledger idempotency key,
calculate the new `int64` balance with overflow checks, reject negative result,
update the materialized balance, and append the immutable ledger row in one
transaction. Wallet order payment then enters the same inventory/outbox path as
provider payment. Provider top-up event and credit ledger row commit together.

## Outbox claim and external call

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

Telegram call occurs outside a transaction with an idempotency marker derived
from order/event. After success, one transaction locks order/event/items,
inserts a succeeded delivery attempt, changes mapped inventory to sold, changes
order to delivered, appends history, and completes outbox. Re-entry sees the
completed/delivered state and does not resend.

On retryable failure, a transaction inserts the attempt and reschedules outbox.
On exhaustion, it also transitions the order to `delivery_failed` and enqueues
an admin notification. Inventory mapping is retained.

## Order expiry

A worker claims due `pending_payment` orders in batches with
`FOR UPDATE SKIP LOCKED`. Each transaction rechecks state/time, transitions to
expired, appends history, and records audit/system event. A concurrent payment
holding the same order lock wins deterministically; the second transaction
rechecks and follows late-payment policy.

## Customer/admin cancellation

Lock the order, validate owner or RBAC and current state, transition once,
append history/audit, and optionally enqueue notification. Duplicate callbacks
return the existing cancelled outcome. Paid/reserving/delivering orders require
the refund/recovery use case and cannot use simple cancellation.

## Inventory import

Encrypt each validated line before the transaction. Insert batches with unique
`(product_id,payload_fingerprint)` and `ON CONFLICT DO NOTHING`; record imported
and duplicate counts plus audit in the same transaction. Plaintext is discarded
after encryption and never enters logs/errors.

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

