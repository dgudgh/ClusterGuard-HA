package coordination

import (
	"testing"
	"time"

	"clusterguard.io/ha/pkg/model"
)

func TestFailureWindowRequiresSixConsecutiveSamplesAcrossThirtySeconds(t *testing.T) {
	window := NewFailureWindow(6, 30*time.Second)
	clusterID := model.NewResourceID()
	start := time.Date(2026, time.July, 13, 16, 0, 0, 0, time.UTC)
	window.Record(clusterID, true, start)
	for index := 1; index <= 6; index++ {
		window.Record(clusterID, true, start.Add(time.Duration(index)*5*time.Second))
		if index < 6 && window.Stable(clusterID, start.Add(time.Duration(index)*5*time.Second)) {
			t.Fatalf("failure became stable after %d samples", index+1)
		}
	}
	if !window.Stable(clusterID, start.Add(30*time.Second)) {
		t.Fatal("six samples across thirty seconds were not stable")
	}
}

func TestFailureWindowSuccessResetsConsecutiveFailureEvidence(t *testing.T) {
	window := NewFailureWindow(6, 30*time.Second)
	clusterID := model.NewResourceID()
	start := time.Now().UTC()
	for index := 0; index < 5; index++ {
		window.Record(clusterID, true, start.Add(time.Duration(index)*6*time.Second))
	}
	window.Record(clusterID, false, start.Add(30*time.Second))
	window.Record(clusterID, true, start.Add(36*time.Second))
	if window.Stable(clusterID, start.Add(36*time.Second)) {
		t.Fatal("a healthy sample did not reset stable-failure evidence")
	}
}

func TestFailureWindowIgnoresDuplicateObservationTimestamp(t *testing.T) {
	window := NewFailureWindow(1, time.Second)
	clusterID := model.NewResourceID()
	at := time.Now().UTC()
	window.Record(clusterID, true, at)
	window.Record(clusterID, true, at)
	if window.Stable(clusterID, at.Add(time.Second)) {
		t.Fatal("duplicate discovery observation counted twice")
	}
}

func TestFailureWindowExposesStableIncidentIdentityUntilRecovery(t *testing.T) {
	window := NewFailureWindow(6, 30*time.Second)
	clusterID := model.NewResourceID()
	start := time.Date(2026, time.July, 13, 22, 0, 0, 0, time.UTC)
	window.Record(clusterID, true, start)
	for index := 1; index <= 6; index++ {
		window.Record(clusterID, true, start.Add(time.Duration(index)*5*time.Second))
	}
	incident, stable := window.Incident(clusterID, start.Add(30*time.Second))
	if !stable || !incident.Equal(start) {
		t.Fatalf("stable incident=(%s,%t), want (%s,true)", incident, stable, start)
	}
	window.Record(clusterID, false, start.Add(35*time.Second))
	if incident, stable := window.Incident(clusterID, start.Add(35*time.Second)); stable || !incident.IsZero() {
		t.Fatalf("healthy observation retained incident=(%s,%t)", incident, stable)
	}
}

func TestFailureWindowAcceptsFourConsecutiveSamplesAcrossSixSeconds(t *testing.T) {
	window := NewFailureWindow(3, 6*time.Second)
	clusterID := model.NewResourceID()
	start := time.Date(2026, time.August, 13, 16, 0, 0, 0, time.UTC)
	window.Record(clusterID, true, start)
	for index := 1; index <= 3; index++ {
		observedAt := start.Add(time.Duration(index) * 2 * time.Second)
		window.Record(clusterID, true, observedAt)
		if index < 3 && window.Stable(clusterID, observedAt) {
			t.Fatalf("fast failure window became stable after %d observations", index+1)
		}
	}
	if !window.Stable(clusterID, start.Add(6*time.Second)) {
		t.Fatal("four consecutive observations across six seconds were not stable")
	}
}

func TestFailureWindowAcceptsConfiguredFiveSecondDiscoveryCadence(t *testing.T) {
	window := NewFailureWindow(3, 6*time.Second, WithMaximumObservationGap(10*time.Second))
	clusterID := model.NewResourceID()
	start := time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC)
	window.Record(clusterID, true, start)
	for index := 1; index <= 3; index++ {
		observedAt := start.Add(time.Duration(index) * 5 * time.Second)
		window.Record(clusterID, true, observedAt)
		if index < 3 && window.Stable(clusterID, observedAt) {
			t.Fatalf("failure became stable after only %d observations", index+1)
		}
	}
	if !window.Stable(clusterID, start.Add(15*time.Second)) {
		t.Fatal("four consecutive five-second observations did not become stable")
	}
}

func TestFailureWindowConfiguredGapStillRejectsStaleEvidence(t *testing.T) {
	window := NewFailureWindow(3, 6*time.Second, WithMaximumObservationGap(10*time.Second))
	clusterID := model.NewResourceID()
	start := time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC)
	window.Record(clusterID, true, start)
	window.Record(clusterID, true, start.Add(5*time.Second))
	window.Record(clusterID, true, start.Add(10*time.Second))
	window.Record(clusterID, true, start.Add(15*time.Second))
	if window.Stable(clusterID, start.Add(26*time.Second)) {
		t.Fatal("failure evidence remained stable after the configured observation gap expired")
	}
}
