# Payment ingestion and acceptance

## Provider boundary

`internal/payment.Registry` exposes typed webhook and transaction-API lookups
from immutable startup registration. Each adapter declares its name,
environment, enabled state, and webhook/reconciliation/test capabilities. An
adapter implements only supported capabilities and emits one provider-neutral
event; provider DTOs never cross into the application core.

The repository currently ships only `signed_json`, a webhook-only controlled
private/test contract. It is disabled unless `PAYMENT_PROVIDERS` contains
`signed_json`, requires a dedicated secret, and is rejected in the production
provider environment. It does not claim compatibility with a bank or payment
company.

The contract requires JSON fields `event_id`, `transaction_id`,
`payment.received`, exact `reference`, positive integer `amount_vnd`, `VND`, a
Unix `timestamp`, and RFC3339 `occurred_at`. The caller sends the same Unix value
in `X-Payment-Timestamp` and an HMAC-SHA-256 hex digest of the unmodified body in
`X-Payment-Signature`. The adapter compares digests in constant time and rejects
timestamps outside the configured tolerance. The timestamp is inside the signed
body, so changing either representation invalidates the request.

## HTTP acknowledgement

`POST /webhooks/payments/:provider` validates the provider name, performs a typed
enabled webhook-capability lookup, bounds and preserves the raw body, delegates
verification/parsing, and stores the event before returning the adapter's
validated static acknowledgement. The generic Gin handler contains no provider
DTO switch, SQLC, order, wallet, inventory, or delivery logic. It never logs raw
body, signature, secret, or full account identifier.

- `signed_json` new or exact duplicate durable event: `202 Accepted`;
- unknown provider: `404`;
- bad signature or replay timestamp: `401`;
- malformed normalized event: `400`;
- same event ID with a different body hash: `409`;
- temporary ingestion failure: `503`, allowing provider retry.

## Durable processing and decisions

The worker claims `received` events and stale `processing` leases with ordered
`FOR UPDATE SKIP LOCKED`, bounded batch/run time, attempts, exponential backoff,
and a max-attempt terminal state. Outbound transactions are retained and
completed as ignored. For inbound rows, the worker uses strict finite reference
extraction and exact active provider/environment destination mapping before
entering the existing acceptance service. Business decisions are non-retryable:
unknown reference, amount/currency mismatch, late/cancelled target, competing
payment, transaction conflict, expired top-up, and post-payment stock shortage
become durable review cases.

Exact pending-order payment locks the event/order, inserts the unique payment,
records `pending_payment -> paid -> reserving`, claims an exact inventory set,
adds mappings/allocation/audit, creates the unique delivery job, transitions to
`delivering`, completes the event, and commits once. No inventory is decrypted
and no Telegram request occurs in that transaction.

If real external money was received but stock is short, the exact-set query
updates zero inventory rows. The transaction preserves a confirmed payment,
moves the order `reserving -> out_of_stock`, creates a review, and marks the
event review. It does not refund or credit the wallet automatically. In contrast,
wallet order payment returns an error and rolls back debit, payment, order state,
and all inventory work when stock is short.

Manual confirmation requires a PostgreSQL-authorized admin, owned/versioned
durable session, mandatory manual transaction ID, exact reference/amount/
currency/time, audit, receipt completion, and session completion. It calls the
same `acceptPaymentWithinTransaction` core as worker events.
