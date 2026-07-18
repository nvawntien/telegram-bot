-- name: CreatePendingOrder :one
INSERT INTO orders (
    user_id,
    status,
    currency,
    subtotal_vnd,
    total_vnd,
    payment_reference,
    idempotency_key,
    expires_at
) VALUES (
    sqlc.arg(user_id),
    'pending_payment',
    'VND',
    sqlc.arg(subtotal_vnd),
    sqlc.arg(total_vnd),
    sqlc.arg(payment_reference),
    sqlc.arg(idempotency_key),
    sqlc.arg(expires_at)
)
RETURNING *;

-- name: InsertOrderItem :one
INSERT INTO order_items (
    order_id,
    product_id,
    product_name,
    unit_price_vnd,
    quantity,
    line_total_vnd
) VALUES (
    sqlc.arg(order_id),
    sqlc.arg(product_id),
    sqlc.arg(product_name),
    sqlc.arg(unit_price_vnd),
    sqlc.arg(quantity),
    sqlc.arg(line_total_vnd)
)
RETURNING *;

-- name: GetOrderByID :one
SELECT *
FROM orders
WHERE id = sqlc.arg(id);

-- name: GetOrderOwnedByUser :one
SELECT *
FROM orders
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id);

-- name: LockOrderForUpdate :one
SELECT *
FROM orders
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: UpdateOrderStatusGuarded :one
UPDATE orders
SET status = sqlc.arg(new_status),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = sqlc.arg(expected_status)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: InsertOrderStatusHistory :one
INSERT INTO order_status_history (
    order_id,
    from_status,
    to_status,
    reason_code,
    actor_type,
    actor_id,
    request_id
) VALUES (
    sqlc.arg(order_id),
    sqlc.narg(from_status),
    sqlc.arg(to_status),
    sqlc.narg(reason_code),
    sqlc.arg(actor_type),
    sqlc.narg(actor_id),
    sqlc.narg(request_id)
)
RETURNING *;
