package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestDeliveryClientReturnsConfirmedMessageEvidence(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		captured = string(body)
		telegramDeliveryResponse(writer, http.StatusOK, map[string]any{
			"ok": true, "result": map[string]any{"message_id": 91, "date": time.Now().Unix(), "chat": map[string]any{"id": 44}},
		})
	}))
	defer server.Close()
	client, err := NewClient("123:test-token", server.URL, time.Second, 4096, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.SendDelivery(context.Background(), app.DeliverySendRequest{ChatID: 44, Text: "<code>opaque</code>"})
	if err != nil || result.ChatID != 44 || result.MessageID != 91 || result.Method != "sendMessage" {
		t.Fatalf("SendDelivery() = %#v, %v", result, err)
	}
	if calls.Load() != 1 || !strings.Contains(captured, `\u003ccode\u003eopaque\u003c/code\u003e`) {
		t.Fatalf("calls=%d body=%s", calls.Load(), captured)
	}
}

func TestDeliveryClientClassifiesTelegramResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    int
		payload   any
		wantClass domain.DeliveryResultClass
		wantCode  string
		wantRetry time.Duration
	}{
		{"rate limit", 429, map[string]any{"ok": false, "error_code": 429, "description": "retry", "parameters": map[string]any{"retry_after": 7}}, domain.DeliveryResultRetryable, "rate_limited", 7 * time.Second},
		{"server error", 500, map[string]any{"ok": false, "error_code": 500, "description": "temporary"}, domain.DeliveryResultRetryable, "server_error", 0},
		{"blocked", 403, map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"}, domain.DeliveryResultPermanent, "bot_blocked", 0},
		{"chat missing", 400, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}, domain.DeliveryResultPermanent, "chat_not_found", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				telegramDeliveryResponse(writer, test.status, test.payload)
			}))
			defer server.Close()
			client, err := NewClient("123:test-token", server.URL, time.Second, 4096, nil)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			_, err = client.SendDelivery(context.Background(), app.DeliverySendRequest{ChatID: 44, Text: "delivery"})
			var sendError *app.DeliverySendError
			if !errors.As(err, &sendError) || sendError.Class != test.wantClass || sendError.Code != test.wantCode || sendError.RetryAfter != test.wantRetry {
				t.Fatalf("error = %#v, want %s/%s/%s", err, test.wantClass, test.wantCode, test.wantRetry)
			}
		})
	}
}

func TestDeliveryClientTreatsUnverifiableResultsAsAmbiguous(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"malformed response", func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "not-json") }},
		{"response lost after accepted request", func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.ReadAll(request.Body)
			<-request.Context().Done()
		}},
		{"connection reset after request", func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.ReadAll(request.Body)
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				return
			}
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			client, err := NewClient("123:test-token", server.URL, 25*time.Millisecond, 4096, nil)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			_, err = client.SendDelivery(context.Background(), app.DeliverySendRequest{ChatID: 44, Text: "delivery"})
			var sendError *app.DeliverySendError
			if !errors.As(err, &sendError) || sendError.Class != domain.DeliveryResultAmbiguous {
				t.Fatalf("error = %#v, want ambiguous", err)
			}
			if strings.Contains(err.Error(), "123:test-token") {
				t.Fatal("error leaked bot token")
			}
		})
	}
}

func TestDeliveryClientConnectFailureIsRetryableBeforeWrite(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()
	client, err := NewClient("123:test-token", serverURL, 100*time.Millisecond, 4096, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.SendDelivery(context.Background(), app.DeliverySendRequest{ChatID: 44, Text: "delivery"})
	var sendError *app.DeliverySendError
	if !errors.As(err, &sendError) || sendError.Class != domain.DeliveryResultRetryable || sendError.Code != "connect_failed" {
		t.Fatalf("error = %#v", err)
	}
}

func telegramDeliveryResponse(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}
