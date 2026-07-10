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
		result[instanceID] = DerivedMetrics{ObservedAt: newest.ObservedAt, Values: derived}
	}
	return result
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
