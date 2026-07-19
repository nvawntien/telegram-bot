package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/domain"
)

const (
	SignedJSONProvider = "signed_json"
	signatureHeader    = "X-Payment-Signature"
	timestampHeader    = "X-Payment-Timestamp"
)

var referencePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{2,127}$`)

type SignedJSON struct {
	secret    []byte
	tolerance time.Duration
	clock     func() time.Time
}

type signedJSONPayload struct {
	EventID         string         `json:"event_id"`
	TransactionID   string         `json:"transaction_id"`
	EventType       string         `json:"event_type"`
	Reference       string         `json:"reference"`
	AmountVND       int64          `json:"amount_vnd"`
	Currency        string         `json:"currency"`
	Timestamp       int64          `json:"timestamp"`
	OccurredAt      time.Time      `json:"occurred_at"`
	ReceivedAccount string         `json:"received_account,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

func NewSignedJSON(secret string, tolerance time.Duration) (*SignedJSON, error) {
	if strings.TrimSpace(secret) == "" || tolerance <= 0 {
		return nil, app.ErrInvalidInput
	}
	return &SignedJSON{secret: []byte(secret), tolerance: tolerance, clock: time.Now}, nil
}

func (s *SignedJSON) VerifyAndNormalize(_ context.Context, headers http.Header, body []byte) (app.NormalizedPaymentEvent, error) {
	provided, err := hex.DecodeString(strings.TrimSpace(headers.Get(signatureHeader)))
	if err != nil || len(provided) != sha256.Size {
		return app.NormalizedPaymentEvent{}, app.ErrInvalidWebhookSignature
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(provided, expected) != 1 {
		return app.NormalizedPaymentEvent{}, app.ErrInvalidWebhookSignature
	}

	headerTimestamp, err := strconv.ParseInt(strings.TrimSpace(headers.Get(timestampHeader)), 10, 64)
	if err != nil {
		return app.NormalizedPaymentEvent{}, app.ErrWebhookReplay
	}
	var payload signedJSONPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return app.NormalizedPaymentEvent{}, app.ErrInvalidInput
	}
	if payload.Timestamp != headerTimestamp || outsideTolerance(s.clock(), time.Unix(payload.Timestamp, 0), s.tolerance) {
		return app.NormalizedPaymentEvent{}, app.ErrWebhookReplay
	}
	if !validPayload(payload) {
		return app.NormalizedPaymentEvent{}, app.ErrInvalidInput
	}
	sanitized, err := json.Marshal(map[string]any{
		"reference": payload.Reference, "amount_vnd": payload.AmountVND,
		"currency": payload.Currency, "occurred_at": payload.OccurredAt,
		"metadata": payload.Metadata,
	})
	if err != nil {
		return app.NormalizedPaymentEvent{}, app.ErrInvalidInput
	}
	hash := sha256.Sum256(body)
	return app.NormalizedPaymentEvent{
		Provider: SignedJSONProvider, ExternalEventID: payload.EventID,
		ProviderTransactionID: payload.TransactionID, Reference: payload.Reference,
		Amount: domain.Money(payload.AmountVND), Currency: payload.Currency,
		OccurredAt: payload.OccurredAt, ReceivedAccount: maskAccount(payload.ReceivedAccount),
		EventType: payload.EventType, PayloadHash: hash[:], SanitizedMetadata: sanitized,
	}, nil
}

func validPayload(payload signedJSONPayload) bool {
	return strings.TrimSpace(payload.EventID) == payload.EventID && payload.EventID != "" && len(payload.EventID) <= 128 &&
		strings.TrimSpace(payload.TransactionID) == payload.TransactionID && payload.TransactionID != "" && len(payload.TransactionID) <= 128 &&
		payload.EventType == "payment.received" && referencePattern.MatchString(payload.Reference) &&
		payload.AmountVND > 0 && payload.Currency == "VND" && !payload.OccurredAt.IsZero()
}

func outsideTolerance(now, timestamp time.Time, tolerance time.Duration) bool {
	delta := now.Sub(timestamp)
	return delta > tolerance || delta < -tolerance
}

func maskAccount(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return ""
	}
	return "***" + value[len(value)-4:]
}
