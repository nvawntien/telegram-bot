package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
			Environment: row.PaymentEnvironment, Source: row.EventSource,
			Direction: row.TransferDirection, TransferContent: row.TransferContent,
			DestinationAccountID: row.DestinationAccountIdentity.String,
		})
		item := &items[len(items)-1]
		if item.DestinationAccountID != "" {
			mapping, mappingErr := s.queries.ResolveActivePaymentProviderAccount(ctx, generated.ResolveActivePaymentProviderAccountParams{
				Provider: row.Provider, Environment: row.PaymentEnvironment,
				ExternalAccountIdentity: item.DestinationAccountID,
			})
			if mappingErr == nil {
				item.ProviderAccountMappingID = mapping.ID
				item.LocalBankAccountID = mapping.LocalBankAccountID
				if _, err := s.queries.AttachPaymentEventProviderAccount(ctx, generated.AttachPaymentEventProviderAccountParams{
					ProviderAccountMappingID: requiredInt8(mapping.ID), ID: row.ID,
				}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
			} else if !errors.Is(mappingErr, pgx.ErrNoRows) {
				return nil, mappingErr
			}
		}
	}
	return items, nil
}

func (s *AppStore) ReviewPaymentEvent(ctx context.Context, item app.PaymentEventWorkItem, reference, reason string, reviewedAt time.Time) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		row, err := queries.LockPaymentEvent(ctx, item.ID)
		if err != nil {
			return err
		}
		if row.ProcessingStatus == "review" || row.ProcessingStatus == "completed" {
			return nil
		}
		if _, err := queries.InsertPaymentReviewCase(ctx, generated.InsertPaymentReviewCaseParams{
			PaymentEventID: requiredInt8(item.ID), Provider: item.Provider,
			ProviderTransactionID: optionalText(item.ProviderTransactionID), PaymentReference: reference,
			AmountVnd: item.Amount.Int64(), Currency: item.Currency, OccurredAt: requiredTimestamp(item.OccurredAt),
			Reason: reason, PaymentEnvironment: item.Environment, EventSource: item.Source,
			ProviderAccountMappingID:   optionalInt8(item.ProviderAccountMappingID),
			DestinationAccountIdentity: optionalText(item.DestinationAccountID),
		}); err != nil {
			return err
		}
		_, err = queries.MarkPaymentEventReviewedBeforeAcceptance(ctx, generated.MarkPaymentEventReviewedBeforeAcceptanceParams{
			ProcessedAt: requiredTimestamp(reviewedAt), ProcessingError: optionalText("payment provider event requires review"),
			LastErrorCode: optionalText(reason), ID: item.ID,
		})
		return err
	})
}

func (s *AppStore) CompleteIgnoredPaymentEvent(ctx context.Context, eventID int64, reason string, completedAt time.Time) error {
	_, err := s.queries.CompleteIgnoredPaymentEvent(ctx, generated.CompleteIgnoredPaymentEventParams{
		ProcessedAt: requiredTimestamp(completedAt), ProcessingError: optionalText("provider transaction ignored"),
		LastErrorCode: optionalText(reason), ID: eventID,
	})
	return err
}

func (s *AppStore) SchedulePaymentEventRetry(ctx context.Context, eventID int64, nextAttemptAt, processedAt time.Time, errorCode, errorDetail string) error {
	_, err := s.queries.SchedulePaymentEventRetry(ctx, generated.SchedulePaymentEventRetryParams{
		NextAttemptAt: requiredTimestamp(nextAttemptAt), ProcessedAt: requiredTimestamp(processedAt),
		ProcessingError: optionalText(errorDetail), LastErrorCode: optionalText(errorCode), ID: eventID,
	})
	return err
}

var _ app.PaymentEventWorkRepository = (*AppStore)(nil)
