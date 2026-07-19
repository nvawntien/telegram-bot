# Phase 4 completion report

## Completed

- Added authenticated, versioned application-layer encryption for opaque
  inventory and a concurrency-safe keyed duplicate fingerprint.
- Added redacted PostgreSQL-authorized inventory administration through the
  existing Gin Telegram webhook, typed router, durable sessions, application
  services, transaction runner, SQLC, and PostgreSQL.
- Added exact atomic claim, historical idempotent release, conservative expiry
  recovery, metrics, and real PostgreSQL contention/security tests.
- Kept order creation, payment/VietQR/wallet, customer delivery, broadcast,
  Sheet synchronization, plaintext export, and full key rotation out of scope.

## Files added

- `migrations/00015_encrypted_inventory.sql`.
- `internal/inventorycrypto/cipher.go` and crypto tests.
- `internal/app/inventory_service.go`,
  `internal/app/inventory_reservation_service.go`, inventory domain policy, and
  their tests.
- `internal/postgres/inventory_admin_store.go` and
  `internal/postgres/inventory_reservation_store.go`.
- `tests/integration/phase4_test.go`.
- Inventory encryption, administration security, reservation design documents,
  and this report.

## Files changed

- API composition, config, `.env.example`, Compose, Telegram parser/router/views,
  metrics, existing unit/integration expectations, README, schema/transaction/
  roadmap documents, SQLC queries, and checked-in generated SQLC code.

## Migrations

Migration 00015 adds nonce, encryption format, positive key version, importing
admin, and optimistic version to `inventory_items` while retaining Phase 2
`legacy-v0` compatibility. Existing ciphertext/fingerprint/status constraints
remain authoritative. New constraints validate the AES-GCM envelope and mapping
release state. Partial indexes support reserved-order lookup and one active
mapping per item; released mappings retain history.

The down migration refuses silent loss when Phase 4 encrypted rows or released
mappings exist. An empty schema passes up to version 15, down-to-zero, and up to
15 again. Existing Phase 3 rows remain migratable.

## Encryption design

New rows use AES-256-GCM with a fresh 12-byte `crypto/rand` nonce. Deterministic
versioned binary associated data binds format purpose, key version, and product
ID; mutable lifecycle fields are excluded. Authentication fails for a wrong
key, key version, product, nonce, or modified ciphertext. Empty payloads are
rejected and cryptographic errors are generic.

## Key derivation

API startup requires a standard-base64 value decoding to exactly 32 bytes and a
positive current key version. HKDF-SHA-256 derives independent 32-byte subkeys
using `telegram-shop/inventory/encryption/v1` and
`telegram-shop/inventory/fingerprint/v1`. PostgreSQL never stores a key. A
keyring constructor permits future multi-version reads, but Phase 4 config uses
one current version and has no rotation command.

## Fingerprint semantics

HMAC-SHA-256 binds a versioned input containing product ID, payload length, and
opaque payload. Equal payload is duplicate only within the same product. The
database unique `(product_id,payload_fingerprint)` constraint plus `ON CONFLICT
DO NOTHING` resolves concurrent imports without check-then-insert races.

## Inventory administration

Active PostgreSQL admins can view paginated per-product available/reserved/sold/
disabled/total counts, list redacted metadata, start/cancel an import, disable
only available items, and re-enable only disabled items. Read and mutation
authorization is database-backed. Toggle SQL uses status plus optimistic
version guards. Reserved/sold rows cannot be disabled, and no item is hard
deleted.

Callbacks are typed, numeric-validated, UTF-8 validated, bounded to 64 bytes,
and contain safe IDs/versions/page/target only. Telegram handlers do not import
SQLC, open transactions, encrypt, authorize from raw config, or decide status
transitions.

## Bulk import behavior

One LF-delimited line is one item. A terminal CR is removed; every other byte is
preserved. Blank/whitespace-only lines are ignored and counted rejected.
Invalid UTF-8, no usable item, invalid limits, item-count overflow, per-item
overflow, or total-byte overflow rejects the request. Embedded newline payloads
are unsupported.

Cryptography completes before the database write transaction; a crypto failure
therefore leaves no persisted partial work. The transaction reauthorizes the
admin, verifies/locks durable session and product, inserts envelopes, skips
duplicates, writes a count-only audit, completes session, and completes receipt.
Database/audit/session/receipt failure rolls everything back. Telegram summary
failure after commit does not roll back, and a completed duplicate update is not
dispatched again. Temporary byte slices are overwritten best-effort without a
claim of complete Go process-memory zeroization.

## Claim transaction

The service validates IDs, positive exact quantity, and a future deadline
within its configured maximum. The transaction locks the existing order,
requires `reserving`, locks a matching order item/product with exact quantity,
claims stable available `aes-256-gcm-v1` rows with `FOR UPDATE SKIP LOCKED`, inserts mappings,
checks exact count, writes safe audit metadata, and commits. A shortage or later
failure rolls all row updates and mappings back. Claim returns item IDs only and
never decrypts.

## Release transaction

Release locks the order and accepts only the typed reason implied by a safe
terminal status. It locks reserved rows owned by that order, returns them to
available, marks active mappings released with timestamp/reason, writes safe
audit metadata, and commits. No match is an idempotent zero release. Mapping
history is retained; another order and sold rows are untouched.

## Reservation recovery policy

Cancelled, expired, refunded, and out-of-stock orders can release with their
matching typed reason. Pending-payment must not own inventory.
Payment-review, paid, reserving, delivering, delivered, and delivery-failed
orders retain inventory. An expired reservation is not released on time alone;
recovery locks the order and either applies the safe release policy or writes
one redacted recovery-required audit marker per order/status.

An automatic sweeper is deferred because Phase 4 has no payment/delivery flow
that can safely resolve sensitive expired reservations. A process crash after
claim leaves durable reservation/mapping state for explicit recovery.

## Security guarantees

- PostgreSQL stores AES-GCM ciphertext, nonce, format/key metadata, and keyed
  fingerprint, never new Phase 4 plaintext or the encryption key.
- Separate derived subkeys prevent raw key reuse; every encryption uses a new
  random nonce and versioned AAD.
- Audit/session/receipt/callback/response/log/metric boundaries use safe IDs,
  versions, statuses, counts, correlation, operations, and bounded results.
- Database uniqueness resolves duplicate-import races; row locks, exact-count
  rollback, and partial mapping uniqueness prevent oversell/partial claim.
- Active PostgreSQL RBAC, durable ownership/expiry/version sessions, resource
  state/version guards, and completed update receipts protect admin mutations.
- External Telegram calls occur after commit and outside transactions.

## Plaintext boundaries

Telegram supplies the import message in memory. The router does not log or echo
it and passes a temporary byte copy to the application service. Plaintext is
parsed, fingerprinted, encrypted, then best-effort overwritten. It is never
placed in a database column, audit JSON, session JSON, receipt, callback,
metric, structured log, error string, or committed fixture. Tests inspect these
observable boundaries with runtime-generated opaque values; they do not claim
to scan or zeroize all process memory.

## Tests

- Crypto: round trip, ciphertext leakage, nonce/ciphertext randomness, wrong
  key/version/product, ciphertext/nonce tamper, empty payload, invalid key,
  random-source failure, keyed fingerprint semantics, and subkey separation.
- Parser/domain/Telegram: LF/CRLF, blank lines, byte/count/total/UTF-8 limits,
  byte preservation, typed transitions/release policy, callback validation,
  durable inventory states, redacted formatting, callback length, and generic
  error/output behavior.
- PostgreSQL: raw encrypted persistence/decrypt, no plaintext column/boundary,
  duplicate and concurrent import, duplicate update, durable session,
  non-admin/revoked denial, toggle guards, audit rollback, exact/short/excluded
  claim, mapping rollback, 100 concurrent last-item claims, concurrent multi-item
  claims, safe/unsafe/idempotent/concurrent release, mapping history, sold-item
  protection, recovery hold/release, and Telegram failure after commit.
- Existing Phase 1-3 unit, HTTP, Telegram, migration, constraint, transaction,
  and integration tests remain enabled.

## Commands run

```text
git status --short
git branch --show-current
git log --oneline -n 15
go test ./...
go test -race ./...
go vet ./...
go build ./...
make sqlc
make lint
make test-integration
docker compose config --quiet
git diff --exit-code
```

Migration validation uses the isolated integration harness for empty up,
down-to-zero, and up again. Git identity/history/trailer checks are run after
the final commit.

## Test results

The clean Phase 3 baseline passed unit, race, vet, build, SQLC regeneration with
no drift, lint, full PostgreSQL integration, and Compose validation before Phase
4 changes. The Phase 4 focused and full integration suites pass, including real
100-goroutine contention. The entire final command matrix is rerun immediately
before the documentation commit.

## Known limitations

- No customer order creation, payment provider, VietQR, wallet, automatic
  delivery/decryption, broadcast, Sheet sync, or plaintext inventory export.
- No full key rotation/backfill command or external secret-manager integration.
- Legacy `legacy-v0` rows remain redacted but are not decryptable by the Phase 4
  cipher without a future explicit migration adapter.
- Reservation recovery is an explicit primitive; no automatic sweeper runs.
- Post-commit Telegram summaries have no response outbox/retry.

## Next phase

Phase 5 can build idempotent customer order creation, guarded order history,
expiry, and VietQR instructions on the existing claim/recovery primitives. It
must preserve the rule that payment/delivery state owns the order lock and that
no inventory plaintext is delivered before a durable fulfilment transaction.
