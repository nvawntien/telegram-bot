# Manual delivery recovery

Open `/admin`, choose **Delivery queue**, then inspect a redacted job and its
attempt history. The view shows order, recipient chat, product snapshot,
quantity, job state/version, safe error classification, timestamps, and known
Telegram message ID. It never decrypts or displays inventory.

For retry, the operator must first verify the message was not delivered, choose
the retry workflow, enter a mandatory evidence reason, and confirm the sensitive
action. For mark delivered, the operator supplies a positive Telegram message
ID and mandatory verification reason, then confirms. A cancel button ends the
session without mutation.

Both actions require a currently active PostgreSQL admin and durable session
owner/state/version/expiry. The final transaction rechecks job version/state,
order and exact inventory ownership, writes manual resolution and append-only
attempt/audit/history, completes the session and Telegram receipt, and commits.
A stale or duplicate callback cannot repeat the effect. Audit/receipt failure
rolls the action back.

Retry only queues the existing job; the admin handler never sends Telegram or
decrypts. Mark delivered sends nothing and is allowed only for ambiguous/review
state with message evidence. Ambiguous inventory is never released. Use the
reconciliation view for anomalies; it is read-only and performs no dangerous
automatic repair.
