# Phase 5 order creation

## Customer flow

The Telegram flow is product detail, preset quantity, active bank selection,
confirmation, committed order, then payment instruction. Typed callbacks carry
only resource IDs, quantity, page/version, and a stable confirmation flow ID.
They never carry price, amount, payment reference, account number, inventory
payload, or an arbitrary status.

The bank-selection update ID becomes the confirmation flow ID. Every click on
that confirmation message therefore derives the same application idempotency
key even though Telegram assigns a new update ID to each click. A user may start
a new purchase flow for the same product later without reusing the old key.

## Transaction

`OrderService.Create` validates positive bounded quantity, then runs the
following through its consumer-owned transaction interface:

1. Lock the PostgreSQL user by Telegram ID and require active status.
2. Return an existing order for `(user_id,idempotency_key)` when present.
3. Load and share-lock product/category; require active inventory delivery.
4. Multiply integer VND unit price by quantity with overflow detection.
5. Count current authenticated `available` inventory rows.
6. Load/share-lock the selected active AES bank account.
7. Generate a bounded random uppercase payment reference and attempt an insert.
8. On a conflict, return the existing idempotent order or retry a reference at
   most eight times.
9. Insert the immutable product and bank instruction snapshots.
10. Append `null -> pending_payment` history and complete the Telegram receipt.
11. Commit before decryption, VietQR generation, or Telegram send.

Database uniqueness on payment reference and `(user_id,idempotency_key)` is the
race authority. `ON CONFLICT DO NOTHING` keeps a reference collision usable
inside the current transaction; the service distinguishes an idempotency winner
from a reference collision and never loops indefinitely.

## Ownership and cancellation

List, detail, and lock-for-cancel queries join the order to the Telegram user in
PostgreSQL. Missing and foreign orders both map to the same customer-facing
not-found result. Cancellation locks the ownership-scoped row and applies an
additional ID/user/status/expiry/version conditional update. Only an unexpired
`pending_payment` order can become `cancelled`; one history row and receipt
completion commit with the status. A repeated cancel returns the existing
cancelled result without another transition.

## Availability is not reservation

The count rejects a requested quantity that is unavailable at transaction time.
It does not lock inventory for the order, update inventory status, or insert an
active `order_inventory_items` mapping. Multiple pending orders can observe the
same available rows. Atomic claim happens only after accepted payment in the
next phase.
