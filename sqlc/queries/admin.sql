-- name: StartAdminSession :one
INSERT INTO admin_sessions (admin_id, state, payload, expires_at)
VALUES (sqlc.arg(admin_id), sqlc.arg(state), sqlc.arg(payload), sqlc.arg(expires_at))
ON CONFLICT (admin_id) DO UPDATE
SET state = EXCLUDED.state,
    payload = EXCLUDED.payload,
    expires_at = EXCLUDED.expires_at,
    version = admin_sessions.version + 1
RETURNING *;

-- name: GetActiveAdminSession :one
SELECT *
FROM admin_sessions
WHERE admin_id = sqlc.arg(admin_id)
  AND expires_at > clock_timestamp()
  AND state NOT IN ('completed', 'cancelled');

-- name: LockAdminSessionByID :one
SELECT *
FROM admin_sessions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: AdvanceAdminSessionGuarded :one
UPDATE admin_sessions
SET state = sqlc.arg(state),
    payload = sqlc.arg(payload),
    expires_at = sqlc.arg(expires_at),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND admin_id = sqlc.arg(admin_id)
  AND version = sqlc.arg(expected_version)
  AND expires_at > clock_timestamp()
  AND state NOT IN ('completed', 'cancelled')
RETURNING *;

-- name: FinishAdminSessionGuarded :one
UPDATE admin_sessions
SET state = sqlc.arg(state),
    payload = '{}'::jsonb,
    expires_at = clock_timestamp(),
    version = version + 1
WHERE id = sqlc.arg(id)
  AND admin_id = sqlc.arg(admin_id)
  AND version = sqlc.arg(expected_version)
  AND expires_at > clock_timestamp()
  AND state NOT IN ('completed', 'cancelled')
RETURNING *;

-- name: InsertAuditLog :one
INSERT INTO audit_logs (
    actor_type, actor_id, action, resource_type, resource_id,
    before_data, after_data, request_id, telegram_update_id
) VALUES (
    sqlc.arg(actor_type), sqlc.narg(actor_id), sqlc.arg(action),
    sqlc.arg(resource_type), sqlc.narg(resource_id), sqlc.narg(before_data),
    sqlc.narg(after_data), sqlc.narg(request_id), sqlc.narg(telegram_update_id)
)
RETURNING *;
