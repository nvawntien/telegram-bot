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
