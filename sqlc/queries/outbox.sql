-- name: InsertOutboxEvent :one
INSERT INTO outbox_events (
    event_type,
    aggregate_type,
    aggregate_id,
    deduplication_key,
    payload,
    max_attempts,
    next_attempt_at
) VALUES (
    sqlc.arg(event_type),
    sqlc.arg(aggregate_type),
    sqlc.arg(aggregate_id),
    sqlc.arg(deduplication_key),
    sqlc.arg(payload),
    sqlc.arg(max_attempts),
    sqlc.arg(next_attempt_at)
)
RETURNING *;

-- name: ClaimPendingOutboxEvents :many
WITH selected_events AS (
    SELECT id
    FROM outbox_events
    WHERE status = 'pending'
      AND next_attempt_at <= clock_timestamp()
      AND attempts < max_attempts
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)::integer
)
UPDATE outbox_events AS event
SET status = 'processing',
    attempts = event.attempts + 1,
    locked_at = clock_timestamp(),
    locked_by = sqlc.arg(worker_id)
FROM selected_events
WHERE event.id = selected_events.id
RETURNING event.*;

-- name: MarkOutboxEventCompleted :one
UPDATE outbox_events
SET status = 'completed',
    locked_at = NULL,
    locked_by = NULL,
    last_error_code = NULL,
    last_error_detail = NULL,
    completed_at = clock_timestamp()
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND locked_by = sqlc.arg(worker_id)
RETURNING *;

-- name: ScheduleOutboxRetry :one
UPDATE outbox_events
SET status = 'pending',
    next_attempt_at = sqlc.arg(next_attempt_at),
    locked_at = NULL,
    locked_by = NULL,
    last_error_code = sqlc.arg(error_code),
    last_error_detail = sqlc.narg(error_detail)
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND locked_by = sqlc.arg(worker_id)
  AND attempts < max_attempts
RETURNING *;
