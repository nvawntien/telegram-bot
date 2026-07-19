package app

import (
	"context"
	"fmt"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

type PaymentEventWorkItem struct {
	ID                    int64
	Provider              string
	ExternalEventID       string
	ProviderTransactionID string
	Reference             string
	Amount                domain.Money
	Currency              string
	OccurredAt            time.Time
	Attempts              int32
}

type PaymentEventWorkRepository interface {
	ClaimPaymentEvents(context.Context, time.Time, time.Time, int32) ([]PaymentEventWorkItem, error)
	SchedulePaymentEventRetry(context.Context, int64, time.Time, time.Time, string, string) error
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
		_, acceptErr := j.acceptor.Accept(ctx, AcceptPaymentCommand{
			PaymentEventID: item.ID, Provider: item.Provider, ExternalEventID: item.ExternalEventID,
			ProviderTransactionID: item.ProviderTransactionID, Reference: item.Reference,
			Amount: item.Amount, Currency: item.Currency, OccurredAt: item.OccurredAt,
			Actor: PaymentActor{Type: "provider"}, RequestID: fmt.Sprintf("payment-event-%d", item.ID),
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
