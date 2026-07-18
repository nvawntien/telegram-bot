-- +goose Up
CREATE TABLE inventory_items (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id bigint NOT NULL,
    encrypted_payload bytea NOT NULL,
    encryption_key_id text NOT NULL,
    payload_fingerprint bytea NOT NULL,
    status text NOT NULL DEFAULT 'available',
    reserved_order_id bigint,
    reserved_until timestamptz,
    sold_order_id bigint,
    disabled_reason text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT inventory_items_product_id_fk FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE RESTRICT,
    CONSTRAINT inventory_items_reserved_order_id_fk FOREIGN KEY (reserved_order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT inventory_items_sold_order_id_fk FOREIGN KEY (sold_order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT inventory_items_product_fingerprint_unique UNIQUE (product_id, payload_fingerprint),
    CONSTRAINT inventory_items_payload_not_empty CHECK (octet_length(encrypted_payload) > 0),
    CONSTRAINT inventory_items_key_id_not_blank CHECK (btrim(encryption_key_id) <> ''),
    CONSTRAINT inventory_items_fingerprint_sha256 CHECK (octet_length(payload_fingerprint) = 32),
    CONSTRAINT inventory_items_status_valid CHECK (status IN ('available', 'reserved', 'sold', 'disabled')),
    CONSTRAINT inventory_items_disabled_reason_not_blank CHECK (disabled_reason IS NULL OR btrim(disabled_reason) <> ''),
    CONSTRAINT inventory_items_state_consistent CHECK (
        (status = 'available' AND reserved_order_id IS NULL AND reserved_until IS NULL AND sold_order_id IS NULL AND disabled_reason IS NULL)
        OR (status = 'reserved' AND reserved_order_id IS NOT NULL AND reserved_until IS NOT NULL AND sold_order_id IS NULL AND disabled_reason IS NULL)
        OR (status = 'sold' AND reserved_order_id IS NULL AND reserved_until IS NULL AND sold_order_id IS NOT NULL AND disabled_reason IS NULL)
        OR (status = 'disabled' AND reserved_order_id IS NULL AND reserved_until IS NULL AND sold_order_id IS NULL)
    )
);

CREATE INDEX inventory_items_claim_idx ON inventory_items (product_id, created_at, id) WHERE status = 'available';
CREATE INDEX inventory_items_reserved_expiry_idx ON inventory_items (reserved_until, id) WHERE status = 'reserved';

CREATE TRIGGER inventory_items_set_updated_at
BEFORE UPDATE ON inventory_items
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE order_inventory_items (
    order_id bigint NOT NULL,
    order_item_id bigint NOT NULL,
    inventory_item_id bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT order_inventory_items_pk PRIMARY KEY (order_id, inventory_item_id),
    CONSTRAINT order_inventory_items_inventory_unique UNIQUE (inventory_item_id),
    CONSTRAINT order_inventory_items_order_id_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT order_inventory_items_order_item_fk FOREIGN KEY (order_id, order_item_id) REFERENCES order_items (order_id, id) ON DELETE RESTRICT,
    CONSTRAINT order_inventory_items_inventory_id_fk FOREIGN KEY (inventory_item_id) REFERENCES inventory_items (id) ON DELETE RESTRICT
);

CREATE INDEX order_inventory_items_order_item_idx ON order_inventory_items (order_item_id, inventory_item_id);

-- +goose Down
DROP TABLE IF EXISTS order_inventory_items;
DROP TABLE IF EXISTS inventory_items;
