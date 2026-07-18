-- name: CountAvailableInventoryByProduct :one
SELECT count(*)::bigint
FROM inventory_items
WHERE product_id = sqlc.arg(product_id)
  AND status = 'available';

-- name: ClaimAvailableInventory :many
WITH selected_items AS (
    SELECT available.id
    FROM inventory_items AS available
    WHERE available.product_id = sqlc.arg(product_id)
      AND available.status = 'available'
    ORDER BY available.created_at, available.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(quantity)::integer
)
UPDATE inventory_items AS inventory
SET status = 'reserved',
    reserved_order_id = sqlc.arg(order_id),
    reserved_until = sqlc.arg(reserved_until)
FROM selected_items
WHERE inventory.id = selected_items.id
RETURNING inventory.*;
