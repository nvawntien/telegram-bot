package observability

import "github.com/prometheus/client_golang/prometheus"

type WalletMetrics struct {
	topups      *prometheus.CounterVec
	credited    *prometheus.CounterVec
	payments    *prometheus.CounterVec
	ledger      *prometheus.CounterVec
	adjustments *prometheus.CounterVec
}

func NewWalletMetrics(registerer prometheus.Registerer) *WalletMetrics {
	metrics := &WalletMetrics{
		topups:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_topups_created_total", Help: "Wallet top-up intents."}, []string{"result"}),
		credited:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_topups_credited_total", Help: "Wallet top-up credits."}, []string{"result"}),
		payments:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_payments_total", Help: "Wallet order payments."}, []string{"result"}),
		ledger:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_ledger_entries_total", Help: "Wallet ledger entries."}, []string{"type", "result"}),
		adjustments: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "wallet_adjustments_total", Help: "Wallet adjustments."}, []string{"type", "result"}),
	}
	registerer.MustRegister(metrics.topups, metrics.credited, metrics.payments, metrics.ledger, metrics.adjustments)
	return metrics
}

func (m *WalletMetrics) ObserveWalletTopupCreated(result string) {
	m.topups.WithLabelValues(result).Inc()
}
func (m *WalletMetrics) ObserveWalletTopupCredited(result string) {
	m.credited.WithLabelValues(result).Inc()
}
func (m *WalletMetrics) ObserveWalletPayment(result string) { m.payments.WithLabelValues(result).Inc() }
func (m *WalletMetrics) ObserveWalletLedger(entryType, result string) {
	m.ledger.WithLabelValues(entryType, result).Inc()
}
func (m *WalletMetrics) ObserveWalletAdjustment(entryType, result string) {
	m.adjustments.WithLabelValues(entryType, result).Inc()
}
