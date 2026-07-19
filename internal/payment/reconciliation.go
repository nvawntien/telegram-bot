package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
)

var (
	ErrCheckpointLeaseUnavailable = errors.New("payment provider checkpoint lease unavailable")
	ErrMalformedTransactionPage   = errors.New("malformed payment provider transaction page")
)

type ProviderAPIError struct {
	Code       string
	Temporary  bool
	AuthFailed bool
	RetryAfter time.Duration
}

func (e *ProviderAPIError) Error() string {
	code := safeErrorCode(e.Code)
	if code == "provider_api_error" {
		return code
	}
	return "provider API error: " + code
}

type ProviderAccount struct {
	ID                      int64
	Provider                string
	Environment             ProviderEnvironment
	ExternalAccountIdentity string
	LocalBankAccountID      int64
}

type ProviderCheckpoint struct {
	ID                        int64
	ProviderAccountID         int64
	Cursor                    string
	LastTransactionExternalID string
	LastOccurredAt            time.Time
	LeaseOwner                string
	Version                   int64
}

type ReconciliationCheckpointRepository interface {
	ListActiveProviderAccounts(context.Context) ([]ProviderAccount, error)
	EnsureProviderCheckpoint(context.Context, int64) (ProviderCheckpoint, error)
	ClaimProviderCheckpoint(context.Context, int64, string, time.Time, time.Time) (ProviderCheckpoint, bool, error)
	AdvanceProviderCheckpoint(context.Context, ProviderCheckpoint, string, string, time.Time) (ProviderCheckpoint, error)
	CompleteProviderCheckpoint(context.Context, ProviderCheckpoint, time.Time) error
	FailProviderCheckpoint(context.Context, ProviderCheckpoint, string) error
}

type EventIngestor interface {
	Ingest(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error)
}

type ReconciliationMetrics interface {
	ObserveReconciliationRun(provider, result string, duration time.Duration)
	ObserveReconciliationTransaction(provider, result string)
	ObserveProviderAPIRequest(provider, operation, result string, duration time.Duration)
}

type ReconciliationSummary struct {
	Accounts   int
	Pages      int
	Fetched    int
	Ingested   int
	Duplicates int
	Skipped    int
}

type ReconciliationJob struct {
	registry       *Registry
	checkpoints    ReconciliationCheckpointRepository
	ingestor       EventIngestor
	owner          string
	maxPages       int
	pageSize       int
	requestTimeout time.Duration
	leaseDuration  time.Duration
	clock          func() time.Time
	metrics        ReconciliationMetrics
}

func NewReconciliationJob(registry *Registry, checkpoints ReconciliationCheckpointRepository, ingestor EventIngestor, owner string, maxPages, pageSize int, requestTimeout, leaseDuration time.Duration, metrics ReconciliationMetrics) (*ReconciliationJob, error) {
	owner = strings.TrimSpace(owner)
	if registry == nil || checkpoints == nil || ingestor == nil || owner == "" || len(owner) > 128 || maxPages <= 0 || maxPages > 100 || pageSize <= 0 || pageSize > 1000 || requestTimeout <= 0 || leaseDuration <= requestTimeout {
		return nil, app.ErrInvalidInput
	}
	return &ReconciliationJob{
		registry: registry, checkpoints: checkpoints, ingestor: ingestor, owner: owner,
		maxPages: maxPages, pageSize: pageSize, requestTimeout: requestTimeout,
		leaseDuration: leaseDuration, clock: time.Now, metrics: metrics,
	}, nil
}

func (j *ReconciliationJob) RunOnce(ctx context.Context) (ReconciliationSummary, error) {
	accounts, err := j.checkpoints.ListActiveProviderAccounts(ctx)
	if err != nil {
		return ReconciliationSummary{}, fmt.Errorf("list provider accounts: %w", err)
	}
	var summary ReconciliationSummary
	var runErrors []error
	for _, account := range accounts {
		provider, lookupErr := j.registry.GetTransactionAPIProvider(account.Provider)
		if lookupErr != nil || provider.Environment() != account.Environment {
			summary.Skipped++
			continue
		}
		summary.Accounts++
		started := j.clock()
		accountSummary, reconcileErr := j.reconcileAccount(ctx, provider, account)
		summary.Pages += accountSummary.Pages
		summary.Fetched += accountSummary.Fetched
		summary.Ingested += accountSummary.Ingested
		summary.Duplicates += accountSummary.Duplicates
		result := "success"
		if errors.Is(reconcileErr, ErrCheckpointLeaseUnavailable) {
			result = "leased"
			summary.Skipped++
		} else if reconcileErr != nil {
			result = "failed"
			runErrors = append(runErrors, fmt.Errorf("reconcile provider %s account %d: %w", account.Provider, account.ID, reconcileErr))
		}
		if j.metrics != nil {
			j.metrics.ObserveReconciliationRun(account.Provider, result, j.clock().Sub(started))
		}
	}
	return summary, errors.Join(runErrors...)
}

func (j *ReconciliationJob) reconcileAccount(ctx context.Context, provider TransactionAPIProvider, account ProviderAccount) (summary ReconciliationSummary, returnedErr error) {
	checkpoint, err := j.checkpoints.EnsureProviderCheckpoint(ctx, account.ID)
	if err != nil {
		return summary, err
	}
	now := j.clock()
	checkpoint, claimed, err := j.checkpoints.ClaimProviderCheckpoint(ctx, checkpoint.ID, j.owner, now, now.Add(j.leaseDuration))
	if err != nil {
		return summary, err
	}
	if !claimed {
		return summary, ErrCheckpointLeaseUnavailable
	}
	defer func() {
		if returnedErr != nil {
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j.requestTimeout)
			defer cancel()
			_ = j.checkpoints.FailProviderCheckpoint(failureCtx, checkpoint, reconciliationErrorCode(returnedErr))
		}
	}()

	cursor := checkpoint.Cursor
	for pageNumber := 1; pageNumber <= j.maxPages; pageNumber++ {
		requestStarted := j.clock()
		requestCtx, cancel := context.WithTimeout(ctx, j.requestTimeout)
		page, fetchErr := provider.ListTransactions(requestCtx, ListTransactionsRequest{
			ProviderAccountID: account.ID, ExternalAccountIdentity: account.ExternalAccountIdentity,
			Environment: account.Environment, Cursor: cursor, PageSize: j.pageSize,
		})
		cancel()
		requestResult := "success"
		if fetchErr != nil {
			requestResult = "failed"
		}
		if j.metrics != nil {
			j.metrics.ObserveProviderAPIRequest(account.Provider, "list_transactions", requestResult, j.clock().Sub(requestStarted))
		}
		if fetchErr != nil {
			return summary, fetchErr
		}
		if page.HasMore && (strings.TrimSpace(page.NextCursor) == "" || page.NextCursor == cursor) {
			return summary, ErrMalformedTransactionPage
		}
		summary.Pages++
		summary.Fetched += len(page.Transactions)
		lastTransactionID := checkpoint.LastTransactionExternalID
		lastOccurredAt := checkpoint.LastOccurredAt
		for _, event := range page.Transactions {
			event.Provider = account.Provider
			event.Environment = string(account.Environment)
			event.Source = "reconciliation"
			if event.DestinationAccountID == "" {
				event.DestinationAccountID = account.ExternalAccountIdentity
			}
			result, ingestErr := j.ingestor.Ingest(ctx, event)
			metricResult := "ingested"
			if result.Duplicate {
				metricResult = "duplicate"
				summary.Duplicates++
			} else if ingestErr == nil {
				summary.Ingested++
			}
			if ingestErr != nil {
				metricResult = "failed"
			}
			if j.metrics != nil {
				j.metrics.ObserveReconciliationTransaction(account.Provider, metricResult)
			}
			if ingestErr != nil {
				return summary, ingestErr
			}
			lastTransactionID = event.ProviderTransactionID
			if event.OccurredAt.After(lastOccurredAt) {
				lastOccurredAt = event.OccurredAt
			}
		}
		nextCursor := page.NextCursor
		if !page.HasMore && nextCursor == "" {
			nextCursor = cursor
		}
		checkpoint, err = j.checkpoints.AdvanceProviderCheckpoint(ctx, checkpoint, nextCursor, lastTransactionID, lastOccurredAt)
		if err != nil {
			return summary, err
		}
		cursor = nextCursor
		if !page.HasMore {
			if err := j.checkpoints.CompleteProviderCheckpoint(ctx, checkpoint, j.clock()); err != nil {
				return summary, err
			}
			return summary, nil
		}
	}
	if err := j.checkpoints.CompleteProviderCheckpoint(ctx, checkpoint, j.clock()); err != nil {
		return summary, err
	}
	return summary, nil
}

func reconciliationErrorCode(err error) string {
	var apiError *ProviderAPIError
	if errors.As(err, &apiError) {
		if apiError.AuthFailed {
			return "provider_api_auth_failed"
		}
		return safeErrorCode(apiError.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "provider_api_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "provider_api_cancelled"
	}
	if errors.Is(err, ErrMalformedTransactionPage) {
		return "provider_malformed_page"
	}
	return "provider_reconciliation_failed"
}

func safeErrorCode(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || len(value) > 64 {
		return "provider_api_error"
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' {
			return "provider_api_error"
		}
	}
	return value
}
