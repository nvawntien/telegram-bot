package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nvawntien/telegram-bot/internal/app"
	"github.com/nvawntien/telegram-bot/internal/config"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
	"github.com/nvawntien/telegram-bot/internal/worker"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("worker process stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.AppEnv, cfg.LogLevel, os.Stdout).With("process", "worker")
	slog.SetDefault(logger)

	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		URL:               cfg.DatabaseURL,
		MaxConnections:    cfg.DatabaseMaxConnections,
		MinConnections:    cfg.DatabaseMinConnections,
		MaxConnectionLife: cfg.DatabaseConnectionTTL,
		HealthTimeout:     cfg.DatabaseHealthTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	store := postgres.NewAppStore(pool)
	expiryService := app.NewOrderExpiryService(store, cfg.OrderExpiryBatchSize)
	expiryMetrics := observability.NewOrderExpiryMetrics(prometheus.DefaultRegisterer)
	paymentMetrics := observability.NewPaymentMetrics(prometheus.DefaultRegisterer)
	paymentAcceptance := app.NewPaymentAcceptanceService(store, app.DefaultPostPaymentReservationTTL, paymentMetrics).
		WithDeliveryMaxAttempts(cfg.DeliveryMaxAttempts)
	paymentEvents := app.NewPaymentEventJob(
		store, paymentAcceptance, cfg.PaymentEventBatchSize,
		cfg.PaymentEventRetryBase, cfg.PaymentStaleProcessingTimeout,
	)
	deliveryMetrics := observability.NewDeliveryMetrics(prometheus.DefaultRegisterer)
	inventoryCipher, err := inventorycrypto.New(cfg.InventoryEncryptionKey, cfg.InventoryEncryptionKeyVersion, nil)
	if err != nil {
		return fmt.Errorf("initialize delivery inventory decryption: %w", err)
	}
	telegramClient, err := telegramadapter.NewClient(cfg.TelegramBotToken, "", cfg.TelegramAPITimeout, 1<<20, nil)
	if err != nil {
		return fmt.Errorf("initialize delivery Telegram client: %w", err)
	}
	hostname, _ := os.Hostname()
	deliveryJob := app.NewDeliveryJob(
		store, inventoryCipher, telegramClient, deliveryMetrics,
		app.DeliveryRetryPolicy{
			Base: cfg.DeliveryRetryBase, Max: cfg.DeliveryRetryMax,
			JitterRatio: cfg.DeliveryRetryJitter, MaxAttempts: cfg.DeliveryMaxAttempts,
		},
		cfg.DeliveryBatchSize, cfg.DeliveryProcessingLease, cfg.DeliveryJobTimeout,
		cfg.DeliveryMessageMaxBytes, cfg.SupportContact,
		fmt.Sprintf("%s-%d", hostname, os.Getpid()),
	).WithStaleScanInterval(cfg.DeliveryStaleScanInterval)
	runner := worker.NewRunner(
		postgres.NewChecker(pool, cfg.DatabaseHealthTimeout), expiryService, logger,
		30*time.Second, cfg.OrderExpiryInterval, cfg.OrderExpiryRunTimeout, expiryMetrics,
	).WithPaymentEvents(paymentEvents, cfg.PaymentEventPollInterval, cfg.PaymentEventRunTimeout, paymentMetrics).
		WithDelivery(deliveryJob, cfg.DeliveryPollInterval, cfg.DeliveryRunTimeout)
	return runner.Run(ctx)
}
