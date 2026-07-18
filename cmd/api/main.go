package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nvawntien/telegram-bot/internal/config"
	"github.com/nvawntien/telegram-bot/internal/httpapi"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/nvawntien/telegram-bot/internal/postgres"
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
	server := httpapi.NewServer(
		httpapi.ServerConfig{
			Address:           cfg.HTTPAddr,
			Environment:       cfg.AppEnv,
			PrometheusEnabled: cfg.PrometheusEnabled,
		},
		postgres.NewChecker(pool, cfg.DatabaseHealthTimeout),
		metrics,
		prometheus.DefaultGatherer,
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
