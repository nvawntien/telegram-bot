package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/nvawntien/telegram-bot/internal/config"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.LoadMigration()
	if err != nil {
		return err
	}
	command := "up"
	if len(args) > 0 {
		command = args[0]
	}
	if len(args) > 1 {
		return errors.New("usage: migrate [up|up-by-one|down|status|version]")
	}

	database, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer database.Close()

	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping migration database: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}

	switch command {
	case "up":
		err = goose.UpContext(ctx, database, cfg.MigrationsDir)
	case "up-by-one":
		err = goose.UpByOneContext(ctx, database, cfg.MigrationsDir)
	case "down":
		err = goose.DownContext(ctx, database, cfg.MigrationsDir)
	case "status":
		err = goose.StatusContext(ctx, database, cfg.MigrationsDir)
	case "version":
		_, err = goose.GetDBVersionContext(ctx, database)
	default:
		return fmt.Errorf("unknown migration command %q", command)
	}
	if err != nil {
		return fmt.Errorf("goose %s: %w", command, err)
	}
	slog.Info("migration command completed", "command", command)
	return nil
}
