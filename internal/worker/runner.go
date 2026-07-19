// Package worker owns background process lifecycle. Domain workers are added by
// phase behind this runner so every goroutine shares cancellation semantics.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/nvawntien/telegram-bot/internal/payment"
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

type DeliveryJob interface {
	RunOnce(context.Context) (int, error)
}

type PaymentReconciliationJob interface {
	RunOnce(context.Context) (payment.ReconciliationSummary, error)
}

// Runner provides cancellation and dependency monitoring for future job loops.
type Runner struct {
	checker                DependencyChecker
	logger                 *slog.Logger
	healthInterval         time.Duration
	expiry                 OrderExpiryJob
	expiryInterval         time.Duration
	runTimeout             time.Duration
	metrics                ExpiryMetrics
	payment                PaymentEventJob
	paymentInterval        time.Duration
	paymentTimeout         time.Duration
	paymentMetrics         PaymentMetrics
	delivery               DeliveryJob
	deliveryInterval       time.Duration
	deliveryTimeout        time.Duration
	reconciliation         PaymentReconciliationJob
	reconciliationInterval time.Duration
	reconciliationTimeout  time.Duration
}

func (r *Runner) WithPaymentReconciliation(job PaymentReconciliationJob, interval, timeout time.Duration) *Runner {
	r.reconciliation = job
	r.reconciliationInterval = interval
	r.reconciliationTimeout = timeout
	return r
}

func (r *Runner) WithPaymentEvents(job PaymentEventJob, interval, timeout time.Duration, metrics PaymentMetrics) *Runner {
	r.payment = job
	r.paymentInterval = interval
	r.paymentTimeout = timeout
	r.paymentMetrics = metrics
	return r
}

func (r *Runner) WithDelivery(job DeliveryJob, interval, timeout time.Duration) *Runner {
	r.delivery = job
	r.deliveryInterval = interval
	r.deliveryTimeout = timeout
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
	var deliveryTicker *time.Ticker
	var deliveryChannel <-chan time.Time
	if r.delivery != nil {
		deliveryTicker = time.NewTicker(r.deliveryInterval)
		deliveryChannel = deliveryTicker.C
		defer deliveryTicker.Stop()
	}
	var reconciliationTicker *time.Ticker
	var reconciliationChannel <-chan time.Time
	if r.reconciliation != nil {
		reconciliationTicker = time.NewTicker(r.reconciliationInterval)
		reconciliationChannel = reconciliationTicker.C
		defer reconciliationTicker.Stop()
	}
	defer healthTicker.Stop()
	defer expiryTicker.Stop()
	r.runExpiry(ctx)
	r.runPaymentEvents(ctx)
	r.runDelivery(ctx)
	r.runReconciliation(ctx)

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
		case <-deliveryChannel:
			r.runDelivery(ctx)
		case <-reconciliationChannel:
			r.runReconciliation(ctx)
		}
	}
}

func (r *Runner) runReconciliation(ctx context.Context) {
	if r.reconciliation == nil {
		return
	}
	started := time.Now()
	defer func() {
		if recover() != nil {
			r.logger.ErrorContext(ctx, "payment reconciliation run panicked", "worker", "payment_reconciliation", "result", "panic")
		}
	}()
	runCtx, cancel := context.WithTimeout(ctx, r.reconciliationTimeout)
	defer cancel()
	summary, err := r.reconciliation.RunOnce(runCtx)
	if err != nil {
		r.logger.ErrorContext(ctx, "payment reconciliation run degraded",
			"worker", "payment_reconciliation", "operation", "reconcile_provider_transactions",
			"result", "failed", "accounts", summary.Accounts, "pages", summary.Pages,
			"transactions_fetched", summary.Fetched, "events_ingested", summary.Ingested,
			"duplicates", summary.Duplicates, "duration_ms", time.Since(started).Milliseconds(), "error", err,
		)
		return
	}
	r.logger.InfoContext(ctx, "payment reconciliation run completed",
		"worker", "payment_reconciliation", "operation", "reconcile_provider_transactions",
		"result", "success", "accounts", summary.Accounts, "pages", summary.Pages,
		"transactions_fetched", summary.Fetched, "events_ingested", summary.Ingested,
		"duplicates", summary.Duplicates, "skipped", summary.Skipped,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func (r *Runner) runDelivery(ctx context.Context) {
	if r.delivery == nil {
		return
	}
	started := time.Now()
	defer func() {
		if recover() != nil {
			r.logger.ErrorContext(ctx, "delivery run panicked", "worker", "delivery", "result", "panic")
		}
	}()
	runCtx, cancel := context.WithTimeout(ctx, r.deliveryTimeout)
	defer cancel()
	count, err := r.delivery.RunOnce(runCtx)
	if err != nil {
		r.logger.ErrorContext(ctx, "delivery run failed",
			"worker", "delivery", "operation", "process_delivery_jobs",
			"result", "failed", "duration_ms", time.Since(started).Milliseconds(), "error", err,
		)
		return
	}
	r.logger.InfoContext(ctx, "delivery run completed",
		"worker", "delivery", "operation", "process_delivery_jobs",
		"result", "success", "batch_size", count, "duration_ms", time.Since(started).Milliseconds(),
	)
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
