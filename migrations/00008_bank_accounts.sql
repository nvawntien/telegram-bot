-- +goose Up
CREATE TABLE bank_accounts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bank_bin text NOT NULL,
    bank_name text NOT NULL,
    account_name text NOT NULL,
    encrypted_account_number bytea NOT NULL,
    account_number_fingerprint bytea NOT NULL,
    encryption_key_id text NOT NULL,
    display_last4 text NOT NULL,
    sort_order integer NOT NULL DEFAULT 0,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT bank_accounts_fingerprint_unique UNIQUE (account_number_fingerprint),
    CONSTRAINT bank_accounts_bin_valid CHECK (bank_bin ~ '^[0-9]{6}$'),
    CONSTRAINT bank_accounts_bank_name_not_blank CHECK (btrim(bank_name) <> ''),
    CONSTRAINT bank_accounts_account_name_not_blank CHECK (btrim(account_name) <> ''),
    CONSTRAINT bank_accounts_encrypted_number_not_empty CHECK (octet_length(encrypted_account_number) > 0),
    CONSTRAINT bank_accounts_fingerprint_sha256 CHECK (octet_length(account_number_fingerprint) = 32),
    CONSTRAINT bank_accounts_key_id_not_blank CHECK (btrim(encryption_key_id) <> ''),
    CONSTRAINT bank_accounts_last4_valid CHECK (display_last4 ~ '^[0-9]{4}$'),
    CONSTRAINT bank_accounts_sort_order_nonnegative CHECK (sort_order >= 0)
);

CREATE INDEX bank_accounts_active_sort_idx ON bank_accounts (is_active, sort_order, id);

CREATE TRIGGER bank_accounts_set_updated_at
BEFORE UPDATE ON bank_accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS bank_accounts;
