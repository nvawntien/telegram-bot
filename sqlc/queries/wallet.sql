-- name: EnsureWalletAccount :one
INSERT INTO wallet_accounts (user_id)
VALUES (sqlc.arg(user_id))
ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING *;

-- name: GetWalletByTelegramUser :one
SELECT wallet.* FROM wallet_accounts AS wallet
JOIN users ON users.id = wallet.user_id
WHERE users.telegram_user_id = sqlc.arg(telegram_user_id);

-- name: LockWalletAccount :one
SELECT * FROM wallet_accounts WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: UpdateWalletBalance :one
UPDATE wallet_accounts
SET balance_vnd = balance_vnd + sqlc.arg(amount_vnd), version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'active'
  AND balance_vnd + sqlc.arg(amount_vnd) >= 0
RETURNING *;

-- name: InsertWalletLedgerEntry :one
INSERT INTO wallet_ledger_entries (
    account_id, entry_type, amount_vnd, balance_after_vnd,
    reference_type, reference_id, idempotency_key
) VALUES (
    sqlc.arg(account_id), sqlc.arg(entry_type), sqlc.arg(amount_vnd),
    sqlc.arg(balance_after_vnd), sqlc.arg(reference_type),
    sqlc.arg(reference_id), sqlc.arg(idempotency_key)
)
RETURNING *;

-- name: GetWalletLedgerByIdempotency :one
SELECT * FROM wallet_ledger_entries
WHERE account_id = sqlc.arg(account_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ListWalletLedger :many
SELECT * FROM wallet_ledger_entries WHERE account_id = sqlc.arg(account_id)
ORDER BY id DESC LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: SumWalletLedger :one
SELECT COALESCE(sum(amount_vnd), 0)::bigint FROM wallet_ledger_entries
WHERE account_id = sqlc.arg(account_id);

-- name: CreateWalletTopup :one
INSERT INTO wallet_topup_intents (
    user_id, wallet_account_id, amount_vnd, payment_reference,
    idempotency_key, expires_at, bank_account_id, bank_bin_snapshot,
    bank_name_snapshot, bank_display_name_snapshot, bank_account_name_snapshot,
    encrypted_account_number_snapshot, account_number_nonce_snapshot,
    account_encryption_format_snapshot, account_key_version_snapshot, account_last4_snapshot,
    payment_environment
) VALUES (
    sqlc.arg(user_id), sqlc.arg(wallet_account_id), sqlc.arg(amount_vnd),
    sqlc.arg(payment_reference), sqlc.arg(idempotency_key), sqlc.arg(expires_at),
    sqlc.arg(bank_account_id), sqlc.arg(bank_bin_snapshot), sqlc.arg(bank_name_snapshot),
    sqlc.arg(bank_display_name_snapshot), sqlc.arg(bank_account_name_snapshot),
    sqlc.arg(encrypted_account_number_snapshot), sqlc.arg(account_number_nonce_snapshot),
    'aes-256-gcm-v1', sqlc.arg(account_key_version_snapshot), sqlc.arg(account_last4_snapshot),
    COALESCE(NULLIF(sqlc.arg(payment_environment), ''), 'production')
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: FindWalletTopupByIdempotency :one
SELECT * FROM wallet_topup_intents
WHERE user_id = sqlc.arg(user_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: FindWalletTopupByReference :one
SELECT * FROM wallet_topup_intents WHERE payment_reference = sqlc.arg(payment_reference);

-- name: LockWalletTopupByReference :one
SELECT * FROM wallet_topup_intents WHERE payment_reference = sqlc.arg(payment_reference) FOR UPDATE;

-- name: MarkWalletTopupCredited :one
UPDATE wallet_topup_intents
SET status = 'credited', credited_at = sqlc.arg(credited_at), version = version + 1
WHERE id = sqlc.arg(id) AND status = 'pending_payment' AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: MarkWalletTopupReview :one
UPDATE wallet_topup_intents
SET status = 'payment_review', version = version + 1
WHERE id = sqlc.arg(id) AND status IN ('pending_payment', 'expired')
RETURNING *;

-- name: ListWalletTopupsByUser :many
SELECT * FROM wallet_topup_intents WHERE user_id = sqlc.arg(user_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;
