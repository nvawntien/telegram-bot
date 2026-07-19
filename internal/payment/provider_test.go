package payment

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/nvawntien/telegram-bot/internal/app"
)

type testProvider struct {
	name         ProviderName
	enabled      bool
	environment  ProviderEnvironment
	capabilities ProviderCapabilities
}

func (p testProvider) Name() ProviderName                 { return p.name }
func (p testProvider) Enabled() bool                      { return p.enabled }
func (p testProvider) Environment() ProviderEnvironment   { return p.environment }
func (p testProvider) Capabilities() ProviderCapabilities { return p.capabilities }
func (p testProvider) VerifyAndNormalizeWebhook(context.Context, WebhookRequest) (app.NormalizedPaymentEvent, WebhookAcknowledgement, error) {
	return app.NormalizedPaymentEvent{Provider: string(p.name)}, JSONAcknowledgement(http.StatusAccepted, []byte(`{}`)), nil
}
func (p testProvider) ListTransactions(context.Context, ListTransactionsRequest) (TransactionPage, error) {
	return TransactionPage{}, nil
}

func TestProviderRegistryRoutesCapabilities(t *testing.T) {
	webhook := testProvider{name: "webhook", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true}}
	api := testProvider{name: "api", enabled: true, environment: EnvironmentProduction, capabilities: ProviderCapabilities{SupportsReconciliation: true}}
	both := testProvider{name: "both", enabled: true, environment: EnvironmentDevelopment, capabilities: ProviderCapabilities{SupportsWebhook: true, SupportsReconciliation: true}}
	disabled := testProvider{name: "disabled", environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true}}

	registry, err := NewProviderRegistry(webhook, api, both, disabled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.GetWebhookProvider("webhook"); err != nil {
		t.Fatalf("webhook provider: %v", err)
	}
	if _, err := registry.GetTransactionAPIProvider("api"); err != nil {
		t.Fatalf("api provider: %v", err)
	}
	if _, err := registry.GetWebhookProvider("both"); err != nil {
		t.Fatalf("combined webhook provider: %v", err)
	}
	if _, err := registry.GetTransactionAPIProvider("both"); err != nil {
		t.Fatalf("combined api provider: %v", err)
	}
	if _, err := registry.GetTransactionAPIProvider("webhook"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("webhook-only API lookup error = %v", err)
	}
	if _, err := registry.GetWebhookProvider("api"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("api-only webhook lookup error = %v", err)
	}
	if _, err := registry.GetWebhookProvider("disabled"); !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("disabled lookup error = %v", err)
	}
	if _, err := registry.GetWebhookProvider("unknown"); !errors.Is(err, app.ErrUnknownPaymentProvider) {
		t.Fatalf("unknown lookup error = %v", err)
	}
	providers := registry.Providers()
	for index, name := range []ProviderName{"api", "both", "disabled", "webhook"} {
		if providers[index].Name() != name {
			t.Fatalf("providers[%d] = %q", index, providers[index].Name())
		}
	}
}

func TestProviderRegistryRejectsInvalidRegistrations(t *testing.T) {
	valid := testProvider{name: "provider", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true}}
	tests := []struct {
		name      string
		providers []Provider
		want      error
	}{
		{name: "duplicate", providers: []Provider{valid, valid}, want: ErrDuplicateProvider},
		{name: "invalid name", providers: []Provider{testProvider{name: "Bad Name", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true}}}, want: app.ErrInvalidInput},
		{name: "invalid environment", providers: []Provider{testProvider{name: "provider", enabled: true, environment: "staging", capabilities: ProviderCapabilities{SupportsWebhook: true}}}, want: app.ErrInvalidInput},
		{name: "no capability", providers: []Provider{testProvider{name: "provider", enabled: true, environment: EnvironmentTest}}, want: app.ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewProviderRegistry(test.providers...)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProviderRegistryConcurrentReads(t *testing.T) {
	registry, err := NewProviderRegistry(testProvider{name: "both", enabled: true, environment: EnvironmentTest, capabilities: ProviderCapabilities{SupportsWebhook: true, SupportsReconciliation: true}})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for attempt := 0; attempt < 100; attempt++ {
				_, _ = registry.GetWebhookProvider("both")
				_, _ = registry.GetTransactionAPIProvider("both")
				_ = registry.Providers()
			}
		}()
	}
	wait.Wait()
}

func TestSignedJSONCannotBeConfiguredForProduction(t *testing.T) {
	if _, err := NewSignedJSONForEnvironment("secret", 1, EnvironmentProduction, true); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("production adapter error = %v", err)
	}
}
