-- name: InsertPaymentEvent :one
INSERT INTO payment_events (
    provider,
    external_event_id,
    provider_transaction_id,
    event_type,
    payload_hash,
    sanitized_payload,
    signature_verified,
    processing_status
) VALUES (
    sqlc.arg(provider),
    sqlc.arg(external_event_id),
    sqlc.narg(provider_transaction_id),
    sqlc.arg(event_type),
    sqlc.arg(payload_hash),
    sqlc.arg(sanitized_payload),
    sqlc.arg(signature_verified),
    'received'
)
ON CONFLICT (provider, external_event_id) DO NOTHING
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
    confirmed_at
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
    sqlc.narg(confirmed_at)
)
RETURNING *;
