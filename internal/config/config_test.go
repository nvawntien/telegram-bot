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

	cfg, err := LoadAPI()
	if err != nil {
		t.Fatalf("LoadAPI() error = %v", err)
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
	if cfg.TelegramWebhookBodyLimit != 1<<20 || cfg.AdminSessionTTL != 15*time.Minute {
		t.Fatalf("Phase 3 defaults = body:%d session:%s", cfg.TelegramWebhookBodyLimit, cfg.AdminSessionTTL)
	}
	if cfg.InventoryEncryptionKeyVersion != 1 || cfg.InventoryImportMaxItems != 100 ||
		cfg.InventoryImportMaxItemBytes != 4096 || cfg.InventoryImportMaxTotalBytes != 256*1024 {
		t.Fatalf("Phase 4 defaults are invalid: %#v", cfg)
	}
	if cfg.OrderMaxQuantity != 10 || cfg.PaymentReferencePrefix != "TS" || cfg.PaymentReferenceRandomBytes != 6 || cfg.BankAccountEncryptionVersion != 1 {
		t.Fatalf("Phase 5 defaults are invalid: %#v", cfg)
	}
}

func TestLoadReportsAllInvalidValues(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("ADMIN_TELEGRAM_IDS", "not-an-id")
	t.Setenv("INVENTORY_ENCRYPTION_KEY", "not-base64")
	t.Setenv("DELIVERY_MAX_ATTEMPTS", "0")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "short")

	_, err := LoadAPI()
	if err == nil {
		t.Fatal("LoadAPI() error = nil, want validation error")
	}
	for _, expected := range []string{
		"ADMIN_TELEGRAM_IDS",
		"INVENTORY_ENCRYPTION_KEY",
		"DELIVERY_MAX_ATTEMPTS",
		"TELEGRAM_WEBHOOK_SECRET",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("LoadAPI() error %q does not contain %q", err, expected)
		}
	}
}

func TestProductionWebhookRequiresHTTPS(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("TELEGRAM_WEBHOOK_URL", "http://example.test/webhooks/telegram")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("LoadAPI() error = %v, want HTTPS validation error", err)
	}
}

func TestLoadRejectsConnectionCountOverflow(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_MAX_CONNECTIONS", "2147483648")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_MAX_CONNECTIONS") {
		t.Fatalf("LoadAPI() error = %v, want connection overflow validation error", err)
	}
}

func TestLoadWorkerDoesNotRequireWebhookConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")
	t.Setenv("TELEGRAM_WEBHOOK_URL", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("ADMIN_TELEGRAM_IDS", "")
	t.Setenv("INVENTORY_ENCRYPTION_KEY", "")

	if _, err := LoadWorker(); err != nil {
		t.Fatalf("LoadWorker() error = %v", err)
	}
}

func TestLoadAPIValidatesInventoryConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("INVENTORY_ENCRYPTION_KEY_VERSION", "0")
	t.Setenv("INVENTORY_IMPORT_MAX_ITEM_BYTES", "200")
	t.Setenv("INVENTORY_IMPORT_MAX_TOTAL_BYTES", "100")

	_, err := LoadAPI()
	if err == nil {
		t.Fatal("LoadAPI() error = nil, want inventory validation error")
	}
	for _, expected := range []string{
		"INVENTORY_ENCRYPTION_KEY_VERSION",
		"INVENTORY_IMPORT_MAX_ITEM_BYTES",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("LoadAPI() error %q does not contain %q", err, expected)
		}
	}
}

func TestLoadAPIRequiresWebhookConfiguration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("TELEGRAM_WEBHOOK_URL", "")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_WEBHOOK_URL") {
		t.Fatalf("LoadAPI() error = %v, want webhook URL validation error", err)
	}
}

func TestLoadAPIValidatesTelegramRuntimeLimits(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("TELEGRAM_WEBHOOK_BODY_LIMIT_BYTES", "100")
	t.Setenv("ADMIN_SESSION_TTL_MINUTES", "0")
	t.Setenv("SUPPORT_CONTACT", "")

	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_WEBHOOK_BODY_LIMIT_BYTES") || !strings.Contains(err.Error(), "ADMIN_SESSION_TTL_MINUTES") {
		t.Fatalf("LoadAPI() error = %v", err)
	}
}

func TestLoadAPIValidatesPhase5Configuration(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("PAYMENT_REFERENCE_PREFIX", "bad-prefix")
	t.Setenv("PAYMENT_REFERENCE_RANDOM_BYTES", "2")
	t.Setenv("VIETQR_TEMPLATE", "../escape")
	_, err := LoadAPI()
	if err == nil || !strings.Contains(err.Error(), "payment reference") || !strings.Contains(err.Error(), "VietQR") {
		t.Fatalf("LoadAPI() error = %v", err)
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
	t.Setenv("BANK_ACCOUNT_ENCRYPTION_KEY", validEncryptionKey)
}
