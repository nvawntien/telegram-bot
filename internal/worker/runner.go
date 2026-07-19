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

type OrderExpiryJob interface {
	RunOnce(context.Context) (int, error)
}

type ExpiryMetrics interface {
	ObserveExpiryRun(result string, count int, duration time.Duration)
}

type PaymentEventJob interface {
	RunOnce(context.Context) (int, error)
}

type PaymentMetrics interface {
	ObservePaymentEventProcessed(provider, result string, duration time.Duration)
}

// Runner provides cancellation and dependency monitoring for future job loops.
type Runner struct {
	checker         DependencyChecker
	logger          *slog.Logger
	healthInterval  time.Duration
	expiry          OrderExpiryJob
	expiryInterval  time.Duration
	runTimeout      time.Duration
	metrics         ExpiryMetrics
	payment         PaymentEventJob
	paymentInterval time.Duration
	paymentTimeout  time.Duration
	paymentMetrics  PaymentMetrics
}

func (r *Runner) WithPaymentEvents(job PaymentEventJob, interval, timeout time.Duration, metrics PaymentMetrics) *Runner {
	r.payment = job
	r.paymentInterval = interval
	r.paymentTimeout = timeout
	r.paymentMetrics = metrics
	return r
}

// NewRunner creates the worker process foundation.
func NewRunner(
	checker DependencyChecker,
	expiry OrderExpiryJob,
	logger *slog.Logger,
	healthInterval time.Duration,
	expiryInterval time.Duration,
	runTimeout time.Duration,
	metrics ExpiryMetrics,
) *Runner {
	return &Runner{
		checker: checker, expiry: expiry, logger: logger,
		healthInterval: healthInterval, expiryInterval: expiryInterval,
		runTimeout: runTimeout, metrics: metrics,
	}
}

// Run blocks until cancellation while continuously checking PostgreSQL. Failed
// checks are logged and retried; startup already fails fast if PostgreSQL is down.
func (r *Runner) Run(ctx context.Context) error {
	r.logger.Info("worker process started")
	healthTicker := time.NewTicker(r.healthInterval)
	expiryTicker := time.NewTicker(r.expiryInterval)
	var paymentTicker *time.Ticker
	var paymentChannel <-chan time.Time
	if r.payment != nil {
		paymentTicker = time.NewTicker(r.paymentInterval)
		paymentChannel = paymentTicker.C
		defer paymentTicker.Stop()
	}
	defer healthTicker.Stop()
	defer expiryTicker.Stop()
	r.runExpiry(ctx)
	r.runPaymentEvents(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("worker process stopped cleanly")
			return nil
		case <-healthTicker.C:
			if err := r.checker.Check(ctx); err != nil {
				r.logger.Error("worker dependency check failed", "dependency", "postgres", "error", err)
			}
		case <-expiryTicker.C:
			r.runExpiry(ctx)
		case <-paymentChannel:
			r.runPaymentEvents(ctx)
		}
	}
}

func (r *Runner) runPaymentEvents(ctx context.Context) {
	if r.payment == nil {
		return
	}
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, r.paymentTimeout)
	defer cancel()
	count, err := r.payment.RunOnce(runCtx)
	result := "success"
	if err != nil {
		result = "failed"
		r.logger.ErrorContext(ctx, "payment event run failed", "worker", "payment_event", "operation", "process_events", "result", result, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	} else {
		r.logger.InfoContext(ctx, "payment event run completed", "worker", "payment_event", "operation", "process_events", "result", result, "batch_size", count, "duration_ms", time.Since(started).Milliseconds())
	}
	if r.paymentMetrics != nil {
		r.paymentMetrics.ObservePaymentEventProcessed("all", result, time.Since(started))
	}
}

func (r *Runner) runExpiry(ctx context.Context) {
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, r.runTimeout)
	defer cancel()
	count, err := r.expiry.RunOnce(runCtx)
	result := "success"
	if err != nil {
		result = "failed"
		r.logger.ErrorContext(ctx, "order expiry run failed",
			"worker", "order_expiry", "operation", "expire_orders",
			"result", result, "duration_ms", time.Since(started).Milliseconds(), "error", err,
		)
	} else {
		r.logger.InfoContext(ctx, "order expiry run completed",
			"worker", "order_expiry", "operation", "expire_orders",
			"result", result, "batch_size", count, "duration_ms", time.Since(started).Milliseconds(),
		)
	}
	if r.metrics != nil {
		r.metrics.ObserveExpiryRun(result, count, time.Since(started))
	}
}
