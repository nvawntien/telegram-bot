-- +goose Up
CREATE TABLE orders (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending_payment',
    currency text NOT NULL DEFAULT 'VND',
    subtotal_vnd bigint NOT NULL,
    total_vnd bigint NOT NULL,
    payment_reference text NOT NULL,
    idempotency_key text NOT NULL,
    expires_at timestamptz NOT NULL,
    paid_at timestamptz,
    delivery_started_at timestamptz,
    delivered_at timestamptz,
    cancelled_at timestamptz,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT orders_user_id_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT orders_payment_reference_unique UNIQUE (payment_reference),
    CONSTRAINT orders_user_idempotency_unique UNIQUE (user_id, idempotency_key),
    CONSTRAINT orders_status_valid CHECK (status IN (
        'pending_payment',
        'payment_review',
        'paid',
        'reserving',
        'delivering',
        'delivered',
        'expired',
        'cancelled',
        'out_of_stock',
        'delivery_failed',
        'refunded'
    )),
    CONSTRAINT orders_currency_vnd CHECK (currency = 'VND'),
    CONSTRAINT orders_subtotal_nonnegative CHECK (subtotal_vnd >= 0),
    CONSTRAINT orders_total_nonnegative CHECK (total_vnd >= 0),
    CONSTRAINT orders_payment_reference_not_blank CHECK (btrim(payment_reference) <> ''),
    CONSTRAINT orders_idempotency_key_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT orders_version_positive CHECK (version > 0),
    CONSTRAINT orders_delivery_timestamp_consistent CHECK (status <> 'delivered' OR delivered_at IS NOT NULL),
    CONSTRAINT orders_cancellation_timestamp_consistent CHECK (status <> 'cancelled' OR cancelled_at IS NOT NULL)
);

CREATE INDEX orders_user_created_idx ON orders (user_id, created_at DESC, id DESC);
CREATE INDEX orders_status_created_idx ON orders (status, created_at, id);
CREATE INDEX orders_pending_expiry_idx ON orders (expires_at, id) WHERE status = 'pending_payment';

CREATE TRIGGER orders_set_updated_at
BEFORE UPDATE ON orders
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE order_items (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id bigint NOT NULL,
    product_id bigint NOT NULL,
    product_name text NOT NULL,
    unit_price_vnd bigint NOT NULL,
    quantity integer NOT NULL,
    line_total_vnd bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT order_items_order_id_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT order_items_product_id_fk FOREIGN KEY (product_id) REFERENCES products (id) ON DELETE RESTRICT,
    CONSTRAINT order_items_order_id_id_unique UNIQUE (order_id, id),
    CONSTRAINT order_items_product_name_not_blank CHECK (btrim(product_name) <> ''),
    CONSTRAINT order_items_unit_price_nonnegative CHECK (unit_price_vnd >= 0),
    CONSTRAINT order_items_quantity_positive CHECK (quantity > 0),
    CONSTRAINT order_items_line_total_nonnegative CHECK (line_total_vnd >= 0),
    CONSTRAINT order_items_line_total_consistent CHECK (line_total_vnd = unit_price_vnd * quantity)
);

CREATE INDEX order_items_order_idx ON order_items (order_id, id);

CREATE TABLE order_status_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id bigint NOT NULL,
    from_status text,
    to_status text NOT NULL,
    reason_code text,
    actor_type text NOT NULL,
    actor_id bigint,
    request_id text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT order_status_history_order_id_fk FOREIGN KEY (order_id) REFERENCES orders (id) ON DELETE RESTRICT,
    CONSTRAINT order_status_history_from_status_valid CHECK (from_status IS NULL OR from_status IN (
        'pending_payment', 'payment_review', 'paid', 'reserving', 'delivering', 'delivered',
        'expired', 'cancelled', 'out_of_stock', 'delivery_failed', 'refunded'
    )),
    CONSTRAINT order_status_history_to_status_valid CHECK (to_status IN (
        'pending_payment', 'payment_review', 'paid', 'reserving', 'delivering', 'delivered',
        'expired', 'cancelled', 'out_of_stock', 'delivery_failed', 'refunded'
    )),
    CONSTRAINT order_status_history_actor_type_valid CHECK (actor_type IN ('user', 'admin', 'system', 'provider')),
    CONSTRAINT order_status_history_reason_not_blank CHECK (reason_code IS NULL OR btrim(reason_code) <> ''),
    CONSTRAINT order_status_history_request_not_blank CHECK (request_id IS NULL OR btrim(request_id) <> '')
);

CREATE INDEX order_status_history_order_idx ON order_status_history (order_id, id);

CREATE TRIGGER order_status_history_append_only
BEFORE UPDATE OR DELETE ON order_status_history
FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose Down
DROP TABLE IF EXISTS order_status_history;
DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;
