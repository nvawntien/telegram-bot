# Payment acceptance invariants

`PaymentAcceptanceService` remains the only external-payment business entry
point. Provider adapters, Gin handlers, and reconciliation code never update a
financial target directly.

The PostgreSQL transaction locks the durable event and exact reference target,
checks provider/environment transaction uniqueness, validates destination bank,
amount, currency, expiry, and state, then creates one payment and one allocation.
For an order it claims exactly the ordered inventory quantity and creates the
delivery handoff in the same commit. For a wallet top-up it locks the wallet,
updates its non-negative balance, writes one append-only ledger credit, and marks
the top-up credited in the same commit.

Late/cancelled, mismatched, competing, unmapped, ambiguous, and out-of-stock
evidence is retained in a provider-neutral review case. A review never silently
credits another target. Same event/payload and same transaction/business data
are idempotent; changed evidence creates a conflict review and no second effect.
