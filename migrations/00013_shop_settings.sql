-- +goose Up
CREATE TABLE shop_settings (
    id smallint PRIMARY KEY DEFAULT 1,
    shop_name text NOT NULL,
    support_contact text NOT NULL,
    default_bank_account_id bigint,
    order_expire_minutes integer NOT NULL DEFAULT 15,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT shop_settings_singleton CHECK (id = 1),
    CONSTRAINT shop_settings_default_bank_fk FOREIGN KEY (default_bank_account_id) REFERENCES bank_accounts (id) ON DELETE RESTRICT,
    CONSTRAINT shop_settings_name_not_blank CHECK (btrim(shop_name) <> ''),
    CONSTRAINT shop_settings_support_not_blank CHECK (btrim(support_contact) <> ''),
    CONSTRAINT shop_settings_expiry_positive CHECK (order_expire_minutes > 0),
    CONSTRAINT shop_settings_version_positive CHECK (version > 0)
);

CREATE TRIGGER shop_settings_set_updated_at
BEFORE UPDATE ON shop_settings
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS shop_settings;
