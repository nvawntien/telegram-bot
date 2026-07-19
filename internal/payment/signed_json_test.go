package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
)

const testSigningSecret = "private-test-secret"

func TestSignedJSONVerifyAndNormalize(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	verifier := newTestVerifier(t, now, time.Minute)
	body := validBody(now)
	headers := signedHeaders(body, now)

	event, err := verifier.VerifyAndNormalize(context.Background(), headers, body)
	if err != nil {
		t.Fatalf("VerifyAndNormalize() error = %v", err)
	}
	if event.Provider != SignedJSONProvider || event.ExternalEventID != "event-1" || event.ProviderTransactionID != "transaction-1" || event.Reference != "TS-ABC123" || event.Amount != 125000 || event.Currency != "VND" {
		t.Fatalf("normalized event = %+v", event)
	}
	if event.ReceivedAccount != "***6789" || len(event.PayloadHash) != sha256.Size || strings.Contains(string(event.SanitizedMetadata), "123456789") {
		t.Fatalf("sensitive normalization = account:%q metadata:%s", event.ReceivedAccount, event.SanitizedMetadata)
	}
}

func TestSignedJSONRejectsSignatureTamperingAndReplay(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		body    []byte
		headers func([]byte) http.Header
		want    error
	}{
		{name: "missing signature", body: validBody(now), headers: func([]byte) http.Header { return http.Header{"X-Payment-Timestamp": []string{fmt.Sprint(now.Unix())}} }, want: app.ErrInvalidWebhookSignature},
		{name: "wrong signature", body: validBody(now), headers: func([]byte) http.Header {
			return http.Header{"X-Payment-Signature": []string{strings.Repeat("00", 32)}, "X-Payment-Timestamp": []string{fmt.Sprint(now.Unix())}}
		}, want: app.ErrInvalidWebhookSignature},
		{name: "tampered body", body: []byte("{}"), headers: func([]byte) http.Header { return signedHeaders(validBody(now), now) }, want: app.ErrInvalidWebhookSignature},
		{name: "whitespace modified body", body: append(validBody(now), ' '), headers: func([]byte) http.Header { return signedHeaders(validBody(now), now) }, want: app.ErrInvalidWebhookSignature},
		{name: "malformed signature", body: validBody(now), headers: func([]byte) http.Header {
			return http.Header{"X-Payment-Signature": []string{"not-hex"}, "X-Payment-Timestamp": []string{fmt.Sprint(now.Unix())}}
		}, want: app.ErrInvalidWebhookSignature},
		{name: "old timestamp", body: validBody(now.Add(-time.Minute - time.Second)), headers: func(body []byte) http.Header { return signedHeaders(body, now.Add(-time.Minute-time.Second)) }, want: app.ErrWebhookReplay},
		{name: "future timestamp", body: validBody(now.Add(time.Minute + time.Second)), headers: func(body []byte) http.Header { return signedHeaders(body, now.Add(time.Minute+time.Second)) }, want: app.ErrWebhookReplay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := newTestVerifier(t, now, time.Minute)
			_, err := verifier.VerifyAndNormalize(context.Background(), test.headers(test.body), test.body)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSignedJSONRejectsMalformedPaymentFields(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	valid := string(validBody(now))
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "malformed JSON", body: "{"},
		{name: "missing event ID", body: strings.Replace(valid, `"event_id":"event-1"`, `"event_id":""`, 1)},
		{name: "missing transaction ID", body: strings.Replace(valid, `"transaction_id":"transaction-1"`, `"transaction_id":""`, 1)},
		{name: "invalid direction", body: strings.Replace(valid, `"direction":"inbound"`, `"direction":"unknown"`, 1)},
		{name: "invalid amount", body: strings.Replace(valid, `"amount_vnd":125000`, `"amount_vnd":0`, 1)},
		{name: "invalid currency", body: strings.Replace(valid, `"currency":"VND"`, `"currency":"USD"`, 1)},
		{name: "invalid occurred at", body: strings.Replace(valid, now.Format(time.RFC3339), "0001-01-01T00:00:00Z", 1)},
		{name: "missing destination", body: strings.Replace(valid, `"destination_account_identity":"reference-account-1"`, `"destination_account_identity":""`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifier := newTestVerifier(t, now, time.Minute)
			body := []byte(test.body)
			_, err := verifier.VerifyAndNormalize(context.Background(), signedHeaders(body, now), body)
			if !errors.Is(err, app.ErrInvalidInput) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(fmt.Sprint(err), testSigningSecret) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestSignedJSONNormalizesOutboundDirection(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	body := []byte(strings.Replace(string(validBody(now)), `"direction":"inbound"`, `"direction":"outbound"`, 1))
	event, err := newTestVerifier(t, now, time.Minute).VerifyAndNormalize(context.Background(), signedHeaders(body, now), body)
	if err != nil || event.Direction != "outbound" {
		t.Fatalf("event = %+v, err=%v", event, err)
	}
}

func TestSignedJSONTimestampBoundaryAndValidation(t *testing.T) {
	now := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	verifier := newTestVerifier(t, now, time.Minute)
	body := validBody(now.Add(-time.Minute))
	if _, err := verifier.VerifyAndNormalize(context.Background(), signedHeaders(body, now.Add(-time.Minute)), body); err != nil {
		t.Fatalf("boundary error = %v", err)
	}

	invalid := strings.Replace(string(validBody(now)), "TS-ABC123", " TS-ABC123 ", 1)
	invalidBody := []byte(invalid)
	if _, err := verifier.VerifyAndNormalize(context.Background(), signedHeaders(invalidBody, now), invalidBody); !errors.Is(err, app.ErrInvalidInput) {
		t.Fatalf("whitespace reference error = %v", err)
	}
}

func newTestVerifier(t *testing.T, now time.Time, tolerance time.Duration) *SignedJSON {
	t.Helper()
	verifier, err := NewSignedJSON(testSigningSecret, tolerance)
	if err != nil {
		t.Fatal(err)
	}
	verifier.clock = func() time.Time { return now }
	return verifier
}

func validBody(timestamp time.Time) []byte {
	return []byte(fmt.Sprintf(`{"event_id":"event-1","transaction_id":"transaction-1","event_type":"payment.received","reference":"TS-ABC123","direction":"inbound","transfer_content":"TS-ABC123","destination_account_identity":"reference-account-1","amount_vnd":125000,"currency":"VND","timestamp":%d,"occurred_at":"%s","received_account":"123456789","metadata":{"channel":"private-test"}}`, timestamp.Unix(), timestamp.Format(time.RFC3339)))
}

func signedHeaders(body []byte, timestamp time.Time) http.Header {
	mac := hmac.New(sha256.New, []byte(testSigningSecret))
	_, _ = mac.Write(body)
	return http.Header{
		"X-Payment-Signature": []string{hex.EncodeToString(mac.Sum(nil))},
		"X-Payment-Timestamp": []string{fmt.Sprint(timestamp.Unix())},
	}
}
