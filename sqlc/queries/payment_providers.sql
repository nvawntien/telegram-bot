-- name: CreatePaymentProviderAccount :one
INSERT INTO payment_provider_accounts (
    provider, environment, external_account_identity,
    external_identity_fingerprint, local_bank_account_id
) VALUES (
    sqlc.arg(provider), sqlc.arg(environment), sqlc.arg(external_account_identity),
    sqlc.arg(external_identity_fingerprint), sqlc.arg(local_bank_account_id)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: CountPaymentProviderAccounts :one
SELECT count(*)::bigint
FROM payment_provider_accounts;

-- name: ListPaymentProviderAccounts :many
SELECT mapping.*, bank.display_name AS bank_display_name, bank.display_last4 AS bank_last4
FROM payment_provider_accounts AS mapping
JOIN bank_accounts AS bank ON bank.id = mapping.local_bank_account_id
ORDER BY mapping.provider, mapping.environment, mapping.id
LIMIT sqlc.arg(page_limit)::integer OFFSET sqlc.arg(page_offset)::integer;

-- name: LockPaymentProviderAccount :one
SELECT * FROM payment_provider_accounts
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: SetPaymentProviderAccountStatusGuarded :one
UPDATE payment_provider_accounts
SET status = sqlc.arg(status), version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND status <> sqlc.arg(status)
RETURNING *;

-- name: ResolveActivePaymentProviderAccount :one
SELECT * FROM payment_provider_accounts
WHERE provider = sqlc.arg(provider)
  AND environment = sqlc.arg(environment)
  AND external_account_identity = sqlc.arg(external_account_identity)
  AND status = 'active';

-- name: AttachPaymentEventProviderAccount :one
UPDATE payment_events
SET provider_account_mapping_id = sqlc.arg(provider_account_mapping_id)
WHERE id = sqlc.arg(id)
  AND processing_status = 'processing'
  AND provider_account_mapping_id IS NULL
RETURNING *;

-- name: ListActivePaymentProviderAccounts :many
SELECT mapping.*, bank.display_name AS bank_display_name, bank.display_last4 AS bank_last4
FROM payment_provider_accounts AS mapping
JOIN bank_accounts AS bank ON bank.id = mapping.local_bank_account_id
WHERE mapping.status = 'active'
ORDER BY mapping.provider, mapping.environment, mapping.id;

-- name: EnsurePaymentProviderCheckpoint :one
INSERT INTO payment_provider_checkpoints (provider_account_id)
VALUES (sqlc.arg(provider_account_id))
ON CONFLICT (provider_account_id) DO UPDATE
SET provider_account_id = EXCLUDED.provider_account_id
RETURNING *;

-- name: ClaimPaymentProviderCheckpoint :one
UPDATE payment_provider_checkpoints
SET lease_owner = sqlc.arg(lease_owner),
    lease_expires_at = sqlc.arg(lease_expires_at),
    last_attempted_at = sqlc.arg(attempted_at),
    last_error_code = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND (lease_owner IS NULL OR lease_expires_at <= sqlc.arg(attempted_at))
RETURNING *;

-- name: AdvancePaymentProviderCheckpoint :one
UPDATE payment_provider_checkpoints
SET cursor_value = sqlc.narg(cursor_value),
    last_transaction_external_id = sqlc.narg(last_transaction_external_id),
    last_occurred_at = sqlc.narg(last_occurred_at),
    last_error_code = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND lease_owner = sqlc.arg(expected_lease_owner)
RETURNING *;

-- name: CompletePaymentProviderCheckpoint :one
UPDATE payment_provider_checkpoints
SET last_successful_at = sqlc.arg(completed_at),
    last_error_code = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND lease_owner = sqlc.arg(expected_lease_owner)
RETURNING *;

-- name: RecordPaymentProviderCheckpointFailure :one
UPDATE payment_provider_checkpoints
SET last_error_code = sqlc.arg(last_error_code),
    lease_owner = NULL,
    lease_expires_at = NULL,
    version = version + 1
WHERE id = sqlc.arg(id)
  AND version = sqlc.arg(expected_version)
  AND lease_owner = sqlc.arg(expected_lease_owner)
RETURNING *;

-- name: GetPaymentProviderCheckpoint :one
SELECT * FROM payment_provider_checkpoints
WHERE provider_account_id = sqlc.arg(provider_account_id);

-- name: CountProviderPendingEvents :one
SELECT count(*)::bigint FROM payment_events
WHERE provider = sqlc.arg(provider)
  AND payment_environment = sqlc.arg(environment)
  AND processing_status IN ('received', 'processing');

-- name: CountProviderOpenReviews :one
SELECT count(*)::bigint FROM payment_review_cases
WHERE provider = sqlc.arg(provider)
  AND payment_environment = sqlc.arg(environment)
  AND status IN ('open', 'held');

-- name: GetProviderLastWebhookAt :one
SELECT max(received_at)::timestamptz FROM payment_events
WHERE provider = sqlc.arg(provider)
  AND payment_environment = sqlc.arg(environment)
  AND event_source = 'webhook';

-- name: GetPaymentProviderHealth :one
SELECT
    (SELECT count(*)::bigint FROM payment_provider_accounts AS mapping
     WHERE mapping.provider = sqlc.arg(provider)
       AND mapping.environment = sqlc.arg(environment)
       AND mapping.status = 'active') AS active_mappings,
    (SELECT max(event.received_at)::timestamptz FROM payment_events AS event
     WHERE event.provider = sqlc.arg(provider)
       AND event.payment_environment = sqlc.arg(environment)
       AND event.event_source = 'webhook') AS last_webhook_at,
    (SELECT max(checkpoint.last_attempted_at)::timestamptz
     FROM payment_provider_checkpoints AS checkpoint
     JOIN payment_provider_accounts AS mapping ON mapping.id = checkpoint.provider_account_id
     WHERE mapping.provider = sqlc.arg(provider) AND mapping.environment = sqlc.arg(environment)) AS last_reconciliation_attempt,
    (SELECT max(checkpoint.last_successful_at)::timestamptz
     FROM payment_provider_checkpoints AS checkpoint
     JOIN payment_provider_accounts AS mapping ON mapping.id = checkpoint.provider_account_id
     WHERE mapping.provider = sqlc.arg(provider) AND mapping.environment = sqlc.arg(environment)) AS last_reconciliation_success,
    (SELECT checkpoint.last_error_code
     FROM payment_provider_checkpoints AS checkpoint
     JOIN payment_provider_accounts AS mapping ON mapping.id = checkpoint.provider_account_id
     WHERE mapping.provider = sqlc.arg(provider) AND mapping.environment = sqlc.arg(environment)
       AND checkpoint.last_error_code IS NOT NULL
     ORDER BY checkpoint.last_attempted_at DESC NULLS LAST, checkpoint.id DESC LIMIT 1) AS last_error_code,
    (SELECT max(checkpoint.last_occurred_at)::timestamptz
     FROM payment_provider_checkpoints AS checkpoint
     JOIN payment_provider_accounts AS mapping ON mapping.id = checkpoint.provider_account_id
     WHERE mapping.provider = sqlc.arg(provider) AND mapping.environment = sqlc.arg(environment)) AS last_transaction_at,
    (SELECT count(*)::bigint FROM payment_events AS event
     WHERE event.provider = sqlc.arg(provider)
       AND event.payment_environment = sqlc.arg(environment)
       AND event.processing_status IN ('received', 'processing')) AS pending_events,
    (SELECT count(*)::bigint FROM payment_review_cases AS review
     WHERE review.provider = sqlc.arg(provider)
       AND review.payment_environment = sqlc.arg(environment)
       AND review.status IN ('open', 'held')) AS open_reviews;

-- name: CountPaymentReferenceTargets :one
SELECT (
    (SELECT count(*) FROM orders AS target_order WHERE target_order.payment_reference = sqlc.arg(reference_value))
    + (SELECT count(*) FROM wallet_topup_intents AS target_topup WHERE target_topup.payment_reference = sqlc.arg(reference_value))
)::bigint AS target_count;
