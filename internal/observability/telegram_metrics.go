package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type TelegramMetrics struct {
	webhookRequests *prometheus.CounterVec
	updates         *prometheus.CounterVec
	updateDuration  *prometheus.HistogramVec
	duplicates      prometheus.Counter
	apiRequests     *prometheus.CounterVec
	apiDuration     *prometheus.HistogramVec
	catalogQueries  *prometheus.CounterVec
	adminMutations  *prometheus.CounterVec
	adminSessions   *prometheus.CounterVec
}

func NewTelegramMetrics(registerer prometheus.Registerer) *TelegramMetrics {
	metrics := &TelegramMetrics{
		webhookRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "telegram_webhook_requests_total",
			Help: "Telegram webhook requests by bounded result.",
		}, []string{"result"}),
		updates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "telegram_updates_total",
			Help: "Telegram updates by type and bounded result.",
		}, []string{"type", "result"}),
		updateDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Name: "telegram_update_duration_seconds",
			Help: "Telegram update processing duration.", Buckets: prometheus.DefBuckets,
		}, []string{"type", "result"}),
		duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "telegram_duplicate_updates_total",
			Help: "Durably deduplicated Telegram updates.",
		}),
		apiRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "telegram_api_requests_total",
			Help: "Telegram Bot API calls by method and result.",
		}, []string{"method", "result"}),
		apiDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Name: "telegram_api_duration_seconds",
			Help: "Telegram Bot API call duration.", Buckets: prometheus.DefBuckets,
		}, []string{"method", "result"}),
		catalogQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "catalog_queries_total",
			Help: "Customer catalog queries by operation and result.",
		}, []string{"operation", "result"}),
		adminMutations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "admin_catalog_mutations_total",
			Help: "Admin catalog mutations by operation and result.",
		}, []string{"operation", "result"}),
		adminSessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "admin_session_operations_total",
			Help: "Admin session operations by operation and result.",
		}, []string{"operation", "result"}),
	}
	registerer.MustRegister(
		metrics.webhookRequests, metrics.updates, metrics.updateDuration,
		metrics.duplicates, metrics.apiRequests, metrics.apiDuration,
		metrics.catalogQueries, metrics.adminMutations, metrics.adminSessions,
	)
	return metrics
}

func (m *TelegramMetrics) ObserveWebhook(result string) {
	m.webhookRequests.WithLabelValues(result).Inc()
}

func (m *TelegramMetrics) ObserveUpdate(updateType, result string, duration time.Duration) {
	m.updates.WithLabelValues(updateType, result).Inc()
	m.updateDuration.WithLabelValues(updateType, result).Observe(duration.Seconds())
}

func (m *TelegramMetrics) ObserveDuplicate() {
	m.duplicates.Inc()
}

func (m *TelegramMetrics) ObserveTelegramAPI(method, result string, duration time.Duration) {
	m.apiRequests.WithLabelValues(method, result).Inc()
	m.apiDuration.WithLabelValues(method, result).Observe(duration.Seconds())
}

func (m *TelegramMetrics) ObserveCatalog(operation, result string) {
	m.catalogQueries.WithLabelValues(operation, result).Inc()
}

func (m *TelegramMetrics) ObserveAdminMutation(operation, result string) {
	m.adminMutations.WithLabelValues(operation, result).Inc()
}

func (m *TelegramMetrics) ObserveAdminSession(operation, result string) {
	m.adminSessions.WithLabelValues(operation, result).Inc()
}
