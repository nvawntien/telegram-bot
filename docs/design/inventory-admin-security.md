# Inventory administration security

## Authorization and navigation

The Gin webhook routes a Telegram update to the typed router, inventory
application service, transaction-backed PostgreSQL adapter, and SQLC. Handlers
never call SQLC, encrypt, open transactions, or authorize from raw config.
Every inventory read and mutation checks current PostgreSQL user/admin state;
revoked or banned administrators are rejected.

The `/admin` inventory surface provides paginated product status counts,
redacted item metadata, import, available-item disable, disabled-item re-enable,
and cancellation. Callback data contains only action, IDs, page, expected
versions, and a boolean target. It is UTF-8 validated, typed, numeric-validated,
and limited to Telegram's 64-byte maximum. It never contains protected data.

## Redacted output

Views may include internal item/product ID, status, creation date, bounded order
reference, reservation deadline, key version, and optimistic version. They do
not include decrypted prefixes/suffixes, ciphertext, nonce, fingerprint, exact
payload length, or a reveal/export action. Claim also returns IDs only. Plaintext
delivery is outside Phase 4.

## Import parsing and atomicity

One LF-delimited line is one opaque item. A terminal CR is removed so CRLF works;
all other leading, trailing, internal, case, and Unicode bytes are preserved.
Empty and whitespace-only lines are ignored and counted rejected. Invalid
UTF-8, no usable item, maximum item count, per-item bytes, total bytes, or
invalid limits reject the request. Embedded newline payloads are unsupported.

The durable import session stores product ID, action, expiry, and version only.
It never stores the Telegram message. Encryption failure causes no write;
database, audit, session, or receipt failure rolls the transaction back.
Duplicates are skipped and counted. A committed import remains committed if the
post-transaction Telegram summary fails, and the completed receipt prevents a
duplicate update from importing again.

Audit rows contain product ID, requested/inserted/duplicate/rejected counts,
key version, request ID, and update ID. Logs and metrics use bounded operation,
result, duration, version, ID, and count metadata only. No protected value or
unkeyed content hash is logged or labeled.
