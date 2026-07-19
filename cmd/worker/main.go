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
	"github.com/nvawntien/telegram-bot/internal/payment"
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
	referenceExtractor, err := app.NewPaymentReferenceExtractor(
		cfg.PaymentReferencePrefix, cfg.PaymentReferenceRandomBytes, app.DefaultPaymentTransferContentLimit,
	)
	if err != nil {
		return fmt.Errorf("initialize payment reference extractor: %w", err)
	}
	paymentEvents.WithReferenceExtractor(referenceExtractor)
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
	if cfg.PaymentReconciliationEnabled {
		providerRegistry, registryErr := payment.NewProviderRegistry()
		if registryErr != nil {
			return fmt.Errorf("initialize reconciliation provider registry: %w", registryErr)
		}
		reconciliation, reconciliationErr := payment.NewReconciliationJob(
			providerRegistry, store,
			app.NewPaymentEventIngestionService(store, cfg.PaymentEventMaxAttempts),
			fmt.Sprintf("%s-%d", hostname, os.Getpid()),
			cfg.PaymentReconciliationMaxPages, cfg.PaymentReconciliationPageSize,
			cfg.PaymentReconciliationRequestTimeout,
			cfg.PaymentReconciliationRunTimeout+cfg.PaymentReconciliationRequestTimeout,
			paymentMetrics,
		)
		if reconciliationErr != nil {
			return fmt.Errorf("initialize provider reconciliation: %w", reconciliationErr)
		}
		runner.WithPaymentReconciliation(reconciliation, cfg.PaymentReconciliationInterval, cfg.PaymentReconciliationRunTimeout)
	}
	return runner.Run(ctx)
}
