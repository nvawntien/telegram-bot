-- +goose Up
CREATE TABLE sheet_sync_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_id text NOT NULL,
    source_version text NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'running',
    imported_count integer NOT NULL DEFAULT 0,
    skipped_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    error_details jsonb NOT NULL DEFAULT '[]'::jsonb,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT sheet_sync_runs_source_version_unique UNIQUE (source_id, source_version),
    CONSTRAINT sheet_sync_runs_idempotency_unique UNIQUE (idempotency_key),
    CONSTRAINT sheet_sync_runs_source_id_not_blank CHECK (btrim(source_id) <> ''),
    CONSTRAINT sheet_sync_runs_source_version_not_blank CHECK (btrim(source_version) <> ''),
    CONSTRAINT sheet_sync_runs_idempotency_not_blank CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT sheet_sync_runs_status_valid CHECK (status IN ('running', 'completed', 'partial', 'failed')),
    CONSTRAINT sheet_sync_runs_counts_nonnegative CHECK (imported_count >= 0 AND skipped_count >= 0 AND failed_count >= 0),
    CONSTRAINT sheet_sync_runs_errors_array CHECK (jsonb_typeof(error_details) = 'array'),
    CONSTRAINT sheet_sync_runs_finish_consistent CHECK (
        (status = 'running' AND finished_at IS NULL)
        OR (status <> 'running' AND finished_at IS NOT NULL)
    )
);

CREATE INDEX sheet_sync_runs_status_started_idx ON sheet_sync_runs (status, started_at, id);

CREATE TRIGGER sheet_sync_runs_set_updated_at
BEFORE UPDATE ON sheet_sync_runs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE IF EXISTS sheet_sync_runs;
