-- name: UpsertTelegramUser :one
INSERT INTO users (
    telegram_user_id,
    username,
    display_name,
    last_seen_at
) VALUES (
    sqlc.arg(telegram_user_id),
    sqlc.narg(username),
    sqlc.narg(display_name),
    clock_timestamp()
)
ON CONFLICT (telegram_user_id) DO UPDATE
SET username = COALESCE(EXCLUDED.username, users.username),
    display_name = COALESCE(EXCLUDED.display_name, users.display_name),
    last_seen_at = clock_timestamp()
RETURNING *;

-- name: EnsureBootstrapUser :one
INSERT INTO users (telegram_user_id, last_seen_at)
VALUES (sqlc.arg(telegram_user_id), clock_timestamp())
ON CONFLICT (telegram_user_id) DO UPDATE
SET last_seen_at = clock_timestamp()
RETURNING *;

-- name: EnsureBootstrapAdmin :execrows
INSERT INTO admins (user_id, role)
VALUES (sqlc.arg(user_id), 'owner')
ON CONFLICT (user_id) DO NOTHING;

-- name: GetAdminAuthorizationByTelegramID :one
SELECT
    admins.id AS admin_id,
    admins.user_id,
    users.telegram_user_id,
    users.status AS user_status,
    admins.role,
    admins.is_active
FROM admins
JOIN users ON users.id = admins.user_id
WHERE users.telegram_user_id = sqlc.arg(telegram_user_id);

-- name: GetUserByTelegramID :one
SELECT *
FROM users
WHERE telegram_user_id = sqlc.arg(telegram_user_id);

-- name: LockUserByTelegramID :one
SELECT * FROM users
WHERE telegram_user_id = sqlc.arg(telegram_user_id)
FOR UPDATE;
