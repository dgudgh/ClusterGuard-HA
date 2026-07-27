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

func TestDerivePostgreSQLPreservesNativeCountersAndGauges(t *testing.T) {
	instanceID := model.NewResourceID()
	start := time.Date(2026, time.July, 20, 15, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{InstanceID: instanceID, ObservedAt: start, Values: map[string]float64{
			"connections": 8, "active_connections": 2, "transactions_total": 100,
			"deadlocks_total": 1, "conflicts_total": 3, "temp_bytes_total": 4096,
			"blocks_read_total": 25, "blocks_hit_total": 975, "database_size_bytes": 1048576,
			"replication_clients": 1, "buffer_cache_hit_ratio": 0.975, "wal_bytes": 8192,
		}},
		{InstanceID: instanceID, ObservedAt: start.Add(10 * time.Second), Values: map[string]float64{
			"connections": 12, "active_connections": 3, "transactions_total": 140,
			"deadlocks_total": 2, "conflicts_total": 4, "temp_bytes_total": 8192,
			"blocks_read_total": 30, "blocks_hit_total": 1170, "database_size_bytes": 2097152,
			"replication_clients": 2, "buffer_cache_hit_ratio": 0.975, "wal_bytes": 16384,
			"checkpoints_total": 9, "max_transaction_age_seconds": 12.5,
		}},
	}

	result := NewService().DeriveForEngine(model.EnginePostgreSQL, samples)[instanceID]
	if !result.ObservedAt.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("selected observed time = %s", result.ObservedAt)
	}
	for name, expected := range samples[1].Values {
		if actual, ok := result.Values[name]; !ok || actual != expected {
			t.Fatalf("%s = %v (present %t), want %v; all metrics: %+v", name, actual, ok, expected, result.Values)
		}
	}
	for _, mysqlOnly := range []string{"qps", "tps", "running_threads", "buffer_pool_hit_ratio"} {
		if _, found := result.Values[mysqlOnly]; found {
			t.Fatalf("PostgreSQL metrics leaked MySQL field %s: %+v", mysqlOnly, result.Values)
		}
	}
}

func TestDeriveOraclePreservesBrokerHAMetrics(t *testing.T) {
	instanceID := model.NewResourceID()
	start := time.Date(2026, time.July, 23, 9, 0, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{InstanceID: instanceID, ObservedAt: start, Values: map[string]float64{
			"broker_status_healthy": 0, "transport_lag_seconds": 10, "apply_lag_seconds": 12, "role_primary": 0,
		}},
		{InstanceID: instanceID, ObservedAt: start.Add(10 * time.Second), Values: map[string]float64{
			"broker_status_healthy": 1, "transport_lag_seconds": 2, "apply_lag_seconds": 3, "role_primary": 1,
			"connections": 999,
		}},
	}

	result := NewService().DeriveForEngine(model.EngineOracle, samples)[instanceID]
	if !result.ObservedAt.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("selected observed time = %s", result.ObservedAt)
	}
	for name, expected := range map[string]float64{"broker_status_healthy": 1, "transport_lag_seconds": 2, "apply_lag_seconds": 3, "role_primary": 1} {
		if actual, ok := result.Values[name]; !ok || actual != expected {
			t.Fatalf("%s = %v (present %t), want %v; all metrics: %+v", name, actual, ok, expected, result.Values)
		}
	}
	if _, found := result.Values["connections"]; found {
		t.Fatalf("Oracle metrics leaked non-Oracle field: %+v", result.Values)
	}
}

func TestDeriveSQLServerPreservesAlwaysOnHAMetrics(t *testing.T) {
	instanceID := model.NewResourceID()
	start := time.Date(2026, time.July, 23, 9, 5, 0, 0, time.UTC)
	samples := []model.MetricSample{
		{InstanceID: instanceID, ObservedAt: start, Values: map[string]float64{
			"always_on_healthy": 0, "connected": 0, "synchronized": 0, "synchronous_commit": 1,
			"log_send_queue_bytes": 64, "redo_queue_bytes": 128, "role_primary": 0, "role_secondary": 1,
		}},
		{InstanceID: instanceID, ObservedAt: start.Add(10 * time.Second), Values: map[string]float64{
			"always_on_healthy": 1, "connected": 1, "synchronized": 1, "synchronous_commit": 1,
			"log_send_queue_bytes": 0, "redo_queue_bytes": 0, "role_primary": 1, "role_secondary": 0,
			"qps": 999,
		}},
	}

	result := NewService().DeriveForEngine(model.EngineSQLServer, samples)[instanceID]
	if !result.ObservedAt.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("selected observed time = %s", result.ObservedAt)
	}
	for name, expected := range map[string]float64{
		"always_on_healthy": 1, "connected": 1, "synchronized": 1, "synchronous_commit": 1,
		"log_send_queue_bytes": 0, "redo_queue_bytes": 0, "role_primary": 1, "role_secondary": 0,
	} {
		if actual, ok := result.Values[name]; !ok || actual != expected {
			t.Fatalf("%s = %v (present %t), want %v; all metrics: %+v", name, actual, ok, expected, result.Values)
		}
	}
	if _, found := result.Values["qps"]; found {
		t.Fatalf("SQL Server metrics leaked MySQL field: %+v", result.Values)
	}
}
