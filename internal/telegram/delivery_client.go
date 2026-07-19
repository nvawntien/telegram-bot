package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

type deliveryAPIResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int32  `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int32 `json:"retry_after"`
	} `json:"parameters"`
	Result struct {
		MessageID int64 `json:"message_id"`
		Date      int64 `json:"date"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"result"`
}

func (c *Client) SendDelivery(ctx context.Context, request app.DeliverySendRequest) (app.DeliverySendResult, error) {
	if err := ctx.Err(); err != nil {
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultRetryable, "cancelled_before_send", "request cancelled before Telegram send", 0, 0, 0)
	}
	if request.ChatID <= 0 || request.Text == "" {
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultPermanent, "invalid_message", "delivery request is invalid", 0, 0, 0)
	}
	body, err := json.Marshal(map[string]any{
		"chat_id": request.ChatID, "text": request.Text, "parse_mode": "HTML",
	})
	if err != nil {
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultPermanent, "invalid_message", "delivery request is invalid", 0, 0, 0)
	}
	defer clear(body)
	endpoint := c.serverURL + "/bot" + c.token + "/sendMessage"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultPermanent, "invalid_endpoint", "Telegram endpoint is invalid", 0, 0, 0)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest.Store(true) }}
	httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), trace))
	started := time.Now()
	response, err := c.deliveryHTTP.Do(httpRequest)
	if err != nil {
		class, code, summary := domain.DeliveryResultRetryable, "connect_failed", "Telegram request was not written"
		if wroteRequest.Load() {
			class, code, summary = domain.DeliveryResultAmbiguous, "transport_after_write", "Telegram send outcome is unknown"
		}
		c.observeDeliveryAPI(code, started)
		return app.DeliverySendResult{}, deliveryError(class, code, summary, 0, 0, 0)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, c.responseLimit+1))
	if readErr != nil || int64(len(responseBody)) > c.responseLimit {
		c.observeDeliveryAPI("invalid_response", started)
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultAmbiguous, "invalid_response", "Telegram response could not be verified", int32(response.StatusCode), 0, 0)
	}
	var payload deliveryAPIResponse
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		c.observeDeliveryAPI("invalid_response", started)
		return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultAmbiguous, "invalid_response", "Telegram response could not be verified", int32(response.StatusCode), 0, 0)
	}
	if payload.OK {
		if response.StatusCode < 200 || response.StatusCode >= 300 || payload.Result.MessageID <= 0 || payload.Result.Chat.ID != request.ChatID {
			c.observeDeliveryAPI("invalid_success", started)
			return app.DeliverySendResult{}, deliveryError(domain.DeliveryResultAmbiguous, "invalid_success", "Telegram success evidence is incomplete", int32(response.StatusCode), payload.ErrorCode, 0)
		}
		sentAt := time.Now()
		if payload.Result.Date > 0 {
			sentAt = time.Unix(payload.Result.Date, 0)
		}
		c.observeDeliveryAPI("success", started)
		return app.DeliverySendResult{ChatID: payload.Result.Chat.ID, MessageID: payload.Result.MessageID, SentAt: sentAt, Method: "sendMessage"}, nil
	}
	result := classifyDeliveryAPIResponse(response.StatusCode, payload)
	c.observeDeliveryAPI(result.Code, started)
	return app.DeliverySendResult{}, result
}

func classifyDeliveryAPIResponse(httpStatus int, response deliveryAPIResponse) *app.DeliverySendError {
	errorCode := response.ErrorCode
	if errorCode == 0 {
		errorCode = int32(httpStatus)
	}
	if httpStatus == http.StatusTooManyRequests || errorCode == http.StatusTooManyRequests {
		return deliveryError(domain.DeliveryResultRetryable, "rate_limited", "Telegram requested a delayed retry", int32(httpStatus), errorCode, time.Duration(response.Parameters.RetryAfter)*time.Second)
	}
	if httpStatus >= 500 || errorCode >= 500 {
		return deliveryError(domain.DeliveryResultRetryable, "server_error", "Telegram returned a temporary server error", int32(httpStatus), errorCode, 0)
	}
	description := strings.ToLower(response.Description)
	switch {
	case strings.Contains(description, "bot was blocked") || strings.Contains(description, "bot blocked"):
		return deliveryError(domain.DeliveryResultPermanent, "bot_blocked", "recipient blocked the bot", int32(httpStatus), errorCode, 0)
	case strings.Contains(description, "chat not found"):
		return deliveryError(domain.DeliveryResultPermanent, "chat_not_found", "recipient chat was not found", int32(httpStatus), errorCode, 0)
	case httpStatus == http.StatusForbidden || errorCode == http.StatusForbidden:
		return deliveryError(domain.DeliveryResultPermanent, "forbidden", "Telegram rejected the recipient", int32(httpStatus), errorCode, 0)
	case httpStatus == http.StatusUnauthorized || errorCode == http.StatusUnauthorized:
		return deliveryError(domain.DeliveryResultPermanent, "unauthorized", "Telegram rejected bot authorization", int32(httpStatus), errorCode, 0)
	case httpStatus == http.StatusBadRequest || errorCode == http.StatusBadRequest:
		return deliveryError(domain.DeliveryResultPermanent, "invalid_message", "Telegram rejected the delivery message", int32(httpStatus), errorCode, 0)
	default:
		return deliveryError(domain.DeliveryResultAmbiguous, "unclassified_response", "Telegram response did not prove delivery outcome", int32(httpStatus), errorCode, 0)
	}
}

func deliveryError(class domain.DeliveryResultClass, code, summary string, httpStatus, telegramCode int32, retryAfter time.Duration) *app.DeliverySendError {
	return &app.DeliverySendError{
		Class: class, Code: code, Summary: summary, HTTPStatus: httpStatus,
		TelegramErrorCode: telegramCode, RetryAfter: retryAfter,
	}
}

func (c *Client) observeDeliveryAPI(result string, started time.Time) {
	if c.metrics != nil {
		c.metrics.ObserveTelegramAPI("sendMessage", result, time.Since(started))
	}
}

var _ app.DeliverySender = (*Client)(nil)
