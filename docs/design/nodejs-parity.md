# Node.js reference analysis and feature parity

Reference inspected: `kentzu213/telegram-shop-bot`, commit `bd2a531` on its
`main` branch. The reference is treated as a product-behaviour specification,
not as an implementation template.

## Behaviour worth preserving

- Vietnamese customer journey: `/start` -> product -> quantity -> bank -> QR.
- Compact Telegram inline keyboards and callbacks for product, quantity, bank,
  cancel, buy-again, and admin quick actions.
- Two product modes: stocked digital delivery and contact-only fulfilment.
- VietQR instructions include exact amount, bank, account, and unique transfer
  content. VietQR creates an instruction only; it never confirms receipt.
- One or two selectable bank accounts.
- Admin product/category/stock/order views, manual payment confirmation,
  automatic delivery when stock exists, and manual fallback when it does not.
- Customer order status/history, balance display, top-up entry point, support,
  and Telegram ID lookup.
- Google Sheet catalog sync, broadcast, and summary statistics.

The text and keyboard layout can evolve, but command names and the short buying
flow should remain familiar.

## Feature parity matrix

| Area | Node.js behaviour actually present | Go target | Phase |
|---|---|---|---|
| User registration | Lazy insert by Telegram ID on `/start`, `/menu`, or checkout | Idempotent upsert, refreshed profile fields, ban status, last-seen timestamp | 3 |
| Customer commands | `/start`, `/menu`, `/product`, `/nap`, `/checkpay`, `/support`, `/myid` | Preserve commands; add `/products` alias and `/orders` customer history | 3, 5, 6 |
| Categories | Listed by product keyboard; admin can only add | Full admin create/update/activate/deactivate; deletion guarded by product references | 3 |
| Products | List/select, contact-only link, add, rename, reprice, toggle, hard delete | Same UX with immutable order snapshots and safe deactivate-first policy | 3 |
| Quantity | Inline choices up to 10, checked against displayed stock | Server-side range validation; real stock is authoritative | 5 |
| Inventory | One SQLite row per account, plaintext `data`, sold flag | One encrypted row per good; available/reserved/sold/disabled state machine | 4 |
| Bank selection | One or two environment-defined banks | Ordered active bank-account catalog with admin management and audit | 5, 8 |
| VietQR | Image URL generated from amount and payment code | Provider-neutral payment instruction; URL parameters escaped and validated | 5 |
| Order creation | Inserts one pending order after a non-locking stock count | Atomic user/product validation, price snapshot, unique payment code, expiry, idempotency key | 5 |
| Customer cancel | Callback updates any supplied order ID; no ownership check | Lock order, verify owner/current state, idempotently transition to cancelled | 5 |
| Manual payment | Single configured admin confirms by command/callback | RBAC-checked application service, exact state validation, audit log | 6 |
| Provider webhook | Not implemented; unused SePay config exists | Signed, size-limited, replay-safe webhook with unique provider event/transaction IDs | 6 |
| Balance top-up | QR is shown but no top-up/payment record is created | Top-up payment record, provider/manual confirmation, immutable wallet ledger | 6 |
| Balance payment | Balance mutators exist but checkout does not use them | Locked wallet debit, non-negative balance, ledger idempotency, same order fulfilment path | 6 |
| Automatic delivery | Marks stock sold and order delivered before Telegram call | Transactional outbox; delivered/sold only after Telegram success | 7 |
| Manual delivery | Admin text stored in RAM flow, order marked delivered before send | Durable admin session; send first, then transactional completion; retry/fallback audit | 7 |
| Order expiry | UI says 15 minutes, database has no `expires_at` worker | Persisted expiry and `pending_payment -> expired` worker transition | 5 |
| Late payment | No handling | Record event, enter `payment_review`, notify admin, never auto-deliver | 6 |
| Admin session | In-process object; lost on restart | Versioned PostgreSQL session with expiry and optimistic locking | 3, 7 |
| Broadcast | Sequential Telegram calls inside command handler | Persisted broadcast/recipients, rate-limited worker, retry-after, resume/cancel/progress | 8 |
| Sheet sync | Periodic CSV overwrite of products; simplistic parser; no run history | Validated adapter, per-row outcome, source idempotency, durable run record | 8 |
| Statistics | Delivered count/revenue, pending, users, available stock | Time-bounded revenue/order/inventory metrics from indexed queries | 8 |
| Audit | Not implemented | Immutable audit rows for every privileged or money/inventory mutation | 2 onward |
| Observability | Console strings, catches broad process errors | `slog`, request/update IDs, Prometheus, liveness/readiness, classified failures | 1 onward |

## Designs that must not be ported

1. SQLite and synchronous calls serialize work and cannot provide the required
   `FOR UPDATE SKIP LOCKED` concurrency semantics.
2. `users.balance` is mutated directly with no ledger, idempotency, or atomic
   non-negative debit.
3. Inventory secrets are stored and displayed as plaintext.
4. Payment confirmation, stock selection, stock sale, and order state changes
   are separate operations. Concurrent confirmation can oversell or duplicate.
5. `confirmAndDeliver` sets `delivered` before Telegram succeeds. A network
   failure permanently loses the delivery job.
6. Telegram and Google calls happen in request handlers or ad-hoc timers rather
   than durable workers.
7. Customer cancellation trusts `callback_data` and does not verify ownership.
8. Payment amount, reference, provider transaction, signature, and replay are
   not verified. Clicking a button is not proof of payment.
9. Admin identity is one environment ID and multi-step state lives in RAM.
10. Broadcast ignores Telegram rate limits and cannot resume after restart.
11. Sheet sync can replace rows by sheet-provided ID, has no run audit, and can
    conflate displayed sheet stock with deliverable encrypted inventory.
12. Product hard deletion and stock deletion can destroy business evidence.
13. Runtime writes `.env`; configuration and mutable business settings are
    mixed and changes are not audited.
14. Broad uncaught-exception handling can continue after corrupted process
    state instead of failing and restarting cleanly.

## Assumptions

- VND is the only currency in the first release; application money is `int64`.
- One order may contain multiple lines in the schema even though Telegram v1
  checkout creates one product line. This avoids a later data migration without
  complicating the initial UX.
- PostgreSQL is the only source of truth. Google Sheets is an import source, not
  an inventory authority.
- Telegram webhook and payment webhook share the `api` process; all retryable
  external side effects belong to the `worker` process.
- A contact-only product does not create an automatically payable order unless
  an admin explicitly converts it to a managed order.
