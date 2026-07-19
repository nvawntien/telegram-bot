-- name: LockOrderForDeliveryHandoff :one
SELECT
    o.id,
    o.user_id,
    o.status,
    o.version,
    u.telegram_user_id,
    oi.product_name,
    oi.quantity
FROM orders AS o
JOIN users AS u ON u.id = o.user_id
JOIN order_items AS oi ON oi.order_id = o.id
WHERE o.id = sqlc.arg(order_id)
FOR UPDATE OF o;

-- name: CountExactReservedInventoryForOrder :one
SELECT count(*)::integer
FROM order_inventory_items AS mapping
JOIN inventory_items AS inventory ON inventory.id = mapping.inventory_item_id
WHERE mapping.order_id = sqlc.arg(order_id)
  AND mapping.status = 'active'
  AND inventory.status = 'reserved'
  AND inventory.reserved_order_id = mapping.order_id;

-- name: InsertDeliveryJob :one
INSERT INTO outbox_events (
    event_type,
    aggregate_type,
    aggregate_id,
    deduplication_key,
    payload,
    max_attempts,
    next_attempt_at,
    delivery_order_id,
    recipient_chat_id
) VALUES (
    'order.delivery_requested',
    'order',
    sqlc.arg(order_id)::bigint,
    'delivery:order:' || sqlc.arg(order_id)::bigint::text,
    jsonb_build_object('order_id', sqlc.arg(order_id)::bigint),
    sqlc.arg(max_attempts)::integer,
    sqlc.arg(next_attempt_at)::timestamptz,
    sqlc.arg(order_id)::bigint,
    sqlc.arg(recipient_chat_id)::bigint
)
ON CONFLICT (deduplication_key) DO NOTHING
RETURNING *;

-- name: GetDeliveryJobByOrder :one
SELECT *
FROM outbox_events
WHERE event_type = 'order.delivery_requested'
  AND delivery_order_id = sqlc.arg(order_id);

-- name: MarkOrderDeliveringGuarded :one
UPDATE orders
SET status = 'delivering',
    delivery_started_at = sqlc.arg(started_at),
    version = version + 1
WHERE id = sqlc.arg(order_id)
  AND status = 'reserving'
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: ListDeliveryBackfillOrderIDs :many
SELECT o.id
FROM orders AS o
WHERE o.status = 'reserving'
  AND (
      SELECT count(*)
      FROM order_inventory_items AS mapping
      JOIN inventory_items AS inventory ON inventory.id = mapping.inventory_item_id
      WHERE mapping.order_id = o.id
        AND mapping.status = 'active'
        AND inventory.status = 'reserved'
        AND inventory.reserved_order_id = o.id
  ) = (
      SELECT COALESCE(sum(item.quantity), 0)
      FROM order_items AS item
      WHERE item.order_id = o.id
  )
  AND NOT EXISTS (
      SELECT 1
      FROM outbox_events AS event
      WHERE event.event_type = 'order.delivery_requested'
        AND event.delivery_order_id = o.id
  )
ORDER BY o.created_at, o.id
FOR UPDATE OF o SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: ClaimDeliveryJobs :many
WITH selected_jobs AS (
    SELECT id
    FROM outbox_events
    WHERE event_type = 'order.delivery_requested'
      AND status IN ('pending', 'retryable_failed')
      AND next_attempt_at <= sqlc.arg(claimed_at)
      AND attempts < max_attempts
    ORDER BY next_attempt_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)::integer
)
UPDATE outbox_events AS job
SET status = 'processing',
    processing_stage = 'claimed',
    locked_at = sqlc.arg(claimed_at),
    locked_by = sqlc.arg(worker_id),
    last_error_code = NULL,
    last_error_detail = NULL,
    version = job.version + 1
FROM selected_jobs
WHERE job.id = selected_jobs.id
RETURNING job.*;

-- name: LockStaleDeliveryJobs :many
SELECT *
FROM outbox_events
WHERE event_type = 'order.delivery_requested'
  AND status = 'processing'
  AND locked_at < sqlc.arg(stale_before)
ORDER BY locked_at, id
FOR UPDATE SKIP LOCKED
LIMIT sqlc.arg(batch_size)::integer;

-- name: MarkStaleDeliveryClaimRetryable :one
UPDATE outbox_events
SET status = 'retryable_failed',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error_code = 'stale_claim_recovered',
    last_error_detail = 'worker stopped before send began',
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'claimed'
RETURNING *;

-- name: MarkStaleDeliverySendAmbiguous :one
UPDATE outbox_events
SET status = 'ambiguous',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    last_error_code = 'stale_after_send_started',
    last_error_detail = 'send outcome requires manual verification',
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'sending'
RETURNING *;

-- name: LockDeliveryJob :one
SELECT *
FROM outbox_events
WHERE id = sqlc.arg(id)
  AND event_type = 'order.delivery_requested'
FOR UPDATE;

-- name: LoadDeliveryEnvelope :one
SELECT
    job.id AS delivery_job_id,
    job.delivery_order_id AS order_id,
    job.recipient_chat_id,
    job.status AS job_status,
    job.processing_stage,
    job.locked_by,
    job.attempts,
    job.max_attempts,
    job.version AS job_version,
    orders.status AS order_status,
    orders.version AS order_version,
    users.telegram_user_id,
    item.product_name,
    item.quantity
FROM outbox_events AS job
JOIN orders ON orders.id = job.delivery_order_id
JOIN users ON users.id = orders.user_id
JOIN order_items AS item ON item.order_id = orders.id
WHERE job.id = sqlc.arg(delivery_job_id)
  AND job.event_type = 'order.delivery_requested';

-- name: ListEncryptedInventoryForDelivery :many
SELECT
    inventory.id,
    inventory.product_id,
    inventory.encrypted_payload,
    inventory.encryption_nonce,
    inventory.encryption_format,
    inventory.encryption_key_version,
    inventory.status,
    inventory.reserved_order_id,
    mapping.order_item_id
FROM order_inventory_items AS mapping
JOIN inventory_items AS inventory ON inventory.id = mapping.inventory_item_id
WHERE mapping.order_id = sqlc.arg(order_id)
  AND mapping.status = 'active'
  AND inventory.status = 'reserved'
  AND inventory.reserved_order_id = mapping.order_id
ORDER BY mapping.order_item_id, inventory.id;

-- name: BeginDeliverySend :one
UPDATE outbox_events
SET processing_stage = 'sending',
    send_attempted_at = sqlc.arg(send_attempted_at),
    attempts = attempts + 1,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'claimed'
  AND locked_by = sqlc.arg(worker_id)
  AND attempts < max_attempts
RETURNING *;

-- name: InsertDeliveryAttemptEvent :one
INSERT INTO delivery_attempts (
    order_id,
    delivery_job_id,
    attempt_number,
    channel,
    status,
    telegram_method,
    http_status,
    telegram_error_code,
    retry_after_seconds,
    telegram_chat_id,
    telegram_message_id,
    error_class,
    error_code,
    error_detail,
    started_at,
    finished_at
) VALUES (
    sqlc.arg(order_id),
    sqlc.arg(delivery_job_id),
    sqlc.arg(attempt_number),
    'telegram',
    sqlc.arg(status),
    'sendMessage',
    sqlc.narg(http_status),
    sqlc.narg(telegram_error_code),
    sqlc.narg(retry_after_seconds),
    sqlc.narg(telegram_chat_id),
    sqlc.narg(telegram_message_id),
    sqlc.narg(error_class),
    sqlc.narg(error_code),
    sqlc.narg(error_detail),
    sqlc.arg(started_at),
    sqlc.narg(finished_at)
)
RETURNING *;

-- name: MarkDeliveryRetryable :one
UPDATE outbox_events
SET status = 'retryable_failed',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error_code = sqlc.arg(error_code),
    last_error_detail = sqlc.arg(error_detail),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'sending'
  AND locked_by = sqlc.arg(worker_id)
RETURNING *;

-- name: MarkClaimedDeliveryFailure :one
UPDATE outbox_events
SET status = sqlc.arg(new_status),
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    attempts = attempts + 1,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error_code = sqlc.arg(error_code),
    last_error_detail = sqlc.arg(error_detail),
    completed_at = sqlc.narg(completed_at),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'claimed'
  AND locked_by = sqlc.arg(worker_id)
  AND sqlc.arg(new_status) IN ('retryable_failed', 'permanent_failed', 'manual_review')
RETURNING *;

-- name: MarkDeliveryPermanentFailed :one
UPDATE outbox_events
SET status = 'permanent_failed',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    last_error_code = sqlc.arg(error_code),
    last_error_detail = sqlc.arg(error_detail),
    completed_at = sqlc.arg(completed_at),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'sending'
  AND locked_by = sqlc.arg(worker_id)
RETURNING *;

-- name: MarkDeliveryAmbiguous :one
UPDATE outbox_events
SET status = 'ambiguous',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    telegram_message_id = COALESCE(sqlc.narg(telegram_message_id), telegram_message_id),
    telegram_sent_at = COALESCE(sqlc.narg(telegram_sent_at), telegram_sent_at),
    last_error_code = sqlc.arg(error_code),
    last_error_detail = sqlc.arg(error_detail),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'sending'
RETURNING *;

-- name: MarkDeliveryCompleted :one
UPDATE outbox_events
SET status = 'completed',
    processing_stage = NULL,
    locked_at = NULL,
    locked_by = NULL,
    telegram_message_id = sqlc.arg(telegram_message_id),
    telegram_sent_at = sqlc.arg(telegram_sent_at),
    last_error_code = NULL,
    last_error_detail = NULL,
    completed_at = sqlc.arg(completed_at),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'processing'
  AND processing_stage = 'sending'
  AND locked_by = sqlc.arg(worker_id)
RETURNING *;

-- name: MarkExactReservedInventorySold :many
WITH eligible AS (
    SELECT inventory.id
    FROM order_inventory_items AS mapping
    JOIN inventory_items AS inventory ON inventory.id = mapping.inventory_item_id
    WHERE mapping.order_id = sqlc.arg(order_id)
      AND mapping.status = 'active'
      AND inventory.status = 'reserved'
      AND inventory.reserved_order_id = mapping.order_id
    ORDER BY inventory.id
    FOR UPDATE OF inventory
), exact_set AS (
    SELECT id
    FROM eligible
    WHERE (SELECT count(*) FROM eligible) = sqlc.arg(expected_count)::integer
)
UPDATE inventory_items AS inventory
SET status = 'sold',
    reserved_order_id = NULL,
    reserved_until = NULL,
    sold_order_id = sqlc.arg(order_id),
    version = inventory.version + 1
FROM exact_set
WHERE inventory.id = exact_set.id
RETURNING inventory.id;

-- name: MarkOrderDeliveredGuarded :one
UPDATE orders
SET status = 'delivered',
    delivered_at = sqlc.arg(delivered_at),
    version = version + 1
WHERE id = sqlc.arg(order_id)
  AND status = 'delivering'
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: MarkOrderDeliveryFailedGuarded :one
UPDATE orders
SET status = 'delivery_failed',
    version = version + 1
WHERE id = sqlc.arg(order_id)
  AND status = 'delivering'
RETURNING *;

-- name: CountDeliveryReviewJobs :one
SELECT count(*)
FROM outbox_events
WHERE event_type = 'order.delivery_requested'
  AND status IN ('ambiguous', 'manual_review', 'permanent_failed', 'retryable_failed');

-- name: ListDeliveryReviewJobs :many
SELECT
    job.id,
    job.delivery_order_id,
    job.status,
    job.attempts,
    job.max_attempts,
    job.recipient_chat_id,
    job.telegram_message_id,
    job.last_error_code,
    job.last_error_detail,
    job.created_at,
    job.updated_at,
    job.version,
    item.product_name,
    item.quantity
FROM outbox_events AS job
JOIN order_items AS item ON item.order_id = job.delivery_order_id
WHERE job.event_type = 'order.delivery_requested'
  AND job.status IN ('ambiguous', 'manual_review', 'permanent_failed', 'retryable_failed')
ORDER BY job.updated_at DESC, job.id DESC
OFFSET sqlc.arg(page_offset)::integer
LIMIT sqlc.arg(page_limit)::integer;

-- name: ListDeliveryAttemptEvents :many
SELECT *
FROM delivery_attempts
WHERE delivery_job_id = sqlc.arg(delivery_job_id)
ORDER BY attempt_number DESC, id DESC;

-- name: ManualRetryDeliveryJob :one
UPDATE outbox_events
SET status = 'pending',
    next_attempt_at = sqlc.arg(next_attempt_at),
    completed_at = NULL,
    manual_resolution = 'retry',
    resolution_reason = sqlc.arg(reason),
    resolved_by_admin_id = sqlc.arg(admin_id),
    resolved_at = sqlc.arg(resolved_at),
    last_error_code = NULL,
    last_error_detail = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND event_type = 'order.delivery_requested'
  AND status IN ('ambiguous', 'manual_review', 'permanent_failed', 'retryable_failed')
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: ManualCompleteDeliveryJob :one
UPDATE outbox_events
SET status = 'completed',
    telegram_message_id = sqlc.arg(telegram_message_id),
    telegram_sent_at = sqlc.arg(resolved_at),
    completed_at = sqlc.arg(resolved_at),
    manual_resolution = 'mark_delivered',
    resolution_reason = sqlc.arg(reason),
    resolved_by_admin_id = sqlc.arg(admin_id),
    resolved_at = sqlc.arg(resolved_at),
    last_error_code = NULL,
    last_error_detail = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND event_type = 'order.delivery_requested'
  AND status IN ('ambiguous', 'manual_review')
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: CountDeliveryReconciliationAnomalies :one
SELECT
    (
        SELECT count(*) FROM orders AS o
        WHERE o.status = 'delivering'
          AND NOT EXISTS (
              SELECT 1 FROM outbox_events AS job
              WHERE job.event_type = 'order.delivery_requested'
                AND job.delivery_order_id = o.id
          )
    )::bigint AS delivering_without_job,
    (
        SELECT count(*) FROM outbox_events AS job
        JOIN orders AS o ON o.id = job.delivery_order_id
        WHERE job.event_type = 'order.delivery_requested'
          AND job.status IN ('pending', 'processing', 'retryable_failed')
          AND o.status <> 'delivering'
    )::bigint AS active_job_wrong_order_state,
    (
        SELECT count(*) FROM outbox_events AS job
        JOIN orders AS o ON o.id = job.delivery_order_id
        WHERE job.event_type = 'order.delivery_requested'
          AND job.status = 'completed'
          AND o.status <> 'delivered'
    )::bigint AS completed_job_order_not_delivered,
    (
        SELECT count(*) FROM orders AS o
        WHERE o.status = 'delivered'
          AND (
              SELECT count(*) FROM inventory_items AS inventory
              WHERE inventory.sold_order_id = o.id AND inventory.status = 'sold'
          ) <> (
              SELECT COALESCE(sum(item.quantity), 0) FROM order_items AS item WHERE item.order_id = o.id
          )
    )::bigint AS delivered_inventory_mismatch,
    (
        SELECT count(*) FROM inventory_items AS inventory
        WHERE inventory.status = 'sold'
          AND NOT EXISTS (
              SELECT 1 FROM outbox_events AS job
              WHERE job.event_type = 'order.delivery_requested'
                AND job.delivery_order_id = inventory.sold_order_id
                AND job.status = 'completed'
          )
    )::bigint AS sold_without_completed_job,
    (
        SELECT count(*) FROM inventory_items AS inventory
        JOIN orders AS o ON o.id = inventory.reserved_order_id
        WHERE inventory.status = 'reserved' AND o.status = 'delivered'
    )::bigint AS delivered_order_reserved_inventory,
    (
        SELECT count(*) FROM (
            SELECT duplicate_job.delivery_order_id
            FROM outbox_events AS duplicate_job
            WHERE duplicate_job.event_type = 'order.delivery_requested'
              AND duplicate_job.status IN ('pending', 'processing', 'retryable_failed', 'ambiguous', 'manual_review')
            GROUP BY duplicate_job.delivery_order_id
            HAVING count(*) > 1
        ) AS duplicates
    )::bigint AS multiple_active_jobs,
    (
        SELECT count(*) FROM outbox_events AS stale_job
        WHERE stale_job.event_type = 'order.delivery_requested'
          AND stale_job.status = 'processing'
          AND stale_job.locked_at < sqlc.arg(stale_before)
    )::bigint AS stale_processing,
    (
        SELECT count(*) FROM outbox_events AS ambiguous_job
        WHERE ambiguous_job.event_type = 'order.delivery_requested'
          AND ambiguous_job.status = 'ambiguous'
          AND ambiguous_job.manual_resolution IS NULL
    )::bigint AS ambiguous_without_review,
    (
        SELECT count(*) FROM delivery_attempts AS attempt
        JOIN outbox_events AS job ON job.id = attempt.delivery_job_id
        WHERE attempt.status = 'succeeded'
          AND attempt.telegram_message_id IS NOT NULL
          AND job.status <> 'completed'
    )::bigint AS success_evidence_not_completed;
