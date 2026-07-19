package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type TelegramMetrics struct {
	webhookRequests        *prometheus.CounterVec
	updates                *prometheus.CounterVec
	updateDuration         *prometheus.HistogramVec
	duplicates             prometheus.Counter
	apiRequests            *prometheus.CounterVec
	apiDuration            *prometheus.HistogramVec
	catalogQueries         *prometheus.CounterVec
	adminMutations         *prometheus.CounterVec
	adminSessions          *prometheus.CounterVec
	inventoryImports       *prometheus.CounterVec
	inventoryImportedItems prometheus.Counter
	inventoryDuplicates    prometheus.Counter
	inventoryClaims        *prometheus.CounterVec
	inventoryClaimedItems  prometheus.Counter
	inventoryReleases      *prometheus.CounterVec
	inventoryReleasedItems prometheus.Counter
	inventoryEncryption    *prometheus.CounterVec
	inventoryRecovery      *prometheus.CounterVec
	ordersCreated          *prometheus.CounterVec
	ordersCancelled        *prometheus.CounterVec
	orderCreationDuration  prometheus.Histogram
	orderHistoryQueries    *prometheus.CounterVec
	paymentInstructions    *prometheus.CounterVec
	bankAccountMutations   *prometheus.CounterVec
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
		inventoryImports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_import_requests_total",
			Help: "Inventory import requests by bounded result.",
		}, []string{"result"}),
		inventoryImportedItems: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_items_imported_total",
			Help: "Encrypted inventory items imported.",
		}),
		inventoryDuplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_duplicates_total",
			Help: "Inventory import duplicates skipped.",
		}),
		inventoryClaims: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_claim_requests_total",
			Help: "Inventory claim requests by bounded result.",
		}, []string{"result"}),
		inventoryClaimedItems: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_items_claimed_total",
			Help: "Inventory items claimed atomically.",
		}),
		inventoryReleases: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_release_requests_total",
			Help: "Inventory release requests by bounded result.",
		}, []string{"result"}),
		inventoryReleasedItems: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_items_released_total",
			Help: "Inventory items released safely.",
		}),
		inventoryEncryption: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_encryption_operations_total",
			Help: "Inventory cryptographic operations by bounded operation and result.",
		}, []string{"operation", "result"}),
		inventoryRecovery: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "inventory_reservation_recovery_total",
			Help: "Inventory reservation recovery decisions by bounded result.",
		}, []string{"result"}),
		ordersCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "orders_created_total",
			Help: "Order creation attempts by bounded result.",
		}, []string{"result"}),
		ordersCancelled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "orders_cancelled_total",
			Help: "Customer cancellation attempts by bounded result.",
		}, []string{"result"}),
		orderCreationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "telegram_shop", Name: "order_creation_duration_seconds",
			Help: "Order creation duration.", Buckets: prometheus.DefBuckets,
		}),
		orderHistoryQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "order_history_queries_total",
			Help: "Ownership-scoped order history queries by bounded result.",
		}, []string{"result"}),
		paymentInstructions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "payment_instructions_generated_total",
			Help: "Payment instruction generation attempts by bounded result.",
		}, []string{"result"}),
		bankAccountMutations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "telegram_shop", Name: "bank_account_mutations_total",
			Help: "Bank account mutations by operation and bounded result.",
		}, []string{"operation", "result"}),
	}
	registerer.MustRegister(
		metrics.webhookRequests, metrics.updates, metrics.updateDuration,
		metrics.duplicates, metrics.apiRequests, metrics.apiDuration,
		metrics.catalogQueries, metrics.adminMutations, metrics.adminSessions,
		metrics.inventoryImports, metrics.inventoryImportedItems, metrics.inventoryDuplicates,
		metrics.inventoryClaims, metrics.inventoryClaimedItems, metrics.inventoryReleases,
		metrics.inventoryReleasedItems, metrics.inventoryEncryption, metrics.inventoryRecovery,
		metrics.ordersCreated, metrics.ordersCancelled, metrics.orderCreationDuration,
		metrics.orderHistoryQueries, metrics.paymentInstructions, metrics.bankAccountMutations,
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

func (m *TelegramMetrics) ObserveInventoryImport(result string, inserted, duplicates int) {
	m.inventoryImports.WithLabelValues(result).Inc()
	m.inventoryImportedItems.Add(float64(inserted))
	m.inventoryDuplicates.Add(float64(duplicates))
}

func (m *TelegramMetrics) ObserveInventoryClaim(result string, claimed int) {
	m.inventoryClaims.WithLabelValues(result).Inc()
	m.inventoryClaimedItems.Add(float64(claimed))
}

func (m *TelegramMetrics) ObserveInventoryRelease(result string, released int) {
	m.inventoryReleases.WithLabelValues(result).Inc()
	m.inventoryReleasedItems.Add(float64(released))
}

func (m *TelegramMetrics) ObserveInventoryEncryption(operation, result string) {
	m.inventoryEncryption.WithLabelValues(operation, result).Inc()
}

func (m *TelegramMetrics) ObserveInventoryRecovery(result string) {
	m.inventoryRecovery.WithLabelValues(result).Inc()
}

func (m *TelegramMetrics) ObserveOrder(operation, result string, duration time.Duration) {
	switch operation {
	case "create":
		m.ordersCreated.WithLabelValues(result).Inc()
		m.orderCreationDuration.Observe(duration.Seconds())
	case "cancel":
		m.ordersCancelled.WithLabelValues(result).Inc()
	case "history":
		m.orderHistoryQueries.WithLabelValues(result).Inc()
	case "instruction":
		m.paymentInstructions.WithLabelValues(result).Inc()
	}
}

func (m *TelegramMetrics) ObserveBankMutation(operation, result string) {
	m.bankAccountMutations.WithLabelValues(operation, result).Inc()
}
