-- name: ListActiveCategories :many
SELECT *
FROM categories
WHERE is_active = true
ORDER BY sort_order, id;

-- name: ListActiveProductsByCategory :many
SELECT *
FROM products
WHERE category_id = sqlc.arg(category_id)
  AND is_active = true
ORDER BY id;
