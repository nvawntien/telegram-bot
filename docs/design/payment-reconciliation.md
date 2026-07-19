# Payment reconciliation

## Provider transaction recovery

The optional provider reconciliation job runs only when enabled and only for
active mappings whose registered provider exposes the transaction-API
capability in the same environment. API calls use per-request and per-run
timeouts, opaque adapter-owned cursors, bounded pages/page size, context
cancellation, and no open database transaction.

Each mapping owns one durable leased checkpoint. A page is fetched and every
normalized transaction goes through the same generic ingestion service as a
webhook. The version/lease-guarded cursor advances only after every transaction
is durably inserted or confirmed as an exact duplicate. Crash or ingestion
failure leaves the page eligible to be read again. The periodic worker interval
provides bounded retry without an immediate busy loop; typed provider error
codes preserve auth/temporary/rate-limit classification without tokens or raw
responses.

Provider failure is degraded: payment-event, order-expiry, delivery, and
Telegram-update loops continue. A webhook-only provider has no checkpoint. An
API-only provider has polling latency. A combined provider uses webhook as the
real-time path and API reconciliation for missed-event recovery.

The Telegram provider health view reports capabilities, active mappings, last
webhook, reconciliation attempt/success, last transaction, bounded error code,
pending events, and open reviews. Account identifiers are masked. Scheduled
reconciliation is available now; an asynchronous audited Telegram manual
trigger is deferred until a concrete transaction-API adapter is selected.

## Financial consistency read model

`app.ReconciliationService` executes the SQLC read model
`GetFinancialReconciliation`. It reports:

- completed provider events without a payment transaction;
- confirmed payments without allocation or an open review outcome;
- cached wallet balances that differ from signed ledger sums;
- `reserving` orders whose active mapping count differs from ordered quantity;
- review cases without a reason.

A clean report contains zero for every counter. This read-only consistency
model is separate from provider transaction fetching and does not mutate or
refund anything.

When a counter is non-zero, preserve all evidence, stop automated resolution
for the affected target, inspect redacted payment/event/allocation/ledger/
history rows, and create or retain a payment review. Never edit/delete ledger,
allocation, payment event, or status history rows. There is no banking refund
API in this phase.

Late policy is conservative: expired and cancelled orders are never revived or
claimed automatically, including an event that arrives after expiry but says it
occurred earlier. Evidence and review are retained for an operator decision.
Competing secondary payments are retained as review and never claim inventory
or auto-credit a wallet.
