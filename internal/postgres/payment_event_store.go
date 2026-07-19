package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

func (s *AppStore) InsertPaymentEvent(ctx context.Context, event app.NormalizedPaymentEvent, maxAttempts int32) (app.PaymentEventRecord, bool, error) {
	row, err := s.queries.InsertPaymentEvent(ctx, generated.InsertPaymentEventParams{
		Provider: event.Provider, ExternalEventID: event.ExternalEventID,
		ProviderTransactionID: optionalText(event.ProviderTransactionID), EventType: event.EventType,
		PayloadHash: event.PayloadHash, SanitizedPayload: event.SanitizedMetadata,
		SignatureVerified: event.Source == "webhook", MaxAttempts: maxAttempts,
		PaymentEnvironment: event.Environment, EventSource: event.Source,
		TransferDirection: event.Direction, TransferContent: event.TransferContent,
		DestinationAccountIdentity: optionalText(event.DestinationAccountID),
		ProviderAccountIdentity:    optionalText(event.ProviderAccountID),
		BusinessFingerprint:        event.BusinessFingerprint,
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
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = s.queries.GetPaymentEventByProviderTransactionID(ctx, generated.GetPaymentEventByProviderTransactionIDParams{
			Provider: event.Provider, PaymentEnvironment: event.Environment,
			ProviderTransactionID: optionalText(event.ProviderTransactionID),
		})
	}
	if err != nil {
		return app.PaymentEventRecord{}, false, err
	}
	return mapPaymentEventRecord(row), false, nil
}

func mapPaymentEventRecord(row generated.PaymentEvent) app.PaymentEventRecord {
	return app.PaymentEventRecord{
		ID: row.ID, Provider: row.Provider, ExternalEventID: row.ExternalEventID,
		ProviderTransactionID: row.ProviderTransactionID.String,
		PayloadHash:           row.PayloadHash, BusinessFingerprint: row.BusinessFingerprint,
	}
}

func (s *AppStore) RecordPaymentEventConflict(ctx context.Context, eventID int64, event app.NormalizedPaymentEvent, reason string) error {
	return s.transactor.WithinTransaction(ctx, func(ctx context.Context, queries *generated.Queries) error {
		row, err := queries.LockPaymentEvent(ctx, eventID)
		if err != nil {
			return err
		}
		reference := event.Reference
		if reference == "" {
			reference = "UNRESOLVED"
		}
		if _, err := queries.InsertPaymentReviewCase(ctx, generated.InsertPaymentReviewCaseParams{
			PaymentEventID: requiredInt8(row.ID), Provider: row.Provider,
			ProviderTransactionID: optionalText(event.ProviderTransactionID),
			PaymentReference:      reference, AmountVnd: event.Amount.Int64(), Currency: event.Currency,
			OccurredAt: requiredTimestamp(event.OccurredAt), Reason: reason,
			PaymentEnvironment: event.Environment, EventSource: event.Source,
			DestinationAccountIdentity: optionalText(event.DestinationAccountID),
		}); err != nil {
			return err
		}
		now := time.Now()
		if _, err := queries.MarkPaymentEventReviewedBeforeAcceptance(ctx, generated.MarkPaymentEventReviewedBeforeAcceptanceParams{
			ProcessedAt: requiredTimestamp(now), ProcessingError: optionalText("payment provider evidence conflict"),
			LastErrorCode: optionalText(reason), ID: row.ID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		after, err := json.Marshal(map[string]any{
			"provider": row.Provider, "environment": row.PaymentEnvironment,
			"reason": reason, "external_event_id": event.ExternalEventID,
		})
		if err != nil {
			return err
		}
		_, err = queries.InsertAuditLog(ctx, generated.InsertAuditLogParams{
			ActorType: "system", ActorID: pgtype.Int8{}, Action: "payment.provider_event_conflict",
			ResourceType: "payment_event", ResourceID: requiredInt8(row.ID), AfterData: after,
			RequestID: optionalText("provider-event-conflict"),
		})
		return err
	})
}
