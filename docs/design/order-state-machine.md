# Order state machine

Only the order application service may transition state. Telegram callbacks,
HTTP handlers, workers, and repositories request a use case; none writes a
status directly. Every successful transition appends `order_status_history` in
the same transaction.

Order creation creates `pending_payment` and exposes only
`pending_payment -> cancelled` to a customer. The expiry service alone applies
`pending_payment -> expired`. `cancelled` and `expired` are terminal for the
customer flow. Phase 6 never revives an expired/cancelled order automatically;
it retains a review case without breaking a terminal transition.

## States

| State | Meaning |
|---|---|
| `pending_payment` | Instruction exists and valid payment has not been accepted. |
| `payment_review` | A late, mismatched, ambiguous, or otherwise non-automatic payment needs an operator decision. |
| `paid` | Exact payment is accepted and persisted. Usually transient inside the confirmation transaction. |
| `reserving` | Payment is accepted and the service is claiming concrete inventory rows. |
| `delivering` | Required inventory is mapped to the order and a durable delivery event exists. |
| `delivered` | Telegram delivery succeeded; inventory was marked sold atomically afterward. |
| `expired` | Payment window elapsed before accepted payment. |
| `cancelled` | Customer owner or authorized admin cancelled before payment acceptance. |
| `out_of_stock` | Payment was accepted but an atomic claim could not obtain the full quantity. |
| `delivery_failed` | Delivery exhausted automatic retries; mapping remains for manual recovery. |
| `refunded` | A recorded compensation/refund completed. |

## Allowed transitions

```text
pending_payment --> paid --> reserving --> delivering --> delivered
       |              |          |             |
       |              |          |             +--> delivery_failed
       |              |          +--> out_of_stock --> refunded
       |              |          +--> out_of_stock --> reserving (admin retry after restock)
       |              +--> payment_review
       +--> expired --> payment_review (late provider event)
       +--> cancelled
       +--> payment_review

payment_review --> paid          (operator accepts exact reconciled payment)
payment_review --> refunded      (funds returned)
payment_review --> cancelled     (no funds captured / rejected event)
delivery_failed --> delivering   (idempotent manual retry)
delivery_failed --> refunded
delivered --> refunded           (explicit exceptional compensation only)
```

`paid` and `reserving` remain explicit for audit and recovery reasoning even
when a normal confirmation transaction records both transitions. Phase 7 now
creates the delivery job and applies `reserving -> delivering` in that same
acceptance transaction. Worker success applies `delivering -> delivered`;
terminal failure applies `delivering -> delivery_failed`; verified manual retry
may restore `delivery_failed -> delivering`. No other transition is allowed.

## Guards

- Customer cancellation requires `orders.user_id` to match the authenticated
  Telegram user and current state `pending_payment`.
- Admin transitions require active admin role/permission and an audit record.
- Payment confirmation locks the order and reviews amount/reference mismatch,
  reused provider transaction IDs, duplicate events, expired instructions, and
  invalid current state.
- Inventory claim must return exactly the sum of order-item quantities. Partial
  claims are rolled back.
- `delivering` requires durable order-to-inventory mappings and a unique pending
  `order.delivery_requested` outbox row.
- `delivered` requires evidence of a successful Telegram send. The transition,
  inventory sale, delivery attempt, and outbox completion commit together.
- Ambiguous delivery leaves the order `delivering`; the delivery job and
  customer label express review without inventing a duplicate order state.
- `refunded` requires a confirmed provider refund or wallet credit ledger entry;
  an operator button alone cannot assert completion.
- Repeated calls with the same idempotency key return the existing result and do
  not append another transition.

## Inventory relationship

- Order creation only checks indicative availability; it does not reserve.
- Order creation never claims or creates an active order-inventory mapping.
- Valid Phase 6 payment acceptance claims an exact set with `FOR UPDATE SKIP LOCKED`.
- A reserved item cannot belong to two orders because the row status/reference
  constraint and unique mapping are enforced in PostgreSQL.
- Delivery failure retains the reservation/mapping. An explicit refund or admin
  recovery use case decides whether to release it; a timer never silently puts
  exposed credentials back into available stock.
