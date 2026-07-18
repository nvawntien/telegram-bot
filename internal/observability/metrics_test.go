package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHTTPMetricsCanUseIsolatedRegistries(t *testing.T) {
	firstRegistry := prometheus.NewRegistry()
	secondRegistry := prometheus.NewRegistry()

	first := NewHTTPMetrics(firstRegistry)
	second := NewHTTPMetrics(secondRegistry)
	first.Observe("GET", "/health/live", 200, time.Millisecond)
	second.Observe("GET", "/health/ready", 503, time.Millisecond)

	for name, registry := range map[string]*prometheus.Registry{
		"first":  firstRegistry,
		"second": secondRegistry,
	} {
		families, err := registry.Gather()
		if err != nil {
			t.Fatalf("%s registry Gather() error = %v", name, err)
		}
		if len(families) != 2 {
			t.Fatalf("%s registry metric families = %d, want 2", name, len(families))
		}
	}
}

func TestTelegramMetricsCanUseIsolatedRegistries(t *testing.T) {
	for _, registry := range []*prometheus.Registry{prometheus.NewRegistry(), prometheus.NewRegistry()} {
		metrics := NewTelegramMetrics(registry)
		metrics.ObserveWebhook("accepted")
		metrics.ObserveUpdate("message", "success", time.Millisecond)
		metrics.ObserveDuplicate()
		metrics.ObserveTelegramAPI("sendMessage", "success", time.Millisecond)
		metrics.ObserveCatalog("list_categories", "success")
		metrics.ObserveAdminMutation("category.create", "success")
		metrics.ObserveAdminSession("start", "success")
		families, err := registry.Gather()
		if err != nil || len(families) != 9 {
			t.Fatalf("Telegram registry families = %d, %v", len(families), err)
		}
	}
}
