-- +goose Up
CREATE TABLE telegram_update_receipts (
    update_id bigint PRIMARY KEY,
    update_type text NOT NULL,
    status text NOT NULL DEFAULT 'received',
    attempts integer NOT NULL DEFAULT 0,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processing_started_at timestamptz,
    processed_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT telegram_update_receipts_id_nonnegative CHECK (update_id >= 0),
    CONSTRAINT telegram_update_receipts_type_not_blank CHECK (btrim(update_type) <> ''),
    CONSTRAINT telegram_update_receipts_status_valid CHECK (status IN ('received', 'processing', 'completed', 'failed')),
    CONSTRAINT telegram_update_receipts_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT telegram_update_receipts_error_not_blank CHECK (last_error IS NULL OR btrim(last_error) <> ''),
    CONSTRAINT telegram_update_receipts_state_consistent CHECK (
        (status = 'received' AND processing_started_at IS NULL AND processed_at IS NULL)
        OR (status = 'processing' AND processing_started_at IS NOT NULL AND processed_at IS NULL)
        OR (status IN ('completed', 'failed') AND processing_started_at IS NOT NULL AND processed_at IS NOT NULL)
    )
);

CREATE INDEX telegram_update_receipts_processing_idx
ON telegram_update_receipts (processing_started_at, update_id)
WHERE status = 'processing';

CREATE INDEX telegram_update_receipts_status_received_idx
ON telegram_update_receipts (status, received_at, update_id);

CREATE TRIGGER telegram_update_receipts_set_updated_at
BEFORE UPDATE ON telegram_update_receipts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE audit_logs
ADD COLUMN telegram_update_id bigint,
ADD CONSTRAINT audit_logs_telegram_update_id_fk
    FOREIGN KEY (telegram_update_id) REFERENCES telegram_update_receipts (update_id) ON DELETE RESTRICT;

CREATE INDEX audit_logs_telegram_update_idx
ON audit_logs (telegram_update_id, id)
WHERE telegram_update_id IS NOT NULL;

ALTER TABLE categories
ADD COLUMN version bigint NOT NULL DEFAULT 1,
ADD CONSTRAINT categories_version_positive CHECK (version > 0);

-- +goose Down
ALTER TABLE categories DROP CONSTRAINT IF EXISTS categories_version_positive;
ALTER TABLE categories DROP COLUMN IF EXISTS version;
DROP INDEX IF EXISTS audit_logs_telegram_update_idx;
ALTER TABLE audit_logs DROP CONSTRAINT IF EXISTS audit_logs_telegram_update_id_fk;
ALTER TABLE audit_logs DROP COLUMN IF EXISTS telegram_update_id;
DROP TABLE IF EXISTS telegram_update_receipts;
