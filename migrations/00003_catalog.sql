-- +goose Up
CREATE TABLE categories (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    slug text NOT NULL,
    emoji text NOT NULL DEFAULT '📦',
    sort_order integer NOT NULL DEFAULT 0,
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT categories_slug_unique UNIQUE (slug),
    CONSTRAINT categories_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT categories_slug_not_blank CHECK (btrim(slug) <> ''),
    CONSTRAINT categories_emoji_not_blank CHECK (btrim(emoji) <> ''),
    CONSTRAINT categories_sort_order_nonnegative CHECK (sort_order >= 0)
);

CREATE INDEX categories_active_sort_idx ON categories (is_active, sort_order, id);

CREATE TRIGGER categories_set_updated_at
BEFORE UPDATE ON categories
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE products (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id bigint NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text,
    price_vnd bigint NOT NULL,
    delivery_type text NOT NULL DEFAULT 'inventory',
    contact_url text,
    is_active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT products_category_id_fk FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE RESTRICT,
    CONSTRAINT products_category_slug_unique UNIQUE (category_id, slug),
    CONSTRAINT products_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT products_slug_not_blank CHECK (btrim(slug) <> ''),
    CONSTRAINT products_price_nonnegative CHECK (price_vnd >= 0),
    CONSTRAINT products_delivery_type_valid CHECK (delivery_type IN ('inventory', 'contact')),
    CONSTRAINT products_delivery_contact_consistent CHECK (
        (delivery_type = 'inventory' AND contact_url IS NULL)
        OR (delivery_type = 'contact' AND contact_url IS NOT NULL AND btrim(contact_url) <> '')
    ),
    CONSTRAINT products_version_positive CHECK (version > 0)
);

CREATE INDEX products_category_active_idx ON products (category_id, is_active, id);

CREATE TRIGGER products_set_updated_at
BEFORE UPDATE ON products
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS categories;
