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
