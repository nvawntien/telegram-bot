-- +goose Up
ALTER TABLE payment_provider_checkpoints
DROP CONSTRAINT payment_provider_checkpoints_success_order;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM payment_provider_checkpoints) THEN
        RAISE EXCEPTION 'cannot restore the legacy checkpoint time constraint while checkpoints exist';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE payment_provider_checkpoints
DROP CONSTRAINT payment_provider_checkpoints_success_order,
ADD CONSTRAINT payment_provider_checkpoints_success_order CHECK (
    last_successful_at IS NULL OR last_attempted_at IS NULL OR last_successful_at <= last_attempted_at
);
