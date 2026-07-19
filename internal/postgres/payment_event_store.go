package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) InsertPaymentEvent(ctx context.Context, event app.NormalizedPaymentEvent, maxAttempts int32) (app.PaymentEventRecord, bool, error) {
	row, err := s.queries.InsertPaymentEvent(ctx, generated.InsertPaymentEventParams{
		Provider: event.Provider, ExternalEventID: event.ExternalEventID,
		ProviderTransactionID: optionalText(event.ProviderTransactionID), EventType: event.EventType,
		PayloadHash: event.PayloadHash, SanitizedPayload: event.SanitizedMetadata,
		SignatureVerified: true, MaxAttempts: maxAttempts,
	})
	if err == nil {
		return mapPaymentEventRecord(row), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return app.PaymentEventRecord{}, false, err
	}
	row, err = s.queries.GetPaymentEventByProviderEventID(ctx, generated.GetPaymentEventByProviderEventIDParams{
		Provider: event.Provider, ExternalEventID: event.ExternalEventID,
	})
	if err != nil {
		return app.PaymentEventRecord{}, false, err
	}
	return mapPaymentEventRecord(row), false, nil
}

func mapPaymentEventRecord(row generated.PaymentEvent) app.PaymentEventRecord {
	return app.PaymentEventRecord{ID: row.ID, Provider: row.Provider, ExternalEventID: row.ExternalEventID, PayloadHash: row.PayloadHash}
}
