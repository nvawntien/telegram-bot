-- +goose Up
ALTER TABLE payments
DROP CONSTRAINT payments_provider_reference_unique,
ADD COLUMN occurred_at timestamptz;

UPDATE payments SET occurred_at = COALESCE(confirmed_at, created_at) WHERE occurred_at IS NULL;
ALTER TABLE payments ALTER COLUMN occurred_at SET NOT NULL;

CREATE INDEX payments_reference_idx
ON payments (payment_reference, created_at DESC, id DESC);

ALTER TABLE payment_events
DROP CONSTRAINT payment_events_status_valid,
DROP CONSTRAINT payment_events_processing_consistent,
ADD COLUMN attempts integer NOT NULL DEFAULT 0,
ADD COLUMN max_attempts integer NOT NULL DEFAULT 5,
ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
ADD COLUMN processing_started_at timestamptz,
ADD COLUMN last_error_code text;

UPDATE payment_events
SET processing_status = CASE processing_status
    WHEN 'processed' THEN 'completed'
    WHEN 'rejected' THEN 'review'
    ELSE processing_status
END;

ALTER TABLE payment_events
ADD CONSTRAINT payment_events_status_valid
    CHECK (processing_status IN ('received', 'processing', 'completed', 'review', 'failed')),
ADD CONSTRAINT payment_events_attempts_valid
    CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
ADD CONSTRAINT payment_events_processing_consistent CHECK (
    (processing_status = 'received' AND processing_started_at IS NULL AND processed_at IS NULL)
    OR (processing_status = 'processing' AND processing_started_at IS NOT NULL AND processed_at IS NULL)
    OR (processing_status IN ('completed', 'review', 'failed') AND processed_at IS NOT NULL)
),
ADD CONSTRAINT payment_events_last_error_code_not_blank
    CHECK (last_error_code IS NULL OR btrim(last_error_code) <> '');

DROP INDEX payment_events_status_received_idx;
CREATE INDEX payment_events_pending_claim_idx
ON payment_events (next_attempt_at, received_at, id)
WHERE processing_status = 'received';
CREATE INDEX payment_events_processing_reclaim_idx
ON payment_events (processing_started_at, id)
WHERE processing_status = 'processing';

CREATE TABLE wallet_topup_intents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    wallet_account_id bigint NOT NULL,
    amount_vnd bigint NOT NULL,
    currency text NOT NULL DEFAULT 'VND',
    payment_reference text NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'pending_payment',
    expires_at timestamptz NOT NULL,
    credited_at timestamptz,
    bank_account_id bigint NOT NULL,
    bank_bin_snapshot text NOT NULL,
    bank_name_snapshot text NOT NULL,
    bank_display_name_snapshot text NOT NULL,
    bank_account_name_snapshot text NOT NULL,
    encrypted_account_number_snapshot bytea NOT NULL,
    account_number_nonce_snapshot bytea NOT NULL,
    account_encryption_format_snapshot text NOT NULL,
    account_key_version_snapshot integer NOT NULL,
    account_last4_snapshot text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT wallet_topups_user_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT wallet_topups_account_fk FOREIGN KEY (wallet_account_id) REFERENCES wallet_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT wallet_topups_bank_fk FOREIGN KEY (bank_account_id) REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT wallet_topups_reference_unique UNIQUE (payment_reference),
    CONSTRAINT wallet_topups_user_idempotency_unique UNIQUE (user_id, idempotency_key),
    CONSTRAINT wallet_topups_amount_positive CHECK (amount_vnd > 0),
    CONSTRAINT wallet_topups_currency_vnd CHECK (currency = 'VND'),
    CONSTRAINT wallet_topups_reference_not_blank CHECK (btrim(payment_reference) <> ''),
    CONSTRAINT wallet_topups_idempotency_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT wallet_topups_status_valid CHECK (status IN ('pending_payment', 'credited', 'expired', 'cancelled', 'payment_review')),
    CONSTRAINT wallet_topups_credit_consistent CHECK ((status = 'credited') = (credited_at IS NOT NULL)),
    CONSTRAINT wallet_topups_bank_snapshot_consistent CHECK (
        bank_bin_snapshot ~ '^[0-9]{6}$'
        AND btrim(bank_name_snapshot) <> ''
        AND btrim(bank_display_name_snapshot) <> ''
        AND btrim(bank_account_name_snapshot) <> ''
        AND octet_length(encrypted_account_number_snapshot) >= 16
        AND octet_length(account_number_nonce_snapshot) = 12
        AND account_encryption_format_snapshot = 'aes-256-gcm-v1'
        AND account_key_version_snapshot > 0
        AND account_last4_snapshot ~ '^[0-9]{4}$'
    ),
    CONSTRAINT wallet_topups_version_positive CHECK (version > 0)
);

CREATE INDEX wallet_topups_user_created_idx
ON wallet_topup_intents (user_id, created_at DESC, id DESC);
CREATE INDEX wallet_topups_pending_expiry_idx
ON wallet_topup_intents (expires_at, id) WHERE status = 'pending_payment';
CREATE TRIGGER wallet_topup_intents_set_updated_at
BEFORE UPDATE ON wallet_topup_intents
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE payment_allocations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id bigint NOT NULL,
    target_type text NOT NULL,
    target_id bigint NOT NULL,
    amount_vnd bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payment_allocations_payment_fk FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE RESTRICT,
    CONSTRAINT payment_allocations_payment_unique UNIQUE (payment_id),
    CONSTRAINT payment_allocations_target_unique UNIQUE (target_type, target_id),
    CONSTRAINT payment_allocations_target_valid CHECK (target_type IN ('order', 'wallet_topup')),
    CONSTRAINT payment_allocations_target_id_positive CHECK (target_id > 0),
    CONSTRAINT payment_allocations_amount_positive CHECK (amount_vnd > 0)
);

CREATE TABLE payment_review_cases (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_event_id bigint,
    payment_id bigint,
    order_id bigint,
    wallet_topup_id bigint,
    provider text NOT NULL,
    provider_transaction_id text,
    payment_reference text NOT NULL,
    amount_vnd bigint NOT NULL,
    currency text NOT NULL,
    occurred_at timestamptz NOT NULL,
    reason text NOT NULL,
    status text NOT NULL DEFAULT 'open',
    resolution_note text,
    resolved_by_admin_id bigint,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payment_reviews_event_fk FOREIGN KEY (payment_event_id) REFERENCES payment_events (id) ON DELETE RESTRICT,
    CONSTRAINT payment_reviews_payment_fk FOREIGN KEY (payment_id) REFERENCES payments (id) ON DELETE RESTRICT,
    CONSTRAINT payment_reviews_order_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT payment_reviews_topup_fk FOREIGN KEY (wallet_topup_id) REFERENCES wallet_topup_intents (id) ON DELETE RESTRICT,
    CONSTRAINT payment_reviews_admin_fk FOREIGN KEY (resolved_by_admin_id) REFERENCES admins (id) ON DELETE RESTRICT,
    CONSTRAINT payment_reviews_event_unique UNIQUE (payment_event_id),
    CONSTRAINT payment_reviews_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT payment_reviews_transaction_not_blank CHECK (provider_transaction_id IS NULL OR btrim(provider_transaction_id) <> ''),
    CONSTRAINT payment_reviews_reference_not_blank CHECK (btrim(payment_reference) <> ''),
    CONSTRAINT payment_reviews_amount_positive CHECK (amount_vnd > 0),
    CONSTRAINT payment_reviews_currency_not_blank CHECK (btrim(currency) <> ''),
    CONSTRAINT payment_reviews_reason_not_blank CHECK (btrim(reason) <> ''),
    CONSTRAINT payment_reviews_status_valid CHECK (status IN ('open', 'held', 'resolved')),
    CONSTRAINT payment_reviews_resolution_consistent CHECK (
        (status <> 'resolved' AND resolved_by_admin_id IS NULL AND resolved_at IS NULL)
        OR (status = 'resolved' AND resolved_by_admin_id IS NOT NULL AND resolved_at IS NOT NULL)
    ),
    CONSTRAINT payment_reviews_note_not_blank CHECK (resolution_note IS NULL OR btrim(resolution_note) <> '')
);

CREATE INDEX payment_reviews_open_created_idx
ON payment_review_cases (created_at, id) WHERE status IN ('open', 'held');
CREATE TRIGGER payment_review_cases_set_updated_at
BEFORE UPDATE ON payment_review_cases
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE payment_events
ADD COLUMN related_order_id bigint,
ADD COLUMN related_wallet_topup_id bigint,
ADD CONSTRAINT payment_events_order_fk FOREIGN KEY (related_order_id) REFERENCES orders (id) ON DELETE RESTRICT,
ADD CONSTRAINT payment_events_topup_fk FOREIGN KEY (related_wallet_topup_id) REFERENCES wallet_topup_intents (id) ON DELETE RESTRICT;

CREATE TRIGGER payment_allocations_append_only
BEFORE UPDATE OR DELETE ON payment_allocations
FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_allocations)
       OR EXISTS (SELECT 1 FROM payment_review_cases)
       OR EXISTS (SELECT 1 FROM wallet_topup_intents)
       OR EXISTS (SELECT 1 FROM payments WHERE occurred_at IS DISTINCT FROM COALESCE(confirmed_at, created_at))
       OR EXISTS (SELECT 1 FROM payment_events WHERE attempts <> 0 OR processing_status IN ('processing', 'failed')) THEN
        RAISE EXCEPTION 'cannot remove Phase 6 financial records or processing state';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS payment_allocations_append_only ON payment_allocations;
ALTER TABLE payment_events
DROP CONSTRAINT IF EXISTS payment_events_topup_fk,
DROP CONSTRAINT IF EXISTS payment_events_order_fk,
DROP COLUMN IF EXISTS related_wallet_topup_id,
DROP COLUMN IF EXISTS related_order_id;
DROP TABLE IF EXISTS payment_review_cases;
DROP TABLE IF EXISTS payment_allocations;
DROP TABLE IF EXISTS wallet_topup_intents;

DROP INDEX IF EXISTS payment_events_processing_reclaim_idx;
DROP INDEX IF EXISTS payment_events_pending_claim_idx;
ALTER TABLE payment_events
DROP CONSTRAINT IF EXISTS payment_events_last_error_code_not_blank,
DROP CONSTRAINT IF EXISTS payment_events_processing_consistent,
DROP CONSTRAINT IF EXISTS payment_events_attempts_valid,
DROP CONSTRAINT IF EXISTS payment_events_status_valid;
UPDATE payment_events SET processing_status = CASE processing_status WHEN 'completed' THEN 'processed' WHEN 'review' THEN 'rejected' ELSE processing_status END;
ALTER TABLE payment_events
DROP COLUMN IF EXISTS last_error_code,
DROP COLUMN IF EXISTS processing_started_at,
DROP COLUMN IF EXISTS next_attempt_at,
DROP COLUMN IF EXISTS max_attempts,
DROP COLUMN IF EXISTS attempts,
ADD CONSTRAINT payment_events_status_valid CHECK (processing_status IN ('received', 'processed', 'review', 'rejected')),
ADD CONSTRAINT payment_events_processing_consistent CHECK (
    (processing_status = 'received' AND processed_at IS NULL)
    OR (processing_status <> 'received' AND processed_at IS NOT NULL)
);
CREATE INDEX payment_events_status_received_idx ON payment_events (processing_status, received_at, id);

DROP INDEX IF EXISTS payments_reference_idx;
ALTER TABLE payments DROP COLUMN IF EXISTS occurred_at;
ALTER TABLE payments ADD CONSTRAINT payments_provider_reference_unique UNIQUE (provider, payment_reference);
