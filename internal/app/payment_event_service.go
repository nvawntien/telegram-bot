package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type NormalizedPaymentEvent struct {
	Provider              string
	ExternalEventID       string
	ProviderTransactionID string
	Reference             string
	Amount                domain.Money
	Currency              string
	OccurredAt            time.Time
	ReceivedAccount       string
	EventType             string
	PayloadHash           []byte
	SanitizedMetadata     json.RawMessage
}

type PaymentEventRecord struct {
	ID              int64
	Provider        string
	ExternalEventID string
	PayloadHash     []byte
}

type PaymentEventIngestionRepository interface {
	InsertPaymentEvent(context.Context, NormalizedPaymentEvent, int32) (PaymentEventRecord, bool, error)
}

type PaymentEventIngestionResult struct {
	EventID   int64
	Duplicate bool
}

type PaymentEventIngestionService struct {
	repository  PaymentEventIngestionRepository
	maxAttempts int32
}

func NewPaymentEventIngestionService(repository PaymentEventIngestionRepository, maxAttempts int32) *PaymentEventIngestionService {
	return &PaymentEventIngestionService{repository: repository, maxAttempts: maxAttempts}
}

func (s *PaymentEventIngestionService) Ingest(ctx context.Context, event NormalizedPaymentEvent) (PaymentEventIngestionResult, error) {
	if event.Provider == "" || event.ExternalEventID == "" || event.ProviderTransactionID == "" ||
		event.Reference == "" || event.Amount <= 0 || event.Currency == "" || event.OccurredAt.IsZero() ||
		event.EventType == "" || len(event.PayloadHash) != 32 || s.maxAttempts <= 0 || !json.Valid(event.SanitizedMetadata) {
		return PaymentEventIngestionResult{}, ErrInvalidInput
	}
	record, inserted, err := s.repository.InsertPaymentEvent(ctx, event, s.maxAttempts)
	if err != nil {
		return PaymentEventIngestionResult{}, fmt.Errorf("ingest payment event: %w", err)
	}
	if !inserted && !bytes.Equal(record.PayloadHash, event.PayloadHash) {
		return PaymentEventIngestionResult{}, ErrPaymentEventConflict
	}
	return PaymentEventIngestionResult{EventID: record.ID, Duplicate: !inserted}, nil
}
