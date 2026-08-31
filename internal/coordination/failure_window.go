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

type FailureWindowOption func(*FailureWindow)

// WithMaximumObservationGap configures how long a series can go without a
// fresh observation. It lets the safety window follow the discovery cadence
// without treating a normal scheduler interval as a recovered primary.
func WithMaximumObservationGap(maximumGap time.Duration) FailureWindowOption {
	return func(window *FailureWindow) {
		if maximumGap > 0 {
			window.maximumGap = maximumGap
		}
	}
}

func NewFailureWindow(requiredChecks int, requiredDuration time.Duration, options ...FailureWindowOption) *FailureWindow {
	if requiredChecks <= 0 {
		requiredChecks = 6
	}
	if requiredDuration <= 0 {
		requiredDuration = 30 * time.Second
	}
	maximumGap := 2 * requiredDuration / time.Duration(requiredChecks)
	window := &FailureWindow{requiredChecks: requiredChecks, requiredDuration: requiredDuration, maximumGap: maximumGap, series: make(map[model.ResourceID]failureSeries)}
	for _, option := range options {
		if option != nil {
			option(window)
		}
	}
	return window
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

// StableIncident reports whether the exact failure series previously qualified
// for automatic recovery. Unlike Stable, it does not expire merely because the
// guarded operation itself outlives the discovery freshness gap. A healthy
// observation or a newly started failure series still invalidates it.
func (window *FailureWindow) StableIncident(clusterID model.ResourceID, startedAt time.Time) bool {
	if startedAt.IsZero() {
		return false
	}
	startedAt = startedAt.UTC()
	window.mu.RLock()
	defer window.mu.RUnlock()
	series, found := window.series[clusterID]
	return found && series.startedAt.Equal(startedAt) &&
		len(series.checks) >= window.requiredChecks &&
		series.lastObserved.Sub(series.startedAt) >= window.requiredDuration
}

func (window *FailureWindow) stableSeries(series failureSeries, now time.Time) bool {
	if len(series.checks) < window.requiredChecks || now.IsZero() {
		return false
	}
	now = now.UTC()
	return now.Sub(series.startedAt) >= window.requiredDuration && !now.Before(series.lastObserved) && now.Sub(series.lastObserved) <= window.maximumGap
}
