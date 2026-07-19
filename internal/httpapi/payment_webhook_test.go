package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/nvawntien/telegram-bot/internal/payment"
	"github.com/prometheus/client_golang/prometheus"
)

type webhookVerifierFunc func(context.Context, http.Header, []byte) (app.NormalizedPaymentEvent, error)

func (f webhookVerifierFunc) VerifyAndNormalize(ctx context.Context, header http.Header, body []byte) (app.NormalizedPaymentEvent, error) {
	return f(ctx, header, body)
}

type paymentIngesterFunc func(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error)

func (f paymentIngesterFunc) Ingest(ctx context.Context, event app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error) {
	return f(ctx, event)
}

type typedWebhookProvider struct {
	name    payment.ProviderName
	enabled bool
	ack     payment.WebhookAcknowledgement
}

func (p typedWebhookProvider) Name() payment.ProviderName             { return p.name }
func (p typedWebhookProvider) Enabled() bool                          { return p.enabled }
func (typedWebhookProvider) Environment() payment.ProviderEnvironment { return payment.EnvironmentTest }
func (typedWebhookProvider) Capabilities() payment.ProviderCapabilities {
	return payment.ProviderCapabilities{SupportsWebhook: true, SupportsTestMode: true}
}
func (p typedWebhookProvider) VerifyAndNormalizeWebhook(context.Context, payment.WebhookRequest) (app.NormalizedPaymentEvent, payment.WebhookAcknowledgement, error) {
	return app.NormalizedPaymentEvent{Provider: string(p.name), Source: "webhook"}, p.ack, nil
}

type typedAPIOnlyProvider struct{}

func (typedAPIOnlyProvider) Name() payment.ProviderName               { return "api_only" }
func (typedAPIOnlyProvider) Enabled() bool                            { return true }
func (typedAPIOnlyProvider) Environment() payment.ProviderEnvironment { return payment.EnvironmentTest }
func (typedAPIOnlyProvider) Capabilities() payment.ProviderCapabilities {
	return payment.ProviderCapabilities{SupportsReconciliation: true, SupportsTestMode: true}
}
func (typedAPIOnlyProvider) ListTransactions(context.Context, payment.ListTransactionsRequest) (payment.TransactionPage, error) {
	return payment.TransactionPage{}, nil
}

func TestPaymentWebhookPolicy(t *testing.T) {
	validEvent := app.NormalizedPaymentEvent{Provider: "signed_json", ExternalEventID: "e", ProviderTransactionID: "t", Reference: "TS-ABC", Amount: 1, Currency: "VND", OccurredAt: time.Now(), EventType: "payment.received", PayloadHash: make([]byte, 32), SanitizedMetadata: []byte(`{}`)}
	tests := []struct {
		name, provider, contentType, body string
		verifyErr, ingestErr              error
		want                              int
	}{
		{name: "unknown provider", provider: "unknown", contentType: "application/json", body: `{}`, want: http.StatusNotFound},
		{name: "unsupported content type", provider: "signed_json", contentType: "text/plain", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "invalid signature", provider: "signed_json", contentType: "application/json", body: `{}`, verifyErr: app.ErrInvalidWebhookSignature, want: http.StatusUnauthorized},
		{name: "replay", provider: "signed_json", contentType: "application/json", body: `{}`, verifyErr: app.ErrWebhookReplay, want: http.StatusUnauthorized},
		{name: "malformed", provider: "signed_json", contentType: "application/json", body: `{`, verifyErr: app.ErrInvalidInput, want: http.StatusBadRequest},
		{name: "ingestion failure", provider: "signed_json", contentType: "application/json", body: `{}`, ingestErr: errors.New("database unavailable"), want: http.StatusServiceUnavailable},
		{name: "accepted", provider: "signed_json", contentType: "application/json", body: `{}`, want: http.StatusAccepted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := webhookVerifierFunc(func(context.Context, http.Header, []byte) (app.NormalizedPaymentEvent, error) {
				return validEvent, test.verifyErr
			})
			ingester := paymentIngesterFunc(func(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error) {
				return app.PaymentEventIngestionResult{}, test.ingestErr
			})
			handler := newPaymentWebhookTestServer(t, payment.NewRegistry(map[string]payment.WebhookVerifier{"signed_json": verifier}), ingester, 128)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/payments/"+test.provider, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestPaymentWebhookProviderCapabilityAndAcknowledgement(t *testing.T) {
	tests := []struct {
		name     string
		provider payment.Provider
		path     string
		want     int
		body     string
		header   string
	}{
		{name: "disabled", provider: typedWebhookProvider{name: "disabled", enabled: false, ack: payment.JSONAcknowledgement(202, []byte(`{}`))}, path: "disabled", want: http.StatusNotFound},
		{name: "API only", provider: typedAPIOnlyProvider{}, path: "api_only", want: http.StatusNotFound},
		{name: "provider acknowledgement", provider: typedWebhookProvider{name: "custom", enabled: true, ack: payment.WebhookAcknowledgement{StatusCode: 204, Headers: map[string]string{"Content-Type": "text/plain", "X-Provider-ACK": "ok"}, Body: nil}}, path: "custom", want: http.StatusNoContent, header: "ok"},
		{name: "invalid acknowledgement status", provider: typedWebhookProvider{name: "bad_status", enabled: true, ack: payment.JSONAcknowledgement(500, []byte(`{}`))}, path: "bad_status", want: http.StatusServiceUnavailable},
		{name: "invalid acknowledgement secret header", provider: typedWebhookProvider{name: "bad_header", enabled: true, ack: payment.WebhookAcknowledgement{StatusCode: 202, Headers: map[string]string{"Content-Type": "application/json", "Authorization": "secret"}, Body: []byte(`{}`)}}, path: "bad_header", want: http.StatusServiceUnavailable},
		{name: "invalid acknowledgement oversized", provider: typedWebhookProvider{name: "bad_body", enabled: true, ack: payment.WebhookAcknowledgement{StatusCode: 202, Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(strings.Repeat("x", 4097))}}, path: "bad_body", want: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := payment.NewProviderRegistry(test.provider)
			if err != nil {
				t.Fatal(err)
			}
			handler := newPaymentWebhookTestServer(t, registry, paymentIngesterFunc(func(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error) {
				return app.PaymentEventIngestionResult{}, nil
			}), 1024)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/payments/"+test.path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || (test.header != "" && response.Header().Get("X-Provider-ACK") != test.header) {
				t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("response leaked secret: %s", response.Body.String())
			}
		})
	}
}

func TestPaymentWebhookBodyLimitAndDuplicateACK(t *testing.T) {
	verifier := webhookVerifierFunc(func(context.Context, http.Header, []byte) (app.NormalizedPaymentEvent, error) {
		return app.NormalizedPaymentEvent{}, nil
	})
	duplicate := paymentIngesterFunc(func(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error) {
		return app.PaymentEventIngestionResult{Duplicate: true}, nil
	})
	handler := newPaymentWebhookTestServer(t, payment.NewRegistry(map[string]payment.WebhookVerifier{"signed_json": verifier}), duplicate, 4)

	request := httptest.NewRequest(http.MethodPost, "/webhooks/payments/signed_json", bytes.NewBufferString("12345"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d", response.Code)
	}

	handler = newPaymentWebhookTestServer(t, payment.NewRegistry(map[string]payment.WebhookVerifier{"signed_json": verifier}), duplicate, 32)
	request = httptest.NewRequest(http.MethodPost, "/webhooks/payments/signed_json", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate response = %d %s", response.Code, response.Body.String())
	}
}

func newPaymentWebhookTestServer(t *testing.T, providers *payment.Registry, ingester PaymentEventIngester, limit int64) http.Handler {
	t.Helper()
	registry := prometheus.NewRegistry()
	server := NewServer(ServerConfig{Address: ":0", Environment: "test"}, nil, observability.NewHTTPMetrics(registry), registry, nil, NewPaymentWebhook(providers, ingester, limit, time.Second, observability.NewPaymentMetrics(registry)), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return server.httpServer.Handler
}
