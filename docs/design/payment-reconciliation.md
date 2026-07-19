# Payment reconciliation runbook

`app.ReconciliationService` executes the SQLC read model
`GetFinancialReconciliation`. It reports:

- completed provider events without a payment transaction;
- confirmed payments without allocation or an open review outcome;
- cached wallet balances that differ from signed ledger sums;
- `reserving` orders whose active mapping count differs from ordered quantity;
- review cases without a reason.

A clean report contains zero for every counter. The query is read-only and safe
to run from an operator-only diagnostic command or integration test. Phase 6
does not schedule it automatically and does not mutate or refund anything.

When a counter is non-zero, preserve all evidence, stop automated resolution for
the affected target, inspect redacted payment/event/allocation/ledger/history
rows, and create or retain a payment review. Never edit/delete ledger,
allocation, payment event, or status history rows. There is no banking refund
API in this phase.

Late policy is conservative: expired and cancelled orders are never revived or
claimed automatically, including an event that arrives after expiry but says it
occurred earlier. Evidence and review are retained for an operator decision.
Competing secondary payments are retained as review and never claim inventory or
auto-credit a wallet.
