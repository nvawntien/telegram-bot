package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nvawntien/telegram-bot/internal/config"
	"github.com/nvawntien/telegram-bot/internal/observability"
	"github.com/nvawntien/telegram-bot/internal/postgres"
	"github.com/nvawntien/telegram-bot/internal/worker"
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

	runner := worker.NewRunner(postgres.NewChecker(pool, cfg.DatabaseHealthTimeout), logger, 30*time.Second)
	return runner.Run(ctx)
}
