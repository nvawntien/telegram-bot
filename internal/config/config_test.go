package config

import (
	"strings"
	"testing"
	"time"
)

const validEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestLoadValidConfig(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("ADMIN_TELEGRAM_IDS", "123, 456,123")
	t.Setenv("ORDER_EXPIRE_MINUTES", "20")
	t.Setenv("PROMETHEUS_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.AdminTelegramIDs) != 2 {
		t.Fatalf("AdminTelegramIDs length = %d, want 2", len(cfg.AdminTelegramIDs))
	}
	if cfg.OrderExpiry != 20*time.Minute {
		t.Fatalf("OrderExpiry = %s, want 20m", cfg.OrderExpiry)
	}
	if cfg.PrometheusEnabled {
		t.Fatal("PrometheusEnabled = true, want false")
	}
}

func TestLoadReportsAllInvalidValues(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("ADMIN_TELEGRAM_IDS", "not-an-id")
	t.Setenv("INVENTORY_ENCRYPTION_KEY", "not-base64")
	t.Setenv("DELIVERY_MAX_ATTEMPTS", "0")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "short")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
	for _, expected := range []string{
		"ADMIN_TELEGRAM_IDS",
		"INVENTORY_ENCRYPTION_KEY",
		"DELIVERY_MAX_ATTEMPTS",
		"TELEGRAM_WEBHOOK_SECRET",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Load() error %q does not contain %q", err, expected)
		}
	}
}

func TestProductionWebhookRequiresHTTPS(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("TELEGRAM_WEBHOOK_URL", "http://example.test/webhooks/telegram")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("Load() error = %v, want HTTPS validation error", err)
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "local")
	t.Setenv("DATABASE_URL", "postgres://shop:shop@localhost:5432/shop?sslmode=disable")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "0123456789abcdef")
	t.Setenv("TELEGRAM_WEBHOOK_URL", "http://localhost:8080/webhooks/telegram")
	t.Setenv("ADMIN_TELEGRAM_IDS", "123")
	t.Setenv("INVENTORY_ENCRYPTION_KEY", validEncryptionKey)
}
