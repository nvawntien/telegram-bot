package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type DeliveryMetrics struct {
	jobsCreated       *prometheus.CounterVec
	jobsClaimed       *prometheus.CounterVec
	attempts          *prometheus.CounterVec
	completed         *prometheus.CounterVec
	retries           *prometheus.CounterVec
	ambiguous         *prometheus.CounterVec
	permanentFailures *prometheus.CounterVec
	manualResolutions *prometheus.CounterVec
	duration          prometheus.Histogram
	reconciliation    *prometheus.CounterVec
	queueDepth        *prometheus.GaugeVec
}

func NewDeliveryMetrics(reg prometheus.Registerer) *DeliveryMetrics {
	metrics := &DeliveryMetrics{
		jobsCreated:       newDeliveryCounter("jobs_created_total", "Delivery jobs created.", []string{"result"}),
		jobsClaimed:       newDeliveryCounter("jobs_claimed_total", "Delivery jobs claimed.", []string{"result"}),
		attempts:          newDeliveryCounter("attempts_total", "Telegram delivery attempts.", []string{"result_class"}),
		completed:         newDeliveryCounter("completed_total", "Completed deliveries.", []string{"result"}),
		retries:           newDeliveryCounter("retry_scheduled_total", "Scheduled delivery retries.", []string{"reason"}),
		ambiguous:         newDeliveryCounter("ambiguous_total", "Ambiguous delivery outcomes.", []string{"reason"}),
		permanentFailures: newDeliveryCounter("permanent_failed_total", "Permanent delivery failures.", []string{"reason"}),
		manualResolutions: newDeliveryCounter("manual_resolutions_total", "Manual delivery resolutions.", []string{"resolution", "result"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Subsystem: "delivery", Name: "duration_seconds",
			Help: "Telegram delivery attempt duration.", Buckets: prometheus.DefBuckets,
		}),
		reconciliation: newDeliveryCounter("reconciliation_anomalies_total", "Delivery reconciliation anomalies.", []string{"type"}),
		queueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "telegram_shop", Subsystem: "delivery", Name: "queue_depth",
			Help: "Current delivery queue depth by bounded status.",
		}, []string{"status"}),
	}
	reg.MustRegister(
		metrics.jobsCreated, metrics.jobsClaimed, metrics.attempts, metrics.completed,
		metrics.retries, metrics.ambiguous, metrics.permanentFailures,
		metrics.manualResolutions, metrics.duration, metrics.reconciliation, metrics.queueDepth,
	)
	return metrics
}

func newDeliveryCounter(name, help string, labels []string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "telegram_shop", Subsystem: "delivery", Name: name, Help: help,
	}, labels)
}

func (m *DeliveryMetrics) ObserveDeliveryJobCreated(result string) {
	m.jobsCreated.WithLabelValues(result).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryJobClaimed(result string, count int) {
	m.jobsClaimed.WithLabelValues(result).Add(float64(count))
}

func (m *DeliveryMetrics) ObserveDeliveryAttempt(resultClass string, duration time.Duration) {
	m.attempts.WithLabelValues(resultClass).Inc()
	m.duration.Observe(duration.Seconds())
}

func (m *DeliveryMetrics) ObserveDeliveryCompleted(result string) {
	m.completed.WithLabelValues(result).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryRetry(reason string) {
	m.retries.WithLabelValues(reason).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryAmbiguous(reason string) {
	m.ambiguous.WithLabelValues(reason).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryPermanentFailure(reason string) {
	m.permanentFailures.WithLabelValues(reason).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryManualResolution(resolution, result string) {
	m.manualResolutions.WithLabelValues(resolution, result).Inc()
}

func (m *DeliveryMetrics) ObserveDeliveryReconciliation(anomaly string, count int64) {
	m.reconciliation.WithLabelValues(anomaly).Add(float64(count))
}

func (m *DeliveryMetrics) SetDeliveryQueueDepth(status string, count int64) {
	m.queueDepth.WithLabelValues(status).Set(float64(count))
}
