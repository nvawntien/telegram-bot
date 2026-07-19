package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type PaymentEventWorkItem struct {
	ID                       int64
	Provider                 string
	ExternalEventID          string
	ProviderTransactionID    string
	Reference                string
	Environment              string
	Source                   string
	Direction                string
	TransferContent          string
	DestinationAccountID     string
	ProviderAccountMappingID int64
	LocalBankAccountID       int64
	Amount                   domain.Money
	Currency                 string
	OccurredAt               time.Time
	Attempts                 int32
}

type PaymentEventWorkRepository interface {
	ClaimPaymentEvents(context.Context, time.Time, time.Time, int32) ([]PaymentEventWorkItem, error)
	SchedulePaymentEventRetry(context.Context, int64, time.Time, time.Time, string, string) error
	ReviewPaymentEvent(context.Context, PaymentEventWorkItem, string, string, time.Time) error
	CompleteIgnoredPaymentEvent(context.Context, int64, string, time.Time) error
}

type PaymentEventAcceptor interface {
	Accept(context.Context, AcceptPaymentCommand) (PaymentAcceptanceResult, error)
}

type PaymentEventJob struct {
	repository PaymentEventWorkRepository
	acceptor   PaymentEventAcceptor
	batchSize  int32
	retryBase  time.Duration
	staleAfter time.Duration
	clock      func() time.Time
	extractor  *PaymentReferenceExtractor
}

func (j *PaymentEventJob) WithReferenceExtractor(extractor *PaymentReferenceExtractor) *PaymentEventJob {
	j.extractor = extractor
	return j
}

func NewPaymentEventJob(repository PaymentEventWorkRepository, acceptor PaymentEventAcceptor, batchSize int32, retryBase, staleAfter time.Duration) *PaymentEventJob {
	return &PaymentEventJob{repository: repository, acceptor: acceptor, batchSize: batchSize, retryBase: retryBase, staleAfter: staleAfter, clock: time.Now}
}

func (j *PaymentEventJob) RunOnce(ctx context.Context) (int, error) {
	if j.batchSize <= 0 || j.retryBase <= 0 || j.staleAfter <= 0 {
		return 0, ErrInvalidInput
	}
	now := j.clock()
	items, err := j.repository.ClaimPaymentEvents(ctx, now, now.Add(-j.staleAfter), j.batchSize)
	if err != nil {
		return 0, fmt.Errorf("claim payment events: %w", err)
	}
	for _, item := range items {
		if item.Direction == "outbound" {
			if err := j.repository.CompleteIgnoredPaymentEvent(ctx, item.ID, "provider_unsupported_transaction", now); err != nil {
				return len(items), fmt.Errorf("ignore outbound payment event: %w", err)
			}
			continue
		}
		reference := item.Reference
		if j.extractor != nil {
			extracted := j.extractor.Extract(item.TransferContent)
			if extracted.Match != PaymentReferenceExact {
				reason := "provider_unmatched_reference"
				if extracted.Match == PaymentReferenceMultiple {
					reason = "provider_ambiguous_reference"
				}
				if err := j.repository.ReviewPaymentEvent(ctx, item, "UNRESOLVED", reason, now); err != nil {
					return len(items), fmt.Errorf("review payment reference: %w", err)
				}
				continue
			}
			reference = extracted.Reference
		}
		if item.DestinationAccountID != "" && item.ProviderAccountMappingID == 0 {
			if err := j.repository.ReviewPaymentEvent(ctx, item, reference, "provider_account_unmapped", now); err != nil {
				return len(items), fmt.Errorf("review unmapped provider account: %w", err)
			}
			continue
		}
		_, acceptErr := j.acceptor.Accept(ctx, AcceptPaymentCommand{
			PaymentEventID: item.ID, Provider: item.Provider, ExternalEventID: item.ExternalEventID,
			ProviderTransactionID: item.ProviderTransactionID, Reference: reference,
			Amount: item.Amount, Currency: item.Currency, OccurredAt: item.OccurredAt,
			Environment: item.Environment, ProviderAccountMappingID: item.ProviderAccountMappingID,
			LocalBankAccountID:   item.LocalBankAccountID,
			Source:               item.Source,
			DestinationAccountID: item.DestinationAccountID,
			Actor:                PaymentActor{Type: "provider"}, RequestID: fmt.Sprintf("payment-event-%d", item.ID),
		})
		if acceptErr == nil {
			continue
		}
		delay := retryDelay(j.retryBase, item.Attempts)
		if retryErr := j.repository.SchedulePaymentEventRetry(ctx, item.ID, now.Add(delay), now, "acceptance_failed", safeProcessingError(acceptErr)); retryErr != nil {
			return len(items), fmt.Errorf("schedule payment event retry: %w", retryErr)
		}
	}
	return len(items), nil
}

func retryDelay(base time.Duration, attempts int32) time.Duration {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 10 {
		shift = 10
	}
	return base * time.Duration(1<<shift)
}

func safeProcessingError(err error) string {
	_ = err
	return "payment acceptance failed"
}
