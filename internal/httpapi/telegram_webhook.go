package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-telegram/bot/models"
)

const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

type TelegramUpdateProcessor interface {
	Process(context.Context, *models.Update, string) error
}

type TelegramWebhookMetrics interface {
	ObserveWebhook(result string)
}

type TelegramWebhook struct {
	secret    string
	bodyLimit int64
	timeout   time.Duration
	processor TelegramUpdateProcessor
	metrics   TelegramWebhookMetrics
}

func NewTelegramWebhook(
	secret string,
	bodyLimit int64,
	timeout time.Duration,
	processor TelegramUpdateProcessor,
	metrics TelegramWebhookMetrics,
) *TelegramWebhook {
	return &TelegramWebhook{
		secret: secret, bodyLimit: bodyLimit, timeout: timeout,
		processor: processor, metrics: metrics,
	}
}

func (w *TelegramWebhook) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !validSecret(c.GetHeader(telegramSecretHeader), w.secret) {
			w.observe("unauthorized")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			w.observe("unsupported_media_type")
			c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{"error": "application/json required"})
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, w.bodyLimit)
		decoder := json.NewDecoder(c.Request.Body)
		var update models.Update
		if err := decoder.Decode(&update); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				w.observe("too_large")
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			w.observe("malformed")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "malformed JSON"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			w.observe("malformed")
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "malformed JSON"})
			return
		}

		requestContext, cancel := context.WithTimeout(c.Request.Context(), w.timeout)
		defer cancel()
		if err := w.processor.Process(requestContext, &update, requestIDFromContext(c.Request.Context())); err != nil {
			w.observe("processing_failed")
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "update processing failed"})
			return
		}
		w.observe("accepted")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func (w *TelegramWebhook) observe(result string) {
	if w.metrics != nil {
		w.metrics.ObserveWebhook(result)
	}
}

func validSecret(provided, expected string) bool {
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
