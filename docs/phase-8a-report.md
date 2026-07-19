# Phase 8A report

## Completed

Phase 8A implements provider-neutral automatic payment infrastructure without
selecting or guessing a commercial provider contract. Providers can expose
webhook only, transaction API only, or both capabilities. Verified/fetched
transactions converge on one durable normalized event pipeline and reuse the
existing payment acceptance, wallet ledger, inventory claim, delivery outbox,
and Telegram delivery boundaries.

VietQR remains an instruction generator only. QR generation, opening, and
scanning never confirm payment. The happy path requires no admin. Manual payment
confirmation remains an audited fallback.

## Baseline

Before Phase 8A, the main branch was clean and synchronized with its remote.
Unit/race tests, vet, build, SQLC generation/drift, lint, full PostgreSQL
integration, migration cycle through migration 18, and Docker Compose
configuration passed. Phase 1-7 tests were retained.

## Provider capability model and registry

- Typed validated provider names and development/test/production environments.
- Independent webhook, reconciliation, and test-mode capabilities.
- Small `WebhookProvider` and `TransactionAPIProvider` interfaces.
- Immutable composition-root registry with typed capability lookups.
- Duplicate, unknown, disabled, environment-mismatched, and unsupported
  capability registrations/lookups fail closed.
- `signed_json` is a webhook-only HMAC-SHA-256 reference adapter with raw-body
  verification, constant-time comparison, signed timestamp tolerance, static
  acknowledgement, and an explicit production constructor/config guard.

No production provider adapter is included because no official provider
authentication, payload, acknowledgement, pagination, rate-limit, or retry
contract was supplied.

## Generic webhook ingestion

The existing Gin `POST /webhooks/payments/:provider` route now performs typed
provider/capability lookup, bounded one-time raw-body reading, adapter-owned
verification/normalization, durable generic ingestion, and validated
provider-specific acknowledgement. Invalid signatures do not insert rows.
Exact duplicates are accepted idempotently; a changed payload for the same event
ID is rejected and retained as a conflict review without a financial effect.

Normalized evidence includes provider/event/transaction identifiers,
environment, source, direction, integer amount, currency, transfer content,
destination/provider account identity, occurrence time, sanitized metadata,
payload hash, and business fingerprint. Signatures, tokens, raw payloads, and
banking credentials are excluded.

## Account mapping and strict matching

`payment_provider_accounts` maps an exact provider/environment destination
identity to a same-environment encrypted local bank account. Mapping creation
and status changes require active database authorization, durable owned session,
explicit confirmation, optimistic version, masked audit, and update receipt in
one transaction. No last-four auto-linking or Telegram secret entry exists.

The worker accepts only inbound VND evidence with exactly one strict current
order/top-up reference and an active exact destination mapping. It does not use
fuzzy repair, amount-only lookup, sender name, or an embedded substring inside
a larger token. Outbound transactions are durably ignored.

## Automatic acceptance

The existing `PaymentAcceptanceService` remains the only external-payment
business entry. It rechecks transaction uniqueness, environment, reference,
destination bank, amount, currency, target state, and expiry under database
locks. Order payment atomically inserts payment/allocation, claims the exact
encrypted inventory quantity, creates one delivery job, and moves to delivery.
Wallet top-up atomically creates one payment/allocation and one append-only
ledger credit. Review outcomes retain evidence and never settle another target.

## Reconciliation and recovery

Transaction-API capability is optional. Active mappings use durable leased
checkpoints with opaque cursors, last transaction/time, attempt/success/error,
and optimistic version. API calls occur outside transactions with run/request
timeouts and bounded pages/page size. A checkpoint advances only after every
page transaction is durably ingested or confirmed duplicate. Crash/failure
rereads safely; provider outage is degraded and does not stop other worker
loops. Webhook and API sightings of the same transaction converge to one
financial effect.

## Schema and migrations

- `00019_payment_provider_automation`: environments, mappings, checkpoints,
  normalized event fields, environment-scoped uniqueness, and review evidence.
- `00020_bank_account_payment_environment`: local-bank environment isolation.
- `00021_payment_checkpoint_time_order`: removes an invalid checkpoint timestamp
  ordering assumption.
- `00022_payment_checkpoint_down_compatibility`: data-guarded compatibility for
  an empty `up -> down-to-zero -> up` cycle without editing committed migration
  21.

Down migrations refuse to silently remove populated provider/financial
evidence.

## Tests

Unit/HTTP tests cover registry capability matrices, duplicate/disabled/unknown
providers, production guard, HMAC/replay/raw-body mutation, invalid payloads,
provider acknowledgement validation, strict reference boundaries/ambiguity,
API pages/cursors/timeouts/cancellation/errors, checkpoint advance/failure, and
registry races.

PostgreSQL tests cover automatic order-to-delivery, missed-webhook API recovery,
webhook/API convergence, API-only and combined fake providers, outbound ignore,
disabled mapping, test/production isolation, payload conflicts, exactly-once
wallet credit, admin mapping authorization/audit/versioning, 100 concurrent
duplicates, and the complete migration cycle through version 22. No test calls
a real provider or the real Telegram API.

## Security review

- No commercial provider name, endpoint, credential namespace, header, payload,
  or acknowledgement is hardcoded into core implementation.
- Adapters contain transport facts only; handler/reconciliation code cannot
  update orders, wallets, inventory, or delivery directly.
- Provider API and Telegram calls occur outside database transactions.
- Invalid signature creates no event; raw body/signature/token/secret is not
  logged, persisted, audited, returned, or shown in Telegram.
- Event, transaction, allocation, ledger, mapping, and delivery uniqueness plus
  row locks protect repeated/concurrent effects.
- Inbound direction, exact destination mapping, strict reference, integer
  amount/currency, target state, and environment are all rechecked.
- Cursor advance follows durable ingestion; provider outage remains isolated.
- `signed_json` is not enabled by default and cannot start in production.

## Configuration and operations

Generic configuration is documented in `.env.example`: provider list/primary
name/environment, reconciliation enablement/interval/run/request/page bounds,
provider review page size, shared reference format, and worker event controls.
Provider secrets stay in adapter-specific environment namespaces. Admin health
shows bounded capability, mapping, webhook, checkpoint, error, pending-event,
and review data with masked account identity.

## Assumptions and known limitations

- Provider-native stable opaque cursors are preferred; a future adapter owns
  any documented time-window overlap/tie-break strategy.
- The periodic interval is the generic retry boundary. A future concrete
  adapter must apply its official retry-after/rate-limit semantics.
- The generic scheduled reconciliation path is complete. An asynchronous,
  audited Telegram manual reconciliation trigger is deferred until a concrete
  transaction-API adapter and its safe operator bounds are supplied.
- A webhook-only provider cannot recover an event the provider does not retry.
  An API-only provider has poll-interval latency.
- Bank refund execution, chargeback, scraping, bank credentials/OTP, broadcast,
  Sheet Sync, and production deployment are outside Phase 8A.

## Validation commands

Final validation passed unit and HTTP tests, the full race detector, vet, build,
SQLC generation with no drift, lint, the complete PostgreSQL integration suite,
the empty-schema migration cycle through version 22, and Docker Compose
configuration. No external provider or Telegram network call was made.

```text
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
git diff --exit-code -- internal/postgres/generated
make lint
make test-integration
INTEGRATION_DATABASE_URL="<local PostgreSQL test URL>" \
  go test -tags=integration -count=1 ./tests/integration -run '^TestMigrationCycle$'
docker compose config --quiet
```
