# Phase 5 order expiry

The worker owns one cancellable periodic job. `ORDER_EXPIRY_INTERVAL` controls
the ticker, `ORDER_EXPIRY_BATCH_SIZE` bounds each transaction, and
`ORDER_EXPIRY_RUN_TIMEOUT` bounds a run. The worker configuration loader needs
PostgreSQL and these controls; it does not require Telegram, VietQR, admin, or
encryption configuration.

Each run selects only:

```text
status = pending_payment AND expires_at <= now
```

Selection is ordered by `expires_at, id`, uses `FOR UPDATE SKIP LOCKED`, and is
rechecked by the guarded update. This permits multiple worker processes without
double transition or blocking behind another claimed batch. For every updated
row, the same transaction appends `pending_payment -> expired` history with
actor `system` and reason `payment_window_elapsed`.

Paid, payment-review, reserving, delivering, delivered, cancelled, refunded,
out-of-stock, and delivery-failed orders are not selected. The job does not call
Telegram, inspect payments, claim/release inventory, create delivery work, or
send a customer notification. Notification/outbox and late-payment
reconciliation remain deferred.

Structured logs include worker, operation, result, batch size, and duration.
Prometheus exposes expiry runs, expired-order counts, batch size, and duration
without user or order IDs as labels.
