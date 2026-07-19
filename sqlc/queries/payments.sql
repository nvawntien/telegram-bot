-- name: InsertPaymentEvent :one
INSERT INTO payment_events (
    provider,
    external_event_id,
    provider_transaction_id,
    event_type,
    payload_hash,
    sanitized_payload,
    signature_verified,
    processing_status,
    max_attempts
) VALUES (
    sqlc.arg(provider),
    sqlc.arg(external_event_id),
    sqlc.narg(provider_transaction_id),
    sqlc.arg(event_type),
    sqlc.arg(payload_hash),
    sqlc.arg(sanitized_payload),
    sqlc.arg(signature_verified),
    'received',
    sqlc.arg(max_attempts)
)
ON CONFLICT (provider, external_event_id) DO NOTHING
RETURNING *;

-- name: GetPaymentEventByProviderEventID :one
SELECT * FROM payment_events
WHERE provider = sqlc.arg(provider) AND external_event_id = sqlc.arg(external_event_id);

-- name: ClaimPaymentEvents :many
WITH selected AS (
    SELECT candidate.id
    FROM payment_events AS candidate
    WHERE (
        (candidate.processing_status = 'received' AND candidate.next_attempt_at <= sqlc.arg(claimed_at))
        OR (candidate.processing_status = 'processing' AND candidate.processing_started_at <= sqlc.arg(stale_before))
    )
    AND candidate.attempts < candidate.max_attempts
    ORDER BY candidate.received_at, candidate.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)::integer
)
UPDATE payment_events AS event
SET processing_status = 'processing',
    attempts = event.attempts + 1,
    processing_started_at = sqlc.arg(claimed_at),
    processed_at = NULL,
    processing_error = NULL,
    last_error_code = NULL
FROM selected
WHERE event.id = selected.id
RETURNING event.*;

-- name: GetPaymentEventByID :one
SELECT * FROM payment_events WHERE id = sqlc.arg(id);

-- name: LockPaymentEvent :one
SELECT * FROM payment_events WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CompletePaymentEvent :one
UPDATE payment_events
SET processing_status = sqlc.arg(processing_status),
    related_order_id = sqlc.narg(related_order_id),
    related_wallet_topup_id = sqlc.narg(related_wallet_topup_id),
    processing_started_at = NULL,
    processed_at = sqlc.arg(processed_at),
    processing_error = sqlc.narg(processing_error),
    last_error_code = sqlc.narg(last_error_code)
WHERE id = sqlc.arg(id) AND processing_status = 'processing'
RETURNING *;

-- name: SchedulePaymentEventRetry :one
UPDATE payment_events
SET processing_status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'received' END,
    next_attempt_at = sqlc.arg(next_attempt_at),
    processing_started_at = NULL,
    processed_at = CASE WHEN attempts >= max_attempts THEN sqlc.arg(processed_at) ELSE NULL END,
    processing_error = sqlc.arg(processing_error),
    last_error_code = sqlc.arg(last_error_code)
WHERE id = sqlc.arg(id) AND processing_status = 'processing'
RETURNING *;

-- name: InsertPayment :one
INSERT INTO payments (
    order_id,
    user_id,
    purpose,
    provider,
    provider_transaction_id,
    payment_reference,
    amount_vnd,
    currency,
    status,
    confirmed_at,
    occurred_at
) VALUES (
    sqlc.narg(order_id),
    sqlc.arg(user_id),
    sqlc.arg(purpose),
    sqlc.arg(provider),
    sqlc.narg(provider_transaction_id),
    sqlc.arg(payment_reference),
    sqlc.arg(amount_vnd),
    'VND',
    sqlc.arg(status),
    sqlc.narg(confirmed_at),
    sqlc.arg(occurred_at)
)
RETURNING *;

-- name: GetPaymentByProviderTransaction :one
SELECT * FROM payments
WHERE provider = sqlc.arg(provider) AND provider_transaction_id = sqlc.arg(provider_transaction_id);

-- name: InsertPaymentAllocation :one
INSERT INTO payment_allocations (payment_id, target_type, target_id, amount_vnd)
VALUES (sqlc.arg(payment_id), sqlc.arg(target_type), sqlc.arg(target_id), sqlc.arg(amount_vnd))
RETURNING *;

-- name: GetPaymentAllocation :one
SELECT * FROM payment_allocations WHERE payment_id = sqlc.arg(payment_id);

-- name: InsertPaymentReviewCase :one
INSERT INTO payment_review_cases (
    payment_event_id, payment_id, order_id, wallet_topup_id,
    provider, provider_transaction_id, payment_reference, amount_vnd,
    currency, occurred_at, reason
) VALUES (
    sqlc.narg(payment_event_id), sqlc.narg(payment_id), sqlc.narg(order_id),
    sqlc.narg(wallet_topup_id), sqlc.arg(provider), sqlc.narg(provider_transaction_id),
    sqlc.arg(payment_reference), sqlc.arg(amount_vnd), sqlc.arg(currency),
    sqlc.arg(occurred_at), sqlc.arg(reason)
)
ON CONFLICT (payment_event_id) DO UPDATE SET reason = EXCLUDED.reason
RETURNING *;

-- name: CountOpenPaymentReviews :one
SELECT count(*)::bigint FROM payment_review_cases WHERE status IN ('open', 'held');

-- name: ListOpenPaymentReviews :many
SELECT * FROM payment_review_cases
WHERE status IN ('open', 'held')
ORDER BY created_at, id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: ResolvePaymentReview :one
UPDATE payment_review_cases
SET status = sqlc.arg(status), resolution_note = sqlc.arg(resolution_note),
    resolved_by_admin_id = CASE WHEN sqlc.arg(status)::text = 'resolved' THEN sqlc.arg(admin_id) ELSE NULL END,
    resolved_at = CASE WHEN sqlc.arg(status)::text = 'resolved' THEN clock_timestamp() ELSE NULL END
WHERE id = sqlc.arg(id) AND status IN ('open', 'held')
RETURNING *;
