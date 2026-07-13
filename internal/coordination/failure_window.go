package coordination

import (
	"sync"
	"time"

	"clusterguard.io/ha/pkg/model"
)

type failureSeries struct {
	startedAt    time.Time
	lastObserved time.Time
	checks       []time.Time
}

type FailureWindow struct {
	mu               sync.RWMutex
	requiredChecks   int
	requiredDuration time.Duration
	maximumGap       time.Duration
	series           map[model.ResourceID]failureSeries
}

func NewFailureWindow(requiredChecks int, requiredDuration time.Duration) *FailureWindow {
	if requiredChecks <= 0 {
		requiredChecks = 6
	}
	if requiredDuration <= 0 {
		requiredDuration = 30 * time.Second
	}
	maximumGap := 2 * requiredDuration / time.Duration(requiredChecks)
	return &FailureWindow{requiredChecks: requiredChecks, requiredDuration: requiredDuration, maximumGap: maximumGap, series: make(map[model.ResourceID]failureSeries)}
}

func (window *FailureWindow) Record(clusterID model.ResourceID, failed bool, observedAt time.Time) {
	if !model.ValidResourceID(clusterID) || observedAt.IsZero() {
		return
	}
	observedAt = observedAt.UTC()
	window.mu.Lock()
	defer window.mu.Unlock()
	if !failed {
		delete(window.series, clusterID)
		return
	}
	series, found := window.series[clusterID]
	if !found || observedAt.Before(series.startedAt) || (!series.lastObserved.IsZero() && observedAt.Sub(series.lastObserved) > window.maximumGap) {
		window.series[clusterID] = failureSeries{startedAt: observedAt, lastObserved: observedAt}
		return
	}
	if !observedAt.After(series.lastObserved) {
		return
	}
	series.lastObserved = observedAt
	series.checks = append(series.checks, observedAt)
	if len(series.checks) > window.requiredChecks {
		series.checks = series.checks[len(series.checks)-window.requiredChecks:]
	}
	window.series[clusterID] = series
}

func (window *FailureWindow) Stable(clusterID model.ResourceID, now time.Time) bool {
	window.mu.RLock()
	defer window.mu.RUnlock()
	series, found := window.series[clusterID]
	return found && window.stableSeries(series, now)
}

func (window *FailureWindow) Incident(clusterID model.ResourceID, now time.Time) (time.Time, bool) {
	window.mu.RLock()
	defer window.mu.RUnlock()
	series, found := window.series[clusterID]
	if !found || !window.stableSeries(series, now) {
		return time.Time{}, false
	}
	return series.startedAt, true
}

func (window *FailureWindow) stableSeries(series failureSeries, now time.Time) bool {
	if len(series.checks) < window.requiredChecks || now.IsZero() {
		return false
	}
	now = now.UTC()
	return now.Sub(series.startedAt) >= window.requiredDuration && !now.Before(series.lastObserved) && now.Sub(series.lastObserved) <= window.maximumGap
}
