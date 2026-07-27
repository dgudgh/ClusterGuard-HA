package metrics

import (
	"math"
	"sort"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type Service struct{}

type DerivedMetrics struct {
	ObservedAt time.Time
	Values     map[string]float64
}

func NewService() *Service {
	return &Service{}
}

func (service *Service) Derive(samples []model.MetricSample) map[model.ResourceID]DerivedMetrics {
	return service.DeriveForEngine(model.EngineMySQL, samples)
}

func (service *Service) DeriveForEngine(engine model.Engine, samples []model.MetricSample) map[model.ResourceID]DerivedMetrics {
	byInstance := make(map[model.ResourceID][]model.MetricSample)
	for _, sample := range samples {
		byInstance[sample.InstanceID] = append(byInstance[sample.InstanceID], sample)
	}

	result := make(map[model.ResourceID]DerivedMetrics, len(byInstance))
	for instanceID, instanceSamples := range byInstance {
		sort.SliceStable(instanceSamples, func(i, j int) bool {
			return instanceSamples[i].ObservedAt.Before(instanceSamples[j].ObservedAt)
		})
		newest := instanceSamples[len(instanceSamples)-1]
		derived := make(map[string]float64)
		switch engine {
		case model.EngineMySQL:
			for _, name := range []string{"connections", "running_threads", "buffer_pool_hit_ratio"} {
				if value, ok := newest.Values[name]; ok && finite(value) {
					derived[name] = value
				}
			}
			if len(instanceSamples) >= 2 {
				previous := instanceSamples[len(instanceSamples)-2]
				seconds := newest.ObservedAt.Sub(previous.ObservedAt).Seconds()
				if seconds > 0 && finite(seconds) {
					deriveRate(derived, "qps", "questions_total", previous.Values, newest.Values, seconds)
					deriveRate(derived, "tps", "transactions_total", previous.Values, newest.Values, seconds)
					deriveRate(derived, "slow_queries_per_second", "slow_queries_total", previous.Values, newest.Values, seconds)
				}
			}
		case model.EnginePostgreSQL:
			for _, name := range []string{
				"connections", "active_connections", "transactions_total", "deadlocks_total", "conflicts_total",
				"temp_bytes_total", "blocks_read_total", "blocks_hit_total", "database_size_bytes", "replication_clients",
				"buffer_cache_hit_ratio", "wal_bytes", "checkpoints_total", "max_transaction_age_seconds",
			} {
				value, ok := newest.Values[name]
				if !ok || !finite(value) || value < 0 {
					continue
				}
				if name == "buffer_cache_hit_ratio" && value > 1 {
					continue
				}
				derived[name] = value
			}
		case model.EngineOracle:
			copyLatestMetrics(derived, newest.Values, []string{
				"broker_status_healthy", "transport_lag_seconds", "apply_lag_seconds", "role_primary",
			})
		case model.EngineSQLServer:
			copyLatestMetrics(derived, newest.Values, []string{
				"always_on_healthy", "connected", "synchronized", "synchronous_commit",
				"log_send_queue_bytes", "redo_queue_bytes", "role_primary", "role_secondary",
			})
		}
		result[instanceID] = DerivedMetrics{ObservedAt: newest.ObservedAt, Values: derived}
	}
	return result
}

func copyLatestMetrics(result map[string]float64, values map[string]float64, names []string) {
	for _, name := range names {
		value, ok := values[name]
		if !ok || !finite(value) || value < 0 {
			continue
		}
		result[name] = value
	}
}

func deriveRate(result map[string]float64, rateName string, counterName string, previous map[string]float64, newest map[string]float64, seconds float64) {
	before, beforeOK := previous[counterName]
	after, afterOK := newest[counterName]
	if !beforeOK || !afterOK || !finite(before) || !finite(after) || after < before {
		return
	}
	rate := (after - before) / seconds
	if finite(rate) {
		result[rateName] = rate
	}
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
