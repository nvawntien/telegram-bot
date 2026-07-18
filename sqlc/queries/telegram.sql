-- name: InsertTelegramUpdateReceipt :execrows
INSERT INTO telegram_update_receipts (update_id, update_type)
VALUES (sqlc.arg(update_id), sqlc.arg(update_type))
ON CONFLICT (update_id) DO NOTHING;

-- name: LockTelegramUpdateReceipt :one
SELECT *
FROM telegram_update_receipts
WHERE update_id = sqlc.arg(update_id)
FOR UPDATE;

-- name: StartTelegramUpdateProcessing :one
UPDATE telegram_update_receipts
SET status = 'processing',
    attempts = attempts + 1,
    processing_started_at = clock_timestamp(),
    processed_at = NULL,
    last_error = NULL
WHERE update_id = sqlc.arg(update_id)
  AND (
      status IN ('received', 'failed')
      OR (status = 'processing' AND processing_started_at <= sqlc.arg(stale_before))
  )
RETURNING *;

-- name: CompleteTelegramUpdateReceipt :one
UPDATE telegram_update_receipts
SET status = 'completed',
    processed_at = clock_timestamp(),
    last_error = NULL
WHERE update_id = sqlc.arg(update_id)
  AND status = 'processing'
RETURNING *;

-- name: FailTelegramUpdateReceipt :one
UPDATE telegram_update_receipts
SET status = 'failed',
    processed_at = clock_timestamp(),
    last_error = sqlc.arg(last_error)
WHERE update_id = sqlc.arg(update_id)
  AND status = 'processing'
RETURNING *;

-- name: GetTelegramUpdateReceipt :one
SELECT *
FROM telegram_update_receipts
WHERE update_id = sqlc.arg(update_id);
