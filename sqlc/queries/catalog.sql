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

-- name: CountActiveCategories :one
SELECT count(*)::bigint FROM categories WHERE is_active = true;

-- name: ListActiveCategoriesPage :many
SELECT *
FROM categories
WHERE is_active = true
ORDER BY sort_order, id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: CountActiveProductsByCategory :one
SELECT count(*)::bigint
FROM products
JOIN categories ON categories.id = products.category_id
WHERE products.category_id = sqlc.arg(category_id)
  AND products.is_active = true
  AND categories.is_active = true;

-- name: ListActiveProductsPage :many
SELECT products.*
FROM products
JOIN categories ON categories.id = products.category_id
WHERE products.category_id = sqlc.arg(category_id)
  AND products.is_active = true
  AND categories.is_active = true
ORDER BY products.id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: GetActiveProductDetail :one
SELECT products.*
FROM products
JOIN categories ON categories.id = products.category_id
WHERE products.id = sqlc.arg(product_id)
  AND products.is_active = true
  AND categories.is_active = true;

-- name: CountAdminCategories :one
SELECT count(*)::bigint FROM categories;

-- name: ListAdminCategoriesPage :many
SELECT *
FROM categories
ORDER BY sort_order, id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: CountAdminProducts :one
SELECT count(*)::bigint FROM products;

-- name: ListAdminProductsPage :many
SELECT *
FROM products
ORDER BY category_id, id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: GetCategoryByID :one
SELECT * FROM categories WHERE id = sqlc.arg(id);

-- name: LockCategoryByID :one
SELECT * FROM categories WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CreateCategory :one
INSERT INTO categories (name, slug, sort_order)
VALUES (sqlc.arg(name), sqlc.arg(slug), sqlc.arg(sort_order))
RETURNING *;

-- name: UpdateCategoryDetailsGuarded :one
UPDATE categories
SET name = sqlc.arg(name),
    sort_order = sqlc.arg(sort_order),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetCategoryActiveGuarded :one
UPDATE categories
SET is_active = sqlc.arg(is_active),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: GetProductByID :one
SELECT * FROM products WHERE id = sqlc.arg(id);

-- name: LockProductByID :one
SELECT * FROM products WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: CreateProduct :one
INSERT INTO products (
    category_id, name, slug, description, price_vnd, delivery_type, contact_url
) VALUES (
    sqlc.arg(category_id), sqlc.arg(name), sqlc.arg(slug), sqlc.narg(description),
    sqlc.arg(price_vnd), sqlc.arg(delivery_type), sqlc.narg(contact_url)
)
RETURNING *;

-- name: UpdateProductDetailsGuarded :one
UPDATE products
SET category_id = sqlc.arg(category_id),
    name = sqlc.arg(name),
    description = sqlc.narg(description),
    price_vnd = sqlc.arg(price_vnd),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: SetProductActiveGuarded :one
UPDATE products
SET is_active = sqlc.arg(is_active),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
RETURNING *;
