-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION prevent_append_only_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_user_id bigint NOT NULL,
    username text,
    display_name text,
    status text NOT NULL DEFAULT 'active',
    ban_reason text,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT users_telegram_user_id_positive CHECK (telegram_user_id > 0),
    CONSTRAINT users_telegram_user_id_unique UNIQUE (telegram_user_id),
    CONSTRAINT users_status_valid CHECK (status IN ('active', 'banned', 'disabled')),
    CONSTRAINT users_username_not_blank CHECK (username IS NULL OR btrim(username) <> ''),
    CONSTRAINT users_display_name_not_blank CHECK (display_name IS NULL OR btrim(display_name) <> '')
);

CREATE INDEX users_status_id_idx ON users (status, id);
CREATE INDEX users_created_at_idx ON users (created_at DESC);

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE admins (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    role text NOT NULL,
    is_active boolean NOT NULL DEFAULT true,
    created_by bigint,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT admins_user_id_unique UNIQUE (user_id),
    CONSTRAINT admins_user_id_fk FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT,
    CONSTRAINT admins_created_by_fk FOREIGN KEY (created_by) REFERENCES admins (id) ON DELETE RESTRICT,
    CONSTRAINT admins_role_valid CHECK (role IN ('owner', 'admin', 'operator', 'support'))
);

CREATE INDEX admins_active_role_idx ON admins (is_active, role, id);

CREATE TRIGGER admins_set_updated_at
BEFORE UPDATE ON admins
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS admins;
DROP TABLE IF EXISTS users;
DROP FUNCTION IF EXISTS prevent_append_only_mutation();
