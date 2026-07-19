package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type NormalizedPaymentEvent struct {
	Provider              string
	Environment           string
	ExternalEventID       string
	ProviderTransactionID string
	Reference             string
	Direction             string
	TransferContent       string
	DestinationAccountID  string
	ProviderAccountID     string
	Source                string
	Amount                domain.Money
	Currency              string
	OccurredAt            time.Time
	ReceivedAccount       string
	EventType             string
	PayloadHash           []byte
	BusinessFingerprint   []byte
	SanitizedMetadata     json.RawMessage
}

type PaymentEventRecord struct {
	ID                    int64
	Provider              string
	ExternalEventID       string
	ProviderTransactionID string
	PayloadHash           []byte
	BusinessFingerprint   []byte
}

type PaymentEventIngestionRepository interface {
	InsertPaymentEvent(context.Context, NormalizedPaymentEvent, int32) (PaymentEventRecord, bool, error)
	RecordPaymentEventConflict(context.Context, int64, NormalizedPaymentEvent, string) error
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
	normalizePaymentEvent(&event)
	if event.Provider == "" || event.ExternalEventID == "" || event.ProviderTransactionID == "" ||
		event.TransferContent == "" || event.Amount <= 0 || event.Currency == "" || event.OccurredAt.IsZero() ||
		event.EventType == "" || !validPaymentEnvironment(event.Environment) || !validPaymentSource(event.Source) ||
		(event.Direction != "inbound" && event.Direction != "outbound") || len(event.PayloadHash) != 32 ||
		len(event.BusinessFingerprint) != 32 || s.maxAttempts <= 0 || !json.Valid(event.SanitizedMetadata) {
		return PaymentEventIngestionResult{}, ErrInvalidInput
	}
	record, inserted, err := s.repository.InsertPaymentEvent(ctx, event, s.maxAttempts)
	if err != nil {
		return PaymentEventIngestionResult{}, fmt.Errorf("ingest payment event: %w", err)
	}
	if !inserted {
		conflictReason := ""
		if record.ExternalEventID == event.ExternalEventID && !bytes.Equal(record.PayloadHash, event.PayloadHash) {
			conflictReason = "provider_event_payload_conflict"
		} else if record.ProviderTransactionID == event.ProviderTransactionID && !bytes.Equal(record.BusinessFingerprint, event.BusinessFingerprint) {
			conflictReason = "provider_duplicate_transaction_conflict"
		}
		if conflictReason != "" {
			if conflictErr := s.repository.RecordPaymentEventConflict(ctx, record.ID, event, conflictReason); conflictErr != nil {
				return PaymentEventIngestionResult{}, fmt.Errorf("record payment event conflict: %w", conflictErr)
			}
			return PaymentEventIngestionResult{}, ErrPaymentEventConflict
		}
	}
	return PaymentEventIngestionResult{EventID: record.ID, Duplicate: !inserted}, nil
}

func normalizePaymentEvent(event *NormalizedPaymentEvent) {
	event.Provider = strings.TrimSpace(event.Provider)
	event.Environment = strings.TrimSpace(strings.ToLower(event.Environment))
	if event.Environment == "" {
		event.Environment = "production"
	}
	event.ExternalEventID = strings.TrimSpace(event.ExternalEventID)
	event.ProviderTransactionID = strings.TrimSpace(event.ProviderTransactionID)
	event.Reference = strings.TrimSpace(strings.ToUpper(event.Reference))
	event.Direction = strings.TrimSpace(strings.ToLower(event.Direction))
	if event.Direction == "" {
		event.Direction = "inbound"
	}
	event.TransferContent = strings.TrimSpace(event.TransferContent)
	if event.TransferContent == "" {
		event.TransferContent = event.Reference
	}
	event.DestinationAccountID = strings.TrimSpace(event.DestinationAccountID)
	event.ProviderAccountID = strings.TrimSpace(event.ProviderAccountID)
	event.Source = strings.TrimSpace(strings.ToLower(event.Source))
	if event.Source == "" {
		event.Source = "webhook"
	}
	event.Currency = strings.TrimSpace(strings.ToUpper(event.Currency))
	if len(event.BusinessFingerprint) == 0 {
		event.BusinessFingerprint = paymentBusinessFingerprint(*event)
	}
}

func paymentBusinessFingerprint(event NormalizedPaymentEvent) []byte {
	canonical := strings.Join([]string{
		event.Provider, event.Environment, event.ProviderTransactionID, event.EventType,
		event.Direction, fmt.Sprintf("%d", event.Amount.Int64()), event.Currency,
		event.TransferContent, event.DestinationAccountID, event.ProviderAccountID,
		event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return sum[:]
}

func validPaymentEnvironment(value string) bool {
	return value == "development" || value == "test" || value == "production"
}

func validPaymentSource(value string) bool {
	return value == "webhook" || value == "reconciliation" || value == "legacy"
}
