package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
)

type ProviderName string
type ProviderEnvironment string

const (
	EnvironmentDevelopment ProviderEnvironment = "development"
	EnvironmentTest        ProviderEnvironment = "test"
	EnvironmentProduction  ProviderEnvironment = "production"
)

type ProviderCapabilities struct {
	SupportsWebhook        bool
	SupportsReconciliation bool
	SupportsTestMode       bool
}

type Provider interface {
	Name() ProviderName
	Enabled() bool
	Environment() ProviderEnvironment
	Capabilities() ProviderCapabilities
}

type WebhookRequest struct {
	Headers    http.Header
	RawBody    []byte
	ReceivedAt time.Time
}

type WebhookAcknowledgement struct {
	StatusCode    int
	Headers       map[string]string
	Body          []byte
	DuplicateBody []byte
}

type WebhookProvider interface {
	Provider
	VerifyAndNormalizeWebhook(context.Context, WebhookRequest) (app.NormalizedPaymentEvent, WebhookAcknowledgement, error)
}

type ListTransactionsRequest struct {
	ProviderAccountID       int64
	ExternalAccountIdentity string
	Environment             ProviderEnvironment
	Cursor                  string
	PageSize                int
}

type TransactionPage struct {
	Transactions []app.NormalizedPaymentEvent
	NextCursor   string
	HasMore      bool
}

type TransactionAPIProvider interface {
	Provider
	ListTransactions(context.Context, ListTransactionsRequest) (TransactionPage, error)
}

var (
	ErrDuplicateProvider     = errors.New("duplicate payment provider")
	ErrProviderDisabled      = errors.New("payment provider disabled")
	ErrUnsupportedCapability = errors.New("payment provider capability unsupported")
)

type Registry struct {
	providers map[ProviderName]Provider
}

func NewProviderRegistry(providers ...Provider) (*Registry, error) {
	registry := &Registry{providers: make(map[ProviderName]Provider, len(providers))}
	for _, provider := range providers {
		if provider == nil || !validProviderName(provider.Name()) || !validEnvironment(provider.Environment()) {
			return nil, app.ErrInvalidInput
		}
		if _, exists := registry.providers[provider.Name()]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateProvider, provider.Name())
		}
		capabilities := provider.Capabilities()
		if !capabilities.SupportsWebhook && !capabilities.SupportsReconciliation {
			return nil, app.ErrInvalidInput
		}
		registry.providers[provider.Name()] = provider
	}
	return registry, nil
}

func (r *Registry) GetWebhookProvider(name string) (WebhookProvider, error) {
	provider, err := r.lookup(name)
	if err != nil {
		return nil, err
	}
	if !provider.Capabilities().SupportsWebhook {
		return nil, ErrUnsupportedCapability
	}
	webhook, ok := provider.(WebhookProvider)
	if !ok {
		return nil, ErrUnsupportedCapability
	}
	return webhook, nil
}

func (r *Registry) GetTransactionAPIProvider(name string) (TransactionAPIProvider, error) {
	provider, err := r.lookup(name)
	if err != nil {
		return nil, err
	}
	if !provider.Capabilities().SupportsReconciliation {
		return nil, ErrUnsupportedCapability
	}
	transactionAPI, ok := provider.(TransactionAPIProvider)
	if !ok {
		return nil, ErrUnsupportedCapability
	}
	return transactionAPI, nil
}

func (r *Registry) Providers() []Provider {
	if r == nil {
		return nil
	}
	result := make([]Provider, 0, len(r.providers))
	for _, provider := range r.providers {
		result = append(result, provider)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result
}

func (r *Registry) lookup(name string) (Provider, error) {
	if r == nil || !validProviderName(ProviderName(name)) {
		return nil, app.ErrUnknownPaymentProvider
	}
	provider, ok := r.providers[ProviderName(name)]
	if !ok {
		return nil, app.ErrUnknownPaymentProvider
	}
	if !provider.Enabled() {
		return nil, ErrProviderDisabled
	}
	return provider, nil
}

func validProviderName(name ProviderName) bool {
	value := string(name)
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validEnvironment(environment ProviderEnvironment) bool {
	return environment == EnvironmentDevelopment || environment == EnvironmentTest || environment == EnvironmentProduction
}

// WebhookVerifier and NewRegistry preserve the original test seam while routing
// legacy verifiers through the capability registry.
type WebhookVerifier interface {
	VerifyAndNormalize(context.Context, http.Header, []byte) (app.NormalizedPaymentEvent, error)
}

type legacyWebhookProvider struct {
	name     ProviderName
	verifier WebhookVerifier
}

func (p legacyWebhookProvider) Name() ProviderName             { return p.name }
func (legacyWebhookProvider) Enabled() bool                    { return true }
func (legacyWebhookProvider) Environment() ProviderEnvironment { return EnvironmentTest }
func (legacyWebhookProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{SupportsWebhook: true, SupportsTestMode: true}
}
func (p legacyWebhookProvider) VerifyAndNormalizeWebhook(ctx context.Context, request WebhookRequest) (app.NormalizedPaymentEvent, WebhookAcknowledgement, error) {
	event, err := p.verifier.VerifyAndNormalize(ctx, request.Headers, request.RawBody)
	ack := JSONAcknowledgement(http.StatusAccepted, []byte(`{"status":"accepted","duplicate":false}`))
	ack.DuplicateBody = []byte(`{"status":"accepted","duplicate":true}`)
	return event, ack, err
}

func NewRegistry(providers map[string]WebhookVerifier) *Registry {
	items := make([]Provider, 0, len(providers))
	for name, verifier := range providers {
		if verifier != nil {
			items = append(items, legacyWebhookProvider{name: ProviderName(name), verifier: verifier})
		}
	}
	registry, err := NewProviderRegistry(items...)
	if err != nil {
		return &Registry{providers: map[ProviderName]Provider{}}
	}
	return registry
}

func JSONAcknowledgement(status int, body []byte) WebhookAcknowledgement {
	return WebhookAcknowledgement{StatusCode: status, Headers: map[string]string{"Content-Type": "application/json"}, Body: body}
}
