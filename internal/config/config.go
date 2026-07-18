// Package config loads and validates process configuration from environment variables.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr       = ":8080"
	defaultShutdownWindow = 10 * time.Second
	defaultOrderExpiry    = 15 * time.Minute
	defaultRetryBase      = 5 * time.Second
)

// Config is immutable after startup and is passed explicitly to process dependencies.
type Config struct {
	AppEnv                 string
	HTTPAddr               string
	DatabaseURL            string
	TelegramBotToken       string
	TelegramWebhookSecret  string
	TelegramWebhookURL     string
	AdminTelegramIDs       []int64
	InventoryEncryptionKey []byte
	OrderExpiry            time.Duration
	DeliveryMaxAttempts    int
	DeliveryRetryBase      time.Duration
	LogLevel               slog.Level
	PrometheusEnabled      bool
	ShutdownTimeout        time.Duration
	DatabaseMaxConnections int32
	DatabaseMinConnections int32
	DatabaseConnectionTTL  time.Duration
	DatabaseHealthTimeout  time.Duration
}

// MigrationConfig contains only values required by the migration process.
type MigrationConfig struct {
	DatabaseURL   string
	MigrationsDir string
}

type processKind uint8

const (
	processAPI processKind = iota
	processWorker
)

// LoadAPI reads and validates API configuration, including webhook settings.
func LoadAPI() (Config, error) {
	return load(processAPI)
}

// LoadWorker reads and validates worker configuration without requiring an HTTP
// address or Telegram webhook URL/secret that the worker never consumes.
func LoadWorker() (Config, error) {
	return load(processWorker)
}

func load(process processKind) (Config, error) {
	cfg := Config{
		AppEnv:                 envOrDefault("APP_ENV", "local"),
		HTTPAddr:               envOrDefault("HTTP_ADDR", defaultHTTPAddr),
		DatabaseURL:            strings.TrimSpace(os.Getenv("DATABASE_URL")),
		TelegramBotToken:       strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramWebhookSecret:  strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET")),
		TelegramWebhookURL:     strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_URL")),
		OrderExpiry:            defaultOrderExpiry,
		DeliveryMaxAttempts:    5,
		DeliveryRetryBase:      defaultRetryBase,
		LogLevel:               slog.LevelInfo,
		PrometheusEnabled:      true,
		ShutdownTimeout:        defaultShutdownWindow,
		DatabaseMaxConnections: 20,
		DatabaseMinConnections: 2,
		DatabaseConnectionTTL:  30 * time.Minute,
		DatabaseHealthTimeout:  2 * time.Second,
	}

	var problems []error
	assign(&problems, "ADMIN_TELEGRAM_IDS", parseAdminIDs(os.Getenv("ADMIN_TELEGRAM_IDS")), &cfg.AdminTelegramIDs)
	assign(&problems, "INVENTORY_ENCRYPTION_KEY", parseEncryptionKey(os.Getenv("INVENTORY_ENCRYPTION_KEY")), &cfg.InventoryEncryptionKey)
	assign(&problems, "ORDER_EXPIRE_MINUTES", parsePositiveDuration(os.Getenv("ORDER_EXPIRE_MINUTES"), time.Minute, cfg.OrderExpiry), &cfg.OrderExpiry)
	assign(&problems, "DELIVERY_MAX_ATTEMPTS", parsePositiveInt(os.Getenv("DELIVERY_MAX_ATTEMPTS"), cfg.DeliveryMaxAttempts), &cfg.DeliveryMaxAttempts)
	assign(&problems, "DELIVERY_RETRY_BASE_SECONDS", parsePositiveDuration(os.Getenv("DELIVERY_RETRY_BASE_SECONDS"), time.Second, cfg.DeliveryRetryBase), &cfg.DeliveryRetryBase)
	assign(&problems, "LOG_LEVEL", parseLogLevel(os.Getenv("LOG_LEVEL")), &cfg.LogLevel)
	assign(&problems, "PROMETHEUS_ENABLED", parseBool(os.Getenv("PROMETHEUS_ENABLED"), cfg.PrometheusEnabled), &cfg.PrometheusEnabled)
	assign(&problems, "SHUTDOWN_TIMEOUT_SECONDS", parsePositiveDuration(os.Getenv("SHUTDOWN_TIMEOUT_SECONDS"), time.Second, cfg.ShutdownTimeout), &cfg.ShutdownTimeout)
	assign(&problems, "DATABASE_MAX_CONNECTIONS", parsePositiveInt32(os.Getenv("DATABASE_MAX_CONNECTIONS"), cfg.DatabaseMaxConnections), &cfg.DatabaseMaxConnections)
	assign(&problems, "DATABASE_MIN_CONNECTIONS", parseNonNegativeInt32(os.Getenv("DATABASE_MIN_CONNECTIONS"), cfg.DatabaseMinConnections), &cfg.DatabaseMinConnections)
	assign(&problems, "DATABASE_CONNECTION_TTL_MINUTES", parsePositiveDuration(os.Getenv("DATABASE_CONNECTION_TTL_MINUTES"), time.Minute, cfg.DatabaseConnectionTTL), &cfg.DatabaseConnectionTTL)
	assign(&problems, "DATABASE_HEALTH_TIMEOUT_SECONDS", parsePositiveDuration(os.Getenv("DATABASE_HEALTH_TIMEOUT_SECONDS"), time.Second, cfg.DatabaseHealthTimeout), &cfg.DatabaseHealthTimeout)

	problems = append(problems, validate(cfg, process)...)
	if err := errors.Join(problems...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// LoadMigration reads values needed to run goose without requiring API secrets.
func LoadMigration() (MigrationConfig, error) {
	cfg := MigrationConfig{
		DatabaseURL:   strings.TrimSpace(os.Getenv("DATABASE_URL")),
		MigrationsDir: envOrDefault("MIGRATIONS_DIR", "migrations"),
	}
	if cfg.DatabaseURL == "" {
		return MigrationConfig{}, errors.New("DATABASE_URL is required")
	}
	if strings.TrimSpace(cfg.MigrationsDir) == "" {
		return MigrationConfig{}, errors.New("MIGRATIONS_DIR is required")
	}
	return cfg, nil
}

type parseResult[T any] struct {
	value T
	err   error
}

func assign[T any](problems *[]error, name string, result parseResult[T], target *T) {
	if result.err != nil {
		*problems = append(*problems, fmt.Errorf("%s: %w", name, result.err))
		return
	}
	*target = result.value
}

func validate(cfg Config, process processKind) []error {
	var problems []error
	required := map[string]string{
		"DATABASE_URL":       cfg.DatabaseURL,
		"TELEGRAM_BOT_TOKEN": cfg.TelegramBotToken,
	}
	if process == processAPI {
		required["TELEGRAM_WEBHOOK_SECRET"] = cfg.TelegramWebhookSecret
		required["TELEGRAM_WEBHOOK_URL"] = cfg.TelegramWebhookURL
	}
	for name, value := range required {
		if value == "" {
			problems = append(problems, fmt.Errorf("%s is required", name))
		}
	}

	if process == processAPI && cfg.HTTPAddr == "" {
		problems = append(problems, errors.New("HTTP_ADDR is required"))
	}
	if process == processAPI && len(cfg.TelegramWebhookSecret) > 0 && len(cfg.TelegramWebhookSecret) < 16 {
		problems = append(problems, errors.New("TELEGRAM_WEBHOOK_SECRET must contain at least 16 characters"))
	}
	if process == processAPI && cfg.TelegramWebhookURL != "" {
		webhookURL, err := url.ParseRequestURI(cfg.TelegramWebhookURL)
		if err != nil || webhookURL.Host == "" {
			problems = append(problems, errors.New("TELEGRAM_WEBHOOK_URL must be an absolute URL"))
		} else if cfg.AppEnv == "production" && webhookURL.Scheme != "https" {
			problems = append(problems, errors.New("TELEGRAM_WEBHOOK_URL must use HTTPS in production"))
		}
	}
	if len(cfg.AdminTelegramIDs) == 0 {
		problems = append(problems, errors.New("ADMIN_TELEGRAM_IDS must contain at least one positive ID"))
	}
	if len(cfg.InventoryEncryptionKey) != 32 {
		problems = append(problems, errors.New("INVENTORY_ENCRYPTION_KEY must decode to exactly 32 bytes"))
	}
	if cfg.DatabaseMinConnections > cfg.DatabaseMaxConnections {
		problems = append(problems, errors.New("DATABASE_MIN_CONNECTIONS cannot exceed DATABASE_MAX_CONNECTIONS"))
	}
	return problems
}

func parseAdminIDs(raw string) parseResult[[]int64] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[[]int64]{value: nil}
	}
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, item := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
		if err != nil || id <= 0 {
			return parseResult[[]int64]{err: fmt.Errorf("%q is not a positive Telegram ID", item)}
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return parseResult[[]int64]{value: ids}
}

func parseEncryptionKey(raw string) parseResult[[]byte] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[[]byte]{value: nil}
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return parseResult[[]byte]{err: errors.New("must be standard base64")}
	}
	return parseResult[[]byte]{value: key}
}

func parsePositiveInt(raw string, fallback int) parseResult[int] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[int]{value: fallback}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return parseResult[int]{err: errors.New("must be a positive integer")}
	}
	return parseResult[int]{value: value}
}

func parsePositiveInt32(raw string, fallback int32) parseResult[int32] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[int32]{value: fallback}
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return parseResult[int32]{err: errors.New("must be a positive 32-bit integer")}
	}
	return parseResult[int32]{value: int32(value)}
}

func parseNonNegativeInt32(raw string, fallback int32) parseResult[int32] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[int32]{value: fallback}
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return parseResult[int32]{err: errors.New("must be a non-negative integer")}
	}
	return parseResult[int32]{value: int32(value)}
}

func parsePositiveDuration(raw string, unit time.Duration, fallback time.Duration) parseResult[time.Duration] {
	result := parsePositiveInt(raw, int(fallback/unit))
	if result.err != nil {
		return parseResult[time.Duration]{err: result.err}
	}
	return parseResult[time.Duration]{value: time.Duration(result.value) * unit}
}

func parseLogLevel(raw string) parseResult[slog.Level] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[slog.Level]{value: slog.LevelInfo}
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(raw)))); err != nil {
		return parseResult[slog.Level]{err: errors.New("must be DEBUG, INFO, WARN, or ERROR")}
	}
	return parseResult[slog.Level]{value: level}
}

func parseBool(raw string, fallback bool) parseResult[bool] {
	if strings.TrimSpace(raw) == "" {
		return parseResult[bool]{value: fallback}
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return parseResult[bool]{err: errors.New("must be true or false")}
	}
	return parseResult[bool]{value: value}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
