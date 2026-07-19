package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type PaymentMetrics struct {
	webhooks   *prometheus.CounterVec
	ingested   *prometheus.CounterVec
	processed  *prometheus.CounterVec
	acceptance *prometheus.CounterVec
	reviews    *prometheus.CounterVec
	duration   prometheus.Histogram
	claims     *prometheus.CounterVec
}

func NewPaymentMetrics(registerer prometheus.Registerer) *PaymentMetrics {
	metrics := &PaymentMetrics{
		webhooks:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_webhook_requests_total", Help: "Payment webhook requests by result."}, []string{"provider", "result"}),
		ingested:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_events_ingested_total", Help: "Durably ingested payment events."}, []string{"provider", "result"}),
		processed:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_events_processed_total", Help: "Processed payment events."}, []string{"provider", "result"}),
		acceptance: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_acceptance_total", Help: "Payment acceptance decisions."}, []string{"target", "result"}),
		reviews:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "payment_review_cases_total", Help: "Payment review cases."}, []string{"reason"}),
		duration:   prometheus.NewHistogram(prometheus.HistogramOpts{Name: "payment_processing_duration_seconds", Help: "Payment processing duration.", Buckets: prometheus.DefBuckets}),
		claims:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "inventory_post_payment_claims_total", Help: "Inventory claims after payment."}, []string{"result"}),
	}
	registerer.MustRegister(metrics.webhooks, metrics.ingested, metrics.processed, metrics.acceptance, metrics.reviews, metrics.duration, metrics.claims)
	return metrics
}

func (m *PaymentMetrics) ObservePaymentWebhook(provider, result string) {
	m.webhooks.WithLabelValues(provider, result).Inc()
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
