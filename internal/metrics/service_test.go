package metrics

import (
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestDeriveUsesNewestSamplesForRatesAndGauges(t *testing.T) {
	instanceID := model.NewResourceID()
	start := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{InstanceID: instanceID, ObservedAt: start.Add(10 * time.Second), Values: map[string]float64{
			"questions_total": 150, "transactions_total": 60, "slow_queries_total": 15,
			"connections": 12, "running_threads": 3, "buffer_pool_hit_ratio": 0.98,
		}},
		{InstanceID: instanceID, ObservedAt: start, Values: map[string]float64{
			"questions_total": 100, "transactions_total": 40, "slow_queries_total": 5,
			"connections": 8, "running_threads": 2, "buffer_pool_hit_ratio": 0.97,
		}},
	}

	result := NewService().Derive(samples)[instanceID]
	if !result.ObservedAt.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("selected observed time = %s", result.ObservedAt)
	}
	derived := result.Values
	want := map[string]float64{
		"qps": 5, "tps": 2, "slow_queries_per_second": 1,
		"connections": 12, "running_threads": 3, "buffer_pool_hit_ratio": 0.98,
	}
	for name, expected := range want {
		if actual, ok := derived[name]; !ok || actual != expected {
			t.Fatalf("%s = %v (present %t), want %v; all metrics: %+v", name, actual, ok, expected, derived)
		}
	}
}

func TestDeriveOmitsRatesAfterCounterReset(t *testing.T) {
	instanceID := model.NewResourceID()
	start := time.Date(2026, time.July, 11, 10, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{InstanceID: instanceID, ObservedAt: start, Values: map[string]float64{
			"questions_total": 100, "transactions_total": 40, "slow_queries_total": 5,
		}},
		{InstanceID: instanceID, ObservedAt: start.Add(10 * time.Second), Values: map[string]float64{
			"questions_total": 10, "transactions_total": 4, "slow_queries_total": 1,
			"connections": 2,
		}},
	}

	result := NewService().Derive(samples)[instanceID]
	if !result.ObservedAt.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("selected observed time = %s", result.ObservedAt)
	}
	derived := result.Values
	for _, name := range []string{"qps", "tps", "slow_queries_per_second"} {
		if _, exists := derived[name]; exists {
			t.Fatalf("counter reset emitted %s: %+v", name, derived)
		}
	}
	if derived["connections"] != 2 {
		t.Fatalf("newest gauge was lost after reset: %+v", derived)
	}
}
