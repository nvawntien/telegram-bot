-- name: CountAvailableInventoryByProduct :one
SELECT count(*)::bigint
FROM inventory_items
WHERE product_id = sqlc.arg(product_id)
  AND status = 'available';

-- name: CountInventoryOverviewProducts :one
SELECT count(*)::bigint
FROM products
WHERE delivery_type = 'inventory';

-- name: ListInventoryOverviewPage :many
SELECT
    products.id AS product_id,
    products.name AS product_name,
    count(inventory.id) FILTER (WHERE inventory.status = 'available')::bigint AS available_count,
    count(inventory.id) FILTER (WHERE inventory.status = 'reserved')::bigint AS reserved_count,
    count(inventory.id) FILTER (WHERE inventory.status = 'sold')::bigint AS sold_count,
    count(inventory.id) FILTER (WHERE inventory.status = 'disabled')::bigint AS disabled_count,
    count(inventory.id)::bigint AS total_count
FROM products
LEFT JOIN inventory_items AS inventory ON inventory.product_id = products.id
WHERE products.delivery_type = 'inventory'
GROUP BY products.id, products.name, products.created_at
ORDER BY products.created_at, products.id
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountRedactedInventoryByProduct :one
SELECT count(*)::bigint
FROM inventory_items
WHERE product_id = sqlc.arg(product_id);

-- name: ListRedactedInventoryPage :many
SELECT
    inventory.id,
    inventory.product_id,
    products.name AS product_name,
    inventory.status,
    inventory.reserved_order_id,
    inventory.reserved_until,
    inventory.encryption_key_version,
    inventory.version,
    inventory.created_at
FROM inventory_items AS inventory
JOIN products ON products.id = inventory.product_id
WHERE inventory.product_id = sqlc.arg(product_id)
ORDER BY inventory.created_at, inventory.id
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: InsertEncryptedInventoryItem :one
INSERT INTO inventory_items (
    product_id,
    encrypted_payload,
    encryption_key_id,
    encryption_nonce,
    encryption_format,
    encryption_key_version,
    payload_fingerprint,
    imported_by_admin_id
) VALUES (
    sqlc.arg(product_id),
    sqlc.arg(encrypted_payload),
    sqlc.arg(encryption_key_id),
    sqlc.arg(encryption_nonce),
    'aes-256-gcm-v1',
    sqlc.arg(encryption_key_version),
    sqlc.arg(payload_fingerprint),
    sqlc.arg(imported_by_admin_id)
)
ON CONFLICT (product_id, payload_fingerprint) DO NOTHING
RETURNING id;

-- name: GetEncryptedInventoryItem :one
SELECT *
FROM inventory_items
WHERE id = sqlc.arg(id);

-- name: LockRedactedInventoryItem :one
SELECT id, product_id, status, reserved_order_id, reserved_until,
       encryption_key_version, version, created_at
FROM inventory_items
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: DisableAvailableInventoryItem :one
UPDATE inventory_items
SET status = 'disabled',
    disabled_reason = sqlc.arg(disabled_reason),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'available'
  AND version = sqlc.arg(expected_version)
RETURNING id, product_id, status, reserved_order_id, reserved_until,
          encryption_key_version, version, created_at;

-- name: EnableDisabledInventoryItem :one
UPDATE inventory_items
SET status = 'available',
    disabled_reason = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND status = 'disabled'
  AND version = sqlc.arg(expected_version)
RETURNING id, product_id, status, reserved_order_id, reserved_until,
          encryption_key_version, version, created_at;

-- name: ClaimAvailableInventory :many
WITH selected_items AS (
    SELECT available.id
    FROM inventory_items AS available
    WHERE available.product_id = sqlc.arg(product_id)
      AND available.status = 'available'
      AND NOT EXISTS (
          SELECT 1
          FROM order_inventory_items AS mapping
          WHERE mapping.inventory_item_id = available.id
            AND mapping.status = 'active'
      )
    ORDER BY available.created_at, available.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(quantity)::integer
)
UPDATE inventory_items AS inventory
SET status = 'reserved',
    reserved_order_id = sqlc.arg(order_id),
    reserved_until = sqlc.arg(reserved_until),
    version = version + 1
FROM selected_items
WHERE inventory.id = selected_items.id
RETURNING inventory.id;

-- name: LockOrderItemForInventory :one
SELECT *
FROM order_items
WHERE id = sqlc.arg(order_item_id)
  AND order_id = sqlc.arg(order_id)
  AND product_id = sqlc.arg(product_id)
FOR SHARE;

-- name: InsertOrderInventoryMapping :exec
INSERT INTO order_inventory_items (
    order_id,
    order_item_id,
    inventory_item_id,
    status
) VALUES (
    sqlc.arg(order_id),
    sqlc.arg(order_item_id),
    sqlc.arg(inventory_item_id),
    'active'
);

-- name: ListReservedInventoryIDsByOrder :many
SELECT id
FROM inventory_items
WHERE reserved_order_id = sqlc.arg(order_id)
  AND status = 'reserved'
ORDER BY id
FOR UPDATE;

-- name: CountExpiredReservationsByOrder :one
SELECT count(*)::bigint
FROM inventory_items
WHERE reserved_order_id = sqlc.arg(order_id)
  AND status = 'reserved'
  AND reserved_until <= sqlc.arg(expired_at);

-- name: ReleaseReservedInventoryByOrder :many
UPDATE inventory_items
SET status = 'available',
    reserved_order_id = NULL,
    reserved_until = NULL,
    version = version + 1
WHERE reserved_order_id = sqlc.arg(order_id)
  AND status = 'reserved'
RETURNING id;

-- name: MarkOrderInventoryMappingsReleased :execrows
UPDATE order_inventory_items
SET status = 'released',
    released_at = clock_timestamp(),
    release_reason = sqlc.arg(release_reason)
WHERE order_id = sqlc.arg(order_id)
  AND inventory_item_id = ANY(sqlc.arg(inventory_item_ids)::bigint[])
  AND status = 'active';

-- name: CountActiveInventoryMappingsByOrder :one
SELECT count(*)::bigint
FROM order_inventory_items
WHERE order_id = sqlc.arg(order_id)
  AND status = 'active';

-- name: InsertInventoryRecoveryAuditOnce :one
INSERT INTO audit_logs (
    actor_type,
    action,
    resource_type,
    resource_id,
    after_data,
    request_id
)
SELECT
    'system',
    'inventory.reservation_recovery_required',
    'order',
    sqlc.arg(order_id)::bigint,
    jsonb_build_object(
        'order_id', sqlc.arg(order_id)::bigint,
        'order_status', sqlc.arg(order_status)::text
    ),
    sqlc.narg(request_id)
WHERE NOT EXISTS (
    SELECT 1
    FROM audit_logs
    WHERE action = 'inventory.reservation_recovery_required'
      AND resource_type = 'order'
      AND resource_id = sqlc.arg(order_id)::bigint
      AND after_data ->> 'order_status' = sqlc.arg(order_status)::text
)
RETURNING id;
