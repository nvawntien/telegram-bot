package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type PaymentMetrics struct {
	webhooks                   *prometheus.CounterVec
	ingested                   *prometheus.CounterVec
	processed                  *prometheus.CounterVec
	acceptance                 *prometheus.CounterVec
	reviews                    *prometheus.CounterVec
	duration                   prometheus.Histogram
	claims                     *prometheus.CounterVec
	providerWebhooks           *prometheus.CounterVec
	providerEvents             *prometheus.CounterVec
	providerSignatures         *prometheus.CounterVec
	reconciliationRuns         *prometheus.CounterVec
	reconciliationTransactions *prometheus.CounterVec
	reconciliationDuration     *prometheus.HistogramVec
	providerAPIRequests        *prometheus.CounterVec
	providerAPIDuration        *prometheus.HistogramVec
	providerAccounts           *prometheus.CounterVec
	providerReviews            *prometheus.CounterVec
	checkpointAge              *prometheus.GaugeVec
	reconciliationLag          *prometheus.GaugeVec
}

func NewPaymentMetrics(registerer prometheus.Registerer) *PaymentMetrics {
	metrics := &PaymentMetrics{
		webhooks:                   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_webhook_requests_total", Help: "Payment webhook requests by result."}, []string{"provider", "result"}),
		ingested:                   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_events_ingested_total", Help: "Durably ingested payment events."}, []string{"provider", "result"}),
		processed:                  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_events_processed_total", Help: "Processed payment events."}, []string{"provider", "result"}),
		acceptance:                 prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_acceptance_total", Help: "Payment acceptance decisions."}, []string{"target", "result"}),
		reviews:                    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_review_cases_total", Help: "Payment review cases."}, []string{"reason"}),
		duration:                   prometheus.NewHistogram(prometheus.HistogramOpts{Name: "payment_processing_duration_seconds", Help: "Payment processing duration.", Buckets: prometheus.DefBuckets}),
		claims:                     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "inventory_post_payment_claims_total", Help: "Inventory claims after payment."}, []string{"result"}),
		providerWebhooks:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_webhook_requests_total", Help: "Provider webhook requests by result."}, []string{"provider", "result"}),
		providerEvents:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_events_total", Help: "Provider payment events by source and result."}, []string{"provider", "source", "result"}),
		providerSignatures:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_signature_verifications_total", Help: "Provider webhook signature verification decisions."}, []string{"provider", "result"}),
		reconciliationRuns:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_reconciliation_runs_total", Help: "Provider reconciliation runs."}, []string{"provider", "result"}),
		reconciliationTransactions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_reconciliation_transactions_total", Help: "Provider reconciliation transactions."}, []string{"provider", "result"}),
		reconciliationDuration:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "payment_provider_reconciliation_duration_seconds", Help: "Provider reconciliation run duration.", Buckets: prometheus.DefBuckets}, []string{"provider"}),
		providerAPIRequests:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_api_requests_total", Help: "Provider API requests."}, []string{"provider", "operation", "result"}),
		providerAPIDuration:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "payment_provider_api_duration_seconds", Help: "Provider API request duration.", Buckets: prometheus.DefBuckets}, []string{"provider", "operation"}),
		providerAccounts:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_account_operations_total", Help: "Provider account mapping operations."}, []string{"provider", "operation", "result"}),
		providerReviews:            prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_provider_review_cases_total", Help: "Provider review cases."}, []string{"provider", "reason"}),
		checkpointAge:              prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "payment_provider_checkpoint_age_seconds", Help: "Age of the last successful provider checkpoint."}, []string{"provider"}),
		reconciliationLag:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "payment_provider_reconciliation_lag_seconds", Help: "Lag from the most recent reconciled transaction."}, []string{"provider"}),
	}
	registerer.MustRegister(
		metrics.webhooks, metrics.ingested, metrics.processed, metrics.acceptance, metrics.reviews, metrics.duration, metrics.claims,
		metrics.providerWebhooks, metrics.providerEvents, metrics.providerSignatures,
		metrics.reconciliationRuns, metrics.reconciliationTransactions, metrics.reconciliationDuration,
		metrics.providerAPIRequests, metrics.providerAPIDuration, metrics.providerAccounts,
		metrics.providerReviews, metrics.checkpointAge, metrics.reconciliationLag,
	)
	return metrics
}

func (m *PaymentMetrics) ObservePaymentWebhook(provider, result string) {
	m.webhooks.WithLabelValues(provider, result).Inc()
	m.providerWebhooks.WithLabelValues(provider, result).Inc()
}
func (m *PaymentMetrics) ObserveProviderEvent(provider, source, result string) {
	m.providerEvents.WithLabelValues(provider, source, result).Inc()
}
func (m *PaymentMetrics) ObserveProviderSignatureVerification(provider, result string) {
	m.providerSignatures.WithLabelValues(provider, result).Inc()
}
func (m *PaymentMetrics) ObserveReconciliationRun(provider, result string, duration time.Duration) {
	m.reconciliationRuns.WithLabelValues(provider, result).Inc()
	m.reconciliationDuration.WithLabelValues(provider).Observe(duration.Seconds())
}
func (m *PaymentMetrics) ObserveReconciliationTransaction(provider, result string) {
	m.reconciliationTransactions.WithLabelValues(provider, result).Inc()
	m.providerEvents.WithLabelValues(provider, "reconciliation", result).Inc()
}
func (m *PaymentMetrics) ObserveProviderAPIRequest(provider, operation, result string, duration time.Duration) {
	m.providerAPIRequests.WithLabelValues(provider, operation, result).Inc()
	m.providerAPIDuration.WithLabelValues(provider, operation).Observe(duration.Seconds())
}
func (m *PaymentMetrics) ObserveProviderAccountOperation(provider, operation, result string) {
	m.providerAccounts.WithLabelValues(provider, operation, result).Inc()
}
func (m *PaymentMetrics) ObserveProviderReview(provider, reason string) {
	m.providerReviews.WithLabelValues(provider, reason).Inc()
}
func (m *PaymentMetrics) SetProviderCheckpointAge(provider string, age time.Duration) {
	m.checkpointAge.WithLabelValues(provider).Set(max(age.Seconds(), 0))
}
func (m *PaymentMetrics) SetProviderReconciliationLag(provider string, lag time.Duration) {
	m.reconciliationLag.WithLabelValues(provider).Set(max(lag.Seconds(), 0))
}
func (m *PaymentMetrics) ObservePaymentEventIngested(provider, result string) {
	m.ingested.WithLabelValues(provider, result).Inc()
}
func (m *PaymentMetrics) ObservePaymentEventProcessed(provider, result string, duration time.Duration) {
	m.processed.WithLabelValues(provider, result).Inc()
	m.duration.Observe(duration.Seconds())
}
func (m *PaymentMetrics) ObservePaymentAcceptance(target, result string) {
	m.acceptance.WithLabelValues(target, result).Inc()
}
func (m *PaymentMetrics) ObservePaymentReview(reason string) { m.reviews.WithLabelValues(reason).Inc() }
func (m *PaymentMetrics) ObservePostPaymentClaim(result string) {
	m.claims.WithLabelValues(result).Inc()
}
