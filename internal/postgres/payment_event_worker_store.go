package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

type storedPaymentMetadata struct {
	Reference  string    `json:"reference"`
	AmountVND  int64     `json:"amount_vnd"`
	Currency   string    `json:"currency"`
	OccurredAt time.Time `json:"occurred_at"`
}

func (s *AppStore) ClaimPaymentEvents(ctx context.Context, now, staleBefore time.Time, batchSize int32) ([]app.PaymentEventWorkItem, error) {
	rows, err := s.queries.ClaimPaymentEvents(ctx, generated.ClaimPaymentEventsParams{
		ClaimedAt: requiredTimestamp(now), StaleBefore: requiredTimestamp(staleBefore), BatchSize: batchSize,
	})
	if err != nil {
		return nil, err
	}
	items := make([]app.PaymentEventWorkItem, 0, len(rows))
	for _, row := range rows {
		var metadata storedPaymentMetadata
		if err := json.Unmarshal(row.SanitizedPayload, &metadata); err != nil {
			return nil, fmt.Errorf("decode payment event %d metadata: %w", row.ID, err)
		}
		items = append(items, app.PaymentEventWorkItem{
			ID: row.ID, Provider: row.Provider, ExternalEventID: row.ExternalEventID,
			ProviderTransactionID: row.ProviderTransactionID.String, Reference: metadata.Reference,
			Amount: domain.Money(metadata.AmountVND), Currency: metadata.Currency,
			OccurredAt: metadata.OccurredAt, Attempts: row.Attempts,
		})
	}
	return items, nil
}

func (s *AppStore) SchedulePaymentEventRetry(ctx context.Context, eventID int64, nextAttemptAt, processedAt time.Time, errorCode, errorDetail string) error {
	_, err := s.queries.SchedulePaymentEventRetry(ctx, generated.SchedulePaymentEventRetryParams{
		NextAttemptAt: requiredTimestamp(nextAttemptAt), ProcessedAt: requiredTimestamp(processedAt),
		ProcessingError: optionalText(errorDetail), LastErrorCode: optionalText(errorCode), ID: eventID,
	})
	return err
}

var _ app.PaymentEventWorkRepository = (*AppStore)(nil)
