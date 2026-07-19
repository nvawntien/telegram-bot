# Wallet ledger

There is exactly one `wallet_accounts` row per user. It is lazy-created under a
unique `user_id` constraint. `balance_vnd` is a cached non-negative balance;
`wallet_ledger_entries` is the append-only audit source of truth and uses signed
amounts: positive credit/refund, negative debit, non-zero adjustment.

Every mutation locks the wallet row, applies a guarded balance delta, inserts a
unique `(account_id,idempotency_key)` ledger entry with `balance_after_vnd`, and
commits both or neither. Reconciliation compares cached balance to ledger sum.

Top-up creation stores no credit. It creates an idempotent intent with a unique
reference, expiry, amount limits, and encrypted bank snapshot, then generates a
VietQR instruction after commit. Exact accepted payment locks the intent and
wallet, inserts payment/allocation, credits cached balance and ledger once, and
marks the intent credited in one transaction. Expired or mismatched payment is
reviewed and never auto-credited.

Wallet order payment verifies ownership/state/expiry and balance, claims exact
inventory, then commits debit ledger, cached balance, payment/allocation,
`paid -> reserving` history, mappings, and Telegram receipt. A short claim or
insufficient balance rolls the entire transaction back. PostgreSQL contention
tests issue 100 simultaneous debits and prove no negative balance or ledger drift.

Admin adjustments require active database authorization, a durable owned
session, positive amount, credit/debit choice, reason, idempotency key, ledger
entry, redacted audit, session completion, and update receipt in one transaction.
