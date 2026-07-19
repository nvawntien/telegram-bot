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

-- name: LockUserForOrderCreation :one
SELECT * FROM users
WHERE telegram_user_id = sqlc.arg(telegram_user_id)
FOR SHARE;

-- name: GetProductForOrderCreation :one
SELECT products.id, products.name, products.price_vnd, products.delivery_type,
       products.is_active AS product_active,
       categories.is_active AS category_active
FROM products
JOIN categories ON categories.id = products.category_id
WHERE products.id = sqlc.arg(product_id)
FOR SHARE OF products, categories;

-- name: CountClaimableInventoryForOrder :one
SELECT count(*)::bigint
FROM inventory_items AS inventory
WHERE inventory.product_id = sqlc.arg(product_id)
  AND inventory.status = 'available'
  AND inventory.encryption_format = 'aes-256-gcm-v1'
  AND NOT EXISTS (
      SELECT 1 FROM order_inventory_items AS mapping
      WHERE mapping.inventory_item_id = inventory.id
        AND mapping.status = 'active'
  );

-- name: CreatePendingOrderWithBank :one
INSERT INTO orders (
    user_id, status, currency, subtotal_vnd, total_vnd,
    payment_reference, idempotency_key, expires_at,
    bank_account_id, bank_bin_snapshot, bank_name_snapshot,
    bank_display_name_snapshot, bank_account_name_snapshot,
    encrypted_account_number_snapshot, account_number_nonce_snapshot,
    account_encryption_format_snapshot, account_key_version_snapshot,
    account_last4_snapshot
) VALUES (
    sqlc.arg(user_id), 'pending_payment', 'VND',
    sqlc.arg(subtotal_vnd), sqlc.arg(total_vnd),
    sqlc.arg(payment_reference), sqlc.arg(idempotency_key),
    sqlc.arg(expires_at), sqlc.arg(bank_account_id),
    sqlc.arg(bank_bin_snapshot), sqlc.arg(bank_name_snapshot),
    sqlc.arg(bank_display_name_snapshot), sqlc.arg(bank_account_name_snapshot),
    sqlc.arg(encrypted_account_number_snapshot),
    sqlc.arg(account_number_nonce_snapshot), 'aes-256-gcm-v1',
    sqlc.arg(account_key_version_snapshot), sqlc.arg(account_last4_snapshot)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: FindOrderByUserIdempotency :one
SELECT * FROM orders
WHERE user_id = sqlc.arg(user_id)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: FindOrderByPaymentReference :one
SELECT * FROM orders
WHERE payment_reference = sqlc.arg(payment_reference);

-- name: CountOrdersOwnedByTelegramUser :one
SELECT count(*)::bigint
FROM orders
JOIN users ON users.id = orders.user_id
WHERE users.telegram_user_id = sqlc.arg(telegram_user_id);

-- name: ListOrdersOwnedByTelegramUser :many
SELECT orders.id, orders.status, orders.total_vnd, orders.payment_reference,
       orders.expires_at, orders.version, orders.created_at,
       item.product_name, item.quantity
FROM orders
JOIN users ON users.id = orders.user_id
JOIN LATERAL (
    SELECT product_name, quantity
    FROM order_items
    WHERE order_id = orders.id
    ORDER BY id
    LIMIT 1
) AS item ON true
WHERE users.telegram_user_id = sqlc.arg(telegram_user_id)
ORDER BY orders.created_at DESC, orders.id DESC
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: GetOrderDetailOwnedByTelegramUser :one
SELECT orders.*, item.id AS order_item_id, item.product_id,
       item.product_name, item.unit_price_vnd, item.quantity,
       item.line_total_vnd
FROM orders
JOIN users ON users.id = orders.user_id
JOIN LATERAL (
    SELECT * FROM order_items
    WHERE order_id = orders.id
    ORDER BY id
    LIMIT 1
) AS item ON true
WHERE orders.id = sqlc.arg(order_id)
  AND users.telegram_user_id = sqlc.arg(telegram_user_id);

-- name: LockOrderDetailOwnedByTelegramUser :one
SELECT orders.*, item.id AS order_item_id, item.product_id,
       item.product_name, item.unit_price_vnd, item.quantity,
       item.line_total_vnd
FROM orders
JOIN users ON users.id = orders.user_id
JOIN LATERAL (
    SELECT * FROM order_items
    WHERE order_id = orders.id
    ORDER BY id
    LIMIT 1
) AS item ON true
WHERE orders.id = sqlc.arg(order_id)
  AND users.telegram_user_id = sqlc.arg(telegram_user_id)
FOR UPDATE OF orders;

-- name: CancelPendingOrderOwnedGuarded :one
UPDATE orders
SET status = 'cancelled',
    cancelled_at = clock_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND user_id = sqlc.arg(user_id)
  AND status = 'pending_payment'
  AND expires_at > sqlc.arg(now)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: ClaimOverduePendingOrders :many
WITH selected_orders AS (
    SELECT id
    FROM orders
    WHERE status = 'pending_payment'
      AND expires_at <= sqlc.arg(now)
    ORDER BY expires_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)::integer
)
UPDATE orders
SET status = 'expired',
    version = version + 1
FROM selected_orders
WHERE orders.id = selected_orders.id
  AND orders.status = 'pending_payment'
  AND orders.expires_at <= sqlc.arg(now)
RETURNING orders.*;

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
