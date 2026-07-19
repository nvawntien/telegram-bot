package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type OrderExpiryMetrics struct {
	runs      *prometheus.CounterVec
	expired   *prometheus.CounterVec
	batchSize prometheus.Histogram
	duration  prometheus.Histogram
}

func NewOrderExpiryMetrics(registerer prometheus.Registerer) *OrderExpiryMetrics {
	metrics := &OrderExpiryMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "order_expiry_runs_total",
			Help: "Order expiry runs by bounded result.",
		}, []string{"result"}),
		expired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "orders_expired_total",
			Help: "Orders expired by bounded result.",
		}, []string{"result"}),
		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Name: "order_expiry_batch_size",
			Help: "Orders transitioned per expiry run.", Buckets: prometheus.ExponentialBuckets(1, 2, 9),
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Name: "order_expiry_duration_seconds",
			Help: "Order expiry run duration.", Buckets: prometheus.DefBuckets,
		}),
	}
	registerer.MustRegister(metrics.runs, metrics.expired, metrics.batchSize, metrics.duration)
	return metrics
}

func (m *OrderExpiryMetrics) ObserveExpiryRun(result string, count int, duration time.Duration) {
	m.runs.WithLabelValues(result).Inc()
	m.expired.WithLabelValues(result).Add(float64(count))
	m.batchSize.Observe(float64(count))
	m.duration.Observe(duration.Seconds())
}
