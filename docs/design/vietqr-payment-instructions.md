# VietQR payment instructions

Phase 5 produces a deterministic image URL; it does not call a VietQR network
endpoint. The adapter parses and validates one configured HTTPS base URL and
template, resolves the BIN/account/template image path, and uses `url.Values`
for amount, transfer content, and Unicode account name.

Input validation requires a six-digit bank BIN, a 4–34 digit account number,
positive integer VND amount, uppercase alphanumeric payment reference, positive
order ID, non-empty names, and an expiration timestamp. Equal input produces
equal output. Special characters are query-escaped rather than concatenated.

Bank account numbers are stored as AES-256-GCM envelopes. The order transaction
copies that envelope and safe display metadata into the order. After commit,
the application decrypts the immutable order snapshot only long enough to build
the instruction. Editing or deactivating a bank account therefore does not
change an old order's instruction.

The Telegram text explicitly says the order is pending and the QR is only an
instruction. There is no paid button, transaction-history lookup, provider
webhook, manual confirmation, payment record, wallet effect, inventory claim,
or delivery effect in this phase. Normal structured logs omit the QR URL and
full account number; metrics use bounded result labels only.
