-- +goose Up
ALTER TABLE orders
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD CONSTRAINT orders_payment_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production'));

ALTER TABLE wallet_topup_intents
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD CONSTRAINT wallet_topups_payment_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production'));

ALTER TABLE payments
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD CONSTRAINT payments_payment_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production'));

DROP INDEX payments_provider_transaction_unique_idx;
CREATE UNIQUE INDEX payments_provider_transaction_unique_idx
ON payments (provider, payment_environment, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;

CREATE TABLE payment_provider_accounts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL,
    environment text NOT NULL,
    external_account_identity text NOT NULL,
    external_identity_fingerprint bytea NOT NULL,
    local_bank_account_id bigint NOT NULL,
    status text NOT NULL DEFAULT 'active',
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payment_provider_accounts_bank_fk
        FOREIGN KEY (local_bank_account_id) REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT payment_provider_accounts_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT payment_provider_accounts_environment_valid
        CHECK (environment IN ('development', 'test', 'production')),
    CONSTRAINT payment_provider_accounts_identity_not_blank
        CHECK (btrim(external_account_identity) <> ''),
    CONSTRAINT payment_provider_accounts_fingerprint_sha256
        CHECK (octet_length(external_identity_fingerprint) = 32),
    CONSTRAINT payment_provider_accounts_status_valid CHECK (status IN ('active', 'inactive')),
    CONSTRAINT payment_provider_accounts_version_positive CHECK (version > 0),
    CONSTRAINT payment_provider_accounts_external_unique
        UNIQUE (provider, environment, external_account_identity),
    CONSTRAINT payment_provider_accounts_fingerprint_unique
        UNIQUE (provider, environment, external_identity_fingerprint)
);

CREATE UNIQUE INDEX payment_provider_accounts_active_bank_unique
ON payment_provider_accounts (provider, environment, local_bank_account_id)
WHERE status = 'active';

CREATE INDEX payment_provider_accounts_list_idx
ON payment_provider_accounts (provider, environment, status, id);

CREATE TRIGGER payment_provider_accounts_set_updated_at
BEFORE UPDATE ON payment_provider_accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE payment_provider_checkpoints (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_account_id bigint NOT NULL,
    cursor_value text,
    last_transaction_external_id text,
    last_occurred_at timestamptz,
    last_attempted_at timestamptz,
    last_successful_at timestamptz,
    last_error_code text,
    lease_owner text,
    lease_expires_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payment_provider_checkpoints_account_fk
        FOREIGN KEY (provider_account_id) REFERENCES payment_provider_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT payment_provider_checkpoints_account_unique UNIQUE (provider_account_id),
    CONSTRAINT payment_provider_checkpoints_cursor_not_blank
        CHECK (cursor_value IS NULL OR btrim(cursor_value) <> ''),
    CONSTRAINT payment_provider_checkpoints_transaction_not_blank
        CHECK (last_transaction_external_id IS NULL OR btrim(last_transaction_external_id) <> ''),
    CONSTRAINT payment_provider_checkpoints_error_not_blank
        CHECK (last_error_code IS NULL OR btrim(last_error_code) <> ''),
    CONSTRAINT payment_provider_checkpoints_lease_consistent CHECK (
        (lease_owner IS NULL AND lease_expires_at IS NULL)
        OR (lease_owner IS NOT NULL AND btrim(lease_owner) <> '' AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT payment_provider_checkpoints_success_order CHECK (
        last_successful_at IS NULL OR last_attempted_at IS NULL OR last_successful_at <= last_attempted_at
    ),
    CONSTRAINT payment_provider_checkpoints_version_positive CHECK (version > 0)
);

CREATE INDEX payment_provider_checkpoints_lease_idx
ON payment_provider_checkpoints (lease_expires_at, id)
WHERE lease_owner IS NOT NULL;

CREATE TRIGGER payment_provider_checkpoints_set_updated_at
BEFORE UPDATE ON payment_provider_checkpoints
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE payment_events
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD COLUMN event_source text NOT NULL DEFAULT 'legacy',
ADD COLUMN transfer_direction text NOT NULL DEFAULT 'inbound',
ADD COLUMN transfer_content text NOT NULL DEFAULT 'legacy-unresolved',
ADD COLUMN destination_account_identity text,
ADD COLUMN provider_account_identity text,
ADD COLUMN provider_account_mapping_id bigint,
ADD COLUMN business_fingerprint bytea NOT NULL DEFAULT decode(repeat('00', 32), 'hex');

UPDATE payment_events
SET transfer_content = COALESCE(NULLIF(sanitized_payload ->> 'reference', ''), external_event_id),
    business_fingerprint = payload_hash;

ALTER TABLE payment_events
ADD CONSTRAINT payment_events_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production')),
ADD CONSTRAINT payment_events_source_valid
    CHECK (event_source IN ('webhook', 'reconciliation', 'legacy')),
ADD CONSTRAINT payment_events_direction_valid
    CHECK (transfer_direction IN ('inbound', 'outbound')),
ADD CONSTRAINT payment_events_transfer_content_not_blank
    CHECK (btrim(transfer_content) <> '' AND octet_length(transfer_content) <= 2048),
ADD CONSTRAINT payment_events_destination_not_blank
    CHECK (destination_account_identity IS NULL OR btrim(destination_account_identity) <> ''),
ADD CONSTRAINT payment_events_provider_account_not_blank
    CHECK (provider_account_identity IS NULL OR btrim(provider_account_identity) <> ''),
ADD CONSTRAINT payment_events_business_fingerprint_sha256
    CHECK (octet_length(business_fingerprint) = 32),
ADD CONSTRAINT payment_events_provider_account_fk
    FOREIGN KEY (provider_account_mapping_id) REFERENCES payment_provider_accounts (id) ON DELETE RESTRICT;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payment_events
        WHERE provider_transaction_id IS NOT NULL
        GROUP BY provider, payment_environment, provider_transaction_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot add provider transaction uniqueness while duplicate payment events exist';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX payment_events_transaction_idx;
CREATE UNIQUE INDEX payment_events_provider_transaction_unique_idx
ON payment_events (provider, payment_environment, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;

CREATE INDEX payment_events_provider_health_idx
ON payment_events (provider, payment_environment, event_source, received_at DESC, id DESC);

ALTER TABLE payment_review_cases
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD COLUMN event_source text NOT NULL DEFAULT 'legacy',
ADD COLUMN provider_account_mapping_id bigint,
ADD COLUMN destination_account_identity text,
ADD CONSTRAINT payment_reviews_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production')),
ADD CONSTRAINT payment_reviews_source_valid
    CHECK (event_source IN ('webhook', 'reconciliation', 'legacy', 'manual')),
ADD CONSTRAINT payment_reviews_provider_account_fk
    FOREIGN KEY (provider_account_mapping_id) REFERENCES payment_provider_accounts (id) ON DELETE RESTRICT,
ADD CONSTRAINT payment_reviews_destination_not_blank
    CHECK (destination_account_identity IS NULL OR btrim(destination_account_identity) <> '');

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_provider_accounts)
       OR EXISTS (SELECT 1 FROM payment_provider_checkpoints)
       OR EXISTS (SELECT 1 FROM payment_events WHERE event_source <> 'legacy')
       OR EXISTS (SELECT 1 FROM orders WHERE payment_environment <> 'production')
       OR EXISTS (SELECT 1 FROM wallet_topup_intents WHERE payment_environment <> 'production')
       OR EXISTS (SELECT 1 FROM payments WHERE payment_environment <> 'production') THEN
        RAISE EXCEPTION 'cannot remove Phase 8A provider automation data';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE payment_review_cases
DROP CONSTRAINT IF EXISTS payment_reviews_destination_not_blank,
DROP CONSTRAINT IF EXISTS payment_reviews_provider_account_fk,
DROP CONSTRAINT IF EXISTS payment_reviews_source_valid,
DROP CONSTRAINT IF EXISTS payment_reviews_environment_valid,
DROP COLUMN IF EXISTS destination_account_identity,
DROP COLUMN IF EXISTS provider_account_mapping_id,
DROP COLUMN IF EXISTS event_source,
DROP COLUMN IF EXISTS payment_environment;

DROP INDEX IF EXISTS payment_events_provider_health_idx;
DROP INDEX IF EXISTS payment_events_provider_transaction_unique_idx;
CREATE INDEX payment_events_transaction_idx ON payment_events (provider, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;

ALTER TABLE payment_events
DROP CONSTRAINT IF EXISTS payment_events_provider_account_fk,
DROP CONSTRAINT IF EXISTS payment_events_business_fingerprint_sha256,
DROP CONSTRAINT IF EXISTS payment_events_provider_account_not_blank,
DROP CONSTRAINT IF EXISTS payment_events_destination_not_blank,
DROP CONSTRAINT IF EXISTS payment_events_transfer_content_not_blank,
DROP CONSTRAINT IF EXISTS payment_events_direction_valid,
DROP CONSTRAINT IF EXISTS payment_events_source_valid,
DROP CONSTRAINT IF EXISTS payment_events_environment_valid,
DROP COLUMN IF EXISTS business_fingerprint,
DROP COLUMN IF EXISTS provider_account_mapping_id,
DROP COLUMN IF EXISTS provider_account_identity,
DROP COLUMN IF EXISTS destination_account_identity,
DROP COLUMN IF EXISTS transfer_content,
DROP COLUMN IF EXISTS transfer_direction,
DROP COLUMN IF EXISTS event_source,
DROP COLUMN IF EXISTS payment_environment;

DROP TABLE IF EXISTS payment_provider_checkpoints;
DROP TABLE IF EXISTS payment_provider_accounts;

DROP INDEX IF EXISTS payments_provider_transaction_unique_idx;
CREATE UNIQUE INDEX payments_provider_transaction_unique_idx
ON payments (provider, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;

ALTER TABLE payments
DROP CONSTRAINT IF EXISTS payments_payment_environment_valid,
DROP COLUMN IF EXISTS payment_environment;

ALTER TABLE wallet_topup_intents
DROP CONSTRAINT IF EXISTS wallet_topups_payment_environment_valid,
DROP COLUMN IF EXISTS payment_environment;

ALTER TABLE orders
DROP CONSTRAINT IF EXISTS orders_payment_environment_valid,
DROP COLUMN IF EXISTS payment_environment;
