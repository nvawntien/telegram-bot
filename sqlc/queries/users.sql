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
SET username = EXCLUDED.username,
    display_name = EXCLUDED.display_name,
    last_seen_at = clock_timestamp()
RETURNING *;

-- name: GetUserByTelegramID :one
SELECT *
FROM users
WHERE telegram_user_id = sqlc.arg(telegram_user_id);
