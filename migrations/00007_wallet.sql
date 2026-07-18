-- +goose Up
CREATE TABLE wallet_accounts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    status text NOT NULL DEFAULT 'active',
    balance_vnd bigint NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT wallet_accounts_user_id_unique UNIQUE (user_id),
    CONSTRAINT wallet_accounts_user_id_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT wallet_accounts_status_valid CHECK (status IN ('active', 'frozen', 'closed')),
    CONSTRAINT wallet_accounts_balance_nonnegative CHECK (balance_vnd >= 0),
    CONSTRAINT wallet_accounts_version_positive CHECK (version > 0)
);

CREATE INDEX wallet_accounts_status_idx ON wallet_accounts (status, id);

CREATE TRIGGER wallet_accounts_set_updated_at
BEFORE UPDATE ON wallet_accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE wallet_ledger_entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id bigint NOT NULL,
    entry_type text NOT NULL,
    amount_vnd bigint NOT NULL,
    balance_after_vnd bigint NOT NULL,
    reference_type text NOT NULL,
    reference_id bigint NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT wallet_ledger_entries_account_id_fk FOREIGN KEY (account_id) REFERENCES wallet_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT wallet_ledger_entries_account_idempotency_unique UNIQUE (account_id, idempotency_key),
    CONSTRAINT wallet_ledger_entries_type_valid CHECK (entry_type IN ('credit', 'debit', 'refund', 'adjustment')),
    CONSTRAINT wallet_ledger_entries_amount_valid CHECK (
        (entry_type IN ('credit', 'refund') AND amount_vnd > 0)
        OR (entry_type = 'debit' AND amount_vnd < 0)
        OR (entry_type = 'adjustment' AND amount_vnd <> 0)
    ),
    CONSTRAINT wallet_ledger_entries_balance_nonnegative CHECK (balance_after_vnd >= 0),
    CONSTRAINT wallet_ledger_entries_reference_type_not_blank CHECK (btrim(reference_type) <> ''),
    CONSTRAINT wallet_ledger_entries_reference_id_positive CHECK (reference_id > 0),
    CONSTRAINT wallet_ledger_entries_idempotency_not_blank CHECK (btrim(idempotency_key) <> '')
);

CREATE INDEX wallet_ledger_entries_account_idx ON wallet_ledger_entries (account_id, id DESC);
CREATE INDEX wallet_ledger_entries_reference_idx ON wallet_ledger_entries (reference_type, reference_id, id);

CREATE TRIGGER wallet_ledger_entries_append_only
BEFORE UPDATE OR DELETE ON wallet_ledger_entries
FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose Down
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wallet_accounts;
