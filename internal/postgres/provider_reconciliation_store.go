package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/payment"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) ListActiveProviderAccounts(ctx context.Context) ([]payment.ProviderAccount, error) {
	rows, err := s.queries.ListActivePaymentProviderAccounts(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]payment.ProviderAccount, 0, len(rows))
	for _, row := range rows {
		result = append(result, payment.ProviderAccount{
			ID: row.ID, Provider: row.Provider, Environment: payment.ProviderEnvironment(row.Environment),
			ExternalAccountIdentity: row.ExternalAccountIdentity, LocalBankAccountID: row.LocalBankAccountID,
		})
	}
	return result, nil
}

func (s *AppStore) EnsureProviderCheckpoint(ctx context.Context, providerAccountID int64) (payment.ProviderCheckpoint, error) {
	row, err := s.queries.EnsurePaymentProviderCheckpoint(ctx, providerAccountID)
	return mapProviderCheckpoint(row), err
}

func (s *AppStore) ClaimProviderCheckpoint(ctx context.Context, checkpointID int64, owner string, attemptedAt, expiresAt time.Time) (payment.ProviderCheckpoint, bool, error) {
	row, err := s.queries.ClaimPaymentProviderCheckpoint(ctx, generated.ClaimPaymentProviderCheckpointParams{
		LeaseOwner: optionalText(owner), LeaseExpiresAt: requiredTimestamp(expiresAt),
		AttemptedAt: requiredTimestamp(attemptedAt), ID: checkpointID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.ProviderCheckpoint{}, false, nil
	}
	return mapProviderCheckpoint(row), err == nil, err
}

func (s *AppStore) AdvanceProviderCheckpoint(ctx context.Context, checkpoint payment.ProviderCheckpoint, cursor, lastTransactionID string, lastOccurredAt time.Time) (payment.ProviderCheckpoint, error) {
	row, err := s.queries.AdvancePaymentProviderCheckpoint(ctx, generated.AdvancePaymentProviderCheckpointParams{
		CursorValue: optionalText(cursor), LastTransactionExternalID: optionalText(lastTransactionID),
		LastOccurredAt: optionalTimestamp(lastOccurredAt), ID: checkpoint.ID,
		ExpectedVersion: checkpoint.Version, ExpectedLeaseOwner: optionalText(checkpointLeaseOwner(checkpoint)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.ProviderCheckpoint{}, payment.ErrCheckpointLeaseUnavailable
	}
	return mapProviderCheckpoint(row), err
}

func (s *AppStore) CompleteProviderCheckpoint(ctx context.Context, checkpoint payment.ProviderCheckpoint, completedAt time.Time) error {
	_, err := s.queries.CompletePaymentProviderCheckpoint(ctx, generated.CompletePaymentProviderCheckpointParams{
		CompletedAt: requiredTimestamp(completedAt), ID: checkpoint.ID, ExpectedVersion: checkpoint.Version,
		ExpectedLeaseOwner: optionalText(checkpointLeaseOwner(checkpoint)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.ErrCheckpointLeaseUnavailable
	}
	return err
}

func (s *AppStore) FailProviderCheckpoint(ctx context.Context, checkpoint payment.ProviderCheckpoint, errorCode string) error {
	_, err := s.queries.RecordPaymentProviderCheckpointFailure(ctx, generated.RecordPaymentProviderCheckpointFailureParams{
		LastErrorCode: optionalText(errorCode), ID: checkpoint.ID, ExpectedVersion: checkpoint.Version,
		ExpectedLeaseOwner: optionalText(checkpointLeaseOwner(checkpoint)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payment.ErrCheckpointLeaseUnavailable
	}
	return err
}

func mapProviderCheckpoint(row generated.PaymentProviderCheckpoint) payment.ProviderCheckpoint {
	return payment.ProviderCheckpoint{
		ID: row.ID, ProviderAccountID: row.ProviderAccountID, Cursor: row.CursorValue.String,
		LastTransactionExternalID: row.LastTransactionExternalID.String,
		LastOccurredAt:            row.LastOccurredAt.Time, LeaseOwner: row.LeaseOwner.String, Version: row.Version,
	}
}

func checkpointLeaseOwner(checkpoint payment.ProviderCheckpoint) string {
	return checkpoint.LeaseOwner
}

func optionalTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

var _ payment.ReconciliationCheckpointRepository = (*AppStore)(nil)
