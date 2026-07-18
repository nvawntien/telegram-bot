// Package worker owns background process lifecycle. Domain workers are added by
// phase behind this runner so every goroutine shares cancellation semantics.
package worker

import (
	"context"
	"log/slog"
	"time"
)

// DependencyChecker verifies a worker dependency is still reachable.
type DependencyChecker interface {
	Check(context.Context) error
}

// Runner provides cancellation and dependency monitoring for future job loops.
type Runner struct {
	checker  DependencyChecker
	logger   *slog.Logger
	interval time.Duration
}

// NewRunner creates the worker process foundation.
func NewRunner(checker DependencyChecker, logger *slog.Logger, interval time.Duration) *Runner {
	return &Runner{checker: checker, logger: logger, interval: interval}
}

// Run blocks until cancellation while continuously checking PostgreSQL. Failed
// checks are logged and retried; startup already fails fast if PostgreSQL is down.
func (r *Runner) Run(ctx context.Context) error {
	r.logger.Info("worker process started")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("worker process stopped cleanly")
			return nil
		case <-ticker.C:
			if err := r.checker.Check(ctx); err != nil {
				r.logger.Error("worker dependency check failed", "dependency", "postgres", "error", err)
			}
		}
	}
}
