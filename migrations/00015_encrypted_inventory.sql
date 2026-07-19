-- +goose Up
ALTER TABLE inventory_items
ADD COLUMN encryption_nonce bytea,
ADD COLUMN encryption_format text NOT NULL DEFAULT 'legacy-v0',
ADD COLUMN encryption_key_version integer NOT NULL DEFAULT 1,
ADD COLUMN imported_by_admin_id bigint,
ADD COLUMN version bigint NOT NULL DEFAULT 1,
ADD CONSTRAINT inventory_items_imported_by_admin_fk
    FOREIGN KEY (imported_by_admin_id) REFERENCES admins (id) ON DELETE RESTRICT,
ADD CONSTRAINT inventory_items_encryption_format_valid
    CHECK (encryption_format IN ('legacy-v0', 'aes-256-gcm-v1')),
ADD CONSTRAINT inventory_items_encryption_envelope_consistent CHECK (
    (encryption_format = 'legacy-v0' AND encryption_nonce IS NULL)
    OR (
        encryption_format = 'aes-256-gcm-v1'
        AND octet_length(encryption_nonce) = 12
        AND octet_length(encrypted_payload) >= 16
    )
),
ADD CONSTRAINT inventory_items_key_version_positive CHECK (encryption_key_version > 0),
ADD CONSTRAINT inventory_items_version_positive CHECK (version > 0);

CREATE INDEX inventory_items_reserved_order_idx
ON inventory_items (reserved_order_id, id)
WHERE status = 'reserved';

ALTER TABLE order_inventory_items
DROP CONSTRAINT order_inventory_items_inventory_unique,
ADD COLUMN status text NOT NULL DEFAULT 'active',
ADD COLUMN released_at timestamptz,
ADD COLUMN release_reason text,
ADD CONSTRAINT order_inventory_items_status_valid
    CHECK (status IN ('active', 'released')),
ADD CONSTRAINT order_inventory_items_release_reason_not_blank
    CHECK (release_reason IS NULL OR btrim(release_reason) <> ''),
ADD CONSTRAINT order_inventory_items_state_consistent CHECK (
    (status = 'active' AND released_at IS NULL AND release_reason IS NULL)
    OR (status = 'released' AND released_at IS NOT NULL AND release_reason IS NOT NULL)
);

CREATE UNIQUE INDEX order_inventory_items_active_inventory_unique
ON order_inventory_items (inventory_item_id)
WHERE status = 'active';

CREATE INDEX order_inventory_items_order_status_idx
ON order_inventory_items (order_id, status, inventory_item_id);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM inventory_items
        WHERE encryption_format <> 'legacy-v0'
           OR imported_by_admin_id IS NOT NULL
           OR version <> 1
    ) THEN
        RAISE EXCEPTION 'cannot remove Phase 4 inventory metadata while Phase 4 inventory rows exist';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM order_inventory_items
        WHERE status <> 'active'
    ) THEN
        RAISE EXCEPTION 'cannot remove Phase 4 reservation history while released mappings exist';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS order_inventory_items_order_status_idx;
DROP INDEX IF EXISTS order_inventory_items_active_inventory_unique;

ALTER TABLE order_inventory_items
DROP CONSTRAINT IF EXISTS order_inventory_items_state_consistent,
DROP CONSTRAINT IF EXISTS order_inventory_items_release_reason_not_blank,
DROP CONSTRAINT IF EXISTS order_inventory_items_status_valid,
DROP COLUMN IF EXISTS release_reason,
DROP COLUMN IF EXISTS released_at,
DROP COLUMN IF EXISTS status,
ADD CONSTRAINT order_inventory_items_inventory_unique UNIQUE (inventory_item_id);

DROP INDEX IF EXISTS inventory_items_reserved_order_idx;

ALTER TABLE inventory_items
DROP CONSTRAINT IF EXISTS inventory_items_version_positive,
DROP CONSTRAINT IF EXISTS inventory_items_key_version_positive,
DROP CONSTRAINT IF EXISTS inventory_items_encryption_envelope_consistent,
DROP CONSTRAINT IF EXISTS inventory_items_encryption_format_valid,
DROP CONSTRAINT IF EXISTS inventory_items_imported_by_admin_fk,
DROP COLUMN IF EXISTS version,
DROP COLUMN IF EXISTS imported_by_admin_id,
DROP COLUMN IF EXISTS encryption_key_version,
DROP COLUMN IF EXISTS encryption_format,
DROP COLUMN IF EXISTS encryption_nonce;
