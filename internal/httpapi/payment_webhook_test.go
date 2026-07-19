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
