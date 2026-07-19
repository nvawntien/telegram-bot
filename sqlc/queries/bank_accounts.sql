-- name: ListActiveBankAccountOptions :many
SELECT id, bank_bin, bank_name, display_name, account_name, display_last4,
       sort_order, version, payment_environment
FROM bank_accounts
WHERE is_active = true
  AND payment_environment = sqlc.arg(payment_environment)
  AND encryption_format = 'aes-256-gcm-v1'
ORDER BY sort_order, id;

-- name: GetActiveBankAccountForOrder :one
SELECT *
FROM bank_accounts
WHERE id = sqlc.arg(id)
  AND is_active = true
  AND payment_environment = sqlc.arg(payment_environment)
  AND encryption_format = 'aes-256-gcm-v1'
FOR SHARE;

-- name: CountAdminBankAccounts :one
SELECT count(*)::bigint FROM bank_accounts;

-- name: ListAdminBankAccountsPage :many
SELECT id, bank_bin, bank_name, display_name, account_name, display_last4,
       sort_order, is_active, encryption_key_version, encryption_format,
       version, created_at, payment_environment
FROM bank_accounts
ORDER BY sort_order, id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: LockBankAccountByID :one
SELECT * FROM bank_accounts WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CreateEncryptedBankAccount :one
INSERT INTO bank_accounts (
    bank_bin, bank_name, display_name, account_name,
    encrypted_account_number, account_number_fingerprint,
    encryption_key_id, encryption_nonce, encryption_format,
    encryption_key_version, display_last4, sort_order, payment_environment
) VALUES (
    sqlc.arg(bank_bin), sqlc.arg(bank_name), sqlc.arg(display_name),
    sqlc.arg(account_name), sqlc.arg(encrypted_account_number),
    sqlc.arg(account_number_fingerprint), sqlc.arg(encryption_key_id),
    sqlc.arg(encryption_nonce), 'aes-256-gcm-v1',
    sqlc.arg(encryption_key_version), sqlc.arg(display_last4),
    sqlc.arg(sort_order), COALESCE(NULLIF(sqlc.arg(payment_environment), ''), 'production')
)
ON CONFLICT (account_number_fingerprint) DO NOTHING
RETURNING *;

-- name: UpdateEncryptedBankAccountGuarded :one
UPDATE bank_accounts
SET bank_bin = sqlc.arg(bank_bin),
    bank_name = sqlc.arg(bank_name),
    display_name = sqlc.arg(display_name),
    account_name = sqlc.arg(account_name),
    encrypted_account_number = sqlc.arg(encrypted_account_number),
    account_number_fingerprint = sqlc.arg(account_number_fingerprint),
    encryption_key_id = sqlc.arg(encryption_key_id),
    encryption_nonce = sqlc.arg(encryption_nonce),
    encryption_format = 'aes-256-gcm-v1',
    encryption_key_version = sqlc.arg(encryption_key_version),
    display_last4 = sqlc.arg(display_last4),
    sort_order = sqlc.arg(sort_order),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetBankAccountActiveGuarded :one
UPDATE bank_accounts
SET is_active = sqlc.arg(is_active),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND is_active <> sqlc.arg(is_active)
RETURNING *;
