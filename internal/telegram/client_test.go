package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCallsTelegramMethods(t *testing.T) {
	requests := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/answerCallbackQuery") {
			_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
			return
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	client, err := NewClient("123:test-token", server.URL, time.Second, 1024, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx := context.Background()
	if err := client.SendMessage(ctx, SendMessageRequest{ChatID: 10, Text: "hello"}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if err := client.EditMessage(ctx, EditMessageRequest{ChatID: 10, MessageID: 1, Text: "edited"}); err != nil {
		t.Fatalf("EditMessage() error = %v", err)
	}
	if err := client.AnswerCallback(ctx, AnswerCallbackRequest{CallbackID: "callback"}); err != nil {
		t.Fatalf("AnswerCallback() error = %v", err)
	}
	close(requests)
	paths := strings.Join(channelValues(requests), ",")
	for _, method := range []string{"sendMessage", "editMessageText", "answerCallbackQuery"} {
		if !strings.Contains(paths, "/bot123:test-token/"+method) {
			t.Errorf("Telegram method %s not called; paths = %s", method, paths)
		}
	}
}

func TestClientClassifiesFailuresWithoutLeakingToken(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		limit     int64
		wantKind  string
		wantRetry time.Duration
	}{
		{name: "rate limit", handler: telegramResponse(http.StatusTooManyRequests, map[string]any{
			"ok": false, "error_code": 429, "description": "retry", "parameters": map[string]any{"retry_after": 7},
		}), limit: 1024, wantKind: "rate_limited", wantRetry: 7 * time.Second},
		{name: "invalid json", handler: func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "not-json") }, limit: 1024, wantKind: "invalid_response"},
		{name: "response too large", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", 2048))
		}, limit: 128, wantKind: "response_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			const token = "123:never-leak-this-token"
			client, err := NewClient(token, server.URL, time.Second, test.limit, nil)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			err = client.SendMessage(context.Background(), SendMessageRequest{ChatID: 1, Text: "test"})
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Kind != test.wantKind || apiError.RetryAfter != test.wantRetry {
				t.Fatalf("SendMessage() error = %#v, want %s/%s", err, test.wantKind, test.wantRetry)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatal("error leaked bot token")
			}
		})
	}
}

func TestClientHonorsTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":1,"type":"private"}}}`)
	}))
	defer server.Close()
	client, err := NewClient("123:test", server.URL, 20*time.Millisecond, 1024, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	err = client.SendMessage(context.Background(), SendMessageRequest{ChatID: 1, Text: "timeout"})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != "timeout" {
		t.Fatalf("timeout error = %#v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.SendMessage(ctx, SendMessageRequest{ChatID: 1, Text: "cancel"})
	if !errors.As(err, &apiError) || apiError.Kind != "cancelled" {
		t.Fatalf("cancellation error = %#v", err)
	}
}

func telegramResponse(status int, payload any) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_ = json.NewEncoder(writer).Encode(payload)
	}
}

func channelValues(values <-chan string) []string {
	result := make([]string, 0)
	for value := range values {
		result = append(result, value)
	}
	return result
}
