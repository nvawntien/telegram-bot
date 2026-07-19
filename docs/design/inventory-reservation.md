# Inventory reservation

## Claim

Claim is an internal Phase 4 primitive; it does not create an order, accept a
payment, or decrypt/deliver an item. It requires an existing `reserving` order,
a matching order item/product, exact positive quantity, and a future deadline
within the caller-configured maximum TTL.

The transaction locks the order, locks the matching order item, selects stable
`available` `aes-256-gcm-v1` rows ordered by creation time and ID with `FOR UPDATE SKIP LOCKED`,
marks them reserved for that order/deadline, inserts active historical mappings,
and writes safe audit metadata. The caller requires the exact count. A shortage
or mapping/audit failure rolls every row back, so partial claim and duplicate
active ownership are impossible.

## Explicit release

Release locks the order and only accepts the typed reason implied by its state:

| Order status | Decision |
|---|---|
| `cancelled` | Release as `order_cancelled`. |
| `expired` | Release as `order_expired`. |
| `refunded` | Release as `order_refunded`. |
| `out_of_stock` | Release as `order_out_of_stock`. |
| `pending_payment` | Reject; this state must not own inventory. |
| `payment_review` | Hold and require recovery review. |
| `paid` | Hold and require recovery review. |
| `reserving` | Hold; another workflow step may still own the transaction. |
| `delivering` | Hold; never make potentially paid stock available. |
| `delivered` | Hold; sold/delivered history must not be recycled. |
| `delivery_failed` | Hold for explicit operator/refund policy. |

Only rows still `reserved` by that order are returned to `available`. Active
mapping rows become `released` with timestamp and reason; no mapping is deleted.
A repeated or concurrent release sees no matching reservation and succeeds with
zero released items. Sold rows and rows owned by another order are untouched.

## Expiry and recovery

`reserved_until <= now` is evidence for recovery, not authority to release.
Recovery locks the order and counts its expired reserved rows. Safe terminal
states use the explicit release operation. Sensitive states retain the
reservation and append at most one
`inventory.reservation_recovery_required` audit marker for the unchanged
order/status. This also covers a process crash after a committed claim and
before a later payment/delivery step: the mapping remains durable and recovery
can inspect it without risking resale.

An automatic release sweeper remains deliberately deferred. Phase 7 can now
classify delivery state, but age alone still cannot prove that an ambiguous or
failed delivery is safe to resell. A future operator/refund workflow may
batch-lock candidates only after an explicit safe terminal transition.

## Delivery ownership

Phase 7 retains the active mapping while delivery is pending, processing,
retryable, ambiguous, in review, or permanently failed. Decryption does not
change ownership. Only confirmed Telegram success, or audited manual completion
with message evidence, changes the exact reserved set to `sold` and records its
order. Ambiguous/manual retry never releases or reassigns rows. A count mismatch
aborts finalization and leaves the set reserved for reconciliation.
