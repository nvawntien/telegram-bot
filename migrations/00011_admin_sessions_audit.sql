-- +goose Up
CREATE TABLE admin_sessions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    admin_id bigint NOT NULL,
    state text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT admin_sessions_admin_id_unique UNIQUE (admin_id),
    CONSTRAINT admin_sessions_admin_id_fk FOREIGN KEY (admin_id) REFERENCES admins (id) ON DELETE RESTRICT,
    CONSTRAINT admin_sessions_state_not_blank CHECK (btrim(state) <> ''),
    CONSTRAINT admin_sessions_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT admin_sessions_version_positive CHECK (version > 0)
);

CREATE INDEX admin_sessions_expiry_idx ON admin_sessions (expires_at, id);

CREATE TRIGGER admin_sessions_set_updated_at
BEFORE UPDATE ON admin_sessions
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE audit_logs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_type text NOT NULL,
    actor_id bigint,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id bigint,
    before_data jsonb,
    after_data jsonb,
    request_id text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT audit_logs_actor_type_valid CHECK (actor_type IN ('user', 'admin', 'system', 'provider')),
    CONSTRAINT audit_logs_actor_consistent CHECK (
        (actor_type = 'system' AND actor_id IS NULL)
        OR (actor_type <> 'system' AND actor_id IS NOT NULL AND actor_id > 0)
    ),
    CONSTRAINT audit_logs_action_not_blank CHECK (btrim(action) <> ''),
    CONSTRAINT audit_logs_resource_type_not_blank CHECK (btrim(resource_type) <> ''),
    CONSTRAINT audit_logs_resource_id_positive CHECK (resource_id IS NULL OR resource_id > 0),
    CONSTRAINT audit_logs_before_object CHECK (before_data IS NULL OR jsonb_typeof(before_data) = 'object'),
    CONSTRAINT audit_logs_after_object CHECK (after_data IS NULL OR jsonb_typeof(after_data) = 'object'),
    CONSTRAINT audit_logs_request_not_blank CHECK (request_id IS NULL OR btrim(request_id) <> '')
);

CREATE INDEX audit_logs_resource_idx ON audit_logs (resource_type, resource_id, created_at DESC, id DESC);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_type, actor_id, created_at DESC, id DESC);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC, id DESC);

CREATE TRIGGER audit_logs_append_only
BEFORE UPDATE OR DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION prevent_append_only_mutation();

-- +goose Down
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS admin_sessions;
