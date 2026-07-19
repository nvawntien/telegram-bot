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
	"github.com/nvawntien/telegram-bot/internal/bankcrypto"
	"github.com/nvawntien/telegram-bot/internal/config"
	"github.com/nvawntien/telegram-bot/internal/domain"
	"github.com/nvawntien/telegram-bot/internal/httpapi"
	"github.com/nvawntien/telegram-bot/internal/inventorycrypto"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/nvawntien/telegram-bot/internal/payment"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	telegramadapter "github.com/nvawntien/telegram-bot/internal/telegram"
	"github.com/nvawntien/telegram-bot/internal/vietqr"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("API process stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(cfg.AppEnv, cfg.LogLevel, os.Stdout).With("process", "api")
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

	metrics := observability.NewHTTPMetrics(prometheus.DefaultRegisterer)
	telegramMetrics := observability.NewTelegramMetrics(prometheus.DefaultRegisterer)
	paymentMetrics := observability.NewPaymentMetrics(prometheus.DefaultRegisterer)
	walletMetrics := observability.NewWalletMetrics(prometheus.DefaultRegisterer)
	deliveryMetrics := observability.NewDeliveryMetrics(prometheus.DefaultRegisterer)
	store := postgres.NewAppStore(pool)
	userService := app.NewUserService(store)
	catalogService := app.NewCatalogService(store, app.DefaultPageSize)
	adminService := app.NewAdminService(store, cfg.AdminSessionTTL)
	if err := adminService.Bootstrap(ctx, cfg.AdminTelegramIDs); err != nil {
		return fmt.Errorf("bootstrap administrators: %w", err)
	}
	inventoryCipher, err := inventorycrypto.New(
		cfg.InventoryEncryptionKey, cfg.InventoryEncryptionKeyVersion, telegramMetrics,
	)
	if err != nil {
		return fmt.Errorf("initialize inventory encryption: %w", err)
	}
	inventoryService := app.NewInventoryAdminService(
		store, adminService, inventoryCipher,
		app.InventoryImportLimits{
			MaxItems: cfg.InventoryImportMaxItems, MaxItemBytes: cfg.InventoryImportMaxItemBytes,
			MaxTotalBytes: cfg.InventoryImportMaxTotalBytes,
		},
		app.DefaultPageSize, telegramMetrics,
	)
	bankCipher, err := bankcrypto.New(cfg.BankAccountEncryptionKey, cfg.BankAccountEncryptionVersion)
	if err != nil {
		return fmt.Errorf("initialize bank account encryption: %w", err)
	}
	bankService := app.NewBankAccountService(store, bankCipher, adminService, cfg.BankAccountPageSize).
		WithPaymentEnvironment(cfg.PaymentProviderEnvironment)
	vietQRGenerator, err := vietqr.New(cfg.VietQRBaseURL, cfg.VietQRTemplate)
	if err != nil {
		return fmt.Errorf("initialize VietQR instructions: %w", err)
	}
	referenceGenerator, err := app.NewPaymentReferenceGenerator(cfg.PaymentReferencePrefix, cfg.PaymentReferenceRandomBytes)
	if err != nil {
		return fmt.Errorf("initialize payment references: %w", err)
	}
	orderService := app.NewOrderService(
		store, bankCipher, vietQRGenerator, referenceGenerator, cfg.OrderExpiry,
		cfg.OrderMaxQuantity, cfg.OrderPageSize,
	).WithPaymentEnvironment(cfg.PaymentProviderEnvironment)
	walletService := app.NewWalletService(
		store, bankCipher, vietQRGenerator, referenceGenerator,
		domain.Money(cfg.WalletTopupMinAmount), domain.Money(cfg.WalletTopupMaxAmount),
		cfg.WalletTopupExpiry, app.DefaultPostPaymentReservationTTL, walletMetrics,
	).WithDeliveryMaxAttempts(cfg.DeliveryMaxAttempts).WithPaymentEnvironment(cfg.PaymentProviderEnvironment)
	paymentAdminService := app.NewPaymentAdminService(store, cfg.PaymentReviewPageSize, app.DefaultPostPaymentReservationTTL).
		WithDeliveryMaxAttempts(cfg.DeliveryMaxAttempts)
	deliveryAdminService := app.NewDeliveryAdminService(store, cfg.DeliveryReviewPageSize).
		WithProcessingLease(cfg.DeliveryProcessingLease).WithMetrics(deliveryMetrics)
	updateService := app.NewUpdateService(store, cfg.TelegramUpdateStaleAfter)
	telegramClient, err := telegramadapter.NewClient(
		cfg.TelegramBotToken, "", cfg.TelegramAPITimeout, 1<<20, telegramMetrics,
	)
	if err != nil {
		return err
	}
	telegramRouter := telegramadapter.NewRouterWithOrdering(
		userService, catalogService, adminService, inventoryService, bankService, orderService,
		walletService,
		paymentAdminService,
		updateService, telegramClient,
		cfg.SupportContact, logger, telegramMetrics,
	).WithDeliveryAdministration(deliveryAdminService)
	telegramWebhook := httpapi.NewTelegramWebhook(
		cfg.TelegramWebhookSecret, cfg.TelegramWebhookBodyLimit,
		cfg.TelegramWebhookTimeout, telegramRouter, telegramMetrics,
	)
	providerAdapters := make([]payment.Provider, 0, len(cfg.PaymentProviders))
	for _, providerName := range cfg.PaymentProviders {
		if providerName == payment.SignedJSONProvider {
			adapter, err := payment.NewSignedJSONForEnvironment(
				cfg.SignedJSONWebhookSecret, cfg.SignedJSONTimestampTolerance,
				payment.ProviderEnvironment(cfg.PaymentProviderEnvironment), true,
			)
			if err != nil {
				return fmt.Errorf("initialize signed payment webhook: %w", err)
			}
			providerAdapters = append(providerAdapters, adapter)
		}
	}
	providerRegistry, err := payment.NewProviderRegistry(providerAdapters...)
	if err != nil {
		return fmt.Errorf("initialize payment provider registry: %w", err)
	}
	providerDescriptors := make([]app.PaymentProviderDescriptor, 0, len(providerRegistry.Providers()))
	for _, providerAdapter := range providerRegistry.Providers() {
		capabilities := providerAdapter.Capabilities()
		providerDescriptors = append(providerDescriptors, app.PaymentProviderDescriptor{
			Name: string(providerAdapter.Name()), Enabled: providerAdapter.Enabled(),
			Environment: string(providerAdapter.Environment()),
			Capabilities: app.PaymentProviderCapabilities{
				Webhook: capabilities.SupportsWebhook, Reconciliation: capabilities.SupportsReconciliation,
				TestMode: capabilities.SupportsTestMode,
			},
		})
	}
	providerAdminService := app.NewPaymentProviderAdminService(
		store, adminService, providerDescriptors, cfg.PaymentProviderReviewPageSize, paymentMetrics,
	)
	telegramRouter.WithPaymentProviderAdministration(providerAdminService)
	paymentWebhook := httpapi.NewPaymentWebhook(
		providerRegistry,
		app.NewPaymentEventIngestionService(store, cfg.PaymentEventMaxAttempts),
		cfg.PaymentWebhookBodyLimit, cfg.PaymentEventRunTimeout, paymentMetrics,
	)
	server := httpapi.NewServer(
		httpapi.ServerConfig{
			Address:           cfg.HTTPAddr,
			Environment:       cfg.AppEnv,
			PrometheusEnabled: cfg.PrometheusEnabled,
		},
		postgres.NewChecker(pool, cfg.DatabaseHealthTimeout),
		metrics,
		prometheus.DefaultGatherer,
		telegramWebhook,
		paymentWebhook,
		logger,
	)

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Run()
	}()

	select {
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}

	select {
	case err := <-serverErrors:
		if err != nil {
			return fmt.Errorf("stop HTTP server: %w", err)
		}
	case <-time.After(cfg.ShutdownTimeout):
		return fmt.Errorf("HTTP server did not stop within %s", cfg.ShutdownTimeout)
	}
	logger.Info("API process stopped cleanly")
	return nil
}
