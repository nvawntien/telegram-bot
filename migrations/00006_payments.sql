-- +goose Up
CREATE TABLE payments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id bigint,
    user_id bigint NOT NULL,
    purpose text NOT NULL,
    provider text NOT NULL,
    provider_transaction_id text,
    payment_reference text NOT NULL,
    amount_vnd bigint NOT NULL,
    currency text NOT NULL DEFAULT 'VND',
    status text NOT NULL DEFAULT 'created',
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payments_order_id_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT payments_user_id_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT payments_provider_reference_unique UNIQUE (provider, payment_reference),
    CONSTRAINT payments_purpose_valid CHECK (purpose IN ('order', 'wallet_topup', 'refund')),
    CONSTRAINT payments_order_purpose_consistent CHECK (purpose <> 'order' OR order_id IS NOT NULL),
    CONSTRAINT payments_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT payments_provider_transaction_not_blank CHECK (provider_transaction_id IS NULL OR btrim(provider_transaction_id) <> ''),
    CONSTRAINT payments_reference_not_blank CHECK (btrim(payment_reference) <> ''),
    CONSTRAINT payments_amount_positive CHECK (amount_vnd > 0),
    CONSTRAINT payments_currency_vnd CHECK (currency = 'VND'),
    CONSTRAINT payments_status_valid CHECK (status IN ('created', 'confirmed', 'failed', 'review', 'refunded')),
    CONSTRAINT payments_confirmation_consistent CHECK (status <> 'confirmed' OR confirmed_at IS NOT NULL)
);

CREATE UNIQUE INDEX payments_provider_transaction_unique_idx
ON payments (provider, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;

CREATE INDEX payments_order_idx ON payments (order_id, created_at DESC) WHERE order_id IS NOT NULL;
CREATE INDEX payments_user_idx ON payments (user_id, created_at DESC);

CREATE TRIGGER payments_set_updated_at
BEFORE UPDATE ON payments
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE payment_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL,
    external_event_id text NOT NULL,
    provider_transaction_id text,
    event_type text NOT NULL,
    payload_hash bytea NOT NULL,
    sanitized_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    signature_verified boolean NOT NULL,
    processing_status text NOT NULL DEFAULT 'received',
    processing_error text,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT payment_events_provider_external_unique UNIQUE (provider, external_event_id),
    CONSTRAINT payment_events_provider_not_blank CHECK (btrim(provider) <> ''),
    CONSTRAINT payment_events_external_id_not_blank CHECK (btrim(external_event_id) <> ''),
    CONSTRAINT payment_events_provider_transaction_not_blank CHECK (provider_transaction_id IS NULL OR btrim(provider_transaction_id) <> ''),
    CONSTRAINT payment_events_event_type_not_blank CHECK (btrim(event_type) <> ''),
    CONSTRAINT payment_events_payload_hash_sha256 CHECK (octet_length(payload_hash) = 32),
    CONSTRAINT payment_events_payload_object CHECK (jsonb_typeof(sanitized_payload) = 'object'),
    CONSTRAINT payment_events_status_valid CHECK (processing_status IN ('received', 'processed', 'review', 'rejected')),
    CONSTRAINT payment_events_processing_consistent CHECK (
        (processing_status = 'received' AND processed_at IS NULL)
        OR (processing_status <> 'received' AND processed_at IS NOT NULL)
    ),
    CONSTRAINT payment_events_error_not_blank CHECK (processing_error IS NULL OR btrim(processing_error) <> '')
);

CREATE INDEX payment_events_transaction_idx ON payment_events (provider, provider_transaction_id)
WHERE provider_transaction_id IS NOT NULL;
CREATE INDEX payment_events_status_received_idx ON payment_events (processing_status, received_at, id);

CREATE TRIGGER payment_events_set_updated_at
BEFORE UPDATE ON payment_events
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS payment_events;
DROP TABLE IF EXISTS payments;
