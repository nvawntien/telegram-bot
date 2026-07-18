// Package postgres owns PostgreSQL connection lifecycle and health checks.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvawntien/telegram-bot/internal/postgres/generated"
)

// PoolConfig contains explicit connection-pool limits.
type PoolConfig struct {
	URL               string
	MaxConnections    int32
	MinConnections    int32
	MaxConnectionLife time.Duration
	HealthTimeout     time.Duration
}

// Open validates the connection string, opens the pool, and proves the database
// is reachable before returning.
func Open(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	poolConfig.MaxConns = cfg.MaxConnections
	poolConfig.MinConns = cfg.MinConnections
	poolConfig.MaxConnLifetime = cfg.MaxConnectionLife
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = 30 * time.Second
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "telegram-shop-bot"

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}

	healthCtx, cancel := context.WithTimeout(ctx, cfg.HealthTimeout)
	defer cancel()
	if err := pool.Ping(healthCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Checker performs a bounded database readiness check.
type Checker struct {
	queries *generated.Queries
	timeout time.Duration
}

// NewChecker creates a database checker.
func NewChecker(pool *pgxpool.Pool, timeout time.Duration) *Checker {
	return &Checker{queries: generated.New(pool), timeout: timeout}
}

// Check returns nil only when PostgreSQL executes a typed health query within
// the configured timeout.
func (c *Checker) Check(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.queries.DatabaseHealth(checkCtx)
	return err
}
