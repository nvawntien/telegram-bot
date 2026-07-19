package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nvawntien/telegram-bot/internal/domain"
)

func TestDeliveryRetryDelayHonorsBackoffJitterCapAndRetryAfter(t *testing.T) {
	t.Parallel()
	policy := DeliveryRetryPolicy{Base: 2 * time.Second, Max: 10 * time.Second, JitterRatio: 0.25, MaxAttempts: 5}
	if got := DeliveryRetryDelay(policy, 1, 0, func() float64 { return 0.5 }); got != 2*time.Second {
		t.Fatalf("first delay = %s", got)
	}
	if got := DeliveryRetryDelay(policy, 8, 0, func() float64 { return 0.5 }); got != 10*time.Second {
		t.Fatalf("capped delay = %s", got)
	}
	low := DeliveryRetryDelay(policy, 2, 0, func() float64 { return 0 })
	high := DeliveryRetryDelay(policy, 2, 0, func() float64 { return 1 })
	if low != 3*time.Second || high != 5*time.Second {
		t.Fatalf("jitter bounds = %s..%s", low, high)
	}
	if got := DeliveryRetryDelay(policy, 1, 30*time.Second, func() float64 { return 0.5 }); got != 30*time.Second {
		t.Fatalf("retry-after delay = %s", got)
	}
}

func TestClassifyDeliveryFailureIsConservativeAndExhaustsAttempts(t *testing.T) {
	t.Parallel()
	policy := DeliveryRetryPolicy{Base: time.Second, Max: time.Minute, MaxAttempts: 2}
	now := time.Now()
	retryable := classifyDeliveryFailure(&DeliverySendError{
		Class: domain.DeliveryResultRetryable, Code: "rate_limited", RetryAfter: 7 * time.Second,
	}, 1, policy, now, func() float64 { return 0.5 })
	if retryable.Class != domain.DeliveryResultRetryable || retryable.NextAttemptAt.Sub(now) != 7*time.Second {
		t.Fatalf("retryable classification = %#v", retryable)
	}
	exhausted := classifyDeliveryFailure(&DeliverySendError{Class: domain.DeliveryResultRetryable, Code: "server_error"}, 2, policy, now, nil)
	if exhausted.Class != domain.DeliveryResultPermanent || exhausted.Code != "attempts_exhausted" {
		t.Fatalf("exhausted classification = %#v", exhausted)
	}
	unknown := classifyDeliveryFailure(errors.New("opaque failure"), 1, policy, now, nil)
	if unknown.Class != domain.DeliveryResultAmbiguous {
		t.Fatalf("unknown error class = %s", unknown.Class)
	}
}

func TestBuildDeliveryMessageEscapesOpaquePayloadAndNeverTruncates(t *testing.T) {
	t.Parallel()
	envelope := DeliveryEnvelope{OrderID: 41, ProductName: "Plan <A>", Quantity: 1}
	message, err := BuildDeliveryMessage(envelope, [][]byte{[]byte("opaque<&value")}, "support & help", 4096)
	if err != nil {
		t.Fatalf("BuildDeliveryMessage() error = %v", err)
	}
	for _, escaped := range []string{"Plan &lt;A&gt;", "opaque&lt;&amp;value", "support &amp; help"} {
		if !strings.Contains(message, escaped) {
			t.Fatalf("message does not contain escaped value %q", escaped)
		}
	}
	if _, err := BuildDeliveryMessage(envelope, [][]byte{[]byte("opaque<&value")}, "", 10); !errors.Is(err, ErrDeliveryMessageTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	if _, err := BuildDeliveryMessage(envelope, nil, "", 4096); !errors.Is(err, ErrDeliveryInventoryMismatch) {
		t.Fatalf("quantity mismatch error = %v", err)
	}
}
