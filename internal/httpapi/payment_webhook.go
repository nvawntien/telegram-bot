package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/payment"
)

type PaymentEventIngester interface {
	Ingest(context.Context, app.NormalizedPaymentEvent) (app.PaymentEventIngestionResult, error)
}

type PaymentWebhookMetrics interface {
	ObservePaymentWebhook(provider, result string)
	ObservePaymentEventIngested(provider, result string)
}

type PaymentWebhook struct {
	providers *payment.Registry
	ingester  PaymentEventIngester
	bodyLimit int64
	timeout   time.Duration
	metrics   PaymentWebhookMetrics
}

func NewPaymentWebhook(providers *payment.Registry, ingester PaymentEventIngester, bodyLimit int64, timeout time.Duration, metrics PaymentWebhookMetrics) *PaymentWebhook {
	return &PaymentWebhook{providers: providers, ingester: ingester, bodyLimit: bodyLimit, timeout: timeout, metrics: metrics}
}

func (w *PaymentWebhook) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		providerName := c.Param("provider")
		provider, providerErr := w.providers.GetWebhookProvider(providerName)
		if providerErr != nil {
			w.observeWebhook(providerName, "unknown_provider")
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "payment provider not found"})
			return
		}
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			w.observeWebhook(providerName, "unsupported_media_type")
			c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{"error": "application/json required"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, w.bodyLimit)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				w.observeWebhook(providerName, "too_large")
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			w.observeWebhook(providerName, "read_failed")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		requestContext, cancel := context.WithTimeout(c.Request.Context(), w.timeout)
		defer cancel()
		event, acknowledgement, err := provider.VerifyAndNormalizeWebhook(requestContext, payment.WebhookRequest{
			Headers: c.Request.Header.Clone(), RawBody: body, ReceivedAt: time.Now(),
		})
		if err != nil {
			result := "malformed"
			status := http.StatusBadRequest
			if errors.Is(err, app.ErrInvalidWebhookSignature) || errors.Is(err, app.ErrWebhookReplay) {
				result = "unauthorized"
				status = http.StatusUnauthorized
			}
			w.observeSignature(providerName, result)
			w.observeWebhook(providerName, result)
			c.AbortWithStatusJSON(status, gin.H{"error": "payment webhook rejected"})
			return
		}
		w.observeSignature(providerName, "verified")
		result, err := w.ingester.Ingest(requestContext, event)
		if err != nil {
			if errors.Is(err, app.ErrPaymentEventConflict) {
				w.observeWebhook(providerName, "conflict")
				w.observeIngested(providerName, "conflict")
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "payment event conflict"})
				return
			}
			w.observeWebhook(providerName, "ingestion_failed")
			w.observeIngested(providerName, "failed")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "payment event ingestion unavailable"})
			return
		}
		ingestionResult := "accepted"
		if result.Duplicate {
			ingestionResult = "duplicate"
		}
		w.observeWebhook(providerName, ingestionResult)
		w.observeIngested(providerName, ingestionResult)
		w.observeProviderEvent(providerName, event.Source, ingestionResult)
		if err := validateProviderAcknowledgement(acknowledgement); err != nil {
			w.observeWebhook(providerName, "invalid_acknowledgement")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "payment provider acknowledgement unavailable"})
			return
		}
		for name, value := range acknowledgement.Headers {
			c.Header(name, value)
		}
		contentType := acknowledgement.Headers["Content-Type"]
		responseBody := acknowledgement.Body
		if result.Duplicate && len(acknowledgement.DuplicateBody) > 0 {
			responseBody = acknowledgement.DuplicateBody
		}
		c.Data(acknowledgement.StatusCode, contentType, responseBody)
	}
}

func (w *PaymentWebhook) observeSignature(provider, result string) {
	if metrics, ok := w.metrics.(interface {
		ObserveProviderSignatureVerification(string, string)
	}); ok {
		metrics.ObserveProviderSignatureVerification(provider, result)
	}
}

func (w *PaymentWebhook) observeProviderEvent(provider, source, result string) {
	if metrics, ok := w.metrics.(interface {
		ObserveProviderEvent(string, string, string)
	}); ok {
		metrics.ObserveProviderEvent(provider, source, result)
	}
}

func validateProviderAcknowledgement(ack payment.WebhookAcknowledgement) error {
	if ack.StatusCode < 200 || ack.StatusCode > 299 || len(ack.Body) > 4096 || len(ack.DuplicateBody) > 4096 || len(ack.Headers) == 0 {
		return app.ErrInvalidInput
	}
	contentType := ""
	for name, value := range ack.Headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || strings.ContainsAny(name+value, "\r\n") || len(value) > 512 || canonical == "Authorization" || canonical == "Set-Cookie" {
			return app.ErrInvalidInput
		}
		if canonical == "Content-Type" {
			contentType = value
		}
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && mediaType != "text/plain") {
		return app.ErrInvalidInput
	}
	return nil
}

func (w *PaymentWebhook) observeWebhook(provider, result string) {
	if w.metrics != nil {
		w.metrics.ObservePaymentWebhook(provider, result)
	}
}

func (w *PaymentWebhook) observeIngested(provider, result string) {
	if w.metrics != nil {
		w.metrics.ObservePaymentEventIngested(provider, result)
	}
}
