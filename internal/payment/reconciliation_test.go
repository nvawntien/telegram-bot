package payment

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

type fakeAPIProvider struct {
	testProvider
	mu       sync.Mutex
	pages    map[string]TransactionPage
	errors   map[string]error
	block    bool
	requests []ListTransactionsRequest
}

func (p *fakeAPIProvider) ListTransactions(ctx context.Context, request ListTransactionsRequest) (TransactionPage, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	block := p.block
	page := p.pages[request.Cursor]
	err := p.errors[request.Cursor]
	p.mu.Unlock()
	if block {
		<-ctx.Done()
		return TransactionPage{}, ctx.Err()
	}
	return page, err
}

type memoryCheckpoints struct {
	accounts      []ProviderAccount
	checkpoint    ProviderCheckpoint
	ensureCount   int
	advanceCount  int
	completeCount int
	failCode      string
	claimed       bool
}

func (m *memoryCheckpoints) ListActiveProviderAccounts(context.Context) ([]ProviderAccount, error) {
	return append([]ProviderAccount(nil), m.accounts...), nil
}
func (m *memoryCheckpoints) EnsureProviderCheckpoint(_ context.Context, accountID int64) (ProviderCheckpoint, error) {
	m.ensureCount++
	if m.checkpoint.ID == 0 {
		m.checkpoint = ProviderCheckpoint{ID: 1, ProviderAccountID: accountID, Version: 1}
	}
	return m.checkpoint, nil
}
func (m *memoryCheckpoints) ClaimProviderCheckpoint(_ context.Context, id int64, owner string, _, _ time.Time) (ProviderCheckpoint, bool, error) {
	if m.claimed {
		return ProviderCheckpoint{}, false, nil
	}
	m.claimed = true
	m.checkpoint.ID = id
	m.checkpoint.LeaseOwner = owner
	m.checkpoint.Version++
	return m.checkpoint, true, nil
}
func (m *memoryCheckpoints) AdvanceProviderCheckpoint(_ context.Context, checkpoint ProviderCheckpoint, cursor, transactionID string, occurredAt time.Time) (ProviderCheckpoint, error) {
	if checkpoint.Version != m.checkpoint.Version || checkpoint.LeaseOwner == "" {
		return ProviderCheckpoint{}, ErrCheckpointLeaseUnavailable
	}
	m.advanceCount++
	m.checkpoint.Cursor = cursor
	m.checkpoint.LastTransactionExternalID = transactionID
	m.checkpoint.LastOccurredAt = occurredAt
	m.checkpoint.Version++
	return m.checkpoint, nil
}
func (m *memoryCheckpoints) CompleteProviderCheckpoint(_ context.Context, checkpoint ProviderCheckpoint, _ time.Time) error {
	if checkpoint.Version != m.checkpoint.Version {
		return ErrCheckpointLeaseUnavailable
	}
	m.completeCount++
	m.checkpoint.Version++
	m.checkpoint.LeaseOwner = ""
	return nil
}
func (m *memoryCheckpoints) FailProviderCheckpoint(_ context.Context, checkpoint ProviderCheckpoint, code string) error {
	if checkpoint.Version != m.checkpoint.Version {
		return ErrCheckpointLeaseUnavailable
	}
	m.failCode = code
	m.checkpoint.Version++
	m.checkpoint.LeaseOwner = ""
	return nil
}

type memoryIngestor struct {
	seen   map[string]int64
	events []app.NormalizedPaymentEvent
	failAt int
}

func (i *memoryIngestor) Ingest(_ context.Context, event app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error) {
	if i.failAt > 0 && len(i.events)+1 == i.failAt {
		return app.PaymentEventIngestionResult{}, errors.New("durable ingest unavailable")
	}
	i.events = append(i.events, event)
	if id, exists := i.seen[event.ProviderTransactionID]; exists {
		return app.PaymentEventIngestionResult{EventID: id, Duplicate: true}, nil
	}
	id := int64(len(i.seen) + 1)
	i.seen[event.ProviderTransactionID] = id
	return app.PaymentEventIngestionResult{EventID: id}, nil
}

func TestReconciliationMultiplePagesAdvanceAfterDurableIngestion(t *testing.T) {
	provider := &fakeAPIProvider{
		testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}},
		pages: map[string]TransactionPage{
			"":         {Transactions: []app.NormalizedPaymentEvent{reconciliationEvent("event-1", "transaction-1")}, NextCursor: "opaque-1", HasMore: true},
			"opaque-1": {Transactions: []app.NormalizedPaymentEvent{reconciliationEvent("event-2", "transaction-2")}, NextCursor: "opaque-2"},
		}, errors: map[string]error{},
	}
	registry, err := NewProviderRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := &memoryCheckpoints{accounts: []ProviderAccount{{ID: 7, Provider: "api", Environment: EnvironmentTest, ExternalAccountIdentity: "account-test"}}}
	ingestor := &memoryIngestor{seen: map[string]int64{}}
	job, err := NewReconciliationJob(registry, checkpoints, ingestor, "worker-1", 5, 100, time.Second, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := job.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pages != 2 || summary.Fetched != 2 || summary.Ingested != 2 || checkpoints.advanceCount != 2 || checkpoints.completeCount != 1 || checkpoints.checkpoint.Cursor != "opaque-2" {
		t.Fatalf("summary=%+v checkpoint=%+v advances=%d completes=%d", summary, checkpoints.checkpoint, checkpoints.advanceCount, checkpoints.completeCount)
	}
	for _, event := range ingestor.events {
		if event.Provider != "api" || event.Environment != "test" || event.Source != "reconciliation" || event.DestinationAccountID != "account-test" {
			t.Fatalf("normalized event = %+v", event)
		}
	}
}

func TestReconciliationDoesNotAdvanceFailedPage(t *testing.T) {
	provider := &fakeAPIProvider{
		testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}},
		pages: map[string]TransactionPage{"": {Transactions: []app.NormalizedPaymentEvent{
			reconciliationEvent("event-1", "transaction-1"), reconciliationEvent("event-2", "transaction-2"),
		}}}, errors: map[string]error{},
	}
	registry, _ := NewProviderRegistry(provider)
	checkpoints := &memoryCheckpoints{accounts: []ProviderAccount{{ID: 7, Provider: "api", Environment: EnvironmentTest, ExternalAccountIdentity: "account-test"}}}
	ingestor := &memoryIngestor{seen: map[string]int64{}, failAt: 2}
	job, _ := NewReconciliationJob(registry, checkpoints, ingestor, "worker-1", 5, 100, time.Second, time.Minute, nil)
	if _, err := job.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce() succeeded")
	}
	if checkpoints.advanceCount != 0 || checkpoints.completeCount != 0 || checkpoints.failCode != "provider_reconciliation_failed" || checkpoints.checkpoint.Cursor != "" {
		t.Fatalf("checkpoint mutated after failure: %+v", checkpoints)
	}
}

func TestReconciliationCapabilityEnvironmentAndLeaseIsolation(t *testing.T) {
	api := &fakeAPIProvider{testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}}, pages: map[string]TransactionPage{"": {}}, errors: map[string]error{}}
	webhook := testProvider{name: "webhook", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true}}
	registry, _ := NewProviderRegistry(api, webhook)
	checkpoints := &memoryCheckpoints{accounts: []ProviderAccount{
		{ID: 1, Provider: "webhook", Environment: EnvironmentTest},
		{ID: 2, Provider: "api", Environment: EnvironmentProduction},
	}}
	job, _ := NewReconciliationJob(registry, checkpoints, &memoryIngestor{seen: map[string]int64{}}, "worker", 1, 10, time.Second, time.Minute, nil)
	summary, err := job.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Skipped != 2 || checkpoints.ensureCount != 0 {
		t.Fatalf("summary=%+v ensure_count=%d", summary, checkpoints.ensureCount)
	}

	checkpoints.accounts = []ProviderAccount{{ID: 3, Provider: "api", Environment: EnvironmentTest}}
	checkpoints.claimed = true
	summary, err = job.RunOnce(context.Background())
	if err != nil || summary.Skipped != 1 || checkpoints.advanceCount != 0 {
		t.Fatalf("leased summary=%+v err=%v", summary, err)
	}
}

func TestReconciliationRejectsMalformedCursorAndHonorsTimeout(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider *fakeAPIProvider
		wantCode string
	}{
		{name: "malformed cursor", provider: &fakeAPIProvider{testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}}, pages: map[string]TransactionPage{"": {NextCursor: "", HasMore: true}}, errors: map[string]error{}}, wantCode: "provider_malformed_page"},
		{name: "timeout", provider: &fakeAPIProvider{testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}}, block: true}, wantCode: "provider_api_timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewProviderRegistry(test.provider)
			checkpoints := &memoryCheckpoints{accounts: []ProviderAccount{{ID: 1, Provider: "api", Environment: EnvironmentTest}}}
			job, _ := NewReconciliationJob(registry, checkpoints, &memoryIngestor{seen: map[string]int64{}}, "worker", 2, 10, 5*time.Millisecond, time.Second, nil)
			if _, err := job.RunOnce(context.Background()); err == nil {
				t.Fatal("RunOnce() succeeded")
			}
			if checkpoints.advanceCount != 0 || checkpoints.failCode != test.wantCode {
				t.Fatalf("checkpoint=%+v", checkpoints)
			}
		})
	}
}

func TestReconciliationDuplicatePollingIsIdempotent(t *testing.T) {
	event := reconciliationEvent("event-1", "transaction-1")
	provider := &fakeAPIProvider{
		testProvider: testProvider{name: "api", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsReconciliation: true}},
		pages:        map[string]TransactionPage{"": {Transactions: []app.NormalizedPaymentEvent{event, event}}}, errors: map[string]error{},
	}
	registry, _ := NewProviderRegistry(provider)
	checkpoints := &memoryCheckpoints{accounts: []ProviderAccount{{ID: 1, Provider: "api", Environment: EnvironmentTest}}}
	ingestor := &memoryIngestor{seen: map[string]int64{}}
	job, _ := NewReconciliationJob(registry, checkpoints, ingestor, "worker", 1, 10, time.Second, time.Minute, nil)
	summary, err := job.RunOnce(context.Background())
	if err != nil || summary.Ingested != 1 || summary.Duplicates != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
}

func TestProviderAPIErrorNeverReflectsCredential(t *testing.T) {
	secret := "api-token-super-secret"
	err := (&ProviderAPIError{Code: secret, AuthFailed: true}).Error()
	if strings.Contains(err, secret) {
		t.Fatalf("error leaked credential: %q", err)
	}
}

func reconciliationEvent(eventID, transactionID string) app.NormalizedPaymentEvent {
	payloadHash := sha256.Sum256([]byte(eventID))
	return app.NormalizedPaymentEvent{
		ExternalEventID: eventID, ProviderTransactionID: transactionID, EventType: "payment.received",
		Direction: "inbound", TransferContent: "TS001122334455", Amount: domain.Money(100_000),
		Currency: "VND", OccurredAt: time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
		PayloadHash: payloadHash[:], SanitizedMetadata: []byte(`{"reference":"TS001122334455","amount_vnd":100000,"currency":"VND","occurred_at":"2026-07-19T10:00:00Z"}`),
	}
}
