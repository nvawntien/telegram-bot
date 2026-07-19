# Inventory encryption

## Envelope

Phase 4 treats each line as opaque secret bytes and protects it at the
application boundary with AES-256-GCM. The configured standard-base64 value
must decode to exactly 32 bytes. Every protection operation obtains a fresh
12-byte nonce from `crypto/rand`; ciphertext includes the GCM authentication
tag. Format `aes-256-gcm-v1`, nonce, ciphertext, positive key version, and safe
operational key ID are stored per row. PostgreSQL never stores the master key.

Associated data is deterministic binary data with a fixed
`telegram-shop/inventory/aad/v1` prefix, delimiter, big-endian key version, and
big-endian product ID. Mutable status, timestamps, and reservation/order fields
are excluded. Moving ciphertext to another product, changing key version,
nonce, or ciphertext causes authenticated decryption to fail.

## Key derivation and fingerprint

HKDF-SHA-256 expands the configured master key into two 32-byte subkeys with
independent labels:

- `telegram-shop/inventory/encryption/v1`
- `telegram-shop/inventory/fingerprint/v1`

The first key is used only by AES-GCM. The second keys HMAC-SHA-256 over a
versioned input containing product ID, payload length, and payload. Therefore
equal payloads are duplicates only within the same product. Random GCM nonces
may produce different ciphertext, while the keyed fingerprint remains stable.
The database unique `(product_id,payload_fingerprint)` constraint resolves
concurrent duplicate inserts; the application does not use check-then-insert.

## Key operations

`INVENTORY_ENCRYPTION_KEY_VERSION` selects the current write version. The
cipher supports an explicit version-to-key map for future reads during
rotation, but Phase 4 runtime config supplies one version and provides no
rotation command. Operators must never replace a key under an existing version.
Keys must be backed up separately from the database with access controls and
version labels. Losing a matching key permanently loses decryption capability.

## Memory and failure limits

Import uses temporary byte slices and overwrites them on a best-effort basis
after encryption. Go strings, compiler/runtime copies, kernel buffers, and
Telegram's infrastructure cannot be guaranteed zeroized. The security claim is
limited to application-controlled persistence and observable outputs. Crypto
errors returned to Telegram are generic and never include key, nonce,
ciphertext, fingerprint, or input bytes.

Legacy `legacy-v0` rows remain visible as redacted metadata for migration
compatibility. The Phase 4 cipher refuses to decrypt an unversioned legacy
envelope.
