package observability

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics contains the foundation metrics owned by the HTTP transport.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewHTTPMetrics registers metrics in the supplied registry.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	metrics := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "telegram_shop",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
	}
	reg.MustRegister(metrics.requests, metrics.duration)
	return metrics
}

// Observe records one completed request. Route must be a bounded router
// template such as /health/ready, never an untrusted raw URL path.
func (m *HTTPMetrics) Observe(method, route string, status int, elapsed time.Duration) {
	statusLabel := strconv.Itoa(status)
	m.requests.WithLabelValues(method, route, statusLabel).Inc()
	m.duration.WithLabelValues(method, route, statusLabel).Observe(elapsed.Seconds())
}
