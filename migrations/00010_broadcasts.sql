-- +goose Up
CREATE TABLE broadcasts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_by bigint NOT NULL,
    content jsonb NOT NULL,
    status text NOT NULL DEFAULT 'draft',
    scheduled_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    cancelled_at timestamptz,
    total_count integer NOT NULL DEFAULT 0,
    success_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT broadcasts_created_by_fk FOREIGN KEY (created_by) REFERENCES admins (id) ON DELETE RESTRICT,
    CONSTRAINT broadcasts_content_object CHECK (jsonb_typeof(content) = 'object'),
    CONSTRAINT broadcasts_status_valid CHECK (status IN ('draft', 'queued', 'running', 'completed', 'cancelled', 'failed')),
    CONSTRAINT broadcasts_counts_nonnegative CHECK (total_count >= 0 AND success_count >= 0 AND failed_count >= 0),
    CONSTRAINT broadcasts_counts_bounded CHECK (success_count + failed_count <= total_count),
    CONSTRAINT broadcasts_finish_consistent CHECK (status NOT IN ('completed', 'failed') OR finished_at IS NOT NULL),
    CONSTRAINT broadcasts_cancel_consistent CHECK (status <> 'cancelled' OR cancelled_at IS NOT NULL)
);

CREATE INDEX broadcasts_claim_idx ON broadcasts (status, scheduled_at, id);

CREATE TRIGGER broadcasts_set_updated_at
BEFORE UPDATE ON broadcasts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE broadcast_recipients (
    broadcast_id bigint NOT NULL,
    user_id bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    telegram_message_id bigint,
    last_error_code text,
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT broadcast_recipients_pk PRIMARY KEY (broadcast_id, user_id),
    CONSTRAINT broadcast_recipients_broadcast_id_fk FOREIGN KEY (broadcast_id) REFERENCES broadcasts (id) ON DELETE RESTRICT,
    CONSTRAINT broadcast_recipients_user_id_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT broadcast_recipients_status_valid CHECK (status IN ('pending', 'sending', 'sent', 'retry', 'failed', 'skipped')),
    CONSTRAINT broadcast_recipients_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT broadcast_recipients_sent_consistent CHECK (status <> 'sent' OR sent_at IS NOT NULL),
    CONSTRAINT broadcast_recipients_error_not_blank CHECK (last_error_code IS NULL OR btrim(last_error_code) <> '')
);

CREATE INDEX broadcast_recipients_claim_idx
ON broadcast_recipients (broadcast_id, status, next_attempt_at, user_id);

CREATE TRIGGER broadcast_recipients_set_updated_at
BEFORE UPDATE ON broadcast_recipients
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS broadcast_recipients;
DROP TABLE IF EXISTS broadcasts;
