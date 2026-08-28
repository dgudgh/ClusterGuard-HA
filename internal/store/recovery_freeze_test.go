package store

import (
	"context"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestSetRecoveryFreezePersistsAndIsIdempotent(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "freeze"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}

	if err := repository.SetRecoveryFreeze(context.Background(), cluster.ResourceID, true); err != nil {
		t.Fatalf("freeze recovery: %v", err)
	}
	frozen, err := repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || !frozen {
		t.Fatalf("frozen=%t err=%v", frozen, err)
	}
	// Idempotent second freeze must not fail.
	if err := repository.SetRecoveryFreeze(context.Background(), cluster.ResourceID, true); err != nil {
		t.Fatalf("freeze recovery twice: %v", err)
	}

	if err := repository.SetRecoveryFreeze(context.Background(), cluster.ResourceID, false); err != nil {
		t.Fatalf("unfreeze recovery: %v", err)
	}
	frozen, err = repository.RecoveryFrozen(context.Background(), cluster.ResourceID)
	if err != nil || frozen {
		t.Fatalf("frozen=%t err=%v", frozen, err)
	}
}

func TestSetRecoveryFreezeRejectsUnknownCluster(t *testing.T) {
	repository := NewMemory()
	err := repository.SetRecoveryFreeze(context.Background(), model.NewResourceID(), true)
	if err == nil {
		t.Fatalf("expected error for unknown cluster")
	}
	_, err = repository.RecoveryFrozen(context.Background(), model.NewResourceID())
	if err == nil {
		t.Fatalf("expected error for unknown cluster read")
	}
}

func TestRecoveryFreezeSurvivesSnapshotRoundTrip(t *testing.T) {
	repository := NewMemory()
	cluster, err := repository.UpsertCluster(model.DatabaseCluster{Engine: model.EngineMySQL, DisplayName: "freeze-roundtrip"})
	if err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	if err := repository.SetRecoveryFreeze(context.Background(), cluster.ResourceID, true); err != nil {
		t.Fatalf("freeze recovery: %v", err)
	}
	repository.mu.RLock()
	reloadedCluster, found := repository.snapshot.Clusters[cluster.ResourceID]
	repository.mu.RUnlock()
	if !found || !reloadedCluster.RecoveryFreeze {
		t.Fatalf("recovery freeze did not survive snapshot commit: %+v", reloadedCluster)
	}
}
