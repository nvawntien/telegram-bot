# Delivery outbox

`outbox_events` remains the single queue. One `order.delivery_requested` row is
created only after exact payment acceptance and exact encrypted inventory
reservation. Its payload contains the order ID only. A global deduplication key
and active-order unique index are the database authorities against duplicate
jobs.

Payment, manual, and wallet acceptance lock the order, verify the exact active
reserved mapping and Telegram owner, insert the job, append safe history and
audit, then change `reserving -> delivering` in the same transaction. The
worker also performs a bounded, locked, idempotent backfill for eligible Phase 6
orders that remain in `reserving` without a job.

Workers claim stable due rows with `FOR UPDATE SKIP LOCKED`, write a committed
lease and `claimed` stage, then decrypt and call Telegram outside a transaction.
`sending` is persisted before the network call. Stale `claimed` rows may retry;
stale `sending` rows become ambiguous. Completed, ambiguous, review, cancelled,
and permanent rows are never automatically claimed.

Attempts and schedules survive restart. Retry stops at the configured maximum;
a controlled admin retry grants one additional attempt generation without
creating a competing queue row.
