package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
)

const testWebhookSecret = "0123456789abcdef"

type updateProcessorFunc func(context.Context, *models.Update, string) error

func (f updateProcessorFunc) Process(ctx context.Context, update *models.Update, requestID string) error {
	return f(ctx, update, requestID)
}

func TestTelegramWebhookValidation(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "missing secret", contentType: "application/json", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "wrong secret", secret: "wrong-secret-value", contentType: "application/json", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "wrong content type", secret: testWebhookSecret, contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "malformed json", secret: testWebhookSecret, contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "trailing json", secret: testWebhookSecret, contentType: "application/json", body: `{} {}`, wantStatus: http.StatusBadRequest},
		{name: "oversized", secret: testWebhookSecret, contentType: "application/json", body: `{"update_id":1,"padding":"` + strings.Repeat("x", 2048) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := newWebhookTestHandler(t, updateProcessorFunc(func(context.Context, *models.Update, string) error { return nil }), 512)
			request := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.secret != "" {
				request.Header.Set(telegramSecretHeader, test.secret)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestTelegramWebhookAcceptsUnknownAndStartUpdates(t *testing.T) {
	var updateTypes []string
	var requestIDs []string
	processor := updateProcessorFunc(func(_ context.Context, update *models.Update, requestID string) error {
		updateTypes = append(updateTypes, classifyWebhookTestUpdate(update))
		requestIDs = append(requestIDs, requestID)
		return nil
	})
	handler, registry := newWebhookTestHandler(t, processor, 4096)
	for index, body := range []string{
		`{"update_id":1,"future_field":{"safe":true}}`,
		`{"update_id":2,"message":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"},"from":{"id":10,"is_bot":false,"first_name":"Test"},"text":"/start"}}`,
	} {
		request := authenticatedWebhookRequest(body)
		if index == 1 {
			request.Header.Set("X-Request-ID", "phase3-request-id")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", response.Code, response.Body.String())
		}
	}
	if strings.Join(updateTypes, ",") != "unknown,message" {
		t.Fatalf("processed update types = %#v", updateTypes)
	}
	if requestIDs[1] != "phase3-request-id" {
		t.Fatalf("request ID = %q", requestIDs[1])
	}
	families, err := registry.Gather()
	if err != nil || len(families) == 0 {
		t.Fatalf("metrics Gather() = %d families, %v", len(families), err)
	}
}

func TestTelegramWebhookDuplicateProcessorSemantics(t *testing.T) {
	var mutex sync.Mutex
	completed := make(map[int64]bool)
	mutations := 0
	processor := updateProcessorFunc(func(_ context.Context, update *models.Update, _ string) error {
		mutex.Lock()
		defer mutex.Unlock()
		if completed[update.ID] {
			return nil
		}
		mutations++
		completed[update.ID] = true
		return nil
	})
	handler, _ := newWebhookTestHandler(t, processor, 4096)
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, authenticatedWebhookRequest(`{"update_id":99}`))
		if response.Code != http.StatusOK {
			t.Fatalf("duplicate status = %d", response.Code)
		}
	}
	if mutations != 1 {
		t.Fatalf("duplicate mutations = %d, want 1", mutations)
	}
}

func TestTelegramWebhookRecoveryAndProcessingError(t *testing.T) {
	tests := []struct {
		name      string
		processor updateProcessorFunc
	}{
		{name: "error", processor: func(context.Context, *models.Update, string) error { return errors.New("database unavailable") }},
		{name: "panic", processor: func(context.Context, *models.Update, string) error { panic("test panic") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := newWebhookTestHandler(t, test.processor, 4096)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authenticatedWebhookRequest(`{"update_id":100}`))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", response.Code)
			}
			if strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "panic") {
				t.Fatalf("response leaked internal error: %s", response.Body.String())
			}
		})
	}
}

func newWebhookTestHandler(t *testing.T, processor TelegramUpdateProcessor, bodyLimit int64) (http.Handler, *prometheus.Registry) {
	t.Helper()
	registry := prometheus.NewRegistry()
	httpMetrics := observability.NewHTTPMetrics(registry)
	telegramMetrics := observability.NewTelegramMetrics(registry)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	webhook := NewTelegramWebhook(testWebhookSecret, bodyLimit, time.Second, processor, telegramMetrics)
	server := NewServer(
		ServerConfig{Address: ":0", Environment: "test", PrometheusEnabled: true},
		readinessCheckFunc(func(context.Context) error { return nil }),
		httpMetrics, registry, webhook, logger,
	)
	return server.httpServer.Handler, registry
}

func authenticatedWebhookRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/webhooks/telegram", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set(telegramSecretHeader, testWebhookSecret)
	return request
}

func classifyWebhookTestUpdate(update *models.Update) string {
	if update.Message != nil {
		return "message"
	}
	return "unknown"
}
