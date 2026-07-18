-- +goose Up
CREATE TABLE outbox_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id bigint NOT NULL,
    deduplication_key text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    locked_at timestamptz,
    locked_by text,
    last_error_code text,
    last_error_detail text,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT outbox_events_deduplication_unique UNIQUE (deduplication_key),
    CONSTRAINT outbox_events_event_type_not_blank CHECK (btrim(event_type) <> ''),
    CONSTRAINT outbox_events_aggregate_type_not_blank CHECK (btrim(aggregate_type) <> ''),
    CONSTRAINT outbox_events_aggregate_id_positive CHECK (aggregate_id > 0),
    CONSTRAINT outbox_events_deduplication_not_blank CHECK (btrim(deduplication_key) <> ''),
    CONSTRAINT outbox_events_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_events_status_valid CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    CONSTRAINT outbox_events_attempts_valid CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
    CONSTRAINT outbox_events_lock_consistent CHECK (
        (status = 'processing' AND locked_at IS NOT NULL AND locked_by IS NOT NULL AND btrim(locked_by) <> '')
        OR (status <> 'processing' AND locked_at IS NULL AND locked_by IS NULL)
    ),
    CONSTRAINT outbox_events_completion_consistent CHECK (
        (status IN ('completed', 'failed') AND completed_at IS NOT NULL)
        OR (status IN ('pending', 'processing') AND completed_at IS NULL)
    ),
    CONSTRAINT outbox_events_last_error_code_not_blank CHECK (last_error_code IS NULL OR btrim(last_error_code) <> ''),
    CONSTRAINT outbox_events_last_error_detail_not_blank CHECK (last_error_detail IS NULL OR btrim(last_error_detail) <> '')
);

CREATE INDEX outbox_events_pending_claim_idx
ON outbox_events (next_attempt_at, created_at, id)
WHERE status = 'pending';

CREATE INDEX outbox_events_processing_lease_idx
ON outbox_events (locked_at, id)
WHERE status = 'processing';

CREATE TRIGGER outbox_events_set_updated_at
BEFORE UPDATE ON outbox_events
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE delivery_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id bigint NOT NULL,
    attempt_number integer NOT NULL,
    channel text NOT NULL DEFAULT 'telegram',
    status text NOT NULL DEFAULT 'started',
    telegram_message_id bigint,
    error_code text,
    error_detail text,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT delivery_attempts_order_id_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT delivery_attempts_order_attempt_unique UNIQUE (order_id, attempt_number),
    CONSTRAINT delivery_attempts_attempt_positive CHECK (attempt_number > 0),
    CONSTRAINT delivery_attempts_channel_not_blank CHECK (btrim(channel) <> ''),
    CONSTRAINT delivery_attempts_status_valid CHECK (status IN ('started', 'succeeded', 'retryable_failed', 'permanent_failed')),
    CONSTRAINT delivery_attempts_finish_consistent CHECK (
        (status = 'started' AND finished_at IS NULL)
        OR (status <> 'started' AND finished_at IS NOT NULL)
    ),
    CONSTRAINT delivery_attempts_error_code_not_blank CHECK (error_code IS NULL OR btrim(error_code) <> ''),
    CONSTRAINT delivery_attempts_error_detail_not_blank CHECK (error_detail IS NULL OR btrim(error_detail) <> '')
);

CREATE INDEX delivery_attempts_order_idx ON delivery_attempts (order_id, attempt_number DESC);

CREATE TRIGGER delivery_attempts_set_updated_at
BEFORE UPDATE ON delivery_attempts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS outbox_events;
