-- +goose Up
ALTER TABLE outbox_events
DROP CONSTRAINT outbox_events_status_valid,
DROP CONSTRAINT outbox_events_lock_consistent,
DROP CONSTRAINT outbox_events_completion_consistent,
ADD COLUMN delivery_order_id bigint,
ADD COLUMN recipient_chat_id bigint,
ADD COLUMN processing_stage text,
ADD COLUMN send_attempted_at timestamptz,
ADD COLUMN telegram_message_id bigint,
ADD COLUMN telegram_sent_at timestamptz,
ADD COLUMN manual_resolution text,
ADD COLUMN resolution_reason text,
ADD COLUMN resolved_by_admin_id bigint,
ADD COLUMN resolved_at timestamptz,
ADD COLUMN version bigint NOT NULL DEFAULT 1,
ADD CONSTRAINT outbox_events_delivery_order_fk
    FOREIGN KEY (delivery_order_id) REFERENCES orders (id) ON DELETE RESTRICT,
ADD CONSTRAINT outbox_events_resolution_admin_fk
    FOREIGN KEY (resolved_by_admin_id) REFERENCES admins (id) ON DELETE RESTRICT,
ADD CONSTRAINT outbox_events_status_valid CHECK (status IN (
    'pending', 'processing', 'retryable_failed', 'ambiguous', 'manual_review',
    'permanent_failed', 'completed', 'cancelled', 'failed'
)),
ADD CONSTRAINT outbox_events_lock_consistent CHECK (
    (
        status = 'processing'
        AND locked_at IS NOT NULL
        AND locked_by IS NOT NULL
        AND btrim(locked_by) <> ''
        AND processing_stage IN ('claimed', 'sending')
    )
    OR (
        status <> 'processing'
        AND locked_at IS NULL
        AND locked_by IS NULL
        AND processing_stage IS NULL
    )
),
ADD CONSTRAINT outbox_events_completion_consistent CHECK (
    (status IN ('completed', 'permanent_failed', 'cancelled', 'failed') AND completed_at IS NOT NULL)
    OR (status IN ('pending', 'processing', 'retryable_failed', 'ambiguous', 'manual_review') AND completed_at IS NULL)
),
ADD CONSTRAINT outbox_events_delivery_fields_consistent CHECK (
    (
        event_type = 'order.delivery_requested'
        AND aggregate_type = 'order'
        AND delivery_order_id = aggregate_id
        AND recipient_chat_id IS NOT NULL
        AND recipient_chat_id > 0
    )
    OR (
        event_type <> 'order.delivery_requested'
        AND delivery_order_id IS NULL
        AND recipient_chat_id IS NULL
        AND send_attempted_at IS NULL
        AND telegram_message_id IS NULL
        AND telegram_sent_at IS NULL
        AND manual_resolution IS NULL
        AND resolution_reason IS NULL
        AND resolved_by_admin_id IS NULL
        AND resolved_at IS NULL
    )
),
ADD CONSTRAINT outbox_events_send_stage_consistent CHECK (
    (processing_stage = 'sending' AND send_attempted_at IS NOT NULL)
    OR processing_stage IS DISTINCT FROM 'sending'
),
ADD CONSTRAINT outbox_events_telegram_evidence_consistent CHECK (
    (telegram_message_id IS NULL AND telegram_sent_at IS NULL)
    OR (telegram_message_id > 0 AND telegram_sent_at IS NOT NULL)
),
ADD CONSTRAINT outbox_events_resolution_consistent CHECK (
    (
        manual_resolution IS NULL
        AND resolution_reason IS NULL
        AND resolved_by_admin_id IS NULL
        AND resolved_at IS NULL
    )
    OR (
        manual_resolution IN ('retry', 'mark_delivered', 'cancel')
        AND resolution_reason IS NOT NULL
        AND btrim(resolution_reason) <> ''
        AND resolved_by_admin_id IS NOT NULL
        AND resolved_at IS NOT NULL
    )
),
ADD CONSTRAINT outbox_events_version_positive CHECK (version > 0);

DROP INDEX outbox_events_pending_claim_idx;
CREATE INDEX outbox_events_pending_claim_idx
ON outbox_events (next_attempt_at, created_at, id)
WHERE status IN ('pending', 'retryable_failed');

CREATE UNIQUE INDEX outbox_events_active_delivery_order_unique
ON outbox_events (delivery_order_id)
WHERE event_type = 'order.delivery_requested'
  AND status IN ('pending', 'processing', 'retryable_failed', 'ambiguous', 'manual_review');

CREATE INDEX outbox_events_delivery_review_idx
ON outbox_events (status, created_at, id)
WHERE event_type = 'order.delivery_requested'
  AND status IN ('ambiguous', 'manual_review', 'permanent_failed');

ALTER TABLE delivery_attempts
DROP CONSTRAINT delivery_attempts_order_attempt_unique,
DROP CONSTRAINT delivery_attempts_status_valid,
DROP CONSTRAINT delivery_attempts_finish_consistent,
ADD COLUMN delivery_job_id bigint,
ADD COLUMN telegram_method text NOT NULL DEFAULT 'sendMessage',
ADD COLUMN http_status integer,
ADD COLUMN telegram_error_code integer,
ADD COLUMN retry_after_seconds integer,
ADD COLUMN telegram_chat_id bigint,
ADD COLUMN error_class text,
ADD CONSTRAINT delivery_attempts_job_fk
    FOREIGN KEY (delivery_job_id) REFERENCES outbox_events (id) ON DELETE RESTRICT,
ADD CONSTRAINT delivery_attempts_status_valid CHECK (status IN (
    'started', 'succeeded', 'retryable_failed', 'ambiguous',
    'permanent_failed', 'manual_resolution'
)),
ADD CONSTRAINT delivery_attempts_finish_consistent CHECK (
    (status = 'started' AND finished_at IS NULL)
    OR (status <> 'started' AND finished_at IS NOT NULL)
),
ADD CONSTRAINT delivery_attempts_method_not_blank CHECK (btrim(telegram_method) <> ''),
ADD CONSTRAINT delivery_attempts_http_status_valid CHECK (
    http_status IS NULL OR http_status BETWEEN 100 AND 599
),
ADD CONSTRAINT delivery_attempts_telegram_error_valid CHECK (
    telegram_error_code IS NULL OR telegram_error_code BETWEEN 100 AND 599
),
ADD CONSTRAINT delivery_attempts_retry_after_positive CHECK (
    retry_after_seconds IS NULL OR retry_after_seconds > 0
),
ADD CONSTRAINT delivery_attempts_chat_positive CHECK (
    telegram_chat_id IS NULL OR telegram_chat_id > 0
),
ADD CONSTRAINT delivery_attempts_message_positive CHECK (
    telegram_message_id IS NULL OR telegram_message_id > 0
),
ADD CONSTRAINT delivery_attempts_success_evidence CHECK (
    status <> 'succeeded'
    OR (telegram_chat_id IS NOT NULL AND telegram_message_id IS NOT NULL)
),
ADD CONSTRAINT delivery_attempts_error_class_not_blank CHECK (
    error_class IS NULL OR btrim(error_class) <> ''
),
ADD CONSTRAINT delivery_attempts_job_attempt_status_unique
    UNIQUE (delivery_job_id, attempt_number, status);

CREATE INDEX delivery_attempts_job_attempt_idx
ON delivery_attempts (delivery_job_id, attempt_number DESC, id DESC);

CREATE INDEX delivery_attempts_success_evidence_idx
ON delivery_attempts (delivery_job_id, telegram_message_id)
WHERE status = 'succeeded';

CREATE TRIGGER delivery_attempts_append_only
BEFORE UPDATE OR DELETE ON delivery_attempts
FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM outbox_events WHERE event_type = 'order.delivery_requested')
       OR EXISTS (SELECT 1 FROM delivery_attempts WHERE delivery_job_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove Phase 7 delivery history';
    END IF;
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS delivery_attempts_append_only ON delivery_attempts;
DROP INDEX IF EXISTS delivery_attempts_success_evidence_idx;
DROP INDEX IF EXISTS delivery_attempts_job_attempt_idx;

ALTER TABLE delivery_attempts
DROP CONSTRAINT IF EXISTS delivery_attempts_job_attempt_status_unique,
DROP CONSTRAINT IF EXISTS delivery_attempts_error_class_not_blank,
DROP CONSTRAINT IF EXISTS delivery_attempts_success_evidence,
DROP CONSTRAINT IF EXISTS delivery_attempts_message_positive,
DROP CONSTRAINT IF EXISTS delivery_attempts_chat_positive,
DROP CONSTRAINT IF EXISTS delivery_attempts_retry_after_positive,
DROP CONSTRAINT IF EXISTS delivery_attempts_telegram_error_valid,
DROP CONSTRAINT IF EXISTS delivery_attempts_http_status_valid,
DROP CONSTRAINT IF EXISTS delivery_attempts_method_not_blank,
DROP CONSTRAINT IF EXISTS delivery_attempts_finish_consistent,
DROP CONSTRAINT IF EXISTS delivery_attempts_status_valid,
DROP CONSTRAINT IF EXISTS delivery_attempts_job_fk,
DROP COLUMN IF EXISTS error_class,
DROP COLUMN IF EXISTS telegram_chat_id,
DROP COLUMN IF EXISTS retry_after_seconds,
DROP COLUMN IF EXISTS telegram_error_code,
DROP COLUMN IF EXISTS http_status,
DROP COLUMN IF EXISTS telegram_method,
DROP COLUMN IF EXISTS delivery_job_id,
ADD CONSTRAINT delivery_attempts_order_attempt_unique UNIQUE (order_id, attempt_number),
ADD CONSTRAINT delivery_attempts_status_valid CHECK (
    status IN ('started', 'succeeded', 'retryable_failed', 'permanent_failed')
),
ADD CONSTRAINT delivery_attempts_finish_consistent CHECK (
    (status = 'started' AND finished_at IS NULL)
    OR (status <> 'started' AND finished_at IS NOT NULL)
);

DROP INDEX IF EXISTS outbox_events_delivery_review_idx;
DROP INDEX IF EXISTS outbox_events_active_delivery_order_unique;
DROP INDEX IF EXISTS outbox_events_pending_claim_idx;
CREATE INDEX outbox_events_pending_claim_idx
ON outbox_events (next_attempt_at, created_at, id)
WHERE status = 'pending';

ALTER TABLE outbox_events
DROP CONSTRAINT IF EXISTS outbox_events_version_positive,
DROP CONSTRAINT IF EXISTS outbox_events_resolution_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_telegram_evidence_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_send_stage_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_delivery_fields_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_completion_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_lock_consistent,
DROP CONSTRAINT IF EXISTS outbox_events_status_valid,
DROP CONSTRAINT IF EXISTS outbox_events_resolution_admin_fk,
DROP CONSTRAINT IF EXISTS outbox_events_delivery_order_fk,
DROP COLUMN IF EXISTS version,
DROP COLUMN IF EXISTS resolved_at,
DROP COLUMN IF EXISTS resolved_by_admin_id,
DROP COLUMN IF EXISTS resolution_reason,
DROP COLUMN IF EXISTS manual_resolution,
DROP COLUMN IF EXISTS telegram_sent_at,
DROP COLUMN IF EXISTS telegram_message_id,
DROP COLUMN IF EXISTS send_attempted_at,
DROP COLUMN IF EXISTS processing_stage,
DROP COLUMN IF EXISTS recipient_chat_id,
DROP COLUMN IF EXISTS delivery_order_id,
ADD CONSTRAINT outbox_events_status_valid CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
ADD CONSTRAINT outbox_events_lock_consistent CHECK (
    (status = 'processing' AND locked_at IS NOT NULL AND locked_by IS NOT NULL AND btrim(locked_by) <> '')
    OR (status <> 'processing' AND locked_at IS NULL AND locked_by IS NULL)
),
ADD CONSTRAINT outbox_events_completion_consistent CHECK (
    (status IN ('completed', 'failed') AND completed_at IS NOT NULL)
    OR (status IN ('pending', 'processing') AND completed_at IS NULL)
);
