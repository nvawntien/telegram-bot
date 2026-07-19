-- +goose Up
ALTER TABLE bank_accounts
ADD COLUMN payment_environment text NOT NULL DEFAULT 'production',
ADD CONSTRAINT bank_accounts_payment_environment_valid
    CHECK (payment_environment IN ('development', 'test', 'production'));

CREATE INDEX bank_accounts_payment_environment_idx
ON bank_accounts (payment_environment, is_active, sort_order, id);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM bank_accounts WHERE payment_environment <> 'production') THEN
        RAISE EXCEPTION 'cannot remove non-production bank account environment data';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS bank_accounts_payment_environment_idx;

ALTER TABLE bank_accounts
DROP CONSTRAINT IF EXISTS bank_accounts_payment_environment_valid,
DROP COLUMN IF EXISTS payment_environment;
