-- +goose Up
ALTER TABLE bank_accounts
ADD COLUMN display_name text,
ADD COLUMN encryption_nonce bytea,
ADD COLUMN encryption_format text NOT NULL DEFAULT 'legacy-v0',
ADD COLUMN encryption_key_version integer NOT NULL DEFAULT 1,
ADD COLUMN version bigint NOT NULL DEFAULT 1;

UPDATE bank_accounts SET display_name = bank_name WHERE display_name IS NULL;

ALTER TABLE bank_accounts
ALTER COLUMN display_name SET NOT NULL,
ADD CONSTRAINT bank_accounts_display_name_not_blank CHECK (btrim(display_name) <> ''),
ADD CONSTRAINT bank_accounts_encryption_format_valid
    CHECK (encryption_format IN ('legacy-v0', 'aes-256-gcm-v1')),
ADD CONSTRAINT bank_accounts_encryption_envelope_consistent CHECK (
    (encryption_format = 'legacy-v0' AND encryption_nonce IS NULL)
    OR (
        encryption_format = 'aes-256-gcm-v1'
        AND octet_length(encryption_nonce) = 12
        AND octet_length(encrypted_account_number) >= 16
    )
),
ADD CONSTRAINT bank_accounts_key_version_positive CHECK (encryption_key_version > 0),
ADD CONSTRAINT bank_accounts_version_positive CHECK (version > 0);

ALTER TABLE orders
ADD COLUMN bank_account_id bigint,
ADD COLUMN bank_bin_snapshot text,
ADD COLUMN bank_name_snapshot text,
ADD COLUMN bank_display_name_snapshot text,
ADD COLUMN bank_account_name_snapshot text,
ADD COLUMN encrypted_account_number_snapshot bytea,
ADD COLUMN account_number_nonce_snapshot bytea,
ADD COLUMN account_encryption_format_snapshot text,
ADD COLUMN account_key_version_snapshot integer,
ADD COLUMN account_last4_snapshot text,
ADD CONSTRAINT orders_bank_account_id_fk
    FOREIGN KEY (bank_account_id) REFERENCES bank_accounts (id) ON DELETE RESTRICT,
ADD CONSTRAINT orders_bank_snapshot_consistent CHECK (
    (
        bank_account_id IS NULL
        AND bank_bin_snapshot IS NULL
        AND bank_name_snapshot IS NULL
        AND bank_display_name_snapshot IS NULL
        AND bank_account_name_snapshot IS NULL
        AND encrypted_account_number_snapshot IS NULL
        AND account_number_nonce_snapshot IS NULL
        AND account_encryption_format_snapshot IS NULL
        AND account_key_version_snapshot IS NULL
        AND account_last4_snapshot IS NULL
    )
    OR (
        bank_account_id IS NOT NULL
        AND bank_bin_snapshot IS NOT NULL
        AND bank_bin_snapshot ~ '^[0-9]{6}$'
        AND bank_name_snapshot IS NOT NULL
        AND btrim(bank_name_snapshot) <> ''
        AND bank_display_name_snapshot IS NOT NULL
        AND btrim(bank_display_name_snapshot) <> ''
        AND bank_account_name_snapshot IS NOT NULL
        AND btrim(bank_account_name_snapshot) <> ''
        AND encrypted_account_number_snapshot IS NOT NULL
        AND octet_length(encrypted_account_number_snapshot) >= 16
        AND account_number_nonce_snapshot IS NOT NULL
        AND octet_length(account_number_nonce_snapshot) = 12
        AND account_encryption_format_snapshot IS NOT NULL
        AND account_encryption_format_snapshot = 'aes-256-gcm-v1'
        AND account_key_version_snapshot IS NOT NULL
        AND account_key_version_snapshot > 0
        AND account_last4_snapshot IS NOT NULL
        AND account_last4_snapshot ~ '^[0-9]{4}$'
    )
);

CREATE INDEX orders_bank_account_idx
ON orders (bank_account_id, created_at DESC, id DESC)
WHERE bank_account_id IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM orders WHERE bank_account_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove Phase 5 bank snapshots while Phase 5 orders exist';
    END IF;
    IF EXISTS (
        SELECT 1 FROM bank_accounts
        WHERE encryption_format <> 'legacy-v0'
           OR version <> 1
           OR display_name <> bank_name
    ) THEN
        RAISE EXCEPTION 'cannot remove Phase 5 bank metadata while Phase 5 bank rows exist';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS orders_bank_account_idx;

ALTER TABLE orders
DROP CONSTRAINT IF EXISTS orders_bank_snapshot_consistent,
DROP CONSTRAINT IF EXISTS orders_bank_account_id_fk,
DROP COLUMN IF EXISTS account_last4_snapshot,
DROP COLUMN IF EXISTS account_key_version_snapshot,
DROP COLUMN IF EXISTS account_encryption_format_snapshot,
DROP COLUMN IF EXISTS account_number_nonce_snapshot,
DROP COLUMN IF EXISTS encrypted_account_number_snapshot,
DROP COLUMN IF EXISTS bank_account_name_snapshot,
DROP COLUMN IF EXISTS bank_display_name_snapshot,
DROP COLUMN IF EXISTS bank_name_snapshot,
DROP COLUMN IF EXISTS bank_bin_snapshot,
DROP COLUMN IF EXISTS bank_account_id;

ALTER TABLE bank_accounts
DROP CONSTRAINT IF EXISTS bank_accounts_version_positive,
DROP CONSTRAINT IF EXISTS bank_accounts_key_version_positive,
DROP CONSTRAINT IF EXISTS bank_accounts_encryption_envelope_consistent,
DROP CONSTRAINT IF EXISTS bank_accounts_encryption_format_valid,
DROP CONSTRAINT IF EXISTS bank_accounts_display_name_not_blank,
DROP COLUMN IF EXISTS version,
DROP COLUMN IF EXISTS encryption_key_version,
DROP COLUMN IF EXISTS encryption_format,
DROP COLUMN IF EXISTS encryption_nonce,
DROP COLUMN IF EXISTS display_name;
